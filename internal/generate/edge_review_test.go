package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRejectOperationIDsThatAreNotGoIdentifiers(t *testing.T) {
	for _, name := range []string{"Injected() {}\nfunc init() {}\nfunc (c *Client) GetThing", "with-dash", "type", "getThing", "_"} {
		spec := smallSpec("get")
		op := spec.Paths["/things/{thing_id}"]["get"]
		op.ID = name
		spec.Paths["/things/{thing_id}"]["get"] = op
		data, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		if outputs, err := generate(data); err == nil || outputs != nil {
			t.Errorf("operation ID %q accepted as Go source", name)
		}
	}
}

func TestPathParameterNamesCompile(t *testing.T) {
	for _, name := range []string{"ctx", "err", "http", "nil", "body", "path", "expand_path", "do_json", "_Thing", "bool"} {
		t.Run(name, func(t *testing.T) {
			spec := smallSpec("get")
			op := spec.Paths["/things/{thing_id}"]["get"]
			op.Parameters[0].Name = name
			if name == "bool" {
				r := op.Responses["200"]
				r.Schema = &schema{Type: "boolean"}
				op.Responses["200"] = r
			}
			spec.Paths = map[string]map[string]operation{"/things/{" + name + "}": {"get": op}}
			testGeneratedPackage(t, generateSpec(t, spec), "package sourcecraft\n")
		})
	}
}

func TestAppSecInlineResponseAndScalarBodyCompile(t *testing.T) {
	for _, kind := range []string{"raw_map_response", "required_scalar", "optional_scalar"} {
		t.Run(kind, func(t *testing.T) {
			var spec openAPISpecification
			if err := json.Unmarshal([]byte(appsecFixture("get")), &spec); err != nil {
				t.Fatal(err)
			}
			op := spec.Paths["/v1/scans/{scanUuid}"]["get"]
			if kind == "raw_map_response" {
				r := op.Responses["200"]
				r.Content = openAPIContent{"application/json": {Schema: &openAPISchema{Type: openAPITypes{"object"}, AdditionalProperties: &openAPISchema{}}}}
				op.Responses["200"] = r
			} else {
				if err := json.Unmarshal([]byte(`{"required":true,"content":{"application/json":{"schema":{"type":"string"}}}}`), &op.RequestBody); err != nil {
					t.Fatal(err)
				}
				op.RequestBody.Required = kind == "required_scalar"
			}
			spec.Paths["/v1/scans/{scanUuid}"]["get"] = op
			data, _ := json.Marshal(spec)
			outputs, err := generate(data)
			if err != nil {
				t.Fatal(err)
			}
			testGeneratedPackage(t, outputs, "package sourcecraft\n")
		})
	}
}

func TestPlatformRejectsNamedDictionaries(t *testing.T) {
	for _, properties := range []map[string]*schema{nil, {"known": {Type: "string"}}} {
		spec := smallSpec("get")
		spec.Definitions["Thing"].AdditionalProperties = &schema{Type: "string"}
		spec.Definitions["Thing"].Properties = properties
		data, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		if outputs, err := generate(data); err == nil || outputs != nil {
			t.Error("named dictionary accepted even though its entries cannot be represented by the generated struct")
		}
	}
}

func TestRejectMixedInlineObjects(t *testing.T) {
	for _, appsec := range []bool{false, true} {
		var data []byte
		if appsec {
			var spec openAPISpecification
			if err := json.Unmarshal([]byte(appsecFixture("get")), &spec); err != nil {
				t.Fatal(err)
			}
			spec.Components.Schemas["Thing"].Properties["mixed"] = &openAPISchema{Type: openAPITypes{"object"}, Properties: map[string]*openAPISchema{"count": {Type: openAPITypes{"integer"}}}, AdditionalProperties: &openAPISchema{Type: openAPITypes{"string"}}}
			data, _ = json.Marshal(spec)
		} else {
			spec := smallSpec("get")
			spec.Definitions["Thing"].Properties["mixed"] = &schema{Type: "object", Properties: map[string]*schema{"count": {Type: "integer"}}, AdditionalProperties: &schema{Type: "string"}}
			data, _ = json.Marshal(spec)
		}
		if outputs, err := generate(data); err == nil || outputs != nil {
			t.Errorf("mixed object loses its declared property types (AppSec=%v)", appsec)
		}
	}
}

func TestModelJSONNames(t *testing.T) {
	for _, appsec := range []bool{false, true} {
		for _, name := range []string{"名称", "-"} {
			label := "platform/" + name
			if appsec {
				label = "appsec/" + name
			}
			t.Run(label, func(t *testing.T) {
				var outputs map[string][]byte
				typeName := "Thing"
				if appsec {
					var spec openAPISpecification
					if err := json.Unmarshal([]byte(appsecFixture("get")), &spec); err != nil {
						t.Fatal(err)
					}
					spec.Components.Schemas["Thing"] = &openAPISchema{Type: openAPITypes{"object"}, Required: []string{name}, Properties: map[string]*openAPISchema{name: {Type: openAPITypes{"string"}}}}
					data, _ := json.Marshal(spec)
					var err error
					outputs, err = generate(data)
					if err != nil {
						t.Fatal(err)
					}
					typeName = "AppSecThing"
				} else {
					spec := smallSpec("get")
					spec.Definitions["Thing"] = &schema{Type: "object", Required: []string{name}, Properties: map[string]*schema{name: {Type: "string"}}}
					outputs = generateSpec(t, spec)
				}
				wire, _ := json.Marshal(map[string]string{name: "value"})
				literal, _ := json.Marshal(string(wire))
				testGeneratedPackage(t, outputs, `package sourcecraft
import ("encoding/json"; "testing")
func TestRoundTrip(t *testing.T) {
 var model `+typeName+`
 if err := json.Unmarshal([]byte(`+string(literal)+`), &model); err != nil { t.Fatal(err) }
 data,err := json.Marshal(model)
 if err != nil || string(data) != `+string(literal)+` { t.Fatalf("round trip: %s %v",data,err) }
}
`)
			})
		}
	}
}

func TestRejectUnrepresentableJSONNames(t *testing.T) {
	for _, name := range []string{"", "with,comma", "bad\"quote", "bad\\slash", "bad\nline"} {
		for _, appsec := range []bool{false, true} {
			var data []byte
			if appsec {
				var spec openAPISpecification
				if err := json.Unmarshal([]byte(appsecFixture("get")), &spec); err != nil {
					t.Fatal(err)
				}
				spec.Components.Schemas["Thing"].Properties[name] = &openAPISchema{Type: openAPITypes{"string"}}
				data, _ = json.Marshal(spec)
			} else {
				spec := smallSpec("get")
				spec.Definitions["Thing"].Properties[name] = &schema{Type: "string"}
				data, _ = json.Marshal(spec)
			}
			if outputs, err := generate(data); err == nil || outputs != nil {
				t.Errorf("unrepresentable JSON name %q accepted (AppSec=%v)", name, appsec)
			}
		}
	}
}

func TestRejectDuplicateQueryFields(t *testing.T) {
	for _, appsec := range []bool{false, true} {
		var data []byte
		if appsec {
			var spec openAPISpecification
			if err := json.Unmarshal([]byte(appsecFixture("get")), &spec); err != nil {
				t.Fatal(err)
			}
			op := spec.Paths["/v1/scans/{scanUuid}"]["get"]
			op.Parameters = append(op.Parameters, openAPIParameter{Name: "git_repo", In: "query", Schema: &openAPISchema{Type: openAPITypes{"string"}}})
			spec.Paths["/v1/scans/{scanUuid}"]["get"] = op
			data, _ = json.Marshal(spec)
		} else {
			spec := smallSpec("get")
			op := spec.Paths["/things/{thing_id}"]["get"]
			op.Parameters = append(op.Parameters, parameter{Name: "page_id", In: "query", Type: "string"}, parameter{Name: "pageID", In: "query", Type: "string"})
			spec.Paths["/things/{thing_id}"]["get"] = op
			data, _ = json.Marshal(spec)
		}
		if outputs, err := generate(data); err == nil || outputs != nil {
			t.Errorf("duplicate query fields accepted (AppSec=%v)", appsec)
		}
	}
}

func TestOptionalBodiesOmitNil(t *testing.T) {
	for _, appsec := range []bool{false, true} {
		name, call, explicit := "platform", `client.CreateThing(t.Context(), "id", nil, nil)`, `client.CreateThing(t.Context(), "id", &CreateThingBody{}, nil)`
		var outputs map[string][]byte
		if appsec {
			name, call, explicit = "appsec", `client.CreateComment(t.Context(), nil, nil)`, `client.CreateComment(t.Context(), &AppSecThing{}, nil)`
			var spec openAPISpecification
			if err := json.Unmarshal([]byte(appsecFixture("json")), &spec); err != nil {
				t.Fatal(err)
			}
			op := spec.Paths["/v1/activities/comment"]["post"]
			op.RequestBody.Required = false
			op.Parameters = op.Parameters[:2]
			spec.Paths["/v1/activities/comment"]["post"] = op
			data, _ := json.Marshal(spec)
			var err error
			outputs, err = generate(data)
			if err != nil {
				t.Fatal(err)
			}
		} else {
			spec := smallSpec("post_query")
			op := spec.Paths["/things/{thing_id}"]["post"]
			op.Parameters[1].Required = false
			spec.Paths["/things/{thing_id}"]["post"] = op
			outputs = generateSpec(t, spec)
		}
		t.Run(name, func(t *testing.T) {
			clientType := "Client"
			if appsec {
				clientType = "AppSecClient"
			}
			runtime := strings.Replace(generatedRuntimeStub, "type Client struct{}", "var observedBody any\ntype Client struct{}", 1)
			runtime = strings.Replace(runtime, "opts,body any) (*T,*Response,error) { return nil,nil,nil }", "opts,body any) (*T,*Response,error) { observedBody=body; return nil,nil,nil }", 1)
			testGeneratedPackageWithRuntime(t, outputs, `package sourcecraft
import "testing"
func TestOmittedBody(t *testing.T) {
 client := &`+clientType+`{}
 if _,_,err := `+call+`; err != nil { t.Fatal(err) }
 if observedBody != nil { t.Fatalf("nil optional body encoded as typed %T instead of omitted", observedBody) }
 if _,_,err := `+explicit+`; err != nil { t.Fatal(err) }
 if observedBody == nil { t.Fatal("present body omitted") }
}
`, runtime)
		})
	}
}
