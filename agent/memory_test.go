package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractMemorySection(t *testing.T) {
	content := "[USER_PROFILE]\n- 用户使用 Windows\n- 中文环境\n\n[LONG_TERM_MEMORY]\n- 项目根目录 C:\\project\n\n[DATE_SUMMARY]\n- 完成了记忆模块\n"
	profile := extractMemorySection(content, memoryProfileMarker)
	long := extractMemorySection(content, memoryLongMarker)
	date := extractMemorySection(content, memoryDateMarker)
	if !strings.Contains(profile, "用户使用 Windows") || strings.Contains(profile, "项目根目录") {
		t.Fatalf("profile section wrong: %q", profile)
	}
	if !strings.Contains(long, "项目根目录") || strings.Contains(long, "[DATE_SUMMARY]") {
		t.Fatalf("long section wrong: %q", long)
	}
	if !strings.Contains(date, "完成了记忆模块") {
		t.Fatalf("date section wrong: %q", date)
	}
}

func TestAppendMemoryEntriesDedup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.md")
	appendMemoryEntries(path, "- 事实 A\n\n- 事实 B\n")
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "事实 A") || !strings.Contains(string(data), "事实 B") {
		t.Fatalf("first append missing content: %q", string(data))
	}
	appendMemoryEntries(path, "- 事实 A\n\n- 事实 C\n")
	data, _ = os.ReadFile(path)
	if strings.Count(string(data), "事实 A") != 1 {
		t.Fatalf("duplicate 事实 A should not re-append: %q", string(data))
	}
	if !strings.Contains(string(data), "事实 C") {
		t.Fatalf("new 事实 C missing: %q", string(data))
	}
}

func TestAppendDateSummaryDedup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "2026-08-23.md")
	appendDateSummary(path, "今天完成了记忆模块的接入。", "MiniMax-M3")
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "今天完成了记忆模块的接入。") || !strings.Contains(string(data), "MiniMax-M3") {
		t.Fatalf("date summary missing: %q", string(data))
	}
	appendDateSummary(path, "今天完成了记忆模块的接入。", "MiniMax-M3")
	data, _ = os.ReadFile(path)
	if strings.Count(string(data), "今天完成了记忆模块的接入。") != 1 {
		t.Fatalf("duplicate summary re-appended: %q", string(data))
	}
}

func TestCountUserTurnsSkipsNudges(t *testing.T) {
	msgs := []Message{
		NewMessage("user", "你好", nil, "", ""),
		NewMessage("assistant", "你好！", nil, "", ""),
		NewMessage("user", tutorialNudgeText, nil, "", ""),
		NewMessage("user", reflectionNudgeText, nil, "", ""),
		NewMessage("user", "继续", nil, "", ""),
	}
	if n := countUserTurns(msgs); n != 2 {
		t.Fatalf("expected 2 real user turns, got %d", n)
	}
}

func TestSessionSummaryThrottle(t *testing.T) {
	cfg := &Config{RepoRoot: t.TempDir(), MemoryDir: "memory", ConfigPath: filepath.Join(t.TempDir(), "x.json")}
	// Threshold relaxed: first summary fires after ANY real user turn so a
	// short session still captures explicit preferences.
	if !shouldSummarizeSession(cfg, 1) {
		t.Fatal("should summarize at 1 turn without stamp (relaxed threshold)")
	}
	stampSessionSummary(cfg, 2)
	if shouldSummarizeSession(cfg, 3) {
		t.Fatal("should not summarize 1 turn after stamp")
	}
	if !shouldSummarizeSession(cfg, 5) {
		t.Fatal("should summarize at +3 turns after stamp")
	}
	cfg2 := &Config{RepoRoot: t.TempDir(), MemoryDir: "memory", ConfigPath: filepath.Join(t.TempDir(), "y.json")}
	if !shouldSummarizeSession(cfg2, 2) {
		t.Fatal("different config should have its own throttle state")
	}
}

func TestSummarizeSkipsSubtask(t *testing.T) {
	t.Setenv("AGENT_RUN_KIND", "subtask")
	cfg := &Config{RepoRoot: t.TempDir(), MemoryDir: "memory"}
	msgs := []Message{NewMessage("user", strings.Repeat("请帮我完成记忆模块。", 30), nil, "", "")}
	if err := SummarizeAndStoreMemory(cfg, msgs, "test-model"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.RepoRoot, "memory")); !os.IsNotExist(err) {
		t.Fatal("subtask must not create memory dir")
	}
}

func TestSummarizeDisabled(t *testing.T) {
	disabled := false
	cfg := &Config{RepoRoot: t.TempDir(), MemoryDir: "memory", MemorySummarize: &disabled}
	msgs := []Message{NewMessage("user", strings.Repeat("请帮我完成记忆模块。", 30), nil, "", "")}
	if err := SummarizeAndStoreMemory(cfg, msgs, "test-model"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.RepoRoot, "memory")); !os.IsNotExist(err) {
		t.Fatal("disabled memory_summarize must not create memory dir")
	}
}

func TestReadRecentDateMemory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	dir := filepath.Join(root, "date-memory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 10; i++ {
		day := fmt.Sprintf("2026-08-%02d", i)
		if err := os.WriteFile(filepath.Join(dir, day+".md"), []byte("digest "+day), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &Config{RepoRoot: filepath.Dir(root), MemoryDir: "memory"}
	got := readRecentDateMemory(cfg)
	if !strings.Contains(got, "digest 2026-08-10") {
		t.Fatalf("missing newest digest: %q", got)
	}
	if strings.Contains(got, "digest 2026-08-03") {
		t.Fatalf("should not include digest older than last 7: %q", got)
	}
}

func TestSummarizeAndStoreWritesMemory(t *testing.T) {
	root := t.TempDir()
	mockPath := filepath.Join(root, "mock.json")
	mock := `[{"content":"[USER_PROFILE]\n- 用户是开发者，中文交流\n\n[LONG_TERM_MEMORY]\n- Fairy 项目使用 Go 语言\n\n[DATE_SUMMARY]\n- 完成了记忆模块的单元测试\n","finish_reason":"stop"}]`
	if err := os.WriteFile(mockPath, []byte(mock), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		RepoRoot:   root,
		MemoryDir:  "memory",
		UseMock:    true,
		MockFile:   "mock.json",
		ConfigPath: filepath.Join(root, "config.json"),
	}
	userText := strings.Repeat("请帮我完成记忆模块的实现并编写测试。", 8)
	msgs := []Message{
		NewMessage("user", userText, nil, "", ""),
		NewMessage("assistant", strings.Repeat("好的，我来实现。", 12), nil, "", ""),
		NewMessage("user", userText, nil, "", ""),
		NewMessage("assistant", "完成。", nil, "", ""),
	}
	if err := SummarizeAndStoreMemory(cfg, msgs, "mock-model"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, f := range []string{filepath.Join(root, "memory", "user.md"), filepath.Join(root, "memory", "memory.md")} {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if !strings.Contains(string(data), "用户是开发者") && !strings.Contains(string(data), "Go 语言") {
			t.Fatalf("unexpected content in %s: %q", f, string(data))
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, "memory", "date-memory"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("date-memory not written: %v (entries=%d)", err, len(entries))
	}
}

func TestBuildSystemPromptAppendsDateMemory(t *testing.T) {
	root := t.TempDir()
	partsDir := filepath.Join(root, "config", "system", "parts", "zh")
	if err := os.MkdirAll(partsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "config", "modules", "date_memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partsDir, "00_behavior.md"), []byte("BEHAVIOR"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "system", "parts", "manifest.yml"), []byte("zh:\n  - 00_behavior.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "modules", "date_memory", "zh.md"), []byte("# 最近几天\n{{ DATE_MEMORY_BLOCK }}"), 0o644); err != nil {
		t.Fatal(err)
	}
	memRoot := filepath.Join(root, "memory")
	if err := os.MkdirAll(filepath.Join(memRoot, "date-memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memRoot, "date-memory", "2026-08-22.md"), []byte("digest-22"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		RepoRoot:       root,
		SystemPartsDir: "config/system/parts/zh",
		MemoryDir:      "memory",
	}
	cfg.Prompts.SystemPath = "config/system/zh.md"
	got := BuildSystemPrompt(cfg)
	if !strings.Contains(got, "BEHAVIOR") {
		t.Fatalf("parts not assembled: %q", got)
	}
	if !strings.Contains(got, "digest-22") {
		t.Fatalf("date memory digest missing: %q", got)
	}
}

func TestBuildSystemPromptDoesNotInjectLongTermMemory(t *testing.T) {
	root := t.TempDir()
	partsDir := filepath.Join(root, "config", "system", "parts", "zh")
	if err := os.MkdirAll(partsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partsDir, "00_behavior.md"), []byte("BEHAVIOR\n{{ LONG_TERM_MEMORY }}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "system", "parts", "manifest.yml"), []byte("zh:\n  - 00_behavior.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	memRoot := filepath.Join(root, "memory")
	if err := os.MkdirAll(memRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memRoot, "memory.md"), []byte("SECRET_LONG_TERM_MEMORY"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		RepoRoot:       root,
		SystemPartsDir: "config/system/parts/zh",
		MemoryDir:      "memory",
	}
	cfg.Prompts.SystemPath = "config/system/zh.md"

	got := BuildSystemPrompt(cfg)
	if !strings.Contains(got, "BEHAVIOR") {
		t.Fatalf("parts not assembled: %q", got)
	}
	if strings.Contains(got, "SECRET_LONG_TERM_MEMORY") {
		t.Fatalf("long-term memory must not be injected into the system prompt: %q", got)
	}
}
