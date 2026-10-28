package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInjectRequestContextIncludesPlanPrefix(t *testing.T) {
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "chat.json")
	t.Setenv("AGENT_SESSION_FILE", sessionFile)
	planDir := filepath.Join(dir, "plans")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{
		"question": "ship it",
		"items": []map[string]any{
			{"id": "step-1", "status": "in_progress", "action": "wire injector"},
			{"id": "step-2", "status": "done", "action": "port store"},
		},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, "chat.plan.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{RepoRoot: dir, WorkspaceDir: "workspace"}
	got := injectRequestContext("keep going", cfg)
	if !strings.HasPrefix(got, "<plan>") {
		t.Fatalf("plan must be the request-context prefix: %q", got)
	}
	if !strings.Contains(got, "wire injector") {
		t.Fatalf("plan prefix missing: %q", got)
	}
	if strings.Contains(got, "port store") {
		t.Fatalf("finished plan items must not be injected: %q", got)
	}
	if !strings.HasSuffix(got, "keep going") {
		t.Fatalf("user message must stay at the end: %q", got)
	}
}

func TestInjectRequestContextWithoutPlanIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AGENT_SESSION_FILE", filepath.Join(dir, "chat.json"))
	cfg := &Config{RepoRoot: dir, WorkspaceDir: "workspace"}
	if got := injectRequestContext("hello", cfg); got != "hello" {
		t.Fatalf("message without a plan must stay unchanged: %q", got)
	}
}

func TestInjectRequestContextSkipsUnrelatedPlan(t *testing.T) {
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "chat.json")
	t.Setenv("AGENT_SESSION_FILE", sessionFile)
	planDir := filepath.Join(dir, "plans")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{
		"question": "ship the injector",
		"items": []map[string]any{
			{"id": "step-1", "status": "in_progress", "action": "wire injector"},
		},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, "chat.plan.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{RepoRoot: dir, WorkspaceDir: "workspace"}
	if got := injectRequestContext("write a poem about rain", cfg); got != "write a poem about rain" {
		t.Fatalf("unrelated request must not inherit a stale plan: %q", got)
	}
}

func TestInjectRequestContextContinuationSkipsInjection(t *testing.T) {
	cfg := &Config{RepoRoot: t.TempDir(), WorkspaceDir: "workspace"}
	if got := injectRequestContext("", cfg); got != "" {
		t.Fatalf("continuation run must not inject: %q", got)
	}
}

func TestStripRequestContextPrefix(t *testing.T) {
	plan := "<plan>{\"items\":[]}</plan>\n"
	for _, test := range []struct {
		name string
		in   string
		want string
	}{
		{"plain", "hello world", "hello world"},
		{"empty", "", ""},
		{"plan", plan + "hello", "hello"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := stripRequestContextPrefix(test.in); got != test.want {
				t.Fatalf("strip failed: got %q want %q", got, test.want)
			}
		})
	}
}

func TestSaveSessionStripsPlanPrefix(t *testing.T) {
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "chat.json")
	t.Setenv("AGENT_SESSION_FILE", sessionFile)
	planDir := filepath.Join(dir, "plans")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rawPlan, _ := json.Marshal(map[string]any{
		"question": "q",
		"items": []map[string]any{
			{"id": "step-1", "status": "in_progress", "action": "original user text"},
		},
	})
	if err := os.WriteFile(filepath.Join(planDir, "chat.plan.json"), rawPlan, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{RepoRoot: dir, WorkspaceDir: "workspace"}
	wrapped := injectRequestContext("original user text", cfg)
	if !strings.HasPrefix(wrapped, "<plan>") {
		t.Fatalf("expected a plan-prefixed message, got %q", wrapped)
	}
	if err := SaveSession(sessionFile, []Message{{Role: "user", Content: wrapped}}, "m"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(sessionFile)
	if err != nil {
		t.Fatal(err)
	}
	var session struct {
		Messages []Message `json:"messages"`
	}
	if err := json.Unmarshal(raw, &session); err != nil {
		t.Fatal(err)
	}
	if len(session.Messages) != 1 || session.Messages[0].Content != "original user text" {
		t.Fatalf("injected prefix leaked into history: %#v", session.Messages)
	}
}
