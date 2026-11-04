//nolint:testpackage // These tests share white-box helpers for private SDK behavior.
package sourcecraft

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func BenchmarkClient(b *testing.B) {
	for _, size := range []int{256, 32 << 10, 2 << 20} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			data, err := json.Marshal(GetCubeLogsResponse{Logs: strings.Repeat("x", size), Done: true, PageComplete: true})
			if err != nil {
				b.Fatal(err)
			}
			client, err := NewClient("token", WithHTTPClient(&http.Client{
				Timeout: 30 * time.Second,
				Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), ContentLength: int64(len(data)), Body: io.NopCloser(bytes.NewReader(data))}, nil
				}),
			}))
			if err != nil {
				b.Fatal(err)
			}
			ctx := context.Background()
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				logs, _, err := client.GetCubeLogs(ctx, "org", "repo", "run", "workflow", "task", "cube", nil)
				if err != nil || len(logs.Logs) != size {
					b.Fatalf("logs: %v", err)
				}
			}
		})
	}
}

func BenchmarkHTTP(b *testing.B) {
	data := []byte(`{"id":"repo","slug":"demo","description":"repository","counters":{"forks":"12"}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	}))
	b.Cleanup(server.Close)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 64
	b.Cleanup(transport.CloseIdleConnections)
	client, err := NewClient("token", WithBaseURL(server.URL), WithHTTPClient(&http.Client{Transport: transport, Timeout: 30 * time.Second}))
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			repo, _, err := client.GetRepository(ctx, "org", "repo")
			if err != nil || repo.Slug != "demo" {
				b.Errorf("repository: %v", err)
				return
			}
		}
	})
}

func BenchmarkDiscard(b *testing.B) {
	for _, size := range []int{0, 32 << 10, 2 << 20} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			data := make([]byte, size)
			client, err := NewClient("token", WithHTTPClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), ContentLength: int64(size), Body: io.NopCloser(bytes.NewReader(data))}, nil
			})}))
			if err != nil {
				b.Fatal(err)
			}
			ctx := context.Background()
			b.ReportAllocs()
			for b.Loop() {
				if _, err := client.DeleteRepository(ctx, "org", "repo", nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkNoBody(b *testing.B) {
	client, err := NewClient("token", WithHTTPClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: http.NoBody}, nil
	})}))
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := client.DeleteRepository(ctx, "org", "repo", nil); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUpload(b *testing.B) {
	for _, size := range []int{8 << 10, 8 << 20} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			data := make([]byte, size)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, err := io.Copy(io.Discard, r.Body); err != nil {
					b.Error(err)
				}
				_, _ = io.WriteString(w, `{"id":"attachment"}`)
			}))
			b.Cleanup(server.Close)
			transport := http.DefaultTransport.(*http.Transport).Clone()
			b.Cleanup(transport.CloseIdleConnections)
			client, err := NewClient("token", WithBaseURL(server.URL), WithHTTPClient(&http.Client{Transport: transport}))
			if err != nil {
				b.Fatal(err)
			}
			ctx := context.Background()
			b.ReportAllocs()
			b.SetBytes(int64(size))
			for b.Loop() {
				attachment, _, err := client.UploadIssueAttachment(ctx, "org", "repo", "1", &Upload{Filename: "file.bin", Reader: bytes.NewReader(data)})
				if err != nil || attachment.ID != "attachment" {
					b.Fatalf("upload: %v", err)
				}
			}
		})
	}
}

func BenchmarkMultipart(b *testing.B) {
	for _, size := range []int{8 << 10, 8 << 20} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			data := make([]byte, size)
			b.ReportAllocs()
			for b.Loop() {
				body, _, err := multipartBody(&Upload{Filename: "file.bin", Reader: bytes.NewReader(data)}, []formField{{"name", "file"}})
				if err != nil {
					b.Fatal(err)
				}
				if _, err := io.Copy(io.Discard, body); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkPaginate(b *testing.B) {
	for _, pages := range []int{10, 10000} {
		b.Run(strconv.Itoa(pages), func(b *testing.B) {
			tokens := make([]string, pages)
			index := make(map[string]int, pages)
			for i := 1; i < pages; i++ {
				tokens[i] = strconv.Itoa(i)
				index[tokens[i]] = i
			}
			items := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
			fetch := func(_ context.Context, token string) ([]int, string, error) {
				next := ""
				if i := index[token] + 1; i < pages {
					next = tokens[i]
				}
				return items, next, nil
			}
			ctx := context.Background()
			b.ReportAllocs()
			for b.Loop() {
				count := 0
				for _, err := range Paginate(ctx, fetch) {
					if err != nil {
						b.Fatal(err)
					}
					count++
				}
				if count != pages*len(items) {
					b.Fatal(count)
				}
			}
		})
	}
}

func BenchmarkWait(b *testing.B) {
	for _, polls := range []int{1, 100} {
		b.Run(strconv.Itoa(polls), func(b *testing.B) {
			ctx := context.Background()
			b.ReportAllocs()
			for b.Loop() {
				calls := 0
				_, _, err := waitOperation(ctx, "job", time.Nanosecond,
					func(context.Context) (*MergePullRequestOperation, *Response, error) {
						calls++
						status := OperationStatusInProgress
						if calls == polls {
							status = OperationStatusSuccess
						}
						return &MergePullRequestOperation{Status: status}, nil, nil
					},
					func(op *MergePullRequestOperation) (OperationStatus, *OperationError) { return op.Status, op.Error })
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
