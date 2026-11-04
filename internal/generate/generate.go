package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"maps"
	"path"
	"slices"
	"strconv"
)

// generate is the in-memory boundary of the compiler: parse and project the
// pinned schema, then prepare both Go files before the caller performs any I/O.
func generate(data []byte) (outputs map[string][]byte, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			outputs = nil
			err = fmt.Errorf("generate: %v", recovered)

			return
		}

		if err == nil {
			if err = validateDeclarations(outputs); err != nil {
				outputs = nil
			}
		}
	}()

	var version struct {
		OpenAPI string `json:"openapi"`
	}
	if err := json.Unmarshal(data, &version); err != nil {
		return nil, err
	}

	if version.OpenAPI != "" {
		return generateAppSec(data)
	}

	g := generator{inputs: make(map[string]bool), projections: make(map[string]bool)}
	if err := json.Unmarshal(data, &g.spec); err != nil {
		return nil, err
	}

	if g.spec.Swagger != "2.0" {
		return nil, fmt.Errorf("unsupported Swagger version %q", g.spec.Swagger)
	}

	if err := json.Unmarshal(platformTypes, &g.pointerOptionals); err != nil {
		return nil, err
	}

	for _, methods := range g.spec.Paths {
		for _, op := range methods {
			for _, p := range op.Parameters {
				if p.In == nameBody {
					g.markInput(p.Schema)
				}
			}
		}
	}

	g.planProjections()

	outputs = map[string][]byte{"models_gen.go": g.models(), "api_gen.go": g.methods()}
	for _, name := range slices.Sorted(maps.Keys(outputs)) {
		formatted, err := formatGeneratedFile(name, outputs[name])
		if err != nil {
			return nil, err
		}

		outputs[name] = formatted
	}

	return outputs, nil
}

// formatGeneratedFile removes unused candidate imports by inspecting Go syntax,
// so import names mentioned only in schema descriptions do not count as uses.
func formatGeneratedFile(name string, source []byte) ([]byte, error) {
	fset := token.NewFileSet()
	// Import pruning only needs syntax; declaration validation runs separately.
	file, err := parser.ParseFile(fset, name, source, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("format %s: %w", name, err)
	}

	used := make(map[string]bool)

	ast.Inspect(file, func(node ast.Node) bool {
		if selector, ok := node.(*ast.SelectorExpr); ok {
			if qualifier, ok := selector.X.(*ast.Ident); ok {
				used[qualifier.Name] = true
			}
		}

		return true
	})

	declarations := file.Decls[:0]
	for _, declaration := range file.Decls {
		group, ok := declaration.(*ast.GenDecl)
		if ok && group.Tok == token.IMPORT {
			imports := group.Specs[:0]
			for _, spec := range group.Specs {
				keep, err := generatedImportUsed(spec, used)
				if err != nil {
					return nil, fmt.Errorf("format %s: %w", name, err)
				}

				if keep {
					imports = append(imports, spec)
				}
			}

			group.Specs = imports
			if len(imports) == 0 {
				continue
			}
		}

		declarations = append(declarations, declaration)
	}

	file.Decls = declarations

	var formatted bytes.Buffer
	if err := format.Node(&formatted, fset, file); err != nil {
		return nil, fmt.Errorf("format %s: %w", name, err)
	}

	return formatted.Bytes(), nil
}

func generatedImportUsed(spec ast.Spec, used map[string]bool) (bool, error) {
	importSpec, ok := spec.(*ast.ImportSpec)
	if !ok {
		return false, errors.New("unexpected import declaration")
	}

	importPath, err := strconv.Unquote(importSpec.Path.Value)
	if err != nil {
		return false, err
	}

	qualifier := path.Base(importPath)
	if importSpec.Name != nil {
		qualifier = importSpec.Name.Name
	}

	return qualifier == "_" || qualifier == "." || used[qualifier], nil
}
