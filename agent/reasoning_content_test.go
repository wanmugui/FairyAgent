package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReasoningContentRoundTripsThroughStreamingRequest(t *testing.T) {
	var requestBody APIRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &requestBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"new reasoning\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	cfg := &APIConfig{
		BaseURL:    server.URL,
		APIKey:     "test-key",
		Model:      "test-model",
		TimeoutSec: 5,
	}
	resp, err := CallLLMStream(cfg, []Message{{
		Role:             "assistant",
		ReasoningContent: "previous reasoning",
		ToolCalls: []ToolCall{{
			ID: "call-1", Type: "function",
			Function: ToolCallFunc{Name: "read_file", Arguments: `{"path":"x"}`},
		}},
	}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(requestBody.Messages) != 1 || requestBody.Messages[0].ReasoningContent != "previous reasoning" {
		t.Fatalf("reasoning_content was not sent back: %#v", requestBody.Messages)
	}
	if resp.ReasoningContent != "new reasoning" || resp.Content != "hello" {
		t.Fatalf("unexpected streamed response: reasoning=%q content=%q", resp.ReasoningContent, resp.Content)
	}
}
