package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// askTimeout is injected into every call in this file so the backend resolves
// within a test instead of blocking for the full AskUserAutoSelectSeconds.
const askTimeout = `"__wait_timeout_seconds":1,`

func executeLocalAskUser(t *testing.T, workspace, args string) ToolResult {
	t.Helper()
	tool := NewLocalAskUserTool(localFileTestSchema("ask_user"))
	result, err := tool.Execute(context.Background(), ToolInvocation{
		Name:      "ask_user",
		Workspace: workspace,
		Args:      json.RawMessage(args),
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// The tool used to return immediately with WaitingReply and let the frontend
// own the countdown. It now waits for the reply itself and resolves before
// returning, so the result is answered rather than waiting.
func TestLocalAskUserResolvesBeforeReturning(t *testing.T) {
	result := executeLocalAskUser(t, t.TempDir(),
		`{"ask_type":"select_mode",`+askTimeout+`"questions":[{"id":"mode","question":"Choose"}]}`)
	if result.IsError {
		t.Fatalf("unexpected error: %#v", result.Value)
	}
	if result.WaitingReply || result.Value["waiting_reply"] != false {
		t.Fatalf("tool must resolve before returning, got %#v", result)
	}
	if result.Value["ask_type"] != "select_mode" {
		t.Fatalf("unexpected ask type: %#v", result.Value["ask_type"])
	}
	resultInfo, ok := result.Value["result"].(map[string]any)
	if !ok || resultInfo["type"] != "confirmation" {
		t.Fatalf("unexpected result metadata: %#v", result.Value["result"])
	}
	// No human replied, so the deadline decided and said so.
	if resultInfo["status"] != "auto_selected" {
		t.Fatalf("status must reflect the timeout, got %#v", resultInfo["status"])
	}
}

func TestLocalAskUserPersistsQuestionsInWorkspace(t *testing.T) {
	workspace := t.TempDir()
	result := executeLocalAskUser(t, workspace, `{"ask_type":"confirm",`+askTimeout+`"questions":[]}`)
	if result.IsError {
		t.Fatalf("unexpected error: %#v", result.Value)
	}
	path := filepath.Join(workspace, "ask_user", "ask_results.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var entries []map[string]any
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0]["ask_type"] != "confirm" {
		t.Fatalf("unexpected persisted entries: %#v", entries)
	}
	// answered is now true: the deadline resolved it and recorded that.
	if entries[0]["answered"] != true {
		t.Fatalf("entry should be resolved, got %#v", entries[0]["answered"])
	}
	if entries[0][askUserAutoSelectedParam] != true {
		t.Fatalf("entry should be flagged as auto-selected: %#v", entries[0])
	}
	// The questions payload must survive the resolution so the transcript
	// still shows what was asked.
	if entries[0]["questions"] == nil {
		t.Fatalf("questions must be preserved after resolution: %#v", entries[0])
	}
	if result.Value["stored"] != path {
		t.Fatalf("unexpected stored path: got %#v want %q", result.Value["stored"], path)
	}
}

func TestLocalAskUserNormalizesJSONStringQuestions(t *testing.T) {
	result := executeLocalAskUser(t, t.TempDir(),
		`{"ask_type":"select_mode",`+askTimeout+`"questions":"[{\"id\":\"one\",\"question\":\"First\"}]"}`)
	if result.IsError {
		t.Fatalf("unexpected error: %#v", result.Value)
	}
	questions, ok := result.Value["questions"].([]any)
	if !ok || len(questions) != 1 {
		t.Fatalf("expected normalized question array, got %T %#v", result.Value["questions"], result.Value["questions"])
	}
}
