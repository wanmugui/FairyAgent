package builtin

import (
	"strings"
	"testing"
)

// TestPlanAuditActionIsRemoved proves that plan(action=audit) fails fast
// instead of reaching an external classifier. The audit used to POST the
// session transcript to a remote service; that path is gone, and the tool
// must say so rather than silently doing nothing.
func TestPlanAuditActionIsRemoved(t *testing.T) {
	workspace := t.TempDir()
	tool := NewLocalPlanTool(localFileTestSchema("plan"), workspace)
	result := executeLocalFileTool(t, tool, workspace, `{"action":"audit","id":"anything"}`)
	if !result.IsError {
		t.Fatalf("plan(action=audit) must fail, got %#v", result.Value)
	}
	message, _ := result.Value["error"].(string)
	if !strings.Contains(message, "audit") {
		t.Fatalf("the error should name the removed action: %q", message)
	}
}
