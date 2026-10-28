package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"agentloop/agent/internal/biz/mcp"
)

// liveMCPServerEnabled gates the tests that talk to real MCP servers. They
// need a reachable server, so they only run when the operator asks for them.
func liveMCPServerEnabled(t *testing.T) {
	t.Helper()
	if os.Getenv("FAIRY_LIVE_MCP_TEST") != "1" {
		t.Skip("set FAIRY_LIVE_MCP_TEST=1 to run tests against live MCP servers")
	}
}

// TestRealMCPToolSearchUnlocksAgainstLiveServer connects to the MCP servers
// configured in the repo config and verifies the deferred-tool flow end to end
// against the real catalogues: the tool schemas are hidden by default, a
// tool_search query finds them, and the hit becomes callable.
func TestRealMCPToolSearchUnlocksAgainstLiveServer(t *testing.T) {
	liveMCPServerEnabled(t)
	configPath := "config/config.json"
	repoRoot := findRepoRoot(configPath)
	cfg, err := LoadConfig(repoRoot, configPath)
	if err != nil {
		t.Skipf("repo config unavailable: %v", err)
	}
	if len(cfg.MCPServers) == 0 {
		t.Skip("no MCP servers configured")
	}

	registry, err := NewToolFactory().BuildRegistry(cfg)
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	defer func() { _ = registry.Close() }()

	deferred := registry.ListDeferred()
	if len(deferred) == 0 {
		t.Fatal("expected the live MCP servers to contribute deferred tools")
	}
	t.Logf("live deferred tools: %d", len(deferred))
	for i, info := range deferred {
		if i >= 3 {
			break
		}
		t.Logf("  %s [%s] %s", info.Name, info.Group, truncate(info.Description, 70))
	}

	// Nothing from an MCP server may be exposed before it is searched for.
	for _, def := range registry.ListExposedSchemas() {
		for _, info := range deferred {
			if def.ToolName() == info.Name {
				t.Fatalf("MCP tool %s must not be exposed before it is searched for", info.Name)
			}
		}
	}

	// Search using a word taken from a real tool name, then unlock it.
	target := deferred[0]
	word := firstMeaningfulWord(target.Name)
	searchTool, ok := registry.Get("tool_search")
	if !ok {
		t.Fatal("tool_search must be registered")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := searchTool.Execute(ctx, ToolInvocation{Args: json.RawMessage(`{"query":"` + word + `","limit":5}`)})
	if err != nil {
		t.Fatalf("tool_search failed: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool_search reported an error: %v", result.Value)
	}
	raw, _ := json.Marshal(result.Value)
	t.Logf("tool_search(%q) -> %s", word, truncate(string(raw), 600))

	unlocked, _ := result.Value["unlocked"].([]string)
	if len(unlocked) == 0 {
		t.Fatalf("tool_search must unlock at least one tool for query %q", word)
	}
	for _, name := range unlocked {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("unlocked tool %s must be callable", name)
		}
	}
	// The unlocked tool must now carry its schema into the request.
	exposedNames := map[string]bool{}
	for _, def := range registry.ListExposedSchemas() {
		exposedNames[def.ToolName()] = true
	}
	for _, name := range unlocked {
		if !exposedNames[name] {
			t.Fatalf("unlocked tool %s must be exposed after the search", name)
		}
	}
	t.Logf("unlocked and exposed: %v", unlocked)
}

// firstMeaningfulWord strips the server namespace and returns a word worth
// searching for.
func firstMeaningfulWord(name string) string {
	for _, part := range splitOnUnderscore(name) {
		if len(part) >= 4 {
			return part
		}
	}
	return name
}

func splitOnUnderscore(value string) []string {
	parts := []string{}
	current := ""
	for _, r := range value {
		if r == '_' {
			if current != "" {
				parts = append(parts, current)
			}
			current = ""
			continue
		}
		current += string(r)
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max] + "..."
}

// TestRealMCPClientListsToolsDirectly proves the live server is reachable
// through the MCP client itself, independent of the deferred-tool mechanism.
func TestRealMCPClientListsToolsDirectly(t *testing.T) {
	liveMCPServerEnabled(t)
	configPath := "config/config.json"
	repoRoot := findRepoRoot(configPath)
	cfg, err := LoadConfig(repoRoot, configPath)
	if err != nil {
		t.Skipf("repo config unavailable: %v", err)
	}
	if len(cfg.MCPServers) == 0 {
		t.Skip("no MCP servers configured")
	}
	name := ""
	for candidate := range cfg.MCPServers {
		if cfg.MCPServers[candidate].IsEnabled() {
			name = candidate
			break
		}
	}
	if name == "" {
		t.Skip("no enabled MCP server")
	}
	serverCfg := cfg.MCPServers[name]
	timeoutSec := serverCfg.StartupTimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 30
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()
	client, err := mcp.Connect(ctx, mcp.Options{
		Name:              name,
		Transport:         serverCfg.Transport,
		Command:           serverCfg.Command,
		Args:              serverCfg.Args,
		Env:               serverCfg.Env,
		URL:               serverCfg.URL,
		Headers:           serverCfg.Headers,
		ProtocolVersion:   serverCfg.ProtocolVersion,
		StartupTimeoutSec: timeoutSec,
		ToolPrefix:        serverCfg.ToolPrefix,
	})
	if err != nil {
		t.Skipf("MCP server %q unavailable: %v", name, err)
	}
	defer func() { _ = client.Close() }()
	descriptors, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("list tools from %q: %v", name, err)
	}
	t.Logf("live MCP server %q exposes %d tools", name, len(descriptors))
	if len(descriptors) == 0 {
		t.Fatalf("live MCP server %q reported no tools", name)
	}
}
