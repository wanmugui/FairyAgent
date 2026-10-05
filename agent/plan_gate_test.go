package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentloop/agent/internal/biz/tool/builtin"
)

func TestPlanGateTreatsEveryNonTerminalStatusAsActive(t *testing.T) {
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "sessions", "main.json")
	planFile := builtin.PlanFilePathFor(dir, sessionFile)
	writePlanGateTestFile(t, planFile, `{
  "question":"finish the task",
  "items":[
    {"id":"done","status":"done","action":"finished"},
    {"id":"skipped","status":"skipped","action":"not needed"},
    {"id":"pending","status":"pending","action":"pending work"},
    {"id":"active","status":"in_progress","action":"active work"},
    {"id":"paused","status":"interrupted","action":"resume work"},
    {"id":"blocked","status":"blocked","action":"blocked work"},
    {"id":"waiting","status":"waiting_user","action":"waiting work"}
  ]
}`)

	cfg := &Config{WorkspaceDir: dir}
	snapshot, ok := loadPlanGateSnapshot(cfg, sessionFile)
	if !ok {
		t.Fatal("expected an active plan snapshot")
	}
	if len(snapshot.Items) != 5 {
		t.Fatalf("expected all non-terminal statuses to remain active, got %#v", snapshot.Items)
	}
	for _, item := range snapshot.Items {
		if item.ID == "done" || item.ID == "skipped" {
			t.Fatalf("terminal item leaked into continuation gate: %#v", item)
		}
	}
	if !containsAll(snapshot.Prompt, "pending work", "active work", "resume work", "blocked work", "waiting work") {
		t.Fatalf("continuation prompt omitted active items: %s", snapshot.Prompt)
	}
}

func TestPlanGateStopsOnlyAfterAnUnchangedNoProgressCheck(t *testing.T) {
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "sessions", "main.json")
	planFile := builtin.PlanFilePathFor(dir, sessionFile)
	writePlanGateTestFile(t, planFile, `{
  "question":"finish the task",
  "items":[{"id":"one","status":"pending","action":"do one thing"}]
}`)

	cfg := &Config{WorkspaceDir: dir}
	turn := planGateTurn{StartedAt: time.Now()}
	first := decidePlanContinuation(cfg, sessionFile, 0, "", 0, turn)
	if !first.HasActive || !first.ShouldRun || first.Stalled {
		t.Fatalf("first no-tool call must continue an active plan: %#v", first)
	}

	sameState := decidePlanContinuation(cfg, sessionFile, 0, first.Snapshot.Signature, 0, turn)
	if !sameState.HasActive || sameState.ShouldRun || !sameState.Stalled {
		t.Fatalf("unchanged plan with no tool progress must stall: %#v", sameState)
	}

	withToolProgress := decidePlanContinuation(cfg, sessionFile, 1, first.Snapshot.Signature, 0, turn)
	if !withToolProgress.ShouldRun || withToolProgress.Stalled {
		t.Fatalf("tool progress must allow another continuation: %#v", withToolProgress)
	}
}

func TestPlanGateDoesNotResumeAPlanLeftOverFromAnEarlierTurn(t *testing.T) {
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "sessions", "main.json")
	planFile := builtin.PlanFilePathFor(dir, sessionFile)
	writePlanGateTestFile(t, planFile, `{
  "question":"finish the task",
  "updated_at":"2026-10-01T09:29:32+08:00",
  "items":[{"id":"one","status":"interrupted","action":"do one thing"}]
}`)

	cfg := &Config{WorkspaceDir: dir}
	// The user came back eleven minutes later and asked something else.
	turn := planGateTurn{
		StartedAt: time.Date(2026, 10, 1, 9, 40, 0, 0, time.FixedZone("CST", 8*3600)),
	}
	decision := decidePlanContinuation(cfg, sessionFile, 0, "", 0, turn)

	if !decision.HasActive {
		t.Fatal("the unfinished item is still unfinished; the gate must see it")
	}
	if decision.ShouldRun || !decision.Stale {
		t.Fatalf("a plan older than this turn must not be pushed forward: %#v", decision)
	}
	if planContinuationAllowed(false, decision) {
		t.Fatal("answering the user outranks finishing a plan they did not ask to resume")
	}
}

func TestPlanGateTreatsAMissingTimestampAsCurrent(t *testing.T) {
	// Unknown vintage is not proof of staleness: a hand-written plan without
	// updated_at keeps the old behaviour rather than silently never running.
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "sessions", "main.json")
	planFile := builtin.PlanFilePathFor(dir, sessionFile)
	writePlanGateTestFile(t, planFile, `{
  "question":"finish the task",
  "items":[{"id":"one","status":"pending","action":"do one thing"}]
}`)

	cfg := &Config{WorkspaceDir: dir}
	decision := decidePlanContinuation(cfg, sessionFile, 0, "", 0, planGateTurn{StartedAt: time.Now()})
	if decision.Stale || !decision.ShouldRun {
		t.Fatalf("a plan with no timestamp must keep continuing: %#v", decision)
	}
}

func TestPlanGateResumesAnOlderPlanForARuntimeContinuationTurn(t *testing.T) {
	// A turn the runtime injected to resume a plan is the one case where an
	// older plan is exactly what the turn is for.
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "sessions", "main.json")
	planFile := builtin.PlanFilePathFor(dir, sessionFile)
	writePlanGateTestFile(t, planFile, `{
  "question":"finish the task",
  "updated_at":"2026-10-01T09:29:32+08:00",
  "items":[{"id":"one","status":"in_progress","action":"do one thing"}]
}`)

	cfg := &Config{WorkspaceDir: dir}
	decision := decidePlanContinuation(cfg, sessionFile, 0, "", 0, planGateTurn{
		StartedAt:           time.Date(2026, 10, 1, 9, 45, 0, 0, time.FixedZone("CST", 8*3600)),
		RuntimeContinuation: true,
	})
	if !decision.ShouldRun || decision.Stale {
		t.Fatalf("a runtime continuation must still resume the plan: %#v", decision)
	}
}

func TestPlanGateResumesAnOlderPlanWhenTheUserAsksToContinue(t *testing.T) {
	// "继续" is the user speaking about the plan, so the plan is also injected
	// into the prompt (buildPlanPrefix uses the same cue list). The gate follows
	// the same rule instead of inventing a second one.
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "sessions", "main.json")
	planFile := builtin.PlanFilePathFor(dir, sessionFile)
	writePlanGateTestFile(t, planFile, `{
  "question":"finish the task",
  "updated_at":"2026-10-01T09:29:32+08:00",
  "items":[{"id":"one","status":"interrupted","action":"do one thing"}]
}`)

	cfg := &Config{WorkspaceDir: dir}
	decision := decidePlanContinuation(cfg, sessionFile, 0, "", 0, planGateTurn{
		StartedAt:             time.Date(2026, 10, 1, 11, 0, 0, 0, time.FixedZone("CST", 8*3600)),
		ContinuationRequested: isPlanContinuationRequest(normalizePlanMatchText("继续")),
	})
	if !decision.ShouldRun || decision.Stale {
		t.Fatalf("an explicit 继续 must pick the plan back up: %#v", decision)
	}
	// And an unrelated sentence must not: this is the shape of the bug where a
	// short new request was answered with an hour-old plan.
	unrelated := decidePlanContinuation(cfg, sessionFile, 0, "", 0, planGateTurn{
		StartedAt:             time.Date(2026, 10, 1, 11, 0, 0, 0, time.FixedZone("CST", 8*3600)),
		ContinuationRequested: isPlanContinuationRequest(normalizePlanMatchText("【自检·QQ桥】请直接回复：桥收到")),
	})
	if !unrelated.Stale || unrelated.ShouldRun {
		t.Fatalf("an unrelated short message must leave the old plan alone: %#v", unrelated)
	}
}

func TestPlanGateRequiresFinalAcceptanceForTerminalPlan(t *testing.T) {
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "sessions", "main.json")
	planFile := builtin.PlanFilePathFor(dir, sessionFile)
	writePlanGateTestFile(t, planFile, `{
  "question":"finish the task",
  "items":[
    {"id":"one","status":"done","action":"done","done_when":"运行 go test 通过","evidence_refs":["call-1"]},
    {"id":"two","status":"skipped","action":"skip"}
  ]
}`)

	decision := decidePlanContinuation(&Config{WorkspaceDir: dir}, sessionFile, 2, "old", 0,
		planGateTurn{StartedAt: time.Now()})
	if !decision.HasActive || !decision.ShouldRun || decision.Stalled || !decision.Snapshot.NeedsFinalAcceptance {
		t.Fatalf("terminal plan must require final acceptance: %#v", decision)
	}
	if !strings.Contains(decision.Snapshot.Prompt, "External JEV audit is optional") ||
		!strings.Contains(decision.Snapshot.Prompt, "plan(action=accept") {
		t.Fatalf("terminal plan should accept without JEV: %s", decision.Snapshot.Prompt)
	}

	writePlanGateTestFile(t, planFile, `{
  "question":"finish the task",
  "accepted":true,
  "items":[
    {"id":"one","status":"done","action":"done","done_when":"运行 go test 通过","evidence_refs":["call-1"],"audit_status":"done"}
  ]
}`)
	accepted := decidePlanContinuation(&Config{WorkspaceDir: dir}, sessionFile, 2, "old", 0,
		planGateTurn{StartedAt: time.Now()})
	if accepted.HasActive || accepted.ShouldRun || accepted.Stalled {
		t.Fatalf("accepted plan must not gate the turn: %#v", accepted)
	}
}

func TestAgentLoopContinuesOnceWhenPlanIsUnfinished(t *testing.T) {
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "sessions", "main.json")
	planFile := builtin.PlanFilePathFor(dir, sessionFile)
	writePlanGateTestFile(t, planFile, `{
  "question":"finish the task",
  "items":[{"id":"one","status":"pending","action":"do one thing"}]
}`)

	mockPath := writeMockFile(t, dir, []map[string]any{
		{"finish_reason": "stop", "content": "I will continue."},
		{"finish_reason": "stop", "content": "Still waiting."},
	})
	cfg := &Config{
		UseMock:        true,
		MockFile:       mockPath,
		RepoRoot:       repoRootForTest(t),
		SystemPartsDir: "config/system/parts/zh",
		WorkspaceDir:   dir,
	}
	result, err := RunAgentLoop(cfg, NewToolRegistry(), "", "continue the background plan", nil, nil, "", "", "", sessionFile, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Steps != 2 {
		t.Fatalf("expected one plan continuation before the no-progress stop, got steps=%d", result.Steps)
	}
	var continued, stalled bool
	for _, event := range result.Trace {
		switch event["event"] {
		case "plan_continuation":
			continued = true
		case "plan_gate_stalled":
			stalled = true
		}
	}
	if !continued || !stalled {
		t.Fatalf("missing plan gate trace events: continued=%v stalled=%v trace=%#v", continued, stalled, result.Trace)
	}
}

func writePlanGateTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func containsAll(text string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(text, needle) {
			return false
		}
	}
	return true
}
