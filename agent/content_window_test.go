package main

import (
	"strings"
	"testing"
)

// 回归测试：发给模型的历史消息里，窗口外的 <thinking> 块必须被剥离。
//
// 背景：部分模型（MiniMax 等）把思考写在 Content 的 <thinking> 标签里，
// 而不是独立的 reasoning_content 字段。原先的窗口清理只处理
// reasoning_content，Content 里的 thinking 被原样送出，每条历史消息最多
// 带 capThinking 上限（3000 字符）的思考，几十轮下来请求体会膨胀到
// 几十万 token，工具与思考历史的清理形同虚设。
func TestContentForRequestStripsThinkingOutsideWindow(t *testing.T) {
	const body = "这是回答正文。"
	content := "<thinking>推理过程很长的内容</thinking>" + body

	// 窗口内（tailRank <= reasoningKeepRecent）：保留 thinking
	kept := contentForRequest(content, 1)
	if !strings.Contains(kept, "<thinking>") {
		t.Fatalf("窗口内应保留 thinking，实际 %q", kept)
	}

	// 窗口外（tailRank > reasoningKeepRecent）：剥离 thinking，但保留正文
	stripped := contentForRequest(content, reasoningKeepRecent+1)
	if strings.Contains(stripped, "<thinking>") {
		t.Fatalf("窗口外应剥离 thinking，实际 %q", stripped)
	}
	if !strings.Contains(stripped, body) {
		t.Fatalf("窗口外应保留正文，实际 %q", stripped)
	}
}

func TestContentForRequestKeepsNonThinkingContent(t *testing.T) {
	const content = "没有任何思考标签的普通回答"
	if got := contentForRequest(content, reasoningKeepRecent+1); got != content {
		t.Fatalf("无 thinking 的内容不应被改动：got %q", got)
	}
}

func TestContentForRequestEmpty(t *testing.T) {
	if got := contentForRequest("", 1); got != "" {
		t.Fatalf("空内容应返回空串：got %q", got)
	}
}

// 窗口外的历史消息整体不应再携带任何 <thinking> 块。
func TestContentForRequestRemovesAllThinkingOutsideWindow(t *testing.T) {
	for rank := reasoningKeepRecent + 1; rank <= 50; rank++ {
		content := "<thinking>abc</thinking>正文一<thinking>def</thinking>正文二"
		got := contentForRequest(content, rank)
		if strings.Contains(got, "<thinking>") || strings.Contains(got, "abc") || strings.Contains(got, "def") {
			t.Fatalf("rank=%d 仍残留 thinking：%q", rank, got)
		}
		if !strings.Contains(got, "正文一") || !strings.Contains(got, "正文二") {
			t.Fatalf("rank=%d 正文被误删：%q", rank, got)
		}
	}
}
