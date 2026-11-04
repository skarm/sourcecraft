// Command checkcontracts compares every pinned API contract with its published
// specification and saves canonical JSON, diffs and a summary outside the SDK.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const maxSpecBytes = 32 << 20

type contract struct {
	Name     string `json:"name"`
	Snapshot string `json:"snapshot"`
	URL      string `json:"url"`
}

type manifest struct {
	Contracts []contract `json:"contracts"`
}

func main() {
	manifestPath := flag.String("manifest", "api/contracts.json", "manifest of all pinned API contracts")
	outDir := flag.String("out", "", "report directory (defaults to a new temporary directory)")

	flag.Parse()

	if err := run(context.Background(), *manifestPath, *outDir, &http.Client{Timeout: 30 * time.Second}, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, manifestPath, outDir string, client *http.Client, output io.Writer) error {
	contracts, err := readManifest(manifestPath)
	if err != nil {
		return err
	}

	if outDir == "" {
		outDir, err = os.MkdirTemp("", "sourcecraft-contracts-")
		if err != nil {
			return err
		}
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil { //nolint:gosec // This directory contains public contract comparison artifacts, not credentials.
		return err
	}

	inputInfo, err := os.Stat(filepath.Dir(manifestPath))
	if err != nil {
		return err
	}

	outputInfo, err := os.Stat(outDir)
	if err != nil {
		return err
	}

	if os.SameFile(inputInfo, outputInfo) {
		return errors.New("report directory must differ from the snapshot directory")
	}

	var summary strings.Builder
	fmt.Fprint(&summary, "## API contract comparison\n\n")

	changed, failed := 0, 0

	for _, c := range contracts {
		drift, err := compare(ctx, c, filepath.Dir(manifestPath), outDir, client)
		switch {
		case err != nil:
			failed++

			fmt.Fprintf(&summary, "- **%s: ERROR** — %v\n", c.Name, err)
		case drift:
			changed++

			fmt.Fprintf(&summary, "- **%s: CHANGED** — see `%s.diff`.\n", c.Name, c.Name)
		default:
			fmt.Fprintf(&summary, "- **%s: unchanged**\n", c.Name)
		}
	}

	fmt.Fprintf(&summary, "\nChecked %d contracts: %d changed, %d failed.\n", len(contracts), changed, failed)

	if err := os.WriteFile(filepath.Join(outDir, "summary.md"), []byte(summary.String()), 0o644); err != nil { //nolint:gosec // This file contains public source or contract metadata, not credentials.
		return err
	}

	_, outputErr := fmt.Fprintf(output, "%s\nReport: %s\n", summary.String(), outDir)
	if changed != 0 || failed != 0 {
		return errors.Join(fmt.Errorf("contract check failed: %d changed, %d failed", changed, failed), outputErr)
	}

	return outputErr
}

func readManifest(path string) ([]contract, error) {
	data, err := os.ReadFile(path) //nolint:gosec // The local CLI caller explicitly selects this input file.
	if err != nil {
		return nil, err
	}

	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}

	if len(m.Contracts) == 0 {
		return nil, errors.New("contract manifest is empty")
	}

	names, snapshots := map[string]bool{}, map[string]bool{}

	for _, c := range m.Contracts {
		if c.Name == "" || strings.IndexFunc(c.Name, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') }) >= 0 {
			return nil, fmt.Errorf("invalid contract name %q", c.Name)
		}

		if c.Snapshot == "" || filepath.Base(c.Snapshot) != c.Snapshot || !strings.HasSuffix(c.Snapshot, ".json") || c.Snapshot == filepath.Base(path) {
			return nil, fmt.Errorf("invalid snapshot %q", c.Snapshot)
		}

		u, err := url.Parse(c.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil {
			return nil, fmt.Errorf("invalid contract URL %q", c.URL)
		}

		if names[c.Name] || snapshots[c.Snapshot] {
			return nil, fmt.Errorf("duplicate contract %q or snapshot %q", c.Name, c.Snapshot)
		}

		names[c.Name], snapshots[c.Snapshot] = true, true
	}

	files, err := filepath.Glob(filepath.Join(filepath.Dir(path), "*.json"))
	if err != nil {
		return nil, err
	}

	for _, file := range files {
		if filepath.Base(file) != filepath.Base(path) && !snapshots[filepath.Base(file)] {
			return nil, fmt.Errorf("snapshot %s is missing from the contract manifest", file)
		}
	}

	return m.Contracts, nil
}

// canonicalizeSpec changes whitespace and object-key ordering only. UseNumber
// preserves integer and decimal text, so even adjacent large IDs stay distinct.
func canonicalizeSpec(data []byte) ([]byte, error) {
	if !json.Valid(data) {
		return nil, errors.New("specification is not valid JSON")
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()

	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}

	if value == nil {
		return nil, errors.New("specification must be a JSON object")
	}

	if _, ok := value["paths"].(map[string]any); !ok {
		return nil, errors.New("specification has no paths object")
	}

	swagger, _ := value["swagger"].(string)

	openapi, _ := value["openapi"].(string)
	if swagger == "" && openapi == "" {
		return nil, errors.New("specification has no Swagger or OpenAPI version")
	}

	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}

	return append(encoded, '\n'), nil
}

func fetch(ctx context.Context, client *http.Client, source string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "sourcecraft-contract-check")

	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func(body io.Closer) {
		// Preserve the read result; closing an already-consumed body is best effort.
		_ = body.Close()
	}(response.Body)

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned HTTP %d", response.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(response.Body, maxSpecBytes+1))
	if err != nil {
		return nil, err
	}

	if len(data) > maxSpecBytes {
		return nil, errors.New("specification exceeds 32 MiB")
	}

	return canonicalizeSpec(data)
}

func compare(ctx context.Context, c contract, baseDir, outDir string, client *http.Client) (bool, error) {
	diffPath := filepath.Join(outDir, c.Name+".diff")
	// A failed recheck must not publish snapshots from an earlier run alongside
	// the current error summary.
	for _, suffix := range []string{".diff", ".local.json", ".published.json"} {
		if err := os.Remove(filepath.Join(outDir, c.Name+suffix)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}

	data, err := os.ReadFile(filepath.Join(baseDir, c.Snapshot)) //nolint:gosec // The local CLI caller explicitly selects this input file.
	if err != nil {
		return false, fmt.Errorf("local snapshot: %w", err)
	}

	local, err := canonicalizeSpec(data)
	if err != nil {
		return false, fmt.Errorf("local snapshot: %w", err)
	}

	remote, err := fetch(ctx, client, c.URL)
	if err != nil {
		return false, fmt.Errorf("published specification: %w", err)
	}

	localPath, remotePath := filepath.Join(outDir, c.Name+".local.json"), filepath.Join(outDir, c.Name+".published.json")
	for path, content := range map[string][]byte{localPath: local, remotePath: remote} {
		if err := os.WriteFile(path, content, 0o644); err != nil { //nolint:gosec // This file contains public source or contract metadata, not credentials.
			return false, err
		}
	}

	if bytes.Equal(local, remote) {
		return false, nil
	}

	command := exec.CommandContext(ctx, "git", "diff", "--no-index", "--no-color", "--no-ext-diff", "--no-textconv", "--", localPath, remotePath) //nolint:gosec // Arguments are passed directly without a shell; the executable and operation are fixed.

	diff, err := command.CombinedOutput()
	if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 1 {
		return false, fmt.Errorf("create contract diff: %w: %s", err, diff)
	}

	if err := os.WriteFile(diffPath, diff, 0o644); err != nil { //nolint:gosec // This file contains public source or contract metadata, not credentials.
		return false, err
	}

	return true, nil
}
