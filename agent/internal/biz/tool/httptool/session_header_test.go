package httptool

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestHTTPToolAddsStableSessionIDHeader(t *testing.T) {
	var got []string
	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("X-FAIRY-Session-ID"))
		_, _ = w.Write([]byte(`{"tool_call_id":"call_test","result":"{\"ok\":true}"}`))
	}))
	defer server.Close()

	tool := testHTTPTool(server.URL, "test_tool", 1)

	// 同一会话文件：两次调用应该带同一个 session id
	_ = executeHTTPTool(t, tool, ToolInvocation{CallID: "c1", Name: "test_tool", SessionFile: "/sessions/chat-A.json", Args: json.RawMessage(`{}`)})
	_ = executeHTTPTool(t, tool, ToolInvocation{CallID: "c2", Name: "test_tool", SessionFile: "/sessions/chat-A.json", Args: json.RawMessage(`{}`)})
	// 不同会话文件：应该带不同的 session id
	_ = executeHTTPTool(t, tool, ToolInvocation{CallID: "c3", Name: "test_tool", SessionFile: "/sessions/chat-B.json", Args: json.RawMessage(`{}`)})

	if len(got) != 3 {
		t.Fatalf("expected 3 requests, got %d", len(got))
	}
	for i, id := range got {
		if id == "" {
			t.Fatalf("request %d missing X-FAIRY-Session-ID", i)
		}
	}
	if got[0] != got[1] {
		t.Fatalf("same session should reuse same session id: %q vs %q", got[0], got[1])
	}
	if got[0] == got[2] {
		t.Fatalf("different sessions should have different session ids: %q vs %q", got[0], got[2])
	}
}

func TestHTTPToolDoesNotOverrideExplicitSessionID(t *testing.T) {
	var got string
	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-FAIRY-Session-ID")
		_, _ = w.Write([]byte(`{"tool_call_id":"call_test","result":"{\"ok\":true}"}`))
	}))
	defer server.Close()

	tool := testHTTPTool(server.URL, "test_tool", 1)
	_ = executeHTTPTool(t, tool, ToolInvocation{
		CallID:      "call_test",
		Name:        "test_tool",
		SessionFile: "/sessions/chat-C.json",
		Args:        json.RawMessage(`{}`),
		Metadata:    map[string]string{"X-FAIRY-Session-ID": "explicit-session"},
	})
	if got != "explicit-session" {
		t.Fatalf("explicit metadata header should win, got %q", got)
	}
}

func TestSessionIDPersistsToSessionFile(t *testing.T) {
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "chat-test.json")
	if err := os.WriteFile(sessionFile, []byte(`{"messages":[],"model":"m"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	var got []string
	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("X-FAIRY-Session-ID"))
		_, _ = w.Write([]byte(`{"tool_call_id":"call_test","result":"{\"ok\":true}"}`))
	}))
	defer server.Close()
	tool := testHTTPTool(server.URL, "test_tool", 1)

	_ = executeHTTPTool(t, tool, ToolInvocation{CallID: "c1", Name: "test_tool", SessionFile: sessionFile, Args: json.RawMessage(`{}`)})

	// ??????? JSON ???? session_id????????
	data, err := os.ReadFile(sessionFile)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	fileID, _ := doc["session_id"].(string)
	if fileID == "" {
		t.Fatalf("session_id not persisted to file: %s", data)
	}
	if fileID != got[0] {
		t.Fatalf("header id %q != file id %q", got[0], fileID)
	}

	// ?????????????????????????? id
	sessionUUIDCache.Lock()
	sessionUUIDCache.m = map[string]string{}
	sessionUUIDCache.Unlock()

	_ = executeHTTPTool(t, tool, ToolInvocation{CallID: "c2", Name: "test_tool", SessionFile: sessionFile, Args: json.RawMessage(`{}`)})
	if len(got) < 2 || got[1] != got[0] {
		t.Fatalf("after restart, session id changed: %q -> %v", got[0], got)
	}

	// ????? session_id?EnsureSessionID ??????????
	if id := EnsureSessionID(sessionFile); id != got[0] {
		t.Fatalf("EnsureSessionID should reuse existing id, got %q want %q", id, got[0])
	}
}
