package websearch

import (
	"sort"
	"strings"
)

// This file implements query relaxation for providers that cannot handle
// multi-term AND semantics.
//
// The problem is not ranking, it is recall. Bing's public HTML endpoint does
// not parse a long query as a conjunction: handed "Blender 角色建模 教程" it
// matches loosely on the leading token and answers with blender.org landing
// pages. The same provider, asked the shorter "Blender 教程", returns actual
// tutorials. Both were measured on the same provider within the same minute.
//
// So when the first answer only matches the query's most generic term, the fix
// is to ask a shorter question rather than to post-filter harder. Harder
// filtering cannot work here: every threshold that finally removes the landing
// pages also empties the result set, and the fallback returns everything again.

// relaxedQueries derives shorter fallback phrasings for a query.
//
// A variant pairs the query's latin tokens with exactly one CJK run. Runs are
// tried shortest-first, because a short run is the most generic and therefore
// the most likely to be understood by the endpoint.
//
// Returns nil when the query is already short enough that the endpoint handles
// it, so the common case costs nothing.
func relaxedQueries(query string) []string {
	latin, cjkRuns := splitQueryParts(query)
	if len(cjkRuns) == 0 {
		return nil
	}
	// One CJK run plus at most one latin token is already a two-part query,
	// which is the shape the endpoint answers well. Relaxing it would only
	// add a request without adding recall.
	if len(cjkRuns) == 1 && len(latin) <= 1 {
		return nil
	}

	byLength := make([]string, len(cjkRuns))
	copy(byLength, cjkRuns)
	sort.SliceStable(byLength, func(i, j int) bool {
		return len([]rune(byLength[i])) < len([]rune(byLength[j]))
	})

	seen := map[string]bool{normalizeQuery(query): true}
	out := make([]string, 0, 2)
	for _, run := range byLength {
		v := buildQuery(latin, run)
		key := normalizeQuery(v)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, v)
		// Two variants is enough to cover the common "product + two CJK
		// qualifiers" shape without turning one search into a crawl.
		if len(out) == 2 {
			break
		}
	}
	return out
}

// splitQueryParts separates a query into its latin/digit tokens and its
// maximal CJK runs. Punctuation and whitespace act as separators for both.
func splitQueryParts(query string) (latin []string, cjkRuns []string) {
	var latinBuf strings.Builder
	var cjkBuf strings.Builder
	flushLatin := func() {
		if latinBuf.Len() >= 2 {
			latin = append(latin, latinBuf.String())
		}
		latinBuf.Reset()
	}
	flushCJK := func() {
		if cjkBuf.Len() > 0 {
			cjkRuns = append(cjkRuns, cjkBuf.String())
		}
		cjkBuf.Reset()
	}
	for _, r := range query {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			flushCJK()
			latinBuf.WriteRune(r)
		case r >= 0x4e00 && r <= 0x9fff:
			flushLatin()
			cjkBuf.WriteRune(r)
		default:
			flushLatin()
			flushCJK()
		}
	}
	flushLatin()
	flushCJK()
	return latin, cjkRuns
}

func buildQuery(latin []string, cjkRun string) string {
	parts := make([]string, 0, len(latin)+1)
	parts = append(parts, latin...)
	if cjkRun != "" {
		parts = append(parts, cjkRun)
	}
	return strings.Join(parts, " ")
}

func normalizeQuery(q string) string {
	return strings.ToLower(strings.Join(strings.Fields(q), " "))
}

// answerIsWeak reports whether a source list engages the query only through its
// most generic terms, which is the signature of a provider that ignored the
// conjunction rather than a result set that merely ranked badly.
//
// The bar is "some source names two or more distinct query terms". Requiring a
// higher bar is what silently disables recovery: when the upstream genuinely
// cannot answer, no source clears any absolute bar, and every variant of this
// check falls back to "everything is fine".
func answerIsWeak(sources []map[string]any, query string) bool {
	terms := queryTerms(query)
	if len(terms) < 2 || len(sources) == 0 {
		return false
	}
	for _, src := range sources {
		hay := strings.ToLower(sourceTitle(src) + " " + sourceURL(src))
		covered := 0
		for _, t := range terms {
			if strings.Contains(hay, t) {
				covered++
			}
		}
		if covered >= 2 {
			return false
		}
	}
	return true
}

// mergeSearchSources concatenates two source lists, keeping preferred first and
// dropping URL duplicates (compared case-insensitively, ignoring a trailing
// slash).
//
// The retry list is the preferred one. Reaching this function at all means the
// first answer was judged a weak recall, so the sources the relaxed phrasing
// found are the better evidence; appending them behind a row of landing pages
// would bury the only usable result.
func mergeSearchSources(preferred, fallback []map[string]any, maxResults int) []map[string]any {
	seen := make(map[string]bool, len(preferred)+len(fallback))
	canonical := func(raw string) string {
		return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), "/")
	}
	out := make([]map[string]any, 0, maxResults)
	for _, list := range [][]map[string]any{preferred, fallback} {
		for _, src := range list {
			key := canonical(sourceURL(src))
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, src)
			if maxResults > 0 && len(out) >= maxResults {
				return out
			}
		}
	}
	return out
}
