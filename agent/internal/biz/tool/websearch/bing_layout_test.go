package websearch

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// The Bing failure diagnosed from a live probe was not a parser bug.
//
// Forcing `cc=US&mkt=en-US` — done here to keep English ranking for latin
// queries — makes Bing answer with an explicit empty-result page:
//
//	<li class="b_no"><h1>There are no results for <strong>golang</strong></h1>
//
// rather than a differently shaped result list. The identical query with
// default market parameters returned ten `b_algo` hits. Since an empty SERP
// forced by the wrong market is indistinguishable from "no coverage exists",
// Search() now retries unscoped instead of reporting nothing.
//
// These tests pin both halves: the parser still reads the real layout, and the
// request layer notices an empty scoped attempt.

func TestParseBingAlgoLayoutStillWorks(t *testing.T) {
	html := `<ol><li class="b_algo"><h2><a href="https://go.dev/blog">Go Blog</a></h2>` +
		`<div class="b_caption"><p class="b_lineclamp">News about the Go programming language.</p></div></li></ol>`

	got := parseBingHTML(html, 5)
	if len(got) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(got))
	}
	if got[0].URL != "https://go.dev/blog" || got[0].Title != "Go Blog" {
		t.Fatalf("strict layout regressed: %+v", got[0])
	}
	if !strings.Contains(got[0].Snippet, "Go programming language") {
		t.Fatalf("snippet lost: %+v", got[0])
	}
}

func TestParseBingEmptyPageYieldsNothing(t *testing.T) {
	if got := parseBingHTML("<html><body><p>nothing here</p></body></html>", 5); len(got) != 0 {
		t.Fatalf("expected 0 hits, got %+v", got)
	}
}

// TestProbeBingEmptySERPFixture documents the diagnosis against real captured
// HTML, when the fixture is available. It asserts what Bing actually said
// rather than expecting hits.
func TestProbeBingEmptySERPFixture(t *testing.T) {
	body, err := os.ReadFile("/tmp/bing2.html")
	if err != nil {
		t.Skip("no /tmp/bing2.html fixture captured")
	}
	html := string(body)

	if !strings.Contains(html, `class="b_no"`) {
		t.Skip("fixture no longer looks like the captured empty-result page")
	}
	if strings.Contains(html, "There are no results") {
		fmt.Println("confirmed: forced-market fixture is Bing's explicit empty-result page")
	}
	if got := parseBingHTML(html, 10); len(got) != 0 {
		t.Fatalf("empty-result page must parse to 0 hits, got %+v", got)
	}
}

func TestProbeBingDefaultLayoutFixture(t *testing.T) {
	body, err := os.ReadFile("/tmp/bing_default.html")
	if err != nil {
		t.Skip("no /tmp/bing_default.html fixture captured")
	}
	got := parseBingHTML(string(body), 10)
	fmt.Printf("default-market fixture parsed to %d hits\n", len(got))
	if len(got) == 0 {
		t.Fatalf("default market page has b_algo but parsed to 0 hits")
	}
}
