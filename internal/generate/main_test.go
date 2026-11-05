package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

func TestPinnedSpecificationMatchesGeneratedSDK(t *testing.T) {
	if err := run("../../api/sourcecraft.swagger.json", "../..", true); err != nil {
		t.Fatal(err)
	}
	if err := run("../../api/appsec.openapi.json", "../..", true); err != nil {
		t.Fatal(err)
	}
}

func TestRunRejectsAliasedOutputsBeforeWriting(t *testing.T) {
	platform, err := json.Marshal(smallSpec("get"))
	if err != nil {
		t.Fatal(err)
	}
	for _, api := range []struct {
		name, source, methods, models string
	}{
		{"platform", string(platform), "api_gen.go", "models_gen.go"},
		{"appsec", appsecFixture("get"), "appsec_api_gen.go", "appsec_models_gen.go"},
	} {
		for _, alias := range []string{"input_path", "input_symlink", "input_hardlink", "output_symlink", "output_hardlink", "dangling_symlink", "dangling_chain"} {
			t.Run(api.name+"/"+alias, func(t *testing.T) {
				dir := t.TempDir()
				input := filepath.Join(dir, "schema.json")
				methods := filepath.Join(dir, api.methods)
				models := filepath.Join(dir, api.models)
				original := map[string][]byte{input: []byte(api.source), methods: []byte("original methods\n")}
				if alias == "input_path" {
					input = models
					original[input] = []byte(api.source)
				}
				if alias == "dangling_symlink" || alias == "dangling_chain" {
					delete(original, methods)
				}
				for path, content := range original {
					if err := os.WriteFile(path, content, 0o600); err != nil {
						t.Fatal(err)
					}
				}

				switch alias {
				case "input_path":
				case "input_symlink":
					generatorTestSymlink(t, input, models)
					original[models] = []byte(api.source)
				case "input_hardlink":
					if err := os.Link(input, models); err != nil {
						t.Fatal(err)
					}
					original[models] = []byte(api.source)
				case "output_symlink":
					generatorTestSymlink(t, methods, models)
					original[models] = original[methods]
				case "output_hardlink":
					if err := os.Link(methods, models); err != nil {
						t.Fatal(err)
					}
					original[models] = original[methods]
				case "dangling_symlink":
					generatorTestSymlink(t, methods, models)
				case "dangling_chain":
					intermediate := filepath.Join(dir, "intermediate")
					generatorTestSymlink(t, methods, intermediate)
					generatorTestSymlink(t, intermediate, models)
				}

				if err := run(input, dir, false); err == nil {
					t.Fatal("aliased outputs were accepted")
				}
				for path, expected := range original {
					actual, err := os.ReadFile(path)
					if err != nil || !bytes.Equal(actual, expected) {
						t.Errorf("preflight modified %s: error=%v", path, err)
					}
				}
				if alias == "dangling_symlink" || alias == "dangling_chain" {
					if _, err := os.Stat(methods); !errors.Is(err, os.ErrNotExist) {
						t.Errorf("first output was created before rejecting dangling alias: %v", err)
					}
				}
			})
		}
	}
}

func TestRunPreservesIndependentOutputSymlinks(t *testing.T) {
	dir := t.TempDir()
	spec := filepath.Join(dir, "schema.json")
	data, err := json.Marshal(smallSpec("get"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spec, data, 0o600); err != nil {
		t.Fatal(err)
	}
	outputs, err := generate(data)
	if err != nil {
		t.Fatal(err)
	}
	for name := range outputs {
		target := filepath.Join(dir, name+".target")
		if err := os.WriteFile(target, []byte("old output"), 0o600); err != nil {
			t.Fatal(err)
		}
		generatorTestSymlink(t, target, filepath.Join(dir, name))
	}
	if err := run(spec, dir, false); err != nil {
		t.Fatal(err)
	}
	// Check mode may read symlinks; it remains read-only and accepts up-to-date
	// output files regardless of their physical location.
	if err := run(spec, dir, true); err != nil {
		t.Fatal(err)
	}
	for name, expected := range outputs {
		actual, err := os.ReadFile(filepath.Join(dir, name+".target"))
		if err != nil || !bytes.Equal(actual, expected) {
			t.Errorf("independent symlink output %s: error=%v", name, err)
		}
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Errorf("output symlink %s was replaced: %v", name, err)
		}
	}
}

func generatorTestSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		// Windows may require a privilege to create symbolic links, even when
		// ordinary files and hard links are supported by the filesystem.
		const windowsPrivilegeNotHeld = syscall.Errno(1314)
		if runtime.GOOS == "windows" && (errors.Is(err, os.ErrPermission) || errors.Is(err, windowsPrivilegeNotHeld)) {
			t.Skipf("creating symbolic links requires Windows permission: %v", err)
		}
		t.Fatal(err)
	}
}
