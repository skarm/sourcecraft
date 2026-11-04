//nolint:testpackage // These tests share white-box helpers for private SDK behavior.
package sourcecraft

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"testing"
)

func TestReadResponseBodyMatchesReadAll(t *testing.T) {
	for _, length := range []int{0, 256, 4095, 4096, 8192, 65535, 65536, 65537, 128 << 10} {
		data := bytes.Repeat([]byte("x"), length)
		for _, hint := range []int64{-1, 0, 1024, 4095, 4096, 8192, 65536, 65537, 32 << 20} {
			for _, limit := range []int64{1024, defaultMaxResponseBytes} {
				for _, fail := range []bool{false, true} {
					t.Run(fmt.Sprintf("bytes=%d/hint=%d/limit=%d/read_error=%v", length, hint, limit, fail), func(t *testing.T) {
						reader := func() io.Reader {
							var r io.Reader = bytes.NewReader(data)
							if fail {
								r = io.MultiReader(r, readFunc(func([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }))
							}
							return io.LimitReader(r, limit+1)
						}
						want, wantErr := io.ReadAll(reader())
						got, err := readResponseBody(reader(), hint, limit)
						if !bytes.Equal(got, want) || !errors.Is(err, wantErr) {
							t.Fatalf("got bytes=%d error=%v; want bytes=%d error=%v", len(got), err, len(want), wantErr)
						}
					})
				}
			}
		}
	}
}
