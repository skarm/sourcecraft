package main

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

type failingWriter struct {
	err       error
	remaining int
}

func TestRunReturnsOutputFailures(t *testing.T) {
	t.Setenv("SOURCECRAFT_TOKEN", "test-token")
	original := http.DefaultTransport
	http.DefaultTransport = transportFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"slug":"42","operation_id":"merge-42","status":"success","response":{}}`)), Header: make(http.Header)}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	writeErr := errors.New("output unavailable")
	for _, tc := range []struct {
		name      string
		args      []string
		remaining int
		wantID    string
	}{
		{"help", []string{"-h"}, 0, ""},
		{"create", []string{"create", "-org", "org", "-repo", "repo", "-source", "feature", "-target", "main", "-title", "test"}, 0, "42"},
		{"merge_id", []string{"merge", "-org", "org", "-repo", "repo", "-pr", "42"}, 0, "merge-42"},
		{"merge_result", []string{"merge", "-org", "org", "-repo", "repo", "-pr", "42"}, 1, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := run(tc.args, &failingWriter{writeErr, tc.remaining})
			if !errors.Is(err, writeErr) {
				t.Fatalf("output error was lost: %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantID) {
				t.Fatalf("lost accepted operation ID %s: %v", tc.wantID, err)
			}
		})
	}
}

func (w *failingWriter) Write(p []byte) (int, error) {
	if w.remaining == 0 {
		w.remaining--
		return 0, w.err
	}
	w.remaining--
	return len(p), nil
}

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
