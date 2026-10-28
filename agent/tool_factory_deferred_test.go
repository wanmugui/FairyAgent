package main

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestBuildToolRegistryDefersMCPToolSchemasUntilSearched wires the whole path:
// an MCP tool is registered and callable, its schema is withheld from the
// model, and tool_search unlocks it so the schema enters the request.
func TestBuildToolRegistryDefersMCPToolSchemasUntilSearched(t *testing.T) {
	server := newFactoryHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			JSONRPC string `json:"jsonrpc"`
			ID      any    `json:"id"`
			Method  string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		response := map[string]any{"jsonrpc": "2.0", "id": request.ID}
		switch request.Method {
		case "initialize":
			response["result"] = map[string]any{"protocolVersion": "2025-06-18"}
		case "tools/list":
			response["result"] = map[string]any{"tools": []map[string]any{
				{"name": "browser_navigate", "description": "navigate the browser", "inputSchema": map[string]any{"type": "object"}},
			}}
		default:
			response["result"] = map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}}
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Fatal(err)
		}
	}))
	defer server.Close()

	cfg := factoryTestConfig(t, "tool_search")
	cfg.MCPServers = map[string]MCPServerConfig{
		"playwright": {
			Required:   true,
			Transport:  "streamable-http",
			URL:        server.URL,
			ToolPrefix: "pw",
		},
	}
	registry, err := NewToolFactory().BuildRegistry(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()

	// The MCP tool is registered and callable straight away.
	if _, ok := registry.Get("pw__browser_navigate"); !ok {
		t.Fatal("MCP tool must be registered and callable")
	}

	// Its schema is withheld until it is searched for.
	for _, def := range registry.ListExposedSchemas() {
		if def.ToolName() == "pw__browser_navigate" {
			t.Fatal("MCP tool schema must not be exposed before it is searched for")
		}
	}
	if len(registry.ListDeferred()) != 1 {
		t.Fatalf("deferred catalogue = %d, want 1", len(registry.ListDeferred()))
	}

	// tool_search is registered and can reach the deferred catalogue.
	searchTool, ok := registry.Get("tool_search")
	if !ok {
		t.Fatal("tool_search must be registered")
	}
	result, err := searchTool.Execute(t.Context(), ToolInvocation{Args: json.RawMessage(`{"query":"navigate browser"}`)})
	if err != nil {
		t.Fatalf("tool_search failed: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool_search reported an error: %v", result.Value)
	}

	// After the search the tool schema enters the exposed set.
	found := false
	for _, def := range registry.ListExposedSchemas() {
		if def.ToolName() == "pw__browser_navigate" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("the searched MCP tool must be exposed after tool_search returns")
	}
}
