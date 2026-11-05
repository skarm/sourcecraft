// Command generate creates the SDK's API methods and models from the pinned
// SourceCraft Swagger or AppSec OpenAPI document, using the Go standard library.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
)

func main() {
	specPath := flag.String("spec", "api/sourcecraft.swagger.json", "path to the pinned Swagger or AppSec OpenAPI JSON specification")
	outDir := flag.String("out", ".", "output directory")
	check := flag.Bool("check", false, "check that generated files match the specification")

	flag.Parse()

	if err := run(*specPath, *outDir, *check); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run owns filesystem access. The compiler has no knowledge of output paths.
func run(specPath, outDir string, check bool) error {
	data, err := os.ReadFile(specPath) //nolint:gosec // The local CLI caller explicitly selects this input file.
	if err != nil {
		return err
	}

	outputs, err := generate(data)
	if err != nil {
		return err
	}

	names := slices.Sorted(maps.Keys(outputs))
	if check {
		for _, name := range names {
			path := filepath.Join(outDir, name)

			current, err := os.ReadFile(path) //nolint:gosec // The local CLI caller explicitly selects this input file.
			if err != nil {
				return err
			}

			if !bytes.Equal(current, outputs[name]) {
				return fmt.Errorf("%s is out of date; run go generate", path)
			}
		}

		return nil
	}

	if err := validateOutputPaths(specPath, outDir, names); err != nil {
		return err
	}

	for _, name := range names {
		if err := os.WriteFile(filepath.Join(outDir, name), outputs[name], 0o644); err != nil { //nolint:gosec // This file contains public source or contract metadata, not credentials.
			return err
		}
	}

	return nil
}

func validateOutputPaths(specPath, outDir string, names []string) error {
	// Reject all known invalid targets before modifying the first output. This is
	// a preflight check, not a multi-file transaction against filesystem failures.
	inputInfo, err := os.Stat(specPath)
	if err != nil {
		return fmt.Errorf("stat specification %s: %w", specPath, err)
	}

	var existingTargets []os.FileInfo

	for _, name := range names {
		path := filepath.Join(outDir, name)

		info, err := os.Stat(path) //nolint:gosec // Output names come only from the generator; the local caller selects its directory.
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}

		if errors.Is(err, os.ErrNotExist) {
			// A dangling link can alias another output that does not exist
			// until the write loop starts. Existing links are checked by inode.
			if _, linkErr := os.Lstat(path); linkErr == nil { //nolint:gosec // The local CLI caller selects the output directory; output names are fixed by the compiler.
				return fmt.Errorf("%s has an unresolved output target", path)
			} else if !errors.Is(linkErr, os.ErrNotExist) {
				return linkErr
			}

			continue
		}

		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", path)
		}

		if os.SameFile(inputInfo, info) {
			return fmt.Errorf("%s aliases the specification %s", path, specPath)
		}

		for _, previous := range existingTargets {
			if os.SameFile(previous, info) {
				return fmt.Errorf("%s aliases another generated output", path)
			}
		}

		existingTargets = append(existingTargets, info)
	}

	return nil
}
