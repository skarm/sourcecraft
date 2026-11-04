package sourcecraft_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/skarm/sourcecraft"
)

func TestPaginationTraversesEmptyPages(t *testing.T) {
	var tokens []string
	fetch := func(_ context.Context, token string) ([]int, string, error) {
		tokens = append(tokens, token)
		switch token {
		case "":
			return []int{1, 2}, "empty", nil
		case "empty":
			return nil, "last", nil
		default:
			return []int{3}, "", nil
		}
	}
	var result []int
	for value, err := range sourcecraft.Paginate(t.Context(), fetch) {
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, value)
	}
	if !reflect.DeepEqual(result, []int{1, 2, 3}) || !reflect.DeepEqual(tokens, []string{"", "empty", "last"}) {
		t.Fatalf("items=%v tokens=%v", result, tokens)
	}
}

func TestPaginationEarlyBreak(t *testing.T) {
	calls := 0
	for _, err := range sourcecraft.Paginate(t.Context(), func(context.Context, string) ([]int, string, error) { calls++; return []int{1, 2}, "next", nil }) {
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	if calls != 1 {
		t.Fatalf("fetched %d pages after early break", calls)
	}
}

func TestPaginationErrors(t *testing.T) {
	sentinel := errors.New("fetch failed")
	for _, tc := range []struct {
		name  string
		fetch func(context.Context, string) ([]int, string, error)
		want  error
		calls int
	}{
		{"fetch", func(context.Context, string) ([]int, string, error) { return nil, "", sentinel }, sentinel, 1},
		{"repeated_token", func(context.Context, string) ([]int, string, error) { return nil, "same", nil }, nil, 2},
		{"nil_fetch", nil, nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, errorsSeen := 0, 0
			fetch := tc.fetch
			if fetch != nil {
				fetch = func(ctx context.Context, token string) ([]int, string, error) { calls++; return tc.fetch(ctx, token) }
			}
			for _, err := range sourcecraft.Paginate(t.Context(), fetch) {
				if err == nil {
					t.Fatal("expected error")
				}
				if tc.want != nil && !errors.Is(err, tc.want) {
					t.Fatalf("error = %v", err)
				}
				errorsSeen++
			}
			if errorsSeen != 1 || calls != tc.calls {
				t.Fatalf("errors=%d calls=%d", errorsSeen, calls)
			}
		})
	}
}

func TestPaginationCancellationBetweenItems(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls, items, failures := 0, 0, 0
	for _, err := range sourcecraft.Paginate(ctx, func(context.Context, string) ([]int, string, error) { calls++; return []int{1, 2}, "next", nil }) {
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			failures++
			continue
		}
		items++
		cancel()
	}
	if calls != 1 || items != 1 || failures != 1 {
		t.Fatalf("calls=%d items=%d errors=%d", calls, items, failures)
	}
}
