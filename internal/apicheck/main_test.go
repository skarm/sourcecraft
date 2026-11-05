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

func TestPublicAPIChecksPromotedMethods(t *testing.T) {
	for _, embedding := range []string{"hidden", "*hidden"} {
		t.Run(embedding, func(t *testing.T) {
			before := "package sdk; type hidden struct{}; func (hidden) Value() string { return \"\" }; func (*hidden) Pointer() {}; type Public struct { " + embedding + " }"
			base := fixtureSurface(t, before)
			for _, declaration := range []string{`func (hidden) Value() string { return "" };`, "func (*hidden) Pointer() {};"} {
				after := strings.Replace(before, declaration, "", 1)
				if changes := incompatibleChanges(base, fixtureSurface(t, after)); len(changes) == 0 {
					t.Fatalf("removal of promoted method %s was missed", declaration)
				}
			}
			// Moving a promoted method to the public type keeps its effective API.
			after := strings.Replace(before, `func (hidden) Value() string { return "" };`, `func (Public) Value() string { return "" };`, 1)
			if changes := incompatibleChanges(base, fixtureSurface(t, after)); len(changes) != 0 {
				t.Fatalf("equivalent method promotion rejected: %v", changes)
			}
		})
	}
}

func TestPublicAPIIgnoresNestedCallbackArgumentNames(t *testing.T) {
	for _, typ := range []string{
		"[]func(name string)",
		"[2]func(name string)",
		"map[string]func(name string)",
		"chan func(name string)",
		"<-chan func(name string)",
		"chan<- func(name string)",
		"*func(name string)",
		"chan (<-chan func(name string))",
	} {
		t.Run(typ, func(t *testing.T) {
			before := "package sdk; type Callbacks " + typ
			after := strings.ReplaceAll(before, "name string", "input string")
			if changes := incompatibleChanges(fixtureSurface(t, before), fixtureSurface(t, after)); len(changes) != 0 {
				t.Fatalf("callback argument rename rejected: %v", changes)
			}
			changed := strings.ReplaceAll(before, "name string", "name int")
			if changes := incompatibleChanges(fixtureSurface(t, before), fixtureSurface(t, changed)); len(changes) == 0 {
				t.Fatal("callback parameter type change was missed")
			}
		})
	}
}

func TestPublicAPIChecksFieldsPromotedFromPrivateTypes(t *testing.T) {
	for _, declaration := range []string{
		"type hidden struct { Name string }; type Public struct { hidden }",
		"type hidden struct { Name string }; type Public struct { *hidden }",
		"type hidden struct { *hidden; Name string }; type Public struct { *hidden }",
		"type hidden struct { Name string }; type Public = hidden",
	} {
		before := "package sdk; " + declaration
		after := strings.ReplaceAll(before, "Name string", "Name int")
		if changes := incompatibleChanges(fixtureSurface(t, before), fixtureSurface(t, after)); len(changes) == 0 {
			t.Errorf("public field type change was missed: %s", declaration)
		}
	}
}

func TestPublicAPIIgnoresInaccessibleEmbeddedMembers(t *testing.T) {
	for _, declarations := range []string{
		"type hidden struct { Name string }; type Public struct { Name bool; hidden }",
		"type hidden struct { Name string }; type other struct { Name bool }; type Public struct { hidden; other }",
		"type hidden struct{}; func (hidden) Name() string { return \"\" }; type Public struct { hidden }; func (Public) Name() bool { return false }",
		"type hidden struct{}; func (hidden) Name() string { return \"\" }; type other struct{}; func (other) Name() bool { return false }; type Public struct { hidden; other }",
	} {
		before := "package sdk; " + declarations
		after := strings.ReplaceAll(before, "Name string", "Name int")
		after = strings.ReplaceAll(after, `Name() string { return "" }`, `Name() int { return 0 }`)
		if changes := incompatibleChanges(fixtureSurface(t, before), fixtureSurface(t, after)); len(changes) != 0 {
			t.Errorf("inaccessible private member changed the public API: %v", changes)
		}
	}
}

func TestPublicAPIChecksAliasesAndEmbeddedInterfaces(t *testing.T) {
	for _, declarations := range []string{
		"type hidden struct{}; func (hidden) Name() string { return \"\" }; type Public = hidden",
		"type hidden interface { Name() string }; type Public interface { hidden }",
	} {
		before := "package sdk; " + declarations
		after := strings.ReplaceAll(before, "Name() string", "Name() any")
		if changes := incompatibleChanges(fixtureSurface(t, before), fixtureSurface(t, after)); len(changes) == 0 {
			t.Errorf("exported method change was missed: %s", declarations)
		}
	}
}

func TestPublicAPIChecksPrivateAliasUnderlyingTypes(t *testing.T) {
	for _, underlying := range []string{"string", "[]string", "map[string]string", "interface { Name() string }", "struct { Name string }"} {
		for _, target := range []string{"hidden", "*hidden", "**hidden"} {
			before := "package sdk; type hidden " + underlying + "; type Public = " + target
			after := strings.ReplaceAll(before, "string", "int")
			if changes := incompatibleChanges(fixtureSurface(t, before), fixtureSurface(t, after)); len(changes) == 0 {
				t.Errorf("private alias target's underlying type change was missed: %s", before)
			}
		}
	}
	for _, builtin := range []string{"error", "any", "string", "*error"} {
		before := fixtureSurface(t, "package sdk; type Public = "+builtin)
		if len(before) == 0 {
			t.Fatalf("predeclared alias %s was not recorded", builtin)
		}
	}
}

func TestPublicAPIChecksNestedChannelDirections(t *testing.T) {
	before := fixtureSurface(t, "package sdk; type Channels chan (<-chan int)")
	after := fixtureSurface(t, "package sdk; type Channels chan<- chan int")
	if changes := incompatibleChanges(before, after); len(changes) == 0 {
		t.Fatal("nested channel direction change was missed")
	}
}

func TestPublicAPIChecksPrivateGenericConstraints(t *testing.T) {
	for _, source := range []string{
		"type hidden interface { ~int }; type Public interface { hidden }; func F[T Public](v T) T { return v }",
		"type hidden interface { ~int }; func F[T hidden](v T) T { return v }",
		"type hidden = interface { ~int }; type Public interface { hidden }",
		"type hidden interface { ~int }; type Public interface { hidden | ~string }",
		"type hidden interface { ~int }; type middle interface { hidden }; type Public interface { middle }",
		"type hidden = middle; type middle interface { ~int }; type Public interface { hidden }",
		"type hidden interface { ~int }; type Box[T hidden] struct { Value T }",
		"type hidden interface { ~int }; type Public[T hidden] = []T",
		"type hidden interface { ~int }; type Public interface { hidden; comparable }",
	} {
		before := "package sdk; " + source
		after := strings.ReplaceAll(before, "~int", "~int64")
		if changes := incompatibleChanges(fixtureSurface(t, before), fixtureSurface(t, after)); len(changes) == 0 {
			t.Errorf("private generic constraint change was missed: %s", source)
		}
	}
	for _, change := range []struct{ before, after string }{
		{"type hidden interface { comparable }; func F[T hidden](v T) T { return v }", "type hidden interface { ~int }; func F[T hidden](v T) T { return v }"},
		{"type hidden interface { Name() string }; func F[T hidden](v T) T { return v }", "type hidden interface { Name() int }; func F[T hidden](v T) T { return v }"},
	} {
		if changes := incompatibleChanges(fixtureSurface(t, "package sdk; "+change.before), fixtureSurface(t, "package sdk; "+change.after)); len(changes) == 0 {
			t.Errorf("private generic constraint change was missed: %s", change.before)
		}
	}
}

func TestPublicAPIConstraintFormattingPreservesConcreteTypeIdentity(t *testing.T) {
	before := "package sdk; type hidden int; type other int; type Public interface { hidden }"
	after := strings.Replace(before, "interface { hidden }", "interface { other }", 1)
	if changes := incompatibleChanges(fixtureSurface(t, before), fixtureSurface(t, after)); len(changes) == 0 {
		t.Fatal("different named concrete constraint terms were conflated")
	}
	before = "package sdk; type hidden interface { ~int }; type other interface { ~int }; type Public interface { hidden }"
	after = strings.Replace(before, "interface { hidden }", "interface { other }", 1)
	if changes := incompatibleChanges(fixtureSurface(t, before), fixtureSurface(t, after)); len(changes) != 0 {
		t.Fatalf("equivalent private interface constraints were rejected: %v", changes)
	}
}

func TestPublicAPIChecksConcretePrivateConstraintTerms(t *testing.T) {
	for _, term := range []string{
		"hidden", "hidden | ~[]byte", "~[]hidden", "~[2]hidden", "~map[string]hidden", "~*hidden", "~chan hidden", "~func() hidden",
	} {
		before := "package sdk; type hidden int; type Public interface { " + term + " }"
		after := strings.Replace(before, "hidden int", "hidden string", 1)
		if changes := incompatibleChanges(fixtureSurface(t, before), fixtureSurface(t, after)); len(changes) == 0 {
			t.Errorf("private concrete constraint's underlying change was missed: %s", term)
		}
	}
	before := "package sdk; type hidden struct { private int }; type Public interface { hidden }"
	after := strings.Replace(before, "private int", "private []int", 1)
	if changes := incompatibleChanges(fixtureSurface(t, before), fixtureSurface(t, after)); len(changes) == 0 {
		t.Fatal("constraint term's comparability change was missed")
	}
	// A recursive concrete term must terminate; its nested named references retain identity.
	before = "package sdk; type hidden struct { Next *hidden; Name int }; type Public interface { hidden }"
	after = strings.Replace(before, "Name int", "Name string", 1)
	if changes := incompatibleChanges(fixtureSurface(t, before), fixtureSurface(t, after)); len(changes) == 0 {
		t.Fatal("recursive concrete constraint's direct field change was missed")
	}
}

func TestPublicAPIRecursivePrivateInterfaceConstraintsTerminate(t *testing.T) {
	before := "package sdk; type hidden interface { Next() hidden; Visit(hidden); Name() string }; type Public interface { hidden }"
	after := strings.Replace(before, "Name() string", "Name() int", 1)
	if changes := incompatibleChanges(fixtureSurface(t, before), fixtureSurface(t, after)); len(changes) == 0 {
		t.Fatal("recursive interface constraint's method change was missed")
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
