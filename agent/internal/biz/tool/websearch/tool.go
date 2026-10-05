package websearch

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"agentloop/agent/internal/biz/tool/shared"
	"agentloop/agent/internal/dtypes"
)

type Tool struct {
	schema          dtypes.ToolDef
	racer           *Racer
	factory         func() *http.Client // injected for tests
	ddg             *DuckDuckGoProvider
	bing            *BingPublicProvider
	maxResults      int
	maxSnippetChars int
}

// OutputOptions controls the model-visible search result shape.
type OutputOptions struct {
	MaxResults      int
	MaxSnippetChars int
}

func DefaultOutputOptions() OutputOptions {
	return OutputOptions{MaxResults: 8, MaxSnippetChars: 600}
}

func NewTool(schema dtypes.ToolDef) *Tool {
	return NewToolWithOutputOptions(schema, DefaultOutputOptions())
}

func NewToolWithOutputOptions(schema dtypes.ToolDef, opts OutputOptions) *Tool {
	return newToolWithClientAndOutputOptions(schema, defaultHTTPClient(), opts)
}

// NewToolWithRecent builds a web_search tool whose providers are configured
// for "past 24 hours" results when recent is true. Use this for live-news
// queries so search engines return current-day sources rather than evergreen
// government pages.
func NewToolWithRecent(schema dtypes.ToolDef, recent bool) *Tool {
	t := NewTool(schema)
	t.ddg.WithRecent(recent)
	t.bing.WithRecent(recent)
	t.racer.SetProviders([]Provider{t.ddg, t.bing})
	return t
}

func NewToolWithClient(schema dtypes.ToolDef, client *http.Client) *Tool {
	return newToolWithClientAndOutputOptions(schema, client, DefaultOutputOptions())
}

func newToolWithClientAndOutputOptions(schema dtypes.ToolDef, client *http.Client, opts OutputOptions) *Tool {
	if opts.MaxResults <= 0 {
		opts.MaxResults = 8
	}
	if opts.MaxSnippetChars <= 0 {
		opts.MaxSnippetChars = 600
	}
	ddg := NewDuckDuckGoProvider(client)
	bing := NewBingPublicProvider(client)
	providers := []Provider{ddg, bing}
	return &Tool{
		schema:          schema,
		racer:           NewRacer(Options{PerProviderTimeout: 6 * time.Second, OverallTimeout: 8 * time.Second, MaxResults: opts.MaxResults}, providers...),
		factory:         func() *http.Client { return client },
		ddg:             ddg,
		bing:            bing,
		maxResults:      opts.MaxResults,
		maxSnippetChars: opts.MaxSnippetChars,
	}
}

func (t *Tool) Name() string { return "web_search" }

func (t *Tool) Schema() dtypes.ToolDef { return t.schema }

func (t *Tool) Execute(ctx context.Context, invocation dtypes.ToolInvocation) (dtypes.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return dtypes.ToolResult{}, err
	}
	args, err := shared.DecodeArgs(invocation)
	if err != nil {
		return shared.ErrorResult("web_search", err), nil
	}
	// Toggle per-call "recent" mode on the cached providers. Cheap because the
	// providers just flip a flag — no new HTTP sockets.
	if recent, ok := args["recent"].(bool); ok {
		t.ddg.WithRecent(recent)
		t.bing.WithRecent(recent)
	}
	single := shared.StringArg(args, "query", "q")
	batch := decodeStringSlice(args["queries"])

	queries := make([]string, 0, 1+len(batch))
	if single != "" {
		queries = append(queries, single)
	}
	queries = append(queries, batch...)
	if len(queries) == 0 {
		return shared.ErrorResult("web_search", fmt.Errorf("query (or queries) is required")), nil
	}

	maxResults := t.maxResults
	maxSnippetChars := t.maxSnippetChars
	budgetTokens := shared.IntMetadata(invocation, dtypes.ToolMetadataWebBudgetTokens, 0)
	if budgetTokens > 0 {
		maxResults = searchResultsForBudget(budgetTokens, t.maxResults)
		maxSnippetChars = searchSnippetCharsForBudget(budgetTokens, maxResults, t.maxSnippetChars)
	}

	// Single-query path: race providers, return first non-empty.
	if len(queries) == 1 {
		results, providers, err := t.racer.SearchLimit(ctx, queries[0], maxResults)
		if err != nil {
			return shared.ErrorResult("web_search", err), nil
		}
		sources, poolTruncated := selectSources(results, queries[0], maxResults, maxSnippetChars)
		return dtypes.ToolResult{Value: map[string]any{
			"tool":      "web_search",
			"ok":        true,
			"type":      "web_search_call",
			"action":    map[string]any{"type": "search", "query": queries[0]},
			"count":     len(sources),
			"sources":   sources,
			"providers": providers,
			"truncated": poolTruncated,
			"budget":    searchBudgetResult(budgetTokens, maxResults, maxSnippetChars, sources),
		}}, nil
	}

	// Batch path: each query gets its own racer round.
	type batchEntry struct {
		Query     string           `json:"query"`
		Sources   []map[string]any `json:"sources"`
		Count     int              `json:"count"`
		Truncated bool             `json:"truncated,omitempty"`
		Failed    bool             `json:"failed,omitempty"`
		Error     string           `json:"error,omitempty"`
	}
	entries := make([]batchEntry, len(queries))
	queryBudgets := allocateSearchResultBudgets(maxResults, len(queries))
	allSources := make([]map[string]any, 0, maxResults)
	for i, q := range queries {
		perQuery := queryBudgets[i]
		if perQuery <= 0 {
			entries[i] = batchEntry{Query: q, Failed: true, Error: "dynamic web budget allocated no sources to this query"}
			continue
		}
		results, _, err := t.racer.SearchLimit(ctx, q, perQuery)
		if err != nil {
			entries[i] = batchEntry{Query: q, Failed: true, Error: err.Error()}
			continue
		}
		sources, poolTruncated := selectSources(results, q, perQuery, maxSnippetChars)
		allSources = append(allSources, sources...)
		entries[i] = batchEntry{Query: q, Sources: sources, Count: len(sources), Truncated: poolTruncated}
	}
	return dtypes.ToolResult{Value: map[string]any{
		"tool":    "web_search",
		"ok":      true,
		"type":    "web_search_call",
		"action":  map[string]any{"type": "search", "queries": queries},
		"count":   len(queries),
		"results": entries,
		"budget":  searchBudgetResult(budgetTokens, maxResults, maxSnippetChars, allSources),
	}}, nil
}

// selectSources turns raw provider results into the model-visible source list.
// Unlike the older toSources pass (which only rune-truncated snippets), it first
// de-duplicates by canonical URL, ranks what is left by query relevance, trims
// each snippet to the window that actually mentions the query, and only then
// applies rune-safe truncation. The goal is to hand the model the few results
// that matter and drop the boilerplate, instead of echoing the whole SERP.
//
// The second return value is `poolTruncated`: true when the input pool was
// larger than the user-visible top-N, so callers can report honest truncation
// without leaking the internal fetchMultiplier expansion.
func selectSources(results []SearchResult, query string, maxResults, maxSnippetChars int) ([]map[string]any, bool) {
	ranked := rankAndFilter(results, query, maxResults)
	for i := range ranked {
		ranked[i].Snippet = condenseSnippet(ranked[i].Snippet, query, maxSnippetChars)
	}
	sources, snippetTruncated := toSources(ranked, maxSnippetChars)
	return sources, snippetTruncated || len(results) > maxResults
}

// rankAndFilter collapses duplicate URLs, scores each result against the query,
// and keeps the most relevant entries up to maxResults. Results that share a
// score keep their original provider order.
func rankAndFilter(results []SearchResult, query string, maxResults int) []SearchResult {
	terms := queryTerms(query)
	type scored struct {
		order int
		score int
		r     SearchResult
	}
	seen := make(map[string]bool, len(results))
	ranked := make([]scored, 0, len(results))
	for i, r := range results {
		key := CanonicalizeURL(strings.TrimSpace(r.URL))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		ranked = append(ranked, scored{order: i, score: relevanceScore(r, terms), r: r})
	}
	if len(terms) > 0 {
		sort.SliceStable(ranked, func(i, j int) bool {
			if ranked[i].score != ranked[j].score {
				return ranked[i].score > ranked[j].score
			}
			return ranked[i].order < ranked[j].order
		})
	}
	if maxResults > 0 && len(ranked) > maxResults {
		ranked = ranked[:maxResults]
	}
	out := make([]SearchResult, len(ranked))
	for i := range ranked {
		out[i] = ranked[i].r
	}
	return out
}

// relevanceScore is a cheap keyword-overlap heuristic: title mentions weigh
// most, then snippet text, then the URL slug.
func relevanceScore(r SearchResult, terms []string) int {
	if len(terms) == 0 {
		return 0
	}
	title := strings.ToLower(r.Title)
	url := strings.ToLower(r.URL)
	snippet := strings.ToLower(r.Snippet)
	score := 0
	for _, term := range terms {
		if strings.Contains(title, term) {
			score += 3
		}
		if strings.Contains(snippet, term) {
			score += 2
		}
		if strings.Contains(url, term) {
			score++
		}
	}
	return score
}

// condenseSnippet keeps the part of a snippet that mentions the query. Search
// engines sometimes front snippets with site chrome; this recentres the window
// on the first query hit so truncation happens around the useful text.
func condenseSnippet(snippet, query string, maxChars int) string {
	snippet = strings.TrimSpace(snippet)
	if snippet == "" || maxChars <= 0 {
		return snippet
	}
	terms := queryTerms(query)
	if len(terms) == 0 {
		return snippet
	}
	lower := strings.ToLower(snippet)
	hit := -1
	for _, term := range terms {
		idx := strings.Index(lower, term)
		if idx < 0 {
			continue
		}
		pos := utf8.RuneCountInString(lower[:idx])
		if hit < 0 || pos < hit {
			hit = pos
		}
	}
	if hit <= maxChars/3 {
		// The query already shows up near the start; leave it alone.
		return snippet
	}
	runes := []rune(snippet)
	start := hit - maxChars/3
	if start <= 0 || start >= len(runes) {
		return snippet
	}
	return "..." + string(runes[start:])
}

// queryTerms extracts comparable tokens from a query: latin words of length >= 2
// and CJK bigrams (single characters for one-character CJK queries).
func queryTerms(query string) []string {
	query = strings.ToLower(query)
	var terms []string
	var latin strings.Builder
	var cjk []rune
	flushLatin := func() {
		if latin.Len() >= 2 {
			terms = append(terms, latin.String())
		}
		latin.Reset()
	}
	flushCJK := func() {
		for i := 0; i+1 < len(cjk); i++ {
			terms = append(terms, string(cjk[i:i+2]))
		}
		if len(cjk) == 1 {
			terms = append(terms, string(cjk))
		}
		cjk = cjk[:0]
	}
	for _, r := range query {
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
	return dedupeStrings(terms)
}

func dedupeStrings(in []string) []string {
	if len(in) <= 1 {
		return in
	}
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func toSources(results []SearchResult, maxSnippetChars int) ([]map[string]any, bool) {
	sources := make([]map[string]any, 0, len(results))
	truncated := false
	for index, r := range results {
		snippet, cut := truncateRunes(r.Snippet, maxSnippetChars)
		if cut {
			truncated = true
		}
		sources = append(sources, map[string]any{
			"index":   index + 1,
			"title":   r.Title,
			"url":     r.URL,
			"snippet": snippet,
		})
	}
	return sources, truncated
}

func searchResultsForBudget(budgetTokens, configuredMax int) int {
	if configuredMax <= 0 {
		configuredMax = 8
	}
	if budgetTokens <= 0 {
		return configuredMax
	}
	// Conservative source overhead: title + URL + JSON fields are roughly 40
	// tokens; the remaining budget is used for snippets. This keeps a burst of
	// small searches from exceeding the allocated text budget.
	n := (budgetTokens - 40) / 80
	if n < 1 {
		n = 1
	}
	if n > configuredMax {
		n = configuredMax
	}
	return n
}

func searchSnippetCharsForBudget(budgetTokens, results, configuredMax int) int {
	if configuredMax <= 0 {
		configuredMax = 600
	}
	if budgetTokens <= 0 || results <= 0 {
		return configuredMax
	}
	overhead := results * 40
	available := budgetTokens - overhead
	if available < results*80 {
		available = results * 80
	}
	chars := available / results
	if chars < 80 {
		chars = 80
	}
	if chars > configuredMax {
		chars = configuredMax
	}
	return chars
}

func allocateSearchResultBudgets(total, queries int) []int {
	if queries <= 0 {
		return nil
	}
	if total < queries {
		total = queries
	}
	out := make([]int, queries)
	base := total / queries
	remainder := total % queries
	for i := range out {
		out[i] = base
		if i < remainder {
			out[i]++
		}
	}
	return out
}

func searchBudgetResult(budgetTokens, maxResults, snippetChars int, sources []map[string]any) map[string]any {
	used := 0
	for _, source := range sources {
		used += estimateTextTokens(stringValue(source["title"]))
		used += estimateTextTokens(stringValue(source["url"]))
		used += estimateTextTokens(stringValue(source["snippet"]))
	}
	return map[string]any{
		"context_size":  searchContextSize(budgetTokens),
		"max_tokens":    budgetTokens,
		"used_tokens":   used,
		"source_limit":  maxResults,
		"snippet_chars": snippetChars,
	}
}

func searchContextSize(tokens int) string {
	switch {
	case tokens >= 24000:
		return "high"
	case tokens >= 8000:
		return "medium"
	default:
		return "low"
	}
}

func stringValue(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

func estimateTextTokens(text string) int {
	if text == "" {
		return 0
	}
	n := len([]rune(text))
	if n < 1 {
		return 1
	}
	return n
}

func truncateRunes(text string, maxChars int) (string, bool) {
	if maxChars <= 0 {
		return text, false
	}
	runes := []rune(text)
	if len(runes) <= maxChars {
		return text, false
	}
	return string(runes[:maxChars]) + "... [truncated]", true
}

// decodeStringSlice extracts a JSON array of strings from an untyped argument.
// It accepts []any (typical) or a single string (used as a one-element batch).
func decodeStringSlice(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	}
	return nil
}
