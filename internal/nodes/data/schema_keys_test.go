package data

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"monoagent/internal/workflow"
)

// TestCompressionUsesSchemaOperationsAndKeys is a regression test: the schema
// (data.compression.json) uses "input_field" and operations gzip/gunzip/zip/unzip;
// the code previously read "field" and expected gzip_compress/zip_compress etc,
// making the node 100% non-functional through the UI.
func TestCompressionUsesSchemaOperationsAndKeys(t *testing.T) {
	node := &CompressionNode{}
	raw := base64.StdEncoding.EncodeToString([]byte("hello world"))

	// gzip round trip
	out, err := node.Execute(context.Background(), workflow.NodeInput{
		Items: []workflow.Item{{JSON: map[string]interface{}{"data": raw}}},
	}, map[string]interface{}{"operation": "gzip", "input_field": "data", "output_field": "compressed"})
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	compressed, _ := out[0].Items[0].JSON["compressed"].(string)
	if compressed == "" {
		t.Fatal("gzip: expected non-empty compressed output")
	}

	out, err = node.Execute(context.Background(), workflow.NodeInput{
		Items: []workflow.Item{{JSON: map[string]interface{}{"data": compressed}}},
	}, map[string]interface{}{"operation": "gunzip", "input_field": "data", "output_field": "plain"})
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	plainB64, _ := out[0].Items[0].JSON["plain"].(string)
	plain, err := base64.StdEncoding.DecodeString(plainB64)
	if err != nil {
		t.Fatalf("decode plain: %v", err)
	}
	if string(plain) != "hello world" {
		t.Errorf("gunzip round trip: got %q, want %q", plain, "hello world")
	}
}

// TestHTMLNodeUsesCodeOperationNames is a regression test: the schema was
// rewritten to match the code's real operations (extract/extract_all/text/generate)
// since sanitize/extract_links were never implemented.
func TestHTMLNodeUsesCodeOperationNames(t *testing.T) {
	node := &HTMLNode{}
	out, err := node.Execute(context.Background(), workflow.NodeInput{
		Items: []workflow.Item{{JSON: map[string]interface{}{"html": "<div><p>Hello</p></div>"}}},
	}, map[string]interface{}{"operation": "extract", "field": "html", "selector": "p", "output_field": "text"})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if got := out[0].Items[0].JSON["text"]; got != "Hello" {
		t.Errorf("extract: got %v, want %q", got, "Hello")
	}
}

// TestWriteBinaryFileUsesSchemaKeys is a regression test: the schema uses
// "file_path" and "field", matching the code (previously the schema exposed
// "path"/"data_field", which the code never read, so file_path was always
// empty and every run errored).
func TestWriteBinaryFileUsesSchemaKeys(t *testing.T) {
	node := &WriteBinaryFileNode{}
	dst := filepath.Join(t.TempDir(), "out.txt")
	_, err := node.Execute(context.Background(), workflow.NodeInput{
		Items: []workflow.Item{{JSON: map[string]interface{}{"content": "hi there"}}},
	}, map[string]interface{}{"file_path": dst, "field": "content", "encoding": "utf8"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(got) != "hi there" {
		t.Errorf("file content = %q, want %q", got, "hi there")
	}
}

// TestCryptoUsesSchemaOperationNames is a regression test: the schema uses
// hash_md5/hash_sha256/hash_sha512, which the code previously didn't implement
// (it had md5/sha256/sha512 instead).
func TestCryptoUsesSchemaOperationNames(t *testing.T) {
	node := &CryptoNode{}
	out, err := node.Execute(context.Background(), workflow.NodeInput{
		Items: []workflow.Item{{JSON: map[string]interface{}{"password": "secret"}}},
	}, map[string]interface{}{"operation": "hash_sha256", "field": "password", "output_field": "hashed"})
	if err != nil {
		t.Fatalf("hash_sha256: %v", err)
	}
	if got, _ := out[0].Items[0].JSON["hashed"].(string); len(got) != 64 {
		t.Errorf("hash_sha256: got %q (len %d), want a 64-char hex digest", got, len(got))
	}
}
