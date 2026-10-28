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

// TestPlanAcceptsDescriptiveDoneWhen locks in the 2026-10 rule change: the
// done_when wording gate used to reject semantically checkable descriptions
// ("验收计划卡片脱离工具链") because a keyword table found no marker in them.
// 354 plan calls died on that one rule. Wording is now free; the hard gate is
// the evidence requirement on status=done.
func TestPlanAcceptsDescriptiveDoneWhen(t *testing.T) {
	workspace := t.TempDir()
	tool := NewLocalPlanTool(localFileTestSchema("plan"), workspace)

	// Still rejected: an empty done_when. Required means required.
	result := executeLocalFileTool(t, tool, workspace, `{"action":"update","items":[{"id":"one","action":"inspect"}]}`)
	if !result.IsError || !strings.Contains(result.Value["error"].(string), "done_when") {
		t.Fatalf("missing done_when must be rejected: %#v", result.Value)
	}

	// Now accepted: pure prose, zero keywords, previously a hard reject.
	result = executeLocalFileTool(t, tool, workspace, `{"action":"update","items":[{"id":"one","action":"inspect the card","done_when":"验收计划卡片脱离工具链"}]}`)
	if result.IsError {
		t.Fatalf("descriptive done_when must be accepted: %#v", result.Value)
	}
	result = executeLocalFileTool(t, tool, workspace, `{"action":"update","items":[{"id":"two","action":"verify","done_when":"看起来应该可以"}]}`)
	if result.IsError {
		t.Fatalf("vague done_when must be accepted at update time: %#v", result.Value)
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
		t.Fatalf("accept failed: %#v", result.Value)
	}
	if accepted, _ := result.Value["accepted"].(bool); !accepted {
		t.Fatalf("accept did not close the plan: %#v", result.Value)
	}
}

func TestPinnedToolCallIDsReleaseAfterModelVerification(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, ".plan.json")
	// A done item without an evidence summary has not been verified yet, so
	// its evidence stays pinned until the plan is accepted.
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

	// Once the model records its own evidence summary the item counts as
	// verified, and the evidence can be released before acceptance.
	doc = readPlanOrEmpty(path)
	doc.Items[0].Evidence = []PlanEvidence{{
		Kind: "command", Ref: "go test ./...", Summary: "go test ./... exited 0",
	}}
	if err := writePlan(path, &doc); err != nil {
		t.Fatal(err)
	}
	if pinned = PinnedToolCallIDs(path); len(pinned) != 0 {
		t.Fatalf("verified evidence should be released: %#v", pinned)
	}
}

func TestPlanAcceptsChineseObservableDoneWhen(t *testing.T) {
	workspace := t.TempDir()
	tool := NewLocalPlanTool(localFileTestSchema("plan"), workspace)
	result := executeLocalFileTool(t, tool, workspace, `{"action":"update","items":[{"id":"shot","action":"给卡片截图","done_when":"浏览器打开 localhost:5173 并截图保存到 workspace/result/after.png"}]}`)
	if result.IsError {
		t.Fatalf("chinese observable done_when must be accepted: %#v", result.Value)
	}
}

func TestPlanReportsEveryProblemAtOnce(t *testing.T) {
	workspace := t.TempDir()
	tool := NewLocalPlanTool(localFileTestSchema("plan"), workspace)
	result := executeLocalFileTool(t, tool, workspace, `{"action":"update","items":[
		{"id":"a","done_when":"看起来应该可以"},
		{"id":"b","action":"第二步"},
		{"id":"c","action":"第三步","done_when":"截图保存到 out.png","depends_on":["ghost"]}
	]}`)
	if !result.IsError {
		t.Fatalf("invalid plan must be rejected: %#v", result.Value)
	}
	message, _ := result.Value["error"].(string)
	for _, want := range []string{`"a"`, `"b"`, `"c"`, "ghost", "3 problem(s)"} {
		if !strings.Contains(message, want) {
			t.Fatalf("error must mention %s in one pass, got: %s", want, message)
		}
	}
}

// TestPlanDoneGateLivesOnEvidence guards the other half of the rule change.
// The done_when wording gate moved out of update; the evidence requirement on
// status=done is now the only hard gate, so it needs its own test. Without it
// a future refactor could quietly delete the constraint and every finished item
// would be an unverifiable claim.
func TestPlanDoneGateLivesOnEvidence(t *testing.T) {
	workspace := t.TempDir()
	tool := NewLocalPlanTool(localFileTestSchema("plan"), workspace)

	seed := executeLocalFileTool(t, tool, workspace, `{"action":"update","question":"ship it","items":[{"id":"build","action":"build the thing","done_when":"看结果对不对"}]}`)
	if seed.IsError {
		t.Fatalf("seed update failed: %#v", seed.Value)
	}

	// Rejected: marking done with no evidence of any kind.
	noEvidence := executeLocalFileTool(t, tool, workspace, `{"action":"mark","id":"build","status":"done"}`)
	if !noEvidence.IsError {
		t.Fatalf("done without evidence must be rejected: %#v", noEvidence.Value)
	}
	errText := noEvidence.Value["error"].(string)
	if !strings.Contains(errText, "evidence") {
		t.Fatalf("rejection must name evidence: %#v", noEvidence.Value)
	}

	// Rejected: evidence_refs present but no human-readable summary.
	refOnly := executeLocalFileTool(t, tool, workspace, `{"action":"mark","id":"build","status":"done","evidence_refs":["call_abc123"]}`)
	if !refOnly.IsError || !strings.Contains(refOnly.Value["error"].(string), "evidence summary") {
		t.Fatalf("done without evidence summary must be rejected: %#v", refOnly.Value)
	}

	// Accepted: refs plus a summary, even with vague done_when wording.
	ok := executeLocalFileTool(t, tool, workspace, `{"action":"mark","id":"build","status":"done","evidence_refs":["call_abc123"],"evidence":[{"kind":"test","ref":"go test ./internal/biz/tool/builtin/","tool_call_id":"call_abc123","summary":"go test 退出码 0，5 个 plan 用例全绿"}]}`)
	if ok.IsError {
		t.Fatalf("done with evidence must be accepted: %#v", ok.Value)
	}
}
