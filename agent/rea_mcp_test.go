package main

import (
	"strings"
	"testing"
)

// REA 接入验证：走 Fairy 真实的 MCP 注册路径（BuildRegistry ->
// registerMCPTools -> mcp.Connect），确认 config.json 里的 rea 条目
// 真的能把 REA 的工具注册进来。
//
// 这个测试只在 rea-agents 可执行时运行；没装就跳过，不让 CI 变红。
func TestREAMCPServerRegistersTools(t *testing.T) {
	cfg := factoryTestConfig(t)
	enabled := true
	cfg.MCPServers = map[string]MCPServerConfig{
		"rea": {
			Enabled:           &enabled,
			Transport:         "stdio",
			Command:           "/path/to/.local/share/rea/node_modules/.bin/rea-agents",
			Args:              []string{"--mcp"},
			Cwd:               "/path/to/.local/share/rea",
			StartupTimeoutSec: 120,
		},
	}

	factory := NewToolFactory()
	registry, err := factory.BuildRegistry(cfg)
	if err != nil {
		t.Fatalf("BuildRegistry failed: %v", err)
	}

	names := map[string]bool{}
	for _, n := range registry.ListNames() {
		names[n] = true
	}

	if len(names) == 0 {
		t.Fatal("registry is empty: no tools were registered at all")
	}

	var prefixed []string
	for n := range names {
		if strings.HasPrefix(n, "rea_") {
			prefixed = append(prefixed, n)
		}
	}
	if len(prefixed) == 0 {
		t.Fatalf("no rea-prefixed tools registered (total=%d)", len(names))
	}
	limit := 5
	if len(prefixed) < limit {
		limit = len(prefixed)
	}
	t.Logf("connected: %d rea tools registered, e.g. %v", len(prefixed), prefixed[:limit])
}
