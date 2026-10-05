package builtin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalPlanToolUpdateViewMark(t *testing.T) {
	workspace := t.TempDir()
	tool := NewLocalPlanTool(localFileTestSchema("plan"), workspace)

	// update: create plan with two items (one without id/status to test backfill)
	result := executeLocalFileTool(t, tool, workspace, `{"action":"update","question":"build a blog","items":"[{\"action\":\"write posts\",\"done_when\":\"运行 go test ./... 通过\"},{\"action\":\"deploy\",\"id\":\"step-deploy\",\"status\":\"pending\",\"done_when\":\"执行 deploy check 并确认 exit 0\"}]"}`)
	if result.IsError {
		t.Fatalf("update failed: %#v", result.Value)
	}
	items, _ := result.Value["items"].([]PlanItem)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].ID != "step-1" || items[0].Status != "pending" {
		t.Fatalf("backfill failed: %#v", items[0])
	}

	// mark step-1 done
	result = executeLocalFileTool(t, tool, workspace, `{"action":"mark","id":"step-1","status":"done","evidence_refs":["call-1"],"evidence":[{"kind":"test","ref":"call-1","tool_call_id":"call-1","summary":"go test passed"}]}`)
	if result.IsError {
		t.Fatalf("mark failed: %#v", result.Value)
	}

	// view: step-1 done, step-deploy pending
	result = executeLocalFileTool(t, tool, workspace, `{"action":"view"}`)
	if result.IsError {
		t.Fatalf("view failed: %#v", result.Value)
	}
	byStatus, _ := result.Value["by_status"].(map[string]int)
	if byStatus["done"] != 1 || byStatus["pending"] != 1 {
		t.Fatalf("unexpected by_status: %#v", byStatus)
	}

	// update with same id preserves done status (merge semantics)
	result = executeLocalFileTool(t, tool, workspace, `{"action":"update","items":"[{\"action\":\"write posts\",\"id\":\"step-1\",\"done_when\":\"运行 go test ./... 通过\"}]"}`)
	if result.IsError {
		t.Fatalf("merge update failed: %#v", result.Value)
	}
	items, _ = result.Value["items"].([]PlanItem)
	if len(items) != 2 {
		t.Fatalf("merge should preserve step-deploy, got %d items", len(items))
	}
	// Items are sorted by status; find step-1 by id, not by index.
	var step1 *PlanItem
	for i := range items {
		if items[i].ID == "step-1" {
			step1 = &items[i]
		}
	}
	if step1 == nil {
		t.Fatalf("step-1 vanished after merge: %#v", items)
	}
	if step1.Status != "done" {
		t.Fatalf("merge should keep done status, got %#v", *step1)
	}

	// file exists on disk and is valid JSON
	data, err := os.ReadFile(filepath.Join(workspace, ".plan.json"))
	if err != nil {
		t.Fatalf("plan file missing: %v", err)
	}
	var doc planDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("plan file invalid JSON: %v", err)
	}
}

func TestLocalPlanToolMarkUnknownID(t *testing.T) {
	workspace := t.TempDir()
	tool := NewLocalPlanTool(localFileTestSchema("plan"), workspace)
	result := executeLocalFileTool(t, tool, workspace, `{"action":"mark","id":"nope","status":"done"}`)
	if !result.IsError {
		t.Fatalf("mark with unknown id should error, got %#v", result.Value)
	}
}

func TestLocalPlanToolAcceptsArrayItems(t *testing.T) {
	workspace := t.TempDir()
	tool := NewLocalPlanTool(localFileTestSchema("plan"), workspace)
	result := executeLocalFileTool(t, tool, workspace, `{
		"action":"update",
		"question":"plan with array",
		"items":[
			{"id":"step-1","action":"inspect","status":"in_progress","done_when":"运行 go test 通过"},
			{"id":"step-2","action":"implement","done_when":"运行 node --test 通过"}
		]
	}`)
	if result.IsError {
		t.Fatalf("array update failed: %#v", result.Value)
	}
	items, _ := result.Value["items"].([]PlanItem)
	if len(items) != 2 || items[0].ID != "step-1" {
		t.Fatalf("unexpected items: %#v", items)
	}
}

func TestLocalPlanToolBadStatus(t *testing.T) {
	workspace := t.TempDir()
	tool := NewLocalPlanTool(localFileTestSchema("plan"), workspace)
	result := executeLocalFileTool(t, tool, workspace, `{"action":"mark","id":"x","status":"finished"}`)
	if !result.IsError {
		t.Fatalf("invalid status should error, got %#v", result.Value)
	}
}

func TestParsePlanEvidenceAcceptsCommonModelSynonyms(t *testing.T) {
	evidence, err := parsePlanEvidence(`[
		{"path":"E:\\Fairy\\agent\\config.go","summary":"tests pass"},
		{"command":"go test ./...","summary":"exit 0"},
		{"tool_call_id":"call-1","summary":"tool result"}
	]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 3 {
		t.Fatalf("evidence = %#v", evidence)
	}
	if evidence[0].Kind != "file" || evidence[1].Kind != "command" || evidence[2].Kind != "tool_result" {
		t.Fatalf("kinds were not inferred: %#v", evidence)
	}
}

func TestLocalPlanToolNewQuestionStartsNewPlan(t *testing.T) {
	workspace := t.TempDir()
	tool := NewLocalPlanTool(localFileTestSchema("plan"), workspace)

	result := executeLocalFileTool(t, tool, workspace, `{"action":"update","question":"build a blog","items":[{"id":"old-step","action":"write posts","status":"done","done_when":"运行 go test 通过","evidence_refs":["call-old"],"evidence":[{"kind":"test","ref":"call-old","tool_call_id":"call-old","summary":"go test passed"}]}]}`)
	if result.IsError {
		t.Fatalf("initial update failed: %#v", result.Value)
	}

	result = executeLocalFileTool(t, tool, workspace, `{"action":"update","question":"deploy a mobile app","items":[{"id":"new-step","action":"prepare release build","done_when":"运行 release build 并确认 exit 0"}]}`)
	if result.IsError {
		t.Fatalf("new-question update failed: %#v", result.Value)
	}
	items, _ := result.Value["items"].([]PlanItem)
	if len(items) != 1 || items[0].ID != "new-step" {
		t.Fatalf("new question should start a fresh plan, got %#v", items)
	}

	result = executeLocalFileTool(t, tool, workspace, `{"action":"view"}`)
	if result.IsError {
		t.Fatalf("view failed: %#v", result.Value)
	}
	if result.Value["question"] != "deploy a mobile app" {
		t.Fatalf("plan question was not replaced: %#v", result.Value["question"])
	}
	viewItems, _ := result.Value["items"].([]PlanItem)
	if len(viewItems) != 1 || viewItems[0].ID != "new-step" {
		t.Fatalf("old plan items survived a question change: %#v", viewItems)
	}
}

func TestPlanRequiresExecutableDoneWhen(t *testing.T) {
	workspace := t.TempDir()
	tool := NewLocalPlanTool(localFileTestSchema("plan"), workspace)
	result := executeLocalFileTool(t, tool, workspace, `{"action":"update","items":[{"id":"one","action":"inspect"}]}`)
	if !result.IsError || !strings.Contains(result.Value["error"].(string), "done_when") {
		t.Fatalf("missing done_when must be rejected: %#v", result.Value)
	}
	result = executeLocalFileTool(t, tool, workspace, `{"action":"update","items":[{"id":"one","action":"inspect","done_when":"看起来应该可以"}]}`)
	if !result.IsError || !strings.Contains(result.Value["error"].(string), "not executable") {
		t.Fatalf("non-executable done_when must be rejected: %#v", result.Value)
	}
}

func TestPlanRequiresChildrenForUmbrellaItems(t *testing.T) {
	workspace := t.TempDir()
	tool := NewLocalPlanTool(localFileTestSchema("plan"), workspace)
	result := executeLocalFileTool(t, tool, workspace, `{"action":"update","items":[{"id":"api","action":"实现 POST /api/models 与 DELETE /api/models、测试和文档","done_when":"运行 go test ./... 通过"}]}`)
	if !result.IsError || !strings.Contains(result.Value["error"].(string), "too broad") {
		t.Fatalf("umbrella item must be rejected: %#v", result.Value)
	}
	result = executeLocalFileTool(t, tool, workspace, `{"action":"update","items":[
		{"id":"api","action":"实现模型 API 与测试、文档","done_when":"运行 go test ./... 通过"},
		{"id":"api-post","parent_id":"api","action":"实现 POST /api/models","done_when":"运行 go test 通过"},
		{"id":"api-delete","parent_id":"api","action":"实现 DELETE /api/models/:id","done_when":"运行 go test 通过"},
		{"id":"api-doc","parent_id":"api","action":"更新模型 API 文档","done_when":"运行 markdown lint 通过"}
	]}`)
	if result.IsError {
		t.Fatalf("split umbrella item must be accepted: %#v", result.Value)
	}
}

func TestPlanMarkDoneRequiresEvidenceAndAcceptWorksWithoutJEV(t *testing.T) {
	workspace := t.TempDir()
	tool := NewLocalPlanTool(localFileTestSchema("plan"), workspace)
	result := executeLocalFileTool(t, tool, workspace, `{"action":"update","items":[{"id":"one","action":"implement","done_when":"运行 go test 通过"}]}`)
	if result.IsError {
		t.Fatalf("update failed: %#v", result.Value)
	}
	result = executeLocalFileTool(t, tool, workspace, `{"action":"mark","id":"one","status":"done"}`)
	if !result.IsError || !strings.Contains(result.Value["error"].(string), "without evidence") {
		t.Fatalf("done without evidence must be rejected: %#v", result.Value)
	}
	result = executeLocalFileTool(t, tool, workspace, `{"action":"mark","id":"one","status":"done","evidence_refs":["call-1"],"evidence":[{"kind":"test","ref":"call-1","tool_call_id":"call-1","summary":"go test passed"}]}`)
	if result.IsError {
		t.Fatalf("done with evidence failed: %#v", result.Value)
	}
	result = executeLocalFileTool(t, tool, workspace, `{"action":"accept","summary":"all verified"}`)
	if result.IsError {
		t.Fatalf("JEV-disabled accept failed: %#v", result.Value)
	}
	if accepted, _ := result.Value["accepted"].(bool); !accepted {
		t.Fatalf("accept did not close the plan: %#v", result.Value)
	}
}

func TestPinnedToolCallIDsReleaseAfterAudit(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, ".plan.json")
	doc := planDoc{Items: []PlanItem{{
		ID: "one", Status: "done", Action: "implement", DoneWhen: "运行 go test 通过",
		EvidenceRefs: []string{"call-1", "call-2"},
	}}}
	if err := writePlan(path, &doc); err != nil {
		t.Fatal(err)
	}
	pinned := PinnedToolCallIDs(path)
	if !pinned["call-1"] || !pinned["call-2"] {
		t.Fatalf("evidence refs were not pinned: %#v", pinned)
	}
	doc = readPlanOrEmpty(path)
	doc.Items[0].AuditStatus = "done"
	if err := writePlan(path, &doc); err != nil {
		t.Fatal(err)
	}
	if pinned = PinnedToolCallIDs(path); len(pinned) != 0 {
		t.Fatalf("audited evidence should be released: %#v", pinned)
	}
}
