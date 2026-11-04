package sourcecraft_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/skarm/sourcecraft"
)

// This test uses the official specification as an independent wire contract.
// Each generated method is called through reflection with every documented
// query/body field populated. A transport checks its HTTP request and returns a
// complete specimen of the documented response for a lossless JSON round trip.
type contractSchema struct {
	Ref                  string                     `json:"$ref"`
	Type                 string                     `json:"type"`
	Format               string                     `json:"format"`
	Enum                 []string                   `json:"enum"`
	Properties           map[string]*contractSchema `json:"properties"`
	AdditionalProperties *contractSchema            `json:"additionalProperties"`
	Items                *contractSchema            `json:"items"`
}

type contractParameter struct {
	Name             string          `json:"name"`
	In               string          `json:"in"`
	Type             string          `json:"type"`
	Format           string          `json:"format"`
	CollectionFormat string          `json:"collectionFormat"`
	Schema           *contractSchema `json:"schema"`
}

type contractOperation struct {
	ID         string              `json:"operationId"`
	Parameters []contractParameter `json:"parameters"`
	Responses  map[string]struct {
		Schema *contractSchema `json:"schema"`
	} `json:"responses"`
	RequestBody *struct {
		Content map[string]struct {
			Schema *contractSchema `json:"schema"`
		} `json:"content"`
	} `json:"requestBody"`
}

type contractSpec struct {
	Paths       map[string]map[string]contractOperation `json:"paths"`
	Definitions map[string]*contractSchema              `json:"definitions"`
}

type contractTransport func(*http.Request) (*http.Response, error)

func TestEveryEndpointAgainstSwagger(t *testing.T) {
	data, err := os.ReadFile("api/sourcecraft.swagger.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec contractSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	var count int
	for path, methods := range spec.Paths {
		for method, operation := range methods {
			count++
			t.Run(operation.ID, func(t *testing.T) {
				status := 0
				var responseSchema *contractSchema
				for code, response := range operation.Responses {
					if strings.HasPrefix(code, "2") {
						status, _ = strconv.Atoi(code)
						responseSchema = response.Schema
					}
				}
				if status == 0 {
					t.Fatal("no success status")
				}
				var expectedResponse, expectedBody any
				responseJSON := []byte{}
				if responseSchema != nil {
					expectedResponse = specimen(t, spec, responseSchema, 0)
					responseJSON, err = json.Marshal(expectedResponse)
					if err != nil {
						t.Fatal(err)
					}
				}
				expectedPath := "/api%20v1" + path
				expectedQuery := url.Values{}
				var expectedFields map[string]string
				transport := contractTransport(func(req *http.Request) (*http.Response, error) {
					if req.Method != strings.ToUpper(method) || req.URL.EscapedPath() != expectedPath {
						t.Errorf("request = %s %s; want %s %s", req.Method, req.URL.EscapedPath(), strings.ToUpper(method), expectedPath)
					}
					if !reflect.DeepEqual(req.URL.Query(), expectedQuery) {
						t.Errorf("query = %v, want %v", req.URL.Query(), expectedQuery)
					}
					if req.Header.Get("Authorization") != "Bearer contract-token" {
						t.Error("missing authorization")
					}
					if req.Body != nil {
						defer req.Body.Close()
					}
					if operation.RequestBody != nil {
						mediaType, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
						if err != nil || mediaType != "multipart/form-data" {
							t.Fatalf("multipart content type = %s: %v", mediaType, err)
						}
						reader := multipart.NewReader(req.Body, params["boundary"])
						fields := map[string]string{}
						for {
							part, err := reader.NextPart()
							if errors.Is(err, io.EOF) {
								break
							}
							if err != nil {
								t.Fatal(err)
							}
							content, err := io.ReadAll(part)
							if err != nil {
								t.Fatal(err)
							}
							fields[part.FormName()] = string(content)
							if part.FormName() == "file" && part.FileName() != `report "quoted".bin` {
								t.Errorf("filename = %q", part.FileName())
							}
						}
						if !reflect.DeepEqual(fields, expectedFields) {
							t.Errorf("multipart fields = %v, want %v", fields, expectedFields)
						}
					} else if expectedBody != nil {
						if req.Header.Get("Content-Type") != "application/json" {
							t.Error("missing JSON content type")
						}
						body, err := io.ReadAll(req.Body)
						if err != nil {
							t.Fatal(err)
						}
						assertJSON(t, body, expectedBody)
					} else if req.Body != nil {
						t.Error("unexpected request body")
					}
					return &http.Response{StatusCode: status, Header: http.Header{"X-Request-Id": {"contract"}}, Body: io.NopCloser(strings.NewReader(string(responseJSON)))}, nil
				})
				client, err := sourcecraft.NewClient("contract-token", sourcecraft.WithBaseURL("https://api.example.test/api%20v1"), sourcecraft.WithHTTPClient(&http.Client{Transport: transport}))
				if err != nil {
					t.Fatal(err)
				}
				call := reflect.ValueOf(client).MethodByName(operation.ID)
				if !call.IsValid() {
					t.Fatalf("missing API method %s", operation.ID)
				}
				arguments := []reflect.Value{reflect.ValueOf(t.Context())}
				for _, p := range operation.Parameters {
					if p.In == "path" {
						value := p.Name + " /?#+%"
						arguments = append(arguments, reflect.ValueOf(value))
						expectedPath = strings.ReplaceAll(expectedPath, "{"+p.Name+"}", url.PathEscape(value))
					}
				}
				for _, p := range operation.Parameters {
					if p.In == "body" {
						expectedBody = specimen(t, spec, p.Schema, 0)
						encoded, err := json.Marshal(expectedBody)
						if err != nil {
							t.Fatal(err)
						}
						body := reflect.New(call.Type().In(len(arguments)).Elem())
						if err := json.Unmarshal(encoded, body.Interface()); err != nil {
							t.Fatalf("request model is incompatible with schema: %v", err)
						}
						arguments = append(arguments, body)
					}
				}
				if operation.RequestBody != nil {
					form := operation.RequestBody.Content["multipart/form-data"].Schema
					expectedFields = map[string]string{"file": "file contents\x00\xff"}
					var fields []string
					for name := range form.Properties {
						if name != "file" {
							fields = append(fields, name)
						}
					}
					slices.Sort(fields)
					for _, name := range fields {
						value := name + "-value"
						expectedFields[name] = value
						arguments = append(arguments, reflect.ValueOf(value))
					}
					arguments = append(arguments, reflect.ValueOf(&sourcecraft.Upload{Filename: `report "quoted".bin`, Reader: strings.NewReader(expectedFields["file"])}))
				}
				var queryParams []contractParameter
				for _, p := range operation.Parameters {
					if p.In == "query" {
						queryParams = append(queryParams, p)
					}
				}
				if len(queryParams) > 0 {
					options := reflect.New(call.Type().In(len(arguments)).Elem())
					for _, p := range queryParams {
						field := optionField(t, options.Elem(), p.Name)
						switch p.Type {
						case "string":
							if p.Format == "uint64" {
								field.SetUint(^uint64(0))
								expectedQuery.Set(p.Name, "18446744073709551615")
							} else {
								field.SetString("query /?&=#+%")
								expectedQuery.Set(p.Name, "query /?&=#+%")
							}
						case "boolean":
							if field.Kind() == reflect.Pointer {
								field.Set(reflect.ValueOf(new(false)))
							} else {
								field.SetBool(false)
							}
							expectedQuery.Set(p.Name, "false")
						case "integer":
							field.SetInt(7)
							expectedQuery.Set(p.Name, "7")
						case "array":
							values := []string{"first &/", "second +%"}
							field.Set(reflect.ValueOf(values))
							expectedQuery[p.Name] = values
						default:
							t.Fatalf("unsupported query type %s", p.Type)
						}
					}
					arguments = append(arguments, options)
				}
				results := call.Call(arguments)
				if errValue := results[len(results)-1]; !errValue.IsNil() {
					t.Fatal(errValue.Interface())
				}
				response := results[len(results)-2].Interface().(*sourcecraft.Response)
				if response.StatusCode != status || response.Header.Get("X-Request-ID") != "contract" {
					t.Fatalf("unexpected response metadata: %+v", response)
				}
				if expectedResponse != nil {
					encoded, err := json.Marshal(results[0].Interface())
					if err != nil {
						t.Fatal(err)
					}
					assertJSON(t, encoded, expectedResponse)
				}
			})
		}
	}
	if count == 0 {
		t.Fatal("empty API specification")
	}
	t.Logf("validated %d API endpoints", count)
}

func ExamplePaginate() {
	fetch := func(_ context.Context, token string) ([]int, string, error) {
		if token == "" {
			return []int{1, 2}, "next", nil
		}
		return []int{3}, "", nil
	}
	for number, err := range sourcecraft.Paginate(context.Background(), fetch) {
		if err != nil {
			panic(err)
		}
		fmt.Println(number)
	}
	// Output:
	// 1
	// 2
	// 3
}

func TestContractJSONPreservesLargeIntegers(t *testing.T) {
	first := decodeContractJSON(t, []byte(`{"id":9007199254740992,"maximum":9223372036854775807}`))
	second := decodeContractJSON(t, []byte(`{"id":9007199254740993,"maximum":9223372036854775807}`))
	if reflect.DeepEqual(first, second) {
		t.Fatal("adjacent large integer IDs compared equal")
	}
	if got := second.(map[string]any)["maximum"]; got != json.Number("9223372036854775807") {
		t.Fatalf("maximum int64 lost precision: %v", got)
	}
}

func (f contractTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func specimen(t *testing.T, spec contractSpec, schema *contractSchema, depth int) any {
	t.Helper()
	if depth > 20 {
		t.Fatal("recursive schema needs an explicit fixture")
	}
	if schema.Ref != "" {
		schema = spec.Definitions[strings.TrimPrefix(schema.Ref, "#/definitions/")]
		if schema == nil {
			t.Fatal("unknown schema reference")
		}
	}
	if len(schema.Enum) > 0 {
		for _, value := range schema.Enum {
			if value != "" {
				return value
			}
		}
		return ""
	}
	switch schema.Type {
	case "object":
		object := map[string]any{}
		for key, property := range schema.Properties {
			object[key] = specimen(t, spec, property, depth+1)
		}
		if schema.AdditionalProperties != nil {
			object["entry"] = specimen(t, spec, schema.AdditionalProperties, depth+1)
		}
		return object
	case "array":
		return []any{specimen(t, spec, schema.Items, depth+1)}
	case "boolean":
		return true
	case "integer":
		return 7
	case "number":
		return 2.5
	case "string":
		switch schema.Format {
		case "date-time":
			return "2026-11-04T08:00:00.123456789Z"
		case "int64":
			return "9223372036854775807"
		case "uint64":
			return "18446744073709551615"
		case "byte":
			return "aGVsbG8="
		default:
			return "text /?#+%&"
		}
	case "":
		return map[string]any{"detail": "arbitrary JSON", "large": json.Number("9007199254740993")}
	default:
		t.Fatalf("unhandled schema type %q", schema.Type)
		return nil
	}
}

func assertJSON(t *testing.T, actual []byte, expected any) {
	t.Helper()
	expectedJSON, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeContractJSON(t, actual)
	want := decodeContractJSON(t, expectedJSON)
	if !reflect.DeepEqual(got, want) {
		directory := t.ArtifactDir()
		for name, content := range map[string][]byte{"actual.json": actual, "expected.json": expectedJSON} {
			if err := os.WriteFile(filepath.Join(directory, name), content, 0o644); err != nil {
				t.Errorf("write contract artifact %s: %v", name, err)
			}
		}
		t.Errorf("wire JSON mismatch; actual.json and expected.json are in %s", directory)
	}
}

func decodeContractJSON(t *testing.T, data []byte) any {
	t.Helper()
	if !json.Valid(data) {
		t.Fatalf("invalid JSON %q", data)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func optionField(t *testing.T, options reflect.Value, name string) reflect.Value {
	t.Helper()
	normalize := func(s string) string { return strings.ToLower(strings.ReplaceAll(s, "_", "")) }
	for field, value := range options.Fields() {
		if normalize(field.Name) == normalize(name) {
			return value
		}
	}
	t.Fatalf("option field %s was not generated", name)
	return reflect.Value{}
}
