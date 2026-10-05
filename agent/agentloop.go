package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"agentloop/agent/internal/biz/tool/httptool"
)

func estimateMessageTokens(m Message) int {
	n := len(m.Content)/3 + 4
	if len(m.ToolCalls) > 0 {
		for _, tc := range m.ToolCalls {
			n += len(tc.Function.Arguments)/3 + 8
		}
	}
	return n
}

func isSummaryMarker(m Message) bool {
	return m.InternalType == internalTypeContextSummary ||
		strings.HasPrefix(strings.TrimSpace(m.Content), "<summary>")
}

var summaryTagPattern = regexp.MustCompile(`(?is)</?summary[^>]*>`)
var summaryBlockPattern = regexp.MustCompile(`(?is)<summary\b[^>]*>.*?</summary\s*>`)
var summaryPrimaryIntentHeadingPattern = regexp.MustCompile(`(?i)^\s*\[(?:primary request and intent|主要请求与意图|用户请求与意图)\]\s*$`)
// The heading the mechanical fallback writes for past requests. [历史要点] is the
// label the fixed summary structure mandates; the older prose heading is still
// matched so checkpoints written before the change keep carrying their requests
// forward instead of dropping them at the next compaction.
var summaryHistoryUserHeadingPattern = regexp.MustCompile(`(?i)^\s*(?:\[历史要点\]|历史用户请求|history(?:al)? user requests?|user requests?)\s*[:：]?\s*$`)

// normalizeSummaryText removes the outer protocol wrapper from a summary. The
// summarizer prompt asks for <summary>, and previous checkpoints can themselves
// contain that wrapper; stripping it here prevents an ever-growing nested
// <summary><summary> chain in persisted context.
func normalizeSummaryText(value string) string {
	return strings.TrimSpace(summaryTagPattern.ReplaceAllString(value, ""))
}

// sanitizeUserInputProtocol removes context-checkpoint blocks that accidentally
// arrive through the user-input path. Internal checkpoints themselves use a
// typed marker and are deliberately not passed through this function.
func sanitizeUserInputProtocol(value string) string {
	value = summaryBlockPattern.ReplaceAllString(value, "")
	return strings.TrimSpace(summaryTagPattern.ReplaceAllString(value, ""))
}

func isSummarySectionHeading(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	if summaryPrimaryIntentHeadingPattern.MatchString(trimmed) || summaryHistoryUserHeadingPattern.MatchString(trimmed) {
		return true
	}
	if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
		return true
	}
	lower := strings.ToLower(trimmed)
	for _, prefix := range []string{
		"历史压缩摘要",
		"历史助手结论",
		"最近工具结果",
		"historical summary",
		"assistant conclusions",
		"recent tool results",
	} {
		if strings.HasPrefix(lower, strings.ToLower(prefix)) {
			return true
		}
	}
	return false
}

func summaryPrimaryIntent(value string) string {
	lines := strings.Split(normalizeSummaryText(value), "\n")
	for index, line := range lines {
		if !summaryPrimaryIntentHeadingPattern.MatchString(strings.TrimSpace(line)) {
			continue
		}
		body := make([]string, 0, 4)
		for next := index + 1; next < len(lines); next++ {
			trimmed := strings.TrimSpace(lines[next])
			if trimmed == "" {
				if len(body) > 0 {
					break
				}
				continue
			}
			if isSummarySectionHeading(trimmed) {
				break
			}
			body = append(body, trimmed)
		}
		if len(body) > 0 {
			return compactContextText(strings.Join(body, "\n"), 800)
		}
	}
	return ""
}

// summaryHistoricalUserRequests carries user intent across repeated
// compression. The full checkpoint stays historical background, but the real
// user-request section must survive as user intent instead of being nested as
// another <summary> checkpoints.
func summaryHistoricalUserRequests(value string) []string {
	lines := strings.Split(normalizeSummaryText(value), "\n")
	out := make([]string, 0, 3)
	inUserRequests := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if summaryHistoryUserHeadingPattern.MatchString(trimmed) {
			inUserRequests = true
			continue
		}
		if !inUserRequests {
			continue
		}
		if isSummarySectionHeading(trimmed) {
			break
		}
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "• ") {
			entry := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(trimmed, "- "), "• "))
			if entry != "" && len(out) < 3 {
				out = append(out, compactContextText(entry, 800))
			}
			continue
		}
		if trimmed == "" {
			if len(out) > 0 {
				break
			}
			continue
		}
		if len(out) > 0 && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) {
			out[len(out)-1] += " " + trimmed
		}
	}
	if len(out) == 0 {
		if intent := summaryPrimaryIntent(value); intent != "" {
			out = append(out, "历史摘要中的用户目标：\n"+intent)
		}
	}
	return out
}

const (
	internalTypeContextSummary   = "context_summary"
	internalTypeAutoContinue     = "auto_continue"
	internalTypeDeliveredStatus  = "delivered_status"
	internalTypeAutoAnswer       = "auto_answer"
	internalTypeExecutionGuard   = "execution_guard"
	internalTypePlanContinuation = "plan_continuation"
	internalTypeVisionContext    = "vision_context"
	internalTypeUserInterrupt    = "user_interrupt"

	// userInterruptRuleText is attached to the working context the moment a
	// message arrives from the middle of a running turn. The message itself is
	// already in the transcript; this says what to do about it, because the
	// plan-continuation instructions would otherwise be the louder voice.
	userInterruptRuleText = "主人刚在你执行过程中插话。规则（最高优先级）：下一个动作就是回应这条消息，不管手上的链路走到哪一步。" +
		"正在做的事收成一个暂停点——未完成的计划项保持未完成，不要标记完成，也不要继续推进；不要把插话当成原计划的补充材料。" +
		"回答完这条消息就停，不要擅自续跑原计划；只有主人明确说继续时才从未完成的计划项接着做。"
)

// isInternalControlMessage identifies machine-generated turns that steer the
// agent loop. They must remain in workingMsgs when a provider requires a
// trailing user message, but they are never user input and must not reach the
// persisted transcript or the chat UI.
func isInternalControlMessage(m Message) bool {
	switch m.InternalType {
	case internalTypeAutoContinue, internalTypeDeliveredStatus, internalTypeAutoAnswer, internalTypeExecutionGuard, internalTypePlanContinuation, internalTypeVisionContext, internalTypeUserInterrupt:
		return true
	}
	return strings.HasPrefix(strings.TrimSpace(m.Content), "[会话状态]")
}

const (
	maxIdenticalReadsPerRange      = 2
	maxReadOnlyStepsBeforeProgress = 3
	maxExecutionGuardsPerRun       = 2
)

type readFileAttemptKey struct {
	Path     string
	Offset   int
	Limit    int
	MaxBytes int
}

func parseReadFileAttemptKey(arguments string) (readFileAttemptKey, bool) {
	var args map[string]any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return readFileAttemptKey{}, false
	}
	path := strings.TrimSpace(fmt.Sprint(args["file_path"]))
	if path == "" || path == "<nil>" {
		path = strings.TrimSpace(fmt.Sprint(args["path"]))
	}
	if path == "" || path == "<nil>" {
		return readFileAttemptKey{}, false
	}
	jsonInt := func(key string) int {
		switch value := args[key].(type) {
		case float64:
			return int(value)
		case int:
			return value
		default:
			return 0
		}
	}
	return readFileAttemptKey{
		Path:     path,
		Offset:   jsonInt("offset"),
		Limit:    jsonInt("limit"),
		MaxBytes: jsonInt("max_bytes"),
	}, true
}

func shouldBlockRepeatedRead(count int) bool {
	return count >= maxIdenticalReadsPerRange
}

func isReadOnlyContextTool(name string) bool {
	switch name {
	case "read_file", "grep", "glob":
		return true
	default:
		return false
	}
}

func isExecutionProgressTool(name string) bool {
	switch name {
	case "plan", "edit_file", "write_file", "bash", "bash_job", "create_subtask", "show_result", "ask_user":
		return true
	default:
		return false
	}
}

func likelyImplementationTask(prompt string) bool {
	text := strings.ToLower(prompt)
	for _, keyword := range []string{
		"改一下", "修改", "实现", "修复", "重构", "添加", "新增", "接入", "优化", "升级",
		"调整", "替换", "删除", "编写", "写一个", "做一个", "更新代码", "开始做",
		"implement", "fix", "refactor", "add", "update", "modify", "write", "build",
	} {
		if strings.Contains(text, keyword) {
			return true
		}
	}
	return false
}

func executionGuardText(readOnlySteps int) string {
	return fmt.Sprintf(
		"运行时收口约束：已连续 %d 步只读或检索，尚未产生实际修改。现在停止再次读取相同范围。先用一句内部计划写清：①已确认的接口、约束和关键结论；②下一步要修改的文件与函数；③完成后运行什么验证。若 plan 可用，立即用 plan 更新进度；随后直接调用 edit_file 或 write_file。只有缺口明确、且尚未读取过对应范围时，才允许读取新位置。",
		readOnlySteps,
	)
}

func newInternalControlMessage(role, content, internalType string) Message {
	message := NewMessage(role, content, nil, "", "")
	message.InternalType = internalType
	return message
}

// lastSummaryMarkerIndex returns the index of the newest <summary> checkpoint,
// or -1 when the transcript has never been compressed. Everything at or before
// that index is archived display history: the AI working view skips it, so a
// later checkpoint must never pull it back into a compression window.
func lastSummaryMarkerIndex(msgs []Message) int {
	idx := -1
	for i, m := range msgs {
		if isSummaryMarker(m) {
			idx = i
		}
	}
	return idx
}

// buildSummaryMessages gives the summarizer the immutable initial task as
// context. The running agent keeps that task outside the compressed window,
// but the summarizer must still see the original request so it cannot conclude
// that no user request existed.
// maxSummaryDepth bounds the recursion of chunked compaction. Three levels turn
// a two-million-token history into eight summaries; deeper than that is a
// history nobody is going to read anyway.
const maxSummaryDepth = 3

// summarizeRegionWithModel writes the handover for a region of history with the
// model, in chunks when it does not fit one call.
//
// "Too big" must not mean "give up and write a mechanical stub". The mechanical
// handover loses detail and recites old work, and reaching for it whenever the
// history was long is what made a long conversation come back as a recap. So an
// oversized region is split, each chunk is summarised, and those partial
// summaries are summarised again. Only a genuine failure (model unreachable,
// retried once) returns an error, and only an error is allowed to degrade the
// compaction.
func summarizeRegionWithModel(cfg *Config, context, region, recent []Message, prompt string, budget, depth int) (string, error) {
	if budget <= 0 {
		budget = 60000
	}
	total := estimateMessagesContextTokens(region)
	if total > budget && depth < maxSummaryDepth {
		chunks := splitMessagesWithinBudget(region, budget)
		if len(chunks) > 1 {
			partials := make([]string, 0, len(chunks))
			for _, chunk := range chunks {
				part, err := summarizeRegionWithModel(cfg, context, chunk, nil, prompt, budget, depth+1)
				if err != nil {
					return "", err
				}
				if strings.TrimSpace(part) != "" {
					partials = append(partials, part)
				}
			}
			if len(partials) == 1 {
				return partials[0], nil
			}
			if len(partials) > 1 {
				merged := newInternalControlMessage("user",
					"下面是同一段历史的分段摘要，请按同样的栏目合并成一份：去重、保留事实、不要复述。\n\n"+
						strings.Join(partials, "\n\n"), internalTypeContextSummary)
				return summarizeRegionWithModel(cfg, context, []Message{merged}, nil, prompt, budget, depth+1)
			}
		}
	}
	if total > budget {
		// At the depth cap - or with a single message bigger than the budget -
		// keep the newest part that fits and say what was dropped, which is an
		// honest truncation rather than a stub.
		trimmed := tailMessagesWithinBudget(region, budget)
		note := newInternalControlMessage("user",
			"[这段历史超出摘要预算，只保留了最近部分；更早的内容没有进入摘要]", internalTypeContextSummary)
		region = append([]Message{note}, trimmed...)
	}
	summaryMsgs := buildSummaryMessages(context, region, recent, prompt)
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		summaryResp, err := CallConfiguredLLM(cfg, summaryMsgs, nil)
		if err == nil && summaryResp != nil {
			if text := normalizeSummaryText(summaryResp.Content); text != "" {
				return text, nil
			}
			lastErr = nil
			continue
		}
		lastErr = err
	}
	return "", lastErr
}

// splitMessagesWithinBudget cuts a region into consecutive groups that each fit
// the budget. Order is what makes a handover legible, so groups are only ever
// consecutive - never re-sorted or merged across the seam.
func splitMessagesWithinBudget(messages []Message, budget int) [][]Message {
	if budget <= 0 || len(messages) == 0 {
		return nil
	}
	chunks := make([][]Message, 0, 4)
	current := make([]Message, 0, 8)
	used := 0
	for _, message := range messages {
		cost := estimateMessageTokens(message) + 4
		if len(current) > 0 && used+cost > budget {
			chunks = append(chunks, current)
			current = make([]Message, 0, 8)
			used = 0
		}
		current = append(current, message)
		used += cost
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}

// tailMessagesWithinBudget keeps the newest messages that fit.
func tailMessagesWithinBudget(messages []Message, budget int) []Message {
	if budget <= 0 || len(messages) == 0 {
		return messages
	}
	out := make([]Message, 0, len(messages))
	used := 0
	for index := len(messages) - 1; index >= 0; index-- {
		cost := estimateMessageTokens(messages[index]) + 4
		if len(out) > 0 && used+cost > budget {
			break
		}
		out = append(out, messages[index])
		used += cost
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func buildSummaryMessages(messages, compressed, recent []Message, summaryPrompt string) []Message {
	summaryMsgs := make([]Message, 0, len(compressed)+2)
	for _, message := range messages {
		if message.Role == "user" && strings.TrimSpace(message.Content) != "" {
			content := strings.TrimSpace(message.Content)
			if isSummaryMarker(message) {
				content = "[历史压缩摘要，不是新的用户请求]\n" + normalizeSummaryText(content)
			}
			summaryMsgs = append(summaryMsgs, NewMessage("user", content, nil, "", ""))
			break
		}
	}
	for _, message := range compressed {
		if isSummaryMarker(message) {
			content := normalizeSummaryText(message.Content)
			if content == "" {
				continue
			}
			summaryMsgs = append(summaryMsgs, NewMessage("user", "[历史压缩摘要，不是新的用户请求]\n"+content, nil, "", ""))
			continue
		}
		summaryMsgs = append(summaryMsgs, message)
	}
	// The rows that stay in the working context are input to the compressor as
	// well, not just background it may ignore. A handover written from the stale
	// middle alone described work the user had already left behind as the current
	// task; seeing the last few rounds is what makes "what is happening now" an
	// observation rather than a guess. The caller drops these when they would not
	// fit, in which case the labelled current request below still carries the
	// priority. The user messages among them are the same ones the loop keeps
	// above the summary, so the roles and pairing stay familiar.
	if len(recent) > 0 {
		summaryMsgs = append(summaryMsgs, newInternalControlMessage("user",
			"[最近对话（这些消息会原样保留在上下文里；只用它们判断当前进度，不要逐条复述）]",
			internalTypeContextSummary))
		summaryMsgs = append(summaryMsgs, recent...)
	}
	// The newest request sits in the retained tail, outside `compressed`, so the
	// compressor would never see it - and would go on describing the oldest and
	// longest-running work as the primary intent, in the present tense. That is
	// how "the user changed the subject" comes back as "keep going with the old
	// task": the handover document says the old job is what is being done.
	if latest := latestUserRequest(messages); latest != "" {
		summaryMsgs = append(summaryMsgs, NewMessage("user",
			"[当前用户请求（最新一条，尚未处理）]\n"+latest+
				"\n\n注意：历史请求里与它冲突的目标都已被取代。摘要必须保留这条当前请求，并且不得把更早的任务写成「当前正在完成」。",
			nil, "", ""))
	}
	summaryMsgs = append(summaryMsgs, NewMessage("user", summaryPrompt, nil, "", ""))
	return summaryMsgs
}

// latestUserRequest returns the newest real user message, skipping the internal
// control turns the loop writes for itself (summaries, continuations, runtime
// status notes). Those are not requests and must never be mistaken for one.
func latestUserRequest(messages []Message) string {
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role != "user" {
			continue
		}
		if isInternalControlMessage(message) || isSummaryMarker(message) {
			continue
		}
		if text := strings.TrimSpace(message.Content); text != "" {
			return text
		}
	}
	return ""
}

// currentRequestLine leads a handover summary with what the user is asking now.
func currentRequestLine(messages []Message) string {
	latest := latestUserRequest(messages)
	if latest == "" {
		return ""
	}
	// Same label the summary prompt mandates, so the mechanical fallback and the
	// model-written handover are read the same way by the next turn.
	return "[当前请求] " + compactContextText(latest, 600)
}

func withCurrentRequest(summary string, messages []Message) string {
	line := currentRequestLine(messages)
	if line == "" {
		return summary
	}
	if strings.TrimSpace(summary) == "" {
		return line
	}
	return line + "\n\n" + summary
}

const (
	recentRoundsBudgetRatio = 0.4
	recentUserBudgetRatio   = 0.2
)

type contextRound struct {
	start  int
	end    int
	tokens int
}

type compressionPlan struct {
	compressStart int
	compressEnd   int
	retainedUsers []Message
	retainedTail  []Message
	retainedToken int
}

type contextPreflightResult struct {
	Messages       []Message
	SummaryMessage *Message
	TranscriptTail []Message
	Summarized     bool
	BeforeTokens   int
	AfterTokens    int
	// Degraded says the fallback wrote the handover instead of the model, and
	// DegradeReason says why. Compaction that quietly turns into a stub is how a
	// conversation loses its history without anyone noticing until the agent
	// behaves as if nothing had happened before.
	Degraded      bool
	DegradeReason string
}

func contextPreflightPreview(result contextPreflightResult) string {
	preview := fmt.Sprintf("上下文 %d → %d tokens", result.BeforeTokens, result.AfterTokens)
	if result.Summarized {
		preview += " · 已摘要压缩"
	}
	return preview
}

// estimateContextTokens is deliberately conservative for mixed Chinese/English
// tool output. Gateway-reported prompt usage triggers compression; this local
// estimate only chooses a safe model-visible tail.
func estimateContextTokens(message Message) int {
	tokens := estimateTextTokens(message.Content)
	for _, call := range message.ToolCalls {
		tokens += estimateTextTokens(call.Function.Name)
		tokens += estimateTextTokens(call.Function.Arguments)
	}
	if message.Role == "tool" {
		tokens += estimateTextTokens(message.Name)
		tokens += estimateTextTokens(message.ToolCallID)
	}
	for _, part := range message.ContentParts {
		if part.Type == "image_url" && part.ImageURL != nil && part.ImageURL.URL != "" {
			tokens += estimatedImageContextTokens
		}
	}
	return tokens
}

// estimateTextTokens counts characters the way tokenizers actually split them.
//
// The previous version counted one token per rune, which is right for Chinese
// and about four times too high for the English, code, logs and JSON that make up
// most of a working session. That single line is why a context whose real prompt
// was 81k tokens looked like 478k to the harness: it compacted conversations
// that were nowhere near the threshold, while the trace panel - which shows the
// provider's own count - said nothing was wrong.
//
// ASCII runs about four characters per token; a non-ASCII rune (Chinese, and the
// full-width punctuation that comes with it) is about one token per character.
func estimateTextTokens(text string) int {
	if text == "" {
		return 0
	}
	runes := 0
	wide := 0
	for _, r := range text {
		runes++
		if r > 0x7F {
			wide++
		}
	}
	ascii := runes - wide
	if ascii < 0 {
		ascii = 0
	}
	return ascii/4 + wide
}

func collectContextRounds(messages []Message, start int) []contextRound {
	rounds := make([]contextRound, 0)
	for index := start; index < len(messages); {
		if messages[index].Role != "assistant" {
			index++
			continue
		}
		end := index
		tokens := estimateContextTokens(messages[index])
		for end+1 < len(messages) && messages[end+1].Role == "tool" {
			end++
			tokens += estimateContextTokens(messages[end])
		}
		rounds = append(rounds, contextRound{start: index, end: end, tokens: tokens})
		index = end + 1
	}
	return rounds
}

// selectRecentUserMessages keeps recent user intent in front of the generated
// summary. The first selected message is retained even if it alone exceeds the
// budget, so a fresh confirmation is never lost.
func selectRecentUserMessages(messages []Message, budget int) []Message {
	var retained []Message
	used := 0
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role != "user" || isInternalControlMessage(message) || isSummaryMarker(message) {
			continue
		}
		tokens := estimateContextTokens(message)
		if len(retained) > 0 && used+tokens > budget {
			break
		}
		retained = append(retained, message)
		used += tokens
	}
	for left, right := 0, len(retained)-1; left < right; left, right = left+1, right-1 {
		retained[left], retained[right] = retained[right], retained[left]
	}
	return retained
}

// selectCompressionPlan mirrors the bounded context selector: retain sticky
// system messages and recent user intent, summarize the middle, then keep a
// bounded tail made from complete assistant/tool rounds.
func selectCompressionPlan(messages []Message, threshold, retainTokens int) (compressionPlan, bool) {
	if threshold <= 0 || len(messages) < 5 {
		return compressionPlan{}, false
	}

	firstUser := -1
	for index, message := range messages {
		if message.Role == "user" {
			firstUser = index
			break
		}
	}
	if firstUser < 0 || firstUser >= len(messages)-1 {
		return compressionPlan{}, false
	}

	// How much of the recent conversation stays verbatim. Two thirds of it as
	// complete rounds, one third as user intent; when the operator has set
	// summary_retain_tokens that number wins, because it is the knob that decides
	// how much headroom a compaction buys - retaining 60k against a 200k trigger
	// is what made the agent compact twice in six minutes.
	roundsBudget := int(float64(threshold) * recentRoundsBudgetRatio)
	usersBudget := int(float64(threshold) * recentUserBudgetRatio)
	if retainTokens > 0 {
		// A ceiling, not a replacement: summary_retain_tokens only ever reduces
		// what stays verbatim, so setting it can never make a compaction keep
		// more than the ratios already allow.
		if cap := retainTokens * 2 / 3; cap < roundsBudget {
			roundsBudget = cap
		}
		if cap := retainTokens / 3; cap < usersBudget {
			usersBudget = cap
		}
	}
	budget := roundsBudget
	retainedUsers := selectRecentUserMessages(messages, usersBudget)
	rounds := collectContextRounds(messages, firstUser+1)
	if len(rounds) == 0 {
		return compressionPlan{}, false
	}

	tailStart := len(messages)
	retainedTokens := 0
	retainedRounds := 0
	for index := len(rounds) - 1; index >= 0; index-- {
		round := rounds[index]
		if retainedRounds > 0 && retainedTokens+round.tokens > budget {
			break
		}
		tailStart = round.start
		retainedTokens += round.tokens
		retainedRounds++
	}

	compressStart := firstUser + 1
	compressEnd := tailStart - 1
	if compressEnd-compressStart < 4 {
		return compressionPlan{}, false
	}
	retainedTail := make([]Message, 0, len(messages)-tailStart)
	for _, message := range messages[tailStart:] {
		if message.Role == "user" || message.Role == "system" {
			continue
		}
		retainedTail = append(retainedTail, message)
	}

	return compressionPlan{
		compressStart: compressStart,
		compressEnd:   compressEnd,
		retainedUsers: retainedUsers,
		retainedTail:  retainedTail,
		retainedToken: retainedTokens,
	}, true
}

func estimateMessagesContextTokens(messages []Message) int {
	total := 0
	for _, message := range messages {
		total += estimateContextTokens(message)
	}
	return total
}

// buildInitialWorkingMessages recreates the model-visible history for a new
// interaction. Same-session history stays intact until summary compression;
// after compression, the newest summary marker and its recovery tail are the
// model-visible boundary so old raw tool output is not reintroduced.
func buildInitialWorkingMessages(isSubtaskRun bool, systemPrompt string, initialMessages []Message, userMsg Message) []Message {
	working := []Message{NewMessage("system", systemPrompt, nil, "", "")}
	if isSubtaskRun {
		for _, message := range initialMessages {
			if message.Role != "system" {
				working = append(working, message)
			}
		}
	} else {
		start := 0
		for index, message := range initialMessages {
			if isSummaryMarker(message) {
				start = index
			}
		}
		for _, message := range initialMessages[start:] {
			if message.Role != "system" {
				working = append(working, message)
			}
		}
	}
	working = append(working, userMsg)
	return working
}

func insertDynamicSkillSystemMessage(working []Message, content string) []Message {
	if strings.TrimSpace(content) == "" {
		return working
	}
	out := make([]Message, 0, len(working)+1)
	if len(working) == 0 {
		return append(out, NewMessage("system", content, nil, "", ""))
	}
	out = append(out, working[0])
	out = append(out, NewMessage("system", content, nil, "", ""))
	out = append(out, working[1:]...)
	return out
}

// contextOutputReserve is the room a call has to leave for the model's own
// answer: max_tokens plus a margin for provider-side accounting.
func contextOutputReserve(cfg *Config) int {
	reserve := 8192
	if cfg != nil && cfg.API.MaxTokens > 0 {
		reserve = cfg.API.MaxTokens
	}
	return reserve + 4096
}

// effectiveContextBudget is where compaction has to happen: the smaller of what
// the model can accept in one request and the operator's policy number.
//
// A fixed threshold alone is wrong at both ends. With a large-window model it
// compacts far more often than it needs to, and every compaction is a real loss
// of detail; with a small-window model it lets a call run past the provider
// limit and be rejected. min(window - output reserve, threshold) is the honest
// bound. When the window is not declared there is nothing to compare against, so
// the policy number stands on its own.
func effectiveContextBudget(cfg *Config) int {
	threshold := 60000
	if cfg != nil && cfg.SummaryThresholdTokens > 0 {
		threshold = cfg.SummaryThresholdTokens
	}
	if cfg == nil || cfg.API.ContextWindow <= 0 {
		return threshold
	}
	// Never plan to fill the window: the provider accounts for the prompt its own
	// way, the tool schemas ride along with every request, and leaving the last
	// fifth free is what keeps a long step from being rejected outright.
	usable := int(float64(cfg.API.ContextWindow) * contextWindowSafetyRatio)
	if usable <= 0 {
		usable = cfg.API.ContextWindow
	}
	budget := usable - contextOutputReserve(cfg)
	if budget <= 0 {
		// A window smaller than the answer reserve is a misconfiguration; half
		// the window at least leaves room to work.
		budget = usable / 2
	}
	if budget < threshold {
		return budget
	}
	return threshold
}

// contextWindowSafetyRatio is the fraction of a model's declared window the
// harness is willing to fill before compacting.
const contextWindowSafetyRatio = 0.8

// contextScaleFactor says how far the local token estimate is from the count the
// provider actually billed.
//
// Measured against every call: on this machine the estimate ran 1.3x to 5.9x
// high, so a conversation whose real prompt was 81k tokens looked like 478k to
// the harness - which then compacted twice in six minutes and every compaction
// is a real loss of detail. The ratio is clamped so one odd call (a retry, a
// provider that counts cached tokens differently) cannot swing the budget.
func contextScaleFactor(actual, estimated int) float64 {
	if actual <= 0 || estimated <= 0 {
		return 0
	}
	scale := float64(actual) / float64(estimated)
	switch {
	case scale < 0.2:
		return 0.2
	case scale > 2:
		return 2
	default:
		return scale
	}
}

// calibratedTokens converts a local estimate into provider tokens.
func calibratedTokens(estimated int, scale float64) int {
	if estimated <= 0 {
		return 0
	}
	if scale <= 0 {
		return estimated
	}
	return int(float64(estimated) * scale)
}

func effectiveSummaryPrompt(summaryPrompt string, readFiles map[string]bool) string {
	prompt := summaryPrompt
	if len(readFiles) == 0 {
		return prompt
	}
	paths := make([]string, 0, len(readFiles))
	for path := range readFiles {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	prompt += "\n\n## 已读文件清单（必须保留）\n" +
		"以下文件已在本会话被读取过。summary 的 [Important Files and Artifacts] 栏目必须逐条保留这些路径，" +
		"并为每个文件写 1-2 句关键内容/约束摘要。后续模型应据此判断无需重复读取：\n" +
		strings.Join(paths, "\n")
	return prompt
}

func forceCompactWorkingMessages(messages []Message, threshold int) ([]Message, *Message) {
	sticky := make([]Message, 0, 2)
	lastUser := -1
	for index, message := range messages {
		if message.Role == "system" {
			sticky = append(sticky, message)
		}
		if message.Role == "user" {
			lastUser = index
		}
	}

	compacted := append([]Message{}, sticky...)
	fixedTokens := estimateMessagesContextTokens(compacted)
	if lastUser >= 0 {
		latest := messages[lastUser]
		charBudget := (threshold - fixedTokens) / 3
		if charBudget < 2000 {
			charBudget = 2000
		}
		latest.Content = compactContextText(latest.Content, charBudget)
		compacted = append(compacted, latest)
	}

	summaryText := withCurrentRequest(strings.TrimSpace(buildCompactExtractiveSummary(messages)), messages)
	var summaryMessage *Message
	if summaryText != "" {
		summaryBudget := threshold - estimateMessagesContextTokens(compacted) - 32
		if summaryBudget < 32 {
			summaryBudget = 32
		}
		summaryText = compactContextText(summaryText, summaryBudget)
		// Context checkpoints are synthetic runtime state, not model turns.
		// Sending them as assistant messages breaks providers that require every
		// assistant turn to carry reasoning_content.
		summary := NewMessage("user", "<summary>\n"+summaryText+"\n</summary>", nil, "", "")
		summary.InternalType = "context_summary"
		summaryMessage = &summary
		compacted = append(compacted, summary)
	}
	if len(compacted) == 0 || compacted[len(compacted)-1].Role != "user" {
		compacted = append(compacted, newInternalControlMessage("user", "请继续。", internalTypeAutoContinue))
	}
	return compacted, summaryMessage
}

// preflightWorkingContext is the single outbound context guard. It runs before
// every model call and also accepts force=true when the provider reports a real
// context-length error. The returned SummaryMessage must be appended to the
// full transcript by the caller and tagged for segmented memory.
func preflightWorkingContext(
	workingMsgs []Message,
	cfg *Config,
	summaryPrompt string,
	readFiles map[string]bool,
	deliveredItems []string,
	force bool,
	scale float64,
) contextPreflightResult {
	// The trigger is the smaller of the model's real window and the operator's
	// policy: see effectiveContextBudget.
	threshold := effectiveContextBudget(cfg)
	// The budget is in provider tokens; everything inside this function compares
	// against the local estimate, so it is converted once here and the numbers
	// reported back are converted the other way. That is what makes the trace
	// panel (provider tokens) and the trigger agree.
	estimatedThreshold := threshold
	if scale > 0 && scale != 1 {
		estimatedThreshold = int(float64(threshold) / scale)
	}
	if estimatedThreshold <= 0 {
		estimatedThreshold = threshold
	}
	retainEstimated := cfg.SummaryRetainTokens
	if scale > 0 && scale != 1 && retainEstimated > 0 {
		retainEstimated = int(float64(retainEstimated) / scale)
	}
	result := contextPreflightResult{
		Messages:     workingMsgs,
		BeforeTokens: estimateMessagesContextTokens(workingMsgs),
	}
	result.AfterTokens = result.BeforeTokens
	if !force && result.BeforeTokens < threshold {
		return result
	}

	out := append([]Message{}, workingMsgs...)
	result.AfterTokens = calibratedTokens(estimateMessagesContextTokens(out), scale)
	if result.AfterTokens < threshold {
		result.Messages = out
		return result
	}

	plan, ok := selectCompressionPlan(out, estimatedThreshold, retainEstimated)
	if !ok {
		result.Messages, result.SummaryMessage = forceCompactWorkingMessages(out, estimatedThreshold)
		result.Summarized = true
		result.Degraded = true
		result.DegradeReason = "没有可选的压缩区间（消息太少或结构异常），改用强制压缩"
		result.AfterTokens = calibratedTokens(estimateMessagesContextTokens(result.Messages), scale)
		result.TranscriptTail = transcriptRecoveryMessages(result.Messages)
		return result
	}

	compressMsgs := out[plan.compressStart : plan.compressEnd+1]
	summaryText := ""
	compressTokens := estimateMessagesContextTokens(compressMsgs)
	// The summarizer is a model call of its own, not the working context, so its
	// budget is not the compaction threshold: at 08:49 a 287k-token region was
	// skipped by a 200k threshold and the mechanical fallback recited old work
	// instead. Give it room, and say so when even that is not enough.
	summaryCallBudget := estimatedThreshold * 2
	if summaryPrompt == "" {
		result.Degraded = true
		result.DegradeReason = "未配置摘要提示词（config/modules/summary/zh.md），无法调用摘要模型"
	} else {
		// The rounds that survive compression still tell the compressor what is
		// happening now, so they go in as input too - as long as the call still
		// fits, otherwise the labelled current request below carries the priority
		// on its own.
		recent := append(append([]Message{}, plan.retainedUsers...), plan.retainedTail...)
		if compressTokens+estimateMessagesContextTokens(recent) > summaryCallBudget {
			recent = nil
		}
		summaryText, lastErr := summarizeRegionWithModel(cfg, out, compressMsgs, recent, effectiveSummaryPrompt(summaryPrompt, readFiles), summaryCallBudget, 0)
		if lastErr != nil {
			result.Degraded = true
			result.DegradeReason = "摘要模型调用失败（已重试）：" + lastErr.Error()
			fmt.Fprintf(os.Stderr, "[AgentLoop] preflight summary failed; using extractive fallback: %v\n", lastErr)
		} else if summaryText == "" {
			result.Degraded = true
			result.DegradeReason = "摘要模型返回了空内容"
		}
	}
	if summaryText == "" {
		summaryText = withCurrentRequest(strings.TrimSpace(buildCompactExtractiveSummary(compressMsgs)), out)
	}
	// Either the model wrote the handover or the mechanical fallback did; the
	// reason is carried whether or not the fallback found anything to say.
	if summaryText == "" || result.Degraded {
		result.Degraded = true
		if result.DegradeReason == "" {
			result.DegradeReason = "摘要模型没有产出内容，改用机械兜底"
		}
	}
	if summaryText == "" {
		result.Messages, result.SummaryMessage = forceCompactWorkingMessages(out, threshold)
		result.Summarized = true
		result.AfterTokens = estimateMessagesContextTokens(result.Messages)
		result.TranscriptTail = transcriptRecoveryMessages(result.Messages)
		return result
	}

	// Keep internal summaries in a non-assistant role. They are context state,
	// not replies, and reasoning-mode APIs reject synthetic assistant turns
	// without reasoning_content.
	summaryMsg := NewMessage("user", "<summary>\n"+summaryText+"\n</summary>", nil, "", "")
	summaryMsg.InternalType = "context_summary"
	result.SummaryMessage = &summaryMsg

	stickyMessages := make([]Message, 0, 1)
	for _, message := range out {
		if message.Role == "system" {
			stickyMessages = append(stickyMessages, message)
		}
	}
	next := append([]Message{}, stickyMessages...)
	next = append(next, plan.retainedUsers...)
	next = append(next, summaryMsg)
	if len(deliveredItems) > 0 {
		next = append(next, newInternalControlMessage("user", deliveredStatusText(deliveredItems), internalTypeDeliveredStatus))
	}
	next = append(next, plan.retainedTail...)
	if len(next) > 0 && next[len(next)-1].Role != "user" {
		next = append(next, newInternalControlMessage("user", "请继续。", internalTypeAutoContinue))
	}

	result.Messages = next
	result.Summarized = true
	result.AfterTokens = calibratedTokens(estimateMessagesContextTokens(next), scale)
	if result.AfterTokens >= threshold {
		result.Messages, result.SummaryMessage = forceCompactWorkingMessages(next, estimatedThreshold)
		result.Degraded = true
		if result.DegradeReason == "" {
			result.DegradeReason = fmt.Sprintf("压缩后仍超过阈值（%d tokens），再压一次到机械兜底", result.AfterTokens)
		}
		result.AfterTokens = calibratedTokens(estimateMessagesContextTokens(result.Messages), scale)
	}
	result.TranscriptTail = transcriptRecoveryMessages(result.Messages)
	return result
}

func transcriptRecoveryMessages(working []Message) []Message {
	start := -1
	for index, message := range working {
		if isSummaryMarker(message) {
			start = index
		}
	}
	if start < 0 {
		return nil
	}
	out := make([]Message, 0, len(working)-start)
	for _, message := range working[start:] {
		if message.Role == "system" || isInternalControlMessage(message) {
			continue
		}
		out = append(out, message)
	}
	return out
}

func appendTranscriptRecovery(messages []Message, preflight contextPreflightResult, recorder *TurnMemoryRecorder) []Message {
	tail := preflight.TranscriptTail
	if len(tail) == 0 && preflight.SummaryMessage != nil {
		tail = []Message{*preflight.SummaryMessage}
	}
	for _, message := range tail {
		message := message
		if isInternalControlMessage(message) {
			continue
		}
		recorder.TagMessage(&message)
		messages = append(messages, message)
	}
	return messages
}

type SessionUsage struct {
	PromptTokens     int   `json:"prompt_tokens"`
	CompletionTokens int   `json:"completion_tokens"`
	DurationMs       int64 `json:"duration_ms"`
}

type AgentResult struct {
	Messages        []Message
	Steps           int
	Usage           *SessionUsage
	PerMessageUsage map[int]*PerMsgUsageEntry
	Title           string
	Trace           []map[string]interface{}
}

type PerMsgUsageEntry struct {
	Usage      *UsageInfo `json:"usage"`
	DurationMs int64      `json:"duration_ms"`
}

func NewMessage(role, content string, toolCalls []ToolCall, toolCallID, name string) Message {
	m := Message{Role: role}
	if content != "" {
		m.Content = content
	}
	if len(toolCalls) > 0 {
		m.ToolCalls = toolCalls
	}
	if toolCallID != "" {
		m.ToolCallID = toolCallID
	}
	if name != "" {
		m.Name = name
	}
	return m
}

var emitEventMu sync.Mutex

// emitEvent prints a JSON event to stdout for real-time SSE forwarding.
// Serializing writes keeps heartbeat goroutines from interleaving with
// assistant chunks or tool events.
func emitEvent(eventType string, data map[string]interface{}) {
	emitEventMu.Lock()
	defer emitEventMu.Unlock()
	if data == nil {
		data = make(map[string]interface{})
	}
	data["type"] = eventType
	line, err := json.Marshal(data)
	if err != nil {
		return
	}
	fmt.Println(string(line))
}

func emitLLMProgress(step int, progress LLMStreamProgress) {
	emitEvent("llm_progress", map[string]interface{}{
		"step":                 step,
		"phase":                progress.Phase,
		"elapsed_ms":           progress.ElapsedMs,
		"since_last_delta_ms":  progress.SinceLastDeltaMs,
		"received_bytes":       progress.ReceivedBytes,
		"content_bytes":        progress.ContentBytes,
		"tool_arguments_bytes": progress.ToolArgumentsBytes,
		"chunk_count":          progress.ChunkCount,
		"tool_calls":           progress.ToolCalls,
	})
}

func addLLMUpstreamRequestID(event map[string]interface{}, resp *APIResponse) {
	if resp == nil || strings.TrimSpace(resp.UpstreamRequestID) == "" {
		return
	}
	event["upstream_request_id"] = resp.UpstreamRequestID
}

func addLLMStreamDiagnostics(event map[string]interface{}, resp *APIResponse) {
	if resp == nil || !resp.Streaming {
		return
	}
	event["streaming"] = true
	event["request_sent_at_ms"] = resp.RequestSentAtMs
	event["response_complete_at_ms"] = resp.ResponseCompleteAtMs
	event["first_delta_ms"] = resp.FirstDeltaMs
	event["stream_chunk_count"] = resp.StreamChunkCount
	event["received_bytes"] = resp.ReceivedBytes
	event["content_bytes"] = resp.ContentBytes
	event["tool_arguments_bytes"] = resp.ToolArgumentsBytes
}

func RunAgentLoop(
	cfg *Config,
	registry *ToolRegistry,
	systemPrompt, userPrompt string,
	initialMessages []Message,
	initialSessionUsage *SessionUsage,
	summaryPrompt, generateTitlePrompt, reflectionPrompt string,
	sessionFile, modelName string,
	autoAnswerAskUser bool,
) (*AgentResult, error) {
	return RunAgentLoopCtx(context.Background(), nil, cfg, registry, systemPrompt, userPrompt,
		initialMessages, initialSessionUsage, summaryPrompt, generateTitlePrompt, reflectionPrompt,
		sessionFile, modelName, autoAnswerAskUser)
}

// RunAgentLoopCtx is the cancellable + injectable variant. The optional
// controller exposes:
//   - Cancel(): cancels the agent's context. Any in-flight LLM stream and
//     tool call abort immediately. The loop emits `{"type":"interrupted"}`
//     and returns ErrAgentInterrupted.
//   - InjectMessage(text): appends text to the user message queue. The next
//     time the loop is between steps it will pick the message up and feed it
//     to the model as a new user turn. Pending messages are delivered before
//     any tool dispatch in the same step.
//
// Passing nil controller is equivalent to RunAgentLoop.
func RunAgentLoopCtx(
	ctx context.Context,
	controller *AgentController,
	cfg *Config,
	registry *ToolRegistry,
	systemPrompt, userPrompt string,
	initialMessages []Message,
	initialSessionUsage *SessionUsage,
	summaryPrompt, generateTitlePrompt, reflectionPrompt string,
	sessionFile, modelName string,
	autoAnswerAskUser bool,
) (*AgentResult, error) {
	if registry == nil {
		return nil, fmt.Errorf("tool registry is nil")
	}
	isSubtaskRun := os.Getenv("AGENT_RUN_KIND") == "subtask"
	if controller == nil {
		controller = NewAgentController(ctx)
	}
	defer controller.Close()
	// ???? JSON ??????? session_id?httptool ??? X-FAIRY-Session-ID ?????/?????
	if sessionFile != "" {
		httptool.EnsureSessionID(sessionFile)
	}
	// Append-only session event log (durable JSONL; replayed to rebuild context).
	var eventLog *SessionEventLog
	if sessionFile != "" {
		if log, err := OpenSessionEventLog(sessionFile); err == nil {
			eventLog = log
			defer log.Close()
			_ = log.Append(map[string]any{"type": "session_start", "model": modelName})
		}
	}
	logEvent := func(ev map[string]any) {
		if eventLog != nil {
			_ = eventLog.Append(ev)
		}
	}
	memoryRecorder := NewTurnMemoryRecorder(cfg, sessionFile, isSubtaskRun)
	defer memoryRecorder.Close()
	toolDefs := registry.ListSchemas()
	subtaskForbidden := subtaskForbiddenToolSet(cfg)
	if isSubtaskRun {
		toolDefs = filterSubtaskToolDefs(toolDefs, subtaskForbidden)
	}
	dispatcher := &ToolDispatcher{Registry: registry, MaxConcurrency: 4}

	var messages []Message
	// readFiles: files successfully read via read_file in this agent run.
	// Kept across summary compressions so the model never re-reads them after
	// compression forgets the earlier reads (graded protection: system > user
	// request > skill docs > tool call history).
	readFiles := map[string]bool{}
	readFileAttempts := map[readFileAttemptKey]int{}
	readOnlyStreak := 0
	executionGuards := 0
	requirePlanBeforeMoreReads := false
	implementationTask := likelyImplementationTask(userPrompt)
	// deliveredItems: final reports/answers already produced in this
	// conversation. Survives summary compression so later turns never redo
	// completed work (code-level state, injected back after compression).
	var deliveredItems []string
	userMsg := NewMessage("user", userPrompt, nil, "", "")
	userMsg.Ts = time.Now().UnixMilli()
	memoryRecorder.TagMessage(&userMsg)
	turnStartTs := userMsg.Ts
	// A runtime continuation ("后台子任务已完成" and friends) is not a new user
	// request: it exists to push an already accepted plan forward. Every other
	// turn is the user speaking, and the plan gate must treat a plan older than
	// this turn as that older turn's unfinished business.
	planGateTurnInfo := planGateTurn{
		StartedAt:             time.UnixMilli(turnStartTs),
		RuntimeContinuation:   isInternalControlMessage(userMsg),
		ContinuationRequested: isPlanContinuationRequest(normalizePlanMatchText(userPrompt)),
	}
	if len(initialMessages) > 0 {
		messages = append([]Message{}, initialMessages...)
		// Seed delivered items from a resumed session so completion state
		// survives across processes (no redo after resume).
		for _, m := range initialMessages {
			if m.Role == "assistant" {
				if title := extractReportTitle(m.Content); title != "" {
					deliveredItems = addUnique(deliveredItems, title)
				}
			}
		}
		messages = append(messages, userMsg)
	} else {
		messages = append(messages, NewMessage("system", systemPrompt, nil, "", ""))
		messages = append(messages, userMsg)
	}
	logEvent(map[string]any{"type": "message", "role": "user", "content": userPrompt, "step": 0})
	memoryRecorder.RecordUserRequest(userPrompt)

	// AI working view keeps the same-session history and normal summary
	// compression boundary. The full transcript remains in `messages` for
	// UI/trace/persistence; cross-session recall is carried by the compact
	// segmented-memory KEY block in systemPrompt.
	workingMsgs := buildInitialWorkingMessages(isSubtaskRun, systemPrompt, initialMessages, userMsg)
	if !isSubtaskRun {
		if skillContext := BuildSkillRoutingContext(cfg, userPrompt); skillContext != "" {
			workingMsgs = insertDynamicSkillSystemMessage(workingMsgs, skillContext)
		}
	}
	// 用户上传的图片此前只是以 <file_context> JSON 拼在消息文本里，模型只能
	// 拿到一个文件路径，想看图就只能绕道 image_vqa 再问一次另一个模型 ——
	// 既慢又丢信息。这里把图片转成真正的 image_url 视觉附件直接挂到本轮，
	// 主模型像看工具截图一样直接看图；image_vqa 退回纯文本模型兜底的定位。
	if visionMsg, ok := userUploadVisionContextMessage(cfg, workingMsgs); ok {
		workingMsgs = append(workingMsgs, visionMsg)
	}

	perMsgUsage := make(map[int]*PerMsgUsageEntry)
	postReflection := false           // Track if reflection has been injected
	toolsUsedThisTurn := 0            // tool calls executed this turn (multi-step signal)
	turnToolUsage := map[string]int{} // tool name -> call count for this turn
	turnToolCalls := 0                // total tool calls replayed this turn
	lastPromptTokens := 0             // prompt tokens of the last LLM call (context size)
	contextScale := 0.0               // provider tokens / local estimate, from the last call
	executionNudges := 0              // times the loop nudged a no-tool tutorial back to execution
	userInterrupted := false          // a message arrived from the middle of this turn
	lastPlanGateSignature := ""       // last plan snapshot checked at a no-tool-call stop
	planGateLastToolsUsed := 0        // tool count when the last plan gate ran
	var pendingToolScoreCleanup *toolScoreCleanupJob
	cleanupRerunQueued := false
	defer func() {
		if pendingToolScoreCleanup == nil {
			return
		}
		// Final non-blocking drain: cleanup may finish while the last model call
		// is in flight, after which there is no next step boundary. Apply only a
		// completed result; an in-flight classifier request is still cancelled.
		done, cleared, _, _, cleanupErr := drainToolScoreCleanupJob(
			pendingToolScoreCleanup, workingMsgs, messages, pinnedPlanToolCallIDs(cfg, sessionFile),
		)
		if done && cleanupErr == nil && cleared > 0 && sessionFile != "" {
			if saveErr := SaveSession(sessionFile, messages, modelName); saveErr != nil {
				fmt.Fprintf(os.Stderr, "[AgentLoop] WARN: final async tool score cleanup save session: %v\n", saveErr)
			}
		}
		pendingToolScoreCleanup.cancel()
	}()

	sessionUsage := &SessionUsage{}
	if initialSessionUsage != nil {
		sessionUsage.PromptTokens = initialSessionUsage.PromptTokens
		sessionUsage.CompletionTokens = initialSessionUsage.CompletionTokens
		sessionUsage.DurationMs = initialSessionUsage.DurationMs
	}

	softStepLimit := cfg.MaxSteps
	if softStepLimit <= 0 {
		softStepLimit = 60
	}
	if isSubtaskRun {
		softStepLimit = cfg.SubtaskMaxSteps
		if softStepLimit <= 0 {
			softStepLimit = 20
		}
	}

	// Trace log (matches AgentLoop loop.go)
	trace := []map[string]interface{}{}
	trace = append(trace, map[string]interface{}{
		"event":          "loop_start",
		"step":           0,
		"messages_count": len(messages),
		// Which budget compaction will use, and where it came from: the trace
		// panel is the place an operator can see whether the model's window or
		// the policy number is the binding constraint.
		"context_threshold": cfg.SummaryThresholdTokens,
		"context_window":    cfg.API.ContextWindow,
		"context_budget":    effectiveContextBudget(cfg),
	})

	applyReadyToolScoreCleanup := func(currentStep int) error {
		if pendingToolScoreCleanup == nil {
			return nil
		}
		done, cleared, saved, cleaned, cleanupErr := drainToolScoreCleanupJob(
			pendingToolScoreCleanup, workingMsgs, messages, pinnedPlanToolCallIDs(cfg, sessionFile),
		)
		if !done {
			return nil
		}
		trigger := pendingToolScoreCleanup.trigger
		pendingToolScoreCleanup = nil
		if cleanupErr != nil {
			trace = append(trace, map[string]interface{}{
				"event": "tool_score_cleanup", "step": currentStep, "status": "failed",
				"trigger": trigger, "error": cleanupErr.Error(), "async": true,
			})
			if !cfg.ToolScoreCleanup.FailOpen {
				return fmt.Errorf("tool score cleanup at step %d: %w", currentStep, cleanupErr)
			}
			fmt.Fprintf(os.Stderr, "[AgentLoop] WARN: async tool score cleanup skipped: %v\n", cleanupErr)
			cleanupRerunQueued = false
			return nil
		}
		if cleared > 0 {
			trace = append(trace, map[string]interface{}{
				"event": "tool_score_cleanup", "step": currentStep, "cleared_results": cleared,
				"saved_runes": saved, "trigger": trigger, "threshold": cfg.ToolScoreCleanup.Threshold,
				"cleaned": cleaned, "async": true,
			})
			if sessionFile != "" {
				if saveErr := SaveSession(sessionFile, messages, modelName); saveErr != nil {
					trace = append(trace, map[string]interface{}{
						"event": "tool_score_cleanup", "step": currentStep, "status": "save_failed",
						"trigger": trigger, "error": saveErr.Error(), "async": true,
					})
					fmt.Fprintf(os.Stderr, "[AgentLoop] WARN: async tool score cleanup save session: %v\n", saveErr)
				}
			}
		}
		if cleanupRerunQueued {
			cleanupRerunQueued = false
			if !isSubtaskRun && cfg.ToolScoreCleanup.Enabled {
				if candidates := CollectToolScoreCandidatesPinned(workingMsgs, cfg, pinnedPlanToolCallIDs(cfg, sessionFile)); len(candidates) > 0 {
					pendingToolScoreCleanup = startToolScoreCleanupJob(controller.Context(), candidates, cfg, "rerun", currentStep)
					trace = append(trace, map[string]interface{}{
						"event": "tool_score_cleanup", "step": currentStep, "status": "scheduled",
						"trigger": "rerun", "scored": len(candidates), "async": true,
					})
				}
			}
		}
		return nil
	}

	// Accumulated text for streaming route detection
	accumulatedText := ""

	// Hard cap on research/network tool calls per agent loop (web_search / web_fetch / image_search / image_generate).
	// Prevents runaway research loops (observed 32x web_search + 35x web_fetch in one deep-research subtask).
	networkCalls := 0

	finalRecoveryAttempts := 0
	contextRecoveryAttempts := 0
	contextThreshold := effectiveContextBudget(cfg)

	step := 0
	for {
		step++
		if err := applyReadyToolScoreCleanup(step); err != nil {
			return nil, err
		}

		// Step limits are advisory checkpoints, not hard stops. Long-running
		// tasks (PPT generation, deep research, large refactors) often need more
		// than the recommended budget; killing them here causes incomplete work
		// and expensive full-context restarts. Nudge the model once to finish the
		// current work without abandoning or duplicating it, then keep going.
		if step == softStepLimit {
			nudge := "已达到建议步数检查点。继续执行，直到当前任务真正完成；不要因为步数检查点提前结束、放弃未完成工作或重复已经完成的操作。"
			if isSubtaskRun {
				nudge = "已达到建议步数检查点。继续完成当前工作包；不要因为步数检查点提前输出失败的 subtask_result、放弃未完成工作或重复已经完成的操作。只有任务确实完成或遇到真实阻塞时才结束。"
			}
			workingMsgs = append(workingMsgs, NewMessage("user", nudge, nil, "", ""))
			emitEvent("status", map[string]interface{}{
				"step": step, "message": "已达到建议步数检查点，任务将继续执行...", "soft_step_limit": softStepLimit,
			})
			trace = append(trace, map[string]interface{}{"event": "soft_step_limit", "step": step, "limit": softStepLimit})
		}

		// Mid-loop interrupts: three distinct exits:
		//   1. Hard cancel (ctx.Done) — abort everything, return ErrAgentInterrupted.
		//   2. Soft stop with queued messages — drain the pending queue into a
		//      fresh turn, run it to completion, then exit cleanly. This is the
		//      "user interrupted and changed topic" path; no work is lost.
		//   3. Soft stop with empty queue — exit immediately after the current
		//      tool/LLM round lands a final answer (no extra LLM call).
		select {
		case <-controller.Done():
			emitEvent("interrupted", map[string]interface{}{"step": step, "reason": "user_cancel"})
			logEvent(map[string]any{"type": "interrupted", "step": step, "reason": "user_cancel"})
			return &AgentResult{Messages: messages, Steps: step}, ErrAgentInterrupted
		default:
		}
		if controller.SoftStopRequested() && len(controller.PeekPending()) == 0 {
			emitEvent("interrupted", map[string]interface{}{"step": step, "reason": "user_stop_empty"})
			logEvent(map[string]any{"type": "interrupted", "step": step, "reason": "user_stop_empty"})
			return &AgentResult{Messages: messages, Steps: step}, ErrAgentInterrupted
		}
		for {
			injected, injectID, ok := controller.PopPending()
			if !ok {
				break
			}
			// From here to the end of this turn, the user's message is the job.
			// The flag is what makes that true rather than aspirational: the plan
			// gate below would otherwise keep pushing the old chain forward.
			if !userInterrupted {
				userInterrupted = true
				interruptNote := newInternalControlMessage("system", userInterruptRuleText, internalTypeUserInterrupt)
				interruptNote.Step = step
				workingMsgs = append(workingMsgs, interruptNote)
				trace = append(trace, map[string]interface{}{"event": "user_interrupt", "step": step})
			}
			userMsg := NewMessage("user", injected, nil, "", "")
			userMsg.Ts = time.Now().UnixMilli()
			memoryRecorder.TagMessage(&userMsg)
			messages = append(messages, userMsg)
			if !isSubtaskRun {
				if skillContext := BuildSkillRoutingContext(cfg, injected); skillContext != "" {
					workingMsgs = append(workingMsgs, NewMessage("system", skillContext, nil, "", ""))
				}
			}
			workingMsgs = append(workingMsgs, userMsg)
			logEvent(map[string]any{"type": "injected_user", "id": injectID, "step": step, "content": injected})
			emitEvent("injected_user", map[string]interface{}{"id": injectID, "step": step, "content": injected, "pending": false})
		}

		// Outbound preflight: compact before the request, not after a provider
		// rejects it. This covers both local estimates and a provider-reported
		// context size from the previous successful call.
		preflight := preflightWorkingContext(
			workingMsgs,
			cfg,
			summaryPrompt,
			readFiles,
			deliveredItems,
			lastPromptTokens >= contextThreshold,
			contextScale,
		)
		workingMsgs = preflight.Messages
		if preflight.Summarized {
			messages = appendTranscriptRecovery(messages, preflight, memoryRecorder)
			if preflight.SummaryMessage != nil {
				logEvent(map[string]any{"type": "summary", "content": preflight.SummaryMessage.Content})
			}
			event := map[string]interface{}{
				"event":           "context_preflight",
				"step":            step,
				"before_tokens":   preflight.BeforeTokens,
				"after_tokens":    preflight.AfterTokens,
				"summarized":      preflight.Summarized,
				"content_preview": contextPreflightPreview(preflight),
			}
			if preflight.Degraded {
				event["degraded"] = true
				event["degrade_reason"] = preflight.DegradeReason
			}
			trace = append(trace, event)
			message := "正在整理当前轮次上下文..."
			if preflight.Summarized {
				message = "正在压缩当前轮次上下文..."
			}
			if preflight.Degraded {
				// Never let a degraded handover pass unnoticed: the user is the one
				// who pays for it (the agent behaves as if the history never
				// happened), so the status line says so and the log keeps why.
				message = "上下文压缩降级：" + preflight.DegradeReason
				logEvent(map[string]any{
					"type": "compaction_degraded", "step": step,
					"reason": preflight.DegradeReason,
					"before_tokens": preflight.BeforeTokens, "after_tokens": preflight.AfterTokens,
				})
				fmt.Fprintf(os.Stderr, "[AgentLoop] WARN: compaction degraded at step %d: %s\n", step, preflight.DegradeReason)
			}
			emitEvent("status", map[string]interface{}{"step": step, "message": message})
			if sessionFile != "" {
				if err := SaveSession(sessionFile, messages, modelName); err != nil {
					fmt.Fprintf(os.Stderr, "[AgentLoop] WARN: post-preflight save session: %v\n", err)
				}
			}
		}

		// Emit progress event
		emitEvent("status", map[string]interface{}{"step": step, "message": fmt.Sprintf("正在调用 LLM（第 %d 步）...", step), "soft_step_limit": softStepLimit})

		// Final outbound guard: never send a history that breaks the
		// assistant tool_calls / tool result pairing contract.
		if err := validateToolMessageHistory(workingMsgs); err != nil {
			trace = append(trace, map[string]interface{}{
				"event": "loop_error", "step": step, "category": "history_contract_error", "error": err.Error(),
			})
			return nil, fmt.Errorf("model history contract at step %d: %w", step, err)
		}

		// Call LLM with streaming: forward user-visible text deltas as
		// assistant/chunk events so the frontend can render incrementally.
		// Malformed tool-call payloads are rejected before execution and get a
		// single bounded recovery attempt.
		var resp *APIResponse
		var err error
		for responseAttempt := 1; responseAttempt <= 2; responseAttempt++ {
			requestMessages := workingMsgs
			if responseAttempt == 2 {
				requestMessages = append([]Message{}, workingMsgs...)
				requestMessages = append(requestMessages, NewMessage("user", invalidToolCallRecoveryPrompt, nil, "", ""))
			}
			maxTokensAttempt := 1
			callCfg := cfg
			for {
				var streamedContent strings.Builder
				var streamThink thinkStreamer
				caller := func(callCfg *Config, callMessages []Message, callTools []ToolDef) (*APIResponse, error) {
					return CallConfiguredLLMStreamCtxWithProgress(
						controller.Context(),
						callCfg,
						callMessages,
						callTools,
						func(delta string) {
							streamedContent.WriteString(delta)
							visDelta, thinkDelta := streamThink.Update(streamedContent.String())
							if visDelta != "" {
								memoryRecorder.AppendAssistantDelta(visDelta, step)
							}
							if thinkDelta != "" {
								emitEvent("assistant/thinking", map[string]interface{}{"content": thinkDelta})
							}
							if visDelta != "" {
								emitEvent("assistant/chunk", map[string]interface{}{"content": visDelta})
							}
						},
						func(progress LLMStreamProgress) {
							emitLLMProgress(step, progress)
						},
					)
				}
				var recoveryAttempted bool
				var initialError llmCallErrorInfo
				resp, err, recoveryAttempted, initialError = callConfiguredLLMWithRecovery(
					callCfg, requestMessages, toolDefs, isSubtaskRun && responseAttempt == 1, caller,
				)
				if recoveryAttempted {
					recoveryTrace := map[string]interface{}{
						"event": "llm_call_recovery", "step": step, "attempt": 2,
						"category": initialError.Category, "retryable": initialError.Retryable, "status": "ok",
					}
					if initialError.HTTPStatus != 0 {
						recoveryTrace["initial_http_status"] = initialError.HTTPStatus
					}
					if err != nil {
						recoveryTrace["status"] = "failed"
					}
					trace = append(trace, recoveryTrace)
				}
				if err != nil {
					finalError := classifyLLMCallError(err)
					errorTrace := map[string]interface{}{
						"event": "loop_error", "step": step, "error": err.Error(),
						"category": finalError.Category, "retryable": finalError.Retryable && !recoveryAttempted,
						"recovery_attempted": recoveryAttempted,
					}
					if finalError.HTTPStatus != 0 {
						errorTrace["http_status"] = finalError.HTTPStatus
					}
					trace = append(trace, errorTrace)
					if reason, statusText, useFallback := shouldFallbackToAnotherModel(err, finalError); useFallback {
						if previousModel, switched := activateFallbackModel(cfg, cfg.FallbackModel); switched {
							trace = append(trace, map[string]interface{}{
								"event": "llm_model_fallback", "step": step,
								"from_model": previousModel, "to_model": cfg.SelectedModelID,
								"reason": reason,
							})
							emitEvent("status", map[string]interface{}{
								"step":    step,
								"message": statusText,
							})
							continue
						}
					}
					if finalError.Category == "llm_context_length" && contextRecoveryAttempts < 1 {
						contextRecoveryAttempts++
						preflight := preflightWorkingContext(
							workingMsgs,
							cfg,
							summaryPrompt,
							readFiles,
							deliveredItems,
							true,
							contextScale,
						)
						workingMsgs = preflight.Messages
						requestMessages = workingMsgs
						messages = appendTranscriptRecovery(messages, preflight, memoryRecorder)
						if preflight.SummaryMessage != nil {
							logEvent(map[string]any{"type": "summary", "content": preflight.SummaryMessage.Content})
						}
						trace = append(trace, map[string]interface{}{
							"event":           "context_recovery",
							"step":            step,
							"before_tokens":   preflight.BeforeTokens,
							"after_tokens":    preflight.AfterTokens,
							"summarized":      preflight.Summarized,
							"content_preview": contextPreflightPreview(preflight),
						})
						emitEvent("status", map[string]interface{}{"step": step, "message": "上下文超限，已压缩并重试..."})
						if sessionFile != "" {
							if saveErr := SaveSession(sessionFile, messages, modelName); saveErr != nil {
								fmt.Fprintf(os.Stderr, "[AgentLoop] WARN: post-context-recovery save session: %v\n", saveErr)
							}
						}
						continue
					}
					return nil, fmt.Errorf("API call at step %d: %w", step, err)
				}

				// Track usage for every gateway response, including a rejected
				// malformed tool response.
				if resp.Usage != nil {
					sessionUsage.PromptTokens += resp.Usage.PromptTokens
					sessionUsage.CompletionTokens += resp.Usage.CompletionTokens
					lastPromptTokens = resp.Usage.PromptTokens
					// Anchor the local estimate to what the provider actually
					// counted. The estimate on its own ran up to 5.9x high here,
					// which is what made the harness compact conversations whose
					// real prompt was 81k tokens.
					if scale := contextScaleFactor(resp.Usage.PromptTokens, estimateMessagesContextTokens(workingMsgs)); scale > 0 {
						contextScale = scale
					}
				}
				// Stream the running session's accumulated token usage to the
				// frontend so the header TOKENS counter ticks up while the
				// turn is still streaming instead of staying frozen at 0
				// until the agent finishes.
				emitEvent("session_usage", map[string]interface{}{
					"prompt_tokens":     sessionUsage.PromptTokens,
					"completion_tokens": sessionUsage.CompletionTokens,
				})
				callTrace := map[string]interface{}{
					"event": "llm_call", "step": step, "response_attempt": responseAttempt,
					"max_tokens_attempt": maxTokensAttempt, "requested_max_tokens": callCfg.API.MaxTokens,
					"finish_reason": resp.FinishStop,
				}
				if resp.Usage != nil {
					callTrace["prompt_tokens"] = resp.Usage.PromptTokens
					callTrace["completion_tokens"] = resp.Usage.CompletionTokens
				}
				if resp.DurationMs > 0 {
					callTrace["duration_ms"] = resp.DurationMs
				}
				addLLMUpstreamRequestID(callTrace, resp)
				addLLMStreamDiagnostics(callTrace, resp)
				trace = append(trace, callTrace)

				if !responseHitMaxTokens(resp, callCfg.API.MaxTokens) {
					break
				}
				if maxTokensAttempt >= 2 {
					trace = append(trace, map[string]interface{}{
						"event": "llm_max_tokens_escalation", "step": step, "status": "exhausted",
						"from_max_tokens": cfg.API.MaxTokens, "to_max_tokens": callCfg.API.MaxTokens,
					})
					return nil, fmt.Errorf("model output still hit max_tokens=%d after one temporary escalation at step %d", callCfg.API.MaxTokens, step)
				}
				nextMaxTokens, ok := doubledMaxTokens(callCfg.API.MaxTokens)
				if !ok {
					return nil, fmt.Errorf("cannot temporarily double max_tokens=%d at step %d", callCfg.API.MaxTokens, step)
				}
				trace = append(trace, map[string]interface{}{
					"event": "llm_max_tokens_escalation", "step": step, "status": "retrying",
					"from_max_tokens": callCfg.API.MaxTokens, "to_max_tokens": nextMaxTokens,
				})
				emitEvent("status", map[string]interface{}{
					"step": step, "message": "模型输出达到上限，正在临时扩大输出预算后重试…",
				})
				maxTokensAttempt++
				callCfg = configWithMaxTokens(cfg, nextMaxTokens)
			}

			// Some providers emit functions.name({...}) as plain text instead
			// of native tool_calls. Parse it before validation so the loop can
			// execute the call instead of leaking pseudo-call text to the UI.
			if len(resp.ToolCalls) == 0 {
				if calls, cleaned := extractInlineToolCalls(resp.Content); len(calls) > 0 {
					resp.ToolCalls = calls
					resp.Content = strings.TrimSpace(cleaned)
				}
			}
			normalizedCalls, validationErr := normalizeToolCalls(resp.ToolCalls)
			if validationErr == nil {
				resp.ToolCalls = normalizedCalls
				break
			}
			trace = append(trace, map[string]interface{}{
				"event": "tool_call_validation_failed", "step": step,
				"response_attempt": responseAttempt, "error": validationErr.Error(),
				"tool_call_count": len(resp.ToolCalls),
			})
			if responseAttempt == 2 {
				return nil, fmt.Errorf("invalid model tool calls after one recovery at step %d: %w", step, validationErr)
			}
			emitEvent("status", map[string]interface{}{
				"step": step, "message": "\u5de5\u5177\u8c03\u7528\u53c2\u6570\u683c\u5f0f\u4e0d\u5b8c\u6574\uff0c\u6b63\u5728\u8fdb\u884c\u4e00\u6b21\u6709\u754c\u6062\u590d\u2026",
			})
		}
		// <done> is the protocol marker meaning "turn finished". Strip it before
		// any downstream processing so it never leaks into UI/history.
		hadDone := strings.Contains(resp.Content, "<done>")
		if hadDone {
			resp.Content = strings.ReplaceAll(resp.Content, "<done>", "")
		}
		// Cap explicit thinking so a verbose model cannot blow up the context.
		resp.Content = capThinking(resp.Content)

		// --- Compliance check (port from AgentLoop loop.go) ---
		hasToolCalls := len(resp.ToolCalls) > 0
		if hasToolCalls {
			toolsUsedThisTurn++
		}

		// A truncated response without tool calls is not a valid final answer.
		// Some reasoning models can consume the entire completion budget before
		// emitting visible content, yielding an empty assistant message. Never
		// treat that as a successful turn; ask for a concise final delivery once
		// more instead of ending the UI with a blank result.
		if !hasToolCalls && !HasVisibleFinalContent(resp.Content) {
			if finalRecoveryAttempts >= 2 {
				return nil, fmt.Errorf("model did not produce a visible final answer after %d recovery attempts at step %d", finalRecoveryAttempts, step)
			}
			finalRecoveryAttempts++
			visibleDraft := strings.TrimSpace(stripReflectionBlocks(stripThinking(resp.Content)))
			if visibleDraft != "" && !strings.HasPrefix(visibleDraft, "<summary") {
				// Carries reasoning_content: this message goes into the next request,
				// and a thinking-mode API rejects an assistant turn that drops it.
				draft := assistantMessageFromResponse(resp, nil, step)
				memoryRecorder.TagMessage(&draft)
				workingMsgs = append(workingMsgs, draft)
			}
			workingMsgs = append(workingMsgs, NewMessage("user",
				"上一条回复没有形成用户可见的最终交付（内容为空、只有内部推理，或误把 <summary> 内部摘要当成回答）。不要再输出 <summary>，也不要展开长篇推理；请立即用简洁、完整、已闭合的最终答案收口，必要时使用 <report>...</report>。",
				nil, "", ""))
			trace = append(trace, map[string]interface{}{
				"event": "final_recovery", "step": step, "reason": "empty_final",
				"attempt": finalRecoveryAttempts,
			})
			emitEvent("status", map[string]interface{}{
				"step": step, "message": "最终回复未生成完整内容，正在重试收口...",
			})
			continue
		}
		route := GetContentRoute(resp.Content, hasToolCalls, accumulatedText)

		var compliance ComplianceResult
		if hasToolCalls {
			compliance = CheckIntermediateTurnCompliant(resp.Content)
			if !compliance.IsCompliant {
				fixed := RepairIntermediateContent(resp.Content)
				if fixed != resp.Content {
					resp.Content = fixed
					compliance = CheckIntermediateTurnCompliant(fixed)
				}
			}
		} else {
			compliance = CheckFinalTurnCompliant(resp.Content)
			if !compliance.IsCompliant {
				fixed := RepairFinalContent(resp.Content)
				if fixed != resp.Content {
					resp.Content = fixed
					compliance = CheckFinalTurnCompliant(fixed)
				}
			}
		}

		contentPreview := ""
		if resp.Content != "" {
			if len(resp.Content) > 120 {
				contentPreview = resp.Content[:120]
			} else {
				contentPreview = resp.Content
			}
		}

		trace = append(trace, map[string]interface{}{
			"event":           "model_response",
			"step":            step,
			"content_preview": contentPreview,
			"tool_call_count": len(resp.ToolCalls),
			"route":           route,
			"compliant":       compliance.IsCompliant,
			"violations":      strings.Join(compliance.Violations, ","),
		})

		if !compliance.IsCompliant {
			fmt.Fprintf(os.Stderr, "[AgentLoop] step %d non-compliant: route=%s violations=%s\n",
				step, route, strings.Join(compliance.Violations, ","))
		}

		accumulatedText += resp.Content

		// Build assistant message
		assistantMsg := assistantMessageFromResponse(resp, resp.ToolCalls, step)
		memoryRecorder.TagMessage(&assistantMsg)
		messages = append(messages, assistantMsg)
		workingMsgs = append(workingMsgs, assistantMsg)
		logEvent(map[string]any{"type": "message", "role": "assistant", "content": assistantMsg.Content, "step": step})
		memoryRecorder.RecordAssistantSnapshot(step, resp.Content, hasToolCalls)

		// Save per-message usage
		msgIdx := len(messages)
		if resp.Usage != nil {
			perMsgUsage[msgIdx] = &PerMsgUsageEntry{
				Usage:      resp.Usage,
				DurationMs: resp.DurationMs,
			}
		}

		// Save session after each step (enables mid-conversation recovery)
		if sessionFile != "" {
			if err := SaveSession(sessionFile, messages, modelName); err != nil {
				fmt.Fprintf(os.Stderr, "[AgentLoop] WARN: mid-loop save session: %v\n", err)
			}
		}

		// A no-tool-call response first passes through the active plan gate.
		// Reflection must not preempt unfinished executable work.
		planGate := planGateDecision{}
		if !hasToolCalls && !isSubtaskRun {
			planGate = decidePlanContinuation(cfg, sessionFile, toolsUsedThisTurn, lastPlanGateSignature, planGateLastToolsUsed, planGateTurnInfo)
		}

		// A no-tool-call response may be followed by reflection injection; mark
		// it as a draft so the frontend can show "drafting" and then replace it
		// with the post-reflection final report.
		willReflect := !hasToolCalls && !planGate.HasActive && reflectionPrompt != "" && cfg.Reflection.Enabled && !postReflection && toolsUsedThisTurn >= 1

		// Emit compliance info via SSE
		intermediateDescription := GetIntermediateDescription(resp.Content)
		emitEvent("assistant", map[string]interface{}{
			"content":                  stripThinking(resp.Content),
			"thinking":                 extractThinking(resp.Content),
			"tool_calls":               resp.ToolCalls,
			"route":                    route,
			"compliant":                compliance.IsCompliant,
			"violations":               compliance.Violations,
			"intermediate_description": intermediateDescription,
			"is_final":                 !hasToolCalls && !planGate.ShouldRun,
			"draft":                    willReflect,
			"step":                     step,
			"tools_used":               turnToolUsage,
		})

		// Process tool calls
		if !hasToolCalls {
			// Plan is the authoritative execution state. If it still contains
			// unfinished items, continue instead of terminating the turn.
			//
			// Unless the user spoke. Answering them is the whole point of this
			// turn then; see planContinuationAllowed. The reflection step below
			// still runs - it sharpens the answer to the new message and does not
			// touch the paused plan.
			if planContinuationAllowed(userInterrupted, planGate) {
				statusMessage := "检测到计划未完成，正在继续执行..."
				if planGate.Snapshot.NeedsFinalAcceptance {
					statusMessage = "计划步骤已完成，正在执行最终验收..."
				}
				emitEvent("status", map[string]interface{}{
					"step": step, "message": statusMessage,
				})
				planMsg := newInternalControlMessage("user", planGate.Snapshot.Prompt, internalTypePlanContinuation)
				planMsg.Step = step
				planMsg.Ts = time.Now().UnixMilli()
				workingMsgs = append(workingMsgs, planMsg)
				lastPlanGateSignature = planGate.Snapshot.Signature
				planGateLastToolsUsed = toolsUsedThisTurn
				trace = append(trace, map[string]interface{}{
					"event": "plan_continuation", "step": step, "question": planGate.Snapshot.Question,
					"active_items": len(planGate.Snapshot.Items),
				})
				continue
			}
			if planGate.Stalled && !userInterrupted {
				trace = append(trace, map[string]interface{}{
					"event": "plan_gate_stalled", "step": step, "question": planGate.Snapshot.Question,
					"active_items": len(planGate.Snapshot.Items),
				})
			}
			if planGate.Stale {
				// Visible in the trace instead of in a status line the user would
				// have to interpret: a leftover plan is normal, and the answer to
				// their new message is the thing that has to happen next.
				trace = append(trace, map[string]interface{}{
					"event": "plan_gate_stale", "step": step, "question": planGate.Snapshot.Question,
					"active_items": len(planGate.Snapshot.Items),
					"updated_at":   planGate.Snapshot.UpdatedAt.Format(time.RFC3339),
				})
			}

			// If the request was actionable but the model only produced a generic
			// tutorial, nudge it back into tool execution before finalizing.
			// A user interrupt is never nudged: the nudge exists to make the agent
			// act on the *previous* request, which is exactly what must wait now.
			if !userInterrupted && !planGate.Stalled && executionNudges < 2 && shouldNudgeToExecute(messages, resp.Content, toolsUsedThisTurn) {
				executionNudges++
				emitEvent("status", map[string]interface{}{"step": step, "message": "检测到只给了方案，正在提醒直接执行..."})
				workingMsgs = append(workingMsgs, NewMessage("user", tutorialNudgeText, nil, "", ""))
				continue
			}

			// No tool calls - check if we should inject reflection first
			if reflectionPrompt != "" && cfg.Reflection.Enabled && !postReflection && toolsUsedThisTurn >= 1 {
				// Inject reflection prompt as a system message for self-check
				postReflection = true
				emitEvent("status", map[string]interface{}{"step": step, "message": "正在起草稿并反思…"})
				workingMsgs = append(workingMsgs, NewMessage("system", reflectionPrompt, nil, "", ""))
				// The Clotho gateway rejects a trailing system message ("last message
				// role must be user"), so follow reflection with a neutral user trigger.
				workingMsgs = append(workingMsgs, NewMessage("user", reflectionNudgeText, nil, "", ""))
				continue
			}
			// A no-tool-call response ends the turn. The model itself decides
			// when it is done (think -> act -> hit problem -> re-think -> act);
			// forcing it to keep answering produced duplicated visible replies.
			if len(turnToolUsage) > 0 {
				assistantMsg.ToolsUsed = turnToolUsage
			}
			if turnStartTs > 0 {
				assistantMsg.RealMs = assistantMsg.Ts - turnStartTs
			}
			// Record this final delivery so post-compression turns never redo it.
			if title := extractReportTitle(resp.Content); title != "" {
				deliveredItems = addUnique(deliveredItems, title)
			} else if s := strings.TrimSpace(resp.Content); len(s) >= 15 {
				snippet := strings.Split(s, "\n")[0]
				deliveredItems = addUnique(deliveredItems, truncateStr(snippet, 40))
			}
			memoryRecorder.EndStep(step)
			endReason := "no_tool_calls"
			if userInterrupted {
				// The turn ended because it answered the user, not because the
				// plan ran out: keep that distinction in the trace.
				endReason = "user_interrupt"
			}
			trace = append(trace, map[string]interface{}{"event": "loop_end", "step": step, "reason": endReason})
			emitEvent("done", map[string]interface{}{"finish_reason": endReason})
			break
		}

		// 将模型工具调用转换为与平台无关的调用对象。受网络预算限制的调用
		// 仍会在原始位置获得结果；其余调用由 Dispatcher 并发处理，最后按照
		// resp.ToolCalls 中的原始顺序回放结果。
		pendingNetworkCalls := 0
		pendingWebTextCalls := 0
		for _, tc := range resp.ToolCalls {
			tname := tc.Function.Name
			if isSubtaskRun && subtaskForbidden[tname] {
				continue
			}
			isNetworkTool := tname == "web_search" || tname == "web_fetch" || tname == "image_search"
			if !isNetworkTool {
				continue
			}
			if cfg.MaxNetworkCalls > 0 && networkCalls+pendingNetworkCalls >= cfg.MaxNetworkCalls {
				continue
			}
			if cfg.ToolNeedsApproval(tname) {
				continue
			}
			pendingNetworkCalls++
			if isWebTextTool(tname) {
				pendingWebTextCalls++
			}
		}
		perWebCallBudget := 0
		if pendingWebTextCalls > 0 && webBudgetEnabled(cfg) {
			perWebCallBudget = dynamicWebBudgetTokens(cfg, lastPromptTokens) / pendingWebTextCalls
			if perWebCallBudget <= 0 {
				perWebCallBudget = 1
			}
			trace = append(trace, map[string]interface{}{
				"event":         "web_budget_allocated",
				"step":          step,
				"prompt_tokens": lastPromptTokens,
				"call_tokens":   perWebCallBudget,
				"call_count":    pendingWebTextCalls,
				"context_size":  webSearchContextSize(perWebCallBudget),
			})
		}
		var invocations []ToolInvocation
		cappedResults := make([]ToolInvocationResult, len(resp.ToolCalls))
		completed := make([]bool, len(resp.ToolCalls))
		pendingReadKeys := make(map[string]readFileAttemptKey)
		hasPlanCall := false
		for _, tc := range resp.ToolCalls {
			if tc.Function.Name == "plan" {
				hasPlanCall = true
				break
			}
		}
		for idx, tc := range resp.ToolCalls {
			tname := tc.Function.Name
			memoryRecorder.RecordToolCall(step, tname, tc.Function.Arguments)
			trace = append(trace, map[string]interface{}{
				"event":    "tool_invoked",
				"step":     step,
				"tool":     tname,
				"call_id":  tc.ID,
				"args_raw": tc.Function.Arguments,
			})
			emitEvent("tool_call", map[string]interface{}{"tool": tname, "status": "start", "arguments": tc.Function.Arguments})
			// Bounded subtasks must not delegate further, wait on the user, or
			// take over the main thread's checklist.
			if isSubtaskRun && subtaskForbidden[tname] {
				cappedResults[idx] = ToolInvocationResult{
					Index:  idx,
					CallID: tc.ID,
					Name:   tname,
					Result: ToolResult{Value: map[string]any{"error": subtaskForbiddenToolMessage(tname)}, IsError: true},
				}
				completed[idx] = true
				trace = append(trace, map[string]interface{}{"event": "tool_forbidden_in_subtask", "step": step, "tool": tname, "call_id": tc.ID})
				continue
			}
			// Research-cap: stop dispatching more network tools once the budget is exhausted.
			// The model still gets a tool result telling it to wrap up, so it does not
			// loop forever on search/fetch.
			isNetworkTool := tname == "web_search" || tname == "web_fetch" || tname == "image_search"
			if isNetworkTool && cfg.MaxNetworkCalls > 0 && networkCalls >= cfg.MaxNetworkCalls {
				capMsg := "\u641c\u7d22/\u6293\u53d6\u914d\u989d\u5df2\u7528\u5c3d\uff08\u6700\u591a " + fmt.Sprintf("%d", cfg.MaxNetworkCalls) + " \u6b21\uff09\uff0c\u8bf7\u57fa\u4e8e\u5df2\u83b7\u53d6\u7684\u8d44\u6599\u76f4\u63a5\u7ed9\u51fa\u7ed3\u8bba\uff0c\u4e0d\u8981\u518d\u8c03\u7528\u641c\u7d22/\u6293\u53d6\u5de5\u5177\u3002"
				cappedResults[idx] = ToolInvocationResult{
					Index:  idx,
					CallID: tc.ID,
					Name:   tname,
					Result: ToolResult{Value: map[string]any{"error": capMsg}, IsError: true},
				}
				completed[idx] = true
				trace = append(trace, map[string]interface{}{"event": "tool_capped", "step": step, "tool": tname, "call_id": tc.ID})
				continue
			}
			if isNetworkTool {
				networkCalls++
			}
			if requirePlanBeforeMoreReads && !hasPlanCall && isReadOnlyContextTool(tname) {
				message := "运行时要求：连续只读后必须先给出下一步计划。请调用 plan(action=update/mark) 写清已确认约束、下一步修改的文件与函数、验证方式，或直接调用 edit_file/write_file；在计划更新或实际修改前，read_file/grep/glob 已被冻结。"
				cappedResults[idx] = ToolInvocationResult{
					Index:  idx,
					CallID: tc.ID,
					Name:   tname,
					Result: ToolResult{Value: map[string]any{
						"tool":                 tname,
						"ok":                   false,
						"plan_required":        true,
						"error":                message,
						"required_next_action": "plan(action=update) 或 edit_file/write_file",
					}, IsError: true},
				}
				completed[idx] = true
				trace = append(trace, map[string]interface{}{
					"event": "read_plan_gate", "step": step, "tool": tname, "call_id": tc.ID,
				})
				emitEvent("status", map[string]interface{}{
					"step": step, "message": "只读阶段已结束；更新下一步计划后才能继续读取。",
				})
				continue
			}
			if tname == "read_file" {
				if key, ok := parseReadFileAttemptKey(tc.Function.Arguments); ok {
					pendingReadKeys[tc.ID] = key
					if shouldBlockRepeatedRead(readFileAttempts[key]) {
						message := fmt.Sprintf(
							"同一文件范围的 read_file 已成功执行 %d 次。该范围已被运行时拦截，不能再次读取。请基于已读内容收口：先更新 plan 或写出下一步计划，然后直接用 edit_file/write_file 修改；如需未读内容，请指定新的 offset/limit 或先说明明确缺口。",
							readFileAttempts[key],
						)
						cappedResults[idx] = ToolInvocationResult{
							Index:  idx,
							CallID: tc.ID,
							Name:   tname,
							Result: ToolResult{Value: map[string]any{
								"tool":          tname,
								"ok":            false,
								"repeated_read": true,
								"path":          key.Path,
								"read_count":    readFileAttempts[key],
								"error":         message,
								"next_action":   "更新 plan 后直接编辑；不要再读同一范围",
							}, IsError: true},
						}
						completed[idx] = true
						trace = append(trace, map[string]interface{}{
							"event": "read_file_guard", "step": step, "tool": tname,
							"call_id": tc.ID, "path": key.Path, "read_count": readFileAttempts[key],
						})
						emitEvent("status", map[string]interface{}{
							"step": step, "message": "检测到同一文件范围重复读取，已要求模型收口并进入修改...",
						})
						continue
					}
				}
			}
			// Approval gate: tools configured as "ask" are not executed without
			// user approval; the model receives an error result so it can
			// negotiate with the user or stop.
			if cfg.ToolNeedsApproval(tname) {
				emitEvent("tool_approval_required", map[string]interface{}{"tool": tname, "call_id": tc.ID, "arguments": tc.Function.Arguments, "step": step})
				cappedResults[idx] = ToolInvocationResult{
					Index:  idx,
					CallID: tc.ID,
					Name:   tname,
					Result: ToolResult{Value: map[string]any{"approval_required": true, "tool": tname, "call_id": tc.ID, "error": "工具需要用户审批，已跳过执行。请征得用户同意，或将 tool_runtime.approval_mode 设为 allow 后重试。"}, IsError: true},
				}
				completed[idx] = true
				trace = append(trace, map[string]interface{}{"event": "tool_approval_blocked", "step": step, "tool": tname, "call_id": tc.ID})
				continue
			}
			toolTimeout := time.Duration(cfg.API.TimeoutSec) * time.Second
			// Subtasks run a full child agent loop (deep research, file work);
			// give them a much longer budget than a normal tool call.
			if tname == "create_subtask" && toolTimeout < 3600*time.Second {
				toolTimeout = 3600 * time.Second
			}
			toolTimeout = cfg.ToolTimeout(tname, toolTimeout)
			metadata := map[string]string(nil)
			if isWebTextTool(tname) {
				metadata = webBudgetMetadata(perWebCallBudget)
			}
			invocations = append(invocations, ToolInvocation{
				Index:       idx,
				CallID:      tc.ID,
				Name:        tname,
				Args:        json.RawMessage(tc.Function.Arguments),
				Timeout:     toolTimeout,
				Workspace:   cfg.ResolvePath(cfg.WorkspaceDir),
				SessionFile: sessionFile,
				Metadata:    metadata,
			})
		}

		if snapshots := snapshotFilesForRollback(cfg, invocations); len(snapshots) > 0 {
			emitEvent("status", map[string]interface{}{"step": step, "message": "修改前已创建回退快照..."})
		}
		dispatchedResults := dispatcher.Execute(controller.Context(), invocations)
		results := make([]ToolInvocationResult, len(resp.ToolCalls))
		for idx, result := range cappedResults {
			if completed[idx] {
				results[idx] = result
			}
		}
		for _, result := range dispatchedResults {
			if result.Index >= 0 && result.Index < len(results) {
				results[result.Index] = result
				completed[result.Index] = true
			}
		}

		// Replay results in original order.
		stageBoundarySeen := false
		for _, r := range results {
			result := r.Result
			errorText := ""
			if r.Err != nil {
				errorText = r.Err.Error()
				result = ToolResult{Value: map[string]any{"error": errorText}, IsError: true}
			} else if result.IsError {
				if value, ok := result.Value["error"].(string); ok {
					errorText = value
				} else {
					errorText = "tool returned an error"
				}
			}
			resultBytes, marshalErr := result.JSON()
			if marshalErr != nil {
				errorText = marshalErr.Error()
				result = ToolResult{Value: map[string]any{"error": errorText}, IsError: true}
				resultBytes, _ = result.JSON()
			}
			memoryRecorder.RecordToolResult(step, r.Name, r.CallID, string(resultBytes))
			if result.IsError {
				emitData := map[string]interface{}{"tool": r.Name, "status": "end", "ok": false, "call_id": r.CallID, "error": errorText}
				if result.UpstreamCode != 0 {
					emitData["upstream_code"] = result.UpstreamCode
				}
				emitEvent("tool_call", emitData)
				traceData := map[string]interface{}{"event": "tool_failed", "step": step, "tool": r.Name, "err": errorText}
				if result.UpstreamCode != 0 {
					traceData["upstream_code"] = result.UpstreamCode
				}
				trace = append(trace, traceData)
				toolMsg := NewMessage("tool", string(resultBytes), nil, r.CallID, r.Name)
				toolMsg.Step = step
				toolMsg.Ts = time.Now().UnixMilli()
				memoryRecorder.TagMessage(&toolMsg)
				messages = append(messages, toolMsg)
				workingMsgs = append(workingMsgs, toolMsg)
				turnToolUsage[r.Name]++
				turnToolCalls++
				logEvent(map[string]any{"type": "tool_result", "tool": r.Name, "call_id": r.CallID, "content": truncateStr(string(resultBytes), 2000), "step": step})
				continue
			}
			// Graded protection: remember files read successfully so summary
			// compression cannot make the model forget it already read them.
			if r.Name == "read_file" {
				if p, ok := result.Value["path"].(string); ok && p != "" {
					readFiles[p] = true
				}
				if key, ok := pendingReadKeys[r.CallID]; ok {
					readFileAttempts[key]++
				}
			}
			if r.Name == "edit_file" || r.Name == "write_file" {
				readFileAttempts = map[readFileAttemptKey]int{}
				requirePlanBeforeMoreReads = false
			}
			if r.Name == "plan" {
				requirePlanBeforeMoreReads = false
			}
			toolMsg := NewMessage("tool", string(resultBytes), nil, r.CallID, r.Name)
			toolMsg.Step = step
			toolMsg.Ts = time.Now().UnixMilli()
			memoryRecorder.TagMessage(&toolMsg)
			messages = append(messages, toolMsg)
			workingMsgs = append(workingMsgs, toolMsg)
			if IsStageBoundaryTool(cfg, r.Name) {
				stageBoundarySeen = true
			}
			turnToolUsage[r.Name]++
			turnToolCalls++
			logEvent(map[string]any{"type": "tool_result", "tool": r.Name, "call_id": r.CallID, "content": truncateStr(string(resultBytes), 2000), "step": step})
			preview := string(resultBytes)
			if len(preview) > 200 {
				preview = preview[:200]
			}
			trace = append(trace, map[string]interface{}{"event": "tool_result", "step": step, "tool": r.Name, "ok": true, "content_preview": preview})
			emitEvent("tool_call", map[string]interface{}{"tool": r.Name, "status": "end", "ok": true, "call_id": r.CallID, "result": string(resultBytes), "result_preview": truncateStr(string(resultBytes), 120)})
		}
		if visionMessage, ok := toolVisionContextMessage(&cfg.API, results); ok {
			workingMsgs = append(workingMsgs, visionMessage)
			workingMsgs = trimVisionContextMessages(workingMsgs, cfg.API.VisionMaxImagesOrDefault())
			trace = append(trace, map[string]interface{}{
				"event":      "vision_attachment",
				"step":       step,
				"images":     contentPartImageCount(visionMessage.ContentParts),
				"max_images": cfg.API.VisionMaxImagesOrDefault(),
				"persisted":  false,
			})
		}

		// JEV-style rolling cleanup: every stage boundary re-scores all still-live
		// tool results using the conversation that followed each result. Scoring
		// runs in the background; the loop only applies completed replacements at
		// safe boundaries before the next model request.
		cleanupTokens := lastPromptTokens
		if estimatedTokens := estimateMessagesContextTokens(workingMsgs); estimatedTokens > cleanupTokens {
			cleanupTokens = estimatedTokens
		}
		cleanupTrigger := ""
		if stageBoundarySeen {
			cleanupTrigger = "stage_boundary"
		} else if cleanupTokens >= cfg.ToolScoreCleanup.TriggerTokens {
			cleanupTrigger = "tokens"
		}
		if ShouldRunToolScoreCleanup(cfg, stageBoundarySeen, cleanupTokens) {
			if candidates := CollectToolScoreCandidatesPinned(workingMsgs, cfg, pinnedPlanToolCallIDs(cfg, sessionFile)); len(candidates) > 0 {
				if pendingToolScoreCleanup == nil {
					pendingToolScoreCleanup = startToolScoreCleanupJob(controller.Context(), candidates, cfg, cleanupTrigger, step)
					trace = append(trace, map[string]interface{}{
						"event": "tool_score_cleanup", "step": step, "status": "scheduled",
						"trigger": cleanupTrigger, "scored": len(candidates), "async": true,
					})
				} else {
					cleanupRerunQueued = true
				}
			}
		}

		stepHadReadOnly := false
		stepHadProgress := false
		for _, r := range results {
			if r.Err != nil {
				continue
			}
			if isReadOnlyContextTool(r.Name) {
				stepHadReadOnly = true
			}
			if !r.Result.IsError && isExecutionProgressTool(r.Name) {
				stepHadProgress = true
			}
		}
		if stepHadProgress {
			readOnlyStreak = 0
			requirePlanBeforeMoreReads = false
		} else if stepHadReadOnly {
			readOnlyStreak++
		}
		if implementationTask && readOnlyStreak >= maxReadOnlyStepsBeforeProgress && executionGuards < maxExecutionGuardsPerRun {
			guard := executionGuardText(readOnlyStreak)
			guardMsg := newInternalControlMessage("user", guard, internalTypeExecutionGuard)
			guardMsg.Step = step
			guardMsg.Ts = time.Now().UnixMilli()
			workingMsgs = append(workingMsgs, guardMsg)
			executionGuards++
			requirePlanBeforeMoreReads = true
			readOnlyStreak = 0
			trace = append(trace, map[string]interface{}{
				"event": "execution_guard", "step": step, "reason": "read_only_streak",
				"guard_count": executionGuards,
			})
			logEvent(map[string]any{"type": "runtime_guard", "kind": "read_only_streak", "step": step})
			emitEvent("status", map[string]interface{}{
				"step": step, "message": "检测到长时间只读，已要求更新下一步计划并进入修改...",
			})
		}
		memoryRecorder.EndStep(step)

		// If any tool waits for user input (ask_user blocks loop), stop the turn.
		for _, r := range results {
			if r.Err != nil {
				continue
			}
			waitingReply := r.Result.WaitingReply
			if waiting, ok := r.Result.Value["waiting_reply"].(bool); ok {
				waitingReply = waitingReply || waiting
			}
			if !waitingReply {
				continue
			}

			// 无人值守/批处理模式：自动回答问题并继续执行，避免等待人工输入。
			// 自动生成的答案会作为普通 user 消息注入，使 LLM 轮次及 token 消耗
			// 与真实交互流程保持一致。
			if autoAnswerAskUser {
				askType, _ := r.Result.Value["ask_type"].(string)
				answer := "\u786e\u8ba4\uff0c\u8bf7\u7ee7\u7eed\u6267\u884c\u3002"
				if strings.Contains(askType, "confirm_outline") {
					answer = "\u5927\u7eb2\u786e\u8ba4\u901a\u8fc7\uff0c\u8bf7\u6309\u5f53\u524d\u5927\u7eb2\u7ee7\u7eed\u6267\u884c\u540e\u7eed\u6b65\u9aa4\u3002"
				} else if strings.Contains(askType, "confirm_params") {
					answer = "\u53c2\u6570\u786e\u8ba4\u901a\u8fc7\uff0c\u8bf7\u6309\u4e0a\u8ff0\u914d\u7f6e\u7ee7\u7eed\u6267\u884c\uff0c\u65e0\u9700\u518d\u786e\u8ba4\u3002"
				}
				answerMsg := newInternalControlMessage("user", answer, internalTypeAutoAnswer)
				memoryRecorder.TagMessage(&answerMsg)
				messages = append(messages, answerMsg)
				workingMsgs = append(workingMsgs, answerMsg)
				emitEvent("auto_answered", map[string]interface{}{
					"ask_type": askType,
					"answer":   answer,
				})
				trace = append(trace, map[string]interface{}{"event": "auto_answered", "step": step, "ask_type": askType})
				break
			}

			// qn/Claude 兼容模型有时会把数组参数 `questions` 序列化成 JSON 字符串，
			// 而不是数组。这里统一转换为数组，确保前端 AskModal 始终收到可用列表。
			askQ := r.Result.Value["questions"]
			if qs, isStr := askQ.(string); isStr {
				var arr []interface{}
				if json.Unmarshal([]byte(qs), &arr) == nil && len(arr) > 0 {
					askQ = arr
				} else {
					askQ = []interface{}{}
				}
			}
			emitEvent("waiting_user_input", map[string]interface{}{
				"ask_type":  r.Result.Value["ask_type"],
				"questions": askQ,
			})
			memoryRecorder.EndStep(step)
			trace = append(trace, map[string]interface{}{"event": "waiting_user_input", "step": step})
			logEvent(map[string]any{"type": "turn_end", "step": step, "tools_used": turnToolUsage, "tool_calls_total": turnToolCalls})
			return &AgentResult{
				Messages:        messages,
				Steps:           step,
				Usage:           sessionUsage,
				PerMessageUsage: perMsgUsage,
				Trace:           trace,
			}, nil
		}

	}

	// Generate title
	title := ""

	if generateTitlePrompt != "" {
		emitEvent("status", map[string]interface{}{"step": step, "message": "正在生成对话标题..."})
		var titleMsgs []Message
		for _, m := range workingMsgs {
			if m.Role == "system" || m.Role == "tool" {
				continue
			}
			if m.Role == "assistant" && m.Content == "" {
				continue
			}
			c := ""
			if m.Content != "" {
				c = m.Content
			}
			titleMsgs = append(titleMsgs, NewMessage(m.Role, c, nil, "", ""))
		}
		titleMsgs = append(titleMsgs, NewMessage("user", generateTitlePrompt, nil, "", ""))

		titleResp, err := CallConfiguredLLM(cfg, titleMsgs, nil)
		if err == nil && titleResp != nil && titleResp.Content != "" {
			title = strings.TrimSpace(titleResp.Content)
			if len(title) > 120 {
				title = title[:120]
			}
		}
	}

	logEvent(map[string]any{"type": "turn_end", "step": step, "tools_used": turnToolUsage, "tool_calls_total": turnToolCalls})
	return &AgentResult{
		Messages:        messages,
		Steps:           step,
		Usage:           sessionUsage,
		PerMessageUsage: perMsgUsage,
		Title:           title,
		Trace:           trace,
	}, nil
}

// sessionWriteMu serialises writes to a single session.json file so an
// injected turn saving its partial messages cannot race with the main loop
// finishing its own save. Per-file keys; one mutex per path.
var (
	sessionWriteMuMu sync.Mutex
	sessionWriteMu   = make(map[string]*sync.Mutex)
)

func lockForSessionFile(path string) *sync.Mutex {
	sessionWriteMuMu.Lock()
	defer sessionWriteMuMu.Unlock()
	mu, ok := sessionWriteMu[path]
	if !ok {
		mu = &sync.Mutex{}
		sessionWriteMu[path] = mu
	}
	return mu
}

// stripRequestContextPrefixes removes injected plan prefixes so the persisted
// transcript matches the original user input.
func stripRequestContextPrefixes(messages []Message) []Message {
	out := make([]Message, len(messages))
	copy(out, messages)
	for i := range out {
		if out[i].Role != "user" {
			continue
		}
		out[i].Content = stripRequestContextPrefix(out[i].Content)
		if !isSummaryMarker(out[i]) {
			out[i].Content = sanitizeUserInputProtocol(out[i].Content)
		}
	}
	return out
}

// stripSyntheticControlMessages removes machine-generated continuation turns
// from the persisted transcript. Typed messages are always removed. Legacy
// sessions are handled conservatively: the bracketed delivery note is reserved
// for agent control, while "请继续。" is only removed when it repeats inside an
// interaction that already has a real user request. A genuine new interaction
// whose sole prompt is "请继续。" is therefore preserved.
func stripSyntheticControlMessages(messages []Message) []Message {
	out := make([]Message, 0, len(messages))
	seenUserInteraction := map[string]bool{}
	for _, message := range messages {
		if message.Role == "user" {
			// A compaction checkpoint is machine state wearing a user role so
			// providers accept it. The user-input sanitiser would strip its
			// <summary> wrapper and then drop the message as empty - which is how
			// a compressed conversation could come back from a save with no
			// checkpoint at all and redo everything it had already done.
			if isSummaryMarker(message) {
				out = append(out, message)
				continue
			}
			content := sanitizeUserInputProtocol(message.Content)
			if content == "" {
				continue
			}
			message.Content = content
			interactionID := strings.TrimSpace(message.InteractionID)
			if isInternalControlMessage(message) {
				continue
			}
			if content == "请继续。" && interactionID != "" && seenUserInteraction[interactionID] {
				continue
			}
			if interactionID != "" {
				seenUserInteraction[interactionID] = true
			}
		}
		out = append(out, message)
	}
	return out
}

// SaveSession saves the session JSON file. Concurrent saves for the same
// path are serialised; saves for different paths run in parallel.
func SaveSession(sessionFile string, messages []Message, model string) error {
	dir := filepath.Dir(sessionFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// Strip the per-turn injected plan prefix so the persisted transcript
	// matches user input.
	persistedMessages := stripSyntheticControlMessages(stripRequestContextPrefixes(messages))
	session := map[string]interface{}{}
	if existing, readErr := os.ReadFile(sessionFile); readErr == nil {
		_ = json.Unmarshal(existing, &session)
	}
	session["messages"] = persistedMessages
	session["model"] = model
	// ????? session_id?httptool ?????????????????
	if id := httptool.LoadSessionID(sessionFile); id != "" {
		session["session_id"] = id
	}
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return err
	}

	mu := lockForSessionFile(sessionFile)
	mu.Lock()
	defer mu.Unlock()
	return os.WriteFile(sessionFile, data, 0644)
}

// SaveUsage saves the usage.json file
func SaveUsage(sessionFile string, usage *SessionUsage, messages []Message, perMsgUsage map[int]*PerMsgUsageEntry) error {
	if usage == nil {
		// Soft-stop / hard-cancel exits can return a result with no usage yet.
		// Don't panic; just skip the file.
		return nil
	}
	dir := filepath.Dir(sessionFile)
	usageFile := filepath.Join(dir, "usage.json")

	totalRealMs := int64(0)
	for _, m := range messages {
		if m.Role == "assistant" {
			totalRealMs += m.RealMs
		}
	}

	usageData := map[string]interface{}{
		"prompt_tokens":     usage.PromptTokens,
		"completion_tokens": usage.CompletionTokens,
		"duration_ms":       usage.DurationMs,
		"real_ms":           totalRealMs,
		"turns":             []interface{}{},
	}

	var turns []map[string]interface{}
	for i, m := range messages {
		if m.Role == "assistant" && m.Usage != nil {
			dur := m.DurationMs
			if dur == 0 {
				dur = 0
			}
			turnEntry := map[string]interface{}{
				"message_index":     i,
				"prompt_tokens":     m.Usage.PromptTokens,
				"completion_tokens": m.Usage.CompletionTokens,
				"duration_ms":       dur,
				"real_ms":           m.RealMs,
				"step":              m.Step,
			}
			if len(m.ToolsUsed) > 0 {
				turnEntry["tools_used"] = m.ToolsUsed
			}
			turns = append(turns, turnEntry)
		}
	}
	usageData["turns"] = turns

	data, err := json.MarshalIndent(usageData, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(usageFile, data, 0644)
}

// usageMessageKey identifies one logical model response. Summary checkpoints
// may persist a retained tail again in the display transcript, so aggregate
// metrics must not count those duplicated assistant messages as fresh calls.
func usageMessageKey(m Message) string {
	if m.Usage == nil {
		return ""
	}
	if m.Ts != 0 || m.Step != 0 {
		return fmt.Sprintf("%d:%d:%d:%d", m.Ts, m.Step, m.Usage.PromptTokens, m.Usage.CompletionTokens)
	}
	return fmt.Sprintf("%d:%d:%d:%d:%s", m.DurationMs, m.Usage.PromptTokens, m.Usage.CompletionTokens, len(m.Content), m.Content)
}

// extractInlineToolCalls parses functions.name({...}) blocks some providers emit
// as text, returning native ToolCalls plus the content with those blocks removed.
// <report> is a final-only result marker, so it is left untouched here for the
// host-side report artifact pipeline.
func extractInlineToolCalls(content string) ([]ToolCall, string) {
	var calls []ToolCall
	var cleaned strings.Builder
	remaining := content
	id := 0
	for {
		idx := strings.Index(remaining, "functions.")
		if idx < 0 {
			cleaned.WriteString(remaining)
			break
		}
		cleaned.WriteString(remaining[:idx])
		rest := remaining[idx+len("functions."):]
		endName := strings.IndexByte(rest, '(')
		if endName <= 0 {
			cleaned.WriteString("functions.")
			remaining = rest
			continue
		}
		name := strings.TrimSpace(rest[:endName])
		if name == "" {
			cleaned.WriteString("functions.")
			remaining = rest
			continue
		}
		closeIdx := findInlineCallClose(rest, endName+1)
		if closeIdx < 0 {
			cleaned.WriteString("functions.")
			remaining = rest
			continue
		}
		argsRaw := strings.TrimSpace(rest[endName+1 : closeIdx])
		argsRaw = normalizeInlineCallArguments(argsRaw)
		calls = append(calls, ToolCall{
			ID:       fmt.Sprintf("call_inline_%d", id),
			Type:     "function",
			Function: ToolCallFunc{Name: name, Arguments: argsRaw},
		})
		id++
		remaining = rest[closeIdx+1:]
	}
	return calls, cleaned.String()
}

// normalizeInlineCallArguments repairs raw control characters that some models
// place inside JSON strings, while leaving already-escaped sequences intact.
func normalizeInlineCallArguments(raw string) string {
	var builder strings.Builder
	inString := false
	escaped := false
	for _, r := range raw {
		if !inString {
			if r == '"' {
				inString = true
			}
			builder.WriteRune(r)
			continue
		}
		if escaped {
			escaped = false
			builder.WriteRune(r)
			continue
		}
		switch r {
		case '\\':
			escaped = true
			builder.WriteRune(r)
		case '"':
			inString = false
			builder.WriteRune(r)
		case '\n':
			builder.WriteString("\\n")
		case '\r':
			builder.WriteString("\\r")
		case '\t':
			builder.WriteString("\\t")
		default:
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func findInlineCallClose(rest string, open int) int {
	depth := 0
	inString := false
	escaped := false
	for i := open; i < len(rest); i++ {
		c := rest[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{', '(':
			depth++
		case '}':
			depth--
		case ')':
			if depth == 0 {
				return i
			}
			depth--
		}
	}
	return -1
}

// snapshotFilesForRollback copies files targeted by write/edit calls into
// runs/rollback/<timestamp>/ so the user can restore the pre-change state.
func snapshotFilesForRollback(cfg *Config, invocations []ToolInvocation) []string {
	if cfg == nil || cfg.RepoRoot == "" {
		return nil
	}
	var targets []string
	for _, invocation := range invocations {
		name := strings.ToLower(invocation.Name)
		if name != "write_file" && name != "edit_file" {
			continue
		}
		var args struct {
			FilePath string `json:"file_path"`
		}
		if err := json.Unmarshal(invocation.Args, &args); err != nil || strings.TrimSpace(args.FilePath) == "" {
			continue
		}
		absPath, err := resolveRollbackPath(cfg, args.FilePath)
		if err != nil {
			continue
		}
		info, statErr := os.Stat(absPath)
		if statErr != nil || info.IsDir() {
			continue
		}
		targets = append(targets, absPath)
	}
	if len(targets) == 0 {
		return nil
	}
	rollbackRoot := filepath.Join(cfg.RepoRoot, "runs", "rollback", time.Now().Format("20060102_150405"))
	manifest := make(map[string]string)
	for _, absPath := range targets {
		rel, relErr := filepath.Rel(cfg.RepoRoot, absPath)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			rel = strings.Trim(strings.ReplaceAll(absPath, ":", ""), string(filepath.Separator))
		}
		target := filepath.Join(rollbackRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			continue
		}
		data, err := os.ReadFile(absPath)
		if err != nil {
			continue
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			continue
		}
		manifest[absPath] = target
	}
	if len(manifest) > 0 {
		raw, _ := json.MarshalIndent(manifest, "", "  ")
		_ = os.WriteFile(filepath.Join(rollbackRoot, "manifest.json"), raw, 0o644)
	}
	keys := make([]string, 0, len(manifest))
	for key := range manifest {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func resolveRollbackPath(cfg *Config, requested string) (string, error) {
	lower := strings.ToLower(strings.TrimSpace(requested))
	raw := strings.TrimSpace(requested)
	if strings.HasPrefix(lower, "memory://") {
		raw = strings.TrimLeft(raw[len("memory://"):], "/")
		root := cfg.ResolvePath(cfg.MemoryDir)
		return resolveWithinRollbackRoot(root, raw)
	}
	for _, prefix := range []string{"local://", "knowledge://"} {
		if strings.HasPrefix(lower, prefix) {
			raw = raw[len(prefix):]
			break
		}
	}
	raw = filepath.FromSlash(raw)
	if !filepath.IsAbs(raw) {
		raw = filepath.Join(cfg.ResolvePath(cfg.WorkspaceDir), raw)
	}
	return filepath.Abs(raw)
}

func resolveWithinRollbackRoot(root, requested string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("memory root is not configured")
	}
	requested = filepath.FromSlash(strings.TrimSpace(requested))
	if filepath.IsAbs(requested) {
		return "", fmt.Errorf("path %q is outside rollback root", requested)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	full := filepath.Clean(filepath.Join(absRoot, requested))
	rel, err := filepath.Rel(absRoot, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside rollback root", requested)
	}
	return full, nil
}

const (
	tutorialNudgeText   = "你刚才只给了说明，没有实际动手。请先读取相关文件并直接修改验证，禁止只给方案/示例。"
	reflectionNudgeText = "请根据以上反思，给出你的最终回答。"
)

// shouldNudgeToExecute detects an actionable change request answered with a
// generic tutorial instead of tool calls, so the loop can force another pass.
func shouldNudgeToExecute(messages []Message, content string, toolsUsedThisTurn int) bool {
	lastUser := ""
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			lastUser = messages[i].Content
			break
		}
	}
	if lastUser == "" {
		return false
	}
	lower := strings.ToLower(lastUser)
	actionable := false
	for _, keyword := range []string{"修改", "改成", "修复", "添加", "删除", "创建"} {
		if strings.Contains(lower, keyword) {
			actionable = true
			break
		}
	}
	if !actionable && strings.Contains(lower, "实现") && (strings.Contains(lower, "项目") || strings.Contains(lower, "文件")) {
		actionable = true
	}
	if !actionable {
		return false
	}
	if strings.Contains(lower, "解释") || strings.Contains(lower, "说明") || strings.Contains(lower, "怎么做") {
		return false
	}
	if toolsUsedThisTurn == 0 && looksLikeExecutionAnnouncement(content) {
		return true
	}
	return looksLikeGenericTutorial(content)
}

func looksLikeExecutionAnnouncement(content string) bool {
	lower := strings.ToLower(content)
	if strings.TrimSpace(lower) == "" {
		return false
	}
	for _, marker := range []string{"已完成", "已修复", "已修改", "已添加", "已删除", "已创建", "已通过", "completed", "finished"} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	for _, marker := range []string{
		"开始实施", "开始修改", "开始改", "开始动手", "开始处理",
		"现在开始", "先 update plan", "再下代码", "接下来我", "接下来会",
		"下一步", "我会先", "我先改", "准备修改", "准备实施",
		"start implementing", "let me implement", "i'll implement", "i will implement",
		"next, i'll", "next, i will",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func looksLikeGenericTutorial(content string) bool {
	lower := strings.ToLower(content)
	markers := []string{"通常需要", "假设前提", "以 react", "以 vue", "以 angular", "示例代码", "教程", "让用户自己去", "你可以按照", "需要在前端代码"}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return len(content) > 400 && strings.Contains(content, "```")
}

// truncateStr truncates a string to maxLen chars
func truncateStr(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

func NewMessageMap(role, content string, toolCalls []ToolCall, toolCallID, name string) Message {
	return NewMessage(role, content, toolCalls, toolCallID, name)
}

// assistantMessageFromResponse builds the conversation entry for a model reply.
//
// Reasoning models require an assistant turn's reasoning_content to be sent back
// alongside it. Building these messages by hand in more than one place is how one
// of them ended up without it: the final-answer recovery path appended an
// assistant message that carried the content but not the reasoning, and the next
// API call was rejected with "The `reasoning_content` in the thinking mode must be
// passed back to the API" - killing the run at step 20.
//
// Routing every construction from an APIResponse through here keeps the field
// from being forgotten again.
func assistantMessageFromResponse(resp *APIResponse, toolCalls []ToolCall, step int) Message {
	m := NewMessage("assistant", resp.Content, toolCalls, "", "")
	m.ReasoningContent = resp.ReasoningContent
	m.Usage = resp.Usage
	m.DurationMs = resp.DurationMs
	m.Step = step
	m.Ts = time.Now().UnixMilli()
	return m
}

// extractReportTitle returns the first "# heading" inside a <report> block,
// else the first non-empty line (truncated). Empty if content has no report.
func extractReportTitle(content string) string {
	i := strings.Index(content, "<report>")
	if i < 0 {
		return ""
	}
	rest := content[i+len("<report>"):]
	for _, ln := range strings.Split(rest, "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(ln, "# "))
		}
		if ln != "" {
			return truncateStr(ln, 40)
		}
	}
	return ""
}

// addUnique appends item to list if non-empty and not already present.
func addUnique(list []string, item string) []string {
	if item == "" {
		return list
	}
	for _, x := range list {
		if x == item {
			return list
		}
	}
	return append(list, item)
}

// deliveredStatusText builds a machine-generated handoff note telling later
// turns which deliverables are already completed (so they are never redone).
func deliveredStatusText(items []string) string {
	return "[会话状态] 以下内容已完成并交付，后续不得重复执行：\n- " + strings.Join(items, "\n- ") + "\n仅处理用户新提出的请求。"
}

// toolDefName extracts the function name from a tool schema definition.
func toolDefName(def ToolDef) string {
	function, ok := def.Function.(map[string]any)
	if !ok {
		return ""
	}
	name, _ := function["name"].(string)
	return name
}

func subtaskForbiddenToolSet(cfg *Config) map[string]bool {
	forbidden := make(map[string]bool)
	if cfg == nil {
		return forbidden
	}
	for _, name := range cfg.SubtaskForbiddenTools {
		if name = strings.TrimSpace(name); name != "" {
			forbidden[name] = true
		}
	}
	return forbidden
}

func filterSubtaskToolDefs(toolDefs []ToolDef, forbidden map[string]bool) []ToolDef {
	if len(forbidden) == 0 {
		return toolDefs
	}
	filtered := make([]ToolDef, 0, len(toolDefs))
	for _, def := range toolDefs {
		if !forbidden[toolDefName(def)] {
			filtered = append(filtered, def)
		}
	}
	return filtered
}

func subtaskForbiddenToolMessage(name string) string {
	return fmt.Sprintf("%s is unavailable in a subtask. Finish this bounded work package with the available tools and return a subtask_result; do not delegate or wait for user input.", name)
}

func compactContextText(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}

// buildExtractiveSummary reduces a bounded execution history to recent user
// intent, tool outcomes and assistant conclusions, so a subtask that ran out of
// budget can still hand back an evidence-based result instead of nothing.
// buildExtractiveSummary is the detailed variant: subtask finalisation wants the
// tool trail, with names and results, so the hand-off to the model describes what
// actually ran.
func buildExtractiveSummary(messages []Message) string {
	return buildExtractiveSummaryWith(messages, false)
}

// buildCompactExtractiveSummary is the brief variant used when a compaction has
// to fall back to a mechanical handover: short, labelled, and explicitly marked
// as finished history, because a long recital of old conclusions is what makes
// the next turn pick the old work back up.
func buildCompactExtractiveSummary(messages []Message) string {
	return buildExtractiveSummaryWith(messages, true)
}

func buildExtractiveSummaryWith(messages []Message, brief bool) string {
	parts := make([]string, 0, 3)
	users := make([]string, 0, 8)
	summaries := make([]string, 0, 3)
	assistants := make([]string, 0, 3)
	tools := make([]string, 0, 6)
	seenPaths := make(map[string]bool, 6)

	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		switch message.Role {
		case "user":
			if isSummaryMarker(message) {
				if text := normalizeSummaryText(message.Content); text != "" && len(summaries) < 3 {
					summaries = append(summaries, compactContextText(text, 800))
				}
				for _, request := range summaryHistoricalUserRequests(message.Content) {
					if len(users) >= 8 {
						break
					}
					users = append(users, request)
				}
				continue
			}
			if isInternalControlMessage(message) {
				continue
			}
			limit, width := 8, 800
			if brief {
				limit, width = 3, 160
			}
			if text := strings.TrimSpace(message.Content); text != "" && len(users) < limit {
				users = append(users, compactContextText(text, width))
			}
		case "assistant":
			limit, width := 3, 1000
			if brief {
				limit, width = 2, 200
			}
			if text := strings.TrimSpace(message.Content); text != "" && len(assistants) < limit {
				assistants = append(assistants, compactContextText(text, width))
			}
		case "tool":
			if text := strings.TrimSpace(message.Content); text != "" && len(tools) < 6 {
				if brief {
					if file := firstPathInText(text); file != "" && !seenPaths[file] {
						seenPaths[file] = true
						tools = append(tools, file)
					}
					continue
				}
				label := message.Name
				if label == "" {
					label = "tool"
				}
				tools = append(tools, label+": "+compactContextText(text, 1000))
				}
		}
	}
	reverse := func(values []string) {
		for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
			values[left], values[right] = values[right], values[left]
		}
	}
	reverse(users)
	reverse(summaries)
	reverse(assistants)
	reverse(tools)
	if len(summaries) > 0 {
		parts = append(parts, "历史压缩摘要:\n- "+strings.Join(summaries, "\n- "))
	}
	// The mechanical handover wears the same labels the model-written one is
	// required to use, in the same order, so the next turn reads one shape either
	// way. It only claims what it can actually see: conclusions from assistant
	// turns, files from tool results, requests under [历史要点]. [当前请求] is
	// prepended by the caller; the one label it cannot fill says so instead of
	// inventing a decision.
	if len(assistants) > 0 {
		if brief {
		// Marked, dated, and short. The mechanical handover used to paste old
		// conclusions whole; a model reading that recites the old work and then
		// carries on with it, which is exactly what the user sees as "it repeated
		// what we did before and then went back to it".
			parts = append(parts, "[已完成]\n- 以下为历史结论摘录（已结束，不要接着做）：\n  - "+strings.Join(assistants, "\n  - "))
		} else {
			parts = append(parts, "[已完成]\n- "+strings.Join(assistants, "\n- "))
		}
	}
	if len(tools) > 0 {
		if brief {
			parts = append(parts, "[关键文件]\n- 历史产物路径（已存在，必要时再读）：\n  - "+strings.Join(tools, "\n  - "))
		} else {
			parts = append(parts, "[关键文件]\n- "+strings.Join(tools, "\n- "))
		}
	}
	if len(users) > 0 {
		if brief {
			parts = append(parts, "[历史要点]\n- 用户早前说过（不是本轮任务）：\n  - "+strings.Join(users, "\n  - "))
		} else {
			parts = append(parts, "[历史要点]\n- "+strings.Join(users, "\n- "))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	if brief {
		parts = append(parts, "[决策与约束]\n- 无（机械兜底不判断，规则以用户原话为准）")
	}
	return strings.Join(parts, "\n\n")
}

// firstPathInText finds a file path in a tool result so the mechanical handover
// can list artifacts by path instead of pasting the result. Paths are the one
// thing a later turn genuinely needs from an old tool output; the rest is
// context that has already been acted on.
func firstPathInText(text string) string {
	for _, field := range strings.FieldsFunc(text, func(r rune) bool {
		return r == '"' || r == '\'' || r == ' ' || r == '\n' || r == '\t' || r == ',' || r == '(' || r == ')'
	}) {
		candidate := strings.Trim(field, "\"'`,;:[]{}")
		if !strings.Contains(candidate, "/") || utf8.RuneCountInString(candidate) < 6 {
			continue
		}
		if strings.HasPrefix(candidate, "http://") || strings.HasPrefix(candidate, "https://") {
			continue
		}
		return truncateStr(candidate, 90)
	}
	return ""
}

func renderSubtaskFinalizePrompt(template, originalTask string, messages []Message) string {
	contextSummary := truncateStr(buildExtractiveSummary(messages), 12000)
	if strings.TrimSpace(template) == "" {
		return ""
	}
	rendered, err := renderJinja(template, map[string]any{
		"AgentType":    "子任务",
		"OriginalTask": originalTask,
		"Context":      contextSummary,
	})
	if err != nil || strings.TrimSpace(rendered) == "" {
		return ""
	}
	return rendered
}

// subtaskFinalizeMessages deliberately starts a fresh no-tool conversation: the
// rendered instruction already carries the bounded execution summary, and
// replaying the full history would duplicate context and risk odd tool pairing.
func subtaskFinalizeMessages(instruction string) []Message {
	return []Message{NewMessage("user", instruction, nil, "", "")}
}
