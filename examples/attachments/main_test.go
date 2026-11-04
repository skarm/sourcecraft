package main

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
	file := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(file, []byte("report"), 0o600); err != nil {
		t.Fatal(err)
	}
	original := http.DefaultTransport
	http.DefaultTransport = transportFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":"attachment-42","url":"https://example.test/download"}`)), Header: make(http.Header)}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	writeErr := errors.New("output unavailable")
	for successfulWrites, label := range []string{"metadata", "download_url"} {
		t.Run(label, func(t *testing.T) {
			err := run([]string{"-org", "org", "-repo", "repo", "-issue", "1", "-file", file}, &failingWriter{writeErr, successfulWrites})
			if !errors.Is(err, writeErr) {
				t.Fatalf("output failure after %d writes was lost: %v", successfulWrites, err)
			}
			if successfulWrites == 0 && !strings.Contains(err.Error(), "attachment-42") {
				t.Fatalf("lost uploaded attachment ID: %v", err)
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
