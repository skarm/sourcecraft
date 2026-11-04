package sourcecraft_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/skarm/sourcecraft"
)

func TestRedirectCallbackCannotChangeOrigin(t *testing.T) {
	for _, appsec := range []bool{false, true} {
		for _, action := range []string{"other_origin", "nil_url", "nil_url_error", "callback_error"} {
			name := action
			if appsec {
				name = "appsec/" + name
			}
			t.Run(name, func(t *testing.T) {
				var calls atomic.Int32
				target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if r.Header.Get("Authorization") != "" {
						t.Error("redirect forwarded credentials to another origin")
					}
					_, _ = w.Write([]byte(`{}`))
				}))
				defer target.Close()
				origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Redirect(w, r, "/redirected", http.StatusFound)
				}))
				defer origin.Close()
				callbackErr := errors.New("redirect callback failed")
				httpClient := &http.Client{CheckRedirect: func(req *http.Request, _ []*http.Request) error {
					if action == "nil_url" || action == "nil_url_error" {
						req.URL = nil
						if action == "nil_url_error" {
							return callbackErr
						}
						return nil
					}
					u, err := url.Parse(target.URL + "/redirected")
					if err != nil {
						return err
					}
					req.URL = u
					if action == "callback_error" {
						return callbackErr
					}
					return nil
				}}
				options := []sourcecraft.Option{sourcecraft.WithBaseURL(origin.URL), sourcecraft.WithHTTPClient(httpClient)}
				var response *sourcecraft.Response
				var err error
				if appsec {
					client, createErr := sourcecraft.NewAppSecClient("redirect-test-token", options...)
					if createErr != nil {
						t.Fatal(createErr)
					}
					_, response, err = client.GetScan(t.Context(), "scan", &sourcecraft.AppSecGetScanOptions{GitRepo: "repo"})
				} else {
					client, createErr := sourcecraft.NewClient("redirect-test-token", options...)
					if createErr != nil {
						t.Fatal(createErr)
					}
					_, response, err = client.GetRepository(t.Context(), "org", "repo")
				}
				if calls.Load() != 0 {
					t.Fatalf("sent %d requests to the other origin", calls.Load())
				}
				if response == nil || response.StatusCode != http.StatusFound {
					t.Fatalf("lost original redirect metadata: response=%v error=%v", response, err)
				}
				if action == "callback_error" || action == "nil_url_error" {
					if !errors.Is(err, callbackErr) {
						t.Fatalf("callback error was replaced: %v", err)
					}
				} else if _, ok := errors.AsType[*sourcecraft.APIError](err); !ok {
					t.Fatalf("expected rejected redirect as sourcecraft.APIError, got %v", err)
				}
			})
		}
	}
}
