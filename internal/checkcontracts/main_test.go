package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixture = `{"swagger":"2.0","paths":{"/items":{"get":{"responses":{"200":{"schema":{"$ref":"#/definitions/Item"}}}}}},"definitions":{"Item":{"type":"object","properties":{"id":{"type":"integer","example":9007199254740993}}}}}`

type roundTripFunc func(*http.Request) (*http.Response, error)

type failingWriter struct{ err error }

func TestFailedRecheckRemovesPreviousSnapshots(t *testing.T) {
	base, out := t.TempDir(), t.TempDir()
	c := contract{"platform", "platform.json", "https://example.test/schema"}
	path := writeFixtureManifest(t, base, []contract{c})
	failed := false
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		status, body := http.StatusOK, fixture
		if failed {
			status, body = http.StatusServiceUnavailable, "unavailable"
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	if err := run(t.Context(), path, out, client, io.Discard); err != nil {
		t.Fatal(err)
	}
	failed = true
	if err := run(t.Context(), path, out, client, io.Discard); err == nil {
		t.Fatal("failed download accepted")
	}
	for _, suffix := range []string{".diff", ".local.json", ".published.json"} {
		if _, err := os.Stat(filepath.Join(out, c.Name+suffix)); !os.IsNotExist(err) {
			t.Errorf("previous report artifact %s remains after failed recheck: %v", suffix, err)
		}
	}
}

func TestReportDirectoryCannotBeSnapshotDirectory(t *testing.T) {
	base := t.TempDir()
	path := writeFixtureManifest(t, base, []contract{{"platform", "platform.local.json", "https://example.test/schema"}})
	if err := run(t.Context(), path, base, http.DefaultClient, io.Discard); err == nil {
		t.Fatal("report directory accepted the snapshot directory")
	}
	data, err := os.ReadFile(filepath.Join(base, "platform.local.json"))
	if err != nil || string(data) != fixture {
		t.Fatalf("input snapshot was modified: %v", err)
	}
}

func TestCanonicalPreservesEveryContractFieldAndNumber(t *testing.T) {
	base, err := canonicalizeSpec([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(fixture), &object); err != nil {
		t.Fatal(err)
	}
	reformatted, err := json.MarshalIndent(object, "", "    ")
	if err != nil {
		t.Fatal(err)
	}
	got, err := canonicalizeSpec(reformatted)
	if err != nil || !bytes.Equal(got, base) {
		t.Fatalf("formatting changed comparison: %v", err)
	}
	for _, change := range []struct{ old, new string }{
		{`9007199254740993`, `9007199254740992`},
		{`"integer"`, `"string"`},
		{`"200"`, `"201"`},
		{`"get"`, `"post"`},
		{`"2.0"`, `"3.0"`},
		{`"example":9007199254740993`, `"example":1.0000000000000001`},
	} {
		changed, err := canonicalizeSpec([]byte(strings.Replace(fixture, change.old, change.new, 1)))
		if err != nil || bytes.Equal(changed, base) {
			t.Errorf("contract change %q -> %q was missed: %v", change.old, change.new, err)
		}
	}
	for _, bad := range []string{`null`, `[]`, `{}`, `{"swagger":"2.0","paths":null}`, `{"swagger":"","paths":{}}`, fixture + fixture, `<html>upstream error</html>`} {
		if _, err := canonicalizeSpec([]byte(bad)); err == nil {
			t.Errorf("invalid specification accepted: %s", bad)
		}
	}
}

func TestRunChecksAllContractsAndReportsDrift(t *testing.T) {
	changed := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/json" {
			t.Error("missing JSON negotiation")
		}
		data := fixture
		if r.URL.Path == "/appsec" && changed {
			data = strings.Replace(data, `"integer"`, `"string"`, 1)
		}
		_, _ = w.Write([]byte(data))
	}))
	defer server.Close()
	base, out := t.TempDir(), t.TempDir()
	path := writeFixtureManifest(t, base, []contract{{"platform", "platform.json", server.URL + "/platform"}, {"appsec", "appsec.json", server.URL + "/appsec"}})
	var log bytes.Buffer
	if err := run(t.Context(), path, out, server.Client(), &log); err == nil {
		t.Fatal("schema drift did not fail the check")
	}
	if !strings.Contains(log.String(), "platform: unchanged") || !strings.Contains(log.String(), "appsec: CHANGED") {
		t.Fatalf("incomplete report: %s", &log)
	}
	diff, err := os.ReadFile(filepath.Join(out, "appsec.diff"))
	if err != nil || !bytes.Contains(diff, []byte(`"type": "string"`)) {
		t.Fatalf("missing model diff: %v\n%s", err, diff)
	}
	for _, name := range []string{"platform.local.json", "platform.published.json", "appsec.local.json", "appsec.published.json", "summary.md"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Errorf("missing report file %s: %v", name, err)
		}
	}
	for _, name := range []string{"platform.json", "appsec.json"} {
		data, err := os.ReadFile(filepath.Join(base, name))
		if err != nil || string(data) != fixture {
			t.Fatalf("local snapshot modified: %s %v", name, err)
		}
	}
	changed = false
	log.Reset()
	if err := run(t.Context(), path, out, server.Client(), &log); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "appsec.diff")); !os.IsNotExist(err) {
		t.Fatalf("stale diff remains after a successful recheck: %v", err)
	}
}

func TestFetchFailureDoesNotSkipOtherContracts(t *testing.T) {
	for _, mode := range []string{"http_error", "invalid_json", "missing_paths"} {
		t.Run(mode, func(t *testing.T) {
			secondChecked := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/first" {
					switch mode {
					case "http_error":
						w.WriteHeader(http.StatusServiceUnavailable)
					case "invalid_json":
						_, _ = w.Write([]byte(`not JSON`))
					case "missing_paths":
						_, _ = w.Write([]byte(`{"message":"error"}`))
					}
					return
				}
				secondChecked = true
				_, _ = w.Write([]byte(strings.Replace(fixture, `9007199254740993`, `9007199254740992`, 1)))
			}))
			defer server.Close()
			path := writeFixtureManifest(t, t.TempDir(), []contract{{"first", "first.json", server.URL + "/first"}, {"second", "second.json", server.URL + "/second"}})
			var log bytes.Buffer
			if err := run(t.Context(), path, t.TempDir(), server.Client(), &log); err == nil {
				t.Fatal("fetch failure was ignored")
			}
			if !secondChecked || !strings.Contains(log.String(), "first: ERROR") || !strings.Contains(log.String(), "second: CHANGED") {
				t.Fatalf("check stopped early: %s", &log)
			}
		})
	}
}

func TestManifestRequiresAllSnapshots(t *testing.T) {
	base := t.TempDir()
	path := writeFixtureManifest(t, base, []contract{{"platform", "platform.json", "https://example.test/schema"}})
	if _, err := readManifest(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "another.openapi.json"), []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readManifest(path); err == nil || !strings.Contains(err.Error(), "missing from the contract manifest") {
		t.Fatalf("unregistered contract accepted: %v", err)
	}
}

func TestProjectManifestCoversEveryPinnedContract(t *testing.T) {
	contracts, err := readManifest("../../api/contracts.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range contracts {
		data, err := os.ReadFile(filepath.Join("../../api", c.Snapshot))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := canonicalizeSpec(data); err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
	}
}

func TestFetchHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := fetch(ctx, http.DefaultClient, "https://example.test/schema"); err == nil {
		t.Fatal("cancelled request succeeded")
	}
}

func TestRunReturnsOutputFailure(t *testing.T) {
	writeErr := errors.New("output unavailable")
	for _, changed := range []bool{false, true} {
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			body := fixture
			if changed {
				body = strings.Replace(body, `"integer"`, `"string"`, 1)
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})}
		path := writeFixtureManifest(t, t.TempDir(), []contract{{"platform", "platform.json", "https://example.test/schema"}})
		out := t.TempDir()
		err := run(t.Context(), path, out, client, failingWriter{writeErr})
		if !errors.Is(err, writeErr) {
			t.Fatalf("output failure was lost (changed=%v): %v", changed, err)
		}
		if changed && !strings.Contains(err.Error(), "1 changed") {
			t.Fatalf("contract drift error was lost: %v", err)
		}
		if _, err := os.Stat(filepath.Join(out, "summary.md")); err != nil {
			t.Fatalf("report was not saved: %v", err)
		}
	}
}

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func writeFixtureManifest(t *testing.T, base string, contracts []contract) string {
	t.Helper()
	data, err := json.Marshal(manifest{Contracts: contracts})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "contracts.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range contracts {
		if err := os.WriteFile(filepath.Join(base, c.Snapshot), []byte(fixture), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return path
}
