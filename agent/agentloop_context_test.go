package main

import (
	"strings"
	"testing"
)

func TestSelectCompressionPlanKeepsCompleteRecentRound(t *testing.T) {
	messages := []Message{
		NewMessage("system", "system", nil, "", ""),
		NewMessage("user", "initial request", nil, "", ""),
		NewMessage("assistant", "old answer 1", nil, "", ""),
		NewMessage("tool", "old result 1", nil, "call-1", "bash"),
		NewMessage("assistant", "old answer 2", nil, "", ""),
		NewMessage("tool", "old result 2", nil, "call-2", "bash"),
		NewMessage("assistant", "old answer 3", nil, "", ""),
		NewMessage("tool", "old result 3", nil, "call-3", "bash"),
		NewMessage("assistant", strings.Repeat("latest", 100), nil, "", ""),
	}

	// The token estimate now counts ASCII at ~4 characters per token, so the
	// budget is scaled to match this fixture's size.
	plan, ok := selectCompressionPlan(messages, 200, 0)
	if !ok {
		t.Fatal("expected a compression plan")
	}
	if plan.compressStart != 2 || plan.compressEnd != 7 {
		t.Fatalf("unexpected compression window: [%d,%d]", plan.compressStart, plan.compressEnd)
	}
	if len(plan.retainedTail) != 1 || plan.retainedTail[0].Content != strings.Repeat("latest", 100) {
		t.Fatalf("newest complete round was not retained: %#v", plan.retainedTail)
	}
	if len(plan.retainedUsers) != 1 || plan.retainedUsers[0].Content != "initial request" {
		t.Fatalf("recent user intent was not retained: %#v", plan.retainedUsers)
	}
}

func TestSummaryMarkerSupportsInternalTypeAndLegacyTag(t *testing.T) {
	typed := NewMessage("assistant", "compressed history", nil, "", "")
	typed.InternalType = "context_summary"
	if !isSummaryMarker(typed) {
		t.Fatal("context_summary internal type should be recognized")
	}

	legacy := NewMessage("assistant", "<summary>\nlegacy history\n</summary>", nil, "", "")
	if !isSummaryMarker(legacy) {
		t.Fatal("legacy <summary> marker should remain recognized")
	}

	index := lastSummaryMarkerIndex([]Message{
		NewMessage("system", "system", nil, "", ""),
		legacy,
		NewMessage("user", "next", nil, "", ""),
		typed,
	})
	if index != 3 {
		t.Fatalf("expected newest summary index 3, got %d", index)
	}
}

func TestSummaryGenerationDoesNotNestOldCheckpointAsUserRequest(t *testing.T) {
	old := NewMessage("user", "<summary>\n历史压缩摘要\n- 旧任务\n</summary>", nil, "", "")
	old.InternalType = internalTypeContextSummary
	real := NewMessage("user", "当前真实用户请求", nil, "", "")

	extractive := buildExtractiveSummary([]Message{old, real})
	if strings.Contains(extractive, "<summary>") {
		t.Fatalf("extractive summary nested a checkpoint tag: %q", extractive)
	}
	if !strings.Contains(extractive, "当前真实用户请求") {
		t.Fatalf("extractive summary dropped the real user request: %q", extractive)
	}
	if strings.Contains(extractive, "历史用户请求:\n- 历史压缩摘要") {
		t.Fatalf("old checkpoint was labeled as a user request: %q", extractive)
	}

	msgs := buildSummaryMessages(
		[]Message{old, real},
		[]Message{NewMessage("assistant", "recent work", nil, "", "")},
		nil,
		"summarize",
	)
	if len(msgs) < 3 {
		t.Fatalf("unexpected summary messages: %#v", msgs)
	}
	if !strings.Contains(msgs[0].Content, "不是新的用户请求") {
		t.Fatalf("old checkpoint was not marked as background: %q", msgs[0].Content)
	}
}

func TestExtractiveSummaryCarriesHistoricalUserRequestsForward(t *testing.T) {
	old := NewMessage("user", `<summary>
历史用户请求:
- 原始需求 A
- 用户纠错 B

历史压缩摘要:
- 旧工具结果
</summary>`, nil, "", "")
	old.InternalType = internalTypeContextSummary
	current := NewMessage("user", "当前真实用户请求", nil, "", "")

	extractive := buildExtractiveSummary([]Message{old, current})
	if !strings.Contains(extractive, "历史用户请求:\n- 原始需求 A") {
		t.Fatalf("historical user requests were not carried forward: %q", extractive)
	}
	if !strings.Contains(extractive, "- 当前真实用户请求") {
		t.Fatalf("current user request was dropped: %q", extractive)
	}
	if strings.Contains(extractive, "历史用户请求:\n- <summary>") {
		t.Fatalf("checkpoint wrapper leaked back into user requests: %q", extractive)
	}
}

func TestExtractiveSummaryRecoversIntentFromLegacyNestedCheckpoint(t *testing.T) {
	old := NewMessage("user", `<summary>
历史用户请求:
- <summary>
<key_knowledge>
[Primary Request and Intent]
- 修复旧摘要中的用户请求继承
</key_knowledge>
</summary>
</summary>`, nil, "", "")
	old.InternalType = internalTypeContextSummary

	extractive := buildExtractiveSummary([]Message{old})
	if !strings.Contains(extractive, "修复旧摘要中的用户请求继承") {
		t.Fatalf("legacy nested checkpoint intent was not recovered: %q", extractive)
	}
	if strings.Contains(extractive, "<summary>") {
		t.Fatalf("legacy nested checkpoint leaked protocol tags: %q", extractive)
	}
}

func TestBuildInitialWorkingMessagesKeepsSameSessionHistoryWithoutSummary(t *testing.T) {
	initial := []Message{
		NewMessage("system", "old system", nil, "", ""),
		NewMessage("user", "old request", nil, "", ""),
		NewMessage("assistant", "old answer", nil, "", ""),
		NewMessage("user", "previous request", nil, "", ""),
		NewMessage("assistant", "previous answer", nil, "", ""),
		NewMessage("tool", "previous result", nil, "call-prev", "bash"),
	}
	current := NewMessage("user", "new request", nil, "", "")

	got := buildInitialWorkingMessages(false, "fresh system with keys", initial, current)
	if len(got) != 7 {
		t.Fatalf("main run should keep the full same-session history before compression: %#v", got)
	}
	if got[0].Role != "system" || got[0].Content != "fresh system with keys" {
		t.Fatalf("fresh system prompt was not used: %#v", got[0])
	}
	if got[1].Content != "old request" || got[2].Content != "old answer" || got[3].Content != "previous request" {
		t.Fatalf("same-session history was not preserved raw: %#v", got[1:6])
	}
	if got[6].Role != "user" || got[6].Content != "new request" {
		t.Fatalf("current request was not preserved: %#v", got[6])
	}
}

func TestBuildInitialWorkingMessagesResumesFromLatestSummaryMarker(t *testing.T) {
	oldSummary := NewMessage("assistant", "<summary>old summary</summary>", nil, "", "")
	oldSummary.InternalType = "context_summary"
	newSummary := NewMessage("assistant", "<summary>new summary</summary>", nil, "", "")
	newSummary.InternalType = "context_summary"
	initial := []Message{
		NewMessage("system", "old system", nil, "", ""),
		NewMessage("user", "raw request before compression", nil, "", ""),
		NewMessage("assistant", "raw answer before compression", nil, "", ""),
		oldSummary,
		NewMessage("user", "recovery request one", nil, "", ""),
		NewMessage("assistant", "recovery answer one", nil, "", ""),
		newSummary,
		NewMessage("user", "recovery request two", nil, "", ""),
		NewMessage("assistant", "recovery answer two", nil, "", ""),
	}
	current := NewMessage("user", "new request", nil, "", "")

	got := buildInitialWorkingMessages(false, "fresh system with keys", initial, current)
	if len(got) != 5 {
		t.Fatalf("working context should resume from the latest summary marker: %#v", got)
	}
	if got[1].Content != newSummary.Content || got[2].Content != "recovery request two" || got[3].Content != "recovery answer two" {
		t.Fatalf("latest summary recovery tail was not preserved: %#v", got[1:4])
	}
	for _, message := range got {
		if message.Content == "raw request before compression" || message.Content == oldSummary.Content {
			t.Fatalf("pre-summary raw context leaked after resume: %#v", got)
		}
	}
}

func TestBuildInitialWorkingMessagesKeepsCompactSubtaskHandoff(t *testing.T) {
	summary := NewMessage("assistant", "<summary>subtask handoff</summary>", nil, "", "")
	summary.InternalType = "context_summary"
	current := NewMessage("user", "continue", nil, "", "")

	got := buildInitialWorkingMessages(true, "subtask system", []Message{summary}, current)
	if len(got) != 3 {
		t.Fatalf("unexpected subtask working context: %#v", got)
	}
	if got[1].Content != summary.Content || got[2].Content != current.Content {
		t.Fatalf("compact subtask handoff was not preserved: %#v", got)
	}
}

func TestPreflightForceCompactsToFitBudget(t *testing.T) {
	cfg := &Config{SummaryThresholdTokens: 80}
	messages := []Message{
		NewMessage("system", "system", nil, "", ""),
		NewMessage("user", "do a large task", nil, "", ""),
		NewMessage("assistant", strings.Repeat("a", 500), nil, "", ""),
	}

	result := preflightWorkingContext(messages, cfg, "", nil, nil, true, 1)
	if !result.Summarized || result.SummaryMessage == nil {
		t.Fatalf("expected forced compaction with a summary checkpoint: %#v", result)
	}
	if result.AfterTokens >= result.BeforeTokens {
		t.Fatalf("forced compaction did not reduce context: before=%d after=%d", result.BeforeTokens, result.AfterTokens)
	}
	if len(result.TranscriptTail) == 0 || !isSummaryMarker(result.TranscriptTail[0]) {
		t.Fatalf("forced compaction did not provide a recoverable transcript tail: %#v", result.TranscriptTail)
	}
}

func TestInternalControlMessagesStayOutOfTranscriptRecovery(t *testing.T) {
	summary := NewMessage("assistant", "<summary>compressed</summary>", nil, "", "")
	summary.InternalType = internalTypeContextSummary
	status := newInternalControlMessage("user", deliveredStatusText([]string{"finished work"}), internalTypeDeliveredStatus)
	continuation := newInternalControlMessage("user", "请继续。", internalTypeAutoContinue)

	tail := transcriptRecoveryMessages([]Message{
		NewMessage("user", "real request", nil, "", ""),
		summary,
		status,
		continuation,
		NewMessage("assistant", "visible work", nil, "", ""),
	})
	if len(tail) != 2 {
		t.Fatalf("control turns leaked into transcript recovery: %#v", tail)
	}
	if !isSummaryMarker(tail[0]) || tail[1].Content != "visible work" {
		t.Fatalf("unexpected transcript recovery: %#v", tail)
	}
}

func TestStripSyntheticControlMessagesPreservesNewInteraction(t *testing.T) {
	legacyStatus := NewMessage("user", "[会话状态] 已完成：旧任务", nil, "", "")
	legacyStatus.InteractionID = "req-1"
	legacyContinue := NewMessage("user", "请继续。", nil, "", "")
	legacyContinue.InteractionID = "req-1"
	realNext := NewMessage("user", "请继续。", nil, "", "")
	realNext.InteractionID = "req-2"
	realRequest := NewMessage("user", "real request", nil, "", "")
	realRequest.InteractionID = "req-1"

	got := stripSyntheticControlMessages([]Message{
		realRequest,
		legacyStatus,
		legacyContinue,
		realNext,
	})
	if len(got) != 2 {
		t.Fatalf("unexpected persisted messages: %#v", got)
	}
	if got[0].Content != "real request" || got[1].InteractionID != "req-2" || got[1].Content != "请继续。" {
		t.Fatalf("real user turns were not preserved: %#v", got)
	}
}

func TestSanitizeUserInputProtocolRemovesCheckpointBlocks(t *testing.T) {
	got := sanitizeUserInputProtocol("请继续分析\n<summary>\n历史上下文\n</summary>\n")
	if got != "请继续分析" {
		t.Fatalf("summary block survived user-input sanitization: %q", got)
	}
	if strings.TrimSpace(sanitizeUserInputProtocol("<summary>only context</summary>")) != "" {
		t.Fatal("summary-only user input should collapse to empty")
	}
}
