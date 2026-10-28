package tool

import (
	"context"
	"testing"

	"agentloop/agent/internal/dtypes"
)

// deferredTestTool builds a tool whose schema carries a description, so the
// deferred catalogue exposes searchable text.
func deferredTestTool(name, description string) dtypes.Tool {
	return &testTool{
		name: name,
		schema: dtypes.ToolDef{Type: "function", Function: map[string]any{
			"name":        name,
			"description": description,
		}},
		run: func(context.Context, dtypes.ToolInvocation) (map[string]any, error) {
			return map[string]any{"ok": true}, nil
		},
	}
}

func TestRegistryDeferredToolsAreHiddenUntilExposed(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(dtypes.BackendLocal, deferredTestTool("read_file", "read a file")); err != nil {
		t.Fatalf("register local tool: %v", err)
	}
	if err := registry.RegisterDeferred(dtypes.BackendMCP, "rea", deferredTestTool("rea__analyze_function", "analyze a native function")); err != nil {
		t.Fatalf("register deferred tool: %v", err)
	}

	// The deferred tool stays callable even before it is exposed.
	if _, ok := registry.Get("rea__analyze_function"); !ok {
		t.Fatal("deferred tool must remain retrievable through Get")
	}

	// ListSchemas keeps its old meaning: everything registered.
	if got := len(registry.ListSchemas()); got != 2 {
		t.Fatalf("ListSchemas = %d, want 2", got)
	}

	// The exposed list withholds the deferred tool until it is unlocked.
	exposed := registry.ListExposedSchemas()
	if len(exposed) != 1 {
		t.Fatalf("ListExposedSchemas = %d, want 1 (only the local tool)", len(exposed))
	}
	if got := exposed[0].ToolName(); got != "read_file" {
		t.Fatalf("exposed tool = %q, want read_file", got)
	}

	if !registry.Expose("rea__analyze_function") {
		t.Fatal("Expose must report true for a deferred tool")
	}
	exposed = registry.ListExposedSchemas()
	if len(exposed) != 2 {
		t.Fatalf("ListExposedSchemas after expose = %d, want 2", len(exposed))
	}
}

func TestRegistryExposeRejectsUnknownAndNonDeferredTools(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(dtypes.BackendLocal, deferredTestTool("read_file", "read a file")); err != nil {
		t.Fatalf("register local tool: %v", err)
	}
	if err := registry.RegisterDeferred(dtypes.BackendMCP, "rea", deferredTestTool("rea__analyze_function", "analyze")); err != nil {
		t.Fatalf("register deferred tool: %v", err)
	}
	if registry.Expose("does_not_exist") {
		t.Fatal("Expose must reject an unknown tool")
	}
	if registry.Expose("read_file") {
		t.Fatal("Expose must reject a tool that was not deferred")
	}
}

func TestRegistryExposeIsCaseInsensitive(t *testing.T) {
	registry := NewRegistry()
	if err := registry.RegisterDeferred(dtypes.BackendMCP, "rea", deferredTestTool("rea__analyze_function", "analyze")); err != nil {
		t.Fatalf("register deferred tool: %v", err)
	}
	if !registry.Expose("REA__ANALYZE_FUNCTION") {
		t.Fatal("Expose must resolve names case-insensitively")
	}
	if got := len(registry.ListExposedSchemas()); got != 1 {
		t.Fatalf("ListExposedSchemas = %d, want 1", got)
	}
}

func TestRegistryListDeferredReportsGroupAndDescription(t *testing.T) {
	registry := NewRegistry()
	if err := registry.RegisterDeferred(dtypes.BackendMCP, "rea", deferredTestTool("rea__analyze_function", "analyze a native function")); err != nil {
		t.Fatalf("register deferred tool: %v", err)
	}
	deferred := registry.ListDeferred()
	if len(deferred) != 1 {
		t.Fatalf("ListDeferred = %d, want 1", len(deferred))
	}
	if deferred[0].Name != "rea__analyze_function" {
		t.Fatalf("deferred name = %q", deferred[0].Name)
	}
	if deferred[0].Group != "rea" {
		t.Fatalf("deferred group = %q, want rea", deferred[0].Group)
	}
	if deferred[0].Description != "analyze a native function" {
		t.Fatalf("deferred description = %q", deferred[0].Description)
	}
}

func TestRegistryExposedToolLeavesDeferredCatalogue(t *testing.T) {
	registry := NewRegistry()
	if err := registry.RegisterDeferred(dtypes.BackendMCP, "rea", deferredTestTool("rea__analyze_function", "analyze")); err != nil {
		t.Fatalf("register deferred tool: %v", err)
	}
	if got := len(registry.ListDeferred()); got != 1 {
		t.Fatalf("ListDeferred before expose = %d, want 1", got)
	}
	registry.Expose("rea__analyze_function")
	if got := len(registry.ListDeferred()); got != 0 {
		t.Fatalf("ListDeferred after expose = %d, want 0 (already unlocked)", got)
	}
}

func TestRegistryListExposedSchemasStaysDeterministic(t *testing.T) {
	registry := NewRegistry()
	for _, name := range []string{"zulu", "alpha", "mike"} {
		if err := registry.Register(dtypes.BackendLocal, deferredTestTool(name, "tool "+name)); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	first := registry.ListExposedSchemas()
	second := registry.ListExposedSchemas()
	if len(first) != 3 {
		t.Fatalf("ListExposedSchemas = %d, want 3", len(first))
	}
	for i := range first {
		if first[i].ToolName() != second[i].ToolName() {
			t.Fatalf("ListExposedSchemas is not deterministic at %d: %q vs %q", i, first[i].ToolName(), second[i].ToolName())
		}
	}
	if first[0].ToolName() != "alpha" {
		t.Fatalf("ListExposedSchemas must stay sorted by name, got %q first", first[0].ToolName())
	}
}
