package sourcecraft_test

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/skarm/sourcecraft"
)

// Endpoint contracts exercise request and success models. Events, standalone
// enums and the error model need their own specimens to cover every definition.
func TestPlatformModelsOutsideSuccessContracts(t *testing.T) {
	models := map[string]reflect.Type{
		"ApiErrorResponse":                  reflect.TypeFor[sourcecraft.APIErrorResponse](),
		"CIRunComplete":                     reflect.TypeFor[sourcecraft.CIRunComplete](),
		"CIWorkflowComplete":                reflect.TypeFor[sourcecraft.CIWorkflowComplete](),
		"EventHeader":                       reflect.TypeFor[sourcecraft.EventHeader](),
		"IssueCommentCreate":                reflect.TypeFor[sourcecraft.IssueCommentCreate](),
		"IssueCommentDelete":                reflect.TypeFor[sourcecraft.IssueCommentDelete](),
		"IssueCommentReactionsUpdate":       reflect.TypeFor[sourcecraft.IssueCommentReactionsUpdate](),
		"IssueCommentUpdate":                reflect.TypeFor[sourcecraft.IssueCommentUpdate](),
		"IssueCreate":                       reflect.TypeFor[sourcecraft.IssueCreate](),
		"IssueDelete":                       reflect.TypeFor[sourcecraft.IssueDelete](),
		"IssueUpdate":                       reflect.TypeFor[sourcecraft.IssueUpdate](),
		"ListMyPullRequestsRequest.Role":    reflect.TypeFor[sourcecraft.ListMyPullRequestsRequestRole](),
		"ListUserPullRequestsRequest.Role":  reflect.TypeFor[sourcecraft.ListUserPullRequestsRequestRole](),
		"Ping":                              reflect.TypeFor[sourcecraft.Ping](),
		"PullRequestCommentCreate":          reflect.TypeFor[sourcecraft.PullRequestCommentCreate](),
		"PullRequestCommentCreateBulk":      reflect.TypeFor[sourcecraft.PullRequestCommentCreateBulk](),
		"PullRequestCommentReactionsUpdate": reflect.TypeFor[sourcecraft.PullRequestCommentReactionsUpdate](),
		"PullRequestCommentsPublish":        reflect.TypeFor[sourcecraft.PullRequestCommentsPublish](),
		"PullRequestCreate":                 reflect.TypeFor[sourcecraft.PullRequestCreate](),
		"PullRequestMerge":                  reflect.TypeFor[sourcecraft.PullRequestMerge](),
		"PullRequestMergeFailure":           reflect.TypeFor[sourcecraft.PullRequestMergeFailure](),
		"PullRequestNewIteration":           reflect.TypeFor[sourcecraft.PullRequestNewIteration](),
		"PullRequestPublish":                reflect.TypeFor[sourcecraft.PullRequestPublish](),
		"PullRequestRefresh":                reflect.TypeFor[sourcecraft.PullRequestRefresh](),
		"PullRequestReviewAssignment":       reflect.TypeFor[sourcecraft.PullRequestReviewAssignment](),
		"PullRequestReviewDecision":         reflect.TypeFor[sourcecraft.PullRequestReviewDecision](),
		"PullRequestUpdate":                 reflect.TypeFor[sourcecraft.PullRequestUpdate](),
		"Push":                              reflect.TypeFor[sourcecraft.Push](),
		"RefUpdate":                         reflect.TypeFor[sourcecraft.RefUpdate](),
		"RefUpdateOperation":                reflect.TypeFor[sourcecraft.RefUpdateOperation](),
		"RefsUpdate":                        reflect.TypeFor[sourcecraft.RefsUpdate](),
		"RepositoryCreated":                 reflect.TypeFor[sourcecraft.RepositoryCreated](),
		"RunEmbedded":                       reflect.TypeFor[sourcecraft.RunEmbedded](),
	}
	data, err := os.ReadFile("api/sourcecraft.swagger.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec contractSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	covered := map[string]bool{}
	var visit func(*contractSchema)
	visit = func(schema *contractSchema) {
		if schema == nil {
			return
		}
		if schema.Ref != "" {
			name := strings.TrimPrefix(schema.Ref, "#/definitions/")
			if covered[name] {
				return
			}
			covered[name] = true
			schema = spec.Definitions[name]
			if schema == nil {
				t.Fatalf("unknown schema reference %s", name)
			}
		}
		for _, property := range schema.Properties {
			visit(property)
		}
		visit(schema.Items)
		visit(schema.AdditionalProperties)
	}
	for _, methods := range spec.Paths {
		for _, op := range methods {
			for _, p := range op.Parameters {
				if p.In == "body" {
					visit(p.Schema)
				}
			}
			for status, response := range op.Responses {
				if strings.HasPrefix(status, "2") {
					visit(response.Schema)
				}
			}
			if op.RequestBody != nil {
				for _, content := range op.RequestBody.Content {
					visit(content.Schema)
				}
			}
		}
	}
	for name, typ := range models {
		t.Run(name, func(t *testing.T) {
			schema := spec.Definitions[name]
			if schema == nil {
				t.Fatal("missing schema definition")
			}
			expected := specimen(t, spec, schema, 0)
			input, err := json.Marshal(expected)
			if err != nil {
				t.Fatal(err)
			}
			model := reflect.New(typ)
			if err := json.Unmarshal(input, model.Interface()); err != nil {
				t.Fatal(err)
			}
			actual, err := json.Marshal(model.Interface())
			if err != nil {
				t.Fatal(err)
			}
			assertJSON(t, actual, expected)
		})
		visit(&contractSchema{Ref: "#/definitions/" + name})
	}
	for name := range spec.Definitions {
		if !covered[name] {
			t.Errorf("model %s has no contract specimen", name)
		}
	}
	t.Logf("endpoint and supplemental contracts cover all %d platform models", len(covered))
}
