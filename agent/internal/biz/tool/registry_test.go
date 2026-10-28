package tool

import (
	"agentloop/agent/internal/dtypes"
	"context"
	"reflect"
	"testing"
)

func registryTestTool(name string) dtypes.Tool {
	return newTestTool(name, func(_ context.Context, _ dtypes.ToolInvocation) (map[string]any, error) {
		return map[string]any{"tool": name}, nil
	})
}

func TestToolRegistryLookupIsCaseInsensitive(t *testing.T) {
	registry := NewRegistry()
	tool := registryTestTool("read_file")
	if err := registry.Register(dtypes.BackendLocal, tool); err != nil {
		t.Fatal(err)
	}

	got, ok := registry.Get("READ_FILE")
	if !ok {
		t.Fatal("expected case-insensitive lookup to find read_file")
	}
	if got.Name() != "read_file" {
		t.Fatalf("unexpected tool: %s", got.Name())
	}
	if backend, ok := registry.GetBackend("Read_File"); !ok || backend != dtypes.BackendLocal {
		t.Fatalf("unexpected backend: %q, found=%v", backend, ok)
	}
}

func TestToolRegistryRejectsDuplicateNames(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(dtypes.BackendLocal, registryTestTool("echo")); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(dtypes.BackendHTTP, registryTestTool("echo")); err == nil {
		t.Fatal("expected duplicate tool name to be rejected")
	}
}

func TestToolRegistryRejectsCaseConflict(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(dtypes.BackendLocal, registryTestTool("read_file")); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(dtypes.BackendHTTP, registryTestTool("READ_FILE")); err == nil {
		t.Fatal("expected case-conflicting tool name to be rejected")
	}
}

func TestToolRegistryListSchemasIsDeterministic(t *testing.T) {
	registry := NewRegistry()
	for _, name := range []string{"write_file", "read_file", "glob"} {
		if err := registry.Register(dtypes.BackendLocal, registryTestTool(name)); err != nil {
			t.Fatal(err)
		}
	}

	if got, want := registry.ListNames(), []string{"glob", "read_file", "write_file"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected names: got %#v want %#v", got, want)
	}

	var gotSchemaNames []string
	for _, schema := range registry.ListSchemas() {
		fn, ok := schema.Function.(map[string]any)
		if !ok {
			t.Fatalf("schema function has unexpected type %T", schema.Function)
		}
		gotSchemaNames = append(gotSchemaNames, fn["name"].(string))
	}
	if want := []string{"glob", "read_file", "write_file"}; !reflect.DeepEqual(gotSchemaNames, want) {
		t.Fatalf("unexpected schema order: got %#v want %#v", gotSchemaNames, want)
	}
}

func TestToolRegistryReturnsOnlyRegisteredSchemas(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(dtypes.BackendLocal, registryTestTool("read_file")); err != nil {
		t.Fatal(err)
	}
	if got := len(registry.ListSchemas()); got != 1 {
		t.Fatalf("expected one registered schema, got %d", got)
	}
}
