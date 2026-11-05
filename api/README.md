# API specifications

[English](README.md) · [Русский](README.ru.md)

`sourcecraft.swagger.json` is a snapshot of the official SourceCraft public REST API specification, downloaded on November 4, 2026 from the [Swagger endpoint](https://api.sourcecraft.tech/sourcecraft.swagger.json). See the [API and authentication guide](https://sourcecraft.dev/portal/docs/en/sourcecraft/operations/api-start) for access details.

The document version is `0.0.1`. That version alone does not identify its operation set: the SDK is pinned to the saved file. The current snapshot contains 268 operations and 282 model definitions, including event models. Operations tagged `Withdrawn` are retained for compatibility and marked `Deprecated`.

The generator supports multipart `requestBody` even though the main document uses Swagger 2.0. Attaching a release asset by tag requires a separate `name` field.

Update the snapshot explicitly:

```sh
curl --fail --show-error --location https://api.sourcecraft.tech/sourcecraft.swagger.json -o api/sourcecraft.swagger.json
go generate ./...
go test ./...
```

Building and generating the SDK require Go 1.26 or later. With a compatible toolchain installed, both work without network access. The generator validates its supported schema subset; review generated changes before releasing an API update.

## AppSec OpenAPI

`appsec.openapi.json` is the official JSON snapshot of the separate AppSec security API, using OpenAPI 3.1.0. It was downloaded on November 4, 2026. The [AppSec endpoint](https://appsec.sourcecraft.tech/openapi) returns JSON with `Accept: application/json` and YAML without that header. The snapshot contains 27 operations and 66 model definitions and is saved without content conversion.

Update AppSec explicitly:

```sh
curl --fail --show-error --location -H 'Accept: application/json' https://appsec.sourcecraft.tech/openapi -o api/appsec.openapi.json
go generate ./...
go test ./...
go run ./internal/generate -spec api/appsec.openapi.json -check
```

The AppSec schema has no `operationId` values. Stable Go operation names are defined in `internal/generate/appsec.go`. An unknown route stops generation so new operations cannot be silently skipped. AppSec outputs are `appsec_api_gen.go` and `appsec_models_gen.go`. The shared CLI detects `swagger` or `openapi` and uses a separate AppSec compiler built on the Go standard library. Updating one snapshot does not rewrite the other API's output files.

The compiler supports nullable model properties, property-level `anyOf` with `null`, `allOf` for enums with defaults, cyclic references, property dictionaries containing arbitrary JSON, numeric IDs, and multipart bodies with optional fields. It implements a specialized OpenAPI 3.1 subset. Omit nullable query parameters with `nil` to let the server apply defaults; explicit `0`, `false`, and empty strings are preserved. For query fields represented by Go values, the generator substitutes the declared default for the zero value. The server validates request constraints that the SDK does not check.

Generation rejects a nullable response as a whole, a dictionary at the root of a model definition, and incompatible success responses for one method, such as typed JSON for `200` with no body for `202`. These constructs need separate support. Report uploads declare untyped JSON for `200` and an empty `202`; both are supported. Regression tests verify that unsupported changes fail before files are written.

AppSec tests read the pinned specification independently, call all 27 methods, and check all 66 models, nullable fields, numeric precision, and both report-upload statuses (`200` and `202`). Additional small OpenAPI fixtures compile as independent Go packages. Tests do not call the authenticated live API.

## Platform Swagger compiler

`internal/generate` is one internal command package. `main.go` handles flags, reads the schema, and checks or writes output files. The `generate` function in `generate.go` accepts JSON and prepares both Go files in memory. Schema and type rules are in `schema.go`, naming in `names.go`, models in `models.go`, and methods and query parameters in `operations.go`.

Both files are prepared and output paths are checked before writing starts. Directories, dangling symbolic links, outputs aliasing the input specification, and outputs aliasing one another are rejected before either file is changed. Existing symbolic links to independent regular files remain supported. The writes are not a filesystem transaction: a disk failure or another program changing the files may require regeneration.

Unsupported schema composition (`allOf`, `anyOf`, `oneOf`) is rejected. String-encoded integers are supported in numeric model fields and query parameters; scalar JSON bodies/responses, collection elements and string enum definitions using those formats require separate support. Rejecting these constructs prevents generation of code that silently loses data or uses the wrong JSON representation. AppSec also rejects nullable path parameters.

An `operationId` must be a valid exported Go identifier so SDK consumers can call the method. Declaration validation rejects collisions across output files and with the handwritten SDK runtime. The compiler also rejects JSON names that the standard encoder cannot represent with a struct tag, and objects that mix static properties with dynamic keys. The `AppSec` type and constant prefix is reserved for the security API, so the platform schema cannot declare names in that namespace.

Tests compile small independent schemas covering GET, JSON with dates, POST with query parameters, DELETE, and multipart. They supplement the pinned-snapshot check, detect unnecessary imports, and verify that `-check` does not change the SDK.

## Public Go type stability

Canonical platform models follow the explicit policy in `internal/generate/platform_types.json`, embedded through `go:embed`. The original optional-field representation is preserved for 241 existing object definitions; 64 retain pointers for optional scalars. New models default to value types for optional scalars. Traversing API request usage does not change this policy or existing models.

When request usage needs pointers to distinguish omitted fields from zero values, the compiler creates a separate `<Model>Input` projection. Projections propagate through nested objects, arrays, dictionaries, and cyclic references. New methods accept the input projection, while responses use the canonical model. For example, adding a method with a `Branch` body creates `BranchInput.Name *string` and preserves `Branch.Name string`. A projection name that collides with an existing declaration is rejected before writing files. AppSec uses its own stable rules.

The [public-api.golden](public-api.golden) snapshot records 1149 public API entries across both clients: types, exported fields and their order and tags, signatures and methods, exact constant values, aliases, and type-parameter constraints. It includes accessible fields and methods exposed through embedding or aliases, including private embedded types. It also checks comparability: changing a private field can make a public type unusable with `==` or as a map key. Argument names, including nested callback arguments, documentation, and private fields alone are not treated as public contracts.

Run the compatibility check:

```sh
go run ./internal/apicheck
```

The checker reads actual compiler export data through `go/types`. It rejects changes to or removal of recorded declarations. New declarations are allowed; adding a field to an existing struct requires review and a baseline update. This is a conservative source API check. It does not classify server-side behavior changes. Both CI workflows and the tests run it.

After reviewing changes, update the snapshot explicitly:

```sh
go run ./internal/apicheck -update
```

Include added declarations in the snapshot after review so later changes are checked too. `go generate`, ordinary checks, and CI do not rewrite the representation policy or baseline. Changing existing model rules, or updating the baseline to accept an incompatibility, requires a separate migration decision for consumers.

## Checking published contracts

[contracts.json](contracts.json) lists every snapshot in `api/`, its stable name, and its official URL. It currently contains the platform Swagger and AppSec OpenAPI snapshots; event models are part of the platform Swagger. Add every new JSON snapshot to this manifest. The checker rejects unregistered snapshots.

The [API contracts workflow](../.github/workflows/contracts.yml) supports manual `workflow_dispatch` and runs daily on the `23 4 * * *` schedule (04:23 UTC). It is separate from the main CI workflow and needs no SourceCraft PAT. Alongside downloading specifications, it runs `go test -count=1 ./...`, including every method and model contract and checks that generated files match both snapshots. Local tests and report publication also run when a contract changes or a download fails.

To run the check locally, install Go 1.26 or later and Git:

```sh
go run ./internal/checkcontracts
go run ./internal/checkcontracts -out /tmp/sourcecraft-contracts
```

The report directory must differ from the snapshot directory. Before cleanup, the checker rejects report files that alias an input snapshot or the manifest, including symbolic and hard links. On each recheck, the old summary, diffs and canonical snapshots are removed, so a failed download cannot leave stale data in the new report.

Without `-out`, the command creates a temporary directory. It compares each complete document: routes, methods, models and events, fields and enums, parameters, request bodies, responses, authentication, versions, and descriptions. It ignores whitespace and object-key order. Array order and numeric text are preserved; large numbers are not converted to `float64`. Any difference, including a description change, counts as contract drift. The command does not classify changes as breaking or non-breaking.

Duplicate JSON object names are rejected as ambiguous instead of silently discarding earlier values.

For each successfully downloaded contract, the report contains `<name>.local.json` and `<name>.published.json`, plus `<name>.diff` when the documents differ. The shared `summary.md` distinguishes `unchanged`, `CHANGED`, and `ERROR`. Every contract is checked even if another fails. Both drift and download or parsing errors produce a nonzero exit code; a network failure never counts as a matching schema. Downloads have a 30-second HTTP timeout and a 32 MiB size limit.

GitHub Actions publishes the report in the job summary and the `api-contracts` artifact, retained for 14 days. Snapshots and the SDK are not updated automatically. Review the diff, then update explicitly using the commands above.
