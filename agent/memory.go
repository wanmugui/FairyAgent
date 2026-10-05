package main

import (
	"crypto/sha1"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	memoryProfileMarker = "[USER_PROFILE]"
	memoryLongMarker    = "[LONG_TERM_MEMORY]"
	memoryDateMarker    = "[DATE_SUMMARY]"

	// memorySummaryEvery: summarize a session's memory at most once per N new
	// user turns (the post-run LLM call is cheap but not free).
	memorySummaryEvery = 3
)

// ReadMemoryAgentPrompt returns the memory summarizer instructions
// (config/modules/mem_agent/{lang}.md).
func ReadMemoryAgentPrompt(cfg *Config) string {
	return ReadModulePrompt(cfg, "mem_agent", "zh")
}

// SummarizeAndStoreMemory condenses a finished session into long-term memory,
// OpenClaw-style: USER.md (imperative profile directives), MEMORY.md (curated
// durable facts) and date-memory/YYYY-MM-DD.md (daily digest). It runs as a
// silent post-run step, never inside the interactive loop. Subtask agents
// never touch shared memory.
func SummarizeAndStoreMemory(cfg *Config, messages []Message, modelName string) error {
	if cfg == nil {
		return nil
	}
	if cfg.MemorySummarize != nil && !*cfg.MemorySummarize {
		return nil
	}
	memoryRoot := strings.TrimSpace(cfg.ResolvePath(cfg.MemoryDir))
	if memoryRoot == "" {
		return nil
	}
	// Subtask agents must not modify shared memory (memory policy).
	if strings.EqualFold(os.Getenv("AGENT_RUN_KIND"), "subtask") {
		return nil
	}
	userTurns := countUserTurns(messages)
	if userTurns < 1 {
		return nil
	}
	if !shouldSummarizeSession(cfg, userTurns) {
		return nil
	}
	transcript := buildMemoryTranscript(messages)
	if len([]rune(transcript)) < 200 {
		return nil
	}

	instruction := ReadMemoryAgentPrompt(cfg)
	if strings.TrimSpace(instruction) == "" {
		instruction = "Summarize the conversation into [USER_PROFILE], [LONG_TERM_MEMORY] and [DATE_SUMMARY] sections."
	}
	prompt := instruction +
		"\n\n[已有长期记忆]\n" + readMemoryFilesForSummary(memoryRoot) +
		"\n\n[会话记录]\n" + transcript +
		"\n\n请严格按上面的格式输出三段。"

	resp, err := CallConfiguredLLM(cfg, []Message{
		NewMessage("system", "You are a memory summarizer. Follow the instructions exactly. Output only the three sections.", nil, "", ""),
		NewMessage("user", prompt, nil, "", ""),
	}, nil)
	if err != nil {
		return fmt.Errorf("memory summary LLM: %w", err)
	}

	profile := extractMemorySection(resp.Content, memoryProfileMarker)
	long := extractMemorySection(resp.Content, memoryLongMarker)
	date := extractMemorySection(resp.Content, memoryDateMarker)
	if profile == "" && long == "" && date == "" {
		return nil
	}
	_ = os.MkdirAll(memoryRoot, 0o755)
	appendMemoryEntries(filepath.Join(memoryRoot, "user.md"), profile)
	appendMemoryEntries(filepath.Join(memoryRoot, "memory.md"), long)
	appendDateSummary(filepath.Join(memoryRoot, "date-memory", time.Now().Format("2006-01-02")+".md"), date, modelName)
	stampSessionSummary(cfg, userTurns)
	return nil
}

// countUserTurns returns the number of real user messages (excluding injected
// system nudges).
func countUserTurns(messages []Message) int {
	n := 0
	for _, m := range messages {
		if m.Role != "user" {
			continue
		}
		if m.Content == tutorialNudgeText || m.Content == reflectionNudgeText {
			continue
		}
		n++
	}
	return n
}

// shouldSummarizeSession throttles memory summarization to once per
// memorySummaryEvery new user turns per session, tracked by a stamp file.
// First summary happens after userTurns >= 1 (any real conversation); later
// summaries every memorySummaryEvery new turns. This guarantees the user's
// first explicit preference is captured on a short session, not lost to a
// "wait until next time" rule.
func shouldSummarizeSession(cfg *Config, userTurns int) bool {
	last, ok := readSessionStamp(cfg)
	if !ok {
		return userTurns >= 1
	}
	return userTurns-last >= memorySummaryEvery
}

func stampSessionSummary(cfg *Config, userTurns int) {
	root := strings.TrimSpace(cfg.ResolvePath(cfg.MemoryDir))
	if root == "" {
		return
	}
	stampPath := filepath.Join(root, ".stamps", sessionStampKey(cfg)+".txt")
	_ = os.MkdirAll(filepath.Dir(stampPath), 0o755)
	_ = os.WriteFile(stampPath, []byte(fmt.Sprintf("%d", userTurns)), 0o644)
}

func readSessionStamp(cfg *Config) (int, bool) {
	root := strings.TrimSpace(cfg.ResolvePath(cfg.MemoryDir))
	if root == "" {
		return 0, false
	}
	data, err := os.ReadFile(filepath.Join(root, ".stamps", sessionStampKey(cfg)+".txt"))
	if err != nil {
		return 0, false
	}
	var n int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &n); err != nil {
		return 0, false
	}
	return n, true
}

func sessionStampKey(cfg *Config) string {
	key := "default"
	if cfg.ConfigPath != "" {
		sum := sha1.Sum([]byte(cfg.ConfigPath))
		key = fmt.Sprintf("%x", sum)[:10]
	}
	return key
}

// buildMemoryTranscript condenses the session into a compact, user-visible
// transcript for the summarizer (no protocol tags, no tool noise).
func buildMemoryTranscript(messages []Message) string {
	var sb strings.Builder
	tail := messages
	if len(tail) > 40 {
		tail = tail[len(tail)-40:]
	}
	for _, m := range tail {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		text := strings.TrimSpace(m.Content)
		if text == "" || strings.HasPrefix(text, "<summary>") {
			continue
		}
		text = GetUserVisibleText(text)
		text = stripThinking(text)
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		runes := []rune(text)
		if len(runes) > 800 {
			text = string(runes[:800]) + "…"
		}
		sb.WriteString("[" + m.Role + "] " + text + "\n\n")
		if sb.Len() > 8000 {
			break
		}
	}
	return strings.TrimSpace(sb.String())
}

func readMemoryFilesForSummary(memoryRoot string) string {
	var sb strings.Builder
	for _, name := range []string{"user.md", "memory.md"} {
		data, err := os.ReadFile(filepath.Join(memoryRoot, name))
		if err != nil {
			continue
		}
		content := strings.TrimSpace(string(data))
		runes := []rune(content)
		if len(runes) > 4000 {
			content = string(runes[:4000]) + "…"
		}
		if content != "" {
			sb.WriteString("### " + name + "\n" + content + "\n\n")
		}
	}
	return strings.TrimSpace(sb.String())
}

// extractMemorySection pulls the text after marker up to the next marker/end.
func extractMemorySection(content, marker string) string {
	idx := strings.Index(content, marker)
	if idx < 0 {
		return ""
	}
	rest := content[idx+len(marker):]
	next := len(rest)
	for _, m := range []string{memoryProfileMarker, memoryLongMarker, memoryDateMarker} {
		if m == marker {
			continue
		}
		if i := strings.Index(rest, m); i >= 0 && i < next {
			next = i
		}
	}
	return strings.TrimSpace(rest[:next])
}

// appendMemoryEntries merges markdown paragraphs into a memory file, skipping
// paragraphs that already exist (OpenClaw-style: plain markdown, no HTML blocks).
func appendMemoryEntries(path, content string) {
	content = strings.TrimSpace(content)
	if content == "" {
		return
	}
	existing := ""
	if data, err := os.ReadFile(path); err == nil {
		existing = string(data)
	}
	var paragraphs []string
	for _, p := range strings.Split(content, "\n\n") {
		p = strings.TrimSpace(p)
		if p != "" {
			paragraphs = append(paragraphs, p)
		}
	}
	var out strings.Builder
	out.WriteString(existing)
	if existing != "" && !strings.HasSuffix(strings.TrimRight(existing, "\n"), "\n") {
		out.WriteString("\n")
	}
	appended := 0
	for _, p := range paragraphs {
		if strings.Contains(existing, p) {
			continue
		}
		out.WriteString(p + "\n")
		appended++
	}
	if appended == 0 {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte(out.String()), 0o644)
}

// appendDateSummary appends a session digest entry to today's date-memory file.
func appendDateSummary(path, summary, modelName string) {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return
	}
	header := "## " + time.Now().Format("15:04") + " 会话"
	if modelName != "" {
		header += "（" + modelName + "）"
	}
	existing := ""
	if data, err := os.ReadFile(path); err == nil {
		existing = string(data)
	}
	if strings.Contains(existing, summary) {
		return
	}
	prefix := ""
	if strings.TrimSpace(existing) != "" {
		prefix = "\n----\n\n"
	}
	entry := prefix + header + "\n" + summary + "\n"
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte(existing+entry), 0o644)
}
