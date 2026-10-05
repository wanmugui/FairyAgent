package main

import (
	"os"
	"path/filepath"
	"testing"

	"agentloop/agent/internal/biz/tool/httptool"
)

// TestSaveSessionPreservesSessionID ?? SaveSession ?????????
// httptool ????? session_id ???
func TestSaveSessionPreservesSessionID(t *testing.T) {
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "chat.json")
	const sid = "11111111-1111-4111-8111-111111111111"
	if err := os.WriteFile(sessionFile, []byte(`{"messages":[],"model":"m","session_id":"`+sid+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveSession(sessionFile, nil, "m2"); err != nil {
		t.Fatal(err)
	}
	if got := httptool.LoadSessionID(sessionFile); got != sid {
		t.Fatalf("SaveSession dropped session_id: got %q want %q", got, sid)
	}
}

func TestLoadExistingSessionDropsHarnessErrorsFromModelContext(t *testing.T) {
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "chat.json")
	raw := `{
  "messages": [
    {"role":"system","content":"real system"},
    {"role":"user","content":"hello"},
    {"role":"system","content":"SYSTEM ERROR: harness exited 1: boom","internal_type":"harness_error"},
    {"role":"user","content":"SYSTEM ERROR: harness exited 2: legacy"},
    {"role":"assistant","content":"world"}
  ],
  "model": "m"
}`
	if err := os.WriteFile(sessionFile, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	messages, _ := loadExistingSession(sessionFile)
	if len(messages) != 3 {
		t.Fatalf("harness error should be dropped from model context, got %#v", messages)
	}
	for _, message := range messages {
		if message.Role == "user" && message.Content == "SYSTEM ERROR: harness exited 1: boom" {
			t.Fatalf("harness error was demoted to user context: %#v", messages)
		}
	}
}
