package tool

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSchemasStripsBOM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schemas.json")
	raw := append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"read_file":{"name":"read_file","description":"read","parameters":{"type":"object"}}}`)...)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	schemas, err := LoadSchemas(path)
	if err != nil {
		t.Fatal(err)
	}
	schema, ok := schemas["read_file"]
	if !ok || schema.Name != "read_file" || schema.Description != "read" {
		t.Fatalf("unexpected schema: %#v", schema)
	}
	parameters, ok := schema.Parameters.(map[string]any)
	if !ok || parameters["type"] != "object" {
		t.Fatalf("expected object parameters to be loaded, got %#v", schema.Parameters)
	}
}
