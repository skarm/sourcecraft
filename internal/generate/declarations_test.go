package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReservedDeclarationsMatchRuntime(t *testing.T) {
	entries, err := os.ReadDir("../..")
	if err != nil {
		t.Fatal(err)
	}
	reserved := runtimeDeclarations()
	check := func(name string) {
		t.Helper()
		if _, ok := reserved[name]; !ok {
			t.Errorf("SDK declaration is not reserved: %s", name)
		}
		delete(reserved, name)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_gen.go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join("../..", name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			switch decl := decl.(type) {
			case *ast.FuncDecl:
				if decl.Recv != nil {
					receiver := decl.Recv.List[0].Type
					if pointer, ok := receiver.(*ast.StarExpr); ok {
						receiver = pointer.X
					}
					check("method " + receiver.(*ast.Ident).Name + "." + decl.Name.Name)
				} else {
					check(decl.Name.Name)
				}
			case *ast.GenDecl:
				for _, spec := range decl.Specs {
					var names []*ast.Ident
					switch spec := spec.(type) {
					case *ast.TypeSpec:
						names = []*ast.Ident{spec.Name}
					case *ast.ValueSpec:
						names = spec.Names
					}
					for _, name := range names {
						check(name.Name)
					}
				}
			}
		}
	}
	for name := range reserved {
		t.Errorf("reserved declaration no longer exists: %s", name)
	}
}
