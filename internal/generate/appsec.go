package main

// AppSec publishes OpenAPI 3.1 without operationId values. This compiler reads
// its lossless JSON snapshot, names methods explicitly, and rejects constructs
// outside the subset used by that API. The platform Swagger compiler is separate.
import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

type openAPITypes []string

type openAPISchema struct {
	Ref                  string                    `json:"$ref"`
	Type                 openAPITypes              `json:"type"`
	Format               string                    `json:"format"`
	Description          string                    `json:"description"`
	Properties           map[string]*openAPISchema `json:"properties"`
	Items                *openAPISchema            `json:"items"`
	AdditionalProperties *openAPISchema            `json:"additionalProperties"`
	Enum                 []string                  `json:"enum"`
	Required             []string                  `json:"required"`
	AnyOf                []*openAPISchema          `json:"anyOf"`
	AllOf                []*openAPISchema          `json:"allOf"`
	OneOf                []json.RawMessage         `json:"oneOf"`
	Default              json.RawMessage           `json:"default"`
	Pattern              string                    `json:"pattern"`
	Minimum              *int64                    `json:"minimum"`
	Maximum              *int64                    `json:"maximum"`
	Deprecated           bool                      `json:"deprecated"`
}

type openAPIContent map[string]struct {
	Schema *openAPISchema `json:"schema"`
}

type openAPIParameter struct {
	Name        string         `json:"name"`
	In          string         `json:"in"`
	Description string         `json:"description"`
	Required    bool           `json:"required"`
	Deprecated  bool           `json:"deprecated"`
	Schema      *openAPISchema `json:"schema"`
}

type openAPIOperation struct {
	Summary     string             `json:"summary"`
	Parameters  []openAPIParameter `json:"parameters"`
	RequestBody *struct {
		Required bool           `json:"required"`
		Content  openAPIContent `json:"content"`
	} `json:"requestBody"`
	Responses map[string]struct {
		Content openAPIContent `json:"content"`
	} `json:"responses"`
}

type openAPISpecification struct {
	OpenAPI    string `json:"openapi"`
	Components struct {
		Schemas map[string]*openAPISchema `json:"schemas"`
	} `json:"components"`
	Paths map[string]map[string]openAPIOperation `json:"paths"`
}

type appsecGenerator struct{ spec openAPISpecification }

func generateAppSec(data []byte) (map[string][]byte, error) {
	var g appsecGenerator
	if err := json.Unmarshal(data, &g.spec); err != nil {
		return nil, err
	}

	if g.spec.OpenAPI != "3.1.0" {
		return nil, fmt.Errorf("unsupported AppSec OpenAPI version %q", g.spec.OpenAPI)
	}

	if len(g.spec.Paths) == 0 || len(g.spec.Components.Schemas) == 0 {
		return nil, errors.New("empty AppSec specification")
	}

	outputs := map[string][]byte{"appsec_models_gen.go": g.models(), "appsec_api_gen.go": g.methods()}
	for _, name := range slices.Sorted(maps.Keys(outputs)) {
		formatted, err := formatGeneratedFile(name, outputs[name])
		if err != nil {
			return nil, err
		}

		outputs[name] = formatted
	}

	return outputs, nil
}

func (t *openAPITypes) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*t = []string{single}
		return nil
	}

	return json.Unmarshal(data, (*[]string)(t))
}

// unwrapSchema unwraps nullable unions and the reference/default allOf used by query
// parameters. Other unions are rejected rather than silently losing alternatives.
func (g *appsecGenerator) unwrapSchema(s *openAPISchema) (*openAPISchema, bool) {
	if s == nil {
		panic("missing AppSec schema")
	}

	nullable := slices.Contains(s.Type, typeNull)
	if len(s.OneOf) != 0 {
		panic("unsupported AppSec oneOf")
	}

	if len(s.AnyOf) != 0 {
		var alternative *openAPISchema

		for _, branch := range s.AnyOf {
			if len(branch.Type) == 1 && branch.Type[0] == typeNull {
				nullable = true
				continue
			}

			if alternative != nil {
				panic("unsupported AppSec anyOf")
			}

			alternative = branch
		}

		if alternative == nil {
			panic("AppSec union has no value type")
		}

		base, innerNullable := g.unwrapSchema(alternative)

		return base, nullable || innerNullable
	}

	if len(s.AllOf) != 0 {
		return g.unwrapAllOf(s)
	}

	for _, typ := range s.Type {
		if typ != typeNull && typ != g.kind(s) {
			panic("unsupported AppSec type union")
		}
	}

	return s, nullable
}

func (g *appsecGenerator) kind(s *openAPISchema) string {
	for _, typ := range s.Type {
		if typ != typeNull {
			return typ
		}
	}

	return ""
}

func (g *appsecGenerator) resolve(s *openAPISchema) *openAPISchema {
	if s.Ref == "" {
		return s
	}

	const prefix = "#/components/schemas/"
	if !strings.HasPrefix(s.Ref, prefix) {
		panic("unsupported AppSec reference: " + s.Ref)
	}

	resolved := g.spec.Components.Schemas[strings.TrimPrefix(s.Ref, prefix)]
	if resolved == nil {
		panic("missing AppSec reference: " + s.Ref)
	}

	return resolved
}

func (g *appsecGenerator) fieldType(s *openAPISchema, required bool) string {
	base, nullable := g.unwrapSchema(s)
	resolved := g.resolve(base)

	var typ string
	if base.Ref != "" {
		typ = appsecName(strings.TrimPrefix(base.Ref, "#/components/schemas/"))
	} else {
		switch g.kind(base) {
		case typeString:
			if base.Format != "" && base.Format != formatUUID && base.Format != "binary" {
				panic("unsupported AppSec string format: " + base.Format)
			}

			typ = typeString
		case typeInteger:
			switch base.Format {
			case typeInt32:
				typ = typeInt32
			case typeInt64, "":
				typ = typeInt64
			default:
				panic("unsupported AppSec integer format")
			}
		case typeNumber:
			if base.Format != "" && base.Format != "double" {
				panic("unsupported AppSec number format")
			}

			typ = typeFloat64
		case typeBoolean:
			typ = typeBool
		case typeArray:
			return "[]" + g.fieldType(base.Items, true)
		case typeObject:
			if base.AdditionalProperties == nil {
				panic("unsupported AppSec inline object")
			}

			if len(base.Properties) != 0 {
				panic("mixed AppSec object properties and dictionary entries are unsupported")
			}

			return "map[string]" + g.fieldType(base.AdditionalProperties, true)
		case "":
			return "json.RawMessage"
		default:
			panic("unsupported AppSec type: " + g.kind(base))
		}
	}

	if g.kind(resolved) == typeObject || nullable || !required {
		typ = "*" + typ
	}

	return typ
}

func (g *appsecGenerator) models() []byte {
	b := appsecPreamble(`"encoding/json"`)
	names := map[string]bool{}

	for _, name := range slices.Sorted(maps.Keys(g.spec.Components.Schemas)) {
		g.model(b, name, names)
	}

	return b.Bytes()
}

func (g *appsecGenerator) enum(b *bytes.Buffer, typ string, values []string) {
	fmt.Fprintf(b, "type %s string\n\nconst (\n", typ)

	seen := map[string]bool{}

	for _, value := range values {
		name := typ + goName(strings.ToLower(value))
		if seen[name] {
			panic("duplicate AppSec enum constant: " + name)
		}

		seen[name] = true
		fmt.Fprintf(b, "%s %s = %q\n", name, typ, value)
	}

	fmt.Fprintln(b, ")")
}

func (g *appsecGenerator) defaultValue(s *openAPISchema) json.RawMessage {
	if len(s.Default) > 0 {
		return s.Default
	}

	for _, branch := range s.AllOf {
		if value := g.defaultValue(branch); len(value) > 0 {
			return value
		}
	}

	return nil
}

func (g *appsecGenerator) options(b *bytes.Buffer, name string, params []openAPIParameter) {
	fmt.Fprintf(b, "// %s contains query parameters for the AppSec API.\ntype %s struct {\n", name, name)

	for _, p := range params {
		g.optionField(b, p)
	}

	fmt.Fprintf(b, "}\nfunc (o *%s) queryValues() (url.Values,error) {\nq := make(url.Values)\nif o == nil { return nil, fmt.Errorf(%q) }\n", name, "sourcecraft: AppSec query options are required")

	for _, p := range params {
		g.optionValue(b, p)
	}

	fmt.Fprintln(b, "return q,nil\n}")
}

func (g *appsecGenerator) methods() []byte {
	b := appsecPreamble(`"context"; "encoding/json"; "fmt"; "net/http"; "net/url"; "strconv"; "strings"`)
	seen := map[string]bool{}

	for _, path := range slices.Sorted(maps.Keys(g.spec.Paths)) {
		for _, method := range slices.Sorted(maps.Keys(g.spec.Paths[path])) {
			g.method(b, path, method, seen)
		}
	}

	return b.Bytes()
}

func (g *appsecGenerator) responseType(content openAPIContent) string {
	if len(content) == 0 {
		return ""
	}

	media, ok := content["application/json"]
	if !ok || len(content) != 1 {
		panic("unsupported AppSec response media")
	}

	if media.Schema == nil {
		panic("missing AppSec response schema")
	}

	s, nullable := g.unwrapSchema(media.Schema)
	if nullable {
		panic("nullable AppSec response schema is unsupported")
	}
	// An unspecified JSON schema ({}) and an absent body both return metadata.
	// UploadReport deliberately supports this combination for 200 and 202.
	if s.Ref == "" && len(s.Type) == 0 {
		if len(s.Properties) != 0 || s.AdditionalProperties != nil || s.Items != nil || len(s.Enum) != 0 {
			panic("unsupported AppSec response schema without a type")
		}

		return ""
	}

	return strings.TrimPrefix(g.fieldType(s, true), "*")
}

func appsecName(name string) string {
	switch name {
	case "AppsecStatus":
		return "AppSecStatus"
	case "RestAppsecSettings":
		return "AppSecRepositorySettings"
	case "RestRepositoryStatuses":
		return "AppSecRepositoryStatuses"
	case "EngineType1":
		return "AppSecFindingEngineType"
	case "ScanStatus1":
		return "AppSecScanFilterStatus"
	}

	return "AppSec" + strings.ReplaceAll(goName(name), "Dto", "DTO")
}

func appsecPreamble(imports string) *bytes.Buffer {
	var b bytes.Buffer
	fmt.Fprintln(&b, "// Code generated by go generate from api/appsec.openapi.json; DO NOT EDIT.\n\npackage sourcecraft\n\nimport ("+imports+")")

	return &b
}

func appsecVariable(name string) string {
	return safeVariableName(goName(name))
}

// Keep these names stable even if a documentation summary is reworded.
func appsecMethod(endpoint string) string {
	switch endpoint {
	case "get /v1/activities":
		return "ListActivities"
	case "post /v1/activities/comment":
		return "CreateComment"
	case "patch /v1/activities/comment/{activityUuid}":
		return "UpdateComment"
	case "delete /v1/activities/comment/{activityUuid}":
		return "DeleteComment"
	case "get /v1/activities/{activityUuid}":
		return "GetActivity"
	case "get /v1/admin/repository-statuses":
		return "GetRepositoryStatuses"
	case "post /v1/admin/repository-statuses":
		return "ChangeRepositoryStatuses"
	case "post /v1/ai/fix-runs/{runUuid}/pull-request":
		return "ReportPullRequest"
	case "get /v1/defect-groups":
		return "ListDefectGroups"
	case "get /v1/defect-groups/by-link":
		return "ListDefectGroupsByLink"
	case "post /v1/defect-groups/links/batch":
		return "AddBatchDefectGroupsLinks"
	case "post /v1/defect-groups/status/batch":
		return "SetBatchDefectGroupsStatus"
	case "post /v1/defect-groups/ticket-url/batch":
		return "SetBatchDefectGroupsTicketURL"
	case "get /v1/defect-groups/types":
		return "ListEngineTypes"
	case "get /v1/defect-groups/{publicId}":
		return "GetDefectGroup"
	case "get /v1/defect-groups/{publicId}/allowed-transitions":
		return "GetAllowedTransitions"
	case "get /v1/dependencies":
		return "ListDependencies"
	case "get /v1/findings":
		return "ListFindings"
	case "post /v1/reports/generate":
		return "GenerateReport"
	case "get /v1/reports/operations/{operationUuid}":
		return "GetReportOperation"
	case "get /v1/reports/presigned-url":
		return "GetReportURL"
	case "post /v1/reports/upload":
		return "UploadReport"
	case "get /v1/repositories":
		return "ListRepositories"
	case "get /v1/scans":
		return "ListScans"
	case "get /v1/scans/latest":
		return "GetLatestScan"
	case "post /v1/scans/sca":
		return "StartSCAScan"
	case "get /v1/scans/{scanUuid}":
		return "GetScan"
	}

	return ""
}

func (g *appsecGenerator) unwrapAllOf(s *openAPISchema) (*openAPISchema, bool) {
	var base *openAPISchema

	for _, branch := range s.AllOf {
		if branch.Ref == "" && len(branch.Type) == 0 && len(branch.Properties) == 0 && len(branch.Default) > 0 {
			continue
		}

		if base != nil {
			panic("unsupported AppSec allOf")
		}

		base = branch
	}

	if base == nil {
		panic("AppSec allOf has no value type")
	}

	return g.unwrapSchema(base)
}

func (g *appsecGenerator) model(b *bytes.Buffer, name string, names map[string]bool) {
	s := g.spec.Components.Schemas[name]
	if s.Ref != "" || len(s.Type) != 1 || len(s.AnyOf) != 0 || len(s.AllOf) != 0 || len(s.OneOf) != 0 || s.AdditionalProperties != nil {
		panic("unsupported AppSec model schema: " + name)
	}

	typ := appsecName(name)
	if names[typ] {
		panic("duplicate AppSec model: " + typ)
	}

	names[typ] = true
	comment(b, typ+" is the AppSec API model for "+name+".")

	if s.Description != "" {
		comment(b, s.Description)
	}

	if len(s.Enum) > 0 {
		g.enum(b, typ, s.Enum)
		return
	}

	if g.kind(s) != typeObject {
		fmt.Fprintf(b, "type %s %s\n\n", typ, g.fieldType(s, true))
		return
	}

	fmt.Fprintf(b, "type %s struct {\n", typ)

	fields := map[string]bool{}

	for _, field := range slices.Sorted(maps.Keys(s.Properties)) {
		property := s.Properties[field]

		fieldName := goName(field)
		if fields[fieldName] {
			panic("duplicate AppSec field: " + fieldName)
		}

		fields[fieldName] = true

		if property.Description != "" {
			comment(b, property.Description)
		}

		if property.Deprecated {
			comment(b, "Deprecated: deprecated by the AppSec specification.")
		}

		required := slices.Contains(s.Required, field)

		fieldType := g.fieldType(property, required)
		if len(property.Enum) > 0 {
			fieldType = strings.Replace(fieldType, typeString, typ+fieldName, 1)
		}

		tag := jsonFieldName(field)
		if !required {
			tag += ",omitzero"
		}

		if tag == "-" {
			tag += ","
		}

		if name == "ReportUploadForm" && field == nameFile {
			fieldType, tag = "*Upload", "-"
		}

		fmt.Fprintf(b, "%s %s `json:%q`\n", fieldName, fieldType, tag)
	}

	fmt.Fprintln(b, "}")

	for _, field := range slices.Sorted(maps.Keys(s.Properties)) {
		if property := s.Properties[field]; len(property.Enum) > 0 {
			g.enum(b, typ+goName(field), property.Enum)
		}
	}
}

func (g *appsecGenerator) optionField(b *bytes.Buffer, p openAPIParameter) {
	if p.Description != "" {
		comment(b, p.Description)
	}

	if p.Deprecated {
		comment(b, "Deprecated: deprecated by the AppSec specification.")
	}

	if value := g.defaultValue(p.Schema); len(value) > 0 {
		if strings.HasPrefix(g.fieldType(p.Schema, p.Required), "*") {
			comment(b, "A nil value uses the API default: "+string(value)+".")
		} else {
			comment(b, "The zero value uses the API default: "+string(value)+".")
		}
	}

	fmt.Fprintf(b, "%s %s\n", goName(p.Name), g.fieldType(p.Schema, p.Required))
}

func (g *appsecGenerator) optionValue(b *bytes.Buffer, p openAPIParameter) {
	base, _ := g.unwrapSchema(p.Schema)
	resolved := g.resolve(base)
	field, typ := "o."+goName(p.Name), g.fieldType(p.Schema, p.Required)
	pointer := strings.HasPrefix(typ, "*")

	value := field
	if pointer {
		fmt.Fprintf(b, "if %s != nil {\n", field)
		value = "*" + field
	}

	defaultValue := g.defaultValue(p.Schema)
	// A pointer's nil value omits the parameter. A present pointer must
	// preserve an explicit zero, false or empty string instead of a default.
	if !pointer && len(defaultValue) > 0 && string(defaultValue) != `""` && string(defaultValue) != typeNull {
		zero := `""`

		switch g.kind(resolved) {
		case typeString:
		case typeInteger:
			zero = "0"
		default:
			panic("unsupported AppSec query default: " + p.Name)
		}

		fmt.Fprintf(b, "value%s := %s\nif value%s == %s { value%s = %s }\n", goName(p.Name), value, goName(p.Name), zero, goName(p.Name), defaultValue)
		value = "value" + goName(p.Name)
	}

	if g.kind(resolved) == typeString && p.Required && !pointer && len(defaultValue) == 0 {
		fmt.Fprintf(b, "if strings.TrimSpace(string(%s)) == \"\" { return nil,fmt.Errorf(%q) }\n", value, "sourcecraft: AppSec query parameter "+p.Name+" is required")
	}

	if resolved.Minimum != nil {
		fmt.Fprintf(b, "if %s < %d { return nil,fmt.Errorf(%q) }\n", value, *resolved.Minimum, "sourcecraft: AppSec query parameter "+p.Name+" is below its minimum")
	}

	if resolved.Maximum != nil {
		fmt.Fprintf(b, "if %s > %d { return nil,fmt.Errorf(%q) }\n", value, *resolved.Maximum, "sourcecraft: AppSec query parameter "+p.Name+" exceeds its maximum")
	}

	switch g.kind(resolved) {
	case typeString:
		fmt.Fprintf(b, "q.Set(%q,string(%s))\n", p.Name, value)
	case typeInteger:
		fmt.Fprintf(b, "q.Set(%q,strconv.FormatInt(int64(%s),10))\n", p.Name, value)
	case typeBoolean:
		fmt.Fprintf(b, "q.Set(%q,strconv.FormatBool(%s))\n", p.Name, value)
	case typeArray:
		if base.Items == nil || g.kind(base.Items) != typeString {
			panic("unsupported AppSec query array")
		}

		fmt.Fprintf(b, "for _, value := range %s { q.Add(%q,value) }\n", value, p.Name)
	default:
		panic("unsupported AppSec query type")
	}

	if pointer {
		fmt.Fprintln(b, "}")
	}
}

func (g *appsecGenerator) method(b *bytes.Buffer, path, method string, seen map[string]bool) {
	op := g.spec.Paths[path][method]

	name := appsecMethod(method + " " + path)
	if name == "" || seen[name] {
		panic("unknown or duplicate AppSec endpoint: " + method + " " + path)
	}

	seen[name] = true

	pathParams, query := appsecParameters(op)

	options := "AppSec" + name + "Options"
	g.options(b, options, query)

	responseType := g.successResponseType(op, name)

	bodyType, multipart := g.requestType(op)

	errorReturn := g.methodSignature(b, path, method, op, name, options, pathParams, bodyType, responseType)

	if bodyType != "" && op.RequestBody.Required {
		fmt.Fprintf(b, "if body == nil { err := fmt.Errorf(%q); %s }\n", "sourcecraft: body is required for AppSec "+name, errorReturn)
	}

	g.methodPath(b, path, pathParams, errorReturn)

	body := appsecRequestBody(b, op, bodyType, multipart)

	if multipart {
		g.multipartMethod(b, method, bodyType, responseType)
	} else if responseType == "" {
		fmt.Fprintf(b, "return doEmpty(ctx,c.transport(),%q,path,opts,%s)\n", strings.ToUpper(method), body)
	} else {
		fmt.Fprintf(b, "return doJSON[%s](ctx,c.transport(),%q,path,opts,%s)\n", responseType, strings.ToUpper(method), body)
	}

	fmt.Fprintln(b, "}")
}

func appsecParameters(op openAPIOperation) ([]openAPIParameter, []openAPIParameter) {
	var pathParams, query []openAPIParameter

	for _, p := range op.Parameters {
		switch p.In {
		case namePath:
			pathParams = append(pathParams, p)
		case "query":
			query = append(query, p)
		default:
			panic("unsupported AppSec parameter location")
		}
	}

	return pathParams, query
}

func (g *appsecGenerator) successResponseType(op openAPIOperation, name string) string {
	var (
		responseType string
		success      bool
	)

	for _, status := range slices.Sorted(maps.Keys(op.Responses)) {
		if !strings.HasPrefix(status, "2") {
			continue
		}

		candidate := g.responseType(op.Responses[status].Content)
		if success && candidate != responseType {
			panic("incompatible AppSec success responses: " + name)
		}

		responseType = candidate
		success = true
	}

	if !success {
		panic("missing AppSec success response")
	}

	return responseType
}

func (g *appsecGenerator) requestType(op openAPIOperation) (string, bool) {
	var bodyType string

	multipart := false

	if op.RequestBody != nil {
		if len(op.RequestBody.Content) != 1 {
			panic("unsupported AppSec request content")
		}

		for media, content := range op.RequestBody.Content {
			if media != "application/json" && media != "multipart/form-data" {
				panic("unsupported AppSec request media")
			}

			multipart = media == "multipart/form-data"
			// A body argument must express absence, including scalar bodies.
			bodyType = g.fieldType(content.Schema, false)
		}
	}

	return bodyType, multipart
}

func (g *appsecGenerator) methodSignature(b *bytes.Buffer, path, method string, op openAPIOperation, name, options string, pathParams []openAPIParameter, bodyType, responseType string) string {
	comment(b, name+" calls "+strings.ToUpper(method)+" "+path+".")
	comment(b, op.Summary+".")
	fmt.Fprintf(b, "func (c *AppSecClient) %s(ctx context.Context", name)

	for _, p := range pathParams {
		fmt.Fprintf(b, ", %s %s", appsecVariable(p.Name), g.fieldType(p.Schema, true))
	}

	if bodyType != "" {
		fmt.Fprintf(b, ", body %s", bodyType)
	}

	fmt.Fprintf(b, ", opts *%s)", options)

	errorReturn := "return nil,err"

	if responseType == "" {
		fmt.Fprintln(b, " (*Response,error) {")
	} else {
		fmt.Fprintf(b, " (*%s,*Response,error) {\n", responseType)

		errorReturn = "return nil,nil,err"
	}

	return errorReturn
}

func (g *appsecGenerator) methodPath(b *bytes.Buffer, path string, pathParams []openAPIParameter, errorReturn string) {
	fmt.Fprintf(b, "path,err := expandPath(%q", path)

	for _, p := range pathParams {
		value := appsecVariable(p.Name)

		base, _ := g.unwrapSchema(p.Schema)
		if g.kind(base) == typeInteger {
			value = "strconv.FormatInt(int64(" + value + "),10)"
		} else if g.kind(base) != typeString {
			panic("unsupported AppSec path type")
		}

		fmt.Fprintf(b, ", pathParam{%q,%s}", p.Name, value)
	}

	fmt.Fprintf(b, ")\nif err != nil { %s }\n", errorReturn)
}

func appsecRequestBody(b *bytes.Buffer, op openAPIOperation, bodyType string, multipart bool) string {
	body := exprNil
	if bodyType != "" {
		body = nameBody

		if !op.RequestBody.Required {
			if multipart {
				panic("optional AppSec multipart bodies are unsupported")
			}

			fmt.Fprintln(b, "var requestBody any\nif body != nil { requestBody = body }")

			body = nameRequestBody
		}
	}

	return body
}

func (g *appsecGenerator) multipartMethod(b *bytes.Buffer, method, bodyType, responseType string) {
	if responseType != "" {
		panic("unsupported typed AppSec upload response")
	}

	form := g.spec.Components.Schemas["ReportUploadForm"]

	if bodyType != "*AppSecReportUploadForm" || method != "post" {
		panic("unsupported AppSec upload form")
	}

	fmt.Fprintln(b, "fields := []formField{}")

	for _, field := range slices.Sorted(maps.Keys(form.Properties)) {
		g.multipartField(b, form, field)
	}

	fmt.Fprintln(b, "return c.uploadReport(ctx,path,opts,body.File,fields)")
}

func (g *appsecGenerator) multipartField(b *bytes.Buffer, form *openAPISchema, field string) {
	if field == nameFile {
		return
	}

	p := form.Properties[field]
	value := "body." + goName(field)

	required := slices.Contains(form.Required, field)
	if !required {
		fmt.Fprintf(b, "if %s != nil {\n", value)
		value = "*" + value
	}

	base, _ := g.unwrapSchema(p)
	switch g.kind(base) {
	case typeString:
		if required {
			fmt.Fprintf(b, "if strings.TrimSpace(string(%s)) == \"\" { return nil,fmt.Errorf(%q) }\n", value, "sourcecraft: multipart field "+field+" is required")
		}

		value = "string(" + value + ")"
	case typeInteger:
		value = "strconv.FormatInt(int64(" + value + "),10)"
	case typeBoolean:
		value = "strconv.FormatBool(" + value + ")"
	default:
		panic("unsupported AppSec multipart field")
	}

	fmt.Fprintf(b, "fields = append(fields,formField{%q,%s})\n", field, value)

	if !required {
		fmt.Fprintln(b, "}")
	}
}
