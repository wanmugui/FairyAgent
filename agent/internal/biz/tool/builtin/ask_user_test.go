package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

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

func TestLocalAskUserReturnsWaitingReply(t *testing.T) {
	result := executeLocalAskUser(t, t.TempDir(), `{"ask_type":"select_mode","questions":[{"id":"mode","question":"Choose"}]}`)
	if result.IsError {
		t.Fatalf("unexpected error: %#v", result.Value)
	}
	if !result.WaitingReply || result.Value["waiting_reply"] != true {
		t.Fatalf("expected waiting reply: %#v", result)
	}
	if result.Value["ask_type"] != "select_mode" {
		t.Fatalf("unexpected ask type: %#v", result.Value["ask_type"])
	}
	resultInfo, ok := result.Value["result"].(map[string]any)
	if !ok || resultInfo["status"] != "waiting_questions" || resultInfo["type"] != "confirmation" {
		t.Fatalf("unexpected result metadata: %#v", result.Value["result"])
	}
}

func TestLocalAskUserPersistsQuestionsInWorkspace(t *testing.T) {
	workspace := t.TempDir()
	result := executeLocalAskUser(t, workspace, `{"ask_type":"confirm","questions":[]}`)
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
	if len(entries) != 1 || entries[0]["ask_type"] != "confirm" || entries[0]["answered"] != false {
		t.Fatalf("unexpected persisted entries: %#v", entries)
	}
	if result.Value["stored"] != path {
		t.Fatalf("unexpected stored path: got %#v want %q", result.Value["stored"], path)
	}
}

func TestLocalAskUserNormalizesJSONStringQuestions(t *testing.T) {
	result := executeLocalAskUser(t, t.TempDir(), `{"ask_type":"select_mode","questions":"[{\"id\":\"one\",\"question\":\"First\"}]"}`)
	if result.IsError {
		t.Fatalf("unexpected error: %#v", result.Value)
	}
	questions, ok := result.Value["questions"].([]any)
	if !ok || len(questions) != 1 {
		t.Fatalf("expected normalized question array, got %T %#v", result.Value["questions"], result.Value["questions"])
	}
}
