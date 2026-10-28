package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"agentloop/agent/internal/dtypes"
)

// schemaBytesOf measures the JSON footprint of a tool definition, which is what
// the model actually pays for on every request.
func schemaBytesOf(def dtypes.ToolDef) int {
	raw, err := json.Marshal(def)
	if err != nil {
		return 0
	}
	return len(raw)
}

// TestMeasureDeferredSchemaFootprint reports how much request payload the
// deferred-tool design removes. It is a measurement, not a gate: the exact
// numbers depend on which MCP servers are reachable in the test environment,
// so it logs instead of failing when a server is unavailable.
func TestMeasureDeferredSchemaFootprint(t *testing.T) {
	if os.Getenv("FAIRY_LIVE_MCP_TEST") != "1" {
		t.Skip("set FAIRY_LIVE_MCP_TEST=1 to measure against live MCP servers")
	}
	configPath := os.Getenv("FAIRY_CONFIG_PATH")
	if configPath == "" {
		configPath = "config/config.json"
	}
	repoRoot := findRepoRoot(configPath)
	cfg, err := LoadConfig(repoRoot, configPath)
	if err != nil {
		t.Skipf("repo config unavailable: %v", err)
	}
	registry, err := NewToolFactory().BuildRegistry(cfg)
	if err != nil {
		t.Skipf("tool registry unavailable: %v", err)
	}
	defer func() { _ = registry.Close() }()

	all := registry.ListSchemas()
	exposed := registry.ListExposedSchemas()

	allBytes := 0
	for _, def := range all {
		allBytes += schemaBytesOf(def)
	}
	exposedBytes := 0
	for _, def := range exposed {
		exposedBytes += schemaBytesOf(def)
	}
	deferredCount := len(registry.ListDeferred())

	t.Logf("registered tools: %d (exposed %d, deferred %d)", len(all), len(exposed), deferredCount)
	t.Logf("schema bytes: all=%d exposed=%d saved=%d (%.1f%% reduction)",
		allBytes, exposedBytes, allBytes-exposedBytes, percent(allBytes, allBytes-exposedBytes))
	if deferredCount > 0 && exposedBytes >= allBytes {
		t.Fatalf("deferred tools must reduce the exposed payload: exposed=%d all=%d", exposedBytes, allBytes)
	}
}

func percent(total, part int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(part) / float64(total) * 100
}

// TestMeasureToolSearchPayload reports the real payload of one tool_search
// call against the live MCP catalogue, so the siblings list and the
// description echo can be judged against a real number.
func TestMeasureToolSearchPayload(t *testing.T) {
	if os.Getenv("FAIRY_LIVE_MCP_TEST") != "1" {
		t.Skip("set FAIRY_LIVE_MCP_TEST=1 to measure against live MCP servers")
	}
	configPath := os.Getenv("FAIRY_CONFIG_PATH")
	if configPath == "" {
		configPath = "config/config.json"
	}
	repoRoot := findRepoRoot(configPath)
	cfg, err := LoadConfig(repoRoot, configPath)
	if err != nil {
		t.Skipf("repo config unavailable: %v", err)
	}
	registry, err := NewToolFactory().BuildRegistry(cfg)
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	defer func() { _ = registry.Close() }()

	searchTool, ok := registry.Get("tool_search")
	if !ok {
		t.Fatal("tool_search must be registered")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := searchTool.Execute(ctx, ToolInvocation{Args: json.RawMessage(`{"query":"asset","limit":5}`)})
	if err != nil {
		t.Fatalf("tool_search failed: %v", err)
	}
	raw, _ := json.Marshal(result.Value)
	t.Logf("tool_search payload bytes: %d", len(raw))
	type entry struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Siblings    []string `json:"siblings"`
	}
	var payload struct {
		Results []entry `json:"results"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	for _, res := range payload.Results {
		t.Logf("  %s siblings=%d desc_len=%d", res.Name, len(res.Siblings), len(res.Description))
	}
}
