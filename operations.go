package sourcecraft

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// OperationFailedError reports a failed or cancelled asynchronous operation.
// The API's structured error is available in OperationError when provided.
type OperationFailedError struct {
	OperationID    string
	Status         OperationStatus
	OperationError *OperationError
}

func (e *OperationFailedError) Error() string {
	message := fmt.Sprintf("sourcecraft: operation %s ended with status %s", e.OperationID, e.Status)
	if e.OperationError != nil {
		message += ": " + e.OperationError.Message
	}

	return message
}

// WaitPullRequestMerge polls a merge operation until completion. A non-positive
// interval uses one second. Use a context deadline to bound the total wait.
func (c *Client) WaitPullRequestMerge(ctx context.Context, operationID string, interval time.Duration) (*MergePullRequestOperation, *Response, error) {
	return waitOperation(ctx, operationID, interval,
		func(ctx context.Context) (*MergePullRequestOperation, *Response, error) {
			return c.GetPullRequestMergeOperationByID(ctx, operationID)
		},
		func(op *MergePullRequestOperation) (OperationStatus, *OperationError) { return op.Status, op.Error })
}

// WaitOrganizationInvites polls an invitation operation until completion. Inspect
// the successful response for any per-invite errors reported by SourceCraft.
func (c *Client) WaitOrganizationInvites(ctx context.Context, operationID string, interval time.Duration) (*CreateOrganizationInvitesOperation, *Response, error) {
	return waitOperation(ctx, operationID, interval,
		func(ctx context.Context) (*CreateOrganizationInvitesOperation, *Response, error) {
			return c.GetOrganizationInvitesOperationByID(ctx, operationID)
		},
		func(op *CreateOrganizationInvitesOperation) (OperationStatus, *OperationError) {
			return op.Status, op.Error
		})
}

// WaitSecret polls a secret operation until completion.
func (c *Client) WaitSecret(ctx context.Context, operationID string, interval time.Duration) (*SecretsOperation, *Response, error) {
	return waitOperation(ctx, operationID, interval,
		func(ctx context.Context) (*SecretsOperation, *Response, error) {
			return c.GetSecretOperationByID(ctx, operationID)
		},
		func(op *SecretsOperation) (OperationStatus, *OperationError) { return op.Status, op.Error })
}

func waitOperation[T any](ctx context.Context, operationID string, interval time.Duration,
	fetch func(context.Context) (*T, *Response, error), state func(*T) (OperationStatus, *OperationError),
) (*T, *Response, error) {
	if ctx == nil {
		return nil, nil, errors.New("sourcecraft: operation context is required")
	}

	if interval <= 0 {
		interval = time.Second
	}

	var (
		operation *T
		response  *Response
	)

	for {
		if err := ctx.Err(); err != nil {
			return operation, response, err
		}

		var err error

		operation, response, err = fetch(ctx)
		if err != nil {
			return operation, response, err
		}

		status, operationError := state(operation)
		switch status {
		case OperationStatusSuccess:
			return operation, response, nil
		case OperationStatusFailed, OperationStatusCancel:
			return operation, response, &OperationFailedError{OperationID: operationID, Status: status, OperationError: operationError}
		case OperationStatusScheduled, OperationStatusInProgress:
			// Continue below.
		default:
			return operation, response, fmt.Errorf("sourcecraft: unknown operation status %q", status)
		}

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return operation, response, ctx.Err()
		case <-timer.C:
		}
	}
}
