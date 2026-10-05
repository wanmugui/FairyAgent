package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAgentLoopToolDispatchPreservesCallIDsAndOrder(t *testing.T) {
	registry := NewToolRegistry()
	for _, name := range []string{"first", "second"} {
		toolName := name
		if err := registry.Register(BackendLocal, NewLocalToolFunc(toolName, ToolDef{Type: "function", Function: map[string]any{"name": toolName}}, func(ctx context.Context, inv ToolInvocation) (map[string]any, error) {
			return map[string]any{"tool": toolName, "call_id": inv.CallID, "workspace": inv.Workspace, "session": inv.SessionFile}, nil
		})); err != nil {
			t.Fatal(err)
		}
	}
	calls := []ToolCall{
		{ID: "call_first", Type: "function", Function: ToolCallFunc{Name: "first", Arguments: `{"value":1}`}},
		{ID: "call_second", Type: "function", Function: ToolCallFunc{Name: "second", Arguments: `{"value":2}`}},
	}

	results := processToolCalls(context.Background(), registry, calls, "/tmp/workspace", "/tmp/session.json", 2*time.Second)
	if len(results) != 2 || results[0].CallID != "call_first" || results[1].CallID != "call_second" {
		t.Fatalf("unexpected result identity/order: %#v", results)
	}
	if results[0].Result.Value["call_id"] != "call_first" || results[1].Result.Value["call_id"] != "call_second" {
		t.Fatalf("tool call IDs were not passed to implementations: %#v", results)
	}
	if results[0].Result.Value["workspace"] != "/tmp/workspace" || results[0].Result.Value["session"] != "/tmp/session.json" {
		t.Fatalf("invocation context was not passed: %#v", results[0].Result.Value)
	}
}

func TestAgentLoopToolDispatchReturnsStructuredJSONObject(t *testing.T) {
	registry := NewToolRegistry()
	if err := registry.Register(BackendLocal, NewLocalToolFunc("object", ToolDef{Type: "function", Function: map[string]any{"name": "object"}}, func(ctx context.Context, inv ToolInvocation) (map[string]any, error) {
		return map[string]any{"ok": true}, nil
	})); err != nil {
		t.Fatal(err)
	}
	results := processToolCalls(context.Background(), registry, []ToolCall{{ID: "call_object", Function: ToolCallFunc{Name: "object", Arguments: `{}`}}}, "", "", time.Second)
	raw, err := results[0].Result.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["ok"] != true {
		t.Fatalf("unexpected tool message object: %#v", decoded)
	}
}

func TestAgentLoopToolDispatchAssociatesErrorsWithOriginalCall(t *testing.T) {
	registry := NewToolRegistry()
	if err := registry.Register(BackendLocal, NewLocalToolFunc("fails", ToolDef{Type: "function", Function: map[string]any{"name": "fails"}}, func(ctx context.Context, inv ToolInvocation) (map[string]any, error) {
		return nil, errors.New("tool failed")
	})); err != nil {
		t.Fatal(err)
	}
	results := processToolCalls(context.Background(), registry, []ToolCall{{ID: "call_fails", Function: ToolCallFunc{Name: "fails", Arguments: `{}`}}}, "", "", time.Second)
	if len(results) != 1 || results[0].Err == nil || results[0].CallID != "call_fails" || results[0].Name != "fails" || !strings.Contains(results[0].Err.Error(), "tool failed") {
		t.Fatalf("unexpected associated error: %#v", results)
	}
}

func TestAgentLoopToolDispatchPreservesWaitingReply(t *testing.T) {
	registry := NewToolRegistry()
	if err := registry.Register(BackendLocal, NewLocalToolFunc("ask", ToolDef{Type: "function", Function: map[string]any{"name": "ask"}}, func(ctx context.Context, inv ToolInvocation) (map[string]any, error) {
		return map[string]any{"waiting_reply": true, "ask_type": "select_mode"}, nil
	})); err != nil {
		t.Fatal(err)
	}
	results := processToolCalls(context.Background(), registry, []ToolCall{{ID: "call_ask", Function: ToolCallFunc{Name: "ask", Arguments: `{}`}}}, "", "", time.Second)
	if len(results) != 1 || !results[0].Result.WaitingReply || results[0].Result.Value["waiting_reply"] != true {
		t.Fatalf("waiting_reply signal was not preserved: %#v", results)
	}
}
