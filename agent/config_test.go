package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeConfigForTest(t *testing.T, value map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigCreatesExplicitToolRouterForOldConfig(t *testing.T) {
	path := writeConfigForTest(t, map[string]any{"workspace_dir": "workspace"})
	cfg, err := LoadConfig(filepath.Dir(path), path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ToolRuntime == nil {
		t.Fatal("expected tool runtime defaults")
	}
	if len(cfg.ToolRuntime.Tools) != 0 {
		t.Fatalf("unexpected default routes: %#v", cfg.ToolRuntime.Tools)
	}
	if cfg.ToolRuntime.RetryCount != 1 {
		t.Fatalf("unexpected default retry count: %d", cfg.ToolRuntime.RetryCount)
	}
}

func TestLoadConfigReadsExplicitToolBackend(t *testing.T) {
	path := writeConfigForTest(t, map[string]any{
		"tool_runtime": map[string]any{
			"tools": map[string]any{
				"read_file": map[string]any{"backend": "local"},
			},
			"retry_count": 3,
		},
	})
	cfg, err := LoadConfig(filepath.Dir(path), path)
	if err != nil {
		t.Fatal(err)
	}
	override, ok := cfg.ToolRuntime.Tools["read_file"]
	if !ok || override.Backend != BackendLocal {
		t.Fatalf("unexpected tool override: %#v", override)
	}
	if cfg.ToolRuntime.RetryCount != 3 {
		t.Fatalf("unexpected retry count: %d", cfg.ToolRuntime.RetryCount)
	}
}

func TestLoadConfigReadsMCPServers(t *testing.T) {
	path := writeConfigForTest(t, map[string]any{
		"mcp_servers": map[string]any{
			"playwright": map[string]any{
				"enabled":             true,
				"transport":           "stdio",
				"command":             "npx",
				"args":                []string{"-y", "@playwright/mcp@latest"},
				"tool_prefix":         "pw",
				"allowed_tools":       []string{"browser_navigate"},
				"startup_timeout_sec": 12,
			},
		},
	})
	cfg, err := LoadConfig(filepath.Dir(path), path)
	if err != nil {
		t.Fatal(err)
	}
	server, ok := cfg.MCPServers["playwright"]
	if !ok {
		t.Fatal("MCP server configuration was not loaded")
	}
	if !server.IsEnabled() || server.Transport != "stdio" || server.Command != "npx" || server.ToolPrefix != "pw" || server.StartupTimeoutSec != 12 {
		t.Fatalf("unexpected MCP server config: %#v", server)
	}
	if len(server.Args) != 2 || len(server.AllowedTools) != 1 {
		t.Fatalf("unexpected MCP server lists: %#v", server)
	}
}

func TestLoadConfigRejectsUnknownBackend(t *testing.T) {
	path := writeConfigForTest(t, map[string]any{
		"tool_runtime": map[string]any{"tools": map[string]any{"read_file": map[string]any{"backend": "powershell"}}},
	})
	if _, err := LoadConfig(filepath.Dir(path), path); err == nil {
		t.Fatal("expected unknown backend to be rejected")
	}
}

func TestLoadConfigDefaultsHTTPRetryCount(t *testing.T) {
	path := writeConfigForTest(t, map[string]any{
		"tool_runtime": map[string]any{},
	})
	cfg, err := LoadConfig(filepath.Dir(path), path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ToolRuntime.RetryCount != 1 {
		t.Fatalf("expected retry count 1, got %d", cfg.ToolRuntime.RetryCount)
	}
}

func TestLoadConfigPreservesReflectionAndToolAPISettings(t *testing.T) {
	path := writeConfigForTest(t, map[string]any{
		"reflection": map[string]any{"enabled": true},
		"tools": map[string]any{
			"webFetch": map[string]any{
				"summaryModelName":    "summary-model",
				"summaryModelBaseUrl": "https://summary.example/v1",
			},
			"readFile": map[string]any{
				"maxReadFileSizeBytes": 4096,
			},
			"imageSearch": map[string]any{
				"sourcePriority": []string{"wikimedia", "bing"},
				"timeoutSec":     12,
				"maxImageBytes":  1048576,
			},
		},
	})
	cfg, err := LoadConfig(filepath.Dir(path), path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Reflection.Enabled {
		t.Fatal("expected reflection configuration to be preserved")
	}
	if cfg.Tools.WebFetch.SummaryModelName != "summary-model" || cfg.Tools.WebFetch.SummaryModelBaseUrl != "https://summary.example/v1" {
		t.Fatalf("unexpected web fetch settings: %#v", cfg.Tools.WebFetch)
	}
	if cfg.Tools.ReadFile.MaxReadFileSizeBytes != 4096 {
		t.Fatalf("unexpected read file settings: %#v", cfg.Tools.ReadFile)
	}
	imageSearch := cfg.Tools.ImageSearch
	if len(imageSearch.SourcePriority) != 2 || imageSearch.SourcePriority[0] != "wikimedia" || imageSearch.TimeoutSec != 12 || imageSearch.MaxImageBytes != 1048576 {
		t.Fatalf("unexpected image search settings: %#v", imageSearch)
	}
}

func TestLoadConfigAppliesImageSearchDefaults(t *testing.T) {
	path := writeConfigForTest(t, map[string]any{})
	cfg, err := LoadConfig(filepath.Dir(path), path)
	if err != nil {
		t.Fatal(err)
	}
	imageSearch := cfg.Tools.ImageSearch
	want := []string{"bing", "baidu", "wikimedia"}
	if len(imageSearch.SourcePriority) != len(want) {
		t.Fatalf("unexpected default source priority: %#v", imageSearch.SourcePriority)
	}
	for i := range want {
		if imageSearch.SourcePriority[i] != want[i] {
			t.Fatalf("unexpected default source priority: %#v", imageSearch.SourcePriority)
		}
	}
	if imageSearch.TimeoutSec != 45 || imageSearch.MaxImageBytes != 20<<20 {
		t.Fatalf("unexpected image search defaults: %#v", imageSearch)
	}
	if cfg.Tools.WebFetch.SummaryModelName != "" {
		t.Fatalf("web fetch config should stay empty by default: %#v", cfg.Tools.WebFetch)
	}
}

func TestLoadConfigAppliesToolScoreCleanupDefaults(t *testing.T) {
	path := writeConfigForTest(t, map[string]any{})
	cfg, err := LoadConfig(filepath.Dir(path), path)
	if err != nil {
		t.Fatal(err)
	}
	cleanup := cfg.ToolScoreCleanup
	if cleanup.Enabled {
		t.Fatalf("tool score cleanup must stay disabled unless explicitly enabled: %#v", cleanup)
	}
	if cleanup.TriggerTokens != 30000 || cleanup.Threshold != defaultScoreThreshold {
		t.Fatalf("unexpected tool score cleanup defaults: %#v", cleanup)
	}
	if cleanup.Tier != defaultScoreTier || cleanup.Endpoint != defaultScoreEndpoint {
		t.Fatalf("unexpected classifier defaults: %#v", cleanup)
	}
}

func TestLoadConfigReadsLocalExecutables(t *testing.T) {
	path := writeConfigForTest(t, map[string]any{
		"tool_runtime": map[string]any{
			"executables": map[string]any{
				"python":  "/custom/python",
				"shell":   "/custom/shell",
				"browser": "/custom/browser",
			},
		},
	})
	cfg, err := LoadConfig(filepath.Dir(path), path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ToolRuntime.Executables.Python != "/custom/python" ||
		cfg.ToolRuntime.Executables.Shell != "/custom/shell" ||
		cfg.ToolRuntime.Executables.Browser != "/custom/browser" {
		t.Fatalf("unexpected local executable config: %#v", cfg.ToolRuntime.Executables)
	}
}

func TestDefaultConfigsUseLocalEnvironmentAdapters(t *testing.T) {
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	for _, relativePath := range []string{"config/config.json"} {
		t.Run(filepath.Base(relativePath), func(t *testing.T) {
			cfg, err := LoadConfig(repoRoot, relativePath)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"bash", "powershell", "create_subtask", "html_to_png"} {
				override, ok := configuredBackendOverride(cfg.ToolRuntime.Tools, name)
				if !ok || override.Backend != BackendLocal {
					t.Fatalf("%s must use the local backend in %s: %#v", name, relativePath, override)
				}
			}
			if cfg.ToolRuntime.Executables != (LocalExecutableConfig{}) {
				t.Fatalf("shared config must not contain user-specific executable paths: %#v", cfg.ToolRuntime.Executables)
			}
		})
	}
}

func TestDefaultConfigsRouteAllSchemasWithoutLegacyExecutables(t *testing.T) {
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	for _, relativePath := range []string{"config/config.json"} {
		t.Run(filepath.Base(relativePath), func(t *testing.T) {
			cfg, err := LoadConfig(repoRoot, relativePath)
			if err != nil {
				t.Fatal(err)
			}
			registry, err := NewToolFactory().BuildRegistry(cfg)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range registry.ListNames() {
				backend, ok := registry.GetBackend(name)
				if !ok {
					t.Fatalf("schema %q is not registered", name)
				}
				// BackendMCP 是与 Local/HTTP 并列的合法 backend（见 tool_runtime.go），
				// MCP server 的工具同样注册进 registry，不应被判为 unsupported。
				if backend != BackendLocal && backend != BackendHTTP && backend != BackendMCP {
					t.Fatalf("schema %q has unsupported backend %q in %s", name, backend, relativePath)
				}
			}
		})
	}
}

func TestSelectModelAppliesProfile(t *testing.T) {
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(repoRoot, "config/config.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Models) == 0 {
		t.Fatal("expected the unified config to declare models")
	}
	if err := cfg.SelectModel("minimax-m3"); err != nil {
		t.Fatal(err)
	}
	if cfg.API.Model != "MiniMax-M3" || cfg.SelectedModelID != "minimax-m3" {
		t.Fatalf("unexpected selected profile: %#v", cfg.API)
	}
	if err := cfg.SelectModel("does-not-exist"); err == nil {
		t.Fatal("unknown model must be rejected")
	}

	def, err := LoadConfig(repoRoot, "config/config.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := def.SelectModel(""); err != nil {
		t.Fatal(err)
	}
	if def.SelectedModelID != def.DefaultModel {
		t.Fatalf("empty model id must fall back to default: got %q want %q", def.SelectedModelID, def.DefaultModel)
	}
}

func TestSelectModelResolvesKeyFromRepoRoot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "MINIMAX_key.txt"), []byte("test-minimax-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := writeConfigForTest(t, map[string]any{
		"models": map[string]any{
			"mini": map[string]any{"api": map[string]any{"model": "MiniMax-M3", "api_key": "READ_FROM_MINIMAX_KEY_TXT"}},
		},
	})
	cfg, err := LoadConfig(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.SelectModel("mini"); err != nil {
		t.Fatal(err)
	}
	if cfg.API.APIKey != "test-minimax-key" {
		t.Fatalf("key file was not resolved: %q", cfg.API.APIKey)
	}
}

func TestLoadConfigReadsFallbackFromUISettings(t *testing.T) {
	path := writeConfigForTest(t, map[string]any{
		"default_model": "primary",
		"settings": map[string]any{
			"auto_fallback":  true,
			"fallback_model": "backup",
		},
		"models": map[string]any{
			"primary": map[string]any{"api": map[string]any{"model": "primary-model", "api_key": "primary-key"}},
			"backup":  map[string]any{"api": map[string]any{"model": "backup-model", "api_key": "backup-key"}},
		},
	})
	cfg, err := LoadConfig(t.TempDir(), path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FallbackModel != "backup" {
		t.Fatalf("fallback_model from settings was ignored: %q", cfg.FallbackModel)
	}

	path = writeConfigForTest(t, map[string]any{
		"settings": map[string]any{
			"auto_fallback":  false,
			"fallback_model": "backup",
		},
		"models": map[string]any{
			"primary": map[string]any{"api": map[string]any{"model": "primary-model", "api_key": "primary-key"}},
			"backup":  map[string]any{"api": map[string]any{"model": "backup-model", "api_key": "backup-key"}},
		},
	})
	cfg, err = LoadConfig(t.TempDir(), path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FallbackModel != "" {
		t.Fatalf("auto_fallback=false must disable fallback, got %q", cfg.FallbackModel)
	}
}

func TestImageVQAConfigResolvesDeepSeekKeyFromRepoRoot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "DEEPSEEK_key.txt"), []byte("test-deepseek-key\ndeepseek-flash\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := writeConfigForTest(t, map[string]any{
		"tools": map[string]any{
			"imageVQA": map[string]any{
				"modelName": "deepseek-flash",
				"baseUrl":   "https://api.deepseek.com",
				"apiKey":    "READ_FROM_DEEPSEEK_KEY_TXT",
			},
		},
	})
	cfg, err := LoadConfig(dir, path)
	if err != nil {
		t.Fatal(err)
	}
	imageVQA := cfg.Tools.ImageVQA
	if imageVQA.APIKey != "test-deepseek-key" {
		t.Fatalf("image_vqa key file was not resolved: %q", imageVQA.APIKey)
	}
	if imageVQA.ModelName != "deepseek-flash" || imageVQA.TimeoutSec != 120 || imageVQA.MaxTokens != 2048 {
		t.Fatalf("unexpected image_vqa defaults: %#v", imageVQA)
	}
}

func TestLegacySingleModelConfigIgnoresSelectModel(t *testing.T) {
	path := writeConfigForTest(t, map[string]any{"api": map[string]any{"model": "legacy-model"}})
	cfg, err := LoadConfig(filepath.Dir(path), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.SelectModel("whatever"); err != nil {
		t.Fatalf("legacy config must not fail model selection: %v", err)
	}
	if cfg.API.Model != "legacy-model" {
		t.Fatalf("legacy api block was overwritten: %#v", cfg.API)
	}
}

// TestRepoConfigResolvesLocalModelKeys exercises the real repo config against
// the (gitignored) local key files. It skips on clean checkouts that have no
// key files, so CI is unaffected.
func TestRepoConfigResolvesLocalModelKeys(t *testing.T) {
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "MINIMAX_key.txt")); err != nil {
		t.Skip("local key files are not present")
	}
	cfg, err := LoadConfig(repoRoot, "config/config.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.SelectModel("minimax-m3"); err != nil {
		t.Fatal(err)
	}
	if cfg.API.APIKey == "" || cfg.API.APIKey == "READ_FROM_MINIMAX_KEY_TXT" {
		t.Fatalf("MINIMAX_key.txt was not resolved: %q", cfg.API.APIKey)
	}
	if err := cfg.SelectModel("deepseek-v4-flash"); err != nil {
		t.Fatal(err)
	}
	if cfg.API.APIKey == "" || cfg.API.APIKey == "READ_FROM_DEEPSEEK_KEY_TXT" {
		t.Fatalf("DEEPSEEK_key.txt was not resolved: %q", cfg.API.APIKey)
	}
}
