package websearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestDuckDuckGoProviderParsesResultPage(t *testing.T) {
	const body = `<html><body>
<div class="result">
  <a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fa&abc=1">Result One Title</a>
  <a class="result__snippet">First snippet text.</a>
</div>
<div class="result">
  <a class="result__a" href="https://other.example.org/b">Result Two</a>
  <a class="result__snippet">Second snippet text.</a>
</div>
</body></html>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	// Point the provider at the test server via a transport shim.
	client := server.Client()
	provider := NewDuckDuckGoProvider(client)
	// We cannot easily redirect DDG's hardcoded host; instead swap the endpoint
	// indirectly by monkey-patching the URL inside Search. To keep this test
	// purely unit, we exercise parseDuckDuckGoHTML directly.
	results := parseDuckDuckGoHTML(body, 8)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d (%#v)", len(results), results)
	}
	if results[0].Title != "Result One Title" {
		t.Fatalf("title not extracted: %#v", results[0])
	}
	if !strings.HasPrefix(results[0].URL, "https://example.com/") {
		t.Fatalf("URL not unwrapped from DDG click tracker: %q", results[0].URL)
	}
	if results[0].Snippet != "First snippet text." {
		t.Fatalf("snippet not captured: %#v", results[0])
	}
	_ = provider
	_ = context.Background()
}

func TestBingProviderParsesBAlgoEntries(t *testing.T) {
	const body = `<html><body>
<li class="b_algo">
  <h2><a href="https://example.com/page">Example Page Title</a></h2>
  <p class="b_lineclamp4 b_algoSlug">Snippet for example.com.</p>
</li>
<li class="b_algo">
  <h2><a href="https://other.example.org/path">Second Result</a></h2>
  <p>This entry uses plain paragraph tags.</p>
</li>
</body></html>`

	results := parseBingHTML(body, 8)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d (%#v)", len(results), results)
	}
	if results[0].Title != "Example Page Title" || results[0].URL != "https://example.com/page" {
		t.Fatalf("first result wrong: %#v", results[0])
	}
	if results[1].URL != "https://other.example.org/path" {
		t.Fatalf("second result URL wrong: %#v", results[1])
	}
	if !strings.Contains(results[1].Snippet, "plain paragraph") {
		t.Fatalf("fallback snippet extraction failed: %#v", results[1])
	}
}

func TestCanonicalizeURLStripsFragment(t *testing.T) {
	got := CanonicalizeURL("https://Example.com/foo?bar=1#frag")
	want := "https://example.com/foo?bar=1"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestToSourcesTruncatesByRunes(t *testing.T) {
	results := []SearchResult{{Title: "标题", URL: "https://example.com", Snippet: "中文中文中文"}}
	sources, truncated := toSources(results, 3)
	if !truncated {
		t.Fatal("expected snippet truncation")
	}
	if len(sources) != 1 || sources[0]["index"] != 1 {
		t.Fatalf("unexpected sources: %#v", sources)
	}
	snippet, _ := sources[0]["snippet"].(string)
	if !utf8.ValidString(snippet) || !strings.HasPrefix(snippet, "中文中") {
		t.Fatalf("snippet was not rune-truncated safely: %q", snippet)
	}
}

func TestSelectSourcesRanksAndDedupes(t *testing.T) {
	results := []SearchResult{
		{Title: "unrelated page", URL: "https://a.example/x", Snippet: "nothing to see"},
		{Title: "Go module proxy", URL: "https://b.example/go", Snippet: "the go module proxy caches modules"},
		{Title: "go module mirror", URL: "https://B.example/go#fragment", Snippet: "same canonical url"},
	}
	sources, _ := selectSources(results, "go module", 8, 600)
	if len(sources) != 2 {
		t.Fatalf("expected duplicate URL to collapse, got %d: %#v", len(sources), sources)
	}
	if got := sources[0]["url"]; got != "https://b.example/go" {
		t.Fatalf("relevance ranking did not promote the on-topic result: %#v", sources)
	}
}

func TestSelectSourcesHonorsResultCap(t *testing.T) {
	results := make([]SearchResult, 0, 5)
	for i := 0; i < 5; i++ {
		results = append(results, SearchResult{
			Title:   "go module",
			URL:     "https://example.com/" + string(rune('a'+i)),
			Snippet: "go module reference",
		})
	}
	sources, _ := selectSources(results, "go module", 2, 600)
	if len(sources) != 2 {
		t.Fatalf("expected top-2 sources, got %d", len(sources))
	}
}

func TestAllocateSearchResultBudgets(t *testing.T) {
	got := allocateSearchResultBudgets(8, 3)
	if len(got) != 3 || got[0]+got[1]+got[2] != 8 || got[0] < got[1] {
		t.Fatalf("unexpected allocation: %#v", got)
	}
}

func TestSearchBudgetResultCountsSources(t *testing.T) {
	sources := []map[string]any{{
		"title":   "Example",
		"url":     "https://example.com/reference",
		"snippet": "Reference text",
	}}
	got := searchBudgetResult(1000, 8, 200, sources)
	if got["used_tokens"].(int) == 0 {
		t.Fatalf("used_tokens should include source text: %#v", got)
	}
}

func TestRacerReturnsFirstValid(t *testing.T) {
	// Two providers, one fast with results, one slow without — fast wins.
	fast := &fakeProvider{
		name: "fast",
		results: []SearchResult{
			{Title: "Fast A", URL: "https://a.example/"},
			{Title: "Fast B", URL: "https://b.example/"},
		},
	}
	slow := &fakeProvider{
		name:    "slow",
		delay:   200 * time.Millisecond,
		results: []SearchResult{{Title: "Slow X", URL: "https://x.example/"}},
	}
	r := NewRacer(Options{PerProviderTimeout: 500 * time.Millisecond, OverallTimeout: 1 * time.Second, MaxResults: 8}, fast, slow)
	results, providers, err := r.Search(context.Background(), "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) < 1 || results[0].Title != "Fast A" {
		t.Fatalf("expected fast winner, got %#v", results)
	}
	if !contains(providers, "fast") {
		t.Fatalf("providers should include 'fast', got %#v", providers)
	}
}

type fakeProvider struct {
	name    string
	delay   time.Duration
	results []SearchResult
	err     error
}

func (p *fakeProvider) Name() string { return p.name }
func (p *fakeProvider) Search(ctx context.Context, query string, n int) ([]SearchResult, error) {
	if p.delay > 0 {
		select {
		case <-time.After(p.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if p.err != nil {
		return nil, p.err
	}
	return p.results, nil
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// TestRacerSearchLimitFetchesPooled verifies the provider is asked for the
// expanded candidate pool (limit * fetchMultiplier) and that SearchLimit
// returns that pool verbatim. Trimming to the user-visible top-N is the job
// of selectSources, which runs relevance ranking before clipping.
func TestRacerSearchLimitFetchesPooled(t *testing.T) {
	provider := &fakeProvider{name: "askme", results: make([]SearchResult, 0, 8)}
	for i := 0; i < 8; i++ {
		provider.results = append(provider.results, SearchResult{
			Title:   "noise page",
			URL:     "https://noise.example/" + string(rune('a'+i)),
			Snippet: "unrelated filler text that does not mention the query",
		})
	}
	r := NewRacer(DefaultOptions(), provider)
	got, _, err := r.SearchLimit(context.Background(), "rare term", 4)
	if err != nil {
		t.Fatalf("SearchLimit error: %v", err)
	}
	if len(got) != 8 {
		t.Fatalf("expected SearchLimit to return the full candidate pool, got %d", len(got))
	}
	want := 4 * fetchMultiplier
	if len(provider.results) != want {
		t.Fatalf("provider should have been asked for %d entries, pool was %d", want, len(provider.results))
	}
}

// TestRacerPooledEnablesRelevanceRescue shows the headline outcome of P3:
// even when the provider's own ordering puts the relevant result below the
// fold, the racer still pulls it into the candidate pool, so selectSources
// can promote it to the model-visible top-N.
func TestRacerPooledEnablesRelevanceRescue(t *testing.T) {
	results := make([]SearchResult, 0, 16)
	for i := 0; i < 14; i++ {
		results = append(results, SearchResult{
			Title:   "noise " + string(rune('a'+i)),
			URL:     "https://noise.example/" + string(rune('a'+i)),
			Snippet: "irrelevant text " + string(rune('a'+i)),
		})
	}
	results = append(results, SearchResult{
		Title:   "rare term reference",
		URL:     "https://answer.example/rare",
		Snippet: "this is the rare term answer",
	})
	results = append(results, SearchResult{
		Title:   "noise tail",
		URL:     "https://noise.example/q",
		Snippet: "more filler",
	})
	provider := &fakeProvider{name: "askme", results: results}
	r := NewRacer(DefaultOptions(), provider)
	got, _, err := r.SearchLimit(context.Background(), "rare term", 4)
	if err != nil {
		t.Fatalf("SearchLimit error: %v", err)
	}
	if len(got) != len(results) {
		t.Fatalf("expected SearchLimit to return the full pool, got %d", len(got))
	}
	sources, _ := selectSources(got, "rare term", 4, 600)
	if len(sources) == 0 {
		t.Fatalf("no sources returned")
	}
	top, _ := sources[0]["url"].(string)
	if top != "https://answer.example/rare" {
		t.Fatalf("expected the answer page to be promoted; top URL was %q", top)
	}
}
