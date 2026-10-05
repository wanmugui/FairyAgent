package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseReadFileAttemptKeySeparatesRanges(t *testing.T) {
	full, ok := parseReadFileAttemptKey(`{"file_path":"D:\\Fairy\\agent\\agentloop.go"}`)
	if !ok || full.Path != `D:\Fairy\agent\agentloop.go` {
		t.Fatalf("unexpected full-read key: %#v ok=%v", full, ok)
	}
	window, ok := parseReadFileAttemptKey(`{"path":"D:\\Fairy\\agent\\agentloop.go","offset":100,"limit":80}`)
	if !ok || window.Path != full.Path || window.Offset != 100 || window.Limit != 80 {
		t.Fatalf("unexpected ranged-read key: %#v ok=%v", window, ok)
	}
	if full == window {
		t.Fatalf("different ranges must not share the same read guard key: %#v", full)
	}
}

func TestRepeatedReadGuardAllowsTwoReadsThenBlocks(t *testing.T) {
	if shouldBlockRepeatedRead(0) || shouldBlockRepeatedRead(1) {
		t.Fatal("first two reads should be allowed")
	}
	if !shouldBlockRepeatedRead(2) || !shouldBlockRepeatedRead(3) {
		t.Fatal("third and later identical reads should be blocked")
	}
}

func TestLikelyImplementationTaskAndGuardText(t *testing.T) {
	if !likelyImplementationTask("你能改一下自己的联网工具吗？") {
		t.Fatal("expected implementation task to be detected")
	}
	if likelyImplementationTask("解释一下这个函数是做什么的") {
		t.Fatal("explanation request should not be classified as implementation")
	}
	text := executionGuardText(3)
	for _, want := range []string{"下一步", "plan", "edit_file", "write_file"} {
		if !strings.Contains(text, want) {
			t.Fatalf("guard text missing %q: %s", want, text)
		}
	}
}

func TestExecutionGuardIsInternalControlMessage(t *testing.T) {
	msg := newInternalControlMessage("user", "guard", internalTypeExecutionGuard)
	if !isInternalControlMessage(msg) {
		t.Fatal("execution guard must stay out of the persisted user transcript")
	}
}

func TestAgentLoopBlocksThirdIdenticalReadAndNudgesToEdit(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "target.go")
	if err := os.WriteFile(filePath, []byte("package target\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	arguments, err := json.Marshal(map[string]string{"file_path": filePath})
	if err != nil {
		t.Fatal(err)
	}
	readCall := func(id string) map[string]any {
		return map[string]any{
			"id": id, "type": "function",
			"function": map[string]any{"name": "read_file", "arguments": string(arguments)},
		}
	}
	mockPath := writeMockFile(t, dir, []map[string]any{
		{"finish_reason": "tool_calls", "tool_calls": []map[string]any{readCall("read-1")}},
		{"finish_reason": "tool_calls", "tool_calls": []map[string]any{readCall("read-2")}},
		{"finish_reason": "tool_calls", "tool_calls": []map[string]any{readCall("read-3")}},
		{"finish_reason": "tool_calls", "tool_calls": []map[string]any{readCall("read-4")}},
		{"finish_reason": "tool_calls", "tool_calls": []map[string]any{{"id": "plan-1", "type": "function", "function": map[string]any{"name": "plan", "arguments": "{}"}}}},
		{"finish_reason": "stop", "content": "<report>done</report>"},
	})
	registry := NewToolRegistry()
	executedReads := 0
	executedPlans := 0
	err = registry.Register(BackendLocal, NewLocalToolFunc("read_file", ToolDef{Type: "function", Function: map[string]any{"name": "read_file"}}, func(ctx context.Context, inv ToolInvocation) (map[string]any, error) {
		executedReads++
		return map[string]any{"path": filePath, "content": "   1: package target", "line_count": 1}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	err = registry.Register(BackendLocal, NewLocalToolFunc("plan", ToolDef{Type: "function", Function: map[string]any{"name": "plan"}}, func(ctx context.Context, inv ToolInvocation) (map[string]any, error) {
		executedPlans++
		return map[string]any{"ok": true}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		UseMock:         true,
		MockFile:        mockPath,
		RepoRoot:        repoRootForTest(t),
		SystemPartsDir:  "config/system/parts/zh",
		MaxNetworkCalls: 0,
	}
	result, err := RunAgentLoop(cfg, registry, "", "修改这个文件并修复问题", nil, nil, "", "", "", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if executedReads != 2 {
		t.Fatalf("third identical read must be blocked before dispatch: executed=%d", executedReads)
	}
	var sawReadGuard, sawExecutionGuard, sawPlanGate bool
	for _, event := range result.Trace {
		switch event["event"] {
		case "read_file_guard":
			sawReadGuard = true
		case "execution_guard":
			sawExecutionGuard = true
		case "read_plan_gate":
			sawPlanGate = true
		}
	}
	if !sawReadGuard || !sawExecutionGuard || !sawPlanGate || executedPlans != 1 {
		t.Fatalf("missing loop guards: read=%v execution=%v plan_gate=%v plans=%d trace=%#v", sawReadGuard, sawExecutionGuard, sawPlanGate, executedPlans, result.Trace)
	}
}
