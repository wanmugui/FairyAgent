package main

import (
	"strings"
	"testing"
)

func TestThinkStreamerSeparatesThinkingAndVisible(t *testing.T) {
	s := &thinkStreamer{}
	feed := func(raw string) (string, string) { return s.Update(raw) }

	// thinking opens, nothing visible yet
	vis, think := feed("<think>先分析")
	if vis != "" {
		t.Fatalf("expected empty visible while thinking open, got %q", vis)
	}
	if think != "先分析" {
		t.Fatalf("expected thinking delta 先分析, got %q", think)
	}

	// thinking continues
	vis, think = feed("<think>先分析问题")
	if think != "问题" {
		t.Fatalf("expected thinking delta 问题, got %q", think)
	}

	// thinking closes, visible content starts
	vis, think = feed("<think>先分析问题</think>那么结论")
	if think != "" {
		t.Fatalf("expected no thinking delta after close, got %q", think)
	}
	if vis != "那么结论" {
		t.Fatalf("expected visible 那么结论, got %q", vis)
	}

	if got := s.Thinking(); got != "先分析问题" {
		t.Fatalf("expected accumulated thinking 先分析问题, got %q", got)
	}
}

func TestThinkStreamerStripsThinkAndPreservesMsg(t *testing.T) {
	s := &thinkStreamer{}
	raw := "<think>推理</think><msg>正在处理</msg>结果"
	vis, think := s.Update(raw)
	if think != "推理" {
		t.Fatalf("expected thinking 推理, got %q", think)
	}
	if strings.Contains(vis, "<think>") {
		t.Fatalf("visible text leaked protocol tags: %q", vis)
	}
	if vis != "<msg>正在处理</msg>结果" {
		t.Fatalf("visible text should preserve literal <msg> text, got %q", vis)
	}
}

func TestExtractThinkingAndStripThinking(t *testing.T) {
	content := "<think>A</think>\n正文<think>B</think>尾巴"
	if got := extractThinking(content); got != "AB" {
		t.Fatalf("extractThinking: got %q", got)
	}
	if got := stripThinking(content); got != "\n正文尾巴" {
		t.Fatalf("stripThinking: got %q", got)
	}
	if got := stripThinking("<think>未闭合"); got != "" {
		t.Fatalf("stripThinking unclosed: got %q", got)
	}
	if got := extractThinking("no think here"); got != "" {
		t.Fatalf("extractThinking empty: got %q", got)
	}
}

func TestThinkingTagAlias(t *testing.T) {
	content := "<thinking>先想清楚</thinking>然后动手"
	if got := extractThinking(content); got != "先想清楚" {
		t.Fatalf("extractThinking with <thinking>: got %q", got)
	}
	if got := stripThinking(content); got != "然后动手" {
		t.Fatalf("stripThinking with <thinking>: got %q", got)
	}
	s := &thinkStreamer{}
	vis, think := s.Update(content)
	if think != "先想清楚" || vis != "然后动手" {
		t.Fatalf("thinkStreamer with <thinking>: vis=%q think=%q", vis, think)
	}
}

func TestThinkStreamerCapsThinking(t *testing.T) {
	s := &thinkStreamer{}
	long := strings.Repeat("想", maxThinkingChars+100)
	raw := "<think>" + long + "</think>OK"
	vis, think := s.Update(raw)
	if think == "" {
		t.Fatal("expected some thinking")
	}
	if got := s.Thinking(); len(got) != maxThinkingChars {
		t.Fatalf("expected capped thinking length %d, got %d", maxThinkingChars, len(got))
	}
	if vis != "OK" {
		t.Fatalf("expected visible OK, got %q", vis)
	}
}

func TestCapThinking(t *testing.T) {
	short := "<thinking>短</thinking>正文"
	if got := capThinking(short); got != short {
		t.Fatalf("short thinking should pass through, got %q", got)
	}
	long := "<thinking>" + strings.Repeat("长", maxThinkingChars+50) + "</thinking>正文"
	got := capThinking(long)
	if runes := len([]rune(extractThinking(got))); runes > maxThinkingChars {
		t.Fatalf("capThinking did not bound thinking: %d runes", runes)
	}
	if !strings.HasSuffix(stripThinking(got), "正文") {
		t.Fatalf("capThinking should keep visible body, got %q", got)
	}
}

func TestIsRetryableStatus(t *testing.T) {
	for _, s := range []int{429, 500, 502, 503, 504} {
		if !isRetryableStatus(s) {
			t.Fatalf("expected %d retryable", s)
		}
	}
	for _, s := range []int{400, 401, 403, 404, 200} {
		if isRetryableStatus(s) {
			t.Fatalf("expected %d non-retryable", s)
		}
	}
}
