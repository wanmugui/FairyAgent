package websearch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// DuckDuckGoProvider scrapes https://html.duckduckgo.com/html/?q=... The HTML
// variant requires no JS and no API key; rate-limiting kicks in around 30+
// requests/minute per IP, so callers should debounce. Inspired by dsh's
// approach of treating HTML providers as a fallback transport.
type DuckDuckGoProvider struct {
	client    *http.Client
	recent    bool
}

func NewDuckDuckGoProvider(client *http.Client) *DuckDuckGoProvider {
	if client == nil {
		client = defaultHTTPClient()
	}
	return &DuckDuckGoProvider{client: client}
}

// WithRecent toggles DDG's time filter via df (d=day, w=week, m=month, y=year).
// Returns the receiver so callers can configure in one expression.
func (p *DuckDuckGoProvider) WithRecent(recent bool) *DuckDuckGoProvider {
	p.recent = recent
	return p
}

func (p *DuckDuckGoProvider) Name() string { return "duckduckgo_html" }

func (p *DuckDuckGoProvider) Search(ctx context.Context, query string, n int) ([]SearchResult, error) {
	if n <= 0 {
		n = 8
	}
	form := url.Values{}
	form.Set("q", query)
	form.Set("kl", "us-en")
	if p.recent {
		// df=d constrains DDG HTML to "Past day" results. The wire format is
		// intentionally raw: DDG's HTML form endpoint ignores prettier values.
		form.Set("df", "d")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://html.duckduckgo.com/html/", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build ddg request: %w", err)
	}
	req.Header.Set("User-Agent", defaultUserAgent)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ddg request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ddg returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("ddg read body: %w", err)
	}
	// Bot-detection pages are tiny JS-only documents; real result pages are
	// always multi-KB. Use body size as a quick sanity gate so we don't waste
	// the parser on a 2KB "please prove you're human" stub.
	if len(body) < 4096 {
		return nil, fmt.Errorf("ddg returned suspiciously short body (%d bytes, likely bot-detection)", len(body))
	}
	results := parseDuckDuckGoHTML(string(body), n)
	if len(results) == 0 {
		return nil, fmt.Errorf("ddg returned no parseable hits (possibly bot-detection page, body=%d bytes)", len(body))
	}
	return results, nil
}

// parseDuckDuckGoHTML extracts result entries from a DDG HTML page. The
// current layout (2025+) uses prefixed classes that share a stem with the
// enclosing <li>: `result-link`, `result__a`, or the older `result__a` for
// the title anchor, plus `result-snippet` / `result__snippet` for the text.
// URLs are wrapped in a click tracker (//duckduckgo.com/l/?uddg=...) which
// we unwrap to the real target.
//
// We accept any class that starts with "result" and contains "a"/"link"
// / "title" for titles and any class that starts with "result" and contains
// "snippet" for snippets; this catches both legacy (result__a / result__snippet)
// and current (result-link / result-snippet) layouts.
func parseDuckDuckGoHTML(body string, n int) []SearchResult {
	anchors := findAnchors(body)
	results := make([]SearchResult, 0, n)
	seenURLs := make(map[string]bool, n)
	for i, a := range anchors {
		if !isDDGTitleClass(a.Class) || a.Href == "" {
			continue
		}
		title := strings.TrimSpace(a.Text)
		if title == "" {
			continue
		}
		url := CanonicalizeURL(unwrapDDGClick(a.Href))
		if url == "" || seenURLs[url] {
			continue
		}
		seenURLs[url] = true
		// Look ahead in the anchors list for the matching snippet. The DDG
		// HTML layout places the snippet anchor within ~20 anchors after the
		// title anchor.
		var snippet string
		for j := i + 1; j < len(anchors) && j < i+30; j++ {
			if isDDGSnippetClass(anchors[j].Class) {
				snippet = strings.TrimSpace(anchors[j].Text)
				break
			}
		}
		results = append(results, SearchResult{Title: title, URL: url, Snippet: snippet})
		if len(results) >= n {
			break
		}
	}
	return results
}

// isDDGTitleClass matches DDG's current title anchors (class="result-link")
// and the legacy ones (class="result__a"). We accept any class that contains
// "result" plus one of: link, title, "result__a".
func isDDGTitleClass(class string) bool {
	for _, tok := range strings.Fields(class) {
		if tok == "result-link" || tok == "result__a" || tok == "result__title" {
			return true
		}
	}
	return false
}

// isDDGSnippetClass matches DDG snippets (class="result-snippet" or the
// legacy "result__snippet").
func isDDGSnippetClass(class string) bool {
	for _, tok := range strings.Fields(class) {
		if tok == "result-snippet" || tok == "result__snippet" {
			return true
		}
	}
	return false
}

func unwrapDDGClick(raw string) string {
	if raw == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if u.Host == "duckduckgo.com" && u.Path == "/l/" {
		if target := u.Query().Get("uddg"); target != "" {
			return target
		}
	}
	return raw
}
