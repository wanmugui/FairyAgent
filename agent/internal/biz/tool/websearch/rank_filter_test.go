package websearch

import "testing"

// A CJK query such as "Blender 角色建模 教程" yields bigram terms like 角色, 建模,
// 教程. Bing's public HTML endpoint often answers those with generic pages --
// blender.org download pages, a dictionary definition of the first word -- that
// match only the most generic term. Those were passed through unchanged, so the
// caller received results unrelated to what it asked.
func TestFilterGenericEchoRemovesSingleTermEcho(t *testing.T) {
	sources := []map[string]any{
		{"title": "Blender 官方下载", "url": "https://www.blender.org/download/"},
		{"title": "角色的释义", "url": "https://dict.example/character"},
		{"title": "角色建模教程 - 从零学三渲二", "url": "https://tutorial.example/role"},
	}
	got := filterGenericEcho(sources, "Blender 角色建模 教程")

	if len(got) != 1 {
		t.Fatalf("expected only the on-topic source, got %d: %+v", len(got), got)
	}
	if got[0]["url"] != "https://tutorial.example/role" {
		t.Errorf("wrong survivor: %v", got[0]["url"])
	}
}

// selectSources is the shared sort+dedupe path and its contract is to keep
// every result. Dropping weak matches there would break URL-collapse
// expectations, which is why filtering happens at the tool boundary instead.
func TestSelectSourcesKeepsAllForDedup(t *testing.T) {
	results := []SearchResult{
		{Title: "Blender docs", URL: "https://docs.blender.org/"},
		{Title: "Blender docs mirror", URL: "https://docs.blender.org/"},
		{Title: "unrelated page", URL: "https://other.example/"},
	}
	got := rankAndFilter(results, "blender", 10)
	if len(got) != 2 {
		t.Errorf("rankAndFilter must not drop results (want 2 after dedupe), got %d", len(got))
	}
}

// A single-term query has nothing to be weak against: keep everything.
func TestFilterGenericEchoSingleTermKeepsAll(t *testing.T) {
	sources := []map[string]any{
		{"title": "Blender 3D Software", "url": "https://www.blender.org/"},
		{"title": "Something else", "url": "https://other.example/x"},
	}
	if got := filterGenericEcho(sources, "blender"); len(got) != 2 {
		t.Errorf("single-term query must keep all, got %d", len(got))
	}
}

// When nothing matches the query, return the input rather than an empty slice:
// a weak result set beats no result set.
func TestFilterGenericEchoFallsBack(t *testing.T) {
	sources := []map[string]any{
		{"title": "Blender Download", "url": "https://www.blender.org/download/"},
		{"title": "Dictionary", "url": "https://dict.example/x"},
	}
	if got := filterGenericEcho(sources, "角色建模"); len(got) != 2 {
		t.Errorf("expected fallback to keep 2, got %d", len(got))
	}
}
