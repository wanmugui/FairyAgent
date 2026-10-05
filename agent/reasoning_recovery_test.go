package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A thinking-mode API rejects an assistant turn that drops its reasoning_content.
//
// The final-answer recovery path appends a draft assistant message to the next
// request. A response made only of XML tags - which is exactly what leaks through
// when a model emits its native tool-call format instead of the OpenAI one - counts
// as "no visible content", so that path is reachable in normal use. The draft used
// to be built without reasoning_content and the next call died with
// "The `reasoning_content` in the thinking mode must be passed back to the API".
//
// This test enforces that contract the way the real API does, so it fails if any
// assistant turn in a request loses its reasoning. The success condition is that
// the run completes at all.
func TestRecoveryDraftKeepsReasoningContentForThinkingMode(t *testing.T) {
	var requests []APIRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request APIRequest
		if err := json.Unmarshal(body, &request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests = append(requests, request)

		for _, message := range request.Messages {
			if message.Role == "assistant" && strings.TrimSpace(message.ReasoningContent) == "" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w,
					`{"error":{"message":"The `+"`reasoning_content`"+` in the thinking mode must be passed back to the API."}}`)
				return
			}
		}

		w.Header().Set("Content-Type", "text/event-stream")
		if len(requests) == 1 {
			// Reasoning plus content that is only markup: no visible answer, but a
			// draft worth carrying into the next request.
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"internal reasoning\"}}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"<tool_call></tool_call>\"},\"finish_reason\":\"stop\"}]}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"<report>done</report>\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	cfg := &Config{
		UseMock:        false,
		RepoRoot:       repoRootForTest(t),
		SystemPartsDir: "config/system/parts/zh",
		API: APIConfig{
			BaseURL:    server.URL,
			APIKey:     "test-key",
			Model:      "test-model",
			TimeoutSec: 10,
		},
	}

	result, err := RunAgentLoop(cfg, NewToolRegistry(), "", "say something", nil, nil, "", "", "", "", "", false)
	if err != nil {
		t.Fatalf("run failed, which is what happens when reasoning_content is dropped: %v", err)
	}
	if result == nil {
		t.Fatal("RunAgentLoop returned no result")
	}

	// The recovery draft must have reached the second request with its reasoning.
	if len(requests) < 2 {
		t.Fatalf("expected at least two API calls, got %d", len(requests))
	}
	second := requests[1]
	found := false
	for _, message := range second.Messages {
		content, _ := message.Content.(string)
		if message.Role == "assistant" && strings.Contains(content, "<tool_call>") {
			found = true
			if message.ReasoningContent != "internal reasoning" {
				t.Fatalf("recovery draft reached the API without its reasoning: %#v", message)
			}
		}
	}
	if !found {
		t.Fatalf("recovery draft was not carried into the second request: %#v", second.Messages)
	}
}
