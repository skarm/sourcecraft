package main

import (
	"encoding/json"
	"testing"
)

func TestPlatformIntegerQueryEncodings(t *testing.T) {
	for _, tc := range []struct{ format, value, want string }{
		{"int32", "-2147483648", "-2147483648"},
		{"uint64", "^uint64(0)", "18446744073709551615"},
	} {
		t.Run(tc.format, func(t *testing.T) {
			spec := smallSpec("get")
			op := spec.Paths["/things/{thing_id}"]["get"]
			op.Parameters = append(op.Parameters, parameter{Name: "value", In: "query", Type: "integer", Format: tc.format})
			spec.Paths["/things/{thing_id}"]["get"] = op
			testGeneratedPackage(t, generateSpec(t, spec), `package sourcecraft
import "testing"
func TestWire(t *testing.T) {
 o := &GetThingOptions{Value:`+tc.value+`}
 q,err := o.queryValues()
 if err != nil || q.Get("value") != "`+tc.want+`" { t.Fatalf("query=%v error=%v",q,err) }
 o.Value = 0
 q,err = o.queryValues()
 if err != nil || q.Has("value") { t.Fatalf("optional zero: query=%v error=%v",q,err) }
}
`)
		})
	}
}

func TestPlatformRejectsUnencodableQueryTypes(t *testing.T) {
	for _, p := range []parameter{
		{Name: "value", In: "query", Type: "string", Format: "date-time"},
		{Name: "value", In: "query", Type: "string", Format: "byte"},
		{Name: "value", In: "query", Type: "array", CollectionFormat: "multi", Items: &schema{Type: "string", Format: "date-time"}},
	} {
		spec := smallSpec("get")
		op := spec.Paths["/things/{thing_id}"]["get"]
		op.Parameters = append(op.Parameters, p)
		spec.Paths["/things/{thing_id}"]["get"] = op
		data, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		if outputs, err := generate(data); err == nil || outputs != nil {
			t.Errorf("unencodable query accepted: type=%s format=%s error=%v", p.Type, p.Format, err)
		}
	}
}

func TestPlatformInlineResponseImports(t *testing.T) {
	for _, response := range []*schema{
		{Type: "string", Format: "date-time"},
		{Type: "object", AdditionalProperties: &schema{}},
	} {
		spec := smallSpec("get")
		op := spec.Paths["/things/{thing_id}"]["get"]
		r := op.Responses["200"]
		r.Schema = response
		op.Responses["200"] = r
		spec.Paths["/things/{thing_id}"]["get"] = op
		testGeneratedPackage(t, generateSpec(t, spec), "package sourcecraft\n")
	}
}

func TestAppSecOptionalDefaultsPreserveExplicitValues(t *testing.T) {
	for _, tc := range []struct{ name, typ, defaultJSON, field, value, want string }{
		{"count", "integer", "7", "Count", "new(int64(0))", "0"},
		{"label", "string", `"fallback"`, "Label", `new("")`, ""},
		{"enabled", "boolean", "true", "Enabled", "new(false)", "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var spec openAPISpecification
			if err := json.Unmarshal([]byte(appsecFixture("get")), &spec); err != nil {
				t.Fatal(err)
			}
			op := spec.Paths["/v1/scans/{scanUuid}"]["get"]
			op.Parameters = append(op.Parameters, openAPIParameter{Name: tc.name, In: "query", Schema: &openAPISchema{Type: openAPITypes{tc.typ}, Default: json.RawMessage(tc.defaultJSON)}})
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
func TestWire(t *testing.T) {
 o := &AppSecGetScanOptions{GitRepo:"repo", `+tc.field+`:`+tc.value+`}
 q,err := o.queryValues()
 if err != nil || !q.Has("`+tc.name+`") || q.Get("`+tc.name+`") != "`+tc.want+`" { t.Fatalf("explicit value: query=%v error=%v",q,err) }
 o.`+tc.field+` = nil
 q,err = o.queryValues()
 if err != nil || q.Has("`+tc.name+`") { t.Fatalf("absent value: query=%v error=%v",q,err) }
}
`)
		})
	}
}

func TestGenerateRejectsDeclarationCollisions(t *testing.T) {
	t.Run("appsec_enum_constant", func(t *testing.T) {
		var spec openAPISpecification
		if err := json.Unmarshal([]byte(appsecFixture("get")), &spec); err != nil {
			t.Fatal(err)
		}
		spec.Components.Schemas["StateOpen"] = &openAPISchema{Type: openAPITypes{"object"}}
		data, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		if outputs, err := generate(data); err == nil || outputs != nil {
			t.Fatalf("constant/type collision accepted: %v", err)
		}
	})
	for _, name := range []string{"GetThingOptions", "Client", "NewClient", "AppSecFinding", "AppSecListFindingsOptions"} {
		t.Run(name, func(t *testing.T) {
			spec := smallSpec("get")
			op := spec.Paths["/things/{thing_id}"]["get"]
			op.Parameters = append(op.Parameters, parameter{Name: "page", In: "query", Type: "string"})
			spec.Paths["/things/{thing_id}"]["get"] = op
			spec.Definitions[name] = &schema{Type: "object"}
			data, err := json.Marshal(spec)
			if err != nil {
				t.Fatal(err)
			}
			if outputs, err := generate(data); err == nil || outputs != nil {
				t.Fatalf("declaration collision accepted: %v", err)
			}
		})
	}
}
