# SourceCraft Go SDK

[English](README.md) · [Русский](README.ru.md)

A Go client for the SourceCraft REST API: repositories, issues, pull requests, CI/CD, and code security through AppSec. Requires **Go 1.26 or later**. No external dependencies.

`Client` covers 268 platform operations, and `AppSecClient` covers 27 security API operations. Methods and models are generated from [pinned specifications](api/README.md) captured on November 4, 2026.

## Installation

Add the SDK to your Go project:

```sh
go get github.com/skarm/sourcecraft
```

## Your first request

Set a personal access token (PAT) with access to the resources you need:

```sh
export SOURCECRAFT_TOKEN='your_access_token_here'
```

Create a client and fetch a repository using its organization and repository slugs:

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/skarm/sourcecraft"
)

func main() {
	client, err := sourcecraft.NewClient(os.Getenv("SOURCECRAFT_TOKEN"))
	if err != nil {
		log.Fatal(err)
	}

	repo, _, err := client.GetRepository(
		context.Background(), "my-org", "my-repo",
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(repo.Slug, repo.WebURL)
}
```

Each HTTP request has a 30-second timeout by default. To limit an entire sequence of requests, pass a context with a timeout or deadline. A constructed client is safe for concurrent use.

The remaining snippets belong in a function that returns `error`. They assume `client` has been constructed and `ctx` holds the operation's context.

## Request fields

Optional scalar fields use pointers: `nil` omits a field, `new(false)` sends `false`, and `new("")` sends an empty string. For slices, `nil` omits the field and `[]T{}` sends an empty array.

For example, clear a repository's description and links:

```go
_, _, err := client.UpdateRepository(ctx, "my-org", "my-repo",
	&sourcecraft.UpdateRepositoryBody{
		Description: new(""),
		Links:       []sourcecraft.Link{},
	}, nil,
)
if err != nil {
	return err
}
```

Use the built-in `new(value)` introduced in Go 1.26 to create pointers. The deprecated `sourcecraft.Ptr` wrapper remains available for compatibility.

## Pagination

`Paginate` fetches pages as you iterate. Pass each returned token to the next request and keep the other parameters unchanged:

```go
fetch := func(
	ctx context.Context, token string,
) ([]sourcecraft.Repository, string, error) {
	page, _, err := client.ListMyRepositories(ctx,
		&sourcecraft.ListMyRepositoriesOptions{PageSize: 100, PageToken: token},
	)
	if err != nil {
		return nil, "", err
	}
	return page.Repositories, page.NextPageToken, nil
}
for repo, err := range sourcecraft.Paginate(ctx, fetch) {
	if err != nil {
		return err
	}
	fmt.Println(repo.Slug)
}
```

The iterator stops fetching on `break`, context cancellation, or an error. A repeated page token produces an error instead of an infinite loop.

## Waiting for asynchronous operations

Use `WaitPullRequestMerge`, `WaitOrganizationInvites`, and `WaitSecret` for pull request merges, organization invitations, and secret changes. Pass the ID returned when you started the operation:

```go
operation, _, err := client.WaitPullRequestMerge(ctx, operationID, time.Second)
if err != nil {
	return err
}
fmt.Println(operation.Status)
```

Set a context deadline to bound the total wait. The `failed` and `cancel` statuses return `*sourcecraft.OperationFailedError`. After `WaitOrganizationInvites` succeeds, also check `Response.Errors`: individual invitations may have failed.

## File uploads

Pass an open file through `Upload`. The SDK streams its contents; close the file after the call completes:

```go
file, err := os.Open("report.txt")
if err != nil {
	return err
}
defer func() { _ = file.Close() }()

attachment, _, err := client.UploadIssueAttachment(ctx, "my-org", "my-repo", "1",
	&sourcecraft.Upload{Filename: "report.txt", Reader: file},
)
if err != nil {
	return err
}
fmt.Println(attachment.ID)
```

## Errors and HTTP configuration

Methods return a typed result, HTTP metadata, and an error. Methods without a response model return `(*Response, error)`. The SDK reads and closes the response body.

HTTP statuses outside the 200–299 range return `*sourcecraft.APIError` when the body can be read within the size limit:

```go
if apiErr, ok := errors.AsType[*sourcecraft.APIError](err); ok {
	fmt.Println(apiErr.StatusCode, apiErr.ErrorCode, apiErr.RequestID)
	fmt.Println(string(apiErr.Body))
}
```

Read errors and size-limit errors take precedence over API error decoding. If an HTTP response was received, its status and headers are available in `Response`. Transport errors and context cancellation preserve the underlying cause for `errors.Is`.

Configure the timeout, User-Agent, and response body limit when constructing a client. The default size limit is 32 MiB:

```go
client, err := sourcecraft.NewClient(token,
	sourcecraft.WithHTTPClient(&http.Client{Timeout: time.Minute}),
	sourcecraft.WithUserAgent("my-service/1.0"),
	sourcecraft.WithMaxResponseBytes(64<<20),
)
if err != nil {
	return err
}
```

`WithBaseURL` sets the API address, including an optional path prefix. The client blocks redirects to another origin and does not retry requests automatically. Decide whether to retry based on the error, status, and response headers such as `Retry-After`.

## AppSec

Create a separate security client with `NewAppSecClient`. It uses `DefaultAppSecBaseURL`, accepts the same HTTP options, and shares the `APIError` type:

```go
security, err := sourcecraft.NewAppSecClient(token)
if err != nil {
	return err
}
page, _, err := security.ListDefectGroups(ctx,
	&sourcecraft.AppSecListDefectGroupsOptions{
		GitRepo:  repositoryID,
		PageSize: 100,
		Severity: []string{"HIGH", "CRITICAL"},
	},
)
if err != nil {
	return err
}
for _, defect := range page.Data {
	fmt.Println(defect.UUID, defect.RuleName, defect.FileName)
}
```

Set `GitRepo` to the repository's UUID or internal numeric ID as a string. `ListRepositories` requires `OrgID`. AppSec models and options use the `AppSec` prefix.

List responses contain items in `Data` and a continuation token in `NextPageToken`. Use `Paginate` to traverse them. A zero `PageSize` selects the default of 50; the valid range is 1–250. Set optional pointer query parameters to `nil` to omit them.

## Runnable examples

Run the commands from the repository root. They read `SOURCECRAFT_TOKEN` from the environment. The [examples guide](examples/README.md) describes their arguments and prerequisites:

- [Pull requests](examples/pullrequests/main.go): create, merge, and wait for completion
- [Attachments](examples/attachments/main.go): upload a file and obtain a download URL
- [CI/CD](examples/cicd/main.go): start a workflow, inspect statuses, read paginated logs, and retrieve artifact URLs
- [Repositories](examples/repositories/main.go): iterate over accessible repositories
- [AppSec](examples/appsec/main.go): iterate over defect groups

## Development and API updates

Run the linter, tests, and generation checks from the repository root:

```sh
make lint
go test -race ./...
go vet ./...
go run ./internal/generate -check
go run ./internal/generate -spec api/appsec.openapi.json -check
go run ./internal/apicheck
```

Tests validate requests and models against the pinned specifications, along with HTTP errors, cancellation, pagination, and asynchronous operations. They require no SourceCraft token or calls to its API.

`go generate ./...` regenerates methods and models for both clients from local snapshots. See the [specification guide](api/README.md) for the schema and public Go API update process.

To compare the local snapshots with published specifications, run the separate network check:

```sh
go run ./internal/checkcontracts
```

The command prints the report directory containing a summary and diffs. The [API contracts workflow](.github/workflows/contracts.yml) runs the same check daily and on manual dispatch. It does not update snapshots or the SDK automatically.
