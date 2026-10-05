package main

import (
	"strings"
	"testing"
)

// The compressor only ever saw the compressed middle of the conversation. The
// newest request is kept in the tail, so the handover document was written from
// history alone - and described the longest-running old task as the primary
// intent, in the present tense, which is how the agent came back from a
// compression talking about the video work the user had already left behind.
func TestTheSummaryIsGivenTheNewestRequest(t *testing.T) {
	older := NewMessage("user", "把视频抽帧这条路跑通，然后并回视频摘要", nil, "", "")
	assistant := NewMessage("assistant", "抽帧已独立成脚本", nil, "", "")
	newest := NewMessage("user", "别再管之前那个了：现在三个待办事项 1绝对路径 2summary机制 3域名", nil, "", "")
	internal := newInternalControlMessage("user", "请继续。", internalTypeAutoContinue)
	summary := NewMessage("user", "<summary>\n历史摘要\n</summary>", nil, "", "")
	summary.InternalType = internalTypeContextSummary

	context := []Message{older, assistant, internal, summary, newest}
	// The tail that stays in the working context is also input: a handover
	// written from the stale middle alone describes the wrong "current work".
	messages := buildSummaryMessages(context, []Message{older, assistant}, []Message{assistant, newest}, "压缩说明")

	last := messages[len(messages)-1]
	if !strings.Contains(last.Content, "压缩说明") {
		t.Fatal("the compression prompt must stay last")
	}
	body := ""
	for _, message := range messages[:len(messages)-1] {
		body += message.Content + "\n"
	}
	if !strings.Contains(body, "3域名") {
		t.Fatal("the newest request has to reach the compressor")
	}
	if !strings.Contains(body, "已被取代") {
		t.Fatal("and it has to say that conflicting historical goals are superseded")
	}
	if !strings.Contains(body, "最近对话") {
		t.Fatal("the retained tail has to reach the compressor too")
	}

	if got := latestUserRequest(context); !strings.Contains(got, "3域名") {
		t.Fatalf("latestUserRequest returned %q", got)
	}
	// Internal control turns and summaries are never mistaken for a request.
	if got := latestUserRequest([]Message{older, internal, summary}); !strings.Contains(got, "抽帧") {
		t.Fatalf("latestUserRequest must skip internal turns, got %q", got)
	}
}

// The extractive fallback writes the summary without a model; it has to lead
// with the same thing the model-written one does.
func TestTheExtractiveSummaryLeadsWithTheCurrentRequest(t *testing.T) {
	older := NewMessage("user", "帮我做视频摘要", nil, "", "")
	newest := NewMessage("user", "现在先帮我改一下绝对路径", nil, "", "")

	rendered := withCurrentRequest(buildCompactExtractiveSummary([]Message{older}), []Message{older, newest})
	if !strings.HasPrefix(rendered, "[当前请求]") {
		t.Fatalf("the current request must lead the summary, got %q", rendered)
	}
	if !strings.Contains(rendered, "绝对路径") {
		t.Fatalf("the newest request is missing from %q", rendered)
	}
}

// The mechanical handover has to wear the same labels as the model-written one,
// and only claim what it can actually see.
func TestTheMechanicalHandoverUsesTheFixedLabels(t *testing.T) {
	user1 := NewMessage("user", "帮我做视频摘要", nil, "", "")
	assistant := NewMessage("assistant", "抽帧已独立成脚本并跑通", nil, "", "")
	tool := NewMessage("tool", "/home/user/Fairy/skills/video-summary/scripts/extract_frames.py 已创建", nil, "", "")
	tool.Name = "write_file"
	user2 := NewMessage("user", "顺便把域名指一下", nil, "", "")
	context := []Message{user1, assistant, tool, user2}

	rendered := withCurrentRequest(buildCompactExtractiveSummary(context), context)
	for _, label := range []string{"[当前请求]", "[已完成]", "[关键文件]", "[决策与约束]", "[历史要点]"} {
		if !strings.Contains(rendered, label) {
			t.Fatalf("mechanical handover is missing %s:\n%s", label, rendered)
		}
	}
	if !strings.Contains(rendered, "extract_frames.py") {
		t.Fatalf("tool results are where the file facts come from:\n%s", rendered)
	}
	if !strings.Contains(rendered, "抽帧已独立成脚本") {
		t.Fatalf("assistant conclusions are where [已完成] comes from:\n%s", rendered)
	}
}

// A compaction that quietly degrades into a stub is how a conversation loses its
// history without anyone noticing, so the reason has to travel back with the
// result. The config here has no usable model, which is the case that must be
// reported rather than hidden (an oversized region is no longer a reason to
// degrade at all - it is split and summarised in chunks).
func TestCompactionReportsWhyItDegraded(t *testing.T) {
	cfg := &Config{RepoRoot: t.TempDir(), SummaryThresholdTokens: 200}
	big := strings.Repeat("这是一段足够长的历史材料，用来把上下文撑过阈值。", 30) // ≈ 300 tokens per message
	messages := make([]Message, 0, 12)
	for i := 0; i < 12; i++ {
		role := "assistant"
		if i%2 == 0 {
			role = "user"
		}
		messages = append(messages, NewMessage(role, big, nil, "", ""))
	}

	result := preflightWorkingContext(messages, cfg, "把历史压缩成事实交接", nil, nil, false, 1)
	if !result.Summarized {
		t.Fatal("a context past the threshold has to be compacted")
	}
	if !result.Degraded {
		t.Fatalf("expected a degraded compaction, got reason=%q", result.DegradeReason)
	}
	if !strings.Contains(result.DegradeReason, "摘要模型") {
		t.Fatalf("the reason should name the summarizer that failed, got %q", result.DegradeReason)
	}
}

// The mechanical handover used to paste old conclusions whole. A model reading
// that recites the old work and then continues it, which is what the user saw:
// "it repeated what we did before and then went back to it".
func TestTheMechanicalHandoverDoesNotReciteOldWork(t *testing.T) {
	oldConclusion := strings.Repeat("这是很久以前的结论，包含一整段解释和一段代码块，早就做完了。", 40) // ≈ 1100 chars
	assistant := NewMessage("assistant", oldConclusion, nil, "", "")
	user := NewMessage("user", "test", nil, "", "")
	context := []Message{assistant, user}

	rendered := withCurrentRequest(buildCompactExtractiveSummary(context), context)
	if len(rendered) > 700 {
		t.Fatalf("a mechanical handover has to stay short, got %d chars:\n%s", len(rendered), rendered)
	}
	if !strings.Contains(rendered, "不要接着做") {
		t.Fatalf("old conclusions must be marked as finished:\n%s", rendered)
	}
	if !strings.Contains(rendered, "[当前请求] test") {
		t.Fatalf("the current request must lead the handover:\n%s", rendered)
	}
}


// "Too big for one call" has to mean "summarise it in chunks", not "write a stub".
func TestAnOversizedRegionIsSplitNotStubbed(t *testing.T) {
	budget := 400
	messages := make([]Message, 0, 20)
	for i := 0; i < 20; i += 1 {
		messages = append(messages, NewMessage("assistant", strings.Repeat("历史材料。", 40), nil, "", ""))
	}
	chunks := splitMessagesWithinBudget(messages, budget)
	if len(chunks) < 2 {
		t.Fatalf("expected the region to be split, got %d chunk(s)", len(chunks))
	}
	flat := make([]string, 0, len(messages))
	for _, chunk := range chunks {
		for _, message := range chunk {
			flat = append(flat, message.Content)
		}
	}
	if len(flat) != len(messages) {
		t.Fatalf("chunks lost messages: %d of %d", len(flat), len(messages))
	}
	for i, message := range messages {
		if flat[i] != message.Content {
			t.Fatalf("chunk order changed at %d", i)
		}
	}
	for _, chunk := range chunks {
		if len(chunk) > 1 && estimateMessagesContextTokens(chunk) > budget*3 {
			t.Fatalf("chunk is far over budget: %d tokens", estimateMessagesContextTokens(chunk))
		}
	}

	tail := tailMessagesWithinBudget(messages, budget)
	if len(tail) == 0 || len(tail) >= len(messages) {
		t.Fatalf("tail should be a proper suffix, got %d of %d", len(tail), len(messages))
	}
	if tail[len(tail)-1].Content != messages[len(messages)-1].Content {
		t.Fatal("the tail must keep the newest message")
	}
}

// The trigger has to be the smaller of the model's real window and the policy
// number: a fixed threshold either compacts too often (big window) or lets a
// call run past the provider limit (small window).
func TestContextBudgetIsTheSmallerOfWindowAndPolicy(t *testing.T) {
	// No window declared: the policy number is all we have.
	plain := &Config{SummaryThresholdTokens: 200000, API: APIConfig{MaxTokens: 16384}}
	if got := effectiveContextBudget(plain); got != 200000 {
		t.Fatalf("without a declared window the threshold stands, got %d", got)
	}

	// A window smaller than the policy: window minus the answer reserve wins.
	small := &Config{SummaryThresholdTokens: 200000, API: APIConfig{MaxTokens: 16384, ContextWindow: 128000}}
	// The window is first reduced by the safety ratio (never plan to fill it),
	// then the answer reserve is taken out.
	want := int(128000*contextWindowSafetyRatio) - contextOutputReserve(small)
	if got := effectiveContextBudget(small); got != want {
		t.Fatalf("small window: got %d want %d", got, want)
	}
	if want >= 200000 {
		t.Fatal("this case is only meaningful when the window is the smaller bound")
	}

	// A window much larger than the policy: the policy still caps the cost.
	big := &Config{SummaryThresholdTokens: 200000, API: APIConfig{MaxTokens: 16384, ContextWindow: 1000000}}
	if got := effectiveContextBudget(big); got != 200000 {
		t.Fatalf("big window: the policy number should still cap it, got %d", got)
	}

	// Misconfigured (window smaller than the answer): fall back to half the window.
	broken := &Config{SummaryThresholdTokens: 200000, API: APIConfig{MaxTokens: 200000, ContextWindow: 100000}}
	if got := effectiveContextBudget(broken); got != int(100000*contextWindowSafetyRatio)/2 {
		t.Fatalf("misconfigured window: got %d want %d", got, int(100000*contextWindowSafetyRatio)/2)
	}
}

// summary_retain_tokens used to be read by nobody. It is the knob that decides
// how much headroom a compaction buys.
func TestRetainTokensBoundsWhatSurvivesCompression(t *testing.T) {
	messages := make([]Message, 0, 40)
	for i := 0; i < 40; i += 1 {
		role := "assistant"
		if i%2 == 0 {
			role = "user"
		}
		messages = append(messages, NewMessage(role, strings.Repeat("材料。", 60), nil, "", ""))
	}

	loose, ok := selectCompressionPlan(messages, 6000, 0)
	if !ok {
		t.Fatal("expected a plan with the ratio defaults")
	}
	tight, ok := selectCompressionPlan(messages, 6000, 1000)
	if !ok {
		t.Fatal("expected a plan with a retain cap")
	}
	if tight.retainedToken >= loose.retainedToken {
		t.Fatalf("a retain cap must keep less: %d vs %d", tight.retainedToken, loose.retainedToken)
	}
}

// The estimate used to count one token per rune, which is ~4x too high for the
// English, code, logs and JSON that dominate a working session - the reason a
// context whose real prompt was 81k tokens looked like 478k and got compacted.
func TestTokenEstimateIsCalibratedForAsciiAndCjk(t *testing.T) {
	cases := []struct {
		name string
		text string
		want int
	}{
		{"ascii", strings.Repeat("a", 400), 100},
		{"cjk", strings.Repeat("中", 100), 100},
		{"mixed", strings.Repeat("a", 200) + strings.Repeat("中", 50), 100},
	}
	for _, tc := range cases {
		got := estimateTextTokens(tc.text)
		if got < tc.want*8/10 || got > tc.want*13/10 {
			t.Fatalf("%s: estimate %d, want ~%d", tc.name, got, tc.want)
		}
	}
	// Code and logs are ASCII, so a message that used to look 4x bigger now
	// looks right.
	code := strings.Repeat("func main() { println(\"hi\") }\n", 20)
	if got := estimateTextTokens(code); got > len(code)/2 {
		t.Fatalf("ascii code still over-estimated: %d for %d chars", got, len(code))
	}
}

func TestContextScaleFactorIsClampedAndApplied(t *testing.T) {
	if got := contextScaleFactor(100, 100); got != 1 {
		t.Fatalf("equal counts → 1, got %v", got)
	}
	if got := contextScaleFactor(600, 100); got != 2 {
		t.Fatalf("wildly low estimate is clamped to 2, got %v", got)
	}
	if got := contextScaleFactor(10, 100); got != 0.2 {
		t.Fatalf("wildly high estimate is clamped to 0.2, got %v", got)
	}
	if got := contextScaleFactor(0, 100); got != 0 {
		t.Fatalf("no provider count means no anchor, got %v", got)
	}
	if got := calibratedTokens(1000, 0.5); got != 500 {
		t.Fatalf("calibratedTokens: got %d", got)
	}
	if got := calibratedTokens(1000, 0); got != 1000 {
		t.Fatalf("without an anchor the raw estimate stands, got %d", got)
	}
}
