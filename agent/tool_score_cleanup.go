package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ToolScoreCleanupConfig drives classifier-scored cleanup of tool results.
//
// The classifier scores how likely a tool result is no longer needed. Cleanup
// is a threshold action, never a free-form per-item judgement. The classifier
// receives the conversation after each result, which is the signal that decides
// whether the result still matters at the current stage boundary.
type ToolScoreCleanupConfig struct {
	Enabled                bool     `json:"enabled"`
	Endpoint               string   `json:"endpoint"`
	Tier                   string   `json:"tier"`
	Threshold              float64  `json:"threshold"`
	TriggerTokens          int      `json:"trigger_tokens"`
	MinRunes               int      `json:"min_runes"`
	MaxCandidates          int      `json:"max_candidates"` // <0 = unlimited
	TriggerOnStageBoundary *bool    `json:"trigger_on_stage_boundary"`
	StageBoundaryTools     []string `json:"stage_boundary_tools"`
	MaxFollowUpRunes       int      `json:"max_follow_up_runes"`
	TimeoutSec             int      `json:"timeout_sec"`
	FailOpen               bool     `json:"fail_open"`
	ExcludeTools           []string `json:"exclude_tools"`
	Placeholder            string   `json:"placeholder"`
}

const (
	labelScoreKeep  = "应该保留：后续对话还会用到这条工具结果"
	labelScoreClear = "可以清理：后续对话不再需要这条结果，可以只留一行占位"

	defaultScoreEndpoint         = "https://classifier.dev/"
	defaultScoreTier             = "fast"
	defaultScoreThreshold        = 0.70
	defaultScoreMinRunes         = 400
	defaultScoreMaxCands         = 30
	defaultScoreMaxFollowUp      = 800
	defaultScoreTimeoutSec       = 30
	defaultScoreMarker           = "[工具结果已按分类器评分清理]"
	defaultScorePlaceholder      = "[工具结果已按分类器评分清理] %[1]s%[2]s 共 %[3]d 字符（评分 %[4]s）；需要该内容时请重新调用 %[1]s。"
	toolScoreCleanedInternalType = "tool_score_cleaned"
)

// ApplyDefaults fills unset knobs. SummaryThreshold derives the token fallback.
func (c *ToolScoreCleanupConfig) ApplyDefaults(summaryThreshold int) {
	if c.Endpoint == "" {
		c.Endpoint = defaultScoreEndpoint
	}
	if c.Tier == "" {
		c.Tier = defaultScoreTier
	}
	if c.Threshold <= 0 {
		c.Threshold = defaultScoreThreshold
	}
	if c.MinRunes <= 0 {
		c.MinRunes = defaultScoreMinRunes
	}
	if c.MaxCandidates == 0 {
		c.MaxCandidates = defaultScoreMaxCands
	}
	if c.TriggerOnStageBoundary == nil {
		on := true
		c.TriggerOnStageBoundary = &on
	}
	if len(c.StageBoundaryTools) == 0 {
		c.StageBoundaryTools = []string{"create_subtask"}
	}
	if c.MaxFollowUpRunes <= 0 {
		c.MaxFollowUpRunes = defaultScoreMaxFollowUp
	}
	if c.TimeoutSec <= 0 {
		c.TimeoutSec = defaultScoreTimeoutSec
	}
	if c.TriggerTokens <= 0 {
		c.TriggerTokens = summaryThreshold / 2
		if c.TriggerTokens <= 0 {
			c.TriggerTokens = 30000
		}
	}
	if c.Placeholder == "" {
		c.Placeholder = defaultScorePlaceholder
	}
	if len(c.ExcludeTools) == 0 {
		c.ExcludeTools = []string{"ask_user", "create_subtask", "plan"}
	}
}

// ToolScoreCandidate is one tool result that may be cleaned.
type ToolScoreCandidate struct {
	Index    int
	CallID   string
	Name     string
	Args     string
	Result   string
	Runes    int
	FollowUp string
}

type classifierScoredResult struct {
	Label      string             `json:"label"`
	Confidence float64            `json:"confidence"`
	Scores     map[string]float64 `json:"scores"`
}

type classifierScoreResponse struct {
	Results []classifierScoredResult `json:"results"`
	Model   string                   `json:"model"`
}

// CollectToolScoreCandidates returns every still-live tool result that is old
// enough and large enough to score. Cleaned and excluded payloads are omitted,
// so later boundaries only re-score the survivors plus newly produced results.
func CollectToolScoreCandidates(msgs []Message, cfg *Config) []ToolScoreCandidate {
	return CollectToolScoreCandidatesPinned(msgs, cfg, nil)
}

// CollectToolScoreCandidatesPinned is the evidence-aware form. Pinned call IDs
// are never returned as cleanup candidates, even when the context classifier
// considers the result no longer useful for the immediate conversation.
func CollectToolScoreCandidatesPinned(msgs []Message, cfg *Config, pinned map[string]bool) []ToolScoreCandidate {
	if cfg == nil || !cfg.ToolScoreCleanup.Enabled {
		return nil
	}
	p := cfg.ToolScoreCleanup
	excluded := make(map[string]bool, len(p.ExcludeTools))
	for _, name := range p.ExcludeTools {
		excluded[name] = true
	}

	argsByCallID := make(map[string]string)
	for i := range msgs {
		for _, tc := range msgs[i].ToolCalls {
			argsByCallID[tc.ID] = tc.Function.Arguments
		}
	}

	toolIdx := make([]int, 0, len(msgs))
	for i := range msgs {
		if msgs[i].Role == "tool" {
			toolIdx = append(toolIdx, i)
		}
	}
	if len(toolIdx) == 0 {
		return nil
	}

	candidates := make([]ToolScoreCandidate, 0, len(toolIdx))
	for _, idx := range toolIdx {
		m := msgs[idx]
		if pinned != nil && m.ToolCallID != "" && pinned[m.ToolCallID] {
			continue
		}
		if isProtectedToolScoreResult(m.Name, argsByCallID[m.ToolCallID]) {
			continue
		}
		if excluded[m.Name] {
			continue
		}
		if m.InternalType == toolScoreCleanedInternalType || strings.HasPrefix(m.Content, defaultScoreMarker) {
			continue
		}
		runes := len([]rune(m.Content))
		if runes < p.MinRunes {
			continue
		}
		candidates = append(candidates, ToolScoreCandidate{
			Index:    idx,
			CallID:   m.ToolCallID,
			Name:     m.Name,
			Args:     argsByCallID[m.ToolCallID],
			Result:   m.Content,
			Runes:    runes,
			FollowUp: followUpText(msgs, idx, p.MaxFollowUpRunes),
		})
	}
	if p.MaxCandidates > 0 && len(candidates) > p.MaxCandidates {
		candidates = candidates[:p.MaxCandidates]
	}
	return candidates
}

// followUpText renders the conversation after candidate index idx so the
// classifier can tell whether that result was still referenced afterwards.
func followUpText(msgs []Message, idx, limit int) string {
	var b strings.Builder
	for i := idx + 1; i < len(msgs); i++ {
		m := msgs[i]
		if m.Content != "" {
			b.WriteString(m.Role)
			b.WriteString(": ")
			b.WriteString(truncateToolScoreRunes(strings.ReplaceAll(m.Content, "\n", " "), 240))
			b.WriteString("\n")
		}
		for _, tc := range m.ToolCalls {
			b.WriteString("assistant->tool: ")
			b.WriteString(tc.Function.Name)
			b.WriteString(" ")
			b.WriteString(truncateToolScoreRunes(tc.Function.Arguments, 160))
			b.WriteString("\n")
		}
		if len([]rune(b.String())) >= limit {
			break
		}
	}
	out := []rune(b.String())
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return string(out)
}

// ScoreToolCandidates scores candidates with the classifier. The returned
// slice is parallel to cands and holds the "clear" probability.
func ScoreToolCandidates(cands []ToolScoreCandidate, cfg *Config) ([]float64, error) {
	return ScoreToolCandidatesContext(context.Background(), cands, cfg)
}

// ScoreToolCandidatesContext is the cancellable variant used by asynchronous
// cleanup so a finished agent run does not leave a classifier request behind.
func ScoreToolCandidatesContext(ctx context.Context, cands []ToolScoreCandidate, cfg *Config) ([]float64, error) {
	if cfg == nil || len(cands) == 0 {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	p := cfg.ToolScoreCleanup
	inputs := make([]string, 0, len(cands))
	for _, c := range cands {
		inputs = append(inputs, fmt.Sprintf(
			"工具: %s\n参数: %s\n结果长度: %d 字符\n结果开头: %s\n== 该结果之后的对话（判断是否还需要它）==\n%s",
			c.Name, truncateToolScoreRunes(c.Args, 200), c.Runes,
			truncateToolScoreRunes(strings.ReplaceAll(c.Result, "\n", " "), 200), c.FollowUp))
	}
	payload := map[string]any{
		"inputs": inputs,
		"labels": []string{labelScoreKeep, labelScoreClear},
		"tier":   p.Tier,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: time.Duration(p.TimeoutSec) * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("classifier http %d: %s", resp.StatusCode, truncateToolScoreRunes(string(raw), 200))
	}
	var parsed classifierScoreResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("classifier decode: %w", err)
	}
	if len(parsed.Results) != len(cands) {
		return nil, fmt.Errorf("classifier returned %d results for %d inputs", len(parsed.Results), len(cands))
	}
	scores := make([]float64, len(cands))
	for i, r := range parsed.Results {
		if v, ok := r.Scores[labelScoreClear]; ok {
			scores[i] = v
			continue
		}
		for label, v := range r.Scores {
			if strings.HasPrefix(label, "可以清理") {
				scores[i] = v
			}
		}
		if scores[i] == 0 && strings.HasPrefix(r.Label, "可以清理") {
			scores[i] = r.Confidence
		}
	}
	return scores, nil
}

// CleanedToolResult reports one cleaned payload for tracing.
type CleanedToolResult struct {
	CallID  string  `json:"call_id"`
	Name    string  `json:"name"`
	Runes   int     `json:"runes"`
	Score   float64 `json:"score"`
	Content string  `json:"content,omitempty"`
}

// ApplyToolScoreCleanup replaces payloads whose score passes the threshold.
// Tool pairing fields stay intact so the request remains protocol-valid; the
// full transcript retained for UI/trace is untouched.
func ApplyToolScoreCleanup(msgs []Message, cands []ToolScoreCandidate, scores []float64, cfg *Config) (cleared, savedRunes int, cleaned []CleanedToolResult) {
	if cfg == nil || len(cands) != len(scores) {
		return 0, 0, nil
	}
	p := cfg.ToolScoreCleanup
	for i, c := range cands {
		if scores[i] < p.Threshold {
			continue
		}
		if isProtectedToolScoreResult(c.Name, c.Args) {
			continue
		}
		if c.Index < 0 || c.Index >= len(msgs) {
			continue
		}
		placeholder := fmt.Sprintf(p.Placeholder, c.Name, scorePathHint(c.Args), c.Runes, strconv.FormatFloat(scores[i], 'f', 2, 64))
		before := len([]rune(msgs[c.Index].Content))
		if len([]rune(placeholder)) >= before {
			continue
		}
		msgs[c.Index].Content = placeholder
		msgs[c.Index].InternalType = toolScoreCleanedInternalType
		cleared++
		savedRunes += before - len([]rune(placeholder))
		cleaned = append(cleaned, CleanedToolResult{
			CallID: c.CallID, Name: c.Name, Runes: before, Score: scores[i], Content: placeholder,
		})
	}
	sort.Slice(cleaned, func(i, j int) bool { return cleaned[i].Score > cleaned[j].Score })
	return cleared, savedRunes, cleaned
}

// BuildToolScoreCleanupReplacements is the asynchronous-safe form of
// ApplyToolScoreCleanup: it builds call_id-addressed replacements without
// retaining indices into a slice that may grow while the classifier runs.
func BuildToolScoreCleanupReplacements(cands []ToolScoreCandidate, scores []float64, cfg *Config) ([]CleanedToolResult, int) {
	if cfg == nil || len(cands) != len(scores) {
		return nil, 0
	}
	p := cfg.ToolScoreCleanup
	cleaned := make([]CleanedToolResult, 0, len(cands))
	savedRunes := 0
	for i, c := range cands {
		if scores[i] < p.Threshold {
			continue
		}
		if isProtectedToolScoreResult(c.Name, c.Args) {
			continue
		}
		placeholder := fmt.Sprintf(p.Placeholder, c.Name, scorePathHint(c.Args), c.Runes, strconv.FormatFloat(scores[i], 'f', 2, 64))
		if len([]rune(placeholder)) >= c.Runes {
			continue
		}
		savedRunes += c.Runes - len([]rune(placeholder))
		cleaned = append(cleaned, CleanedToolResult{
			CallID: c.CallID, Name: c.Name, Runes: c.Runes, Score: scores[i], Content: placeholder,
		})
	}
	sort.Slice(cleaned, func(i, j int) bool { return cleaned[i].Score > cleaned[j].Score })
	return cleaned, savedRunes
}

// ApplyToolScoreCleanupByCallID applies replacements after asynchronous
// scoring. It updates both the model-visible working history and the durable
// transcript so a refresh shows the same cleaned tool result as the live UI.
func ApplyToolScoreCleanupByCallID(msgs []Message, replacements []CleanedToolResult) (cleared, savedRunes int, applied []CleanedToolResult) {
	if len(msgs) == 0 || len(replacements) == 0 {
		return 0, 0, nil
	}
	byCallID := make(map[string]CleanedToolResult, len(replacements))
	for _, replacement := range replacements {
		if strings.TrimSpace(replacement.CallID) == "" || strings.TrimSpace(replacement.Content) == "" {
			continue
		}
		byCallID[replacement.CallID] = replacement
	}
	if len(byCallID) == 0 {
		return 0, 0, nil
	}
	used := make(map[string]bool, len(byCallID))
	for i := range msgs {
		if msgs[i].Role != "tool" {
			continue
		}
		if isProtectedToolScoreResult(msgs[i].Name, "") {
			continue
		}
		if msgs[i].InternalType == toolScoreCleanedInternalType || strings.HasPrefix(msgs[i].Content, defaultScoreMarker) {
			continue
		}
		replacement, ok := byCallID[msgs[i].ToolCallID]
		if !ok || used[msgs[i].ToolCallID] {
			continue
		}
		before := len([]rune(msgs[i].Content))
		if len([]rune(replacement.Content)) >= before {
			continue
		}
		msgs[i].Content = replacement.Content
		msgs[i].InternalType = toolScoreCleanedInternalType
		used[msgs[i].ToolCallID] = true
		cleared++
		savedRunes += before - len([]rune(replacement.Content))
		applied = append(applied, CleanedToolResult{
			CallID: replacement.CallID, Name: replacement.Name, Runes: before,
			Score: replacement.Score, Content: replacement.Content,
		})
	}
	sort.Slice(applied, func(i, j int) bool { return applied[i].Score > applied[j].Score })
	return cleared, savedRunes, applied
}

func isProtectedToolScoreResult(name, args string) bool {
	name = strings.TrimSpace(name)
	if name == "plan" {
		return true
	}
	if name == "read_file" && strings.Contains(strings.ToLower(args), ".plan.json") {
		return true
	}
	return false
}

// scorePathHint keeps the re-read hint in the placeholder.
func scorePathHint(args string) string {
	if strings.TrimSpace(args) == "" {
		return ""
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(args), &parsed); err == nil {
		for _, key := range []string{"path", "file_path", "filepath", "pattern", "root", "dir", "directory", "output", "output_path", "target", "filename"} {
			if value, ok := parsed[key].(string); ok && value != "" {
				return "（" + key + "=" + truncateToolScoreRunes(value, 96) + "）"
			}
		}
	}
	for _, token := range strings.Fields(args) {
		if strings.HasPrefix(token, "/") && len(token) > 1 {
			return "（path=" + truncateToolScoreRunes(strings.Trim(token, "\",'"), 96) + "）"
		}
	}
	return ""
}

func truncateToolScoreRunes(value string, limit int) string {
	runes := []rune(value)
	if limit <= 0 || len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}

// IsStageBoundaryTool reports whether a completed tool call ends one stage.
func IsStageBoundaryTool(cfg *Config, name string) bool {
	if cfg == nil {
		return false
	}
	for _, tool := range cfg.ToolScoreCleanup.StageBoundaryTools {
		if tool == name {
			return true
		}
	}
	return false
}

// ShouldRunToolScoreCleanup decides whether the current step is a cleanup
// point: a stage boundary, or the token fallback crossing its trigger.
func ShouldRunToolScoreCleanup(cfg *Config, stageBoundary bool, currentTokens int) bool {
	if cfg == nil || !cfg.ToolScoreCleanup.Enabled {
		return false
	}
	if stageBoundary && cfg.ToolScoreCleanup.TriggerOnStageBoundary != nil && *cfg.ToolScoreCleanup.TriggerOnStageBoundary {
		return true
	}
	return currentTokens >= cfg.ToolScoreCleanup.TriggerTokens
}
