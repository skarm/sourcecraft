//nolint:testpackage // These tests share white-box helpers for private SDK behavior.
package sourcecraft

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// Exercise the separate API's query encoding and structured, pointer-rich pages.
// Preparing the response and options is outside the measured loop.
func BenchmarkAppSec(b *testing.B) {
	for _, count := range []int{1, 50, 250} {
		b.Run(strconv.Itoa(count), func(b *testing.B) {
			items := make([]*AppSecDefectGroupDTO, count)
			for i := range items {
				items[i] = &AppSecDefectGroupDTO{
					UUID:     AppSecUUID("0199140f-4343-7c06-a7e3-b835c305b629"),
					PublicID: new(int64(i + 1)), GitRepo: "88769874964302633",
					RuleID: "go/sql-injection", RuleName: "SQL injection",
					FileName: "internal/handlers/repositories.go", Engine: "SAST",
					Severity: 4, Status: 1, CvssScore: 8.5,
					CodeBlock: strings.Repeat("query := request.URL.Query()\n", 8),
					HelpText:  new("Use parameterized SQL queries."),
					StartLine: new(int64(42)), FindingsCount: new(int64(3)),
					Links: []*AppSecDefectGroupLinkDTO{{Type: AppSecLinkTypeExternalURL, URL: "https://example.test/issues/42"}},
				}
			}
			data, err := json.Marshal(AppSecPaginatedResponseDefectGroupDTO{Data: items, TotalSize: int64(count)})
			if err != nil {
				b.Fatal(err)
			}
			client, err := NewAppSecClient("token", WithHTTPClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: make(http.Header), ContentLength: int64(len(data)), Body: io.NopCloser(bytes.NewReader(data))}, nil
			})}))
			if err != nil {
				b.Fatal(err)
			}
			opts := &AppSecListDefectGroupsOptions{GitRepo: "88769874964302633", PageSize: int32(count), Severity: []string{"HIGH", "CRITICAL"}}
			ctx := context.Background()
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				page, _, err := client.ListDefectGroups(ctx, opts)
				if err != nil || len(page.Data) != count || page.Data[0].RuleName != "SQL injection" {
					b.Fatalf("AppSec page: %v", err)
				}
			}
		})
	}
}

// Unknown-length and inaccurate length hints must also remain efficient. A
// custom RoundTripper can return these independently of the actual body size.
func BenchmarkResponseLength(b *testing.B) {
	for _, size := range []int{256, 32 << 10, 2 << 20} {
		data, err := json.Marshal(Repository{Description: strings.Repeat("x", size)})
		if err != nil {
			b.Fatal(err)
		}
		for _, hint := range []struct {
			name string
			size int64
		}{{"known", int64(len(data))}, {"unknown", -1}, {"short", 1024}, {"overstated", 256 << 10}, {"long", 32 << 20}} {
			b.Run(strconv.Itoa(size)+"/"+hint.name, func(b *testing.B) {
				client, err := NewClient("token", WithHTTPClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: make(http.Header), ContentLength: hint.size, Body: io.NopCloser(bytes.NewReader(data))}, nil
				})}))
				if err != nil {
					b.Fatal(err)
				}
				ctx := context.Background()
				b.ReportAllocs()
				b.SetBytes(int64(len(data)))
				for b.Loop() {
					repo, _, err := client.GetRepository(ctx, "org", "repo")
					if err != nil || len(repo.Description) != size {
						b.Fatalf("repository: %v", err)
					}
				}
			})
		}
	}
}

func BenchmarkExpandPath(b *testing.B) {
	for _, escaped := range []bool{false, true} {
		value := "repository"
		if escaped {
			value = "repository /?#+%"
		}
		for _, count := range []int{1, 2, 6} {
			b.Run(strconv.Itoa(count)+"/escaped="+strconv.FormatBool(escaped), func(b *testing.B) {
				params := make([]pathParam, count)
				var template strings.Builder
				for i := range params {
					name := "resource" + strconv.Itoa(i)
					params[i] = pathParam{name, value}
					template.WriteString("/{" + name + "}")
				}
				path := template.String()
				b.ReportAllocs()
				for b.Loop() {
					result, err := expandPath(path, params...)
					if err != nil || strings.ContainsAny(result, "{}") {
						b.Fatalf("path=%q error=%v", result, err)
					}
				}
			})
		}
	}
}

// Compare decoding strategies on valid responses before considering a change to
// the SDK. Streaming also checks EOF; preserving all error semantics would need
// additional work, so this benchmark alone cannot justify replacing Client.do.
func BenchmarkJSONResponseReader(b *testing.B) {
	for _, size := range []int{256, 32 << 10, 2 << 20} {
		data, err := json.Marshal(GetCubeLogsResponse{Logs: strings.Repeat("x", size), Done: true})
		if err != nil {
			b.Fatal(err)
		}
		for _, streaming := range []bool{false, true} {
			b.Run(strconv.Itoa(size)+"/streaming="+strconv.FormatBool(streaming), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(data)))
				for b.Loop() {
					reader := io.LimitReader(bytes.NewReader(data), defaultMaxResponseBytes+1)
					var result GetCubeLogsResponse
					var err error
					if streaming {
						decoder := json.NewDecoder(reader)
						err = decoder.Decode(&result)
						if err == nil {
							var extra any
							err = decoder.Decode(&extra)
							if errors.Is(err, io.EOF) {
								err = nil
							} else if err == nil {
								b.Fatal("unexpected second JSON value")
							}
						}
					} else {
						var body []byte
						body, err = io.ReadAll(reader)
						if err == nil {
							err = json.Unmarshal(body, &result)
						}
					}
					if err != nil || len(result.Logs) != size {
						b.Fatalf("decoded response: %v", err)
					}
				}
			})
		}
	}
}
