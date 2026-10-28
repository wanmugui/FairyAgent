package main

import (
	"os"
	"path/filepath"
	"testing"
)

// 配置化之后，思考窗口的两个参数必须满足三件事：
// 1. 配置文件缺字段时落回包级默认值（5 条 / 3000 字符），与改动前的写死常量一致；
// 2. 配置文件写了值时，真的会推给读它们的运行时变量；
// 3. 非正数的垃圾值不能让窗口塌成 0（0 会把所有 thinking 都丢掉）。
func TestLoadConfigAppliesReasoningDefaults(t *testing.T) {
	prevKeep, prevChars := reasoningKeepRecent, maxThinkingChars
	t.Cleanup(func() {
		reasoningKeepRecent, maxThinkingChars = prevKeep, prevChars
	})
	reasoningKeepRecent, maxThinkingChars = defaultReasoningKeepRecent, defaultMaxThinkingChars

	dir := t.TempDir()

	cases := []struct {
		name      string
		body      string
		wantKeep  int
		wantChars int
	}{
		{
			name:      "缺字段回落默认值",
			body:      `{}`,
			wantKeep:  5,
			wantChars: 3000,
		},
		{
			name:      "显式配置生效",
			body:      `{"reasoning":{"keep_recent":9,"max_thinking_chars":1234}}`,
			wantKeep:  9,
			wantChars: 1234,
		},
		{
			name:      "零值不生效",
			body:      `{"reasoning":{"keep_recent":0,"max_thinking_chars":0}}`,
			wantKeep:  5,
			wantChars: 3000,
		},
		{
			name:      "负值不生效",
			body:      `{"reasoning":{"keep_recent":-3,"max_thinking_chars":-1}}`,
			wantKeep:  5,
			wantChars: 3000,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(dir, c.name+".json")
			if err := os.WriteFile(path, []byte(c.body), 0o644); err != nil {
				t.Fatalf("write config: %v", err)
			}
			cfg, err := LoadConfig(dir, path)
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if got := cfg.Reasoning.KeepRecentOrDefault(); got != c.wantKeep {
				t.Fatalf("KeepRecentOrDefault() = %d, want %d", got, c.wantKeep)
			}
			if got := cfg.Reasoning.MaxThinkingCharsOrDefault(); got != c.wantChars {
				t.Fatalf("MaxThinkingCharsOrDefault() = %d, want %d", got, c.wantChars)
			}
			if reasoningKeepRecent != c.wantKeep {
				t.Fatalf("运行时 reasoningKeepRecent = %d, want %d", reasoningKeepRecent, c.wantKeep)
			}
			if maxThinkingChars != c.wantChars {
				t.Fatalf("运行时 maxThinkingChars = %d, want %d", maxThinkingChars, c.wantChars)
			}
		})
	}
}

// 配置里的窗口必须真的改变裁剪行为，而不只是写进结构体。
func TestReasoningWindowDrivesRequestTrimming(t *testing.T) {
	prevKeep := reasoningKeepRecent
	t.Cleanup(func() { reasoningKeepRecent = prevKeep })

	setReasoningKeepRecent(3)
	msg := Message{Role: "assistant", ReasoningContent: "思考内容", Content: "<thinking>思考内容</thinking>正文"}

	if got := reasoningForRequest(msg, 3); got != "思考内容" {
		t.Fatalf("窗口内应保留 reasoning, got %q", got)
	}
	if got := reasoningForRequest(msg, 4); got != "" {
		t.Fatalf("窗口外应剥离 reasoning, got %q", got)
	}
	if got := contentForRequest(msg.Content, 4); got != "正文" {
		t.Fatalf("窗口外应剥离 Content 里的 thinking, got %q", got)
	}
}

// 配置里的字符上限必须真的改变截断行为。
func TestMaxThinkingCharsDrivesCapThinking(t *testing.T) {
	prev := maxThinkingChars
	t.Cleanup(func() { maxThinkingChars = prev })

	setMaxThinkingChars(10)
	long := "<thinking>" + string(make([]rune, 50)) + "abc" + "</thinking>正文"
	got := capThinking(long)
	// 截断后 thinking 应被压到 10 个字符以内。
	start := indexRunes(got, "<thinking>")
	end := indexRunes(got, "</thinking>")
	if start < 0 || end < 0 {
		t.Fatalf("capThinking 破坏了 thinking 标签: %q", got)
	}
	if n := len([]rune(got[start+len("<thinking>") : end])); n > 10 {
		t.Fatalf("thinking 截断后 %d 字符，超过配置上限 10", n)
	}
	if !contains(got, "正文") {
		t.Fatalf("capThinking 应保留正文: %q", got)
	}
}

func indexRunes(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func contains(s, sub string) bool {
	return indexRunes(s, sub) >= 0
}
