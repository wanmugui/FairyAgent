package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSegmentedMemoryInteractionUsesStartDayAcrossMidnight(t *testing.T) {
	cfg := testSegmentedMemoryConfig(t.TempDir())
	startedAt := time.Date(2026, 9, 18, 23, 59, 59, 0, shanghaiTimeLocation)
	recorder := newTurnMemoryRecorderAt(cfg, filepath.Join(cfg.RepoRoot, "session.json"), false, startedAt)
	if recorder == nil {
		t.Fatal("recorder is nil")
	}
	defer recorder.Close()
	if got := recorder.sessionID; got != "2026-09-18" {
		t.Fatalf("session id = %q, want start day 2026-09-18", got)
	}
	if !strings.HasPrefix(recorder.interactionID, "req-20260918-") {
		t.Fatalf("interaction id = %q, want date-scoped id", recorder.interactionID)
	}
	recorder.RecordUserRequest("跨午夜交互")
	if _, err := os.Stat(filepath.Join(recorder.root, segmentedMemorySessionsDir, "2026-09-18", segmentedMemoryInteractionsDir, recorder.interactionID, segmentedMemoryManifestFile)); err != nil {
		t.Fatalf("manifest path does not use start day: %v", err)
	}
}

func TestSegmentedMemoryInvalidateAndCascadeDelete(t *testing.T) {
	cfg := testSegmentedMemoryConfig(t.TempDir())
	recorder := newTurnMemoryRecorderAt(cfg, filepath.Join(cfg.RepoRoot, "session.json"), false, time.Date(2026, 9, 18, 12, 0, 0, 0, shanghaiTimeLocation))
	if recorder == nil {
		t.Fatal("recorder is nil")
	}
	recorder.RecordUserRequest("上一条工具结果已经过期")
	recorder.AppendAssistantDelta("这是待修正的回复。", 1)
	recorder.EndStep(1)
	recorder.Close()

	root := recorder.root
	segments := readTestSegments(t, filepath.Join(root, segmentedMemorySessionsDir))
	if len(segments) < 2 {
		t.Fatalf("expected at least two segments, got %d", len(segments))
	}
	target := segments[0]
	result, err := invalidateSegmentedMemorySegment(root, target.InteractionID, target.ID, "工具结果已过期", "", "model")
	if err != nil {
		t.Fatalf("invalidate segment: %v", err)
	}
	if result["segment_id"] != target.ID {
		t.Fatalf("unexpected invalidation result: %#v", result)
	}
	entries := readAllSegmentedIndexEntries(t, root)
	invalidated := 0
	active := 0
	for _, entry := range entries {
		if entry.SegmentID != target.ID {
			continue
		}
		if entry.Status == segmentStatusInvalidated {
			invalidated++
		}
		if segmentedIndexEntryIsActive(&entry) {
			active++
		}
	}
	if invalidated == 0 || active != 0 {
		t.Fatalf("index status after invalidation: invalidated=%d active=%d", invalidated, active)
	}

	deleted, err := deleteSegmentedMemoryInteraction(root, target.InteractionID, "用户要求删除")
	if err != nil {
		t.Fatalf("delete interaction: %v", err)
	}
	if deleted["interaction_id"] != target.InteractionID {
		t.Fatalf("unexpected delete result: %#v", deleted)
	}
	if _, err := os.Stat(filepath.Join(root, segmentedMemorySessionsDir, target.SessionID, segmentedMemoryInteractionsDir, target.InteractionID)); !os.IsNotExist(err) {
		t.Fatalf("interaction directory still exists: %v", err)
	}
	for _, entry := range readAllSegmentedIndexEntries(t, root) {
		if entry.InteractionID == target.InteractionID || entry.TurnID == target.InteractionID {
			t.Fatalf("deleted interaction still has index entry: %#v", entry)
		}
	}
}

func TestSegmentedMemoryManagementToolSupportsSegmentAndInteractionDeletion(t *testing.T) {
	cfg := testSegmentedMemoryConfig(t.TempDir())
	recorder := newTurnMemoryRecorderAt(cfg, filepath.Join(cfg.RepoRoot, "session.json"), false, time.Now())
	if recorder == nil {
		t.Fatal("recorder is nil")
	}
	recorder.RecordUserRequest("需要删除的交互")
	recorder.Close()
	interactionID := recorder.InteractionID()
	segment := readTestSegments(t, filepath.Join(recorder.root, segmentedMemorySessionsDir))[0]

	tool := NewSegmentedMemoryManagementTool("memory_invalidate_segment", ToolDef{Type: "function", Function: map[string]any{"name": "memory_invalidate_segment"}}, cfg)
	args, _ := json.Marshal(map[string]any{"interaction_id": interactionID, "segment_id": segment.ID, "reason": "测试失效"})
	result, err := tool.Execute(t.Context(), ToolInvocation{Name: "memory_invalidate_segment", Args: args})
	if err != nil || result.IsError || result.Value["ok"] != true {
		t.Fatalf("invalidate tool result=%#v err=%v", result, err)
	}

	deleteTool := NewSegmentedMemoryManagementTool("memory_delete_interaction", ToolDef{Type: "function", Function: map[string]any{"name": "memory_delete_interaction"}}, cfg)
	args, _ = json.Marshal(map[string]any{"interaction_id": interactionID, "reason": "测试删除"})
	result, err = deleteTool.Execute(t.Context(), ToolInvocation{Name: "memory_delete_interaction", Args: args})
	if err != nil || result.IsError || result.Value["ok"] != true {
		t.Fatalf("delete tool result=%#v err=%v", result, err)
	}
}

func TestSegmentedMemorySegmentToolsRequireInteractionID(t *testing.T) {
	cfg := testSegmentedMemoryConfig(t.TempDir())
	for _, name := range []string{"memory_invalidate_segment", "memory_delete_segment"} {
		tool := NewSegmentedMemoryManagementTool(name, ToolDef{Type: "function", Function: map[string]any{"name": name}}, cfg)
		args, _ := json.Marshal(map[string]any{"segment_id": "s0001"})
		result, err := tool.Execute(t.Context(), ToolInvocation{Name: name, Args: args})
		if err == nil && !result.IsError {
			t.Fatalf("%s accepted an unscoped segment id: result=%#v err=%v", name, result, err)
		}
	}
}

func readAllSegmentedIndexEntries(t *testing.T, root string) []segmentedMemoryIndexEntry {
	t.Helper()
	var entries []segmentedMemoryIndexEntry
	err := filepath.WalkDir(filepath.Join(root, segmentedMemoryIndexDir), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || strings.ToLower(filepath.Ext(path)) != ".jsonl" {
			return walkErr
		}
		lines, readErr := readSegmentedIndexLines(path)
		if readErr != nil {
			return readErr
		}
		for _, line := range lines {
			var item segmentedMemoryIndexEntry
			if json.Unmarshal([]byte(line), &item) == nil {
				entries = append(entries, item)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read index entries: %v", err)
	}
	return entries
}
