package websearch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// BingPublicProvider scrapes https://www.bing.com/search?q=... — no key
// required but Bing injects bot-detection challenges after sustained traffic.
// Quality varies: usually the top 5 results are clean, the rest are ads /
// "people also ask" widgets. We target <li class="b_algo"> entries.
type BingPublicProvider struct {
	client *http.Client
	recent bool // when true, the search is restricted to recent results
}

func NewBingPublicProvider(client *http.Client) *BingPublicProvider {
	if client == nil {
		client = defaultHTTPClient()
	}
	return &BingPublicProvider{client: client}
}

// WithRecent toggles the provider into a date-restricted (last 24h) mode. The
// returned value shares the underlying HTTP client with the receiver.
func (p *BingPublicProvider) WithRecent(recent bool) *BingPublicProvider {
	p.recent = recent
	return p
}

// bingEndpoint is the upstream search URL. It is a variable so tests can point
// the provider at a local server and assert on the outgoing request.
var bingEndpoint = "https://www.bing.com/search"

// hasCJK reports whether s contains CJK ideographs or kana. Such a query must
// be sent with a Chinese market and language hint; sending it as en-US makes
// Bing fall back to matching only the latin substrings.
func hasCJK(s string) bool {
	for _, r := range s {
		switch {
		case r >= 0x4E00 && r <= 0x9FFF, // CJK unified ideographs
			r >= 0x3400 && r <= 0x4DBF, // extension A
			r >= 0x3040 && r <= 0x30FF, // kana
			r >= 0xAC00 && r <= 0xD7AF, // hangul syllables
			r >= 0xF900 && r <= 0xFAFF: // compatibility ideographs
			return true
		}
	}
	return false
}

func (p *BingPublicProvider) Name() string { return "bing_public_html" }

func (p *BingPublicProvider) Search(ctx context.Context, query string, n int) ([]SearchResult, error) {
	if n <= 0 {
		n = 8
	}
	// cc=US keeps results reasonably broad; the Accept-Language below biases
	// ranking toward English. mkt=US drops "personalized" results that would
	// otherwise be missing for an anonymous fetch.
	q := url.Values{}
	q.Set("q", query)
	// Market, language and region must follow the query's own script. Hardcoding
	// en-US made Bing treat a CJK query as a vague English-entity lookup: it
	// matched only the latin token ("Blender 角色建模 教程" -> blender.org download
	// pages) and dropped everything else. Same for an unrelated query, which came
	// back as dictionary definitions of the first word.
	mkt, setlang, acceptLang := "en-US", "en-US", "en-US,en;q=0.9"
	cc := "US"
	if hasCJK(query) {
		mkt, setlang, acceptLang = "zh-CN", "zh-CN", "zh-CN,zh;q=0.9,en;q=0.8"
		cc = "CN"
	}
	q.Set("cc", cc)
	q.Set("mkt", mkt)
	q.Set("setlang", setlang)
	if p.recent {
		// qdr=d constrains Bing to "Past 24 hours" results, which is the
		// closest you get to "today" with the public HTML endpoint.
		q.Set("qdr", "d")
		q.Set("filters", "ex1%3a%22ez5_%22")
	}
	endpoint := bingEndpoint + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build bing request: %w", err)
	}
	req.Header.Set("User-Agent", defaultUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", acceptLang)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bing request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bing returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("bing read body: %w", err)
	}
	results := parseBingHTML(string(body), n)
	if len(results) == 0 {
		return nil, fmt.Errorf("bing returned no parseable hits (possibly bot-detection page)")
	}
	return results, nil
}

// parseBingHTML extracts <li class="b_algo"> entries. Each result has a title
// in <h2><a href="...">...</a></h2> and a snippet in <p class="b_lineclamp...">
// or with class "b_snippetText". The scanner walks every <a> and <p> in order
// and pairs them by parent <li>; this avoids needing a real HTML parser.
func parseBingHTML(body string, n int) []SearchResult {
	results := make([]SearchResult, 0, n)
	lower := strings.ToLower(body)
	cursor := 0
	for {
		start := strings.Index(lower[cursor:], "<li")
		if start < 0 {
			break
		}
		start += cursor
		// Ensure we are looking at the start of a <li ... class="...b_algo...">.
		openEnd := strings.Index(body[start:], ">")
		if openEnd < 0 {
			break
		}
		openEnd += start
		tagHead := body[start:openEnd]
		if !strings.Contains(strings.ToLower(tagHead), "b_algo") {
			cursor = openEnd + 1
			continue
		}
		closeIdx := strings.Index(lower[openEnd:], "</li>")
		if closeIdx < 0 {
			break
		}
		closeIdx += openEnd
		chunk := body[openEnd:closeIdx]
		r := extractBingEntry(chunk)
		if r.URL != "" {
			r.URL = CanonicalizeURL(r.URL)
			results = append(results, r)
		}
		cursor = closeIdx + 5
		if len(results) >= n {
			break
		}
	}
	return results
}

func extractBingEntry(chunk string) SearchResult {
	var r SearchResult
	anchors := findAnchors(chunk)
	for _, a := range anchors {
		// Bing wraps titles in <h2><a>... so the first anchor in an entry is
		// usually the title. Confirm by sniffing class names it tends to use.
		if r.URL == "" && a.Href != "" && (strings.HasPrefix(a.Href, "http://") || strings.HasPrefix(a.Href, "https://")) {
			r.URL = a.Href
			r.Title = strings.TrimSpace(a.Text)
			continue
		}
		if r.Snippet == "" && (hasClassToken(a.Class, "b_paractl") || hasClassToken(a.Class, "b_snippetText") || hasClassToken(a.Class, "b_caption")) {
			r.Snippet = strings.TrimSpace(a.Text)
		}
	}
	// Fallback: capture <p>...</p> snippets when no anchor carries the text.
	if r.Snippet == "" {
		for _, p := range findParagraphs(chunk) {
			text := strings.TrimSpace(p)
			if text == "" || len(text) < 20 {
				continue
			}
			r.Snippet = text
			break
		}
	}
	return r
}

// findParagraphs returns the inner text of every <p>...</p> in body. Used as a
// last-resort snippet source for Bing entries that don't tag their snippet
// anchor with a recognizable class.
func findParagraphs(body string) []string {
	var out []string
	i := 0
	lower := strings.ToLower(body)
	for {
		start := strings.Index(lower[i:], "<p")
		if start < 0 {
			return out
		}
		start += i
		openEnd := strings.Index(body[start:], ">")
		if openEnd < 0 {
			return out
		}
		openEnd += start
		closeIdx := strings.Index(lower[openEnd:], "</p>")
		if closeIdx < 0 {
			return out
		}
		closeIdx += openEnd
		out = append(out, innerText(body[openEnd+1:closeIdx]))
		i = closeIdx + 4
	}
}
