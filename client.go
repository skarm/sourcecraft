package sourcecraft

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is the public SourceCraft API endpoint.
	DefaultBaseURL                = "https://api.sourcecraft.tech"
	defaultMaxResponseBytes int64 = 32 << 20
)

// Client is safe for concurrent use after construction. Configure it with options
// rather than modifying the supplied HTTP client while requests are in flight.
// The zero value is not ready for use; construct a client with NewClient.
type Client struct {
	baseURL          *url.URL
	httpClient       *http.Client
	token            string
	userAgent        string
	maxResponseBytes int64
}

// Option configures a client during construction.
type Option func(*Client) error

// Response contains HTTP status and headers. Its body has already been read and
// closed by the client; use the method's typed result or APIError.Body instead.
type Response struct{ *http.Response }

// APIError describes a non-2xx HTTP response. Body retains the original error
// payload, including when a proxy returns something other than SourceCraft JSON.
// Details is raw JSON because its shape depends on ErrorCode.
type APIError struct {
	StatusCode int
	Method     string
	Path       string
	APIErrorResponse
	Body []byte
}

// EmptyResponse represents a successful JSON object without specified fields.
type EmptyResponse struct{}

type (
	queryEncoder interface{ queryValues() (url.Values, error) }
	pathParam    struct{ name, value string }
)

// NewClient constructs a client with a 30-second request timeout. The token is
// sent as a Bearer token. An empty token omits the Authorization header.
func NewClient(token string, options ...Option) (*Client, error) {
	if strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("sourcecraft: token contains a newline")
	}

	baseURL, _ := url.Parse(DefaultBaseURL)
	c := &Client{
		baseURL:          baseURL,
		httpClient:       &http.Client{Timeout: 30 * time.Second},
		token:            token,
		userAgent:        "sourcecraft-go",
		maxResponseBytes: defaultMaxResponseBytes,
	}

	for _, option := range options {
		if option == nil {
			return nil, errors.New("sourcecraft: nil client option")
		}

		if err := option(c); err != nil {
			return nil, err
		}
	}
	// Copy the client's settings so the caller's CheckRedirect is not modified.
	// Never forward credentials to another origin, including HTTPS downgrades.
	httpClient := *c.httpClient
	originalRedirect := httpClient.CheckRedirect
	httpClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !strings.EqualFold(req.URL.Scheme, c.baseURL.Scheme) || !strings.EqualFold(req.URL.Host, c.baseURL.Host) {
			return http.ErrUseLastResponse
		}

		if originalRedirect != nil {
			redirectURL := req.URL
			err := originalRedirect(req, via)
			// CheckRedirect may rewrite the destination. Check it again before
			// http.Client sends the copied Authorization header. Restore a nil
			// URL so http.Client can also safely wrap a callback error.
			if req.URL == nil {
				req.URL = redirectURL

				if err != nil {
					return err
				}

				return http.ErrUseLastResponse
			}

			if err != nil {
				return err
			}

			if !strings.EqualFold(req.URL.Scheme, c.baseURL.Scheme) || !strings.EqualFold(req.URL.Host, c.baseURL.Host) {
				return http.ErrUseLastResponse
			}

			return nil
		}

		if len(via) >= 10 {
			return errors.New("sourcecraft: stopped after 10 redirects")
		}

		return nil
	}
	c.httpClient = &httpClient

	return c, nil
}

// WithBaseURL sets an API endpoint, optionally including a path prefix.
// Credentials, query parameters and fragments are not accepted in the URL.
func WithBaseURL(rawURL string) Option {
	return func(c *Client) error {
		u, err := url.Parse(rawURL)
		if err != nil {
			return errors.New("sourcecraft: invalid base URL")
		}

		if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
			return errors.New("sourcecraft: base URL must be an absolute HTTP(S) URL without credentials, query or fragment")
		}

		c.baseURL = u

		return nil
	}
}

// WithHTTPClient sets the HTTP transport and timeout. The client settings are
// copied; the transport remains shared and must be safe for concurrent use.
func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *Client) error {
		if httpClient == nil {
			return errors.New("sourcecraft: nil HTTP client")
		}

		c.httpClient = httpClient

		return nil
	}
}

// WithUserAgent overrides the default User-Agent header.
func WithUserAgent(userAgent string) Option {
	return func(c *Client) error {
		if strings.ContainsAny(userAgent, "\r\n") {
			return errors.New("sourcecraft: user agent contains a newline")
		}

		c.userAgent = userAgent

		return nil
	}
}

// WithMaxResponseBytes sets the maximum JSON response size. This bound applies
// to successful and error responses, and must be positive.
func WithMaxResponseBytes(limit int64) Option {
	return func(c *Client) error {
		if limit <= 0 || limit == int64(^uint64(0)>>1) {
			return errors.New("sourcecraft: response size limit must be positive and less than MaxInt64")
		}

		c.maxResponseBytes = limit

		return nil
	}
}

// Ptr returns a pointer to a value, for optional fields in request bodies.
//
// Deprecated: use new(value).
//
//go:fix inline
func Ptr[T any](value T) *T { return new(value) }

func (e *APIError) Error() string {
	message := e.Message
	if message == "" {
		message = http.StatusText(e.StatusCode)
	}

	if e.ErrorCode != "" {
		message = e.ErrorCode + ": " + message
	}

	return fmt.Sprintf("sourcecraft: %s %s: HTTP %d: %s", e.Method, e.Path, e.StatusCode, message)
}

func (c *Client) do(ctx context.Context, method, path string, opts queryEncoder, body io.Reader, contentType string, result any) (*Response, error) {
	if c == nil || c.baseURL == nil || c.httpClient == nil {
		return nil, errors.New("sourcecraft: client must be constructed with NewClient")
	}

	u := *c.baseURL
	escapedPath := strings.TrimRight(u.EscapedPath(), "/") + path

	var err error

	u.Path, err = url.PathUnescape(escapedPath)
	if err != nil {
		return nil, fmt.Errorf("sourcecraft: build URL: %w", err)
	}

	u.RawPath = escapedPath

	if opts != nil {
		query, err := opts.queryValues()
		if err != nil {
			return nil, err
		}

		u.RawQuery = query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("sourcecraft: create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")

	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}

	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}

	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	rawResponse, err := c.httpClient.Do(req)
	if err != nil {
		var response *Response
		if rawResponse != nil {
			response = &Response{rawResponse}
		}

		return response, fmt.Errorf("sourcecraft: HTTP request: %w", err)
	}

	response := &Response{rawResponse}
	defer func(body io.Closer) {
		// Preserve the read result; closing an already-consumed body is best effort.
		_ = body.Close()
	}(rawResponse.Body)

	reader := io.LimitReader(rawResponse.Body, c.maxResponseBytes+1)

	var (
		data []byte
		size int64
	)

	if result == nil && rawResponse.StatusCode >= 200 && rawResponse.StatusCode < 300 {
		if rawResponse.Body != http.NoBody {
			size, err = io.Copy(io.Discard, reader)
		}
	} else {
		data, err = readResponseBody(reader, rawResponse.ContentLength, c.maxResponseBytes)
		size = int64(len(data))
	}

	if err != nil {
		return response, fmt.Errorf("sourcecraft: read response: %w", err)
	}

	if size > c.maxResponseBytes {
		return response, fmt.Errorf("sourcecraft: response exceeds %d bytes", c.maxResponseBytes)
	}

	if rawResponse.StatusCode < 200 || rawResponse.StatusCode >= 300 {
		apiError := &APIError{StatusCode: rawResponse.StatusCode, Method: method, Path: u.EscapedPath(), Body: data}
		// Preserve the HTTP error even if the response is HTML or malformed JSON.
		_ = json.Unmarshal(data, &apiError.APIErrorResponse)
		if apiError.RequestID == "" {
			apiError.RequestID = rawResponse.Header.Get("X-Request-ID")
		}

		return response, apiError
	}

	if result == nil {
		return response, nil
	}

	if len(bytes.TrimSpace(data)) == 0 {
		return response, fmt.Errorf("sourcecraft: decode response: %w", io.ErrUnexpectedEOF)
	}

	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return response, errors.New("sourcecraft: expected a JSON object, received null")
	}

	if err := json.Unmarshal(data, result); err != nil {
		return response, fmt.Errorf("sourcecraft: decode response: %w", err)
	}

	return response, nil
}

func doJSON[T any](ctx context.Context, c *Client, method, path string, opts queryEncoder, body any) (*T, *Response, error) {
	reader, err := encodeJSON(body)
	if err != nil {
		return nil, nil, err
	}

	var result T

	response, err := c.do(ctx, method, path, opts, reader, "application/json", &result)
	if err != nil {
		return nil, response, err
	}

	return &result, response, nil
}

func doEmpty(ctx context.Context, c *Client, method, path string, opts queryEncoder, body any) (*Response, error) {
	reader, err := encodeJSON(body)
	if err != nil {
		return nil, err
	}

	return c.do(ctx, method, path, opts, reader, "application/json", nil)
}

func encodeJSON(body any) (io.Reader, error) {
	if body == nil {
		return nil, nil
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("sourcecraft: encode request: %w", err)
	}

	return bytes.NewReader(data), nil
}

// readResponseBody uses a small length hint without trusting it as a bound.
// Larger hints use ReadAll, and understated hints fall back to the same reader
// after replaying the prefix. The caller's reader enforces the response limit.
func readResponseBody(reader io.Reader, lengthHint, limit int64) ([]byte, error) {
	if lengthHint < 4<<10 || lengthHint > 64<<10 || lengthHint > limit {
		return io.ReadAll(reader)
	}

	var buffer bytes.Buffer
	buffer.Grow(int(lengthHint) + bytes.MinRead + 1)

	_, err := buffer.ReadFrom(io.LimitReader(reader, lengthHint+1))
	if err != nil || int64(buffer.Len()) <= lengthHint {
		return buffer.Bytes(), err
	}

	return io.ReadAll(io.MultiReader(bytes.NewReader(buffer.Bytes()), reader))
}

func expandPath(template string, params ...pathParam) (string, error) {
	for _, p := range params {
		if p.value == "" || p.value == "." || p.value == ".." {
			return "", fmt.Errorf("sourcecraft: invalid path parameter %s", p.name)
		}
	}
	// One replacement already produces just one path allocation. Build paths
	// with several parameters once instead of copying the full path repeatedly.
	if len(params) <= 1 {
		for _, p := range params {
			template = strings.ReplaceAll(template, "{"+p.name+"}", url.PathEscape(p.value))
		}

		return template, nil
	}
	// API paths currently have at most six parameters. append also supports
	// longer paths; the small local array avoids an allocation in common cases.
	var local [8]string

	escaped := local[:0]
	size := len(template)

	for _, p := range params {
		value := url.PathEscape(p.value)
		escaped = append(escaped, value)
		size += max(0, len(value)-len(p.name)-2)
	}

	var b strings.Builder
	b.Grow(size)

	for {
		before, after, ok := strings.Cut(template, "{")
		if !ok {
			b.WriteString(template)
			break
		}

		b.WriteString(before)

		matched := false

		for i, p := range params {
			if len(after) > len(p.name) && after[len(p.name)] == '}' && strings.HasPrefix(after, p.name) {
				b.WriteString(escaped[i])

				template = after[len(p.name)+1:]
				matched = true

				break
			}
		}

		if !matched {
			b.WriteByte('{')

			template = after
		}
	}

	return b.String(), nil
}
