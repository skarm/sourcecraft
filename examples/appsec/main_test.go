package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/skarm/sourcecraft"
)

type transportFunc func(*http.Request) (*http.Response, error)

type failingWriter struct{ err error }

func TestRunPrintsAllPages(t *testing.T) {
	t.Setenv("SOURCECRAFT_TOKEN", "test-token")
	calls := 0
	setTransport(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/defect-groups" || r.Header.Get("Authorization") != "Bearer test-token" || r.URL.Query().Get("gitRepo") != "repository-id" || r.URL.Query().Get("pageSize") != "100" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
		}
		calls++
		if calls == 1 {
			if r.URL.Query().Get("pageToken") != "" {
				t.Fatalf("unexpected first-page token: %s", r.URL)
			}
			return reply(200, `{"data":[{"uuid":"first","ruleName":"rule","fileName":"first.go"}],"nextPageToken":"next"}`), nil
		}
		if calls != 2 || r.URL.Query().Get("pageToken") != "next" {
			t.Fatalf("unexpected page request: %s", r.URL)
		}
		return reply(200, `{"data":[{"uuid":"second","ruleName":"rule","fileName":"second.go"}],"nextPageToken":""}`), nil
	})
	var out bytes.Buffer
	if err := run([]string{"-repository", "repository-id"}, &out); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || out.String() != "first\trule\tfirst.go\nsecond\trule\tsecond.go\n" {
		t.Fatalf("calls=%d output=%q", calls, out.String())
	}
}

func TestRunStopsOnOutputFailure(t *testing.T) {
	t.Setenv("SOURCECRAFT_TOKEN", "test-token")
	calls := 0
	setTransport(t, func(r *http.Request) (*http.Response, error) {
		calls++
		return reply(200, `{"data":[{"uuid":"first"},{"uuid":"second"}],"nextPageToken":"next"}`), nil
	})
	writeErr := errors.New("output unavailable")
	if err := run([]string{"-repository", "repository-id"}, failingWriter{writeErr}); !errors.Is(err, writeErr) {
		t.Fatalf("output error was lost: %v", err)
	}
	if calls != 1 {
		t.Fatalf("requested pages after output failure: %d", calls)
	}
}

func TestRunValidatesArgumentsBeforeRequests(t *testing.T) {
	t.Setenv("SOURCECRAFT_TOKEN", "test-token")
	setTransport(t, func(r *http.Request) (*http.Response, error) {
		t.Fatal("made a request with invalid arguments")
		return nil, nil
	})
	for _, args := range [][]string{nil, {"-repository", " "}, {"-unknown"}, {"-repository", "repo", "unexpected"}} {
		if err := run(args, io.Discard); err == nil {
			t.Errorf("accepted arguments: %v", args)
		}
	}
	if err := run([]string{"-h"}, io.Discard); err != nil {
		t.Fatalf("help failed: %v", err)
	}
	t.Setenv("SOURCECRAFT_TOKEN", "")
	if err := run([]string{"-repository", "repo"}, io.Discard); err == nil {
		t.Fatal("accepted missing token")
	}
}

func TestRunReturnsPageFailure(t *testing.T) {
	t.Setenv("SOURCECRAFT_TOKEN", "test-token")
	wantErr := errors.New("connection failed")
	setTransport(t, func(r *http.Request) (*http.Response, error) {
		return nil, wantErr
	})
	if err := run([]string{"-repository", "repo"}, io.Discard); !errors.Is(err, wantErr) {
		t.Fatalf("page error was lost: %v", err)
	}
}

func TestRunReturnsAPIError(t *testing.T) {
	t.Setenv("SOURCECRAFT_TOKEN", "test-token")
	setTransport(t, func(r *http.Request) (*http.Response, error) {
		return reply(http.StatusForbidden, `{"message":"access denied"}`), nil
	})
	err := run([]string{"-repository", "repo"}, io.Discard)
	apiErr, ok := errors.AsType[*sourcecraft.APIError](err)
	if !ok || apiErr.StatusCode != http.StatusForbidden {
		t.Fatalf("API error was lost: %v", err)
	}
}

func TestRunBoundsEntirePagination(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		t.Setenv("SOURCECRAFT_TOKEN", "test-token")
		calls := 0
		setTransport(t, func(r *http.Request) (*http.Response, error) {
			calls++
			select {
			case <-r.Context().Done():
				return nil, r.Context().Err()
			case <-time.After(25 * time.Second):
				return reply(200, `{"nextPageToken":"`+strconv.Itoa(calls)+`"}`), nil
			}
		})
		start := time.Now()
		if err := run([]string{"-repository", "repo"}, io.Discard); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("pagination deadline was lost: %v", err)
		}
		if calls != 3 || time.Since(start) != time.Minute {
			t.Fatalf("pagination was not bounded: calls=%d elapsed=%s", calls, time.Since(start))
		}
	})
}

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func setTransport(t *testing.T, handler transportFunc) {
	t.Helper()
	original := http.DefaultTransport
	http.DefaultTransport = handler
	t.Cleanup(func() { http.DefaultTransport = original })
}

func reply(status int, data string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(data))}
}
