package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCallLLMStreamCtxWithProgressCapturesDiagnostics(t *testing.T) {
	oldInterval := llmProgressHeartbeatInterval
	llmProgressHeartbeatInterval = 10 * time.Millisecond
	defer func() { llmProgressHeartbeatInterval = oldInterval }()

	requestCh := make(chan APIRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request APIRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		requestCh <- request

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-Id", "req-test-123")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("response writer does not support flushing")
			return
		}
		writeChunk := func(payload string) {
			_, _ = fmt.Fprintln(w, "data: "+payload)
			flusher.Flush()
		}

		writeChunk(`{"choices":[{"delta":{"content":"你"},"finish_reason":""}]}`)
		time.Sleep(25 * time.Millisecond)
		writeChunk(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"bash","arguments":"{\"cmd\":"}}]},"finish_reason":""}]}`)
		writeChunk(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"pwd\"}"}}]},"finish_reason":"tool_calls"}]}`)
		writeChunk(`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
		writeChunk(`[DONE]`)
	}))
	defer server.Close()

	var mu sync.Mutex
	progress := make([]LLMStreamProgress, 0, 8)
	resp, err := CallLLMStreamCtxWithProgress(
		context.Background(),
		&APIConfig{BaseURL: server.URL, APIKey: "test", Model: "test-model", TimeoutSec: 5, MaxTokens: 128},
		[]Message{{Role: "user", Content: "hi"}},
		nil,
		nil,
		func(item LLMStreamProgress) {
			mu.Lock()
			progress = append(progress, item)
			mu.Unlock()
		},
	)
	if err != nil {
		t.Fatalf("CallLLMStreamCtxWithProgress() error = %v", err)
	}

	request := <-requestCh
	if request.StreamOptions == nil || !request.StreamOptions.IncludeUsage {
		t.Fatalf("stream_options.include_usage = %+v, want true", request.StreamOptions)
	}
	if resp.UpstreamRequestID != "req-test-123" {
		t.Fatalf("UpstreamRequestID = %q", resp.UpstreamRequestID)
	}
	if !resp.Streaming || resp.RequestSentAtMs <= 0 || resp.ResponseCompleteAtMs < resp.RequestSentAtMs {
		t.Fatalf("stream timing diagnostics = %+v", resp)
	}
	if resp.FirstDeltaMs < 0 {
		t.Fatalf("FirstDeltaMs = %d", resp.FirstDeltaMs)
	}
	if resp.StreamChunkCount != 4 {
		t.Fatalf("StreamChunkCount = %d, want 4", resp.StreamChunkCount)
	}
	if resp.Content != "你" {
		t.Fatalf("Content = %q", resp.Content)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Function.Arguments != `{"cmd":"pwd"}` {
		t.Fatalf("ToolCalls = %+v", resp.ToolCalls)
	}
	if resp.ToolArgumentsBytes != len(`{"cmd":"pwd"}`) {
		t.Fatalf("ToolArgumentsBytes = %d", resp.ToolArgumentsBytes)
	}

	mu.Lock()
	defer mu.Unlock()
	phases := map[string]bool{}
	for _, item := range progress {
		phases[item.Phase] = true
	}
	for _, phase := range []string{"waiting_response", "first_delta", "tool_arguments", "response_complete"} {
		if !phases[phase] {
			t.Fatalf("missing progress phase %q; got %+v", phase, progress)
		}
	}
	foundToolSnapshot := false
	for _, item := range progress {
		if len(item.ToolCalls) == 0 {
			continue
		}
		foundToolSnapshot = true
		if item.ToolCalls[0].Function.Name != "bash" {
			t.Fatalf("unexpected streamed tool call name: %+v", item.ToolCalls[0])
		}
		if !strings.Contains(item.ToolCalls[0].Function.Arguments, "cmd") {
			t.Fatalf("streamed tool arguments missing partial cmd: %+v", item.ToolCalls[0])
		}
		break
	}
	if !foundToolSnapshot {
		t.Fatalf("no incremental tool-call snapshot emitted: %+v", progress)
	}
}
