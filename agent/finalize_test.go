package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repoRootForTest(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func writeMockFile(t *testing.T, dir string, responses []map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(responses)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "mock.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newWorkRegistry(t *testing.T, calls *int) *ToolRegistry {
	t.Helper()
	registry := NewToolRegistry()
	if err := registry.Register(BackendLocal, NewLocalToolFunc("work", ToolDef{Type: "function", Function: map[string]any{"name": "work"}}, func(ctx context.Context, inv ToolInvocation) (map[string]any, error) {
		(*calls)++
		return map[string]any{"ok": true}, nil
	})); err != nil {
		t.Fatal(err)
	}
	return registry
}

func TestSubtaskFinalizeMessagesUseOnlyRenderedSummary(t *testing.T) {
	messages := subtaskFinalizeMessages("summarize the bounded execution history")
	if len(messages) != 1 || messages[0].Role != "user" || messages[0].Content != "summarize the bounded execution history" {
		t.Fatalf("finalize must use an isolated summary-only conversation: %#v", messages)
	}
	if len(messages[0].ToolCalls) != 0 {
		t.Fatalf("finalize instruction must not carry historical tool calls: %#v", messages[0])
	}
}

func TestRenderSubtaskFinalizePromptIncludesTaskAndContext(t *testing.T) {
	template := "task={{ OriginalTask }} agent={{ AgentType }}\n{{ Context }}"
	msgs := []Message{
		NewMessage("user", "find the answer", nil, "", ""),
		NewMessage("tool", `{"ok":true}`, nil, "c1", "web_search"),
	}
	got := renderSubtaskFinalizePrompt(template, "find the answer", msgs)
	if !strings.Contains(got, "task=find the answer") || !strings.Contains(got, "web_search") {
		t.Fatalf("finalize prompt did not render task/context: %q", got)
	}
}

func TestSubtaskStepLimitIsSoftAndContinues(t *testing.T) {
	t.Setenv("AGENT_RUN_KIND", "subtask")
	dir := t.TempDir()
	mockPath := writeMockFile(t, dir, []map[string]any{
		{"finish_reason": "tool_calls", "tool_calls": []map[string]any{{"id": "one", "type": "function", "function": map[string]any{"name": "work", "arguments": "{}"}}}},
		{"finish_reason": "tool_calls", "tool_calls": []map[string]any{{"id": "two", "type": "function", "function": map[string]any{"name": "work", "arguments": "{}"}}}},
		{"finish_reason": "tool_calls", "tool_calls": []map[string]any{{"id": "three", "type": "function", "function": map[string]any{"name": "work", "arguments": "{}"}}}},
		{"finish_reason": "stop", "content": "<subtask_result><original_task>work</original_task><work_done>four rounds</work_done><findings>complete</findings><result>done</result><cite_files>[]</cite_files><todo>[]</todo><subtask_status>success</subtask_status></subtask_result>"},
	})
	calls := 0
	registry := newWorkRegistry(t, &calls)
	cfg := &Config{
		UseMock:               true,
		MockFile:              mockPath,
		RepoRoot:              repoRootForTest(t),
		SystemPartsDir:        "config/system/parts/zh",
		SubtaskMaxSteps:       3,
		SubtaskForbiddenTools: []string{"create_subtask", "ask_user"},
	}
	result, err := RunAgentLoop(cfg, registry, "", "complete the work package", nil, nil, "", "", "", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Steps != 4 || calls != 3 {
		t.Fatalf("subtask should continue after the soft step limit: steps=%d calls=%d", result.Steps, calls)
	}
	final := result.Messages[len(result.Messages)-1]
	if len(final.ToolCalls) != 0 || !strings.Contains(final.Content, "<subtask_status>success</subtask_status>") {
		t.Fatalf("subtask did not finish normally after the soft limit: %#v", final)
	}
}

func TestSubtaskNormalCompletionSkipsFinalize(t *testing.T) {
	t.Setenv("AGENT_RUN_KIND", "subtask")
	dir := t.TempDir()
	mockPath := writeMockFile(t, dir, []map[string]any{
		{"finish_reason": "stop", "content": "<subtask_result><result>done</result><subtask_status>success</subtask_status></subtask_result>"},
	})
	calls := 0
	registry := newWorkRegistry(t, &calls)
	cfg := &Config{
		UseMock:               true,
		MockFile:              mockPath,
		RepoRoot:              repoRootForTest(t),
		SystemPartsDir:        "config/system/parts/zh",
		SubtaskMaxSteps:       5,
		SubtaskForbiddenTools: []string{"create_subtask"},
	}
	result, err := RunAgentLoop(cfg, registry, "", "finish quickly", nil, nil, "", "", "", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Steps != 1 || calls != 0 {
		t.Fatalf("normal completion should not finalize: steps=%d calls=%d", result.Steps, calls)
	}
	final := result.Messages[len(result.Messages)-1]
	if !strings.Contains(final.Content, "success") {
		t.Fatalf("normal subtask result was lost: %#v", final)
	}
}
