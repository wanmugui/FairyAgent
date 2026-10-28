package websearch

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// writeFixture emits a DDG result page big enough to clear the provider's
// anti-bot length gate (4096 bytes) so request assertions actually execute.
func writeFixture(w http.ResponseWriter) {
	var b strings.Builder
	b.WriteString(`<html><body>`)
	for i := 0; i < 40; i++ {
		b.WriteString(`<div class="result"><a class="result__a" href="https://x.test/a">A</a>`)
		b.WriteString(`<a class="result__snippet">some snippet text to pad the page body</a></div>`)
	}
	b.WriteString(`</body></html>`)
	io.WriteString(w, b.String())
}

// The DDG provider used to hardcode kl=us-en, so a CJK query was restricted to
// US results -- the same defect fixed on the Bing provider. A CJK query must
// carry the Chinese region hint, and a latin query must keep the default.
func TestDDGRegionFollowsQueryScript(t *testing.T) {
	cases := []struct {
		query string
		want  string
	}{
		{"Blender 角色建模 教程", "cn-zh"},
		{"角色建模", "cn-zh"},
		{"blender character modeling tutorial", "us-en"},
		{"", "us-en"},
	}
	for _, c := range cases {
		if got := ddgRegion(c.query); got != c.want {
			t.Errorf("ddgRegion(%q) = %q, want %q", c.query, got, c.want)
		}
	}
}

// The outgoing form must carry the region hint that ddgRegion chose, and the
// query must survive URL encoding intact.
func TestDDGRequestCarriesRegionAndQuery(t *testing.T) {
	var gotForm url.Values
	var gotLang string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotForm = r.PostForm
		gotLang = r.Header.Get("Accept-Language")
		// The provider rejects bodies under 4096 bytes as bot detection, so the
		// fixture must clear that gate for the request assertions to run.
		writeFixture(w)
	}))
	defer srv.Close()

	restore := ddgEndpoint
	ddgEndpoint = srv.URL
	defer func() { ddgEndpoint = restore }()

	p := &DuckDuckGoProvider{client: srv.Client()}
	const q = "Blender 角色建模 教程"
	if _, err := p.Search(context.Background(), q, 3); err != nil {
		t.Fatalf("search: %v", err)
	}

	if got := gotForm.Get("q"); got != q {
		t.Errorf("query mangled: got %q want %q", got, q)
	}
	if got := gotForm.Get("kl"); got != "cn-zh" {
		t.Errorf("kl = %q, want cn-zh for a CJK query", got)
	}
	if gotLang == "" || gotLang[:2] != "zh" {
		t.Errorf("Accept-Language = %q, want zh-first for a CJK query", gotLang)
	}
}

// A pure-ASCII query must keep the us-en default and English-first headers.
func TestDDGRequestASCIIKeepsDefault(t *testing.T) {
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotForm = r.PostForm
		// The provider rejects bodies under 4096 bytes as bot detection, so the
		// fixture must clear that gate for the request assertions to run.
		writeFixture(w)
	}))
	defer srv.Close()

	restore := ddgEndpoint
	ddgEndpoint = srv.URL
	defer func() { ddgEndpoint = restore }()

	p := &DuckDuckGoProvider{client: srv.Client()}
	if _, err := p.Search(context.Background(), "blender character modeling", 3); err != nil {
		t.Fatalf("search: %v", err)
	}
	if got := gotForm.Get("kl"); got != "us-en" {
		t.Errorf("kl = %q, want us-en for an ASCII query", got)
	}
}
