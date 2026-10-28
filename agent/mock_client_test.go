package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfiguredMockLLMReplaysResponsesInOrder(t *testing.T) {
	repoRoot := t.TempDir()
	mockPath := filepath.Join(repoRoot, "mock.json")
	responses := []map[string]any{
		{"finish_reason": "tool_calls", "content": "", "tool_calls": []map[string]any{{"id": "call_1", "type": "function", "function": map[string]any{"name": "get_current_time", "arguments": "{}"}}}},
		{"finish_reason": "stop", "content": "<report>done</report>", "tool_calls": []any{}},
	}
	raw, err := json.Marshal(responses)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mockPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{UseMock: true, MockFile: "mock.json", RepoRoot: repoRoot}

	first, err := CallConfiguredLLM(cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CallConfiguredLLM(cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.FinishStop != "tool_calls" || len(first.ToolCalls) != 1 || first.ToolCalls[0].Function.Name != "get_current_time" {
		t.Fatalf("unexpected first mock response: %#v", first)
	}
	if second.FinishStop != "stop" || second.Content != "<report>done</report>" {
		t.Fatalf("unexpected second mock response: %#v", second)
	}
}

func TestConfiguredMockLLMReportsExhaustion(t *testing.T) {
	repoRoot := t.TempDir()
	mockPath := filepath.Join(repoRoot, "mock.json")
	if err := os.WriteFile(mockPath, []byte(`[{"finish_reason":"stop","content":"done"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{UseMock: true, MockFile: "mock.json", RepoRoot: repoRoot}
	if _, err := CallConfiguredLLM(cfg, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := CallConfiguredLLM(cfg, nil, nil); err == nil || !strings.Contains(err.Error(), "exhausted") {
		t.Fatalf("unexpected exhausted mock error: %v", err)
	}
}

func TestConfiguredMockLLMUsesSubtaskProfile(t *testing.T) {
	repoRoot := t.TempDir()
	mockPath := filepath.Join(repoRoot, "mock.json")
	fixture := `{
  "main": [{"finish_reason":"stop","content":"main"}],
  "subtask": [{"finish_reason":"stop","content":"child"}]
}`
	if err := os.WriteFile(mockPath, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_RUN_KIND", "subtask")
	cfg := &Config{UseMock: true, MockFile: "mock.json", RepoRoot: repoRoot}
	response, err := CallConfiguredLLM(cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.Content != "child" {
		t.Fatalf("unexpected subtask profile response: %#v", response)
	}
}
