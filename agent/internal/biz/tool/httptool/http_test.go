package httptool

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func newHTTPTestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	return server
}

func testHTTPTool(endpoint string, schemaName string, retryCount int) *HTTPTool {
	tool := NewHTTPTool(
		schemaName,
		endpoint,
		ToolDef{Type: "function", Function: map[string]any{"name": schemaName}},
		2,
		"",
		nil,
		retryCount,
		"",
	)
	tool.retryBackoff = func(int) time.Duration { return 0 }
	return tool
}

func executeHTTPTool(t *testing.T, tool *HTTPTool, inv ToolInvocation) ToolResult {
	t.Helper()
	got, err := tool.Execute(context.Background(), inv)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestHTTPToolSendsProductionPayload(t *testing.T) {
	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("unexpected content type: %s", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var payload httpToolRequest
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.ToolCallID != "call_test" || payload.ToolName != "test_tool" || payload.Arguments != `{"value":"x"}` {
			t.Errorf("unexpected payload: %#v", payload)
		}
		_, _ = w.Write([]byte(`{"tool_call_id":"call_test","result":"{\"ok\":true}"}`))
	}))
	defer server.Close()

	result := executeHTTPTool(t, testHTTPTool(server.URL, "test_tool", 1), ToolInvocation{
		CallID: "call_test",
		Name:   "test_tool",
		Args:   json.RawMessage(`{"value":"x"}`),
	})
	if result.Value["ok"] != true {
		t.Fatalf("unexpected result: %#v", result.Value)
	}
}

func TestHTTPToolAddsBearerTokenAndHeaders(t *testing.T) {
	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("unexpected authorization: %s", got)
		}
		if got := r.Header.Get("X-Test"); got != "header" {
			t.Errorf("unexpected custom header: %s", got)
		}
		if got := r.Header.Get("X-Meta"); got != "metadata" {
			t.Errorf("unexpected metadata header: %s", got)
		}
		_, _ = w.Write([]byte(`{"tool_call_id":"call_test","result":"{\"ok\":true}"}`))
	}))
	defer server.Close()

	tool := NewHTTPTool("test_tool", server.URL, ToolDef{}, 2, "secret", map[string]string{"X-Test": "header"}, 1, "")
	tool.retryBackoff = func(int) time.Duration { return 0 }
	_ = executeHTTPTool(t, tool, ToolInvocation{
		CallID:   "call_test",
		Name:     "test_tool",
		Args:     json.RawMessage(`{}`),
		Metadata: map[string]string{"X-Meta": "metadata"},
	})
}

func TestHTTPToolParsesDirectResponse(t *testing.T) {
	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tool_call_id":"call_test","result":"{\"value\":42}"}`))
	}))
	defer server.Close()

	result := executeHTTPTool(t, testHTTPTool(server.URL, "test_tool", 1), ToolInvocation{CallID: "call_test", Args: json.RawMessage(`{}`)})
	if result.Value["value"] != float64(42) {
		t.Fatalf("unexpected result: %#v", result.Value)
	}
}

func TestHTTPToolParsesWrappedResponse(t *testing.T) {
	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"tool_call_id":"call_test","result":"{\"wrapped\":true}"}}`))
	}))
	defer server.Close()

	result := executeHTTPTool(t, testHTTPTool(server.URL, "test_tool", 1), ToolInvocation{CallID: "call_test", Args: json.RawMessage(`{}`)})
	if result.Value["wrapped"] != true {
		t.Fatalf("unexpected result: %#v", result.Value)
	}
}

func TestHTTPToolWarnsOnToolCallIDMismatch(t *testing.T) {
	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tool_call_id":"different","result":"plain"}`))
	}))
	defer server.Close()

	result := executeHTTPTool(t, testHTTPTool(server.URL, "test_tool", 1), ToolInvocation{CallID: "call_test", Args: json.RawMessage(`{}`)})
	if result.Value["result"] != "plain" {
		t.Fatalf("mismatched ID should still preserve response: %#v", result.Value)
	}
}

func TestHTTPToolRetriesServerErrorThenSucceeds(t *testing.T) {
	var attempts atomic.Int32
	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`server error`))
			return
		}
		_, _ = w.Write([]byte(`{"tool_call_id":"call_test","result":"{\"retried\":true}"}`))
	}))
	defer server.Close()

	result := executeHTTPTool(t, testHTTPTool(server.URL, "test_tool", 2), ToolInvocation{CallID: "call_test", Args: json.RawMessage(`{}`)})
	if attempts.Load() != 2 || result.Value["retried"] != true {
		t.Fatalf("unexpected retry behavior: attempts=%d result=%#v", attempts.Load(), result.Value)
	}
}

func TestHTTPToolReturnsTimeoutError(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()

	tool := testHTTPTool(server.URL, "test_tool", 1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := tool.Execute(ctx, ToolInvocation{CallID: "call_test", Args: json.RawMessage(`{}`)})
	<-started
	close(release)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline exceeded, got %v", err)
	}
}

func TestHTTPToolPreservesUpstreamCodeFor410(t *testing.T) {
	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
		_, _ = w.Write([]byte(`{"code":0,"stream_status":23,"message":"stopped"}`))
	}))
	defer server.Close()

	result := executeHTTPTool(t, testHTTPTool(server.URL, "test_tool", 1), ToolInvocation{CallID: "call_test", Args: json.RawMessage(`{}`)})
	if !result.IsError || result.UpstreamCode != 23 {
		t.Fatalf("unexpected 410 result: %#v", result)
	}
}

func TestHTTPToolReturnsClientErrorResult(t *testing.T) {
	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`bad request`))
	}))
	defer server.Close()

	result := executeHTTPTool(t, testHTTPTool(server.URL, "test_tool", 1), ToolInvocation{CallID: "call_test", Args: json.RawMessage(`{}`)})
	if !result.IsError || result.Value["error"] != "bad request" {
		t.Fatalf("unexpected client error result: %#v", result)
	}
}
