// Package webfetch implements the web_fetch local tool. It does a constrained
// HTTP(S) GET and converts HTML responses to readable text. Inspired by dsh
// `packages/web/web-fetch-http` but stripped of the cordis / ctx.web plumbing
// so it can run inside Fairy's flat LocalBackend.
package webfetch

import (
	"context"
	"errors"
	"encoding/base64"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"agentloop/agent/internal/biz/tool/shared"
	"agentloop/agent/internal/dtypes"
)

const defaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

// Extractive-summary knobs. Long pages are condensed locally (no model call);
// short responses are passed through untouched so small fetches stay lossless.
const (
	summaryMinRunes    = 4000
	summaryTargetRunes = 2400
)

// Per-stage network deadlines. The overall Timeout still caps total work; these
// only stop a single stage from consuming the whole budget on a dead host.
const (
	dialTimeout   = 8 * time.Second
	headerTimeout = 20 * time.Second
)

type Options struct {
	MaxURLLength     int
	MaxResponseBytes int64
	MaxBodyChars     int
	Timeout          time.Duration
	MaxRedirects     int
	UserAgent        string
	// SummaryChars caps the extractive summary length. Zero uses the built-in
	// default; set a negative value to disable summarization entirely.
	SummaryChars int
}

func DefaultOptions() Options {
	return Options{
		MaxURLLength:     2048,
		MaxResponseBytes: 5_000_000,
		MaxBodyChars:     100_000,
		Timeout:          30 * time.Second,
		MaxRedirects:     5,
		UserAgent:        defaultUserAgent,
	}
}

type Tool struct {
	name   string
	schema dtypes.ToolDef
	opts   Options
	client *http.Client
}

func NewTool(schema dtypes.ToolDef) *Tool {
	return NewToolWithName(schema, "web_fetch")
}

func NewToolWithName(schema dtypes.ToolDef, name string) *Tool {
	if strings.TrimSpace(name) == "" {
		name = "web_fetch"
	}
	return NewNamedToolWithOptions(schema, name, DefaultOptions())
}

func NewToolWithOptions(schema dtypes.ToolDef, opts Options) *Tool {
	return NewNamedToolWithOptions(schema, "web_fetch", opts)
}

func NewNamedToolWithOptions(schema dtypes.ToolDef, name string, opts Options) *Tool {
	if opts.MaxURLLength <= 0 {
		opts.MaxURLLength = 2048
	}
	if opts.MaxResponseBytes <= 0 {
		opts.MaxResponseBytes = 5_000_000
	}
	if opts.MaxBodyChars <= 0 {
		opts.MaxBodyChars = 100_000
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.MaxRedirects < 0 {
		opts.MaxRedirects = 0
	}
	if opts.UserAgent == "" {
		opts.UserAgent = defaultUserAgent
	}
	return &Tool{
		name:   name,
		schema: schema,
		opts:   opts,
		client: &http.Client{
			Timeout: opts.Timeout,
			Transport: &http.Transport{
				MaxIdleConns:        8,
				MaxIdleConnsPerHost: 4,
				IdleConnTimeout:     30 * time.Second,
				// Per-stage deadlines. Without a dial deadline a firewalled host
				// holds the request until the full client timeout: a live probe
				// showed three unreachable sites each burning 30s to report the
				// same useless "context deadline exceeded". Dialing is normally
				// sub-second, so capping it turns a 30s stall into a few seconds
				// and leaves the overall timeout to guard real work.
				DialContext: (&net.Dialer{
					Timeout:   dialTimeout,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				ResponseHeaderTimeout: headerTimeout,
			},
		},
	}
}

func (t *Tool) Name() string { return t.name }

func (t *Tool) Schema() dtypes.ToolDef { return t.schema }

func (t *Tool) Execute(ctx context.Context, invocation dtypes.ToolInvocation) (dtypes.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return dtypes.ToolResult{}, err
	}
	args, err := shared.DecodeArgs(invocation)
	if err != nil {
		return shared.ErrorResult(t.Name(), err), nil
	}
	fileMode := strings.ToLower(strings.TrimSpace(shared.StringArg(args, "file_mode")))
	switch fileMode {
	case "", "text", "stateless":
	default:
		return shared.ErrorResult(t.Name(), fmt.Errorf("unsupported file_mode %q", fileMode)), nil
	}
	// stateless keeps the raw bytes in the tool result (base64) instead of
	// rendering text, so asset pipelines can download images without writing
	// into the agent workspace.
	stateless := fileMode == "stateless"

	url := strings.TrimSpace(shared.StringArg(args, "url"))
	if url == "" {
		return shared.ErrorResult(t.Name(), fmt.Errorf("url is required")), nil
	}
	if len(url) > t.opts.MaxURLLength {
		return shared.ErrorResult(t.Name(), fmt.Errorf("url length %d exceeds limit %d", len(url), t.opts.MaxURLLength)), nil
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return shared.ErrorResult(t.Name(), fmt.Errorf("only http(s) urls are supported")), nil
	}

	timeout := t.opts.Timeout
	if invocation.Timeout > 0 && invocation.Timeout < timeout {
		timeout = invocation.Timeout
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, url, nil)
	if err != nil {
		return shared.ErrorResult(t.Name(), fmt.Errorf("build request: %w", err)), nil
	}
	req.Header.Set("User-Agent", t.opts.UserAgent)
	if stateless {
		req.Header.Set("Accept", "*/*")
	} else {
		req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,application/json")
	}

	redirectsLeft := t.opts.MaxRedirects
	client := *t.client
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if redirectsLeft <= 0 {
			return fmt.Errorf("stopped after %d redirects", len(via))
		}
		redirectsLeft--
		return nil
	}

	resp, err := client.Do(req)
	if err != nil {
		return shared.ErrorResult(t.Name(), classifyRequestError(err)), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Try to surface a tiny preview of the body so the model can diagnose.
		preview, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return shared.ErrorResult(t.Name(), fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(preview)))), nil
	}

	readLimit := t.opts.MaxResponseBytes
	if stateless {
		// A truncated binary payload is worse than a failure: callers would get
		// silently corrupt bytes. Read one extra byte so we can detect oversize.
		readLimit++
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, readLimit))
	if err != nil {
		return shared.ErrorResult(t.Name(), fmt.Errorf("read body: %w", err)), nil
	}
	if stateless && int64(len(body)) > t.opts.MaxResponseBytes {
		return shared.ErrorResult(t.Name(), fmt.Errorf(
			"response exceeds maximum size of %d bytes", t.opts.MaxResponseBytes)), nil
	}
	if stateless && len(body) == 0 {
		return shared.ErrorResult(t.Name(), fmt.Errorf("response body is empty")), nil
	}

	contentType := resp.Header.Get("Content-Type")
	truncated := int64(len(body)) >= t.opts.MaxResponseBytes

	if stateless {
		return dtypes.ToolResult{Value: map[string]any{
			"tool":         t.Name(),
			"ok":           true,
			"url":          url,
			"status":       resp.StatusCode,
			"content_type": contentType,
			"bytes":        len(body),
			"truncated":    false,
			"file_mode":    "stateless",
			"file_name":    fileNameFromURL(url, contentType),
			"file_base64":  base64.StdEncoding.EncodeToString(body),
		}}, nil
	}

	maxBodyChars := t.opts.MaxBodyChars
	if budgetTokens := shared.IntMetadata(invocation, dtypes.ToolMetadataWebBudgetTokens, 0); budgetTokens > 0 {
		maxBodyChars = fetchCharsForBudget(budgetTokens, t.opts.MaxBodyChars)
	}
	bodyText, bodyTruncated := renderBody(contentType, body, maxBodyChars)
	truncated = truncated || bodyTruncated

	// JS-render fallback: pages that hydrate their content over XHR (price
	// tables, dashboards) come back from plain HTTP as a near-empty shell. If
	// the text is suspiciously short, re-fetch through the browser bridge and
	// keep whichever version actually carries content. Best effort by design —
	// a missing browser leaves the plain result untouched.
	if shouldTryRender(contentType, bodyText) {
		if hydrated, rerr := tryRender(ctx, url); rerr == nil {
			if len([]rune(hydrated)) > len([]rune(bodyText))*2 {
				bodyText = hydrated
				truncated = true // upstream text is page-dependent, not a real cap
			}
		}
	}

	originalChars := len([]rune(bodyText))
	focus := strings.TrimSpace(shared.StringArg(args, "focus"))
	if focus == "" {
		focus = strings.TrimSpace(shared.StringArg(args, "query"))
	}
	summarized := false
	if t.opts.SummaryChars >= 0 && originalChars >= summaryMinRunes && shouldSummarize(contentType, bodyText) {
		condensed := summarizeWithFocus(bodyText, focus, t.opts.SummaryChars)
		if condensed != "" && condensed != bodyText {
			bodyText = condensed + "\n... [summarized]"
			summarized = true
			truncated = true
		}
	}
	usedTokens := estimateFetchedTokens(bodyText)

	return dtypes.ToolResult{Value: map[string]any{
		"tool":         t.Name(),
		"ok":           true,
		"url":          url,
		"status":       resp.StatusCode,
		"content_type": contentType,
		"bytes":        len(body),
		"truncated":    truncated,
		"summarized":   summarized,
		"text":         bodyText,
		"budget": map[string]any{
			"max_chars":      maxBodyChars,
			"used_tokens":    usedTokens,
			"max_tokens":     maxBodyChars / 2,
			"original_chars": originalChars,
		},
	}}, nil
}

// fileNameFromURL derives a best-effort file name for stateless downloads so
// callers can recover the original extension.
func fileNameFromURL(rawURL, contentType string) string {
	name := "download.bin"
	if parsed, err := url.Parse(rawURL); err == nil {
		if base := strings.TrimSpace(pathBase(parsed.Path)); base != "" {
			name = base
		}
	}
	if strings.Contains(name, ".") {
		return name
	}
	switch strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0])) {
	case "image/png":
		return name + ".png"
	case "image/jpeg", "image/jpg":
		return name + ".jpg"
	case "image/webp":
		return name + ".webp"
	case "image/gif":
		return name + ".gif"
	case "application/pdf":
		return name + ".pdf"
	}
	return name
}

func pathBase(p string) string {
	p = strings.TrimRight(p, "/")
	if idx := strings.LastIndex(p, "/"); idx >= 0 {
		return p[idx+1:]
	}
	return p
}

// renderBody turns an HTTP response body into readable text. HTML is
// stripped of tags; everything else is returned as-is (truncated at the
// configured character cap).
func renderBody(contentType string, body []byte, maxChars int) (string, bool) {
	ct := strings.ToLower(contentType)
	text := string(body)
	if strings.Contains(ct, "html") {
		text = htmlToText(text)
	}
	runes := []rune(text)
	if maxChars > 0 && len(runes) > maxChars {
		return string(runes[:maxChars]) + "\n... [truncated]", true
	}
	return text, false
}

// summarizeText condenses long prose into its highest-signal sentences using a
// purely local, extractive heuristic: each sentence is scored by the document
// frequency of its distinctive tokens (latin words and CJK bigrams), with
// very common tokens treated as boilerplate and a small lead-sentence bonus.
// Selected sentences are replayed in original order so the summary still reads
// linearly; identical sentences are only kept once.
func summarizeText(text string, target int) string {
	if target <= 0 {
		target = summaryTargetRunes
	}
	sentences := splitSentences(text)
	if len(sentences) <= 1 {
		return truncateRunesText(text, target)
	}
	df := tokenDocumentFrequency(sentences)
	docCount := len(sentences)
	type scored struct {
		order int
		score float64
		text  string
	}
	ranked := make([]scored, 0, len(sentences))
	for i, s := range sentences {
		ranked = append(ranked, scored{order: i, score: scoreSentence(s, df, docCount, i), text: s})
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })

	chosen := make([]scored, 0, len(ranked))
	seen := make(map[string]bool, len(ranked))
	used := 0
	for _, it := range ranked {
		key := strings.TrimSpace(it.text)
		if seen[key] {
			continue
		}
		n := len([]rune(key))
		if used+n > target && len(chosen) > 0 {
			continue
		}
		seen[key] = true
		chosen = append(chosen, it)
		used += n
		if used >= target {
			break
		}
	}
	sort.SliceStable(chosen, func(i, j int) bool { return chosen[i].order < chosen[j].order })

	var b strings.Builder
	for i, it := range chosen {
		if i > 0 {
			b.WriteString(" ")
		}
		b.WriteString(strings.TrimSpace(it.text))
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return truncateRunesText(text, target)
	}
	return out
}

// summarizeWithFocus re-ranks sentences by query overlap in addition to the
// document-frequency heuristic. Sentences that mention a focus term float to
// the top, so a one-line factual answer is more likely to survive compression
// than under the generic summarizer. An empty focus falls back to summarizeText.
func summarizeWithFocus(text, focus string, target int) string {
	focus = strings.TrimSpace(focus)
	if focus == "" {
		return summarizeText(text, target)
	}
	if target <= 0 {
		target = summaryTargetRunes
	}
	sentences := splitSentences(text)
	if len(sentences) <= 1 {
		return truncateRunesText(text, target)
	}
	df := tokenDocumentFrequency(sentences)
	docCount := len(sentences)
	terms := focusTerms(focus)
	type scored struct {
		order int
		score float64
		text  string
	}
	ranked := make([]scored, 0, len(sentences))
	for i, s := range sentences {
		base := scoreSentence(s, df, docCount, i)
		ranked = append(ranked, scored{order: i, score: base + focusBoost(s, terms), text: s})
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })

	chosen := make([]scored, 0, len(ranked))
	seen := make(map[string]bool, len(ranked))
	used := 0
	for _, it := range ranked {
		key := strings.TrimSpace(it.text)
		if seen[key] {
			continue
		}
		n := len([]rune(key))
		if used+n > target && len(chosen) > 0 {
			continue
		}
		seen[key] = true
		chosen = append(chosen, it)
		used += n
		if used >= target {
			break
		}
	}
	sort.SliceStable(chosen, func(i, j int) bool { return chosen[i].order < chosen[j].order })

	var b strings.Builder
	for i, it := range chosen {
		if i > 0 {
			b.WriteString(" ")
		}
		b.WriteString(strings.TrimSpace(it.text))
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return truncateRunesText(text, target)
	}
	return out
}

// focusBoost rewards sentences that explicitly mention a focus term.
// Matching in the lower-cased sentence keeps CJK and latin cases aligned
// with focusTerms' output.
func focusBoost(sentence string, terms []string) float64 {
	if len(terms) == 0 {
		return 0
	}
	lower := strings.ToLower(sentence)
	hits := 0
	for _, term := range terms {
		if strings.Contains(lower, term) {
			hits++
		}
	}
	if hits == 0 {
		return 0
	}
	// Strong but bounded: focus terms outweigh generic relevance by a wide
	// margin without drowning out boilerplate that coincidentally contains
	// the same word.
	return 10.0 * float64(hits) / float64(len(terms))
}

// focusTerms breaks a focus string into lowercase tokens that focusBoost can
// match against sentence text. CJK runs are emitted as sliding bigrams to
// match the tokenization used by sentenceTokens; latin tokens are kept whole
// when they hit a minimum length. Empty input yields an empty slice so
// summarizeWithFocus cleanly falls back to its baseline ranking.
func focusTerms(focus string) []string {
	var terms []string
	var latin strings.Builder
	var cjk []rune
	flushLatin := func() {
		if latin.Len() >= 2 {
			terms = append(terms, strings.ToLower(latin.String()))
		}
		latin.Reset()
	}
	flushCJK := func() {
		for i := 0; i+1 < len(cjk); i++ {
			terms = append(terms, strings.ToLower(string(cjk[i:i+2])))
		}
		cjk = cjk[:0]
	}
	for _, r := range focus {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			flushCJK()
			latin.WriteRune(r)
		case r >= 0x4e00 && r <= 0x9fff:
			flushLatin()
			cjk = append(cjk, r)
		default:
			flushLatin()
			flushCJK()
		}
	}
	flushLatin()
	flushCJK()
	return terms
}

// shouldSummarize decides whether summarizeText is safe to run on the given
// content type. Extractive summarization only makes sense for prose; running
// it on JSON, source code, diffs, logs or other structured text destroys
// structure and produces garbage. Anything that isn't HTML-ish prose is left
// alone and the caller relies on MaxBodyChars truncation instead.
func shouldSummarize(contentType string, body string) bool {
	ct := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	switch ct {
	case "application/json", "application/xml", "application/javascript",
		"application/x-javascript", "application/yaml", "application/toml",
		"application/x-yaml", "application/graphql":
		return false
	}
	if strings.HasPrefix(ct, "application/") {
		// Unknown binary-style MIME; do not paraphrase.
		return false
	}
	// Heuristic fallback: short text already returns the whole body from
	// renderBody, and the caller only invokes us when originalChars is above
	// the threshold. If the body lacks sentence-like punctuation or looks
	// like a code/diff/log shape, refuse — extraction would still fold
	// brackets and braces into "sentences" without semantic meaning.
	if looksStructured(body) {
		return false
	}
	return true
}

// looksStructured returns true when the body smells like source code, JSON,
// YAML, a diff or a log: lines that never end with terminal punctuation, lots
// of balanced punctuation, or a high ratio of short lines. We bail out only
// when several of these markers stack up, so ordinary prose with a few code
// samples still passes.
func looksStructured(body string) bool {
	if body == "" {
		return false
	}
	lines := strings.Split(body, "\n")
	if len(lines) < 8 {
		return false
	}
	short, terminal, brackety, codeish := 0, 0, 0, 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if len([]rune(trimmed)) <= 60 {
			short++
		}
		if strings.HasSuffix(trimmed, ".") || strings.HasSuffix(trimmed, "。") ||
			strings.HasSuffix(trimmed, "!") || strings.HasSuffix(trimmed, "?") ||
			strings.HasSuffix(trimmed, "！") || strings.HasSuffix(trimmed, "?") {
			terminal++
		}
		bracketed := false
		for _, r := range trimmed {
			if r == '{' || r == '}' || r == '(' || r == ')' || r == '[' || r == ']' || r == '<' || r == '>' {
				bracketed = true
				break
			}
		}
		if bracketed {
			brackety++
		}
		// Source-code/diff marker: lines that open with a comment, a keyword,
		// a +/- diff prefix, or a logging-style timestamp.
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") ||
			strings.HasPrefix(trimmed, "+") || strings.HasPrefix(trimmed, "-") ||
			strings.HasPrefix(trimmed, "func ") || strings.HasPrefix(trimmed, "package ") ||
			strings.HasPrefix(trimmed, "import ") || strings.HasPrefix(trimmed, "import(") ||
			strings.HasPrefix(trimmed, "type ") || strings.HasPrefix(trimmed, "var ") ||
			strings.HasPrefix(trimmed, "const ") || strings.HasPrefix(trimmed, "return ") ||
			strings.HasPrefix(trimmed, "[20") {
			codeish++
		}
	}
	total := len(lines)
	// Many short lines + virtually no terminal punctuation → structured.
	shortish := short*2 > total
	flat := terminal*4 < total
	if shortish && flat {
		return true
	}
	// Brackets dominate the line set → JSON/YAML/code.
	if brackety*3 > total {
		return true
	}
	// Diff/log/code-style prefixes are reliable markers on their own when
	// they account for a meaningful share of the body.
	if codeish*4 > total {
		return true
	}
	return false
}

func splitSentences(text string) []string {
	var out []string
	var b strings.Builder
	flush := func() {
		s := strings.TrimSpace(b.String())
		b.Reset()
		if s != "" {
			out = append(out, s)
		}
	}
	for _, r := range text {
		b.WriteRune(r)
		switch r {
		case '。', '！', '？', '；', '\n', '.', '!', '?':
			flush()
		}
	}
	flush()
	return out
}

// tokenDocumentFrequency counts, for every token, in how many distinct
// sentences it appears. Document frequency (rather than raw counts) keeps a
// single repeated sentence from inflating its own tokens.
func tokenDocumentFrequency(sentences []string) map[string]int {
	df := make(map[string]int)
	for _, s := range sentences {
		seen := make(map[string]bool)
		for _, tok := range sentenceTokens(s) {
			if seen[tok] {
				continue
			}
			seen[tok] = true
			df[tok]++
		}
	}
	return df
}

func scoreSentence(sentence string, df map[string]int, docCount, index int) float64 {
	tokens := sentenceTokens(sentence)
	if len(tokens) == 0 {
		return 0
	}
	// Tokens that show up in more than half the sentences are boilerplate
	// (navigation, repeated disclaimers) and carry no signal — drop them.
	boilerplate := docCount / 2
	if boilerplate < 2 {
		boilerplate = 2
	}
	seen := make(map[string]bool)
	sum, kept := 0, 0
	for _, tok := range tokens {
		if seen[tok] {
			continue
		}
		seen[tok] = true
		if df[tok] > boilerplate {
			continue
		}
		kept++
		sum += df[tok]
	}
	lead := 1.0 / float64(1+index)
	if kept == 0 {
		return lead
	}
	return float64(sum)/math.Sqrt(float64(kept)) + lead
}

// sentenceTokens tokenizes mixed latin/CJK text without external dependencies:
// runs of latin letters/digits and sliding CJK bigrams.
func sentenceTokens(s string) []string {
	s = strings.ToLower(s)
	var tokens []string
	var latin strings.Builder
	var cjk []rune
	flushLatin := func() {
		if latin.Len() >= 2 {
			tokens = append(tokens, latin.String())
		}
		latin.Reset()
	}
	flushCJK := func() {
		for i := 0; i+1 < len(cjk); i++ {
			tokens = append(tokens, string(cjk[i:i+2]))
		}
		cjk = cjk[:0]
	}
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			flushCJK()
			latin.WriteRune(r)
		case r >= 0x4e00 && r <= 0x9fff:
			flushLatin()
			cjk = append(cjk, r)
		default:
			flushLatin()
			flushCJK()
		}
	}
	flushLatin()
	flushCJK()
	return tokens
}

func truncateRunesText(text string, maxChars int) string {
	runes := []rune(text)
	if maxChars <= 0 || len(runes) <= maxChars {
		return text
	}
	return string(runes[:maxChars]) + "... [truncated]"
}

func fetchCharsForBudget(budgetTokens, configuredMax int) int {
	if configuredMax <= 0 {
		configuredMax = 100000
	}
	if budgetTokens <= 0 {
		return configuredMax
	}
	chars := budgetTokens * 2
	if chars < 1200 {
		chars = 1200
	}
	if chars > configuredMax {
		chars = configuredMax
	}
	return chars
}

func estimateFetchedTokens(text string) int {
	if text == "" {
		return 0
	}
	n := len([]rune(text))
	if n < 1 {
		return 1
	}
	return n
}

// htmlToText does a single pass over an HTML document, removing tags and
// decoding a small set of entities. It is good enough for extracting article
// prose; for clean Markdown conversion use a dedicated library.
// classifyRequestError turns a transport failure into something the agent can
// act on.
//
// The previous form was `request failed: %w`, which collapses four very
// different situations into one opaque string. A live probe of six sites
// returned "context deadline exceeded" for three of them, telling the model
// nothing about whether the host was blocked, firewalled, or merely slow —
// so it could neither retry sensibly nor pick another source. Naming the kind
// plus the next action is the part that actually changes behaviour.
func classifyRequestError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	lower := strings.ToLower(msg)

	// Prefer the typed check: a *net.DNSError can carry IsTimeout with an
	// unhelpful Error() string, so matching text alone misses real timeouts.
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return timeoutError(err)
	}

	switch {
	case errors.Is(err, context.DeadlineExceeded) ||
		strings.Contains(lower, "timeout") ||
		strings.Contains(lower, "deadline exceeded"):
		return timeoutError(err)

	case errors.Is(err, context.Canceled):
		return fmt.Errorf("request cancelled: %w", err)

	case strings.Contains(lower, "no such host"):
		return fmt.Errorf("dns lookup failed: %w; the hostname does not resolve — "+
			"check the spelling, or use a different source", err)

	case strings.Contains(lower, "connection refused"):
		return fmt.Errorf("connection refused: %w; nothing is listening on that "+
			"host or port", err)

	case strings.Contains(lower, "certificate") || strings.Contains(lower, "tls"):
		return fmt.Errorf("tls handshake failed: %w; the site's certificate could "+
			"not be validated", err)

	case strings.Contains(lower, "connection reset") || strings.Contains(lower, "eof"):
		return fmt.Errorf("connection dropped by peer: %w; the site closed the "+
			"connection early, which often means it is refusing automated traffic", err)
	}

	return fmt.Errorf("request failed: %w", err)
}

func timeoutError(err error) error {
	return fmt.Errorf("request timed out: %w; the host did not respond in time — "+
		"it may be unreachable from this network, so prefer another source "+
		"or retry later rather than repeating immediately", err)
}

// tagNameFrom returns the lowercased element name of the tag spanning
// s[start:i] (where s[i] == '>'), without its attributes.
//
// This distinction matters more than it looks. The previous code compared the
// whole tag text, so `<script type="text/javascript">` yielded
// "script type=\"text/javascript\"" which never equals "script" — meaning real
// world script bodies were emitted as prose. A live probe of go.dev returned
// output that began with `(function(w,d,s,l,i){w[l]=w[l]||[]...`, i.e. the
// page's own JavaScript handed straight to the model. Same defect silently
// broke paragraph breaks for every `<div class=...>`.
func tagNameFrom(s string, start, i int) string {
	t := s[start+1 : i]
	if k := strings.IndexAny(t, " \t\r\n/"); k >= 0 {
		t = t[:k]
	}
	return strings.ToLower(strings.TrimSpace(t))
}

// skipTags are elements whose entire subtree is markup machinery rather than
// prose. Their text must never reach the model: it wastes context, and inline
// script is the single easiest way for a page to smuggle instructions into an
// agent's prompt.
var skipTags = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true,
	"svg": true, "iframe": true, "object": true, "canvas": true,
	"head": true, // <title>/<meta> are page furniture, not article text
	"nav": true, "aside": true, "footer": true, "form": true,
}

// blockTags force a paragraph break when they close.
var blockTags = map[string]bool{
	"p": true, "br": true, "li": true, "h1": true, "h2": true,
	"h3": true, "h4": true, "h5": true, "h6": true, "div": true,
	"section": true, "article": true, "blockquote": true, "tr": true,
	"pre": true, "table": true, "hr": true,
}

func htmlToText(s string) string {
	var b strings.Builder
	var pendingBlock strings.Builder
	inTag := false
	flushBlock := func() {
		t := collapseWS(pendingBlock.String())
		if t != "" {
			b.WriteString(t)
			b.WriteString("\n\n")
		}
		pendingBlock.Reset()
	}
	lower := strings.ToLower(s)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '<':
			inTag = true
		case c == '>':
			if inTag {
				if tagStart := lastOpenTag(s, i); tagStart >= 0 {
					name := tagNameFrom(s, tagStart, i)
					if strings.HasPrefix(name, "/") {
						if blockTags[name[1:]] {
							flushBlock()
						}
					} else if skipTags[name] {
						// Jump past the whole subtree so its contents never
						// enter pendingBlock.
						if end, ok := findCloseTag(lower, i, name); ok {
							i = end
						} else {
							// Unterminated: drop the remainder rather than
							// dump a page tail of stray script.
							flushBlock()
							return strings.TrimSpace(b.String())
						}
					} else if blockTags[name] {
						flushBlock()
					}
				}
			}
			inTag = false
		case !inTag:
			if c == '&' {
				if rest, ok := decodeEntity(s[i:]); ok {
					pendingBlock.WriteString(rest)
					advance := strings.IndexAny(s[i:], ";")
					if advance > 0 {
						i += advance
					}
					continue
				}
			}
			pendingBlock.WriteByte(c)
		}
	}
	flushBlock()
	return strings.TrimSpace(b.String())
}

// findCloseTag returns the index of the '>' that closes </name>, starting the
// scan at the '>' of the opening tag. ok is false when the subtree never closes.
func findCloseTag(lower string, from int, name string) (int, bool) {
	needle := "</" + name
	for j := from; ; {
		k := strings.Index(lower[j:], needle)
		if k < 0 {
			return 0, false
		}
		k += j
		// Make sure this is really the matching close, not a prefix of
		// something longer such as </scriptfoo>.
		after := k + len(needle)
		if after >= len(lower) || lower[after] == '>' || lower[after] == ' ' ||
			lower[after] == '\t' || lower[after] == '\n' {
			if e := strings.IndexByte(lower[k:], '>'); e >= 0 {
				return k + e, true
			}
			return 0, false
		}
		j = after
	}
}

func lastOpenTag(s string, before int) int {
	// Walk backwards from `before` to find the matching '<' for the tag we
	// just closed. Cheap because tags are short.
	depth := 0
	for j := before - 1; j >= 0 && before-j < 200; j-- {
		if s[j] == '>' {
			depth++
		}
		if s[j] == '<' {
			if depth == 0 {
				return j
			}
			depth--
		}
	}
	return -1
}

func collapseWS(s string) string {
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		prevSpace = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

func decodeEntity(s string) (string, bool) {
	if len(s) < 4 || s[0] != '&' {
		return "", false
	}
	end := strings.IndexByte(s, ';')
	if end < 0 || end > 8 {
		return "", false
	}
	switch s[:end+1] {
	case "&amp;":
		return "&", true
	case "&lt;":
		return "<", true
	case "&gt;":
		return ">", true
	case "&quot;":
		return `"`, true
	case "&apos;":
		return "'", true
	case "&nbsp;":
		return " ", true
	}
	return "", false
}
