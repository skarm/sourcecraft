//nolint:testpackage // These tests share white-box helpers for private SDK behavior.
package sourcecraft

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestResponseLengthHintsPreserveReadContract(t *testing.T) {
	data, err := json.Marshal(Repository{Description: strings.Repeat("x", 32<<10)})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		hint    int64
		limit   int64
		status  int
		data    []byte
		readErr error
		want    string
	}{
		{"known", int64(len(data)), int64(len(data)), 200, data, nil, ""},
		{"unknown", -1, int64(len(data)), 200, data, nil, ""},
		{"understated", 1024, int64(len(data)), 200, data, nil, ""},
		{"overstated", 256 << 10, int64(len(data)), 200, data, nil, ""},
		{"large_hint", 32 << 20, 32 << 20, 200, data, nil, ""},
		{"over_limit", int64(len(data)), 4096, 200, data, nil, "response exceeds"},
		{"read_error", int64(len(data)), int64(len(data)), 200, data, io.ErrUnexpectedEOF, "read response"},
		{"read_error_before_limit", int64(len(data)), 4096, 200, data, io.ErrUnexpectedEOF, "read response"},
		{"api_error", int64(len(data)), int64(len(data)), 400, data, nil, "api_error"},
		{"empty", 32768, 32768, 200, nil, nil, "decode response"},
		{"whitespace", 32768, 32768, 200, bytes.Repeat([]byte(" "), 8192), nil, "decode response"},
		{"null", 8196, 32768, 200, []byte(strings.Repeat(" ", 8192) + "null"), nil, "received null"},
		{"malformed", 8193, 32768, 200, []byte(strings.Repeat(" ", 8192) + "{"), nil, "decode response"},
		{"multiple_objects", 8197, 32768, 200, []byte(strings.Repeat(" ", 8192) + "{} {}"), nil, "decode response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reader io.Reader = bytes.NewReader(tc.data)
			if tc.readErr != nil {
				reader = readFunc(func(p []byte) (int, error) { return copy(p, tc.data), tc.readErr })
			}
			body := &countedBody{Reader: reader}
			client, err := NewClient("", WithMaxResponseBytes(tc.limit), WithHTTPClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, ContentLength: tc.hint, Header: http.Header{"X-Request-Id": {"read-contract"}}, Body: body}, nil
			})}))
			if err != nil {
				t.Fatal(err)
			}
			repo, response, err := client.GetRepository(t.Context(), "org", "repo")
			if response == nil || response.StatusCode != tc.status || !body.closed || int64(body.read) > tc.limit+1 {
				t.Fatalf("metadata=%v closed=%v bytes=%d", response, body.closed, body.read)
			}
			switch tc.want {
			case "":
				if err != nil || repo == nil || len(repo.Description) != 32<<10 {
					t.Fatalf("result=%v error=%v", repo, err)
				}
			case "api_error":
				apiErr, ok := errors.AsType[*APIError](err)
				if !ok || !bytes.Equal(apiErr.Body, tc.data) || apiErr.RequestID != "read-contract" {
					t.Fatalf("error body or metadata changed: %v", err)
				}
			default:
				if err == nil || repo != nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("result=%v error=%v; want %s", repo, err, tc.want)
				}
				if tc.readErr != nil && !errors.Is(err, tc.readErr) {
					t.Fatalf("read error was not preserved: %v", err)
				}
			}
		})
	}
}

func TestJSONResponseEncodings(t *testing.T) {
	plain, err := json.Marshal(Repository{Description: strings.Repeat("x", 32<<10)})
	if err != nil {
		t.Fatal(err)
	}
	for _, compressed := range []bool{false, true} {
		for _, chunked := range []bool{false, true} {
			t.Run("gzip="+strconv.FormatBool(compressed)+"/chunked="+strconv.FormatBool(chunked), func(t *testing.T) {
				data := plain
				if compressed {
					var buffer bytes.Buffer
					writer := gzip.NewWriter(&buffer)
					if _, err := writer.Write(plain); err != nil {
						t.Fatal(err)
					}
					if err := writer.Close(); err != nil {
						t.Fatal(err)
					}
					data = buffer.Bytes()
				}
				client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
					if compressed {
						w.Header().Set("Content-Encoding", "gzip")
					}
					if chunked {
						w.Header().Set("Trailer", "X-End")
						w.(http.Flusher).Flush()
					} else {
						w.Header().Set("Content-Length", strconv.Itoa(len(data)))
					}
					_, _ = w.Write(data)
					if chunked {
						w.Header().Set("X-End", "done")
					}
				}, WithMaxResponseBytes(int64(len(plain))))
				repo, response, err := client.GetRepository(t.Context(), "org", "repo")
				if err != nil || repo == nil || len(repo.Description) != 32<<10 || response == nil {
					t.Fatalf("result=%v response=%v error=%v", repo, response, err)
				}
				if chunked && response.Trailer.Get("X-End") != "done" {
					t.Fatal("response trailers were not consumed")
				}
			})
		}
	}
}

func TestJSONReadCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	client, err := NewClient("", WithHTTPClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, ContentLength: 32768, Header: make(http.Header), Body: io.NopCloser(readFunc(func([]byte) (int, error) {
			cancel()
			return 0, ctx.Err()
		}))}, nil
	})}))
	if err != nil {
		t.Fatal(err)
	}
	if _, response, err := client.GetRepository(ctx, "org", "repo"); response == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("response=%v error=%v", response, err)
	}
}
