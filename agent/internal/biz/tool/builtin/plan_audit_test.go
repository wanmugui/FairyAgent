package builtin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDerivePlanAuditStatus(t *testing.T) {
	tests := []struct {
		name string
		in   map[string]float64
		want string
	}{
		{"pending", map[string]float64{}, "pending"},
		{"started", map[string]float64{"started": 0.8}, "in_progress"},
		{"blocked", map[string]float64{"started": 0.9, "blocked": 0.8}, "interrupted"},
		{"done", map[string]float64{"started": 0.9, "satisfied": 0.8}, "done"},
		{"skipped", map[string]float64{"abandoned": 0.9}, "skipped"},
		{"satisfied_beats_lower_abandoned", map[string]float64{"satisfied": 0.7, "abandoned": 0.5}, "done"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := derivePlanAuditStatus(tt.in); got != tt.want {
				t.Fatalf("derivePlanAuditStatus()=%q want %q", got, tt.want)
			}
		})
	}
}

func TestPlanAuditFailOpenMarksUnverified(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "session.plan.json")
	sessionPath := filepath.Join(dir, "session.json")
	doc := planDoc{Items: []PlanItem{{
		ID: "step-1", Status: "done", Action: "run tests",
		DoneWhen: "test output shows PASS",
		Evidence: []PlanEvidence{{Kind: "test", Ref: "call-1", Summary: "PASS"}},
	}}}
	if err := writePlan(planPath, &doc); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionPath, []byte(`{"messages":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAIRY_JEV_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("FAIRY_JEV_TIMEOUT_SEC", "1")
	t.Setenv("FAIRY_JEV_FAIL_OPEN", "true")

	result, err := planAudit(context.Background(), planPath, sessionPath, "step-1")
	if err != nil {
		t.Fatal(err)
	}
	results, _ := result.Value["results"].([]map[string]any)
	if len(results) != 1 || results[0]["audit_status"] != "unverified" || results[0]["audit_degraded"] != true {
		t.Fatalf("unexpected degraded audit result: %#v", result.Value["results"])
	}
	current := readPlanOrEmpty(planPath)
	if current.Items[0].AuditStatus != "unverified" || !current.Items[0].AuditDegraded || current.Items[0].AuditError == "" {
		t.Fatalf("degraded audit was not persisted: %#v", current.Items[0])
	}
}

func TestSelectPlanAuditItemsIncludesUnauditedDoneItems(t *testing.T) {
	items := []PlanItem{
		{ID: "done-needs-audit", Status: "done"},
		{ID: "done-audited", Status: "done", AuditStatus: "done"},
		{ID: "active", Status: "in_progress"},
		{ID: "skipped", Status: "skipped"},
	}
	selected := selectPlanAuditItems(items, "")
	if len(selected) != 2 || selected[0].ID != "done-needs-audit" || selected[1].ID != "active" {
		t.Fatalf("unexpected audit selection: %#v", selected)
	}
}

func TestPlanAuditHydratesItemContext(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "session.plan.json")
	sessionPath := filepath.Join(dir, "session.json")
	doc := planDoc{
		Question: "verify live plan state",
		Items: []PlanItem{
			{ID: "step-0", Status: "done", Action: "prepare fixture"},
			{
				ID:        "step-1",
				Status:    "in_progress",
				Action:    "implement audit",
				Details:   "hydrate the full ID record before classifying",
				DoneWhen:  "build passes",
				DependsOn: []string{"step-0"},
			},
		},
	}
	if err := writePlan(planPath, &doc); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	session := map[string]any{"messages": []map[string]any{
		{
			"role": "assistant",
			"tool_calls": []map[string]any{{
				"id": "call-plan",
				"function": map[string]any{
					"name":      "plan",
					"arguments": `{"action":"update","items":[{"id":"step-1","action":"implement audit"}]}`,
				},
			}},
		},
		{"role": "tool", "name": "plan", "tool_call_id": "call-plan", "content": `{"ok":true}`},
		{"role": "assistant", "content": "running npm run build"},
		{"role": "tool", "name": "bash", "tool_call_id": "call-build", "content": `{"exit_code":0,"ok":true,"result":"built in 1s"}`},
	}}
	raw, _ := json.Marshal(session)
	if err := os.WriteFile(sessionPath, raw, 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}

	var captured systemOneRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"answers":{"started":{"noul":0.97},"satisfied":{"noul":0.91},"blocked":{"noul":0.02},"abandoned":{"noul":0.01}},"usage":{"input_tokens":10,"output_tokens":4}}`))
	}))
	defer server.Close()
	t.Setenv("FAIRY_JEV_ENDPOINT", server.URL)
	t.Setenv("FAIRY_JEV_MODEL", "jev-test")

	result, err := planAudit(context.Background(), planPath, sessionPath, "step-1")
	if err != nil {
		t.Fatalf("planAudit: %v", err)
	}
	if !strings.Contains(captured.State, "任务ID: step-1") || !strings.Contains(captured.State, "任务细节: hydrate the full ID record") {
		t.Fatalf("state did not include hydrated item context:\n%s", captured.State)
	}
	if !strings.Contains(captured.State, "依赖任务: step-0[done] prepare fixture") {
		t.Fatalf("state did not include dependency status:\n%s", captured.State)
	}
	if !strings.Contains(captured.State, "[bash]") || !strings.Contains(captured.State, "built in 1s") {
		t.Fatalf("state did not include execution evidence:\n%s", captured.State)
	}
	results, ok := result.Value["results"].([]map[string]any)
	if !ok || len(results) != 1 {
		t.Fatalf("unexpected results: %#v", result.Value["results"])
	}
	if got := results[0]["proposed_status"]; got != "done" {
		t.Fatalf("proposed_status=%v want done; result=%#v", got, results[0])
	}
}

func TestPlanAuditLiveSystemOne(t *testing.T) {
	if os.Getenv("FAIRY_JEV_LIVE") != "1" {
		t.Skip("set FAIRY_JEV_LIVE=1 to call classifier.dev")
	}
	dir := t.TempDir()
	planPath := filepath.Join(dir, "session.plan.json")
	sessionPath := filepath.Join(dir, "session.json")
	doc := planDoc{
		Question: "verify live classifier wiring",
		Items: []PlanItem{{
			ID:       "step-1",
			Status:   "in_progress",
			Action:   "run the tests",
			Details:  "synthetic fixture only; no user session content",
			DoneWhen: "test output shows PASS",
		}},
	}
	if err := writePlan(planPath, &doc); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	session := map[string]any{"messages": []map[string]any{
		{"role": "assistant", "content": "running tests"},
		{"role": "tool", "name": "bash", "tool_call_id": "call-test", "content": `{"exit_code":0,"ok":true,"result":"PASS"}`},
	}}
	raw, _ := json.Marshal(session)
	if err := os.WriteFile(sessionPath, raw, 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	t.Setenv("FAIRY_JEV_ENDPOINT", "")
	result, err := planAudit(context.Background(), planPath, sessionPath, "step-1")
	if err != nil {
		t.Fatalf("planAudit live: %v", err)
	}
	results, ok := result.Value["results"].([]map[string]any)
	if !ok || len(results) != 1 {
		t.Fatalf("unexpected live results: %#v", result.Value["results"])
	}
	if errText, _ := results[0]["error"].(string); errText != "" {
		t.Fatalf("live classifier error: %s", errText)
	}
	if got := results[0]["proposed_status"]; got == "" || got == nil {
		t.Fatalf("missing proposed_status: %#v", results[0])
	}
	t.Logf("live classifier result: %#v", results[0])
}
