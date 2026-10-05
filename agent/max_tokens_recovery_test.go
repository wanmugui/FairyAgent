package main

import "testing"

func TestResponseHitMaxTokensUsesTerminalReasonFirst(t *testing.T) {
	tests := []struct {
		name              string
		response          *APIResponse
		requestedMaxToken int
		want              bool
	}{
		{"length", &APIResponse{FinishStop: "length"}, 16384, true},
		{"explicit max_tokens", &APIResponse{FinishStop: "max_tokens"}, 16384, true},
		{"normal stop at budget", &APIResponse{FinishStop: "stop", Usage: &UsageInfo{CompletionTokens: 16384}}, 16384, false},
		{"tool calls at budget", &APIResponse{FinishStop: "tool_calls", Usage: &UsageInfo{CompletionTokens: 16384}}, 16384, false},
		{"missing reason at budget", &APIResponse{Usage: &UsageInfo{CompletionTokens: 16384}}, 16384, true},
		{"missing reason below budget", &APIResponse{Usage: &UsageInfo{CompletionTokens: 16383}}, 16384, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := responseHitMaxTokens(test.response, test.requestedMaxToken); got != test.want {
				t.Fatalf("responseHitMaxTokens() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRootAgentRetriesMaxTokensTruncationInSameStep(t *testing.T) {
	dir := t.TempDir()
	mockPath := writeMockFile(t, dir, []map[string]any{
		{"finish_reason": "length", "content": "partial answer"},
		{"finish_reason": "stop", "content": "<report>complete answer</report>"},
	})
	cfg := &Config{
		UseMock:        true,
		MockFile:       mockPath,
		RepoRoot:       repoRootForTest(t),
		SystemPartsDir: "config/system/parts/zh",
		API:            APIConfig{MaxTokens: 16384},
	}
	result, err := RunAgentLoop(cfg, NewToolRegistry(), "", "write a complete result", nil, nil, "", "", "", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Steps != 1 {
		t.Fatalf("max_tokens retry must stay inside the same logical step, got %d steps", result.Steps)
	}
	final := result.Messages[len(result.Messages)-1]
	if final.Content != "<report>complete answer</report>" {
		t.Fatalf("truncated partial response leaked as final: %#v", final)
	}
	foundEscalation := false
	for _, event := range result.Trace {
		if event["event"] == "llm_max_tokens_escalation" && event["status"] == "retrying" &&
			event["from_max_tokens"] == 16384 && event["to_max_tokens"] == 32768 {
			foundEscalation = true
		}
	}
	if !foundEscalation {
		t.Fatalf("missing max_tokens escalation trace: %#v", result.Trace)
	}
}

func TestRootAgentFailsAfterSecondMaxTokensTruncation(t *testing.T) {
	dir := t.TempDir()
	mockPath := writeMockFile(t, dir, []map[string]any{
		{"finish_reason": "length", "content": "still partial"},
		{"finish_reason": "length", "content": "still partial again"},
	})
	cfg := &Config{
		UseMock:        true,
		MockFile:       mockPath,
		RepoRoot:       repoRootForTest(t),
		SystemPartsDir: "config/system/parts/zh",
		API:            APIConfig{MaxTokens: 16384},
	}
	if _, err := RunAgentLoop(cfg, NewToolRegistry(), "", "write a complete result", nil, nil, "", "", "", "", "", false); err == nil {
		t.Fatal("second max_tokens truncation must fail instead of returning a partial answer")
	}
}
