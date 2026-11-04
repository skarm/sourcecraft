package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/skarm/sourcecraft"
)

type transportFunc func(*http.Request) (*http.Response, error)

type failingWriter struct {
	err       error
	remaining int
}

func TestWaitForRunIncludesPreparationAndApproval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		states := []sourcecraft.RunStatus{sourcecraft.RunStatusPrepared, sourcecraft.RunStatusProcessing, sourcecraft.RunStatusAwaitingApproval, sourcecraft.RunStatusFailed}
		calls := 0
		client := testClient(t, func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodGet || r.URL.Path != "/repos/org/repo/cicd/runs/42" {
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
			}
			if calls == len(states) {
				t.Fatal("polled after terminal status")
			}
			state := states[calls]
			calls++
			return reply(t, 200, &sourcecraft.Run{Slug: "42", Status: state}), nil
		})
		start := time.Now()
		var out bytes.Buffer
		run, err := waitForRun(t.Context(), client, "org", "repo", &sourcecraft.Run{Slug: "42", Status: sourcecraft.RunStatusCreated}, time.Second, &out)
		if err != nil || run == nil || run.Status != sourcecraft.RunStatusFailed || calls != 4 {
			t.Fatalf("run=%+v calls=%d err=%v", run, calls, err)
		}
		if time.Since(start) != 4*time.Second || !strings.Contains(out.String(), "awaiting_approval") {
			t.Fatalf("polling or status reporting failed: %s", &out)
		}
	})
}

func TestWaitForRunDeadlineAndUnknownState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := testClient(t, func(r *http.Request) (*http.Response, error) {
			return reply(t, 200, &sourcecraft.Run{Slug: "42", Status: sourcecraft.RunStatusAwaitingApproval}), nil
		})
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		_, err := waitForRun(ctx, client, "org", "repo", &sourcecraft.Run{Slug: "42", Status: sourcecraft.RunStatusCreated}, time.Second, io.Discard)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("deadline error: %v", err)
		}
	})
	client := testClient(t, func(r *http.Request) (*http.Response, error) {
		t.Fatal("unknown status should not be polled")
		return nil, nil
	})
	if _, err := waitForRun(t.Context(), client, "org", "repo", &sourcecraft.Run{Slug: "42", Status: "future"}, time.Second, io.Discard); err == nil {
		t.Fatal("unknown state accepted")
	}
}

func TestCubeLogsReadAllPagesAfterDone(t *testing.T) {
	for _, fullFinalPage := range []bool{false, true} {
		t.Run(strconv.FormatBool(fullFinalPage), func(t *testing.T) {
			var pages []string
			client := testClient(t, func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodGet || r.URL.Path != "/repos/org/repo/cicd/logs/42/build/test/go" {
					t.Fatalf("unexpected log request %s %s", r.Method, r.URL)
				}
				page := r.URL.Query().Get("page")
				pages = append(pages, page)
				switch page {
				case "1":
					return reply(t, 200, &sourcecraft.GetCubeLogsResponse{Logs: "first\n", Done: true, PageComplete: true}), nil
				case "2":
					return reply(t, 200, &sourcecraft.GetCubeLogsResponse{Logs: "second\n", Done: true, PageComplete: fullFinalPage}), nil
				case "3":
					return reply(t, 404, map[string]string{"message": "no next page"}), nil
				default:
					t.Fatalf("unexpected page %s", page)
					return nil, nil
				}
			})
			var out bytes.Buffer
			if err := printCubeLogs(t.Context(), client, "org", "repo", "42", "build", "test", "go", &out); err != nil {
				t.Fatal(err)
			}
			want := []string{"1", "2"}
			if fullFinalPage {
				want = append(want, "3")
			}
			if out.String() != "first\nsecond\n" || !reflect.DeepEqual(pages, want) {
				t.Fatalf("output=%q pages=%v", out.String(), pages)
			}
		})
	}
}

func TestCubeLogsDoNotHideMissingOrUnfinishedOutput(t *testing.T) {
	for _, mode := range []string{"missing_first_page", "unfinished", "missing_after_active_page", "forbidden"} {
		t.Run(mode, func(t *testing.T) {
			client := testClient(t, func(r *http.Request) (*http.Response, error) {
				if mode == "unfinished" {
					return reply(t, 200, &sourcecraft.GetCubeLogsResponse{Logs: "partial", Done: false, PageComplete: false}), nil
				}
				if mode == "missing_after_active_page" && r.URL.Query().Get("page") == "1" {
					return reply(t, 200, &sourcecraft.GetCubeLogsResponse{Logs: "partial", PageComplete: true, Done: false}), nil
				}
				status := 404
				if mode == "forbidden" {
					status = 403
				}
				return reply(t, status, map[string]string{"message": "unavailable"}), nil
			})
			if err := printCubeLogs(t.Context(), client, "org", "repo", "42", "build", "test", "go", io.Discard); err == nil {
				t.Fatal("unavailable output was treated as complete")
			}
		})
	}
}

func TestResultsContinueAfterLogFailure(t *testing.T) {
	var requested []string
	client := testClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet {
			t.Fatalf("result retrieval must not start a run: %s", r.Method)
		}
		requested = append(requested, r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "/42/build"):
			return reply(t, 200, &sourcecraft.Workflow{Slug: "build", Status: sourcecraft.RunStatusFailed, Tasks: []sourcecraft.Task{{Slug: "test", Status: sourcecraft.RunStatusFailed, Cubes: []sourcecraft.Cube{
				{Slug: "bad", Status: sourcecraft.RunStatusFailed}, {Slug: "good", Status: sourcecraft.RunStatusSuccess}, {Slug: "skipped", Status: sourcecraft.RunStatusSkipped},
			}}}}), nil
		case strings.Contains(r.URL.Path, "/logs/"):
			if strings.HasSuffix(r.URL.Path, "/bad") {
				return reply(t, 403, map[string]string{"message": "logs unavailable"}), nil
			}
			return reply(t, 200, &sourcecraft.GetCubeLogsResponse{Logs: "good logs", Done: true}), nil
		case strings.Contains(r.URL.Path, "/artifacts/"):
			return reply(t, 200, &sourcecraft.GetCubeArtifactsResponse{Artifacts: []sourcecraft.Artifact{{LocalPath: "report.txt", Status: sourcecraft.ArtifactStatusSuccess, DownloadURL: "https://downloads.example.test/report"}}}), nil
		default:
			t.Fatalf("unexpected request: %s", r.URL)
			return nil, nil
		}
	})
	var out bytes.Buffer
	err := printResults(t.Context(), client, "org", "repo", &sourcecraft.Run{Slug: "42", Status: sourcecraft.RunStatusFailed, Workflows: []sourcecraft.Workflow{{Slug: "build"}}}, &out)
	if err == nil || len(requested) != 5 || !strings.Contains(out.String(), "good logs") || strings.Count(out.String(), "https://downloads.example.test/report") != 2 {
		t.Fatalf("requests=%v output=%s error=%v", requested, &out, err)
	}
}

func TestInputsKeepEmptyValuesAndEquals(t *testing.T) {
	var values inputs
	for _, text := range []string{"empty=", "expression=a=b"} {
		if err := values.Set(text); err != nil {
			t.Fatal(err)
		}
	}
	if *values[0].Value != "" || *values[1].Value != "a=b" {
		t.Fatalf("input values changed: %s", &values)
	}
	for _, text := range []string{"=missing-name", "missing-equals"} {
		if err := values.Set(text); err == nil {
			t.Errorf("invalid input %q accepted", text)
		}
	}
}

func TestOutputFailuresAreReturned(t *testing.T) {
	writeErr := errors.New("output unavailable")
	client := testClient(t, func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, "/logs/"):
			return reply(t, 200, &sourcecraft.GetCubeLogsResponse{Logs: "logs", Done: true}), nil
		case strings.Contains(r.URL.Path, "/artifacts/"):
			return reply(t, 200, &sourcecraft.GetCubeArtifactsResponse{Artifacts: []sourcecraft.Artifact{{LocalPath: "report.txt"}}}), nil
		default:
			return reply(t, 200, &sourcecraft.Workflow{Slug: "build", Tasks: []sourcecraft.Task{{Slug: "test", Cubes: []sourcecraft.Cube{{Slug: "go", Status: sourcecraft.RunStatusSuccess}}}}}), nil
		}
	})
	t.Run("status", func(t *testing.T) {
		if _, err := waitForRun(t.Context(), client, "org", "repo", &sourcecraft.Run{Slug: "42", Status: sourcecraft.RunStatusSuccess}, time.Second, &failingWriter{err: writeErr}); !errors.Is(err, writeErr) {
			t.Fatalf("status output error was lost: %v", err)
		}
	})
	t.Run("run_error", func(t *testing.T) {
		if err := printResults(t.Context(), client, "org", "repo", &sourcecraft.Run{Slug: "42", ErrorMessages: []string{"run failed"}}, &failingWriter{err: writeErr}); !errors.Is(err, writeErr) {
			t.Fatalf("run error output was lost: %v", err)
		}
	})
	for successfulWrites, label := range []string{"workflow", "task", "cube", "logs", "newline", "artifact"} {
		t.Run(label, func(t *testing.T) {
			writer := &failingWriter{err: writeErr, remaining: successfulWrites}
			if err := printResults(t.Context(), client, "org", "repo", &sourcecraft.Run{Slug: "42", Workflows: []sourcecraft.Workflow{{Slug: "build"}}}, writer); !errors.Is(err, writeErr) {
				t.Fatalf("result output error was lost: %v", err)
			}
		})
	}
}

func TestRunReturnsIDOutputFailure(t *testing.T) {
	t.Setenv("SOURCECRAFT_TOKEN", "test-token")
	original := http.DefaultTransport
	http.DefaultTransport = transportFunc(func(r *http.Request) (*http.Response, error) {
		return reply(t, 200, &sourcecraft.Run{Slug: "42", Status: sourcecraft.RunStatusSuccess}), nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	writeErr := errors.New("output unavailable")
	err := run([]string{"-org", "org", "-repo", "repo", "-workflow", "build"}, &failingWriter{err: writeErr})
	if !errors.Is(err, writeErr) || !strings.Contains(err.Error(), "42") {
		t.Fatalf("lost output error or accepted run ID: %v", err)
	}
}

func TestResultOutputFailurePreservesEarlierFetchError(t *testing.T) {
	client := testClient(t, func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/bad") {
			return reply(t, 403, map[string]string{"message": "workflow unavailable"}), nil
		}
		return reply(t, 200, &sourcecraft.Workflow{Slug: "good"}), nil
	})
	writeErr := errors.New("output unavailable")
	err := printResults(t.Context(), client, "org", "repo", &sourcecraft.Run{Slug: "42", Workflows: []sourcecraft.Workflow{{Slug: "bad"}, {Slug: "good"}}}, &failingWriter{err: writeErr})
	apiErr, ok := errors.AsType[*sourcecraft.APIError](err)
	if !errors.Is(err, writeErr) || !ok || apiErr.StatusCode != 403 {
		t.Fatalf("lost output error or earlier fetch error: %v", err)
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

func testClient(t *testing.T, handler transportFunc) *sourcecraft.Client {
	t.Helper()
	client, err := sourcecraft.NewClient("test-token", sourcecraft.WithHTTPClient(&http.Client{Transport: handler}))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func reply(t *testing.T, status int, value any) *http.Response {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data))}
}
