package websearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A CJK query must not be sent to Bing with US-English market and language
// hints. Doing so makes Bing treat the query as a vague English-entity lookup:
// "Blender 角色建模 教程" came back as blender.org download pages, and any
// unrelated query came back as dictionary definitions of the first word.
func TestBingRequestLanguageMatchesQuery(t *testing.T) {
	var got struct {
		rawQuery string
		mkt      string
		setlang  string
		cc       string
		accept   string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		got.rawQuery = q.Get("q")
		got.mkt = q.Get("mkt")
		got.setlang = q.Get("setlang")
		got.cc = q.Get("cc")
		got.accept = r.Header.Get("Accept-Language")
		w.Write([]byte("<html><body><h2><a href=\"https://x.test/a\">A</a></h2></body></html>"))
	}))
	defer srv.Close()

	p := &BingPublicProvider{client: srv.Client()}
	// Point the provider at the test server by overriding its endpoint.
	restore := bingEndpoint
	bingEndpoint = srv.URL
	defer func() { bingEndpoint = restore }()

	_, _ = p.Search(context.Background(), "Blender 角色建模 教程", 3)

	if got.rawQuery != "Blender 角色建模 教程" {
		t.Errorf("query mangled: got %q", got.rawQuery)
	}
	if got.mkt == "en-US" {
		t.Errorf("CJK query sent with mkt=en-US, want zh-CN: got mkt=%q setlang=%q cc=%q accept=%q",
			got.mkt, got.setlang, got.cc, got.accept)
	}
	if strings.HasPrefix(got.accept, "en-US") {
		t.Errorf("CJK query sent with English-first Accept-Language: %q", got.accept)
	}
}

// A pure-ASCII query must keep working; do not regress the default path.
func TestBingRequestEnglishQueryUnchanged(t *testing.T) {
	var got struct {
		mkt    string
		accept string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		got.mkt = q.Get("mkt")
		got.accept = r.Header.Get("Accept-Language")
		w.Write([]byte("<html><body><h2><a href=\"https://x.test/a\">A</a></h2></body></html>"))
	}))
	defer srv.Close()

	p := &BingPublicProvider{client: srv.Client()}
	restore := bingEndpoint
	bingEndpoint = srv.URL
	defer func() { bingEndpoint = restore }()

	_, _ = p.Search(context.Background(), "blender character modeling tutorial", 3)

	if got.mkt == "" {
		t.Errorf("mkt missing for ASCII query: %q", got.mkt)
	}
	_ = time.Second
}
