package main

import (
	"strings"
	"testing"
)

func TestNormalizeToolCallsRejectsMalformedParallelBatch(t *testing.T) {
	calls := []ToolCall{
		{ID: "bad", Type: "function", Function: ToolCallFunc{Name: "write_file", Arguments: `{"file_path":"local:///tmp/out.json"`}},
		{ID: "good", Type: "function", Function: ToolCallFunc{Name: "bash", Arguments: `{"command":"pwd"}`}},
	}
	if _, err := normalizeToolCalls(calls); err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("malformed parallel batch was accepted: %v", err)
	}
}

func TestNormalizeToolCallsAcceptsObjectAndNormalizesEmptyArguments(t *testing.T) {
	calls, err := normalizeToolCalls([]ToolCall{{ID: "call-1", Function: ToolCallFunc{Name: "get_current_time"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Type != "function" || calls[0].Function.Arguments != "{}" {
		t.Fatalf("unexpected normalized call: %#v", calls)
	}
	if _, err := normalizeToolCalls([]ToolCall{{ID: "call-2", Function: ToolCallFunc{Name: "bash", Arguments: `[]`}}}); err == nil {
		t.Fatal("array arguments must not be accepted as a function argument object")
	}
	// Missing business fields are intentionally left to the tool's own
	// validator; the history boundary checks transport syntax only.
	if _, err := normalizeToolCalls([]ToolCall{{ID: "call-3", Function: ToolCallFunc{Name: "write_file", Arguments: `{"file_path":"local:///out.json"}`}}}); err != nil {
		t.Fatalf("valid JSON with a missing business field was rejected too early: %v", err)
	}
}

func TestNormalizeToolCallsRejectsDuplicateAndEmptyIDs(t *testing.T) {
	if _, err := normalizeToolCalls([]ToolCall{{ID: "", Function: ToolCallFunc{Name: "bash", Arguments: `{}`}}}); err == nil {
		t.Fatal("empty tool call id must be rejected")
	}
	dup := []ToolCall{
		{ID: "same", Function: ToolCallFunc{Name: "bash", Arguments: `{}`}},
		{ID: "same", Function: ToolCallFunc{Name: "bash", Arguments: `{}`}},
	}
	if _, err := normalizeToolCalls(dup); err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("duplicate tool call ids were accepted: %v", err)
	}
}

func TestValidateToolMessageHistoryRequiresExactCompletePairing(t *testing.T) {
	complete := []Message{
		NewMessage("user", "work", nil, "", ""),
		NewMessage("assistant", "", []ToolCall{
			{ID: "one", Function: ToolCallFunc{Name: "write_file", Arguments: `{}`}},
			{ID: "two", Function: ToolCallFunc{Name: "bash", Arguments: `{}`}},
		}, "", ""),
		NewMessage("tool", `{"ok":false}`, nil, "one", "write_file"),
		NewMessage("tool", `{"ok":true}`, nil, "two", "bash"),
	}
	if err := validateToolMessageHistory(complete); err != nil {
		t.Fatalf("complete tool round was rejected: %v", err)
	}

	missing := append([]Message(nil), complete[:3]...)
	if err := validateToolMessageHistory(missing); err == nil || !strings.Contains(err.Error(), "unresolved") {
		t.Fatalf("missing result was not detected: %v", err)
	}
	orphan := []Message{NewMessage("tool", `{}`, nil, "unknown", "bash")}
	if err := validateToolMessageHistory(orphan); err == nil || !strings.Contains(err.Error(), "orphaned") {
		t.Fatalf("orphaned result was not detected: %v", err)
	}
}

func TestRepairToolPairingDropsLegacyMalformedArgumentsRound(t *testing.T) {
	messages := []Message{
		NewMessage("user", "start", nil, "", ""),
		NewMessage("assistant", "progress", []ToolCall{
			{ID: "bad", Function: ToolCallFunc{Name: "write_file", Arguments: `{"file_path":"x"`}},
			{ID: "valid-in-same-batch", Function: ToolCallFunc{Name: "bash", Arguments: `{}`}},
		}, "", ""),
		NewMessage("tool", `{"error":"parse"}`, nil, "bad", "write_file"),
		NewMessage("tool", `{"ok":true}`, nil, "valid-in-same-batch", "bash"),
		NewMessage("user", "continue", nil, "", ""),
	}

	repaired := repairToolPairing(messages)
	if len(repaired) != 3 {
		t.Fatalf("unexpected repaired history: %#v", repaired)
	}
	if repaired[1].Role != "assistant" || repaired[1].Content != "progress" || len(repaired[1].ToolCalls) != 0 {
		t.Fatalf("assistant prose was not preserved safely: %#v", repaired[1])
	}
	if repaired[2].Role != "user" || repaired[2].Content != "continue" {
		t.Fatalf("following history was changed: %#v", repaired)
	}
}
