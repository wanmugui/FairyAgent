package main

import (
	"path/filepath"
	"testing"
)

func TestSessionEventLogAppendAndReplay(t *testing.T) {
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "chat-test", "chat-test.json")
	log, err := OpenSessionEventLog(sessionFile)
	if err != nil {
		t.Fatal(err)
	}
	_ = log.Append(map[string]any{"type": "session_start", "model": "MiniMax-M3"})
	_ = log.Append(map[string]any{"type": "message", "role": "user", "content": "你好"})
	_ = log.Append(map[string]any{"type": "message", "role": "assistant", "content": "我是Fairy", "step": 3})
	_ = log.Append(map[string]any{"type": "tool_result", "tool": "bash", "call_id": "c1", "content": "ok"})
	_ = log.Append(map[string]any{"type": "summary", "content": "摘要"})
	_ = log.Append(map[string]any{"type": "turn_end"})
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	msgs, err := ReplaySessionEvents(sessionFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("replay messages = %d, want 2 (tool/summary events skipped)", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "你好" {
		t.Fatalf("msg0 = %#v", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "我是Fairy" {
		t.Fatalf("msg1 = %#v", msgs[1])
	}
	if msgs[1].Step != 3 {
		t.Fatalf("msg1 step = %d, want 3", msgs[1].Step)
	}
	if msgs[1].Ts <= 0 {
		t.Fatal("msg1 ts should be replayed from event log")
	}
}

func TestSessionEventLogMissingFile(t *testing.T) {
	if _, err := ReplaySessionEvents(filepath.Join(t.TempDir(), "nope", "x.json")); err == nil {
		t.Fatal("expected error for missing event log")
	}
}
