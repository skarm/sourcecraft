//nolint:testpackage // These tests share white-box helpers for private SDK behavior.
package sourcecraft

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestAppSecDefaultEndpointAndQueryDefaults(t *testing.T) {
	client, err := NewAppSecClient("token", WithHTTPClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Scheme != "https" || req.URL.Host != "appsec.sourcecraft.tech" || req.URL.Path != "/v1/repositories" {
			t.Fatalf("unexpected endpoint %s", req.URL)
		}
		want := url.Values{"orgId": {"org"}, "pageSize": {"50"}, "pageToken": {""}, "sortBy": {"timeFinished"}, "sortOrder": {"DESC"}}
		if !reflect.DeepEqual(req.URL.Query(), want) {
			t.Fatalf("query = %v; want %v", req.URL.Query(), want)
		}
		if req.Header.Get("Authorization") != "Bearer token" {
			t.Fatal("missing bearer token")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[],"nextPageToken":"","totalSize":0}`))}, nil
	})}))
	if err != nil {
		t.Fatal(err)
	}
	page, _, err := client.ListRepositories(t.Context(), &AppSecListRepositoriesOptions{OrgID: "org"})
	if err != nil || page == nil || page.Data == nil {
		t.Fatalf("page=%+v error=%v", page, err)
	}
	if _, err := NewAppSecClient("invalid\ntoken"); err == nil {
		t.Fatal("invalid token accepted")
	}
}

func TestAppSecQueryDefaultsAndValidation(t *testing.T) {
	q, err := (&AppSecListScansOptions{GitRepo: "repo"}).queryValues()
	if err != nil || q.Get("status") != "FINISHED" {
		t.Fatalf("scan default: query=%v error=%v", q, err)
	}
	q, err = (&AppSecListDependenciesOptions{GitRepo: "repo", MinLicenseRiskScore: new(int32(0))}).queryValues()
	if err != nil || q.Get("minLicenseRiskScore") != "0" {
		t.Fatalf("zero-valued filter: query=%v error=%v", q, err)
	}
	q, err = (&AppSecListDependenciesOptions{GitRepo: "repo"}).queryValues()
	if err != nil || q.Has("minLicenseRiskScore") {
		t.Fatalf("absent filter: query=%v error=%v", q, err)
	}
	for _, options := range []queryEncoder{
		(*AppSecListFindingsOptions)(nil),
		&AppSecListFindingsOptions{},
		&AppSecListFindingsOptions{GitRepo: " \t"},
		&AppSecListFindingsOptions{GitRepo: "repo", PageSize: 251},
		&AppSecListFindingsOptions{GitRepo: "repo", PageSize: -1},
		&AppSecListDependenciesOptions{GitRepo: "repo", MinLicenseRiskScore: new(int32(11))},
		&AppSecListDependenciesOptions{GitRepo: "repo", MinLicenseRiskScore: new(int32(-1))},
		&AppSecListDefectGroupsByLinkOptions{GitRepo: "repo", Identifier: "42"},
	} {
		if _, err := options.queryValues(); err == nil {
			t.Errorf("invalid query accepted: %#v", options)
		}
	}
}

func TestAppSecPartialUpdatesAndNumericPrecision(t *testing.T) {
	patch := AppSecChangeRepositoryStatusesRequest{AppsecRepositorySettings: &AppSecRepositorySettings{MergeScaDefectGroupsByPackage: new(false), DastURL: new("")}}
	data, err := json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, data, `{"appsecRepositorySettings":{"mergeScaDefectGroupsByPackage":false,"dastUrl":""}}`)
	var finding AppSecFinding
	if err := json.Unmarshal([]byte(`{"id":9223372036854775807,"timeFound":9223372036854775806,"properties":{"integer":9007199254740993}}`), &finding); err != nil {
		t.Fatal(err)
	}
	if finding.ID == nil || *finding.ID != 9223372036854775807 || finding.TimeFound == nil || *finding.TimeFound != 9223372036854775806 {
		t.Fatalf("lost numeric precision: %+v", finding)
	}
	if string(finding.Properties["integer"]) != "9007199254740993" {
		t.Fatal("lost precision in arbitrary JSON properties")
	}
	var operation AppSecReportOperationDTO
	if err := json.Unmarshal([]byte(`{"operationUuid":"op","status":"IN_PROGRESS","done":false,"reportUuid":null}`), &operation); err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(operation)
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, data, `{"operationUuid":"op","status":"IN_PROGRESS","done":false,"reportUuid":null}`)
}

func TestAppSecUploadResponseModesAndOptionalFields(t *testing.T) {
	for _, status := range []int{200, 202} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			file := &countedBody{Reader: strings.NewReader(strings.Repeat("x", 16))}
			client, err := NewAppSecClient("", WithHTTPClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if file.read != 0 {
					t.Fatal("upload file was read before transport requested it")
				}
				_, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
				if err != nil {
					t.Fatal(err)
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
					data, err := io.ReadAll(part)
					if err != nil {
						t.Fatal(err)
					}
					fields[part.FormName()] = string(data)
				}
				delete(fields, "file")
				want := map[string]string{"commit": "sha", "engineName": "scanner", "reportType": "SARIF", "isReactive": "false", "expectedReportCount": "0"}
				if !reflect.DeepEqual(fields, want) {
					t.Fatalf("form = %v; want %v", fields, want)
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: http.NoBody}, nil
			})}))
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.UploadReport(t.Context(), &AppSecReportUploadForm{Commit: "sha", EngineName: "scanner", ReportType: AppSecReportUploadFormReportTypeSarif, File: &Upload{Filename: "report.sarif", Reader: file}, IsReactive: new(false), ExpectedReportCount: new(int32(0))}, &AppSecUploadReportOptions{GitRepo: "repo"})
			if err != nil || response == nil || response.StatusCode != status || file.read != 16 || file.closed {
				t.Fatalf("response=%v error=%v bytes=%d", response, err, file.read)
			}
		})
	}
}

func TestAppSecUploadReadErrorAndRequiredFields(t *testing.T) {
	client, err := NewAppSecClient("", WithHTTPClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		_, err := io.Copy(io.Discard, req.Body)
		return nil, err
	})}))
	if err != nil {
		t.Fatal(err)
	}
	valid := AppSecReportUploadForm{Commit: "sha", EngineName: "scanner", ReportType: AppSecReportUploadFormReportTypeSarif, File: &Upload{Filename: "report.sarif", Reader: &failingUploadReader{}}}
	if _, err := client.UploadReport(t.Context(), &valid, &AppSecUploadReportOptions{GitRepo: "repo"}); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("read error not preserved: %v", err)
	}
	for _, field := range []string{"body", "commit", "engineName", "reportType", "file"} {
		form := valid
		body := &form
		switch field {
		case "body":
			body = nil
		case "commit":
			form.Commit = ""
		case "engineName":
			form.EngineName = ""
		case "reportType":
			form.ReportType = ""
		case "file":
			form.File = nil
		}
		if _, err := client.UploadReport(t.Context(), body, &AppSecUploadReportOptions{GitRepo: "repo"}); err == nil {
			t.Errorf("missing %s accepted", field)
		}
	}
}

func TestAppSecErrorsRedirectAndCancellation(t *testing.T) {
	calls := 0
	client, err := NewAppSecClient("token", WithHTTPClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"https://other.example.test/redirect"}}, Body: http.NoBody}, nil
	})}))
	if err != nil {
		t.Fatal(err)
	}
	_, response, err := client.ListFindings(t.Context(), &AppSecListFindingsOptions{GitRepo: "repo"})
	apiError, ok := errors.AsType[*APIError](err)
	if !ok || apiError.StatusCode != 302 || response == nil || calls != 1 {
		t.Fatalf("redirect: calls=%d response=%v error=%v", calls, response, err)
	}
	client, err = NewAppSecClient("token", WithHTTPClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if err := req.Context().Err(); err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: 403, Header: http.Header{"X-Request-Id": {"request"}}, Body: io.NopCloser(strings.NewReader(`{"message":"access denied","extra":"appsec"}`))}, nil
	})}))
	if err != nil {
		t.Fatal(err)
	}
	_, response, err = client.ListFindings(t.Context(), &AppSecListFindingsOptions{GitRepo: "repo"})
	apiError, ok = errors.AsType[*APIError](err)
	if !ok || apiError.StatusCode != 403 || apiError.RequestID != "request" || !strings.Contains(string(apiError.Body), `"extra":"appsec"`) || response.StatusCode != 403 {
		t.Fatalf("error metadata: response=%v error=%v", response, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err = client.ListFindings(ctx, &AppSecListFindingsOptions{GitRepo: "repo"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	for _, client := range []*AppSecClient{nil, {}} {
		if _, _, err := client.ListFindings(t.Context(), &AppSecListFindingsOptions{GitRepo: "repo"}); err == nil {
			t.Fatal("uninitialized client accepted")
		}
	}
}
