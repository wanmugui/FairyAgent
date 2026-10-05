package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSessionUsageEventEmittedDuringTurn proves the streaming TOKENS counter
// fix: every LLM call in the running turn pushes a "session_usage" SSE
// payload to stdout so the frontend can update the header counter live
// instead of waiting for the turn to finish.
//
// We redirect os.Stdout to a buffer, run a minimal mock-backed turn, then
// assert the captured output contains at least one session_usage event and
// that its accumulated token counts monotonically grow across calls.
func TestSessionUsageEventEmittedDuringTurn(t *testing.T) {
	t.Setenv("AGENT_RUN_KIND", "")
	dir := t.TempDir()

	// Three turn responses: each adds tool_calls so the agent loops 3 times,
	// which means we should see 3 session_usage events (one per LLM call).
	mockResponses := []map[string]any{
		{"finish_reason": "tool_calls", "tool_calls": []map[string]any{{"id": "c1", "type": "function", "function": map[string]any{"name": "work", "arguments": "{}"}}}, "usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 10}, "duration_ms": 1200},
		{"finish_reason": "tool_calls", "tool_calls": []map[string]any{{"id": "c2", "type": "function", "function": map[string]any{"name": "work", "arguments": "{}"}}}, "usage": map[string]any{"prompt_tokens": 250, "completion_tokens": 20}, "duration_ms": 900},
		{"finish_reason": "stop", "content": "all done", "usage": map[string]any{"prompt_tokens": 400, "completion_tokens": 30}, "duration_ms": 700},
	}
	raw, _ := json.Marshal(mockResponses)
	mockPath := filepath.Join(dir, "mock.json")
	if err := os.WriteFile(mockPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	calls := 0
	registry := NewToolRegistry()
	if err := registry.Register(BackendLocal, NewLocalToolFunc("work", ToolDef{Type: "function", Function: map[string]any{"name": "work"}}, func(ctx context.Context, inv ToolInvocation) (map[string]any, error) {
		calls++
		return map[string]any{"ok": true}, nil
	})); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{
		UseMock:               true,
		MockFile:              mockPath,
		RepoRoot:              repoRootForTest(t),
		SystemPartsDir:        "config/system/parts/zh",
		SubtaskMaxSteps:       5,
		SubtaskForbiddenTools: []string{"create_subtask"},
	}

	// Redirect stdout to capture emitEvent JSON lines.
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	done := make(chan struct{})
	var buf bytes.Buffer
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	result, runErr := RunAgentLoop(cfg, registry, "", "complete the work package", nil, nil, "", "", "", "", "", false)
	_ = w.Close()
	<-done
	os.Stdout = origStdout

	if runErr != nil {
		t.Fatalf("RunAgentLoop failed: %v", runErr)
	}
	if result == nil {
		t.Fatalf("nil result")
	}

	captured := buf.String()
	t.Logf("captured %d bytes of stdout", len(captured))

	// Find every session_usage event line.
	var sessionUsageCount int
	var promptTokensValues []int
	var completionTokensValues []int
	var durations []float64
	for _, line := range strings.Split(captured, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var obj map[string]interface{}
		if json.Unmarshal([]byte(line), &obj) != nil {
			continue
		}
		if obj["type"] == "session_usage" {
			sessionUsageCount++
			if pt, ok := obj["prompt_tokens"].(float64); ok {
				promptTokensValues = append(promptTokensValues, int(pt))
			}
			if ct, ok := obj["completion_tokens"].(float64); ok {
				completionTokensValues = append(completionTokensValues, int(ct))
			}
			if d, ok := obj["duration_ms"].(float64); ok {
				durations = append(durations, d)
			}
			t.Logf("session_usage event: %v", obj)
		}
	}

	// 3 LLM calls => at least 3 session_usage events (one per call).
	if sessionUsageCount < 3 {
		t.Fatalf("expected >=3 session_usage events (one per LLM call), got %d. stdout:\n%s",
			sessionUsageCount, captured)
	}

	if len(durations) != 0 {
		t.Fatalf("session_usage must not carry timing fields; got duration_ms=%v", durations)
	}

	if len(promptTokensValues) < 3 || promptTokensValues[0] <= 0 || promptTokensValues[len(promptTokensValues)-1] <= promptTokensValues[0] {
		t.Fatalf("expected non-zero, growing prompt_tokens across events, got %v", promptTokensValues)
	}
	if len(completionTokensValues) < 3 || completionTokensValues[0] <= 0 || completionTokensValues[len(completionTokensValues)-1] <= completionTokensValues[0] {
		t.Fatalf("expected non-zero, growing completion_tokens across events, got %v", completionTokensValues)
	}

	// Monotonic growth across the streamed events.
	for i := 1; i < len(promptTokensValues); i++ {
		if promptTokensValues[i] < promptTokensValues[i-1] {
			t.Fatalf("prompt_tokens regressed: %v", promptTokensValues)
		}
	}
}
