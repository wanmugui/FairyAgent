package websearch

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// SogouProvider scrapes https://www.sogou.com/web?query=...
//
// This provider exists because DuckDuckGo and Bing both tokenize Chinese
// place names poorly — "金沙洲" degrades to "金" and returns gold-market news
// instead of property listings. Sogou keeps the multi-character place name
// intact, which is the whole reason we added a domestic engine.
//
// We deliberately did NOT ship a Baidu provider. Baidu answers this host with
// a 302 straight to wappass.baidu.com (a CAPTCHA) on both direct and proxied
// egress; Sogou and 360 serve real result markup to the same host. Choosing
// an engine that answers is not the same as attacking one that doesn't — we
// just don't emit requests we know will be refused.
//
// Sogou hides the true destination in a url="..." attribute and puts a
// /link?url=<opaque> redirect in the anchor's href. We prefer the real target
// and fall back to the absolute redirect form, which still resolves.
type SogouProvider struct {
	client *http.Client
}

func NewSogouProvider(client *http.Client) *SogouProvider {
	if client == nil {
		client = defaultHTTPClient()
	}
	return &SogouProvider{client: client}
}

// Name identifies the provider in logs and in the sources[] output.
func (p *SogouProvider) Name() string { return "sogou" }

// Search performs one Sogou query and returns up to n results.
func (p *SogouProvider) Search(ctx context.Context, query string, n int) ([]SearchResult, error) {
	u := "https://www.sogou.com/web?query=" + url.QueryEscape(query) + "&ie=utf8"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("sogou request: %w", err)
	}
	req.Header.Set("User-Agent", defaultUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sogou fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sogou status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("sogou read: %w", err)
	}
	// Sogou serves a small interstitial for blocked queries with a 200, so
	// gate on plausible document size instead of trusting the status code.
	if len(body) < 4096 {
		return nil, fmt.Errorf("sogou response too small (%d bytes) — likely a block page", len(body))
	}

	return parseSogouHTML(string(body), n), nil
}

// parseSogouHTML extracts organic results. One result lives in div.vrwrap,
// its title in h3.vr-title > a, and the real destination in a url="..."
// attribute inside the block.
func parseSogouHTML(body string, n int) []SearchResult {
	blocks := splitSogouBlocks(body)
	results := make([]SearchResult, 0, n)
	seen := make(map[string]bool, n)

	for _, block := range blocks {
		href, title, ok := sogouTitleAnchor(block)
		if !ok {
			continue
		}
		if real := sogouRealURL(block); real != "" {
			href = real
		} else if strings.HasPrefix(href, "/") {
			// Relative /link?url= redirect: make it absolute so it resolves.
			href = "https://www.sogou.com" + href
		} else if !strings.HasPrefix(href, "http") {
			continue
		}
		href = CanonicalizeURL(href)
		if href == "" || seen[href] || isSogouFooterLink(href) {
			continue
		}
		seen[href] = true
		results = append(results, SearchResult{
			Title:   title,
			URL:     href,
			Snippet: sogouSnippet(block),
		})
		if len(results) >= n {
			break
		}
	}
	return results
}

// splitSogouBlocks slices the document into per-result chunks on div.vrwrap.
// The final block runs to the next footer marker so trailing navigation
// ("下一页" and friends) is not mistaken for a result.
func splitSogouBlocks(body string) []string {
	const marker = `class="vrwrap"`
	blocks := make([]string, 0, 16)
	idx := 0
	for {
		i := strings.Index(body[idx:], marker)
		if i < 0 {
			break
		}
		start := idx + i + len(marker)
		j := strings.Index(body[start:], marker)
		var block string
		if j < 0 {
			block = body[start:]
			if f := strings.Index(block, `class="foot"`); f > 0 {
				block = block[:f]
			}
		} else {
			block = body[start : start+j]
		}
		blocks = append(blocks, block)
		idx = start
		if len(blocks) >= 40 { // hard stop so a pathological page can't spin
			break
		}
	}
	return blocks
}

// isSogouFooterLink rejects Sogou's own chrome links, which are never organic
// results even when one of them is the first anchor in a block.
func isSogouFooterLink(href string) bool {
	for _, prefix := range []string{
		"https://www.sogou.com/web?",
		"https://www.sogou.com/next",
		"https://www.sogou.com/antispider",
		"https://www.sogou.com/help",
	} {
		if strings.HasPrefix(href, prefix) {
			return true
		}
	}
	return false
}

// sogouTitleAnchor returns the href and inner text of the anchor inside
// h3.vr-title.
func sogouTitleAnchor(block string) (href, text string, ok bool) {
	h := strings.Index(block, "vr-title")
	if h < 0 {
		return "", "", false
	}
	rest := block[h:]
	a := strings.Index(rest, "<a ")
	if a < 0 {
		return "", "", false
	}
	tag := rest[a:]
	hs := strings.Index(tag, `href="`)
	if hs < 0 {
		return "", "", false
	}
	hs += len(`href="`)
	he := strings.Index(tag[hs:], `"`)
	if he < 0 {
		return "", "", false
	}
	href = tag[hs : hs+he]
	gt := strings.Index(tag, ">")
	if gt < 0 {
		return "", "", false
	}
	end := strings.Index(tag[gt:], "</a>")
	if end < 0 {
		return "", "", false
	}
	text = strings.TrimSpace(html.UnescapeString(stripTags(tag[gt+1 : gt+end])))
	if href == "" || len([]rune(text)) < 4 {
		return "", "", false
	}
	return href, text, true
}

// sogouRealURL looks for the true destination Sogou hides alongside the
// redirect wrapper.
func sogouRealURL(block string) string {
	for _, attr := range []string{`url="`, `data-url="`} {
		i := strings.Index(block, attr)
		if i < 0 {
			continue
		}
		rest := block[i+len(attr):]
		e := strings.Index(rest, `"`)
		if e <= 0 {
			continue
		}
		v := strings.TrimSpace(html.UnescapeString(rest[:e]))
		if strings.HasPrefix(v, "http") {
			return v
		}
	}
	return ""
}

// sogouSnippet pulls the description text out of the result block. In the live
// markup the description sits in a div carrying id="cacheresult_summary_N"
// and/or a class ending in "clamp2". We extract that node, then drop any
// remaining inline-CSS blob rather than returning it as prose.
func sogouSnippet(block string) string {
	if s := sogouNodeText(block, `id="cacheresult_summary_`); s != "" && !looksLikeCode(s) {
		return s
	}
	if i := strings.Index(block, "clamp2"); i > 0 {
		if s := sogouClean(block[i:]); s != "" && !looksLikeCode(s) {
			return s
		}
	}
	return ""
}

// sogouNodeText finds a node whose attribute starts with attrPrefix and
// returns its text up to the closing tag, tag-stripped and trimmed.
func sogouNodeText(block, attrPrefix string) string {
	i := strings.Index(block, attrPrefix)
	if i < 0 {
		return ""
	}
	rest := block[i:]
	gt := strings.Index(rest, ">")
	if gt < 0 {
		return ""
	}
	rest = rest[gt+1:]
	if j := strings.Index(rest, "</div>"); j > 0 {
		rest = rest[:j]
	}
	return sogouClean(rest)
}

// sogouClean strips markup, unescapes entities and normalizes whitespace,
// truncating runaway blobs.
func sogouClean(in string) string {
	if len(in) > 500 {
		in = in[:500]
	}
	s := html.UnescapeString(stripTags(in))
	return strings.Join(strings.Fields(s), " ")
}

// looksLikeCode filters inline stylesheet blobs Sogou embeds in result blocks.
// Those contain braces and dense `name:value;` runs that never belong in a
// human-readable snippet. Real snippets use the CJK colon "：" and units like
// 元/m², so those must survive.
func looksLikeCode(s string) bool {
	if strings.ContainsAny(s, "{}") {
		return true
	}
	// CSS custom-property / declaration density: --x: y;--a: b;--c: d
	if strings.Count(s, ";") >= 2 && strings.Count(s, ":") >= 2 && strings.Contains(s, "--") {
		return true
	}
	return false
}

// trimLeadingJunk drops the attribute/tag tail left over when a block is cut
// in the middle of an opening tag (e.g. `">` from `class="str-info">`).
func trimLeadingJunk(s string) string {
	s = strings.TrimSpace(s)
	for {
		cut := strings.IndexAny(s, "<>\"")
		if cut != 0 {
			return s
		}
		s = strings.TrimSpace(s[1:])
	}
}

// stripTags removes HTML tags and drops <script>/<style> bodies so their
// source never leaks into a snippet.
func stripTags(in string) string {
	var b strings.Builder
	b.Grow(len(in))
	depth := 0 // >0 while inside <script>/<style>
	for i := 0; i < len(in); {
		lt := strings.IndexByte(in[i:], '<')
		if lt < 0 {
			if depth == 0 {
				b.WriteString(in[i:])
			}
			break
		}
		lt += i
		if depth == 0 {
			b.WriteString(in[i:lt])
		}
		switch {
		case strings.HasPrefix(in[lt:], "<script"):
			depth++
		case strings.HasPrefix(in[lt:], "</script"):
			if depth > 0 {
				depth--
			}
		case strings.HasPrefix(in[lt:], "<style"):
			depth++
		case strings.HasPrefix(in[lt:], "</style"):
			if depth > 0 {
				depth--
			}
		}
		gt := strings.IndexByte(in[lt:], '>')
		if gt < 0 {
			break
		}
		i = lt + gt + 1
	}
	return b.String()
}
