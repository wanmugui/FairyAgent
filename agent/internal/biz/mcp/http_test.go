package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestMCPHTTPClientSessionProtocolAndSSE(t *testing.T) {
	var (
		mu            sync.Mutex
		deleteCount   int
		didInitialize bool
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			mu.Lock()
			deleteCount++
			mu.Unlock()
			if got := r.Header.Get("Mcp-Session-Id"); got != "session-1" {
				t.Errorf("DELETE session header = %q", got)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}

		var request rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if request.Method == "initialize" {
			didInitialize = true
			w.Header().Set("Mcp-Session-Id", "session-1")
		} else {
			if !didInitialize {
				t.Errorf("request %q arrived before initialize", request.Method)
			}
			if got := r.Header.Get("Mcp-Session-Id"); got != "session-1" {
				t.Errorf("%s session header = %q", request.Method, got)
			}
			if got := r.Header.Get("MCP-Protocol-Version"); got != DefaultProtocolVersion {
				t.Errorf("%s protocol header = %q", request.Method, got)
			}
		}

		switch request.Method {
		case "initialize":
			writeRPCResponse(t, w, request.ID, map[string]any{"protocolVersion": DefaultProtocolVersion})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			var params map[string]any
			_ = json.Unmarshal(mustJSON(t, request.Params), &params)
			if params["cursor"] == "next" {
				writeRPCResponse(t, w, request.ID, map[string]any{
					"tools": []map[string]any{{"name": "second", "inputSchema": map[string]any{"type": "object"}}},
				})
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			payload := map[string]any{
				"jsonrpc": "2.0",
				"id":      request.ID,
				"result": map[string]any{
					"tools": []map[string]any{{
						"name":        "echo",
						"description": "echo arguments",
						"inputSchema": map[string]any{"type": "object"},
					}},
					"nextCursor": "next",
				},
			}
			raw, _ := json.Marshal(payload)
			_, _ = w.Write([]byte("event: message\ndata: " + string(raw) + "\n\n"))
		case "tools/call":
			writeRPCResponse(t, w, request.ID, map[string]any{
				"content": []map[string]any{{"type": "text", "text": "called"}},
			})
		default:
			writeRPCError(t, w, request.ID, -32601, "method not found")
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := Connect(ctx, Options{
		Name:      "http-test",
		Transport: "streamable-http",
		URL:       server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 || tools[0].Name != "echo" || tools[1].Name != "second" {
		t.Fatalf("unexpected tools: %#v", tools)
	}
	result, err := client.CallTool(ctx, "echo", map[string]any{"value": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if got := firstTextContent(result.Content); got != "called" {
		t.Fatalf("unexpected tool text: %q", got)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if deleteCount != 1 {
		t.Fatalf("session DELETE count = %d, want 1", deleteCount)
	}
}

func TestNamespacedToolName(t *testing.T) {
	if got, want := NamespacedToolName("playwright", "browser.click", "pw"), "pw__browser_click"; got != want {
		t.Fatalf("NamespacedToolName() = %q, want %q", got, want)
	}
}

func TestReadSSEResponseStopsAtMatchingMessage(t *testing.T) {
	reader, writer := io.Pipe()
	defer func() { _ = writer.Close() }()
	go func() {
		_, _ = writer.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"7\",\"result\":{\"ok\":true}}\n\n"))
	}()

	response, err := readSSEResponse(reader, "7")
	if err != nil {
		t.Fatal(err)
	}
	if string(response.Result) != `{"ok":true}` {
		t.Fatalf("unexpected SSE result: %s", response.Result)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func writeRPCResponse(t *testing.T, w http.ResponseWriter, id any, result any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	}); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func writeRPCError(t *testing.T, w http.ResponseWriter, id any, code int, message string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	}); err != nil {
		t.Errorf("encode error response: %v", err)
	}
}
