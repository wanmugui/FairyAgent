package webfetch

import (
	"strings"
	"testing"
)

func TestShouldTryRender(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		bodyText    string
		want        bool
	}{
		{
			name:        "html shell too thin to be real content",
			contentType: "text/html; charset=utf-8",
			bodyText:    "Loading…",
			want:        true,
		},
		{
			name:        "html with real content is left alone",
			contentType: "text/html; charset=utf-8",
			bodyText:    strings.Repeat("金沙洲南小区均价29552元/平米。", 100),
			want:        false,
		},
		{
			name:        "json never benefits from rendering",
			contentType: "application/json",
			bodyText:    "{}",
			want:        false,
		},
		{
			name:        "plain text never benefits",
			contentType: "text/plain",
			bodyText:    "hi",
			want:        false,
		},
		{
			name:        "404 page is not worth a browser launch",
			contentType: "text/html",
			bodyText:    "404 Not Found",
			want:        false,
		},
		{
			name:        "captcha wall is left alone rather than re-rendered",
			contentType: "text/html",
			bodyText:    "人机验证 请完成验证",
			want:        false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldTryRender(c.contentType, c.bodyText); got != c.want {
				t.Errorf("shouldTryRender(%q, %.20q) = %v, want %v",
					c.contentType, c.bodyText, got, c.want)
			}
		})
	}
}

func TestTryRenderDegradesWithoutRepoRoot(t *testing.T) {
	// No FAIRY_REPO_ROOT must be a clean error, never a panic: webfetch has to
	// keep working on hosts where the browser toolchain isn't installed.
	t.Setenv("FAIRY_REPO_ROOT", "")
	if _, err := tryRender(t.Context(), "https://example.com/"); err == nil {
		t.Error("want an error when FAIRY_REPO_ROOT is unset")
	}
}
