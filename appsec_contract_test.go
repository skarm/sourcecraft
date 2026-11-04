package sourcecraft_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/skarm/sourcecraft"
)

type appSecContract struct {
	Paths map[string]map[string]struct {
		Parameters []struct {
			Name   string         `json:"name"`
			In     string         `json:"in"`
			Schema map[string]any `json:"schema"`
		} `json:"parameters"`
		RequestBody *struct {
			Content map[string]struct {
				Schema map[string]any `json:"schema"`
			} `json:"content"`
		} `json:"requestBody"`
		Responses map[string]struct {
			Content map[string]struct {
				Schema map[string]any `json:"schema"`
			} `json:"content"`
		} `json:"responses"`
	} `json:"paths"`
	Components struct {
		Schemas map[string]map[string]any `json:"schemas"`
	} `json:"components"`
}

func TestAppSecEveryEndpointAgainstOpenAPI(t *testing.T) {
	methodNames := appSecContractMethods()
	spec := readAppSecContract(t)
	count := 0
	for path, methods := range spec.Paths {
		for method, op := range methods {
			count++
			name := methodNames[method+" "+path]
			if name == "" {
				t.Fatalf("missing SDK method mapping for %s %s", method, path)
			}
			t.Run(name, func(t *testing.T) {
				var responseSchema map[string]any
				status := 0
				for code, response := range op.Responses {
					if strings.HasPrefix(code, "2") {
						n, _ := strconv.Atoi(code)
						if status == 0 || n < status {
							status, responseSchema = n, response.Content["application/json"].Schema
						}
					}
				}
				var expectedResponse, expectedBody any
				responseJSON := []byte{}
				if len(responseSchema) > 0 {
					expectedResponse, _ = spec.specimen(t, responseSchema, map[string]bool{})
					responseJSON, _ = json.Marshal(expectedResponse)
				}
				expectedPath := "/prefix%20v1" + path
				expectedQuery := url.Values{}
				var expectedForm map[string]string
				transport := contractTransport(func(req *http.Request) (*http.Response, error) {
					if req.Method != strings.ToUpper(method) || req.URL.EscapedPath() != expectedPath {
						t.Errorf("request = %s %s; want %s %s", req.Method, req.URL.EscapedPath(), strings.ToUpper(method), expectedPath)
					}
					if !reflect.DeepEqual(req.URL.Query(), expectedQuery) {
						t.Errorf("query = %v; want %v", req.URL.Query(), expectedQuery)
					}
					if req.Header.Get("Authorization") != "Bearer appsec-token" || req.Header.Get("User-Agent") != "contract-appsec" || req.Header.Get("Accept") != "application/json" {
						t.Errorf("incorrect request headers: %v", req.Header)
					}
					if req.Body != nil {
						defer req.Body.Close()
					}
					if expectedForm != nil {
						media, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
						if err != nil || media != "multipart/form-data" {
							t.Fatalf("multipart header: %s %v", media, err)
						}
						reader := multipart.NewReader(req.Body, params["boundary"])
						got := map[string]string{}
						for {
							part, err := reader.NextPart()
							if errors.Is(err, io.EOF) {
								break
							}
							if err != nil {
								t.Fatal(err)
							}
							if _, exists := got[part.FormName()]; exists {
								t.Fatalf("duplicate form field %s", part.FormName())
							}
							data, err := io.ReadAll(part)
							if err != nil {
								t.Fatal(err)
							}
							got[part.FormName()] = string(data)
							if part.FormName() == "file" && part.FileName() != `report "quoted".sarif` {
								t.Errorf("filename = %q", part.FileName())
							}
						}
						if !reflect.DeepEqual(got, expectedForm) {
							t.Errorf("form = %v; want %v", got, expectedForm)
						}
					} else if expectedBody != nil {
						if req.Header.Get("Content-Type") != "application/json" {
							t.Error("missing JSON content type")
						}
						data, err := io.ReadAll(req.Body)
						if err != nil {
							t.Fatal(err)
						}
						assertJSON(t, data, expectedBody)
					} else if req.Body != nil {
						t.Error("unexpected request body")
					}
					return &http.Response{StatusCode: status, Header: http.Header{"X-Request-Id": {"appsec-contract"}}, Body: io.NopCloser(strings.NewReader(string(responseJSON)))}, nil
				})
				client, err := sourcecraft.NewAppSecClient("appsec-token", sourcecraft.WithBaseURL("https://security.example.test/prefix%20v1"), sourcecraft.WithUserAgent("contract-appsec"), sourcecraft.WithHTTPClient(&http.Client{Transport: transport}))
				if err != nil {
					t.Fatal(err)
				}
				call := reflect.ValueOf(client).MethodByName(name)
				if !call.IsValid() {
					t.Fatalf("missing AppSec method %s", name)
				}
				args := []reflect.Value{reflect.ValueOf(t.Context())}
				for _, p := range op.Parameters {
					if p.In != "path" {
						continue
					}
					value := "path /?#+%"
					if appSecKind(spec.resolve(p.Schema)) == "integer" {
						value = "9223372036854775807"
						args = append(args, reflect.ValueOf(int64(9223372036854775807)))
					} else {
						args = append(args, reflect.ValueOf(value))
					}
					expectedPath = strings.ReplaceAll(expectedPath, "{"+p.Name+"}", url.PathEscape(value))
				}
				if op.RequestBody != nil {
					multipartSchema := op.RequestBody.Content["multipart/form-data"].Schema
					bodySchema := op.RequestBody.Content["application/json"].Schema
					if multipartSchema != nil {
						bodySchema = multipartSchema
					}
					expectedBody, _ = spec.specimen(t, bodySchema, map[string]bool{})
					if multipartSchema != nil {
						delete(expectedBody.(map[string]any), "file")
					}
					data, _ := json.Marshal(expectedBody)
					body := reflect.New(call.Type().In(len(args)).Elem())
					if err := json.Unmarshal(data, body.Interface()); err != nil {
						t.Fatal(err)
					}
					if multipartSchema != nil {
						expectedForm = map[string]string{"file": "report\x00\xff"}
						for key, value := range expectedBody.(map[string]any) {
							expectedForm[key] = fmt.Sprint(value)
						}
						body.Elem().FieldByName("File").Set(reflect.ValueOf(&sourcecraft.Upload{Filename: `report "quoted".sarif`, Reader: strings.NewReader(expectedForm["file"])}))
					}
					args = append(args, body)
				}
				opts := reflect.New(call.Type().In(len(args)).Elem())
				for _, p := range op.Parameters {
					if p.In != "query" {
						continue
					}
					field := optionField(t, opts.Elem(), p.Name)
					if field.Kind() == reflect.Pointer {
						field.Set(reflect.New(field.Type().Elem()))
						field = field.Elem()
					}
					switch appSecKind(spec.resolve(p.Schema)) {
					case "string":
						field.SetString("query /?&=#+%")
						expectedQuery.Set(p.Name, field.String())
					case "integer":
						value := int64(9223372036854775807)
						if p.Name == "pageSize" {
							value = 100
						}
						if p.Name == "minLicenseRiskScore" {
							value = 0
						}
						field.SetInt(value)
						expectedQuery.Set(p.Name, strconv.FormatInt(value, 10))
					case "array":
						values := []string{"first &/", "second +%"}
						field.Set(reflect.ValueOf(values))
						expectedQuery[p.Name] = values
					default:
						t.Fatalf("unsupported query schema: %v", p.Schema)
					}
				}
				args = append(args, opts)
				results := call.Call(args)
				if err := results[len(results)-1]; !err.IsNil() {
					t.Fatal(err.Interface())
				}
				response := results[len(results)-2].Interface().(*sourcecraft.Response)
				if response.StatusCode != status || response.Header.Get("X-Request-ID") != "appsec-contract" {
					t.Errorf("incorrect response metadata: %+v", response)
				}
				if expectedResponse != nil {
					data, err := json.Marshal(results[0].Interface())
					if err != nil {
						t.Fatal(err)
					}
					assertJSON(t, data, expectedResponse)
				}
			})
		}
	}
	if count != len(methodNames) || count != 27 {
		t.Fatalf("checked %d methods; mapping contains %d", count, len(methodNames))
	}
	t.Logf("validated %d AppSec API endpoints", count)
}

func TestAppSecEveryModelAgainstOpenAPI(t *testing.T) {
	models := map[string]reflect.Type{
		"Activity":                           reflect.TypeFor[sourcecraft.AppSecActivity](),
		"ActivityComment":                    reflect.TypeFor[sourcecraft.AppSecActivityComment](),
		"ActivityType":                       reflect.TypeFor[sourcecraft.AppSecActivityType](),
		"AllowedTransitionsResponse":         reflect.TypeFor[sourcecraft.AppSecAllowedTransitionsResponse](),
		"AppsecStatus":                       reflect.TypeFor[sourcecraft.AppSecStatus](),
		"Artifact":                           reflect.TypeFor[sourcecraft.AppSecArtifact](),
		"AttachmentDto":                      reflect.TypeFor[sourcecraft.AppSecAttachmentDTO](),
		"BatchDefectGroupsLinksRequest":      reflect.TypeFor[sourcecraft.AppSecBatchDefectGroupsLinksRequest](),
		"BatchDefectGroupsLinksResponse":     reflect.TypeFor[sourcecraft.AppSecBatchDefectGroupsLinksResponse](),
		"BatchDefectGroupsStatusRequest":     reflect.TypeFor[sourcecraft.AppSecBatchDefectGroupsStatusRequest](),
		"BatchDefectGroupsStatusResponse":    reflect.TypeFor[sourcecraft.AppSecBatchDefectGroupsStatusResponse](),
		"BatchDefectGroupsTicketUrlRequest":  reflect.TypeFor[sourcecraft.AppSecBatchDefectGroupsTicketURLRequest](),
		"ChangeRepositoryStatusesRequest":    reflect.TypeFor[sourcecraft.AppSecChangeRepositoryStatusesRequest](),
		"CodeFlowStep":                       reflect.TypeFor[sourcecraft.AppSecCodeFlowStep](),
		"Commit":                             reflect.TypeFor[sourcecraft.AppSecCommit](),
		"CreateCommentRequest":               reflect.TypeFor[sourcecraft.AppSecCreateCommentRequest](),
		"DastContext":                        reflect.TypeFor[sourcecraft.AppSecDastContext](),
		"DefectGroup":                        reflect.TypeFor[sourcecraft.AppSecDefectGroup](),
		"DefectGroupDto":                     reflect.TypeFor[sourcecraft.AppSecDefectGroupDTO](),
		"DefectGroupLinkDto":                 reflect.TypeFor[sourcecraft.AppSecDefectGroupLinkDTO](),
		"DefectGroupLinksUpdate":             reflect.TypeFor[sourcecraft.AppSecDefectGroupLinksUpdate](),
		"DefectGroupStatus":                  reflect.TypeFor[sourcecraft.AppSecDefectGroupStatus](),
		"DefectGroupStatusUpdateByPublicId":  reflect.TypeFor[sourcecraft.AppSecDefectGroupStatusUpdateByPublicID](),
		"DefectGroupTicketUrlUpdate":         reflect.TypeFor[sourcecraft.AppSecDefectGroupTicketURLUpdate](),
		"Dependency":                         reflect.TypeFor[sourcecraft.AppSecDependency](),
		"DependencyCveDto":                   reflect.TypeFor[sourcecraft.AppSecDependencyCveDTO](),
		"DependencyInfoDto":                  reflect.TypeFor[sourcecraft.AppSecDependencyInfoDTO](),
		"EngineType":                         reflect.TypeFor[sourcecraft.AppSecEngineType](),
		"EngineType1":                        reflect.TypeFor[sourcecraft.AppSecFindingEngineType](),
		"Finding":                            reflect.TypeFor[sourcecraft.AppSecFinding](),
		"FixRunPullRequest":                  reflect.TypeFor[sourcecraft.AppSecFixRunPullRequest](),
		"FixRunPullRequestResponse":          reflect.TypeFor[sourcecraft.AppSecFixRunPullRequestResponse](),
		"GitReference":                       reflect.TypeFor[sourcecraft.AppSecGitReference](),
		"LinkToAdd":                          reflect.TypeFor[sourcecraft.AppSecLinkToAdd](),
		"LinkType":                           reflect.TypeFor[sourcecraft.AppSecLinkType](),
		"OperationStatus":                    reflect.TypeFor[sourcecraft.AppSecOperationStatus](),
		"PaginatedResponseActivity":          reflect.TypeFor[sourcecraft.AppSecPaginatedResponseActivity](),
		"PaginatedResponseDefectGroupDto":    reflect.TypeFor[sourcecraft.AppSecPaginatedResponseDefectGroupDTO](),
		"PaginatedResponseDependencyInfoDto": reflect.TypeFor[sourcecraft.AppSecPaginatedResponseDependencyInfoDTO](),
		"PaginatedResponseFinding":           reflect.TypeFor[sourcecraft.AppSecPaginatedResponseFinding](),
		"PaginatedResponseRepositoryScanDto": reflect.TypeFor[sourcecraft.AppSecPaginatedResponseRepositoryScanDTO](),
		"PaginatedResponseScanSummaryDto":    reflect.TypeFor[sourcecraft.AppSecPaginatedResponseScanSummaryDTO](),
		"PrComment":                          reflect.TypeFor[sourcecraft.AppSecPRComment](),
		"ReferenceType":                      reflect.TypeFor[sourcecraft.AppSecReferenceType](),
		"RelatedLocation":                    reflect.TypeFor[sourcecraft.AppSecRelatedLocation](),
		"RepoStatus":                         reflect.TypeFor[sourcecraft.AppSecRepoStatus](),
		"ReportDto":                          reflect.TypeFor[sourcecraft.AppSecReportDTO](),
		"ReportOperationDto":                 reflect.TypeFor[sourcecraft.AppSecReportOperationDTO](),
		"ReportUploadForm":                   reflect.TypeFor[sourcecraft.AppSecReportUploadForm](),
		"Repository":                         reflect.TypeFor[sourcecraft.AppSecRepository](),
		"RepositoryScanDto":                  reflect.TypeFor[sourcecraft.AppSecRepositoryScanDTO](),
		"RestAppsecSettings":                 reflect.TypeFor[sourcecraft.AppSecRepositorySettings](),
		"RestRepositoryStatuses":             reflect.TypeFor[sourcecraft.AppSecRepositoryStatuses](),
		"Rule":                               reflect.TypeFor[sourcecraft.AppSecRule](),
		"ScaScanResponse":                    reflect.TypeFor[sourcecraft.AppSecScaScanResponse](),
		"Scan":                               reflect.TypeFor[sourcecraft.AppSecScan](),
		"ScanDetailsDto":                     reflect.TypeFor[sourcecraft.AppSecScanDetailsDTO](),
		"ScanOrigin":                         reflect.TypeFor[sourcecraft.AppSecScanOrigin](),
		"ScanStatus":                         reflect.TypeFor[sourcecraft.AppSecScanStatus](),
		"ScanStatus1":                        reflect.TypeFor[sourcecraft.AppSecScanFilterStatus](),
		"ScanSummaryDto":                     reflect.TypeFor[sourcecraft.AppSecScanSummaryDTO](),
		"ScanType":                           reflect.TypeFor[sourcecraft.AppSecScanType](),
		"Severity":                           reflect.TypeFor[sourcecraft.AppSecSeverity](),
		"TagDto":                             reflect.TypeFor[sourcecraft.AppSecTagDTO](),
		"UUID":                               reflect.TypeFor[sourcecraft.AppSecUUID](),
		"UpdateCommentRequest":               reflect.TypeFor[sourcecraft.AppSecUpdateCommentRequest](),
	}
	spec := readAppSecContract(t)
	if len(models) != len(spec.Components.Schemas) || len(models) != 66 {
		t.Fatalf("%d Go models, %d schemas", len(models), len(spec.Components.Schemas))
	}
	for name, schema := range spec.Components.Schemas {
		t.Run(name, func(t *testing.T) {
			typ, ok := models[name]
			if !ok {
				t.Fatal("missing Go model")
			}
			expected, _ := spec.specimen(t, schema, map[string]bool{})
			if name == "ReportUploadForm" {
				delete(expected.(map[string]any), "file")
			}
			input, err := json.Marshal(expected)
			if err != nil {
				t.Fatal(err)
			}
			model := reflect.New(typ)
			if err := json.Unmarshal(input, model.Interface()); err != nil {
				t.Fatal(err)
			}
			output, err := json.Marshal(model.Interface())
			if err != nil {
				t.Fatal(err)
			}
			assertJSON(t, output, expected)
			if properties, ok := schema["properties"].(map[string]any); ok {
				for field, raw := range properties {
					if !appSecNullable(raw.(map[string]any)) {
						continue
					}
					model := reflect.New(typ)
					input, _ := json.Marshal(map[string]any{field: nil})
					if err := json.Unmarshal(input, model.Interface()); err != nil {
						t.Fatalf("nullable field %s: %v", field, err)
					}
					value := optionField(t, model.Elem(), field)
					if value.Kind() != reflect.Pointer && value.Kind() != reflect.Map && value.Kind() != reflect.Slice {
						t.Fatalf("nullable field %s has non-nullable Go type %s", field, value.Type())
					}
					if !value.IsNil() {
						t.Errorf("nullable field %s did not decode to nil", field)
					}
				}
			}
		})
	}
}

// This is deliberately independent of internal/generate's OpenAPI parser and
// endpoint table. The checked-in official document is the HTTP contract.
func appSecContractMethods() map[string]string {
	return map[string]string{
		"get /v1/activities":                                   "ListActivities",
		"post /v1/activities/comment":                          "CreateComment",
		"patch /v1/activities/comment/{activityUuid}":          "UpdateComment",
		"delete /v1/activities/comment/{activityUuid}":         "DeleteComment",
		"get /v1/activities/{activityUuid}":                    "GetActivity",
		"get /v1/admin/repository-statuses":                    "GetRepositoryStatuses",
		"post /v1/admin/repository-statuses":                   "ChangeRepositoryStatuses",
		"post /v1/ai/fix-runs/{runUuid}/pull-request":          "ReportPullRequest",
		"get /v1/defect-groups":                                "ListDefectGroups",
		"get /v1/defect-groups/by-link":                        "ListDefectGroupsByLink",
		"post /v1/defect-groups/links/batch":                   "AddBatchDefectGroupsLinks",
		"post /v1/defect-groups/status/batch":                  "SetBatchDefectGroupsStatus",
		"post /v1/defect-groups/ticket-url/batch":              "SetBatchDefectGroupsTicketURL",
		"get /v1/defect-groups/types":                          "ListEngineTypes",
		"get /v1/defect-groups/{publicId}":                     "GetDefectGroup",
		"get /v1/defect-groups/{publicId}/allowed-transitions": "GetAllowedTransitions",
		"get /v1/dependencies":                                 "ListDependencies",
		"get /v1/findings":                                     "ListFindings",
		"post /v1/reports/generate":                            "GenerateReport",
		"get /v1/reports/operations/{operationUuid}":           "GetReportOperation",
		"get /v1/reports/presigned-url":                        "GetReportURL",
		"post /v1/reports/upload":                              "UploadReport",
		"get /v1/repositories":                                 "ListRepositories",
		"get /v1/scans":                                        "ListScans",
		"get /v1/scans/latest":                                 "GetLatestScan",
		"post /v1/scans/sca":                                   "StartSCAScan",
		"get /v1/scans/{scanUuid}":                             "GetScan",
	}
}

func (s appSecContract) resolve(schema map[string]any) map[string]any {
	if ref, ok := schema["$ref"].(string); ok {
		return s.Components.Schemas[strings.TrimPrefix(ref, "#/components/schemas/")]
	}
	if branches, ok := schema["anyOf"].([]any); ok {
		for _, branch := range branches {
			b := branch.(map[string]any)
			if b["type"] != "null" {
				return s.resolve(b)
			}
		}
	}
	if branches, ok := schema["allOf"].([]any); ok {
		for _, branch := range branches {
			b := branch.(map[string]any)
			if b["$ref"] != nil || b["type"] != nil {
				return s.resolve(b)
			}
		}
	}
	return schema
}

func (s appSecContract) specimen(t *testing.T, schema map[string]any, visiting map[string]bool) (any, bool) {
	t.Helper()
	if ref, ok := schema["$ref"].(string); ok {
		if visiting[ref] {
			return nil, false
		}
		visiting[ref] = true
		defer delete(visiting, ref)
	}
	schema = s.resolve(schema)
	if schema == nil {
		t.Fatal("missing schema")
	}
	if values, ok := schema["enum"].([]any); ok {
		return values[0], true
	}
	switch appSecKind(schema) {
	case "object":
		out := map[string]any{}
		if properties, ok := schema["properties"].(map[string]any); ok {
			for name, raw := range properties {
				if value, include := s.specimen(t, raw.(map[string]any), visiting); include {
					out[name] = value
				}
			}
		}
		if additional, ok := schema["additionalProperties"].(map[string]any); ok {
			out["dynamic-key"], _ = s.specimen(t, additional, visiting)
		}
		return out, true
	case "array":
		value, include := s.specimen(t, schema["items"].(map[string]any), visiting)
		if !include {
			return []any{}, true
		}
		return []any{value}, true
	case "string":
		return "text /?#+%&", true
	case "integer":
		if schema["format"] == "int32" {
			return 7, true
		}
		return json.Number("9223372036854775807"), true
	case "number":
		return 2.5, true
	case "boolean":
		return false, true
	case "":
		return map[string]any{"large": json.Number("9007199254740993"), "nested": []any{false, nil, "value"}}, true
	default:
		t.Fatalf("unsupported specimen schema: %v", schema)
		return nil, false
	}
}

func readAppSecContract(t *testing.T) appSecContract {
	t.Helper()
	data, err := os.ReadFile("api/appsec.openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec appSecContract
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	return spec
}

func appSecKind(schema map[string]any) string {
	if typ, ok := schema["type"].(string); ok {
		return typ
	}
	if types, ok := schema["type"].([]any); ok {
		for _, typ := range types {
			if typ != "null" {
				return typ.(string)
			}
		}
	}
	return ""
}

func appSecNullable(schema map[string]any) bool {
	if types, ok := schema["type"].([]any); ok && slices.Contains(types, any("null")) {
		return true
	}
	if branches, ok := schema["anyOf"].([]any); ok {
		for _, raw := range branches {
			if raw.(map[string]any)["type"] == "null" {
				return true
			}
		}
	}
	return false
}
