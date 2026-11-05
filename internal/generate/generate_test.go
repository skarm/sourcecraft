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

func TestPlatformRejectsQuotedIntegersOutsideModelFields(t *testing.T) {
	for _, format := range []string{"int64", "uint64"} {
		for _, location := range []string{"response", "body", "array", "dictionary", "nested_array", "nested_dictionary"} {
			t.Run(format+"/"+location, func(t *testing.T) {
				spec := smallSpec("post_query")
				quoted := &schema{Type: "string", Format: format}
				op := spec.Paths["/things/{thing_id}"]["post"]
				switch location {
				case "response":
					r := op.Responses["200"]
					r.Schema = quoted
					op.Responses["200"] = r
				case "body":
					op.Parameters[1].Schema = quoted
				case "array":
					spec.Definitions["Thing"].Properties["values"] = &schema{Type: "array", Items: quoted}
				case "dictionary":
					spec.Definitions["Thing"].Properties["values"] = &schema{Type: "object", AdditionalProperties: quoted}
				case "nested_array":
					spec.Definitions["Thing"].Properties["values"] = &schema{Type: "array", Items: &schema{Type: "array", Items: quoted}}
				case "nested_dictionary":
					spec.Definitions["Thing"].Properties["values"] = &schema{Type: "array", Items: &schema{Type: "object", AdditionalProperties: quoted}}
				}
				spec.Paths["/things/{thing_id}"]["post"] = op
				data, err := json.Marshal(spec)
				if err != nil {
					t.Fatal(err)
				}
				if outputs, err := generate(data); err == nil || outputs != nil || !strings.Contains(err.Error(), "string-encoded integers") {
					t.Fatalf("unsupported wire type was accepted: outputs=%d error=%v", len(outputs), err)
				}
			})
		}
	}
}

func TestPlatformQuotedIntegerFieldsRemainSupported(t *testing.T) {
	spec := smallSpec("get")
	spec.Definitions["Thing"].Required = []string{"id", "signed"}
	spec.Definitions["Thing"].Properties["signed"] = &schema{Type: "string", Format: "int64"}
	spec.Definitions["Thing"].Properties["unsigned"] = &schema{Type: "string", Format: "uint64"}
	spec.Definitions["Holder"] = &schema{Type: "object", Properties: map[string]*schema{
		"thing": {Ref: "#/definitions/Thing"},
		"list":  {Type: "array", Items: &schema{Ref: "#/definitions/Thing"}},
		"by_id": {Type: "object", AdditionalProperties: &schema{Ref: "#/definitions/Thing"}},
	}}
	testGeneratedPackage(t, generateSpec(t, spec), `package sourcecraft
import ("encoding/json";"testing")
func TestWire(t *testing.T) {
 const wire = "{\"id\":\"x\",\"signed\":\"-9223372036854775808\",\"unsigned\":\"18446744073709551615\"}"
 var model Thing
 if err := json.Unmarshal([]byte(wire), &model); err != nil { t.Fatal(err) }
 if model.Signed != -9223372036854775808 || model.Unsigned != ^uint64(0) { t.Fatalf("lost precision: %#v",model) }
 data,err := json.Marshal(model)
 if err != nil || string(data) != wire { t.Fatalf("round trip: %s %v",data,err) }
}
`)
}

func TestPlatformRejectsSchemaComposition(t *testing.T) {
	for _, composition := range []string{"allOf", "anyOf", "oneOf"} {
		for _, location := range []string{"model", "field", "reference", "array", "dictionary", "response", "body", "multipart", "multipart_field"} {
			t.Run(composition+"/"+location, func(t *testing.T) {
				spec := smallSpec("post_query")
				composed := &schema{Type: "object"}
				branches := []json.RawMessage{json.RawMessage(`{"$ref":"#/definitions/Thing"}`)}
				switch composition {
				case "allOf":
					composed.AllOf = branches
				case "anyOf":
					composed.AnyOf = branches
				case "oneOf":
					composed.OneOf = branches
				}
				op := spec.Paths["/things/{thing_id}"]["post"]
				switch location {
				case "model":
					spec.Definitions["Extended"] = composed
				case "field":
					spec.Definitions["CreateThingBody"].Properties["extra"] = composed
				case "reference":
					composed.Ref = "#/definitions/Thing"
					spec.Definitions["CreateThingBody"].Properties["extra"] = composed
				case "array":
					spec.Definitions["CreateThingBody"].Properties["extra"] = &schema{Type: "array", Items: composed}
				case "dictionary":
					spec.Definitions["CreateThingBody"].Properties["extra"] = &schema{Type: "object", AdditionalProperties: composed}
				case "response":
					r := op.Responses["200"]
					r.Schema = composed
					op.Responses["200"] = r
				case "body":
					op.Parameters[1].Schema = composed
				case "multipart", "multipart_field":
					spec = smallSpec("multipart")
					op = spec.Paths["/things/{thing_id}"]["post"]
					if location == "multipart" {
						composed.Properties = op.RequestBody.Content["multipart/form-data"].Schema.Properties
						content := op.RequestBody.Content["multipart/form-data"]
						content.Schema = composed
						op.RequestBody.Content["multipart/form-data"] = content
					} else {
						composed.Type = "string"
						op.RequestBody.Content["multipart/form-data"].Schema.Properties["file"] = composed
					}
				}
				spec.Paths["/things/{thing_id}"]["post"] = op
				data, err := json.Marshal(spec)
				if err != nil {
					t.Fatal(err)
				}
				if outputs, err := generate(data); err == nil || outputs != nil || !strings.Contains(err.Error(), "composition") {
					t.Fatalf("composition was silently flattened: outputs=%d error=%v", len(outputs), err)
				}
			})
		}
	}
}

func TestPlatformRequiredStringIntegerQueriesCompile(t *testing.T) {
	for _, format := range []string{"int64", "uint64"} {
		t.Run(format, func(t *testing.T) {
			spec := smallSpec("get")
			op := spec.Paths["/things/{thing_id}"]["get"]
			op.Parameters = append(op.Parameters, parameter{Name: "count", In: "query", Required: true, Type: "string", Format: format})
			spec.Paths["/things/{thing_id}"]["get"] = op
			limit, want := "int64(-9223372036854775808)", "-9223372036854775808"
			if format == "uint64" {
				limit, want = "^uint64(0)", "18446744073709551615"
			}
			testGeneratedPackage(t, generateSpec(t, spec), `package sourcecraft
import "testing"
func TestRequiredZero(t *testing.T) {
 o:=&GetThingOptions{}
 q,err:=o.queryValues()
 if err!=nil || !q.Has("count") || q.Get("count")!="0" { t.Fatalf("required zero: %v %v",q,err) }
 o.Count=42
 q,err=o.queryValues()
 if err!=nil || q.Get("count")!="42" { t.Fatalf("required count: %v %v",q,err) }
 o.Count=`+limit+`
 q,err=o.queryValues()
 if err!=nil || q.Get("count")!="`+want+`" { t.Fatalf("required limit: %v %v",q,err) }
 if _,err:=(*GetThingOptions)(nil).queryValues();err==nil { t.Fatal("nil required options accepted") }
}
`)
		})
	}
}

func TestPlatformRejectsStringIntegerEnumDefinitions(t *testing.T) {
	for _, format := range []string{"int64", "uint64"} {
		for _, location := range []string{"definition", "field", "array", "dictionary", "response", "body"} {
			t.Run(format+"/"+location, func(t *testing.T) {
				spec := smallSpec("post_query")
				spec.Definitions["Counter"] = &schema{Type: "string", Format: format, Enum: []string{"1", "2"}}
				ref := &schema{Ref: "#/definitions/Counter"}
				op := spec.Paths["/things/{thing_id}"]["post"]
				switch location {
				case "field":
					spec.Definitions["Thing"].Properties["count"] = ref
				case "array":
					spec.Definitions["Thing"].Properties["counts"] = &schema{Type: "array", Items: ref}
				case "dictionary":
					spec.Definitions["Thing"].Properties["counts"] = &schema{Type: "object", AdditionalProperties: ref}
				case "response":
					response := op.Responses["200"]
					response.Schema = ref
					op.Responses["200"] = response
				case "body":
					op.Parameters[1].Schema = ref
				}
				spec.Paths["/things/{thing_id}"]["post"] = op
				data, err := json.Marshal(spec)
				if err != nil {
					t.Fatal(err)
				}
				if outputs, err := generate(data); err == nil || outputs != nil || !strings.Contains(err.Error(), "string-encoded integers") {
					t.Fatalf("string-encoded integer enum generated invalid JSON tags: outputs=%d error=%v", len(outputs), err)
				}
			})
		}
	}
}
