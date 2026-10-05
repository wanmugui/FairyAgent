package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSaveSessionSerialisesConcurrentWrites(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "session.json")

	// Fire N parallel saves to the same path; the per-file mutex must
	// serialise them so no two writes interleave and the file is always a
	// valid JSON snapshot of one of the payloads.
	const N = 32
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			msgs := []Message{{Role: "user", Content: "msg"}}
			if err := SaveSession(path, msgs, "m"); err != nil {
				t.Errorf("save %d failed: %v", i, err)
			}
		}()
	}
	wg.Wait()

	// Verify the final file is well-formed JSON with the expected shape.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("final file unreadable: %v", err)
	}
	var session struct {
		Messages []Message `json:"messages"`
		Model    string    `json:"model"`
	}
	if err := json.Unmarshal(data, &session); err != nil {
		t.Fatalf("final file is invalid JSON: %v\nbody=%s", err, data)
	}
	if session.Model != "m" || len(session.Messages) != 1 {
		t.Fatalf("unexpected payload: model=%q messages=%d", session.Model, len(session.Messages))
	}
}

func TestSaveSessionParallelDifferentPaths(t *testing.T) {
	// Different sessions must not contend on the same mutex.
	tmp := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			path := filepath.Join(tmp, "session-"+string(rune('a'+i))+".json")
			if err := SaveSession(path, []Message{{Role: "user", Content: "x"}}, "m"); err != nil {
				t.Errorf("save %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
}

// Regression: the context-compression checkpoint is a machine-generated user-role
// message. It used to be sanitised (which deletes the whole <summary> block) and
// then dropped as "empty user input", so it never reached the session snapshot
// the UI reads — the tool chain lost its "摘要×N" entry.
func TestSaveSessionPersistsContextSummaryCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")

	checkpoint := NewMessage("user", "<summary>\n压缩后的历史上下文\n</summary>", nil, "", "")
	checkpoint.InternalType = internalTypeContextSummary

	msgs := []Message{
		{Role: "user", Content: "第一条真实输入"},
		checkpoint,
		{Role: "assistant", Content: "好的"},
	}
	if err := SaveSession(path, msgs, "m"); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("session snapshot unreadable: %v", err)
	}
	var session struct {
		Messages []Message `json:"messages"`
	}
	if err := json.Unmarshal(data, &session); err != nil {
		t.Fatalf("session snapshot is invalid JSON: %v", err)
	}

	found := -1
	for i, m := range session.Messages {
		if m.InternalType == internalTypeContextSummary {
			found = i
		}
	}
	if found < 0 {
		t.Fatalf("context summary checkpoint missing from snapshot: %#v", session.Messages)
	}
	if !strings.Contains(session.Messages[found].Content, "压缩后的历史上下文") {
		t.Fatalf("checkpoint body was stripped: %q", session.Messages[found].Content)
	}
}