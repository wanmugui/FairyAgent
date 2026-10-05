package main

import (
	"encoding/json"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"agentloop/agent/internal/biz/tool/builtin"
)

// planTagOpen / planTagClose wrap the per-turn plan snapshot. The plan is
// Fairy's single execution and progress state.
const (
	planTagOpen  = "<plan>"
	planTagClose = "</plan>\n"
)

// planItemForLLM is the compact view of a plan item injected into the prompt.
// Finished items are omitted: the model only needs what is still ahead.
type planItemForLLM struct {
	ID              string   `json:"id"`
	Status          string   `json:"status"`
	Action          string   `json:"action"`
	DoneWhen        string   `json:"done_when,omitempty"`
	AuditStatus     string   `json:"audit_status,omitempty"`
	AuditDegraded   bool     `json:"audit_degraded,omitempty"`
	EvidenceRefs    []string `json:"evidence_refs,omitempty"`
	EvidenceSummary []string `json:"evidence_summary,omitempty"`
}

type planPrefixPayload struct {
	Question string           `json:"question,omitempty"`
	Items    []planItemForLLM `json:"items"`
}

var planContinuationCues = []string{
	"继续", "接着", "下一步", "按计划", "照计划", "保持计划", "完成剩余",
	"continue", "resume", "go on", "next step", "keep going",
}

var planTokenStopWords = map[string]struct{}{
	"任务": {}, "继续": {}, "完成": {}, "执行": {}, "处理": {}, "检查": {},
	"查看": {}, "修改": {}, "更新": {}, "分析": {}, "整理": {}, "生成": {},
	"创建": {}, "使用": {}, "需要": {}, "当前": {}, "这个": {}, "一个": {},
	"进行": {}, "相关": {}, "工作": {}, "计划": {}, "步骤": {},
	"the": {}, "and": {}, "for": {}, "with": {}, "this": {}, "that": {},
	"task": {}, "plan": {}, "work": {}, "continue": {}, "keep": {}, "going": {},
}

// buildPlanPrefix renders the current session plan as a request-context prefix.
// A plan is injected only when the new request is clearly related to it or the
// user explicitly asks to continue it, so stale plans cannot leak into a new
// topic.
func buildPlanPrefix(cfg *Config, userMessage string) string {
	if cfg == nil {
		return ""
	}
	planFile := builtin.PlanFilePathFor(cfg.ResolvePath(cfg.WorkspaceDir), sessionTodoFile())
	if planFile == "" {
		return ""
	}
	raw, err := os.ReadFile(planFile)
	if err != nil {
		return ""
	}
	var doc planPrefixPayload
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ""
	}
	open := make([]planItemForLLM, 0, len(doc.Items))
	for _, item := range doc.Items {
		switch strings.ToLower(strings.TrimSpace(item.Status)) {
		case "done", "skipped":
			continue
		}
		open = append(open, item)
	}
	if len(open) == 0 {
		return ""
	}
	if !planRelatesToMessage(doc.Question, open, userMessage) {
		return ""
	}
	payload, err := json.Marshal(planPrefixPayload{Question: strings.TrimSpace(doc.Question), Items: open})
	if err != nil {
		return ""
	}
	return planTagOpen + string(payload) + planTagClose
}

func planRelatesToMessage(question string, items []planItemForLLM, userMessage string) bool {
	normalized := normalizePlanMatchText(userMessage)
	if normalized == "" {
		return false
	}
	// Runtime continuation events are not user requests. They resume an
	// existing skill/plan, so the active plan must always be visible even when
	// the injected text is longer than the normal continuation-cue window.
	if strings.HasPrefix(normalized, "[会话状态]") {
		return true
	}
	if isPlanContinuationRequest(normalized) {
		return true
	}
	userTokens := planMatchTokens(normalized)
	if len(userTokens) == 0 {
		return false
	}
	var planText strings.Builder
	planText.WriteString(question)
	for _, item := range items {
		planText.WriteByte(' ')
		planText.WriteString(item.Action)
		planText.WriteByte(' ')
		planText.WriteString(item.DoneWhen)
	}
	planTokens := planMatchTokens(planText.String())
	for token := range userTokens {
		if _, ok := planTokens[token]; ok {
			return true
		}
	}
	return false
}

func normalizePlanMatchText(text string) string {
	text = strings.ToLower(strings.TrimSpace(stripRequestContextPrefix(text)))
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) > 12000 {
		text = string(runes[:12000])
	}
	return text
}

func isPlanContinuationRequest(normalized string) bool {
	if normalized == "" || utf8.RuneCountInString(normalized) > 40 {
		return false
	}
	for _, cue := range planContinuationCues {
		if strings.Contains(normalized, cue) {
			return true
		}
	}
	return false
}

func planMatchTokens(text string) map[string]struct{} {
	tokens := make(map[string]struct{})
	var word []rune
	var han []rune
	addToken := func(token string) {
		token = strings.TrimSpace(token)
		if utf8.RuneCountInString(token) < 2 {
			return
		}
		if _, stop := planTokenStopWords[token]; stop {
			return
		}
		tokens[token] = struct{}{}
	}
	flushWord := func() {
		if len(word) > 0 {
			addToken(string(word))
			word = word[:0]
		}
	}
	flushHan := func() {
		for i := 0; i+1 < len(han); i++ {
			addToken(string(han[i : i+2]))
		}
		han = han[:0]
	}
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r):
			flushWord()
			han = append(han, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushHan()
			word = append(word, unicode.ToLower(r))
		default:
			flushWord()
			flushHan()
		}
	}
	flushWord()
	flushHan()
	return tokens
}
