package main

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixture = "package sdk\n" +
	"type Model struct { Name string `json:\"name,omitzero\"`; Enabled *bool `json:\"enabled,omitzero\"`; private int }\n" +
	"type Client struct { private int }\n" +
	"func (*Client) Save(body *Model) (*Model,error)\n" +
	"func Ptr[T any](value T) *T { return &value }\n" +
	"type Alias = Model\n" +
	"type Saver interface { Save(input *Model) (*Model,error) }\n" +
	"const Mode = \"old\"\n" +
	"const Large = 9007199254740993\n"

type failingWriter struct{ err error }

func TestPublicAPICompatibilityDetectsBreakingChanges(t *testing.T) {
	base := fixtureSurface(t, fixture)
	for _, change := range []struct{ name, old, new string }{
		{"field_pointer", "Name string", "Name *string"},
		{"json_tag", `name,omitzero`, `name,omitempty`},
		{"argument", "body *Model", "body Model"},
		{"result", "(*Model,error)", "(Model,error)"},
		{"receiver", "(*Client)", "(Client)"},
		{"constant_value", `"old"`, `"new"`},
		{"large_integer", "9007199254740993", "9007199254740992"},
		{"alias", "type Alias = Model", "type Alias Model"},
		{"generic_constraint", "[T any]", "[T ~string]"},
		{"comparability", "private int", "private []int"},
		{"removed_method", "func (*Client) Save(body *Model) (*Model,error)", ""},
	} {
		t.Run(change.name, func(t *testing.T) {
			current := fixtureSurface(t, strings.Replace(fixture, change.old, change.new, 1))
			if changes := incompatibleChanges(base, current); len(changes) == 0 {
				t.Fatal("breaking change was missed")
			}
		})
	}
}

func TestPublicAPIAllowsAdditionsAndPrivateChanges(t *testing.T) {
	base := fixtureSurface(t, fixture)
	current := strings.ReplaceAll(fixture, "private int", "private bool")
	current = strings.Replace(current, "Save(body *Model)", "Save(input *Model)", 1)
	current = strings.Replace(current, "Save(input *Model) (*Model,error) }", "Save(body *Model) (*Model,error) }", 1)
	current += "\ntype ModelInput struct { Name *string }\nfunc (*Client) NewMethod(body *ModelInput)\n"
	if changes := incompatibleChanges(base, fixtureSurface(t, current)); len(changes) != 0 {
		t.Fatalf("compatible change rejected: %v", changes)
	}
}

func TestSDKMatchesReviewedPublicAPI(t *testing.T) {
	if err := run(t.Context(), "../..", "../../api/public-api.golden", false, io.Discard); err != nil {
		t.Fatal(err)
	}
}

func TestAPICheckDoesNotRewriteBaseline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.golden")
	// run's real export-data loader is exercised against the SDK, not a mock.
	if err := run(t.Context(), "../..", path, true, io.Discard); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), "../..", path, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("compatibility check changed its baseline")
	}
	broken := []byte(`{"type Client":"incompatible fixture"}`)
	if err := os.WriteFile(path, broken, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), "../..", path, false, io.Discard); err == nil {
		t.Fatal("incompatible baseline was accepted")
	}
	after, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(broken) {
		t.Fatal("failed check rewrote its baseline")
	}
}

func TestRunReturnsOutputFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.golden")
	writeErr := errors.New("output unavailable")
	for _, update := range []bool{true, false} {
		name := "check"
		if update {
			name = "update"
		}
		t.Run(name, func(t *testing.T) {
			if err := run(t.Context(), "../..", path, update, failingWriter{writeErr}); !errors.Is(err, writeErr) {
				t.Fatalf("output failure was lost (update=%v): %v", update, err)
			}
		})
	}
}

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func fixtureSurface(t *testing.T, source string) surface {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "sdk.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := new(types.Config).Check("example.test/sdk", fset, []*ast.File{file}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return describe(pkg)
}
