package main

import (
	"testing"
)

// 回归测试：工具历史清理与 reasoning 窗口清理必须作用在不同上下文上。
//
// 历史背景：ApplyToolScoreCleanupByCallID 曾在函数末尾顺带调用
// ApplyReasoningCleanup，而后者是就地修改传入切片的。调用方对
// workingMsgs（模型可见上下文）和 transcript（用户可见上下文）
// 都调了这个函数，于是用户可见的思考内容也被一起清掉了——
// 历史回看时思考会整段消失。
//
// 现在 reasoning 清理从工具结果替换里拆了出来，只由调用方显式
// 对 workingMsgs 执行，transcript 不再受影响。

func TestApplyToolScoreCleanupByCallIDKeepsTranscriptReasoning(t *testing.T) {
	// 构造一条带 reasoning 的 assistant 消息。
	msgs := []Message{{
		Role:             "assistant",
		Content:          "回答",
		ReasoningContent: "这段思考必须保留",
	}}

	// 空替换列表：不做任何工具结果替换，但历史上这里会顺带清掉 reasoning。
	ApplyToolScoreCleanupByCallID(msgs, nil)

	if msgs[0].ReasoningContent == "" {
		t.Fatalf("工具结果清理不应清掉 reasoning，清理后为 %q", msgs[0].ReasoningContent)
	}
	if msgs[0].ReasoningContent != "这段思考必须保留" {
		t.Fatalf("reasoning 内容被改写：got %q", msgs[0].ReasoningContent)
	}
}

func TestApplyReasoningCleanupStillTrimsWorkingContext(t *testing.T) {
	// 模型可见上下文仍然要遵守窗口规则：窗口外的 reasoning 被清空。
	msgs := make([]Message, 0, reasoningKeepRecent+3)
	for i := 0; i < reasoningKeepRecent+3; i++ {
		msgs = append(msgs, Message{
			Role:             "assistant",
			Content:          "回答",
			ReasoningContent: "思考",
		})
	}

	cleared, _ := ApplyReasoningCleanup(msgs)
	if cleared != 3 {
		t.Fatalf("窗口外应清理 %d 条，实际 %d", 3, cleared)
	}
	for i := 0; i < 3; i++ {
		if msgs[i].ReasoningContent != "" {
			t.Fatalf("窗口外的 reasoning 未被清理：index %d = %q", i, msgs[i].ReasoningContent)
		}
	}
	for i := 3; i < len(msgs); i++ {
		if msgs[i].ReasoningContent == "" {
			t.Fatalf("窗口内的 reasoning 被误清理：index %d", i)
		}
	}
}
