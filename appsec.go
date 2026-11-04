package sourcecraft

import "context"

// DefaultAppSecBaseURL is the public SourceCraft security API endpoint.
const DefaultAppSecBaseURL = "https://appsec.sourcecraft.tech"

// AppSecClient provides the public SourceCraft security API. It is safe for
// concurrent use after construction. Construct it with NewAppSecClient.
// HTTP settings and errors use the same Option, Response and APIError types as
// the platform client. Errors without a documented JSON schema retain their
// original payload in APIError.Body.
type AppSecClient struct{ client *Client }

// NewAppSecClient constructs an AppSec client with a Bearer token and a
// 30-second request timeout. Options, including WithBaseURL, override defaults.
func NewAppSecClient(token string, options ...Option) (*AppSecClient, error) {
	options = append([]Option{WithBaseURL(DefaultAppSecBaseURL)}, options...)

	client, err := NewClient(token, options...)
	if err != nil {
		return nil, err
	}

	return &AppSecClient{client: client}, nil
}

func (c *AppSecClient) transport() *Client {
	if c == nil {
		return nil
	}

	return c.client
}

// uploadReport streams the multipart file and accepts both synchronous 200 and
// asynchronous 202 responses. Neither response has a documented result model.
func (c *AppSecClient) uploadReport(ctx context.Context, path string, opts queryEncoder, file *Upload, fields []formField) (*Response, error) {
	body, contentType, err := multipartBody(file, fields)
	if err != nil {
		return nil, err
	}

	return c.transport().do(ctx, "POST", path, opts, body, contentType, nil)
}
