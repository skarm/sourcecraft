package sourcecraft

import (
	"context"
	"errors"
	"iter"
)

// Paginate lazily iterates over a token-paginated endpoint. fetch receives an
// empty token for the first page. It must keep the other query options unchanged
// and return the page's items and NextPageToken. Iteration stops when the token
// is empty, the consumer breaks, the context ends, or fetch returns an error.
// An error is yielded once, with the zero value of T.
func Paginate[T any](ctx context.Context, fetch func(context.Context, string) ([]T, string, error)) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		if ctx == nil || fetch == nil {
			yield(zero, errors.New("sourcecraft: pagination context and fetch function are required"))
			return
		}

		token := ""
		seen := map[string]bool{"": true}

		for {
			if err := ctx.Err(); err != nil {
				yield(zero, err)
				return
			}

			items, next, err := fetch(ctx, token)
			if err != nil {
				yield(zero, err)
				return
			}

			for _, item := range items {
				if err := ctx.Err(); err != nil {
					yield(zero, err)
					return
				}

				if !yield(item, nil) {
					return
				}
			}

			if next == "" {
				return
			}

			if seen[next] {
				yield(zero, errors.New("sourcecraft: pagination returned a repeated page token"))
				return
			}

			seen[next] = true
			token = next
		}
	}
}
