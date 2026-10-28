package webfetch

import (
	"strings"
	"testing"
)

// Regression cover for the extraction defect found by TestProbeRealSites.
//
// The old htmlToText compared whole tag text, so `<script type="text/javascript">`
// never matched "script" and the page's JavaScript was emitted as article prose.
// A live fetch of go.dev returned text beginning with
// "(function(w,d,s,l,i){w[l]=w[l]||[]..." — the model's first input was the
// site's own source code. These tests pin the fix.

func TestHTMLToTextStripsScriptWithAttributes(t *testing.T) {
	html := `<html><body><p>Real prose.</p>
<script type="text/javascript">var x = "IGNORE ME";</script>
<p>More prose.</p></body></html>`

	got := htmlToText(html)

	if strings.Contains(got, "IGNORE ME") {
		t.Fatalf("script body leaked into output:\n%s", got)
	}
	if !strings.Contains(got, "Real prose.") || !strings.Contains(got, "More prose.") {
		t.Fatalf("expected real prose to survive, got:\n%s", got)
	}
}

func TestHTMLToTextStripsSelfClosingAndAttrScript(t *testing.T) {
	cases := []struct{ name, html, leak string }{
		{"src attr", `<div><script src="/a.js"></script><p>ok</p></div>`, "/a.js"},
		{"async defer", `<script async defer src="//cdn/x.js">evil()</script><p>ok</p>`, "evil()"},
		{"style attr", `<style type="text/css">.a{color:red}</style><p>ok</p>`, "color:red"},
		{"noscript", `<noscript>enable js</noscript><p>ok</p>`, "enable js"},
		{"template", `<template><p>ghost</p></template><p>ok</p>`, "ghost"},
		{"svg", `<svg><text>vector junk</text></svg><p>ok</p>`, "vector junk"},
		{"head", `<html><head><title>Page Title</title><meta name="x" content="y"></head><body><p>ok</p></body></html>`, "Page Title"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := htmlToText(c.html)
			if strings.Contains(got, c.leak) {
				t.Fatalf("%s leaked: %q\nfull:\n%s", c.name, c.leak, got)
			}
			if !strings.Contains(got, "ok") {
				t.Fatalf("%s: real content lost:\n%s", c.name, got)
			}
		})
	}
}

func TestHTMLToTextBlockTagsWithAttributesBreakParagraphs(t *testing.T) {
	html := `<div class="a"><p>First para</p></div><div class="b"><p>Second para</p></div>`
	got := htmlToText(html)
	if !strings.Contains(got, "\n\n") {
		t.Fatalf("attributed block tags should still break paragraphs, got:\n%q", got)
	}
}

func TestHTMLToTextHeadDoesNotSwallowBody(t *testing.T) {
	// Regression guard for findCloseTag: a prefix match like </header> must not
	// be mistaken for the </head> closer.
	html := `<html><head><title>t</title></head><body><p>body text</p></body></html>`
	got := htmlToText(html)
	if !strings.Contains(got, "body text") {
		t.Fatalf("body swallowed by head skipping:\n%s", got)
	}
}

func TestHTMLToTextUnterminatedScriptDropsRemainder(t *testing.T) {
	html := `<p>kept</p><script type="text/javascript">never closed`
	got := htmlToText(html)
	if strings.Contains(got, "never closed") {
		t.Fatalf("unterminated script leaked:\n%s", got)
	}
	if !strings.Contains(got, "kept") {
		t.Fatalf("expected kept prose, got:\n%s", got)
	}
}

func TestHTMLToTextDoesNotConfuseScriptWithScriptish(t *testing.T) {
	html := `<script>real()</script><p>ok</p><scriptfoo>nope</scriptfoo>`
	got := htmlToText(html)
	if strings.Contains(got, "real()") {
		t.Fatalf("script body leaked:\n%s", got)
	}
}