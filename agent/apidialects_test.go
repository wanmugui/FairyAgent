package main

// Upstream dialect tests.
//
// Each dialect is exercised against a fake upstream that speaks the real
// wire protocol, so the assertions cover what actually goes over the
// network: the URL, the auth headers, the request body shape and the
// response parsing.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- Anthropic Messages ----------------------------------------------------

func TestAnthropicRequestShape(t *testing.T) {
	cfg := &APIConfig{
		BaseURL:   "https://api.anthropic.com/v1",
		APIKey:    "sk-ant-test",
		Model:     "claude-sonnet-4",
		MaxTokens: 2048,
		Format:    DialectAnthropicMessages,
	}
	msgs := []APIMessage{
		{Role: "system", Content: "you are helpful"},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "calling a tool", ToolCalls: []ToolCall{{
			ID:       "call_1",
			Type:     "function",
			Function: ToolCallFunc{Name: "read_file", Arguments: `{"path":"a.txt"}`},
		}}},
		{Role: "tool", ToolCallID: "call_1", Content: "file body"},
	}
	tools := []ToolDef{{Type: "function", Function: map[string]interface{}{
		"name":        "read_file",
		"description": "read a file",
		"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
	}}}

	req, err := buildAnthropicRequest(cfg, msgs, tools, false)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	// The system turn is hoisted out of the message list.
	if req.System != "you are helpful" {
		t.Fatalf("system should be hoisted, got %q", req.System)
	}
	if len(req.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d: %+v", len(req.Messages), req.Messages)
	}
	// user / assistant / tool-result
	if req.Messages[0].Role != "user" {
		t.Fatalf("first message should be user, got %q", req.Messages[0].Role)
	}
	// The tool result becomes a user turn with a tool_result block.
	last := req.Messages[2]
	if last.Role != "user" {
		t.Fatalf("tool result should be a user turn, got %q", last.Role)
	}
	if len(last.Content) != 1 || last.Content[0].Type != "tool_result" {
		t.Fatalf("tool result block missing: %+v", last.Content)
	}
	if last.Content[0].ToolUseID != "call_1" {
		t.Fatalf("tool_use_id should be call_1, got %q", last.Content[0].ToolUseID)
	}
	// The tool call becomes an assistant turn with a tool_use block.
	asst := req.Messages[1]
	found := false
	for _, b := range asst.Content {
		if b.Type == "tool_use" && b.Name == "read_file" {
			found = true
		}
	}
	if !found {
		t.Fatalf("tool_use block missing: %+v", asst.Content)
	}
	// Tools carry the OpenAI function declaration as Anthropic input_schema.
	if len(req.Tools) != 1 || req.Tools[0].Name != "read_file" {
		t.Fatalf("tools not translated: %+v", req.Tools)
	}
	if len(req.Tools[0].InputSchema) == 0 {
		t.Fatal("input_schema should not be empty")
	}
}

func TestAnthropicEndpointAndHeaders(t *testing.T) {
	cfg := &APIConfig{
		BaseURL: "https://api.anthropic.com/v1",
		APIKey:  "sk-ant-test",
		Model:   "claude-sonnet-4",
		Format:  DialectAnthropicMessages,
	}
	if got := upstreamURL(cfg); got != "https://api.anthropic.com/v1/messages" {
		t.Fatalf("unexpected url: %s", got)
	}
	// A base url that already carries the endpoint must not be doubled.
	cfg.BaseURL = "https://api.anthropic.com/v1/messages"
	if got := upstreamURL(cfg); got != "https://api.anthropic.com/v1/messages" {
		t.Fatalf("endpoint should not be doubled: %s", got)
	}

	req, _ := http.NewRequest("POST", "https://x", nil)
	applyUpstreamAuth(req, cfg)
	if req.Header.Get("x-api-key") != "sk-ant-test" {
		t.Fatalf("x-api-key missing: %v", req.Header)
	}
	if req.Header.Get("anthropic-version") == "" {
		t.Fatal("anthropic-version header missing")
	}
}

func TestAnthropicNonStreamingRoundTrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "sk-ant" {
			t.Errorf("missing x-api-key")
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Errorf("missing anthropic-version")
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(raw, &body)
		if body["model"] != "claude-x" {
			t.Errorf("model not sent: %v", body["model"])
		}
		if _, ok := body["max_tokens"]; !ok {
			t.Errorf("max_tokens should be present for anthropic: %v", body)
		}
		if _, ok := body["max_output_tokens"]; ok {
			t.Errorf("max_output_tokens is the Responses field, not anthropic: %v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"id":"msg_1","model":"claude-x","stop_reason":"tool_use",
			"content":[
				{"type":"text","text":"let me look"},
				{"type":"tool_use","id":"tu_1","name":"read_file","input":{"path":"a.txt"}}
			],
			"usage":{"input_tokens":11,"output_tokens":7}
		}`)
	}))
	defer server.Close()

	cfg := &APIConfig{
		BaseURL:    server.URL + "/v1",
		APIKey:     "sk-ant",
		Model:      "claude-x",
		MaxTokens:  1024,
		Format:     DialectAnthropicMessages,
		TimeoutSec: 10,
	}
	out, err := CallLLM(cfg, []Message{
		{Role: "user", Content: "read a.txt"},
	}, nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if out.Content != "let me look" {
		t.Fatalf("content mismatch: %q", out.Content)
	}
	if len(out.ToolCalls) != 1 || out.ToolCalls[0].Function.Name != "read_file" {
		t.Fatalf("tool call not parsed: %+v", out.ToolCalls)
	}
	if out.Usage == nil || out.Usage.PromptTokens != 11 || out.Usage.CompletionTokens != 7 {
		t.Fatalf("usage not parsed: %+v", out.Usage)
	}
	if out.FinishStop != "tool_calls" {
		t.Fatalf("stop reason should map to tool_calls, got %q", out.FinishStop)
	}
}

func TestAnthropicStreamingRoundTrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		frames := []string{
			`event: message_start` + "\n" + `data: {"type":"message_start","message":{"model":"claude-x","usage":{"input_tokens":5,"output_tokens":0}}}` + "\n\n",
			`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}` + "\n\n",
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello "}}` + "\n\n",
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world"}}` + "\n\n",
			`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu_9","name":"ls"}}` + "\n\n",
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}` + "\n\n",
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"a\"}"}}` + "\n\n",
			`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":1}` + "\n\n",
			`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":5,"output_tokens":9}}` + "\n\n",
			`event: message_stop` + "\n" + `data: {"type":"message_stop"}` + "\n\n",
		}
		for _, f := range frames {
			fmt.Fprint(w, f)
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	defer server.Close()

	cfg := &APIConfig{
		BaseURL:    server.URL + "/v1",
		APIKey:     "sk-ant",
		Model:      "claude-x",
		MaxTokens:  1024,
		Format:     DialectAnthropicMessages,
		TimeoutSec: 10,
	}
	var streamed strings.Builder
	out, err := CallLLMStream(cfg, []Message{{Role: "user", Content: "hi"}}, nil, func(s string) {
		streamed.WriteString(s)
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if out.Content != "Hello world" {
		t.Fatalf("streamed content mismatch: %q", out.Content)
	}
	if streamed.String() != "Hello world" {
		t.Fatalf("onDelta did not receive the text: %q", streamed.String())
	}
	if len(out.ToolCalls) != 1 {
		t.Fatalf("tool call not assembled: %+v", out.ToolCalls)
	}
	if out.ToolCalls[0].ID != "tu_9" || out.ToolCalls[0].Function.Name != "ls" {
		t.Fatalf("tool call fields wrong: %+v", out.ToolCalls[0])
	}
	if out.ToolCalls[0].Function.Arguments != `{"path":"a"}` {
		t.Fatalf("partial json not concatenated: %q", out.ToolCalls[0].Function.Arguments)
	}
	if out.Usage == nil || out.Usage.CompletionTokens != 9 {
		t.Fatalf("stream usage not parsed: %+v", out.Usage)
	}
	if out.FinishStop != "tool_calls" {
		t.Fatalf("stop reason: %q", out.FinishStop)
	}
}

// --- OpenAI Responses ------------------------------------------------------

func TestResponsesEndpointAndBody(t *testing.T) {
	cfg := &APIConfig{
		BaseURL: "https://api.openai.com/v1",
		APIKey:  "sk-test",
		Model:   "gpt-x",
		// A non-zero limit, otherwise omitempty drops the field and the
		// upstream would fall back to its own default.
		MaxTokens: 256,
		Format:    DialectResponses,
	}
	if got := upstreamURL(cfg); got != "https://api.openai.com/v1/responses" {
		t.Fatalf("unexpected url: %s", got)
	}
	if got := dialectOutputLimitField(DialectResponses); got != "max_output_tokens" {
		t.Fatalf("responses output limit field: %s", got)
	}
	if got := dialectOutputLimitField(DialectAnthropicMessages); got != "max_tokens" {
		t.Fatalf("anthropic output limit field: %s", got)
	}

	msgs := []APIMessage{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "sure", ToolCalls: []ToolCall{{
			ID: "fc_1", Type: "function",
			Function: ToolCallFunc{Name: "ls", Arguments: `{"p":"a"}`},
		}}},
		{Role: "tool", ToolCallID: "fc_1", Content: "result"},
	}
	req, err := buildResponsesRequest(cfg, msgs, nil, false)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if req.MaxOutputTokens != cfg.MaxTokens {
		t.Fatalf("max_output_tokens missing: %+v", req)
	}
	raw, _ := json.Marshal(req)
	if !strings.Contains(string(raw), "max_output_tokens") {
		t.Fatalf("body should carry max_output_tokens: %s", raw)
	}
	if !strings.Contains(string(raw), "function_call_output") {
		t.Fatalf("tool result should become function_call_output: %s", raw)
	}
}

func TestResponsesToolItemsAreTopLevelInputItems(t *testing.T) {
	cfg := &APIConfig{
		BaseURL: "https://api.example.com/v1",
		APIKey:  "sk-test",
		Model:   "gpt-x",
		Format:  DialectResponses,
	}
	msgs := []APIMessage{
		{Role: "user", Content: "find a file"},
		{Role: "assistant", Content: "looking", ToolCalls: []ToolCall{{
			ID:       "call_1",
			Type:     "function",
			Function: ToolCallFunc{Name: "find", Arguments: `{"name":"a.txt"}`},
		}}},
		{Role: "tool", ToolCallID: "call_1", Content: "found"},
	}

	req, err := buildResponsesRequest(cfg, msgs, nil, false)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var body struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	var callItem, outputItem map[string]any
	for _, item := range body.Input {
		switch item["type"] {
		case "function_call":
			callItem = item
		case "function_call_output":
			outputItem = item
		}
		if parts, ok := item["content"].([]any); ok {
			for _, rawPart := range parts {
				part, _ := rawPart.(map[string]any)
				switch part["type"] {
				case "function_call", "function_call_output":
					t.Fatalf("tool item must not be nested in message content: %s", raw)
				}
			}
		}
	}
	if callItem == nil {
		t.Fatalf("top-level function_call item missing: %s", raw)
	}
	if callItem["call_id"] != "call_1" || callItem["name"] != "find" {
		t.Fatalf("function_call fields wrong: %#v", callItem)
	}
	if outputItem == nil {
		t.Fatalf("top-level function_call_output item missing: %s", raw)
	}
	if outputItem["call_id"] != "call_1" || outputItem["output"] != "found" {
		t.Fatalf("function_call_output fields wrong: %#v", outputItem)
	}
}

func TestResponsesNonStreamingRoundTrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"id":"resp_1","model":"gpt-x",
			"output":[
				{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi there"}]},
				{"type":"function_call","call_id":"fc_1","name":"ls","arguments":"{\"p\":\"a\"}"}
			],
			"usage":{"input_tokens":4,"output_tokens":6}
		}`)
	}))
	defer server.Close()

	cfg := &APIConfig{
		BaseURL: server.URL + "/v1", APIKey: "sk", Model: "gpt-x",
		MaxTokens: 512, Format: DialectResponses, TimeoutSec: 10,
	}
	out, err := CallLLM(cfg, []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if out.Content != "hi there" {
		t.Fatalf("content: %q", out.Content)
	}
	if len(out.ToolCalls) != 1 || out.ToolCalls[0].Function.Name != "ls" {
		t.Fatalf("tool call: %+v", out.ToolCalls)
	}
	if out.Usage == nil || out.Usage.PromptTokens != 4 {
		t.Fatalf("usage: %+v", out.Usage)
	}
}

func TestResponsesStreamingFunctionCallArgumentsFromDelta(t *testing.T) {
	parser := newResponsesStreamParser()
	frames := []string{
		`{"type":"response.output_item.added","item":{"id":"fc_item_1","type":"function_call","call_id":"call_1","name":"read_file","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_item_1","delta":"{\"file_path\":"}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_item_1","delta":"\"/etc/hostname\"}"}`,
		`{"type":"response.completed"}`,
	}
	for _, frame := range frames {
		if _, _, err := parser.feedPayload([]byte(frame)); err != nil {
			t.Fatalf("feed %s: %v", frame, err)
		}
	}

	out := parser.result()
	if len(out.ToolCalls) != 1 {
		t.Fatalf("expected one tool call, got %+v", out.ToolCalls)
	}
	if got, want := out.ToolCalls[0].Function.Arguments, `{"file_path":"/etc/hostname"}`; got != want {
		t.Fatalf("streamed function arguments = %q, want %q", got, want)
	}
}

func TestResponsesStreamingFunctionCallArgumentsFromDone(t *testing.T) {
	parser := newResponsesStreamParser()
	frames := []string{
		`{"type":"response.output_item.added","item":{"id":"fc_item_2","type":"function_call","call_id":"call_2","name":"bash","arguments":""}}`,
		`{"type":"response.function_call_arguments.done","item_id":"fc_item_2","name":"bash","arguments":"{\"command\":\"pwd\"}"}`,
		`{"type":"response.completed"}`,
	}
	for _, frame := range frames {
		if _, _, err := parser.feedPayload([]byte(frame)); err != nil {
			t.Fatalf("feed %s: %v", frame, err)
		}
	}

	out := parser.result()
	if len(out.ToolCalls) != 1 {
		t.Fatalf("expected one tool call, got %+v", out.ToolCalls)
	}
	if got, want := out.ToolCalls[0].Function.Arguments, `{"command":"pwd"}`; got != want {
		t.Fatalf("final function arguments = %q, want %q", got, want)
	}
}

// --- Chat Completions stays untouched -------------------------------------

func TestChatCompletionsUnchanged(t *testing.T) {
	cfg := &APIConfig{
		BaseURL: "https://api.example.com/v1", APIKey: "sk", Model: "m",
	}
	if got := upstreamURL(cfg); got != "https://api.example.com/v1/chat/completions" {
		t.Fatalf("chat completions url changed: %s", got)
	}
	req, _ := http.NewRequest("POST", "https://x", nil)
	applyUpstreamAuth(req, cfg)
	if req.Header.Get("Authorization") != "Bearer sk" {
		t.Fatalf("bearer auth missing: %v", req.Header)
	}
	if req.Header.Get("x-api-key") != "" {
		t.Fatalf("chat completions should not send x-api-key: %v", req.Header)
	}
}
