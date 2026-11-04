//nolint:testpackage // These tests share white-box helpers for private SDK behavior.
package sourcecraft

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

type trackedBody struct{ closed bool }

type countedBody struct {
	io.Reader
	read   int
	closed bool
}

type readFunc func([]byte) (int, error)

func TestListRepositoriesHTTP(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/prefix/me/repos" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		for key, want := range map[string]string{"Authorization": "Bearer test-token", "Accept": "application/json", "User-Agent": "test-sdk", "Content-Type": ""} {
			if got := r.Header.Get(key); got != want {
				t.Errorf("%s = %q, want %q", key, got, want)
			}
		}
		want := url.Values{"page_size": {"100"}, "page_token": {"a+b/=&? token"}}
		if !reflect.DeepEqual(r.URL.Query(), want) {
			t.Errorf("query = %v, want %v", r.URL.Query(), want)
		}
		w.Header().Set("X-Request-ID", "req-123")
		fmt.Fprint(w, `{"repositories":[{"id":"repo-id","slug":"demo","counters":{"forks":"18446744073709551615"},"last_updated":"2026-11-04T08:00:00Z"}],"next_page_token":"next"}`)
	}, WithUserAgent("test-sdk"))
	// A base URL prefix must be preserved rather than replaced by endpoint paths.
	client.baseURL.Path = "/prefix/"
	page, response, err := client.ListMyRepositories(t.Context(), &ListMyRepositoriesOptions{PageSize: 100, PageToken: "a+b/=&? token"})
	if err != nil {
		t.Fatal(err)
	}
	if page.NextPageToken != "next" || len(page.Repositories) != 1 {
		t.Fatalf("unexpected page: %+v", page)
	}
	repo := page.Repositories[0]
	if repo.Slug != "demo" || repo.Counters.Forks != ^uint64(0) || repo.LastUpdated == nil {
		t.Fatalf("unexpected repository: %+v", repo)
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("X-Request-ID") != "req-123" {
		t.Fatalf("unexpected metadata: %+v", response)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestOptionalRequestFieldsPreserveZeroValues(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %s", r.Method)
		}
		if r.URL.Query().Get("silent") != "false" {
			t.Errorf("query = %v", r.URL.Query())
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		assertJSON(t, data, `{"description":"","links":[],"settings":{"allow_external_contributions":false}}`)
		fmt.Fprint(w, `{"id":"repo"}`)
	})
	body := &UpdateRepositoryBody{Description: new(""), Links: []Link{}, Settings: &RepositorySettings{AllowExternalContributions: new(false)}}
	_, _, err := client.UpdateRepository(t.Context(), "org", "repo", body, &UpdateRepositoryOptions{Silent: new(false)})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(&UpdateRepositoryBody{})
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, encoded, `{}`)
	encoded, err = json.Marshal(&SetSecretBody{Value: []byte("secret")})
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, encoded, `{"value":"c2VjcmV0"}`)
}

func TestAPIErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"structured", http.StatusTooManyRequests, `{"error_code":"QuotaExceeded","message":"try later","request_id":"json-id","details":{"quota":"requests"}}`},
		{"proxy", http.StatusBadGateway, `<html>upstream failure</html>`},
		{"empty", http.StatusForbidden, ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("X-Request-ID", "header-id")
				w.Header().Set("Retry-After", "3")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			result, response, err := client.GetRepository(t.Context(), "org", "repo")
			apiError, ok := errors.AsType[*APIError](err)
			if !ok {
				t.Fatalf("error = %v, want APIError", err)
			}
			if result != nil || response.StatusCode != tc.status || apiError.StatusCode != tc.status {
				t.Fatalf("result = %v; metadata = %v; error = %+v", result, response, apiError)
			}
			if string(apiError.Body) != tc.body || apiError.Path != "/repos/org/repo" || apiError.Method != http.MethodGet {
				t.Fatalf("unexpected error: %+v", apiError)
			}
			if tc.name == "structured" {
				if apiError.ErrorCode != "QuotaExceeded" || apiError.RequestID != "json-id" {
					t.Fatalf("unexpected error payload: %+v", apiError)
				}
				assertJSON(t, apiError.Details, `{"quota":"requests"}`)
			} else if apiError.RequestID != "header-id" {
				t.Fatalf("request ID = %q", apiError.RequestID)
			}
			if response.Header.Get("Retry-After") != "3" || calls.Load() != 1 {
				t.Fatalf("unexpected retry behavior: calls=%d", calls.Load())
			}
		})
	}
}

func TestResponseDecodingFailures(t *testing.T) {
	for _, payload := range []string{"", "null", "{", "{} {}", "[]", `{"counters":{"forks":"invalid"}}`} {
		t.Run(fmt.Sprintf("payload_%q", payload), func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, payload) })
			result, response, err := client.GetRepository(t.Context(), "org", "repo")
			if err == nil || result != nil || response == nil {
				t.Fatalf("result=%v response=%v error=%v", result, response, err)
			}
		})
	}
}

func TestResponseSizeLimit(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"slug":"too-long"}`) }, WithMaxResponseBytes(8))
	_, response, err := client.GetRepository(t.Context(), "org", "repo")
	if err == nil || !strings.Contains(err.Error(), "exceeds 8 bytes") || response == nil {
		t.Fatalf("response=%v error=%v", response, err)
	}
}

func TestResponseReadErrorClosesBody(t *testing.T) {
	body := &trackedBody{}
	client, err := NewClient("", WithHTTPClient(&http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body}, nil
	})}))
	if err != nil {
		t.Fatal(err)
	}
	_, response, err := client.GetRepository(t.Context(), "org", "repo")
	if !errors.Is(err, io.ErrUnexpectedEOF) || response == nil || !body.closed {
		t.Fatalf("error=%v response=%v closed=%v", err, response, body.closed)
	}
}

func TestCrossOriginRedirectDoesNotForwardToken(t *testing.T) {
	var destinationCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		destinationCalls.Add(1)
		fmt.Fprint(w, `{}`)
	}))
	defer destination.Close()
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/capture", http.StatusFound)
	})
	_, response, err := client.GetRepository(t.Context(), "org", "repo")
	if _, ok := errors.AsType[*APIError](err); !ok || response.StatusCode != http.StatusFound {
		t.Fatalf("response=%v error=%v", response, err)
	}
	if destinationCalls.Load() != 0 {
		t.Fatalf("redirect leaked a request to another origin")
	}
}

func TestSameOriginRedirectAndCustomHTTPClient(t *testing.T) {
	var redirectCalls atomic.Int32
	provided := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { redirectCalls.Add(1); return nil }}
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/redirected" {
			http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("missing token after same-origin redirect")
		}
		fmt.Fprint(w, `{"id":"redirected"}`)
	}, WithHTTPClient(provided))
	repo, _, err := client.GetRepository(t.Context(), "org", "repo")
	if err != nil || repo.ID != "redirected" || redirectCalls.Load() != 1 {
		t.Fatalf("repo=%v error=%v redirects=%d", repo, err, redirectCalls.Load())
	}
	if client.httpClient == provided || provided.CheckRedirect == nil {
		t.Fatal("caller HTTP client settings were mutated")
	}
}

func TestContextCancellation(t *testing.T) {
	started := make(chan struct{})
	client := testClient(t, func(_ http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan error, 1)
	go func() { _, _, err := client.GetRepository(ctx, "org", "repo"); finished <- err }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not stop the request")
	}
}

func TestInvalidClientInputs(t *testing.T) {
	for _, option := range []Option{
		WithBaseURL(""), WithBaseURL("/relative"), WithBaseURL("ftp://example.com"), WithBaseURL("http://"),
		WithBaseURL("https://user:secret@example.com"), WithBaseURL("https://example.com?x=y"),
		WithBaseURL("https://example.com#fragment"), WithHTTPClient(nil), WithUserAgent("bad\r\nheader"),
		WithMaxResponseBytes(0), WithMaxResponseBytes(-1), WithMaxResponseBytes(1<<63 - 1), nil,
	} {
		if _, err := NewClient("", option); err == nil {
			t.Fatalf("expected invalid option to fail")
		}
	}
	if _, err := NewClient("bad\nvalue"); err == nil {
		t.Fatal("expected invalid token to fail")
	}
	client, err := NewClient("")
	if err != nil {
		t.Fatal(err)
	}
	if client.httpClient.Timeout != 30*time.Second {
		t.Fatalf("default timeout = %v", client.httpClient.Timeout)
	}
	for _, segment := range []string{"", ".", ".."} {
		if _, _, err := client.GetRepository(t.Context(), segment, "repo"); err == nil {
			t.Fatalf("accepted path segment %q", segment)
		}
	}
	if _, _, err := client.CreateRepository(t.Context(), "org", nil); err == nil {
		t.Fatal("accepted a nil required body")
	}
	if _, _, err := client.GetOrganizationNeurocreditsUsage(t.Context(), "org", nil); err == nil {
		t.Fatal("accepted missing required query options")
	}
	if _, _, err := client.GetOrganizationNeurocreditsUsage(t.Context(), "org", &GetOrganizationNeurocreditsUsageOptions{}); err == nil {
		t.Fatal("accepted missing required start date")
	}
	if _, _, err := client.GetRepository(nil, "org", "repo"); err == nil {
		t.Fatal("accepted nil context")
	}
	var zero Client
	if _, _, err := zero.GetRepository(t.Context(), "org", "repo"); err == nil {
		t.Fatal("accepted zero client")
	}
}

func TestEmptyTokenOmitsAuthorization(t *testing.T) {
	client, err := NewClient("", WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if _, present := r.Header["Authorization"]; present {
			t.Error("Authorization was set for an empty token")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.GetRepository(t.Context(), "org", "repo"); err != nil {
		t.Fatal(err)
	}
}

func TestEmptyResponseReads(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		limit  int64
		fail   bool
	}{
		{"empty", 204, "", 8, false},
		{"at_limit", 200, strings.Repeat("x", 4096), 4096, false},
		{"over_limit", 200, strings.Repeat("x", 16384), 4096, false},
		{"read_error", 200, "data", 8, true},
		{"error_before_limit", 200, "123456789", 8, true},
		{"api_error", 400, `{"error_code":"Denied","message":"no"}`, 128, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reader io.Reader = strings.NewReader(tc.body)
			if tc.fail {
				reader = readFunc(func(p []byte) (int, error) { return copy(p, tc.body), io.ErrUnexpectedEOF })
			}
			body := &countedBody{Reader: reader}
			client, err := NewClient("", WithMaxResponseBytes(tc.limit), WithHTTPClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: body}, nil
			})}))
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.DeleteRepository(t.Context(), "org", "repo", nil)
			if response == nil || response.StatusCode != tc.status || !body.closed || int64(body.read) > tc.limit+1 {
				t.Fatalf("response=%v read=%d closed=%v", response, body.read, body.closed)
			}
			switch {
			case tc.fail:
				if !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatalf("read error = %v", err)
				}
			case int64(len(tc.body)) > tc.limit:
				if err == nil || !strings.Contains(err.Error(), "response exceeds") {
					t.Fatalf("limit error = %v", err)
				}
			case tc.status >= 300:
				apiError, ok := errors.AsType[*APIError](err)
				if !ok || string(apiError.Body) != tc.body || apiError.ErrorCode != "Denied" {
					t.Fatalf("API error = %v", err)
				}
			default:
				if err != nil || body.read != len(tc.body) {
					t.Fatalf("read=%d error=%v", body.read, err)
				}
			}
		})
	}
}

func TestEmptyResponseEncoding(t *testing.T) {
	for _, encoded := range []bool{false, true} {
		for _, size := range []int{0, 4096, 4097} {
			t.Run(fmt.Sprintf("gzip=%v/size=%d", encoded, size), func(t *testing.T) {
				client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Trailer", "X-End")
					if encoded {
						w.Header().Set("Content-Encoding", "gzip")
					}
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
					var writer io.Writer = w
					if encoded {
						compressed := gzip.NewWriter(w)
						defer compressed.Close()
						writer = compressed
					}
					_, _ = io.WriteString(writer, strings.Repeat("x", size))
					w.Header().Set("X-End", "done")
				}, WithMaxResponseBytes(4096))
				response, err := client.DeleteRepository(t.Context(), "org", "repo", nil)
				if size > 4096 {
					if err == nil || !strings.Contains(err.Error(), "response exceeds") {
						t.Fatalf("limit error = %v", err)
					}
				} else if err != nil || response.Trailer.Get("X-End") != "done" {
					t.Fatalf("response=%v error=%v", response, err)
				}
			})
		}
	}
}

func TestEmptyResponseCancellation(t *testing.T) {
	started := make(chan struct{})
	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		response, err := transport.RoundTrip(req)
		if err == nil {
			body := response.Body
			first := true
			response.Body = struct {
				io.Reader
				io.Closer
			}{readFunc(func(p []byte) (int, error) {
				if first {
					first = false
					close(started)
				}
				return body.Read(p)
			}), body}
		}
		return response, err
	})}
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "x")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}, WithHTTPClient(httpClient))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		response, err := client.DeleteRepository(ctx, "org", "repo", nil)
		if response == nil {
			t.Error("missing response metadata")
		}
		finished <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("response read did not stop")
	}
}

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func (b *trackedBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func (b *trackedBody) Close() error { b.closed = true; return nil }

func (b *countedBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += n
	return n, err
}

func (b *countedBody) Close() error { b.closed = true; return nil }

func (f readFunc) Read(p []byte) (int, error) { return f(p) }

func testClient(t *testing.T, handler http.HandlerFunc, options ...Option) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	options = append([]Option{WithBaseURL(server.URL)}, options...)
	client, err := NewClient("test-token", options...)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func assertJSON(t *testing.T, actual []byte, expected string) {
	t.Helper()
	var got, want any
	if !json.Valid(actual) || !json.Valid([]byte(expected)) {
		t.Fatalf("invalid JSON: actual=%s expected=%s", actual, expected)
	}
	actualDecoder := json.NewDecoder(bytes.NewReader(actual))
	actualDecoder.UseNumber()
	if err := actualDecoder.Decode(&got); err != nil {
		t.Fatal(err)
	}
	expectedDecoder := json.NewDecoder(strings.NewReader(expected))
	expectedDecoder.UseNumber()
	if err := expectedDecoder.Decode(&want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("JSON = %s, want %s", actual, expected)
	}
}
