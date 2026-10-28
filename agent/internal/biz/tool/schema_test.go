package tool

import (
	"agentloop/agent/internal/protectedassets"
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

func TestLoadSchemasFallsBackToProtectedBundle(t *testing.T) {
	repo := t.TempDir()
	raw, err := protectedassets.EncodeBundle(map[string][]byte{
		"config/tools/schemas.json": []byte(`{"read_file":{"name":"read_file","description":"protected","parameters":{"type":"object"}}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(repo, filepath.FromSlash(protectedassets.BundleRelPath))
	if err := os.MkdirAll(filepath.Dir(bundlePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bundlePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	schemas, err := LoadSchemas(filepath.Join(repo, "config", "tools", "schemas.json"))
	if err != nil {
		t.Fatal(err)
	}
	if schemas["read_file"].Description != "protected" {
		t.Fatalf("unexpected schema: %#v", schemas["read_file"])
	}
}

func TestRepositorySchemasLoadWhenPlaintextIsAbsent(t *testing.T) {
	repo, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(repo, "config", "tools", "schemas.json")
	if _, err := os.Stat(plain); err == nil {
		t.Skip("repository still uses plaintext schemas")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	schemas, err := LoadSchemas(plain)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"read_file", "plan", "show_result", "web_search"} {
		if _, ok := schemas[name]; !ok {
			t.Fatalf("protected schema bundle is missing %s", name)
		}
	}
}
