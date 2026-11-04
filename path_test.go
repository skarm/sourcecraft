//nolint:testpackage // These tests share white-box helpers for private SDK behavior.
package sourcecraft

import (
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestExpandPathManyParameters(t *testing.T) {
	params := make([]pathParam, 0, 12)
	var template, want strings.Builder
	for i := range 12 {
		name, value := "id"+strconv.Itoa(i), "resource /?#+%"+strconv.Itoa(i)
		params = append(params, pathParam{name, value})
		template.WriteString("/{" + name + "}")
		want.WriteString("/" + url.PathEscape(value))
	}
	got, err := expandPath(template.String(), params...)
	if err != nil || got != want.String() {
		t.Fatalf("path=%q error=%v; want %q", got, err, want.String())
	}
}

func FuzzExpandPath(f *testing.F) {
	for _, value := range []string{"org", "", ".", "..", "a /?#+%{repo}", "репозиторий", "\x00\xff", strings.Repeat("long-slug-", 32)} {
		f.Add(value, "repo /?#+%", "run")
	}
	f.Fuzz(func(t *testing.T, org, repo, run string) {
		params := []pathParam{{"org", org}, {"repo", repo}, {"run", run}}
		for _, template := range []string{
			"/repos/{org}/{repo}/runs/{run}",
			"/repos/{repo}/{org}/{repo}",
			"/{missing}/{{repo}}/{org}/{unfinished",
			"/constant",
		} {
			// The original expansion is an independent reference for the wire
			// path, including validation of parameters unused by the template.
			want, invalid := template, ""
			for _, p := range params {
				if p.value == "" || p.value == "." || p.value == ".." {
					invalid = p.name
					break
				}
				want = strings.ReplaceAll(want, "{"+p.name+"}", url.PathEscape(p.value))
			}
			got, err := expandPath(template, params...)
			if invalid != "" {
				if got != "" || err == nil || err.Error() != "sourcecraft: invalid path parameter "+invalid {
					t.Fatalf("invalid parameter %s: path=%q error=%v", invalid, got, err)
				}
			} else if err != nil || got != want {
				t.Fatalf("expand %q: got %q, %v; want %q", template, got, err, want)
			}
		}
	})
}
