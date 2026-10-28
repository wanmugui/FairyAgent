package builtin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMemorySearchUsesSegmentedKeyIndexFirst(t *testing.T) {
	memoryRoot := t.TempDir()
	indexDir := filepath.Join(memoryRoot, "segmented", "index")
	if err := os.MkdirAll(indexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"key":"修复登录问题","target":"memory://segmented/segments/turn/turn-s0001.json","type":"segment","kind":"user_request","key_status":"llm"}`
	if err := os.WriteFile(filepath.Join(indexDir, "root.jsonl"), []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewLocalMemorySearchTool(localFileTestSchema("memory_search"), memoryRoot)
	result := executeLocalFileTool(t, tool, t.TempDir(), `{"query":"登录"}`)
	if result.IsError {
		t.Fatalf("memory_search failed: %v", result.Value)
	}
	hits, ok := result.Value["results"].([]memoryHit)
	if !ok || len(hits) == 0 {
		t.Fatalf("expected segmented index hit, got %#v", result.Value["results"])
	}
	if hits[0].Key != "修复登录问题" || hits[0].Target != "memory://segmented/segments/turn/turn-s0001.json" {
		t.Fatalf("unexpected index hit: %#v", hits[0])
	}
}

func TestMemorySearchFiltersInvalidatedSegments(t *testing.T) {
	memoryRoot := t.TempDir()
	indexDir := filepath.Join(memoryRoot, "segmented", "index")
	if err := os.MkdirAll(indexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	active := `{"key":"新的有效结论","target":"memory://segmented/sessions/2026-09-18/interactions/req-1/s0001.json","type":"segment","segment_id":"s0001","interaction_id":"req-1","status":"active"}`
	invalid := `{"key":"已经过期的结论","target":"memory://segmented/sessions/2026-09-18/interactions/req-1/s0002.json","type":"segment","segment_id":"s0002","interaction_id":"req-1","status":"invalidated"}`
	if err := os.WriteFile(filepath.Join(indexDir, "root.jsonl"), []byte(active+"\n"+invalid+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewLocalMemorySearchTool(localFileTestSchema("memory_search"), memoryRoot)
	result := executeLocalFileTool(t, tool, t.TempDir(), `{"query":"结论"}`)
	if result.IsError {
		t.Fatalf("memory_search failed: %v", result.Value)
	}
	hits, ok := result.Value["results"].([]memoryHit)
	if !ok {
		t.Fatalf("results has unexpected type: %#v", result.Value["results"])
	}
	var activeHit *memoryHit
	for i := range hits {
		if hits[i].SegmentID == "s0002" {
			t.Fatalf("invalidated segment leaked into search: %#v", hits[i])
		}
		if hits[i].SegmentID == "s0001" {
			activeHit = &hits[i]
		}
	}
	if activeHit == nil || activeHit.InteractionID != "req-1" || activeHit.Status != "active" {
		t.Fatalf("unexpected active hit: %#v", hits)
	}
}
