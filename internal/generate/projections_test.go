package main

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

func TestNewBranchInputKeepsEveryExistingDeclaration(t *testing.T) {
	data, err := os.ReadFile("../../api/sourcecraft.swagger.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec specification
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	before := generateSpec(t, spec)
	spec.Paths["/future/branches"] = map[string]operation{"post": inputOperation("CreateBranchFuture", "Branch")}
	after := generateSpec(t, spec)
	for _, file := range []string{"api_gen.go", "models_gen.go"} {
		current := declarations(t, after[file])
		for name, previous := range declarations(t, before[file]) {
			if current[name] != previous {
				t.Errorf("existing %s changed in %s", name, file)
			}
		}
	}
	if !strings.Contains(string(after["api_gen.go"]), "CreateBranchFuture(ctx context.Context, body *BranchInput)") {
		t.Fatal("new method does not accept BranchInput")
	}
	testGeneratedPackage(t, after, `package sourcecraft
import ("encoding/json"; "testing")
func TestInputWire(t *testing.T) {
 var branch Branch
 branch.Name = "main"
 body := BranchInput{Name:new("")}
 data,err := json.Marshal(body)
 if err != nil || string(data) != "{\"name\":\"\"}" { t.Fatalf("zero-valued input: %s %v",data,err) }
}
`)
	// Removing input operations also cannot change the canonical model layout.
	spec.Paths = map[string]map[string]operation{}
	withoutInputs := generateSpec(t, spec)
	if !bytes.Equal(before["models_gen.go"], withoutInputs["models_gen.go"]) {
		t.Fatal("canonical models changed after removing input usages")
	}
}

func TestNestedInputProjectionsAndZeroValues(t *testing.T) {
	outputs := generateSpec(t, projectionFixture())
	testGeneratedPackage(t, outputs, `package sourcecraft
import ("encoding/json"; "testing")
func TestInputWire(t *testing.T) {
 var response Node
 response.Name = "value"
 response.Enabled = false
 var holder Holder
 holder.Node = &response
 input := HolderInput{Node:&NodeInput{
  Name:new(""),Enabled:new(false),Count:new(int64(0)),Revision:new(^uint64(0)),
  Child:&NodeInput{Name:new("nested")},Children:[]NodeInput{},ByName:map[string]NodeInput{},
 }}
 data,err := json.Marshal(input)
 want := "{\"node\":{\"by_name\":{},\"child\":{\"name\":\"nested\"},\"children\":[],\"count\":0,\"enabled\":false,\"name\":\"\",\"revision\":\"18446744073709551615\"}}"
 if err != nil || string(data) != want { t.Fatalf("input: %s %v; want %s",data,err,want) }
}
`)
}

func TestInputProjectionNameCollisionIsRejected(t *testing.T) {
	spec := projectionFixture()
	spec.Definitions["NodeInput"] = &schema{Type: "object"}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	outputs, err := generate(data)
	if err == nil || outputs != nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("collision was not rejected: %v", err)
	}
}

func generateSpec(t *testing.T, s specification) map[string][]byte {
	t.Helper()
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	outputs, err := generate(data)
	if err != nil {
		t.Fatal(err)
	}
	return outputs
}

func inputOperation(name, model string) operation {
	op := operation{ID: name, Parameters: []parameter{{Name: "body", In: "body", Required: true, Schema: &schema{Ref: "#/definitions/" + model}}}}
	op.Responses = map[string]struct {
		Schema *schema `json:"schema"`
	}{"200": {Schema: &schema{Ref: "#/definitions/" + model}}}
	return op
}

// Compare declarations independently of the generator's representation policy.
func declarations(t *testing.T, data []byte) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "generated.go", data, 0)
	if err != nil {
		t.Fatal(err)
	}
	text := func(node ast.Node) string {
		var b bytes.Buffer
		if err := format.Node(&b, fset, node); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	result := map[string]string{}
	for _, decl := range file.Decls {
		switch decl := decl.(type) {
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					result["type "+spec.Name.Name] = text(spec.Type)
				case *ast.ValueSpec:
					for _, name := range spec.Names {
						result["value "+name.Name] = text(spec)
					}
				}
			}
		case *ast.FuncDecl:
			key := "func " + decl.Name.Name
			if decl.Recv != nil {
				key += " receiver " + text(decl.Recv.List[0].Type)
			}
			result[key] = text(decl.Type)
		}
	}
	return result
}

func projectionFixture() specification {
	node := &schema{Type: "object", Properties: map[string]*schema{
		"name": {Type: "string"}, "enabled": {Type: "boolean"}, "count": {Type: "integer", Format: "int64"},
		"revision": {Type: "string", Format: "uint64"},
		"child":    {Ref: "#/definitions/Node"},
		"children": {Type: "array", Items: &schema{Ref: "#/definitions/Node"}},
		"by_name":  {Type: "object", AdditionalProperties: &schema{Ref: "#/definitions/Node"}},
	}}
	holder := &schema{Type: "object", Required: []string{"node"}, Properties: map[string]*schema{"node": {Ref: "#/definitions/Node"}}}
	return specification{Swagger: "2.0", Definitions: map[string]*schema{"Node": node, "Holder": holder}, Paths: map[string]map[string]operation{"/nodes": {"post": inputOperation("CreateNode", "Holder")}}}
}
