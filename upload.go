package sourcecraft

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"strings"
)

// Upload supplies one multipart file. Reader is streamed without buffering the
// file in memory. The caller owns Reader and is responsible for closing it.
type Upload struct {
	Filename string
	Reader   io.Reader
}

type formField struct{ name, value string }

func doUpload[T any](ctx context.Context, c *Client, method, path string, opts queryEncoder, file *Upload, fields []formField) (*T, *Response, error) {
	body, contentType, err := multipartBody(file, fields)
	if err != nil {
		return nil, nil, err
	}

	var result T

	response, err := c.do(ctx, method, path, opts, body, contentType, &result)
	if err != nil {
		return nil, response, err
	}

	return &result, response, nil
}

func multipartBody(file *Upload, fields []formField) (io.Reader, string, error) {
	if file == nil || file.Reader == nil {
		return nil, "", errors.New("sourcecraft: upload and its reader are required")
	}

	if file.Filename == "" || strings.ContainsAny(file.Filename, "\r\n") {
		return nil, "", errors.New("sourcecraft: upload filename must be non-empty and contain no newlines")
	}

	var framing bytes.Buffer

	writer := multipart.NewWriter(&framing)
	for _, field := range fields {
		if err := writer.WriteField(field.name, field.value); err != nil {
			return nil, "", fmt.Errorf("sourcecraft: multipart field: %w", err)
		}
	}

	if _, err := writer.CreateFormFile("file", file.Filename); err != nil {
		return nil, "", fmt.Errorf("sourcecraft: multipart file: %w", err)
	}

	prefixLength := framing.Len()

	if err := writer.Close(); err != nil {
		return nil, "", err
	}

	data := framing.Bytes()
	// MultiReader avoids a pipe goroutine and reads file.Reader only when the
	// HTTP transport requests bytes. Only headers and boundaries are buffered.
	reader := io.MultiReader(bytes.NewReader(data[:prefixLength]), file.Reader, bytes.NewReader(data[prefixLength:]))

	return reader, writer.FormDataContentType(), nil
}
