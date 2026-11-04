# Runnable examples

[English](README.md) · [Русский](README.ru.md)

Run these commands from the repository root with Go 1.26 or later. Requests require a personal access token (PAT) with access to the target repository. In SourceCraft CI/CD, you can use the provided `SOURCECRAFT_TOKEN`. The examples read the token only from the environment:

```sh
export SOURCECRAFT_TOKEN='your_access_token_here'
```

`-org` and `-repo` are organization and repository slugs. `-pr`, `-issue`, and `-run` are resource numbers within that repository, passed as strings. The `pullrequests`, `attachments`, and `cicd` examples support `-h`, a total `-timeout`, and cancellation with Ctrl+C.

## Creating and merging pull requests

[pullrequests/main.go](pullrequests/main.go) has separate commands for creating a pull request and merging an existing one. Both branches must already exist in SourceCraft, pushed through Git.

```sh
go run ./examples/pullrequests create \
  -org my-org -repo my-repo \
  -source feature -target main \
  -title 'Add feature' -description 'Description of the change'
```

The command prints the pull request number, ID, and status. It publishes the pull request by default; pass `-draft` to create a draft. Creating a pull request does not start a merge.

Use the printed pull request number to merge it:

```sh
go run ./examples/pullrequests merge \
  -org my-org -repo my-repo -pr 42 \
  -squash=true -timeout 5m
```

The example calls `MergePullRequest`, prints the operation ID, and waits with `WaitPullRequestMerge`. On success, it prints the merge commit hash and the target branch commit hash used as the merge base. Validation and asynchronous operation errors are returned to the calling process. The example does not request a forced merge.

Omitting `-squash` and `-delete-source` leaves the corresponding fields `nil`, preserving the repository's merge settings. Explicit `-squash=false` and `-delete-source=false` send `false`. To delete the source branch after merging, pass `-delete-source=true`.

```sh
go run ./examples/pullrequests create -h
go run ./examples/pullrequests merge -h
```

## Uploading an issue attachment

[attachments/main.go](attachments/main.go) opens a local file, streams it through `UploadIssueAttachment`, and closes the reader on exit. It prints the attachment ID, name, and size, then calls `GetIssueAttachment` to retrieve a temporary download URL.

```sh
go run ./examples/attachments \
  -org my-org -repo my-repo -issue 7 \
  -file ./report.txt -timeout 2m
```

The issue must already exist. Retrieving the URL is a separate request: if it fails, the file may already have been uploaded. The error message retains the attachment ID, and the upload is not retried automatically. The example prints the URL; it does not download the file.

## Starting CI/CD and retrieving results

[cicd/main.go](cicd/main.go) starts one workflow by its name in `.sourcecraft/ci.yaml`, waits for the run to finish, and reads workflows, tasks, cubes, log pages, and fresh artifact URLs.

```sh
go run ./examples/cicd \
  -org my-org -repo my-repo -workflow checks \
  -branch feature -config-branch main \
  -input environment=staging -input publish=false \
  -timeout 15m -interval 2s
```

The workflow must exist in the selected configuration, and SourceCraft settings must allow it to be started this way. `-branch` selects the code branch; `-config-branch` selects the CI configuration branch. If either flag is omitted, the corresponding revision uses the repository's default branch. Repeat `-input` to pass multiple inputs. Empty values (`-input name=`) and `=` characters inside values are preserved.

The run number is printed immediately after the request. To inspect an existing run, including retrieving results again after a timeout, use:

```sh
go run ./examples/cicd \
  -org my-org -repo my-repo -run 123 -timeout 15m
```

The `-run` mode issues read requests without starting another CI run. It rejects `-workflow`, `-branch`, `-config-branch`, and `-input`.

The example waits through `created`, `prepared`, `processing`, and `awaiting_approval` within the total timeout. It retrieves results after a terminal status, including unsuccessful runs. Exit code 0 requires `success` and no result-retrieval errors. Other terminal statuses or retrieval failures produce a nonzero exit code.

Logs are read page by page. `done` applies to the entire cube, so the first page with `done=true` may still have successors. `page_complete` determines whether to request another page. If logs are still being written, the command reports this and suggests retrieving the run again with `-run`. A missing next page after a complete final page ends the log stream; an error on the first page is not hidden. See the [GetCubeLogs contract](https://sourcecraft.dev/portal/docs/en/api-ref/CICD/GetCubeLogs).

An error reading one cube's logs is retained while the command continues retrieving artifacts and results for other cubes. Cubes that did not execute because they were skipped, rejected, or awaiting execution or approval are printed without fetching their output. Artifact URLs are temporary, and files are not downloaded automatically.

The timeout and Ctrl+C stop local waiting. A merge or CI operation already accepted by the server may continue running.

## Other examples

These examples traverse paginated lists:

- [repositories](repositories/main.go): accessible repositories
- [appsec](appsec/main.go): defect groups selected by repository UUID or ID

`go test ./...` compiles all examples. CI/CD tests use a test HTTP transport and virtual time to check approval waits, timeouts, completed-log pagination, output errors, and continued result collection.
