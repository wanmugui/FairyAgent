package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestLocalToolFuncReturnsStructuredValue(t *testing.T) {
	tool := NewLocalToolFunc(
		"echo",
		ToolDef{Type: "function", Function: map[string]any{"name": "echo"}},
		func(ctx context.Context, inv ToolInvocation) (map[string]any, error) {
			return map[string]any{"value": "ok", "call_id": inv.CallID}, nil
		},
	)

	got, err := tool.Execute(context.Background(), ToolInvocation{
		CallID: "call_1",
		Name:   "echo",
		Args:   json.RawMessage("{\"value\":\"input\"}"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Value["value"] != "ok" || got.Value["call_id"] != "call_1" {
		t.Fatalf("unexpected result: %#v", got.Value)
	}
}

func TestToolResultJSONUsesObjectPayload(t *testing.T) {
	raw, err := (ToolResult{Value: map[string]any{"ok": true}}).JSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "{\"ok\":true}" {
		t.Fatalf("unexpected JSON: %s", raw)
	}
}

func TestLocalToolFuncHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tool := NewLocalToolFunc(
		"cancel",
		ToolDef{Type: "function", Function: map[string]any{"name": "cancel"}},
		func(ctx context.Context, inv ToolInvocation) (map[string]any, error) {
			return nil, ctx.Err()
		},
	)
	_, err := tool.Execute(ctx, ToolInvocation{Name: "cancel"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
