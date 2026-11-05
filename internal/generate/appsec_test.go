package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestGenerateAppSecFixturesCompile(t *testing.T) {
	for _, kind := range []string{"get", "json", "delete"} {
		t.Run(kind, func(t *testing.T) {
			outputs, err := generate([]byte(appsecFixture(kind)))
			if err != nil {
				t.Fatal(err)
			}
			testGeneratedPackage(t, outputs, "package sourcecraft\n")
		})
	}
}

func TestGenerateAppSecRejectsUnsupportedSchema(t *testing.T) {
	valid := appsecFixture("get")
	cases := []string{
		strings.Replace(valid, `"3.1.0"`, `"3.0.0"`, 1),
		`{"openapi":"3.1.0","paths":{},"components":{"schemas":{}}}`,
		strings.Replace(valid, `/v1/scans/{scanUuid}`, `/v1/new-endpoint`, 1),
		strings.Replace(valid, `"$ref":"#/components/schemas/Thing"`, `"$ref":"#/components/schemas/Missing"`, 1),
		strings.Replace(valid, `"type":["integer","null"]`, `"type":["integer","string"]`, 1),
		strings.Replace(valid, `"anyOf":[{"$ref":"#/components/schemas/State"},{"type":"null"}]`, `"anyOf":[{"type":"string"},{"type":"integer"}]`, 1),
		strings.Replace(valid, `"anyOf":[{"$ref":"#/components/schemas/State"},{"type":"null"}]`, `"oneOf":[{"type":"string"},{"type":"integer"}]`, 1),
		strings.Replace(valid, `"application/json"`, `"text/plain"`, 1),
		strings.Replace(valid, `"type":"string","format":"uuid"`, `"type":"string","format":"unknown"`, 1),
	}
	for i, data := range cases {
		t.Run(string(rune('A'+i)), func(t *testing.T) {
			if !json.Valid([]byte(data)) {
				t.Fatal("test fixture is invalid JSON")
			}
			outputs, err := generate([]byte(data))
			if err == nil || outputs != nil {
				t.Fatalf("expected no outputs and an error; got outputs=%v error=%v", outputs, err)
			}
		})
	}
}

func TestAppSecSharedModelsDoNotDependOnRequestUsage(t *testing.T) {
	get, err := generate([]byte(appsecFixture("get")))
	if err != nil {
		t.Fatal(err)
	}
	post, err := generate([]byte(appsecFixture("json")))
	if err != nil {
		t.Fatal(err)
	}
	if string(get["appsec_models_gen.go"]) != string(post["appsec_models_gen.go"]) {
		t.Fatal("adding input usage changed existing model types")
	}
}

func TestAppSecRejectsLossyResponseAndModelChanges(t *testing.T) {
	for _, kind := range []string{"typed_and_empty_success", "empty_and_typed_success", "nullable_response", "dictionary_model", "union_model"} {
		t.Run(kind, func(t *testing.T) {
			var document map[string]any
			if err := json.Unmarshal([]byte(appsecFixture("get")), &document); err != nil {
				t.Fatal(err)
			}
			schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
			operation := document["paths"].(map[string]any)["/v1/scans/{scanUuid}"].(map[string]any)["get"].(map[string]any)
			switch kind {
			case "typed_and_empty_success":
				operation["responses"].(map[string]any)["202"] = map[string]any{"description": "Accepted"}
			case "empty_and_typed_success":
				responses := operation["responses"].(map[string]any)
				responses["202"] = responses["200"]
				responses["200"] = map[string]any{"description": "OK"}
			case "nullable_response":
				operation["responses"].(map[string]any)["200"] = map[string]any{"content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"anyOf": []any{map[string]any{"$ref": "#/components/schemas/Thing"}, map[string]any{"type": "null"}}}}}}
			case "dictionary_model":
				schemas["Extra"] = map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}
			case "union_model":
				schemas["Extra"] = map[string]any{"anyOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "integer"}}}
			}
			data, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			outputs, err := generate(data)
			if err == nil || outputs != nil {
				t.Fatalf("unsupported change was silently accepted: error=%v files=%d", err, len(outputs))
			}
		})
	}
}

func appsecFixture(kind string) string {
	const models = `"Thing":{"type":"object","required":["name","nullable"],"properties":{"name":{"type":"string"},"nullable":{"type":["integer","null"],"format":"int64"},"state":{"anyOf":[{"$ref":"#/components/schemas/State"},{"type":"null"}]},"parent":{"$ref":"#/components/schemas/Thing"},"children":{"type":"array","items":{"$ref":"#/components/schemas/Thing"}},"metadata":{"type":"object","additionalProperties":{}},"uuid":{"$ref":"#/components/schemas/UUID"}}},"State":{"type":"string","enum":["OPEN","CLOSED"]},"UUID":{"type":"string","format":"uuid"}`
	method, path := "get", "/v1/scans/{scanUuid}"
	op := `"parameters":[{"name":"scanUuid","in":"path","required":true,"schema":{"type":"string"}},{"name":"gitRepo","in":"query","required":true,"schema":{"type":"string"}}],"responses":{"200":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/Thing"}}}}}`
	switch kind {
	case "json":
		method, path = "post", "/v1/activities/comment"
		op = `"parameters":[{"name":"gitRepo","in":"query","required":true,"schema":{"type":"string"}},{"name":"flags","in":"query","schema":{"type":"boolean"}},{"name":"status","in":"query","required":true,"schema":{"allOf":[{"$ref":"#/components/schemas/State"},{"default":"OPEN"}]}}],"requestBody":{"required":true,"content":{"application/json":{"schema":{"$ref":"#/components/schemas/Thing"}}}},"responses":{"200":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/Thing"}}}}}`
	case "delete":
		method, path = "delete", "/v1/activities/comment/{activityUuid}"
		op = `"parameters":[{"name":"activityUuid","in":"path","required":true,"schema":{"type":"string"}},{"name":"gitRepo","in":"query","required":true,"schema":{"type":"string"}}],"responses":{"204":{}}`
	}
	return `{"openapi":"3.1.0","components":{"schemas":{` + models + `}},"paths":{"` + path + `":{"` + method + `":{` + op + `}}}}`
}

func TestAppSecRejectsNullablePathParameters(t *testing.T) {
	for _, encoded := range []string{`{"type":["string","null"]}`, `{"type":["integer","null"]}`, `{"anyOf":[{"type":"string"},{"type":"null"}]}`} {
		var spec openAPISpecification
		if err := json.Unmarshal([]byte(appsecFixture("get")), &spec); err != nil {
			t.Fatal(err)
		}
		op := spec.Paths["/v1/scans/{scanUuid}"]["get"]
		op.Parameters[0].Schema = nil
		if err := json.Unmarshal([]byte(encoded), &op.Parameters[0].Schema); err != nil {
			t.Fatal(err)
		}
		spec.Paths["/v1/scans/{scanUuid}"]["get"] = op
		data, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		if outputs, err := generate(data); err == nil || outputs != nil || !strings.Contains(err.Error(), "nullable AppSec path") {
			t.Fatalf("nullable path generated invalid code: outputs=%d error=%v", len(outputs), err)
		}
	}
}

func TestAppSecStringQueryDefaultsUseJSONEscapes(t *testing.T) {
	var spec openAPISpecification
	if err := json.Unmarshal([]byte(appsecFixture("get")), &spec); err != nil {
		t.Fatal(err)
	}
	op := spec.Paths["/v1/scans/{scanUuid}"]["get"]
	var assertions strings.Builder
	for i, encoded := range []string{`"path\/to"`, `"\ud83d\ude80"`, `"\ud800"`, `"quote\"\nline"`, `"\u0000"`} {
		name := "label" + strconv.Itoa(i)
		op.Parameters = append(op.Parameters, openAPIParameter{Name: name, In: "query", Required: true, Schema: &openAPISchema{Type: openAPITypes{"string"}, Default: json.RawMessage(encoded)}})
		var want string
		if err := json.Unmarshal([]byte(encoded), &want); err != nil {
			t.Fatal(err)
		}
		assertions.WriteString("if q.Get(" + strconv.Quote(name) + ") != " + strconv.Quote(want) + " { t.Fatalf(\"unexpected default: %q\",q.Get(" + strconv.Quote(name) + ")) }\n")
	}
	spec.Paths["/v1/scans/{scanUuid}"]["get"] = op
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	outputs, err := generate(data)
	if err != nil {
		t.Fatal(err)
	}
	testGeneratedPackage(t, outputs, `package sourcecraft
import "testing"
func TestDefaults(t *testing.T) {
 o := &AppSecGetScanOptions{GitRepo:"repo"}
 q,err := o.queryValues()
 if err != nil { t.Fatal(err) }
`+assertions.String()+`}
`)
}
