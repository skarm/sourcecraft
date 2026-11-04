package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Compiling small, independent fixtures catches missing/unused imports and
// broken method signatures that the full production schema can accidentally hide.
func TestGenerateSmallPackagesCompile(t *testing.T) {
	for _, kind := range []string{"get", "json_and_time", "post_query", "delete", "multipart"} {
		t.Run(kind, func(t *testing.T) {
			document := smallSpec(kind)
			data, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			outputs, err := generate(data)
			if err != nil {
				t.Fatal(err)
			}
			testGeneratedPackage(t, outputs, "package sourcecraft\n")
		})
	}
}

func TestGenerateRejectsInvalidSchema(t *testing.T) {
	for _, data := range []string{
		`{`,
		`{"swagger":"3.0","paths":{},"definitions":{}}`,
		`{"swagger":"2.0","paths":{},"definitions":{"Thing":{"type":"object","properties":{"missing":{"$ref":"#/definitions/Missing"}}}}}`,
	} {
		outputs, err := generate([]byte(data))
		if err == nil || outputs != nil {
			t.Fatalf("expected no outputs and an error, got outputs=%v error=%v", outputs, err)
		}
	}
}

func TestRunRejectsInvalidTargetBeforeWriting(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "swagger.json")
	data, err := json.Marshal(smallSpec("get"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, data, 0o644); err != nil {
		t.Fatal(err)
	}
	sentinel := []byte("original api sentinel\n")
	output := filepath.Join(directory, "api_gen.go")
	if err := os.WriteFile(output, sentinel, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(directory, "models_gen.go"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := run(input, directory, false); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("expected invalid output error, got %v", err)
	}
	actual, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, sentinel) {
		t.Fatal("the first output was overwritten before the second target was rejected")
	}
}

func TestRunCheckDoesNotModifyFiles(t *testing.T) {
	directory := t.TempDir()
	data, err := json.Marshal(smallSpec("get"))
	if err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(directory, "swagger.json")
	if err := os.WriteFile(input, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(input, directory, false); err != nil {
		t.Fatal(err)
	}
	if err := run(input, directory, true); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(directory, "api_gen.go")
	sentinel := []byte("outdated api sentinel\n")
	if err := os.WriteFile(output, sentinel, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(input, directory, true); err == nil || !strings.Contains(err.Error(), "out of date") {
		t.Fatalf("expected stale output error, got %v", err)
	}
	actual, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, sentinel) {
		t.Fatal("check mode modified an output")
	}
}

func smallSpec(kind string) specification {
	thing := &schema{Type: "object", Properties: map[string]*schema{"id": {Type: "string", Description: "A string; time.Time and json.RawMessage here are documentation, not imports."}}}
	response := struct {
		Schema *schema `json:"schema"`
	}{Schema: &schema{Ref: "#/definitions/Thing"}}
	op := operation{
		ID:         "GetThing",
		Parameters: []parameter{{Name: "thing_id", In: "path", Required: true, Type: "string"}},
		Responses: map[string]struct {
			Schema *schema `json:"schema"`
		}{"200": response},
	}
	spec := specification{Swagger: "2.0", Paths: map[string]map[string]operation{}, Definitions: map[string]*schema{"Thing": thing}}
	method := "get"
	switch kind {
	case "json_and_time":
		thing.Properties["created_at"] = &schema{Type: "string", Format: "date-time"}
		thing.Properties["details"] = &schema{}
	case "post_query":
		method = "post"
		op.ID = "CreateThing"
		op.Parameters = append(op.Parameters, parameter{Name: "body", In: "body", Required: true, Schema: &schema{Ref: "#/definitions/CreateThingBody"}}, parameter{Name: "silent", In: "query", Type: "boolean"}, parameter{Name: "page_size", In: "query", Type: "string", Format: "uint64"})
		spec.Definitions["CreateThingBody"] = &schema{Type: "object", Properties: map[string]*schema{"title": {Type: "string"}}, Required: []string{"title"}}
	case "delete":
		method = "delete"
		op.ID = "DeleteThing"
		op.Responses = map[string]struct {
			Schema *schema `json:"schema"`
		}{"204": {}}
	case "multipart":
		method = "post"
		op.ID = "UploadThing"
		// Use the same hybrid Swagger extension as SourceCraft's real schema.
		encoded := `{"content":{"multipart/form-data":{"schema":{"type":"object","properties":{"name":{"type":"string"},"file":{"type":"string","format":"binary"}},"required":["name","file"]}}}}`
		if err := json.Unmarshal([]byte(encoded), &op.RequestBody); err != nil {
			panic(err)
		}
	}
	spec.Paths["/things/{thing_id}"] = map[string]operation{method: op}
	return spec
}
