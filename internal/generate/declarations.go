package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"slices"
	"strings"
)

// The generated declarations share a namespace with the handwritten SDK.
// Keeping that boundary explicit also catches collisions across output files.
const sdkRuntimeNames = `
Client
Option
Response
APIError
EmptyResponse
Upload
OperationFailedError
AppSecClient
DefaultBaseURL
DefaultAppSecBaseURL
NewClient
NewAppSecClient
WithBaseURL
WithHTTPClient
WithUserAgent
WithMaxResponseBytes
Ptr
Paginate
method Client.do
method Client.WaitPullRequestMerge
method Client.WaitOrganizationInvites
method Client.WaitSecret
method AppSecClient.transport
method AppSecClient.uploadReport
method APIError.Error
method OperationFailedError.Error
defaultMaxResponseBytes
queryEncoder
pathParam
formField
encodeJSON
expandPath
doJSON
doEmpty
doUpload
multipartBody
waitOperation
readResponseBody
`

type declarationRegistry struct {
	owners   map[string]string
	platform bool
}

func validateDeclarations(outputs map[string][]byte) error {
	_, platform := outputs["api_gen.go"]
	registry := declarationRegistry{owners: runtimeDeclarations(), platform: platform}

	for _, filename := range slices.Sorted(maps.Keys(outputs)) {
		file, err := parser.ParseFile(token.NewFileSet(), filename, outputs[filename], parser.DeclarationErrors)
		if err != nil {
			return fmt.Errorf("declarations in %s: %w", filename, err)
		}

		if err := registry.file(file, filename); err != nil {
			return err
		}
	}

	return nil
}

func (r *declarationRegistry) declare(name, filename string) error {
	if name == "_" {
		return nil
	}
	// The compilers run independently, so reserve AppSec's namespace explicitly.
	if r.platform && strings.HasPrefix(name, "AppSec") {
		return fmt.Errorf("declaration %s in %s uses the reserved AppSec namespace", name, filename)
	}

	if owner, exists := r.owners[name]; exists {
		return fmt.Errorf("declaration %s in %s conflicts with %s", name, filename, owner)
	}

	r.owners[name] = filename

	return nil
}

func (r *declarationRegistry) file(file *ast.File, filename string) error {
	for _, decl := range file.Decls {
		switch decl := decl.(type) {
		case *ast.GenDecl:
			if err := r.group(decl, filename); err != nil {
				return err
			}
		case *ast.FuncDecl:
			name, err := generatedFunctionName(decl, filename)
			if err != nil {
				return err
			}

			if err := r.declare(name, filename); err != nil {
				return err
			}
		}
	}

	return nil
}

func (r *declarationRegistry) group(decl *ast.GenDecl, filename string) error {
	for _, spec := range decl.Specs {
		switch spec := spec.(type) {
		case *ast.TypeSpec:
			if err := r.declare(spec.Name.Name, filename); err != nil {
				return err
			}
		case *ast.ValueSpec:
			for _, name := range spec.Names {
				if err := r.declare(name.Name, filename); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func generatedFunctionName(decl *ast.FuncDecl, filename string) (string, error) {
	if decl.Recv == nil {
		return decl.Name.Name, nil
	}

	receiver := decl.Recv.List[0].Type
	if pointer, ok := receiver.(*ast.StarExpr); ok {
		receiver = pointer.X
	}

	ident, ok := receiver.(*ast.Ident)
	if !ok {
		return "", fmt.Errorf("unsupported generated receiver in %s", filename)
	}

	return "method " + ident.Name + "." + decl.Name.Name, nil
}

func runtimeDeclarations() map[string]string {
	owners := make(map[string]string, 38)
	for name := range strings.SplitSeq(strings.TrimSpace(sdkRuntimeNames), "\n") {
		owners[name] = "the SDK runtime"
	}

	return owners
}
