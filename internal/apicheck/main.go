// Command apicheck checks the SDK's exported Go API against a reviewed baseline.
// New declarations are allowed; changing or removing a recorded declaration fails.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/importer"
	"go/token"
	"go/types"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

type surface map[string]string

func main() {
	baseline := flag.String("baseline", "api/public-api.golden", "reviewed exported API baseline")
	update := flag.Bool("update", false, "explicitly replace the baseline after reviewing API changes")

	flag.Parse()

	if err := run(context.Background(), ".", *baseline, *update, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, dir, baselinePath string, update bool, output io.Writer) error {
	pkg, err := loadPackage(ctx, dir)
	if err != nil {
		return err
	}

	current := describe(pkg)
	if update {
		data, err := json.MarshalIndent(current, "", "  ")
		if err != nil {
			return err
		}

		if err := os.WriteFile(baselinePath, append(data, '\n'), 0o644); err != nil { //nolint:gosec // This file contains public source or contract metadata, not credentials.
			return err
		}

		_, err = fmt.Fprintf(output, "Recorded %d exported declarations in %s\n", len(current), baselinePath)

		return err
	}

	data, err := os.ReadFile(baselinePath) //nolint:gosec // The local CLI caller explicitly selects this input file.
	if err != nil {
		return err
	}

	var baseline surface
	if err := json.Unmarshal(data, &baseline); err != nil {
		return err
	}

	if len(baseline) == 0 {
		return errors.New("public API baseline is empty")
	}

	if changes := incompatibleChanges(baseline, current); len(changes) > 0 {
		return fmt.Errorf("public Go API is incompatible with %s:\n%s", baselinePath, strings.Join(changes, "\n"))
	}

	_, err = fmt.Fprintf(output, "Public Go API is compatible: %d recorded declarations checked\n", len(baseline))

	return err
}

func loadPackage(ctx context.Context, dir string) (*types.Package, error) {
	// Use the toolchain that built this checker: gc export data is versioned.
	command := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "list", "-deps", "-export", "-json", ".") //nolint:gosec,staticcheck // Versioned export data requires the checker's build toolchain; the executable and operation are fixed, without a shell.
	command.Dir = dir

	command.Env = append(os.Environ(), "GOTOOLCHAIN=local")

	var stderr strings.Builder

	command.Stderr = &stderr

	data, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("load exported API: %w: %s", err, &stderr)
	}

	decoder := json.NewDecoder(strings.NewReader(string(data)))
	exports := map[string]string{}

	var root string

	for {
		var entry struct {
			ImportPath, Export string
			DepOnly            bool
		}
		if err := decoder.Decode(&entry); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}

		exports[entry.ImportPath] = entry.Export
		if !entry.DepOnly {
			root = entry.ImportPath
		}
	}

	if root == "" {
		return nil, errors.New("no root package in go list output")
	}

	lookup := func(path string) (io.ReadCloser, error) {
		if exports[path] == "" {
			return nil, fmt.Errorf("missing export data for %s", path)
		}

		return os.Open(exports[path])
	}

	return importer.ForCompiler(token.NewFileSet(), "gc", lookup).Import(root)
}

func incompatibleChanges(baseline, current surface) []string {
	var changes []string

	for _, name := range slices.Sorted(maps.Keys(baseline)) {
		value, ok := current[name]
		if !ok {
			changes = append(changes, "removed: "+name)
		} else if value != baseline[name] {
			changes = append(changes, fmt.Sprintf("changed: %s\n  was: %s\n  now: %s", name, baseline[name], value))
		}
	}

	return changes
}
