package main

import "testing"

// reasoningForRequest 控制历史 reasoning 是否进入请求载荷。
// 窗口内保留（保护进行中的工具调用链），窗口外裁掉（避免每轮重复计费）。
func TestReasoningForRequest(t *testing.T) {
	prevKeep, prevStrip := reasoningKeepRecent, stripHistoryReasoning
	t.Cleanup(func() {
		reasoningKeepRecent, stripHistoryReasoning = prevKeep, prevStrip
	})
	reasoningKeepRecent = 8
	stripHistoryReasoning = true

	msg := Message{Role: "assistant", ReasoningContent: "思考内容"}

	cases := []struct {
		name     string
		tailRank int
		want     string
	}{
		{"窗口内最近一条", 1, "思考内容"},
		{"窗口边界", 8, "思考内容"},
		{"刚出窗口", 9, ""},
		{"很早的消息", 500, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := reasoningForRequest(msg, c.tailRank); got != c.want {
				t.Fatalf("tailRank=%d: got %q, want %q", c.tailRank, got, c.want)
			}
		})
	}

	t.Run("空 reasoning 返回空", func(t *testing.T) {
		if got := reasoningForRequest(Message{Role: "assistant"}, 1); got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})

	t.Run("开关关闭时全部重发", func(t *testing.T) {
		stripHistoryReasoning = false
		defer func() { stripHistoryReasoning = true }()
		if got := reasoningForRequest(msg, 9999); got != "思考内容" {
			t.Fatalf("got %q, want full reasoning", got)
		}
	})
}

// 端到端：构建请求载荷时，窗口外的 assistant 消息不应带 reasoning，
// 且不影响消息条数、角色顺序与正文内容。
func TestAPIMessagesForRequestTrimsOldReasoning(t *testing.T) {
	prevKeep, prevStrip := reasoningKeepRecent, stripHistoryReasoning
	t.Cleanup(func() {
		reasoningKeepRecent, stripHistoryReasoning = prevKeep, prevStrip
	})
	reasoningKeepRecent = 2
	stripHistoryReasoning = true

	msgs := []Message{
		{Role: "user", Content: "第一条"},
		{Role: "assistant", Content: "回答一", ReasoningContent: "想一"},
		{Role: "user", Content: "第二条"},
		{Role: "assistant", Content: "回答二", ReasoningContent: "想二"},
		{Role: "user", Content: "第三条"},
		{Role: "assistant", Content: "回答三", ReasoningContent: "想三"},
	}

	got := apiMessagesForRequest(msgs)
	if len(got) != len(msgs) {
		t.Fatalf("消息条数变化: got %d, want %d", len(got), len(msgs))
	}
	// 前两条 assistant 在窗口外(tailRank 5 和 3)，最后一条在窗口内(tailRank 1)
	if got[1].ReasoningContent != "" {
		t.Fatalf("窗口外 reasoning 未裁剪: %q", got[1].ReasoningContent)
	}
	if got[3].ReasoningContent != "" {
		t.Fatalf("窗口外 reasoning 未裁剪: %q", got[3].ReasoningContent)
	}
	if got[5].ReasoningContent != "想三" {
		t.Fatalf("窗口内 reasoning 被误裁: %q", got[5].ReasoningContent)
	}
	// 正文与角色不受影响
	if got[5].Content != "回答三" || got[5].Role != "assistant" {
		t.Fatalf("正文或角色被破坏: %+v", got[5])
	}
	if got[0].Content != "第一条" {
		t.Fatalf("用户消息被破坏: %+v", got[0])
	}
}

// ApplyReasoningCleanup 把 reasoning 与工具结果放在同一套窗口规则下清理：
// 窗口内保留，窗口外清空，并且只动 reasoning、不碰正文。
func TestApplyReasoningCleanup(t *testing.T) {
	prevKeep, prevStrip := reasoningKeepRecent, stripHistoryReasoning
	t.Cleanup(func() {
		reasoningKeepRecent, stripHistoryReasoning = prevKeep, prevStrip
	})
	reasoningKeepRecent = 2
	stripHistoryReasoning = true

	mk := func() []Message {
		return []Message{
			{Role: "user", Content: "问题一"},
			{Role: "assistant", Content: "答一", ReasoningContent: "想想想一"},
			{Role: "user", Content: "问题二"},
			{Role: "assistant", Content: "答二", ReasoningContent: "想想想二"},
			{Role: "user", Content: "问题三"},
			{Role: "assistant", Content: "答三", ReasoningContent: "想想想三"},
		}
	}

	msgs := mk()
	cleared, saved := ApplyReasoningCleanup(msgs)
	if cleared != 2 {
		t.Fatalf("清理条数: got %d, want 2", cleared)
	}
	if want := len("想想想一") + len("想想想二"); saved != want {
		t.Fatalf("清理字符数: got %d, want %d", saved, want)
	}
	if msgs[1].ReasoningContent != "" || msgs[3].ReasoningContent != "" {
		t.Fatalf("窗口外 reasoning 未清理")
	}
	if msgs[5].ReasoningContent != "想想想三" {
		t.Fatalf("窗口内 reasoning 被误清: %q", msgs[5].ReasoningContent)
	}
	for i, m := range msgs {
		if m.Content == "" {
			t.Fatalf("第 %d 条正文被破坏", i)
		}
	}

	t.Run("开关关闭时不清理", func(t *testing.T) {
		stripHistoryReasoning = false
		defer func() { stripHistoryReasoning = true }()
		m := mk()
		if c, s := ApplyReasoningCleanup(m); c != 0 || s != 0 {
			t.Fatalf("开关关闭仍清理: cleared=%d saved=%d", c, s)
		}
		if m[1].ReasoningContent != "想想想一" {
			t.Fatalf("开关关闭时 reasoning 被清")
		}
	})

	t.Run("幂等", func(t *testing.T) {
		m := mk()
		ApplyReasoningCleanup(m)
		if c, s := ApplyReasoningCleanup(m); c != 0 || s != 0 {
			t.Fatalf("重复清理不应再计数: cleared=%d saved=%d", c, s)
		}
	})
}
