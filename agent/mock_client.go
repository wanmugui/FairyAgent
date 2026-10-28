package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
)

type mockLLMResponse struct {
	Content      string     `json:"content"`
	ToolCalls    []ToolCall `json:"tool_calls"`
	FinishReason string     `json:"finish_reason"`
	Usage        *UsageInfo `json:"usage,omitempty"`
	DurationMs   int64      `json:"duration_ms,omitempty"`
}

type mockLLMState struct {
	responses []mockLLMResponse
	next      int
}

type mockLLMProfiles struct {
	Main    []mockLLMResponse `json:"main"`
	Subtask []mockLLMResponse `json:"subtask"`
}

// CallConfiguredLLM keeps real API requests unchanged while making the existing
// use_mock/mock_file configuration usable for local and frontend smoke tests.
func CallConfiguredLLM(cfg *Config, messages []Message, toolDefs []ToolDef) (*APIResponse, error) {
	if cfg == nil {
		return nil, fmt.Errorf("LLM config is nil")
	}
	if !cfg.UseMock {
		return CallLLM(&cfg.API, messages, toolDefs)
	}
	if cfg.mockLLMState == nil {
		state, err := loadMockLLMState(cfg)
		if err != nil {
			return nil, err
		}
		cfg.mockLLMState = state
	}
	state := cfg.mockLLMState
	if state.next >= len(state.responses) {
		return nil, fmt.Errorf("mock LLM responses exhausted after %d call(s)", len(state.responses))
	}
	response := state.responses[state.next]
	state.next++
	return &APIResponse{
		Content:    response.Content,
		ToolCalls:  append([]ToolCall(nil), response.ToolCalls...),
		FinishStop: response.FinishReason,
		Usage:      response.Usage,
		DurationMs: response.DurationMs,
	}, nil
}

func loadMockLLMState(cfg *Config) (*mockLLMState, error) {
	if cfg.MockFile == "" {
		return nil, fmt.Errorf("mock LLM is enabled but mock_file is empty")
	}
	path := cfg.ResolvePath(cfg.MockFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read mock responses: %w", err)
	}
	if len(raw) >= 3 && raw[0] == 0xEF && raw[1] == 0xBB && raw[2] == 0xBF {
		raw = raw[3:]
	}
	responses, err := parseMockLLMResponses(raw, os.Getenv("AGENT_RUN_KIND"))
	if err != nil {
		return nil, err
	}
	if len(responses) == 0 {
		return nil, fmt.Errorf("mock responses file contains no responses")
	}
	return &mockLLMState{responses: responses}, nil
}

func parseMockLLMResponses(raw []byte, runKind string) ([]mockLLMResponse, error) {
	var responses []mockLLMResponse
	if err := json.Unmarshal(raw, &responses); err == nil {
		return responses, nil
	}
	var profiles mockLLMProfiles
	if err := json.Unmarshal(raw, &profiles); err != nil {
		return nil, fmt.Errorf("parse mock responses: %w", err)
	}
	if runKind == "subtask" {
		if len(profiles.Subtask) == 0 {
			return nil, fmt.Errorf("mock responses file has no subtask profile")
		}
		return profiles.Subtask, nil
	}
	if len(profiles.Main) == 0 {
		return nil, fmt.Errorf("mock responses file has no main profile")
	}
	return profiles.Main, nil
}

// CallConfiguredLLMStream is the streaming counterpart of CallConfiguredLLM.
// In mock mode it delivers the whole text at once (mocks are not streaming);
// otherwise it streams deltas from the real API.
func CallConfiguredLLMStream(cfg *Config, messages []Message, toolDefs []ToolDef, onDelta func(string)) (*APIResponse, error) {
	return CallConfiguredLLMStreamCtx(context.Background(), cfg, messages, toolDefs, onDelta)
}

// CallConfiguredLLMStreamCtx is the cancellable variant. In mock mode it
// returns immediately (mocks are synchronous); in live mode it forwards ctx
// to the underlying CallLLMStreamCtx so cancellation stops the read loop.
func CallConfiguredLLMStreamCtx(ctx context.Context, cfg *Config, messages []Message, toolDefs []ToolDef, onDelta func(string)) (*APIResponse, error) {
	return CallConfiguredLLMStreamCtxWithProgress(ctx, cfg, messages, toolDefs, onDelta, nil)
}

// CallConfiguredLLMStreamCtxWithProgress is the progress-aware variant. Mock
// responses are synchronous, so they emit a minimal start/finish heartbeat
// instead of pretending to stream token by token.
func CallConfiguredLLMStreamCtxWithProgress(
	ctx context.Context,
	cfg *Config,
	messages []Message,
	toolDefs []ToolDef,
	onDelta func(string),
	onProgress LLMProgressFunc,
) (*APIResponse, error) {
	if cfg == nil {
		return nil, fmt.Errorf("LLM config is nil")
	}
	if !cfg.UseMock {
		return CallLLMStreamCtxWithProgress(ctx, &cfg.API, messages, toolDefs, onDelta, onProgress)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if onProgress != nil {
		onProgress(LLMStreamProgress{Phase: "waiting_response"})
	}
	resp, err := CallConfiguredLLM(cfg, messages, toolDefs)
	if err == nil && resp != nil && resp.Content != "" && onDelta != nil {
		onDelta(resp.Content)
	}
	if onProgress != nil {
		phase := "response_complete"
		if err != nil {
			phase = "response_error"
		}
		onProgress(LLMStreamProgress{Phase: phase})
	}
	return resp, err
}
