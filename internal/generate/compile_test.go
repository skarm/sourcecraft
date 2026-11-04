package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The fixtures exercise generated code independently of the SDK's full schema.
// This stub supplies only the runtime declarations referenced by that code.
const generatedRuntimeStub = `package sourcecraft
import "context"
type Client struct{}
type AppSecClient struct { client *Client }
func (c *AppSecClient) transport() *Client { return c.client }
func (c *AppSecClient) uploadReport(context.Context,string,any,*Upload,[]formField) (*Response,error) { return nil,nil }
type Response struct{}
type EmptyResponse struct{}
type Upload struct{}
type formField struct { name, value string }
type pathParam struct { name, value string }
func expandPath(template string, params ...pathParam) (string,error) { return template,nil }
func doJSON[T any](ctx context.Context, client *Client, method,path string,opts,body any) (*T,*Response,error) { return nil,nil,nil }
func doEmpty(ctx context.Context, client *Client, method,path string,opts,body any) (*Response,error) { return nil,nil }
func doUpload[T any](ctx context.Context, client *Client, method,path string,opts any,file *Upload,fields []formField) (*T,*Response,error) { return nil,nil,nil }
`

func testGeneratedPackage(t *testing.T, outputs map[string][]byte, testSource string) {
	t.Helper()
	testGeneratedPackageWithRuntime(t, outputs, testSource, generatedRuntimeStub)
}

func testGeneratedPackageWithRuntime(t *testing.T, outputs map[string][]byte, testSource, runtimeSource string) {
	t.Helper()
	dir := t.TempDir()
	for name, data := range outputs {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range map[string]string{
		"go.mod":          "module example.test/generated\n\ngo 1.26.0\n",
		"runtime.go":      runtimeSource,
		"fixture_test.go": testSource,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.CommandContext(t.Context(), "go", "test", ".")
	command.Dir = dir
	command.Env = append(os.Environ(), "GOWORK=off")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated package failed compilation or tests: %v\n%s", err, output)
	}
}
