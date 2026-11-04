//nolint:testpackage // These tests share white-box helpers for private SDK behavior.
package sourcecraft

import (
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"strings"
	"testing"
)

type failingUploadReader struct{ reads int }

func TestUploadStreamsAndPropagatesReadErrors(t *testing.T) {
	file := &failingUploadReader{}
	reader, contentType, err := multipartBody(&Upload{Filename: "report.txt", Reader: file}, []formField{{"name", "report"}})
	if err != nil {
		t.Fatal(err)
	}
	if file.reads != 0 {
		t.Fatal("upload file was read before sending")
	}
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatal(err)
	}
	parts := multipart.NewReader(reader, params["boundary"])
	name, err := parts.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	nameData, err := io.ReadAll(name)
	if err != nil || string(nameData) != "report" {
		t.Fatalf("name=%s error=%v", nameData, err)
	}
	part, err := parts.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(part)
	if !errors.Is(err, io.ErrUnexpectedEOF) || file.reads == 0 {
		t.Fatalf("error=%v reads=%d", err, file.reads)
	}
}

func TestInvalidUploadInputs(t *testing.T) {
	for _, file := range []*Upload{nil, {}, {Filename: "x"}, {Filename: "", Reader: strings.NewReader("x")}, {Filename: "bad\r\nheader", Reader: strings.NewReader("x")}} {
		if _, _, err := multipartBody(file, nil); err == nil {
			t.Fatal("accepted invalid upload")
		}
	}
	client, err := NewClient("")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.AddReleaseAssetAttachment(t.Context(), "org", "repo", "tag", "", &Upload{Filename: "x", Reader: strings.NewReader("x")}); err == nil {
		t.Fatal("accepted missing release asset name")
	}
}

func (r *failingUploadReader) Read([]byte) (int, error) { r.reads++; return 0, io.ErrUnexpectedEOF }
