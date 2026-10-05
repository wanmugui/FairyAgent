package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestPlanToolScopesBySession guards the plan isolation contract: with a
// session file bound the plan must land in the per-session plans directory and
// must not touch the shared <workspace>/.plan.json.
func TestPlanToolScopesBySession(t *testing.T) {
	workspace := t.TempDir()
	sessionDir := t.TempDir()
	sessionFile := filepath.Join(sessionDir, "chat.json")

	tool := NewLocalPlanTool(localFileTestSchema("plan"), workspace)
	result, err := tool.Execute(context.Background(), ToolInvocation{
		Name:        "plan",
		Workspace:   workspace,
		SessionFile: sessionFile,
		Args:        json.RawMessage(`{"action":"update","items":"[{\"action\":\"do it\",\"done_when\":\"运行 go test 通过\"}]"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("plan update failed: %#v", result.Value)
	}

	scoped := filepath.Join(sessionDir, "plans", "chat.plan.json")
	if _, err := os.Stat(scoped); err != nil {
		t.Fatalf("session-scoped plan file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".plan.json")); err == nil {
		t.Fatal("plan leaked into the shared workspace file despite a bound session")
	}
}
