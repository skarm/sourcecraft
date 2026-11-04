// Package sourcecraft provides a typed client for the SourceCraft public REST API.
//
// Create a client with NewClient and a personal access token. An empty token can
// be used to omit authentication. All requests accept a context and return HTTP
// response metadata alongside the decoded result. Non-2xx responses whose bodies
// were read within the configured size limit are returned as *APIError, which can
// be inspected with errors.AsType[*APIError]. Response-read and size-limit errors
// are returned with HTTP metadata when a response has already been received.
//
// AppSecClient provides the separate security API with AppSec-prefixed models
// and options. Both clients share HTTP configuration and error types.
//
// API methods and models are generated from checked-in official Swagger and
// AppSec OpenAPI specifications. Run go generate without network access.
package sourcecraft

//go:generate go run ./internal/generate
//go:generate go run ./internal/generate -spec api/appsec.openapi.json
