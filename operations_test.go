//nolint:testpackage // These tests share white-box helpers for private SDK behavior.
package sourcecraft

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestWaitPullRequestMerge(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/operations/merge/id:job" {
			t.Errorf("path=%s", r.URL.Path)
		}
		statuses := []string{"scheduled", "in_progress", "success"}
		index := int(calls.Add(1)) - 1
		if index >= len(statuses) {
			t.Error("polled after completion")
			index = len(statuses) - 1
		}
		fmt.Fprintf(w, `{"operation_id":"job","status":%q,"response":{"pull_request_id":"pr"}}`, statuses[index])
	})
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	op, response, err := client.WaitPullRequestMerge(ctx, "job", time.Nanosecond)
	if err != nil || op.Status != OperationStatusSuccess || response.StatusCode != 200 || calls.Load() != 3 {
		t.Fatalf("operation=%v response=%v error=%v calls=%d", op, response, err, calls.Load())
	}
}

func TestWaitOperationTerminalStates(t *testing.T) {
	for _, status := range []string{"failed", "cancel", "unexpected"} {
		t.Run(status, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprintf(w, `{"operation_id":"job","status":%q,"error":{"error_code":"MergeConflict","message":"conflict"}}`, status)
			})
			op, response, err := client.WaitPullRequestMerge(t.Context(), "job", time.Nanosecond)
			if err == nil || op == nil || response == nil {
				t.Fatalf("operation=%v response=%v error=%v", op, response, err)
			}
			if status == "unexpected" {
				if !strings.Contains(err.Error(), "unknown operation status") {
					t.Fatal(err)
				}
			} else {
				if failed, ok := errors.AsType[*OperationFailedError](err); !ok || failed.OperationID != "job" || failed.OperationError.ErrorCode != "MergeConflict" {
					t.Fatalf("error=%v", err)
				}
			}
		})
	}
}

func TestWaitOtherOperationTypes(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/operations/create-invites/id:invites":
			fmt.Fprint(w, `{"status":"success","response":{"errors":[{"message":"one invite failed"}]}}`)
		case "/operations/secrets/id:secret":
			fmt.Fprint(w, `{"status":"success"}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	invites, _, err := client.WaitOrganizationInvites(t.Context(), "invites", time.Nanosecond)
	if err != nil || invites.Response == nil || len(invites.Response.Errors) != 1 {
		t.Fatalf("invites=%v error=%v", invites, err)
	}
	if _, _, err := client.WaitSecret(t.Context(), "secret", time.Nanosecond); err != nil {
		t.Fatal(err)
	}
}

func TestWaitOperationCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 1500*time.Millisecond)
		defer cancel()
		start := time.Now()
		calls := 0
		op, response, err := waitOperation(ctx, "job", time.Second,
			func(context.Context) (*MergePullRequestOperation, *Response, error) {
				calls++
				return &MergePullRequestOperation{Status: OperationStatusInProgress}, nil, nil
			},
			func(op *MergePullRequestOperation) (OperationStatus, *OperationError) { return op.Status, op.Error })
		if !errors.Is(err, context.DeadlineExceeded) || op == nil || response != nil || calls != 2 {
			t.Fatalf("operation=%v response=%v error=%v calls=%d", op, response, err, calls)
		}
		if elapsed := time.Since(start); elapsed != 1500*time.Millisecond {
			t.Fatalf("cancellation took %v of virtual time", elapsed)
		}
	})
}

func TestWaitOperationPollingIntervals(t *testing.T) {
	for _, tc := range []struct {
		name        string
		interval    time.Duration
		wantElapsed time.Duration
	}{
		{"explicit", 5 * time.Second, 10 * time.Second},
		{"default", 0, 2 * time.Second},
		{"negative_uses_default", -time.Second, 2 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				calls := 0
				op, _, err := waitOperation(t.Context(), "job", tc.interval,
					func(context.Context) (*MergePullRequestOperation, *Response, error) {
						calls++
						status := OperationStatusInProgress
						if calls == 3 {
							status = OperationStatusSuccess
						}
						return &MergePullRequestOperation{Status: status}, nil, nil
					},
					func(op *MergePullRequestOperation) (OperationStatus, *OperationError) { return op.Status, op.Error })
				if err != nil || op.Status != OperationStatusSuccess || calls != 3 {
					t.Fatalf("operation=%v error=%v calls=%d", op, err, calls)
				}
				if elapsed := time.Since(start); elapsed != tc.wantElapsed {
					t.Fatalf("polling took %v of virtual time, want %v", elapsed, tc.wantElapsed)
				}
			})
		})
	}
}
