// Package websearch implements the web_search local tool. It races two
// keyless HTML providers (DuckDuckGo and Bing public search) and merges the
// first non-empty response. Each provider hides its own HTML scraping details;
// the orchestrator only sees normalized SearchResult values. Inspired by dsh
// `packages/web/web-search-*` but stripped of the cordis / ctx.web plumbing so
// it can run inside Fairy's flat LocalBackend.
package websearch

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// SearchResult is one entry in a provider's response.
type SearchResult struct {
	Title   string
	URL     string
	Snippet string
}

// Provider returns the first N results for a query, or an error. Implementations
// must respect ctx cancellation.
type Provider interface {
	Name() string
	Search(ctx context.Context, query string, n int) ([]SearchResult, error)
}

// Options configures the racer.
type Options struct {
	// Per-provider timeout. The racer waits up to this duration for the slower
	// provider to catch up when both have returned errors, before giving up.
	PerProviderTimeout time.Duration
	// OverallTimeout caps the racer wall-clock regardless of per-provider
	// state. Zero means "use PerProviderTimeout".
	OverallTimeout time.Duration
	// MaxResults is the upper bound on merged results returned to the caller.
	MaxResults int
}

// DefaultOptions returns safe defaults for an interactive agent call.
func DefaultOptions() Options {
	return Options{
		PerProviderTimeout: 6 * time.Second,
		OverallTimeout:     8 * time.Second,
		MaxResults:         8,
	}
}

// Racer runs every registered provider concurrently and merges the first
// non-empty response. Errors from individual providers are surfaced only if
// every provider fails.
type Racer struct {
	providers []Provider
	opts      Options
}

// SetProviders replaces the racer's provider list. Used by the Tool wrapper to
// toggle "recent" mode (date-restricted providers) without rebuilding the racer.
func (r *Racer) SetProviders(providers []Provider) {
	if r == nil {
		return
	}
	r.providers = providers
}

type providerOutcome struct {
	name    string
	results []SearchResult
	err     error
}

func NewRacer(opts Options, providers ...Provider) *Racer {
	if opts.PerProviderTimeout <= 0 {
		opts.PerProviderTimeout = 6 * time.Second
	}
	if opts.OverallTimeout <= 0 {
		opts.OverallTimeout = opts.PerProviderTimeout + 2*time.Second
	}
	if opts.MaxResults <= 0 {
		opts.MaxResults = 8
	}
	return &Racer{providers: providers, opts: opts}
}

// Search returns merged results. As soon as ANY provider yields at least one
// result, that result set becomes the base; remaining providers are allowed
// up to PerProviderTimeout to add unique URLs before the call returns. If a
// provider is still in flight when its grace window closes, its partial
// results are still merged.
func (r *Racer) Search(ctx context.Context, query string) ([]SearchResult, []string, error) {
	return r.SearchLimit(ctx, query, 0)
}

// SearchLimit runs one search with a per-call result cap. A positive limit
// lowers the racer's configured maximum; zero keeps the configured maximum.
//
// The actual fetch size is limit * fetchMultiplier so providers hand back a
// larger pool than the user-visible top-N. The downstream selectSources step
// then re-ranks by query relevance and trims back to maxResults. Without this,
// provider-side ordering pre-empts relevance ranking entirely.
func (r *Racer) SearchLimit(ctx context.Context, query string, maxResults int) ([]SearchResult, []string, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil, errors.New("query is empty")
	}
	limit := r.opts.MaxResults
	if maxResults > 0 && maxResults < limit {
		limit = maxResults
	}
	if limit <= 0 {
		limit = 8
	}
	fetch := limit * fetchMultiplier
	if fetch < limit {
		fetch = limit
	}
	overallCtx, cancel := context.WithTimeout(ctx, r.opts.OverallTimeout)
	defer cancel()

	out := make(chan providerOutcome, len(r.providers))
	var wg sync.WaitGroup
	for _, p := range r.providers {
		p := p
		wg.Add(1)
		go func() {
			defer wg.Done()
			pctx, pcancel := context.WithTimeout(overallCtx, r.opts.PerProviderTimeout)
			defer pcancel()
			results, err := p.Search(pctx, query, fetch)
			out <- providerOutcome{name: p.Name(), results: results, err: err}
		}()
	}

	merged := make([]SearchResult, 0, fetch)
	seen := make(map[string]bool, fetch)
	used := make(map[string]bool, len(r.providers))
	errs := make(map[string]error)

	providersReturned := 0
	for providersReturned < len(r.providers) {
		select {
		case <-overallCtx.Done():
			// Overall timeout reached. Drain whatever's already in the channel.
			for len(errs) < len(r.providers) && providersReturned < len(r.providers) {
				select {
				case o := <-out:
					providersReturned++
					recordOutcome(o, &merged, seen, used, errs)
				default:
					providersReturned = len(r.providers)
				}
			}
			if len(merged) > 0 {
				return finalize(merged, limit), winnerNames(used), nil
			}
			return nil, nil, fmt.Errorf("web_search overall timeout (%s): %w", r.opts.OverallTimeout, overallCtx.Err())
		case o := <-out:
			providersReturned++
			recordOutcome(o, &merged, seen, used, errs)
			// First non-empty result triggers the grace window. We let the
			// remaining providers finish within PerProviderTimeout so we can
			// dedupe and merge; this is what makes "race for the fastest"
			// degrade gracefully when both succeed.
			if len(merged) > 0 && !used[o.name] && o.err == nil {
				// Keep listening for the rest of providers until either they
				// all return or the grace window closes. The overall timeout
				// also acts as the absolute bound.
				continue
			}
		}
	}
	// wg.Wait is implicit because we just drained the channel. But for safety:
	wg.Wait()

	if len(merged) > 0 {
		// Return the full merged pool; selectSources trims to the user-visible
		// top-N after re-ranking by query relevance. Truncating here would
		// discard highly relevant entries that fell below the provider's own
		// order threshold, defeating the point of fetching a larger pool.
		return merged, winnerNames(used), nil
	}
	// Nothing came back successfully.
	if len(errs) > 0 {
		return nil, nil, formatAllErrors(errs)
	}
	return nil, nil, errors.New("web_search returned no results")
}

func recordOutcome(o providerOutcome, merged *[]SearchResult, seen map[string]bool, used map[string]bool, errs map[string]error) {
	if o.err != nil {
		errs[o.name] = o.err
		return
	}
	used[o.name] = true
	for _, r := range o.results {
		if r.URL == "" || seen[r.URL] {
			continue
		}
		seen[r.URL] = true
		*merged = append(*merged, r)
	}
}

// fetchMultiplier expands how many candidates each provider is asked for so the
// downstream relevance ranker (selectSources) operates on a real pool, not the
// already-truncated top-N.
const fetchMultiplier = 2

func finalize(results []SearchResult, max int) []SearchResult {
	sort.SliceStable(results, func(i, j int) bool {
		// Stable order from each provider; stable-sort preserves arrival order.
		return false
	})
	if len(results) > max {
		results = results[:max]
	}
	return results
}

func winnerNames(used map[string]bool) []string {
	out := make([]string, 0, len(used))
	for n, ok := range used {
		if ok {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func formatAllErrors(errs map[string]error) error {
	parts := make([]string, 0, len(errs))
	for name, err := range errs {
		parts = append(parts, fmt.Sprintf("%s: %s", name, err))
	}
	sort.Strings(parts)
	return fmt.Errorf("all web_search providers failed: %s", strings.Join(parts, "; "))
}

// CanonicalizeURL normalizes URLs for de-duplication. Stripping fragments and
// lowercasing the host is enough to collapse most duplicates returned by two
// search engines.
func CanonicalizeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Fragment = ""
	u.Host = strings.ToLower(u.Host)
	return u.String()
}
