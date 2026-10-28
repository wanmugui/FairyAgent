package main

// 服务世代检测的行为测试。
//
// 覆盖 2026-10-24 暴露的缺陷：服务重启后，模型无从知道「服务在两轮之间
// 重启过」，只能靠 ps 猜。修法是让状态落盘、活过 agent 进程重启。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 用临时目录隔离状态文件，不碰真实 ~/.fairy/state。
func withStateDirs(t *testing.T) (serverFile, agentFile string) {
	t.Helper()
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	serverFile = filepath.Join(state, "server-generation.json")
	agentFile = filepath.Join(state, "agent-seen-generation.json")
	t.Setenv("FAIRY_SERVER_GENERATION_FILE", serverFile)
	t.Setenv("FAIRY_AGENT_SEEN_GENERATION_FILE", agentFile)
	return serverFile, agentFile
}

func writeServerGen(t *testing.T, path string, gen int, startedAt string) {
	t.Helper()
	payload := map[string]any{
		"generation":    gen,
		"startedAtUnix": 1,
		"startedAt":     startedAt,
		"pid":           1234,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write server gen: %v", err)
	}
}

func TestRestartNotice_NoServerFile(t *testing.T) {
	withStateDirs(t)
	// 服务端记录不存在时不该编造重启信息。
	if got := restartNotice(); got != "" {
		t.Fatalf("expected empty notice, got %q", got)
	}
}

func TestRestartNotice_FirstSight(t *testing.T) {
	serverFile, _ := withStateDirs(t)
	writeServerGen(t, serverFile, 1, "2026-10-24 22:00:00")

	got := restartNotice()
	if got == "" {
		t.Fatal("expected a first-sight notice, got empty")
	}
	// 第一次看见时不能声称"重启过"——没有上一轮可比。
	if want := "重启过"; strings.Contains(got, want) {
		t.Fatalf("first-sight notice must not claim a restart: %q", got)
	}
	if !strings.Contains(got, "#1") {
		t.Fatalf("first-sight notice should mention generation #1: %q", got)
	}
}

func TestRestartNotice_SameGeneration_NoRestart(t *testing.T) {
	serverFile, _ := withStateDirs(t)
	writeServerGen(t, serverFile, 3, "2026-10-24 22:00:00")

	// agent 记录：见过第 3 代
	writeSeen(t, 3)
	if got := restartNotice(); got != "" {
		t.Fatalf("same generation must not report a restart, got %q", got)
	}
}

func TestRestartNotice_DetectsRestart(t *testing.T) {
	serverFile, _ := withStateDirs(t)
	writeServerGen(t, serverFile, 4, "2026-10-24 23:00:00")

	// agent 上一轮见过第 3 代，服务现在已是第 4 代 → 重启过
	writeSeen(t, 3)
	got := restartNotice()
	if got == "" {
		t.Fatal("expected restart notice, got empty")
	}
	if !strings.Contains(got, "重启过") {
		t.Fatalf("notice should say restarted: %q", got)
	}
	if !strings.Contains(got, "#3") || !strings.Contains(got, "#4") {
		t.Fatalf("notice should show both generations: %q", got)
	}
}

func TestRestartNotice_MultipleRestarts(t *testing.T) {
	serverFile, _ := withStateDirs(t)
	writeServerGen(t, serverFile, 7, "2026-10-24 23:00:00")

	writeSeen(t, 4)
	got := restartNotice()
	if got == "" {
		t.Fatal("expected restart notice, got empty")
	}
	if !strings.Contains(got, "3 次") {
		t.Fatalf("notice should report restart count: %q", got)
	}
}

// syncSeenGeneration 之后，下一轮应当以新世代为基线。
func TestSyncSeenGeneration_UpdatesBaseline(t *testing.T) {
	serverFile, agentFile := withStateDirs(t)
	writeServerGen(t, serverFile, 5, "2026-10-24 22:00:00")

	writeSeen(t, 5)
	syncSeenGeneration()

	raw, err := os.ReadFile(agentFile)
	if err != nil {
		t.Fatalf("read agent file: %v", err)
	}
	var s seenGeneration
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if s.Generation != 5 {
		t.Fatalf("expected generation 5 recorded, got %d", s.Generation)
	}
}

// TestRuntimeFactsBlock_ReportsRestart 用真实的注入生成函数验证：
// 服务重启后，模型能在第一眼看到「重启过」，而不是自己去猜。
func TestRuntimeFactsBlock_ReportsRestart(t *testing.T) {
	serverFile, agentFile := withStateDirs(t)
	// 服务端现在是第 2 代，agent 上一轮见过第 1 代。
	writeServerGen(t, serverFile, 2, "2026-10-24 23:05:00")
	writeSeenAt(t, agentFile, 1)

	cfg := &Config{RepoRoot: "/path/to/Fairy"}
	out := runtimeFactsBlock(cfg)

	if !strings.Contains(out, "重启过") {
		t.Fatalf("注入应当明确告知服务重启过，实际:\n%s", out)
	}
	if !strings.Contains(out, "#1") || !strings.Contains(out, "#2") {
		t.Fatalf("注入应当给出世代号，实际:\n%s", out)
	}
}

func writeSeen(t *testing.T, gen int) {
	t.Helper()
	writeSeenAt(t, agentSeenGenerationFile(), gen)
}

func writeSeenAt(t *testing.T, f string, gen int) {
	t.Helper()
	if f == "" {
		t.Fatal("no agent seen generation file path")
	}
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	raw, _ := json.Marshal(seenGeneration{Generation: gen, At: "2026-10-24T22:00:00Z"})
	if err := os.WriteFile(f, raw, 0o644); err != nil {
		t.Fatalf("write seen: %v", err)
	}
}
