package websearch

import "testing"

func TestRelaxedQueriesForCompoundCJK(t *testing.T) {
	got := relaxedQueries("Blender 角色建模 教程")

	// Shortest CJK run first, because a generic phrasing is the one the
	// endpoint actually understands.
	if len(got) == 0 {
		t.Fatal("expected at least one relaxed query")
	}
	if got[0] != "Blender 教程" {
		t.Errorf("expected shortest-run variant first, got %q", got[0])
	}
	if len(got) > 1 && got[1] != "Blender 角色建模" {
		t.Errorf("second variant = %q, want %q", got[1], "Blender 角色建模")
	}
}

func TestRelaxedQueriesSkipsShortQueries(t *testing.T) {
	// Two-part queries are the shape the endpoint answers well. Relaxing
	// them would spend a request to add nothing.
	for _, q := range []string{"Blender 教程", "blender tutorial", "教程"} {
		if got := relaxedQueries(q); got != nil {
			t.Errorf("relaxedQueries(%q) = %v, want nil", q, got)
		}
	}
}

func TestRelaxedQueriesNeverEchoesOriginal(t *testing.T) {
	got := relaxedQueries("Blender 教程")
	for _, v := range got {
		if normalizeQuery(v) == normalizeQuery("Blender 教程") {
			t.Errorf("variant %q repeats the original query", v)
		}
	}
}

func TestSplitQueryParts(t *testing.T) {
	latin, cjk := splitQueryParts("Blender 角色建模 教程")
	if len(latin) != 1 || latin[0] != "Blender" {
		t.Errorf("latin = %v, want [Blender]", latin)
	}
	if len(cjk) != 2 || cjk[0] != "角色建模" || cjk[1] != "教程" {
		t.Errorf("cjk = %v, want [角色建模 教程]", cjk)
	}
}

// The exact shape that motivated this file: the long phrasing returns only
// blender.org landing pages, every one of which engages the query through the
// single generic term.
func TestAnswerIsWeakOnGenericEcho(t *testing.T) {
	sources := []map[string]any{
		{"title": "Blender 官方下载", "url": "https://www.blender.org/download/"},
		{"title": "Blender® - The Free 3D Software Suite", "url": "https://www.blender.org/"},
	}
	if !answerIsWeak(sources, "Blender 角色建模 教程") {
		t.Error("generic echo set should be judged weak")
	}
}

func TestAnswerIsWeakFalseWhenOnTopic(t *testing.T) {
	sources := []map[string]any{
		{"title": "Blender 官网", "url": "https://www.blender.org/"},
		{"title": "Blender 角色建模教程 - 三渲二角色制作", "url": "https://tutorial.example/role"},
	}
	if answerIsWeak(sources, "Blender 角色建模 教程") {
		t.Error("set containing an on-topic source must not be judged weak")
	}
}

// A single-term query has no conjunction to ignore.
func TestAnswerIsWeakSingleTerm(t *testing.T) {
	sources := []map[string]any{{"title": "Blender", "url": "https://www.blender.org/"}}
	if answerIsWeak(sources, "blender") {
		t.Error("single-term query must never be judged weak")
	}
}

func TestMergeSearchSourcesPrefersFirstAndDedups(t *testing.T) {
	// Reaching the merge at all means the first answer was a weak recall, so
	// the first list here is the relaxed-query result and must lead.
	preferred := []map[string]any{
		{"title": "Blender 角色建模教程", "url": "https://tutorial.example/role/"},
	}
	fallback := []map[string]any{
		{"title": "Blender 官网", "url": "https://www.blender.org/"},
		{"title": "重复的教程", "url": "https://tutorial.example/role"}, // dup incl. trailing slash
	}
	got := mergeSearchSources(preferred, fallback, 10)
	if len(got) != 2 {
		t.Fatalf("want 2 after dedupe, got %d: %+v", len(got), got)
	}
	if got[0]["url"] != "https://tutorial.example/role/" {
		t.Errorf("preferred list must lead, got %v", got[0]["url"])
	}
}

func TestMergeSearchSourcesRespectsMaxResults(t *testing.T) {
	a := []map[string]any{{"url": "https://a.example/"}}
	b := []map[string]any{{"url": "https://b.example/"}, {"url": "https://c.example/"}}
	if got := mergeSearchSources(a, b, 2); len(got) != 2 {
		t.Errorf("want max 2, got %d", len(got))
	}
}
