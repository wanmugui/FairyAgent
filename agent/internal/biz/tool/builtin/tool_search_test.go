package builtin

import (
	"testing"

	"agentloop/agent/internal/dtypes"
)

// fakeDeferredIndex is a minimal in-memory DeferredIndex for tests.
type fakeDeferredIndex struct {
	entries  []dtypes.DeferredToolInfo
	exposed  map[string]bool
	exposeErr error
}

func newFakeDeferredIndex(entries ...dtypes.DeferredToolInfo) *fakeDeferredIndex {
	return &fakeDeferredIndex{entries: entries, exposed: map[string]bool{}}
}

func (f *fakeDeferredIndex) ListDeferred() []dtypes.DeferredToolInfo {
	remaining := make([]dtypes.DeferredToolInfo, 0, len(f.entries))
	for _, entry := range f.entries {
		if f.exposed[entry.Name] {
			continue
		}
		remaining = append(remaining, entry)
	}
	return remaining
}

func (f *fakeDeferredIndex) Expose(name string) bool {
	if f.exposeErr != nil {
		return false
	}
	for _, entry := range f.entries {
		if entry.Name == name {
			f.exposed[name] = true
			return true
		}
	}
	return false
}

func TestToolSearchFindsDeferredToolByQuery(t *testing.T) {
	index := newFakeDeferredIndex(
		dtypes.DeferredToolInfo{Name: "rea__analyze_function", Description: "analyze a native function", Group: "rea"},
		dtypes.DeferredToolInfo{Name: "rea__trace_call_path", Description: "trace a call path", Group: "rea"},
		dtypes.DeferredToolInfo{Name: "other__thing", Description: "unrelated capability", Group: "other"},
	)
	tool := NewLocalToolSearchTool(localFileTestSchema("tool_search"), index)
	result := executeLocalFileTool(t, tool, t.TempDir(), `{"query":"analyze function"}`)
	if result.IsError {
		t.Fatalf("tool_search failed: %v", result.Value)
	}
	results, ok := result.Value["results"].([]toolSearchEntry)
	if !ok {
		t.Fatalf("results has unexpected type: %T", result.Value["results"])
	}
	if len(results) == 0 {
		t.Fatal("tool_search returned no hits")
	}
	if results[0].Name != "rea__analyze_function" {
		t.Fatalf("top hit = %q, want rea__analyze_function", results[0].Name)
	}
}

func TestToolSearchUnlocksHits(t *testing.T) {
	index := newFakeDeferredIndex(
		dtypes.DeferredToolInfo{Name: "rea__analyze_function", Description: "analyze a native function", Group: "rea"},
	)
	tool := NewLocalToolSearchTool(localFileTestSchema("tool_search"), index)
	result := executeLocalFileTool(t, tool, t.TempDir(), `{"query":"analyze"}`)
	if result.IsError {
		t.Fatalf("tool_search failed: %v", result.Value)
	}
	if !index.exposed["rea__analyze_function"] {
		t.Fatal("tool_search must unlock the tools it returns")
	}
	unlocked, ok := result.Value["unlocked"].([]string)
	if !ok {
		t.Fatalf("unlocked has unexpected type: %T", result.Value["unlocked"])
	}
	if len(unlocked) != 1 || unlocked[0] != "rea__analyze_function" {
		t.Fatalf("unlocked = %#v", unlocked)
	}
	results, _ := result.Value["results"].([]toolSearchEntry)
	if len(results) != 1 || !results[0].Exposed {
		t.Fatalf("result must report the tool as exposed: %#v", results)
	}
}

func TestToolSearchReturnsAllWhenQueryEmpty(t *testing.T) {
	index := newFakeDeferredIndex(
		dtypes.DeferredToolInfo{Name: "rea__one", Description: "first", Group: "rea"},
		dtypes.DeferredToolInfo{Name: "rea__two", Description: "second", Group: "rea"},
	)
	tool := NewLocalToolSearchTool(localFileTestSchema("tool_search"), index)
	result := executeLocalFileTool(t, tool, t.TempDir(), `{"query":""}`)
	if result.IsError {
		t.Fatalf("tool_search failed: %v", result.Value)
	}
	results, _ := result.Value["results"].([]toolSearchEntry)
	if len(results) != 2 {
		t.Fatalf("empty query must return every deferred tool, got %d", len(results))
	}
}

func TestToolSearchReportsSiblings(t *testing.T) {
	index := newFakeDeferredIndex(
		dtypes.DeferredToolInfo{Name: "rea__one", Description: "first", Group: "rea"},
		dtypes.DeferredToolInfo{Name: "rea__two", Description: "second", Group: "rea"},
		dtypes.DeferredToolInfo{Name: "other__three", Description: "third", Group: "other"},
	)
	tool := NewLocalToolSearchTool(localFileTestSchema("tool_search"), index)
	result := executeLocalFileTool(t, tool, t.TempDir(), `{"query":"first"}`)
	if result.IsError {
		t.Fatalf("tool_search failed: %v", result.Value)
	}
	results, _ := result.Value["results"].([]toolSearchEntry)
	if len(results) == 0 {
		t.Fatal("tool_search returned no hits")
	}
	siblings := results[0].Siblings
	if len(siblings) != 1 || siblings[0] != "rea__two" {
		t.Fatalf("siblings = %#v, want [rea__two]", siblings)
	}
}

func TestToolSearchNoMatchLeavesCatalogueUnlocked(t *testing.T) {
	index := newFakeDeferredIndex(
		dtypes.DeferredToolInfo{Name: "rea__analyze_function", Description: "analyze a native function", Group: "rea"},
	)
	tool := NewLocalToolSearchTool(localFileTestSchema("tool_search"), index)
	result := executeLocalFileTool(t, tool, t.TempDir(), `{"query":"completely unrelated zodiac sign"}`)
	if result.IsError {
		t.Fatalf("tool_search failed: %v", result.Value)
	}
	results, _ := result.Value["results"].([]toolSearchEntry)
	if len(results) != 0 {
		t.Fatalf("expected no hits, got %#v", results)
	}
	if len(index.exposed) != 0 {
		t.Fatalf("no tool may be unlocked without a hit, got %#v", index.exposed)
	}
}

func TestToolSearchHonoursLimit(t *testing.T) {
	index := newFakeDeferredIndex(
		dtypes.DeferredToolInfo{Name: "rea__a", Description: "alpha capability", Group: "rea"},
		dtypes.DeferredToolInfo{Name: "rea__b", Description: "alpha capability", Group: "rea"},
		dtypes.DeferredToolInfo{Name: "rea__c", Description: "alpha capability", Group: "rea"},
	)
	tool := NewLocalToolSearchTool(localFileTestSchema("tool_search"), index)
	result := executeLocalFileTool(t, tool, t.TempDir(), `{"query":"alpha","limit":2}`)
	if result.IsError {
		t.Fatalf("tool_search failed: %v", result.Value)
	}
	results, _ := result.Value["results"].([]toolSearchEntry)
	if len(results) != 2 {
		t.Fatalf("limit 2 must cap the results, got %d", len(results))
	}
}

func TestToolSearchWithoutIndexReportsError(t *testing.T) {
	tool := NewLocalToolSearchTool(localFileTestSchema("tool_search"), nil)
	result := executeLocalFileTool(t, tool, t.TempDir(), `{"query":"anything"}`)
	if !result.IsError {
		t.Fatal("tool_search must report an error when the index is unavailable")
	}
}

func TestToolSearchTruncatesLongDescriptions(t *testing.T) {
	long := ""
	for i := 0; i < 2000; i++ {
		long += "x"
	}
	index := newFakeDeferredIndex(
		dtypes.DeferredToolInfo{Name: "rea__verbose", Description: long, Group: "rea"},
	)
	tool := NewLocalToolSearchTool(localFileTestSchema("tool_search"), index)
	result := executeLocalFileTool(t, tool, t.TempDir(), `{"query":"verbose"}`)
	if result.IsError {
		t.Fatalf("tool_search failed: %v", result.Value)
	}
	results, _ := result.Value["results"].([]toolSearchEntry)
	if len(results) != 1 {
		t.Fatalf("expected one hit, got %d", len(results))
	}
	if len(results[0].Description) > toolSearchMaxDescription+3 {
		t.Fatalf("description was not truncated: %d chars", len(results[0].Description))
	}
}

func TestToolSearchCapsSiblingCount(t *testing.T) {
	entries := make([]dtypes.DeferredToolInfo, 0, 40)
	for i := 0; i < 40; i++ {
		entries = append(entries, dtypes.DeferredToolInfo{
			Name:        "rea__tool" + string(rune('a'+i%26)) + string(rune('a'+i/26)),
			Description: "shared capability",
			Group:       "rea",
		})
	}
	index := newFakeDeferredIndex(entries...)
	tool := NewLocalToolSearchTool(localFileTestSchema("tool_search"), index)
	result := executeLocalFileTool(t, tool, t.TempDir(), `{"query":"shared capability","limit":1}`)
	if result.IsError {
		t.Fatalf("tool_search failed: %v", result.Value)
	}
	results, _ := result.Value["results"].([]toolSearchEntry)
	if len(results) != 1 {
		t.Fatalf("expected one hit, got %d", len(results))
	}
	if len(results[0].Siblings) > toolSearchMaxSiblings {
		t.Fatalf("siblings not capped: %d", len(results[0].Siblings))
	}
}

func TestToolSearchScorePrefersNameMatches(t *testing.T) {
	nameHit := toolSearchScore("analyze", "rea__analyze_function", "generic description", "rea")
	descHit := toolSearchScore("analyze", "rea__other", "analyze something", "rea")
	if nameHit <= descHit {
		t.Fatalf("name match must outrank description match: %d vs %d", nameHit, descHit)
	}
	if toolSearchScore("", "rea__x", "y", "rea") <= 0 {
		t.Fatal("an empty query must still produce a positive score")
	}
}
