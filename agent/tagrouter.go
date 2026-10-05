package main

import (
	"regexp"
	"strings"
)

var (
	reportOpenRe    = regexp.MustCompile(`^\s*<report\b`)
	reportTagRe     = regexp.MustCompile(`<report(?:\s[^>]*)?>`)
	reportContentRe = regexp.MustCompile(`(?s)<report(?:\s[^>]*)?>(.*?)</report>`)

	// --- Others ---
	citeRe       = regexp.MustCompile(`<cite\b[^>]*>`)
	imgMdRe      = regexp.MustCompile(`!\[[^\]]*\]\([^)]+\)`)
	allXmlTagsRe = regexp.MustCompile(`<[^>]+>`)
)

// --- Constants for route names ---
const (
	RouteReport          = "report"
	RouteReportViolation = "report_violation"
	RoutePlain           = "plain"
	RouteToolOnly        = "tool_only"
	RouteEmpty           = "empty"
)

// hasOpenTag checks if a chunk opens with a specific tag.
func hasOpenTag(delta string, openRe *regexp.Regexp) bool {
	return openRe.MatchString(delta)
}

// isInsideTag checks whether accumulated text is currently inside a tag pair
func isInsideTag(accumulated, openTagName, closeTag string) bool {
	if accumulated == "" {
		return false
	}
	lastOpen := strings.LastIndex(accumulated, "<"+openTagName)
	if lastOpen < 0 {
		return false
	}
	lastClose := strings.LastIndex(accumulated, closeTag)
	return lastOpen > lastClose
}

// --- Detection helpers ---

func HasReportTag(content string) bool    { return reportTagRe.MatchString(content) }
func HasImageLinkTag(content string) bool { return imgMdRe.MatchString(content) }
func HasCiteTag(content string) bool      { return citeRe.MatchString(content) }

// HasVisibleFinalContent reports whether a final turn contains anything the
// user can read after removing thinking, reflection, and protocol wrappers.
func HasVisibleFinalContent(content string) bool {
	text := strings.TrimSpace(stripReflectionBlocks(stripThinking(content)))
	if text == "" {
		return false
	}
	// Context compression checkpoints are assistant-role messages internally,
	// but they are never user-visible answers. If the model echoes one after a
	// preflight compaction, force the normal empty-final recovery path.
	if strings.HasPrefix(text, "<summary") {
		return false
	}
	if m := reportContentRe.FindStringSubmatch(text); len(m) > 1 && strings.TrimSpace(m[1]) != "" {
		return true
	}
	return strings.TrimSpace(allXmlTagsRe.ReplaceAllString(text, " ")) != ""
}

// --- Inside tag checks (for streaming) ---

func IsInsideReport(accumulated string) bool {
	return isInsideTag(accumulated, "report", "</report>")
}

// GetContentRoute classifies a content chunk by route
func GetContentRoute(delta string, hasToolCalls bool, accumulatedText string) string {
	// Streaming: currently inside tags
	if IsInsideReport(accumulatedText) {
		return RouteReport
	}

	// <msg> is intentionally not a routing protocol. If the model writes it,
	// it is ordinary text and is preserved for the frontend to display.
	if hasToolCalls {
		if reportOpenRe.MatchString(delta) {
			return RouteReportViolation
		}
		if strings.TrimSpace(delta) != "" {
			return RoutePlain
		}
		return RouteToolOnly
	}

	// Final turn (no tool calls)
	if reportOpenRe.MatchString(delta) {
		return RouteReport
	}
	if strings.TrimSpace(delta) != "" {
		return RoutePlain
	}
	return RouteEmpty
}

// GetIntermediateDescription returns the raw progress text for tracing. Tags,
// including <msg>, are intentionally preserved instead of being interpreted.
func GetIntermediateDescription(content string) string {
	return strings.TrimSpace(content)
}

// ComplianceResult holds the result of a compliance check
type ComplianceResult struct {
	IsCompliant  bool
	Violations   []string
	HasReport    bool
	HasImageLink bool
	HasCite      bool
	Description  string
}

// CheckFinalTurnCompliant checks if final turn content is compliant.
// Final answers may be plain natural language or an optional <report>. A
// final answer does not need a protocol tag, and <msg> is ordinary text.
func CheckFinalTurnCompliant(finalContent string) ComplianceResult {
	text := strings.TrimSpace(stripReflectionBlocks(stripThinking(finalContent)))
	return ComplianceResult{
		IsCompliant: true,
		HasReport:   HasReportTag(text),
	}
}

// CheckIntermediateTurnCompliant keeps tool-call rounds free of final-delivery
// content. <msg> is not parsed and is allowed through as ordinary text.
func CheckIntermediateTurnCompliant(content string) ComplianceResult {
	hasReport := HasReportTag(content)
	hasImg := HasImageLinkTag(content)
	hasCite := HasCiteTag(content)

	violations := []string{}

	// Should not have <report> in intermediate
	if hasReport {
		violations = append(violations, "report_in_intermediate")
	}
	if hasImg {
		violations = append(violations, "image_link_in_intermediate")
	}
	if hasCite {
		violations = append(violations, "cite_in_intermediate")
	}

	return ComplianceResult{
		IsCompliant:  len(violations) == 0,
		Violations:   violations,
		HasReport:    hasReport,
		HasImageLink: hasImg,
		HasCite:      hasCite,
		Description:  GetIntermediateDescription(content),
	}
}

// RepairIntermediateContent preserves the model's text exactly, including a
// literal <msg> tag. The backend no longer synthesizes protocol wrappers.
func RepairIntermediateContent(content string) string {
	return strings.TrimSpace(content)
}

// RepairFinalContent intentionally does not post-process <report> tags:
// the final answer is passed through exactly as produced by the model.
func RepairFinalContent(content string) string {
	return strings.TrimSpace(content)
}

// streamTagRe strips Fairy protocol tags for live-streamed display text.
var streamTagRe = regexp.MustCompile(`</?(?:report|file_action|summary|key_knowledge|recent_actions|think|done)[^><]*>`)

// visibleStreamer turns raw streamed content into user-visible text deltas by
// stripping protocol tags as they arrive, so the frontend can render a
// typewriter effect without ever showing partial tags.
type visibleStreamer struct {
	emitted string
}

// Update feeds the accumulated raw content and returns the new visible-text
// suffix ("" when nothing new is visible yet). <thinking> and <reflection>
// blocks are treated like tool results: they never enter the visible text
// stream (the frontend renders them as collapsible cards instead).
func (s *visibleStreamer) Update(raw string) string {
	clean := streamTagRe.ReplaceAllString(maskInlineToolCalls(stripReflectionBlocks(raw)), "")
	clean = holdBackIncompleteTag(clean)
	if len(clean) >= len(s.emitted) && strings.HasPrefix(clean, s.emitted) {
		delta := clean[len(s.emitted):]
		s.emitted = clean
		return delta
	}
	// A "<" that later formed a tag can shrink the clean view; re-baseline on
	// the longest common prefix and emit only the newly-appearing tail.
	common := 0
	max := len(s.emitted)
	if len(clean) < max {
		max = len(clean)
	}
	for common < max && s.emitted[common] == clean[common] {
		common++
	}
	s.emitted = clean
	if common < len(clean) {
		return clean[common:]
	}
	return ""
}

// thinkBlockRe matches complete thinking blocks: the native interleaved form
// (<think>...</think>, e.g. MiniMax-M2.x) and the explicit model-written form
// (<thinking>...</thinking>). Keep them in the raw assistant content for
// continuity, but never surface them as ordinary answer text.
var thinkBlockRe = regexp.MustCompile(`(?s)<(?:think|thinking|mm:think)[^>]*>(.*?)</(?:think|thinking|mm:think)>`)

// reflectionBlockRe matches complete <reflection>...</reflection> self-check
// blocks. Like thinking, reflection is an internal tool-result-style block:
// it is rendered as a collapsible card, never as ordinary answer text.
var reflectionBlockRe = regexp.MustCompile(`(?s)<reflection[^>]*>(.*?)</reflection>`)

var (
	thinkOpenRe  = regexp.MustCompile(`<(?:think|thinking|mm:think)[^>]*>`)
	thinkCloseRe = regexp.MustCompile(`</(?:think|thinking|mm:think)>`)
)

// lastTagIndex returns the byte index of the last occurrence of re, or -1.
func lastTagIndex(content string, re *regexp.Regexp) int {
	locs := re.FindAllStringIndex(content, -1)
	if len(locs) == 0 {
		return -1
	}
	return locs[len(locs)-1][0]
}

// extractThinking returns the concatenated inner text of all complete
// <think>/<thinking> blocks plus any trailing unclosed block (for streaming).
func extractThinking(content string) string {
	var sb strings.Builder
	for _, p := range thinkBlockRe.FindAllStringSubmatch(content, -1) {
		sb.WriteString(p[1])
	}
	lastOpen := lastTagIndex(content, thinkOpenRe)
	lastClose := lastTagIndex(content, thinkCloseRe)
	if lastOpen > lastClose {
		openTag := thinkOpenRe.FindString(content[lastOpen:])
		sb.WriteString(content[lastOpen+len(openTag):])
	}
	return sb.String()
}

// stripThinking removes <think> blocks (complete ones and a trailing unclosed
// block) from content, leaving only user-visible text.
func stripThinking(content string) string {
	out := thinkBlockRe.ReplaceAllString(content, "")
	lastOpen := lastTagIndex(out, thinkOpenRe)
	lastClose := lastTagIndex(out, thinkCloseRe)
	if lastOpen > lastClose {
		out = out[:lastOpen]
	}
	return out
}

// stripReflectionBlocks removes complete <reflection> blocks plus a trailing
// unclosed block (for streaming) from content, leaving only user-visible text.
func stripReflectionBlocks(content string) string {
	out := reflectionBlockRe.ReplaceAllString(content, "")
	if i := strings.LastIndex(out, "<reflection"); i >= 0 {
		out = out[:i]
	}
	return out
}

// maxThinkingChars caps explicit <thinking>/<think> reasoning so a verbose
// model cannot blow up the context or the UI with runaway chain-of-thought.
const maxThinkingChars = 3000

// capThinking truncates the explicit thinking blocks in content to
// maxThinkingChars, rebuilding content with one bounded <thinking> block.
func capThinking(content string) string {
	if maxThinkingChars <= 0 {
		return content
	}
	t := extractThinking(content)
	runes := []rune(t)
	if len(runes) <= maxThinkingChars {
		return content
	}
	// Keep the total bounded: ellipsis marker is counted inside the budget.
	const ellipsis = "…"
	keep := maxThinkingChars - len([]rune(ellipsis))
	if keep < 0 {
		keep = 0
	}
	truncated := string(runes[:keep]) + ellipsis
	return "<thinking>" + truncated + "</thinking>\n" + stripThinking(content)
}

// thinkStreamer splits raw streamed content into thinking (<think>...</think>)
// and user-visible text deltas, so the frontend can render a live collapsible
// thinking block (Codex-style) while the answer streams below it.
type thinkStreamer struct {
	vis      visibleStreamer
	thinkBuf strings.Builder
	emitted  int
}

// Update feeds the full raw content and returns the new visible-text delta and
// the new thinking delta (either may be "").
func (s *thinkStreamer) Update(raw string) (string, string) {
	visDelta := s.vis.Update(stripReflectionBlocks(stripThinking(raw)))
	if s.emitted >= maxThinkingChars {
		return visDelta, ""
	}
	think := extractThinking(raw)
	var thinkDelta string
	if len(think) > s.emitted {
		thinkDelta = think[s.emitted:]
		if s.emitted+len(thinkDelta) > maxThinkingChars {
			thinkDelta = thinkDelta[:maxThinkingChars-s.emitted]
		}
		s.emitted += len(thinkDelta)
		s.thinkBuf.WriteString(thinkDelta)
	}
	return visDelta, thinkDelta
}

// Thinking returns all thinking text accumulated so far.
func (s *thinkStreamer) Thinking() string { return s.thinkBuf.String() }

// GetUserVisibleText extracts report content when present. Other text is kept
// as-is so literal tags such as <msg> remain visible.
func GetUserVisibleText(content string) string {
	if m := reportContentRe.FindStringSubmatch(content); len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	return stripReflectionBlocks(content)
}
