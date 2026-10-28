package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePlanForTest(t *testing.T, dir string, items []map[string]any) {
	t.Helper()
	planDir := filepath.Join(dir, "plans")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"question": "修登录", "items": items})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, "chat.plan.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPlanStatusNoteFinishedPlanForbidsRedo(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENT_SESSION_FILE", filepath.Join(dir, "chat.json"))
	writePlanForTest(t, dir, []map[string]any{
		{"id": "s1", "status": "done", "action": "改后端", "done_when": "测试通过"},
		{"id": "s2", "status": "skipped", "action": "改前端", "done_when": "测试通过"},
	})
	cfg := &Config{RepoRoot: dir, WorkspaceDir: "workspace"}

	note := planStatusNote(cfg)
	if !strings.Contains(note, "全部完成") || !strings.Contains(note, "禁止重做") {
		t.Fatalf("finished plan must forbid redo: %q", note)
	}
	if strings.Contains(note, "<plan>") {
		t.Fatalf("finished plan must not be rendered as open items: %q", note)
	}
}

func TestPlanStatusNoteListsOnlyOpenItems(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENT_SESSION_FILE", filepath.Join(dir, "chat.json"))
	writePlanForTest(t, dir, []map[string]any{
		{"id": "s1", "status": "done", "action": "已经做完的后端改动", "done_when": "测试通过"},
		{"id": "s2", "status": "in_progress", "action": "正在做的联调", "done_when": "接口回包正确"},
		{"id": "s3", "status": "pending", "action": "还没开始的回归", "done_when": "全量测试通过"},
	})
	cfg := &Config{RepoRoot: dir, WorkspaceDir: "workspace"}

	note := planStatusNote(cfg)
	if !strings.Contains(note, "尚未完成") {
		t.Fatalf("open plan must say how to continue: %q", note)
	}
	if !strings.Contains(note, "<plan>") {
		t.Fatalf("open plan must carry the plan snapshot: %q", note)
	}
	for _, want := range []string{"正在做的联调", "还没开始的回归"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note missing open item %q: %q", want, note)
		}
	}
	if strings.Contains(note, "已经做完的后端改动") {
		t.Fatalf("note must not re-list finished items: %q", note)
	}
}

func TestPlanStatusNoteWithoutPlanIsEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENT_SESSION_FILE", filepath.Join(dir, "chat.json"))
	cfg := &Config{RepoRoot: dir, WorkspaceDir: "workspace"}
	if note := planStatusNote(cfg); note != "" {
		t.Fatalf("no plan file must produce no note, got %q", note)
	}
}
