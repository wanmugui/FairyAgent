package local

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteLocalSubtaskTerminalStateMarksCompleted(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "job.json")
	manifest := map[string]any{
		"job_id": "abc",
		"title":  "test subtask",
		"status": "running",
		"pid":    999999,
	}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	writeLocalSubtaskTerminalState(manifestPath, 999999)

	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["status"] != "completed" {
		t.Fatalf("status must become completed, got %#v", got["status"])
	}
	if _, ok := got["finished_at"]; !ok {
		t.Fatalf("finished_at must be recorded: %#v", got)
	}
	if got["title"] != "test subtask" {
		t.Fatalf("other fields must be preserved: %#v", got)
	}
}

func TestWriteLocalSubtaskTerminalStateDoesNotOverwriteFinished(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "job.json")
	manifest := map[string]any{
		"status":    "failed",
		"exit_code": 2,
	}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	writeLocalSubtaskTerminalState(manifestPath, 999999)

	raw, _ := os.ReadFile(manifestPath)
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	if got["status"] != "failed" {
		t.Fatalf("a finished manifest must not be rewritten, got %#v", got["status"])
	}
	if got["exit_code"] != float64(2) {
		t.Fatalf("exit_code must be preserved, got %#v", got["exit_code"])
	}
}

func TestWatchLocalSubtaskManifestFinishesWhenProcessDies(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "job.json")
	data, _ := json.MarshalIndent(map[string]any{
		"status": "running",
		"title":  "watched",
	}, "", "  ")
	if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	// pid 1 exists but is not ours to kill; use a pid that is definitely
	// gone so the watcher exits on its first poll.
	go watchLocalSubtaskManifest(manifestPath, 999998)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(manifestPath)
		if err == nil {
			var got map[string]any
			if json.Unmarshal(raw, &got) == nil {
				if status, _ := got["status"].(string); status != "" && status != "running" {
					return // success: the manifest was updated
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("watcher never wrote the terminal state")
}

func TestLocalProcessAlive(t *testing.T) {
	if !localProcessAlive(os.Getpid()) {
		t.Error("the current process must be reported alive")
	}
	if localProcessAlive(999999) {
		t.Error("a nonexistent pid must be reported dead")
	}
}
