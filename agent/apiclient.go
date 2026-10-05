package main

import (
	"agentloop/agent/internal/dtypes"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Message is the standard chat message format
type Message struct {
	Role             string         `json:"role"`
	Content          string         `json:"content,omitempty"`
	ContentParts     []ContentPart  `json:"-"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	InternalType     string         `json:"internal_type,omitempty"`
	ToolCalls        []ToolCall     `json:"tool_calls,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
	Name             string         `json:"name,omitempty"`
	Usage            *UsageInfo     `json:"usage,omitempty"`
	DurationMs       int64          `json:"duration_ms,omitempty"`
	RealMs           int64          `json:"real_ms,omitempty"`
	Step             int            `json:"step,omitempty"`
	Ts               int64          `json:"ts,omitempty"`
	ToolsUsed        map[string]int `json:"tools_used,omitempty"`
	SessionID        string         `json:"session_id,omitempty"`
	InteractionID    string         `json:"interaction_id,omitempty"`
}

// ContentPart is an OpenAI-compatible multimodal content part. It is only
// used in model requests; session persistence keeps Message.Content as text.
type ContentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *ImageURLPart `json:"image_url,omitempty"`
}

type ImageURLPart struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function ToolCallFunc `json:"function"`
}

type ToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type UsageInfo struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// APIResponse from LLM
type APIResponse struct {
	Content              string     `json:"content,omitempty"`
	ReasoningContent     string     `json:"reasoning_content,omitempty"`
	ToolCalls            []ToolCall `json:"tool_calls,omitempty"`
	Usage                *UsageInfo `json:"usage,omitempty"`
	FinishStop           string     `json:"finish_stop,omitempty"`
	DurationMs           int64      `json:"duration_ms,omitempty"`
	UpstreamRequestID    string     `json:"upstream_request_id,omitempty"`
	RequestSentAtMs      int64      `json:"request_sent_at_ms,omitempty"`
	ResponseCompleteAtMs int64      `json:"response_complete_at_ms,omitempty"`
	FirstDeltaMs         int64      `json:"first_delta_ms,omitempty"`
	StreamChunkCount     int        `json:"stream_chunk_count,omitempty"`
	ReceivedBytes        int        `json:"received_bytes,omitempty"`
	ContentBytes         int        `json:"content_bytes,omitempty"`
	ToolArgumentsBytes   int        `json:"tool_arguments_bytes,omitempty"`
	Streaming            bool       `json:"streaming,omitempty"`
}

// LLMStreamProgress is the low-frequency heartbeat emitted while an upstream
// streaming request is waiting for or receiving data. It intentionally carries
// only counters and timings, never prompt or response content.
type LLMStreamProgress struct {
	Phase              string
	ElapsedMs          int64
	SinceLastDeltaMs   int64
	ReceivedBytes      int
	ContentBytes       int
	ToolArgumentsBytes int
	ChunkCount         int
	ToolCalls          []ToolCall
}

type LLMProgressFunc func(LLMStreamProgress)

// Request to LLM API
type APIRequest struct {
	Model         string            `json:"model"`
	Messages      []APIMessage      `json:"messages"`
	Tools         []ToolDef         `json:"tools,omitempty"`
	MaxTokens     int               `json:"max_tokens,omitempty"`
	Temperature   float64           `json:"temperature,omitempty"`
	Stream        bool              `json:"stream,omitempty"`
	StreamOptions *APIStreamOptions `json:"stream_options,omitempty"`
}

// APIMessage is the wire shape sent to /chat/completions. Content is `any` so
// text-only messages keep the historical string shape while native-vision
// messages serialize as an array of content parts.
type APIMessage struct {
	Role             string     `json:"role"`
	Content          any        `json:"content,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	Name             string     `json:"name,omitempty"`
}

type APIStreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

type ToolDef = dtypes.ToolDef

// RealAPIResponse is the raw response from the external API
type RealAPIResponse struct {
	Choices []struct {
		Message struct {
			Content          string     `json:"content"`
			ReasoningContent string     `json:"reasoning_content"`
			ToolCalls        []ToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *UsageInfo `json:"usage"`
}

// isRetryableStatus reports whether an HTTP status is a transient
// server-side failure worth retrying with backoff.
func isRetryableStatus(status int) bool {
	switch status {
	case 429, 500, 502, 503, 504:
		return true
	}
	return false
}

// doChatCompletion POSTs to /chat/completions and retries transient failures
// (429, 5xx, network errors) with exponential backoff, so a long autonomous
// run survives temporary API hiccups instead of aborting mid-task. The caller
// owns resp.Body.
func doChatCompletion(cfg *APIConfig, reqBody []byte) (*http.Response, error) {
	return doChatCompletionCtx(context.Background(), cfg, reqBody)
}

// doChatCompletionCtx is the cancellable variant. The retry backoff respects
// ctx so a user-triggered cancel breaks the loop immediately instead of
// sleeping through MaxRetries iterations.
func doChatCompletionCtx(ctx context.Context, cfg *APIConfig, reqBody []byte) (*http.Response, error) {
	apiURL := strings.TrimRight(cfg.BaseURL, "/") + "/chat/completions"
	maxRetries := cfg.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	baseMs := cfg.RetryBaseMs
	if baseMs <= 0 {
		baseMs = 1000
	}
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		client := &http.Client{Timeout: time.Duration(cfg.TimeoutSec) * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
		httpReq, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(reqBody))
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+cfg.APIKey)

		resp, err := client.Do(httpReq)
		if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp, nil
		}
		if err != nil {
			lastErr = fmt.Errorf("http call: %w", err)
		} else {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
			if !isRetryableStatus(resp.StatusCode) {
				return nil, lastErr
			}
		}
		if attempt < maxRetries {
			delay := time.Duration(baseMs*(1<<attempt)) * time.Millisecond
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	return nil, lastErr
}

// dumpFailedRequest writes a failing request's message sequence to a debug
// file so the exact payload can be inspected after an unrecoverable API error.
func dumpFailedRequest(cleanMsgs []Message) {
	defer func() { _ = recover() }()
	var sb strings.Builder
	sb.WriteString("--- MESSAGES ---\n")
	for i, m := range cleanMsgs {
		head := m.Content
		if len(head) > 300 {
			head = head[:300]
		}
		tc := ""
		if len(m.ToolCalls) > 0 {
			names := []string{}
			for _, t := range m.ToolCalls {
				names = append(names, t.Function.Name)
			}
			tc = " tool_calls=" + strings.Join(names, ",")
		}
		sb.WriteString(fmt.Sprintf("[%d] role=%s%s content=%q\n", i, m.Role, tc, head))
	}
	_ = os.WriteFile(filepath.Join(os.TempDir(), "apiclient_debug_fail.txt"), []byte(sb.String()), 0644)
}

func apiMessagesForRequest(messages []Message) []APIMessage {
	clean := make([]APIMessage, len(messages))
	for i, message := range messages {
		wire := APIMessage{
			Role:             message.Role,
			ReasoningContent: message.ReasoningContent,
			ToolCalls:        message.ToolCalls,
			ToolCallID:       message.ToolCallID,
			Name:             message.Name,
		}
		if len(message.ContentParts) > 0 {
			parts := make([]ContentPart, 0, len(message.ContentParts)+1)
			if message.Content != "" {
				parts = append(parts, ContentPart{Type: "text", Text: message.Content})
			}
			parts = append(parts, message.ContentParts...)
			wire.Content = parts
		} else if message.Content != "" {
			wire.Content = message.Content
		}
		clean[i] = wire
	}
	return clean
}

func CallLLM(cfg *APIConfig, messages []Message, toolDefs []ToolDef) (*APIResponse, error) {
	start := time.Now()

	// Strip usage/duration_ms from messages before sending (API rejects them)
	cleanMsgs := apiMessagesForRequest(messages)

	reqBody := APIRequest{
		Model:       cfg.Model,
		Messages:    cleanMsgs,
		MaxTokens:   cfg.MaxTokens,
		Temperature: cfg.Temperature,
	}
	if len(toolDefs) > 0 {
		reqBody.Tools = toolDefs
	}

	bodyJSON, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	resp, err := doChatCompletion(cfg, bodyJSON)
	if err != nil {
		dumpFailedRequest(messages)
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	duration := time.Since(start).Milliseconds()

	var realResp RealAPIResponse
	if err := json.Unmarshal(respBody, &realResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if len(realResp.Choices) == 0 {
		return nil, fmt.Errorf("empty choices")
	}

	// Fix: Clotho API returns empty string for arguments when no params,
	// but rejects empty arguments on re-submission. Default to "{}".
	for i := range realResp.Choices[0].Message.ToolCalls {
		if realResp.Choices[0].Message.ToolCalls[i].Function.Arguments == "" {
			realResp.Choices[0].Message.ToolCalls[i].Function.Arguments = "{}"
		}
	}

	choice := realResp.Choices[0]
	result := &APIResponse{
		Content:          choice.Message.Content,
		ReasoningContent: choice.Message.ReasoningContent,
		ToolCalls:        choice.Message.ToolCalls,
		Usage:            realResp.Usage,
		DurationMs:       duration,
		FinishStop:       choice.FinishReason,
	}
	return result, nil
}

// streamChunk is one SSE data payload from an OpenAI-compatible streaming
// /chat/completions response.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *UsageInfo `json:"usage"`
}

type streamToolAcc struct {
	id, name string
	args     strings.Builder
}

// collapseStreamTools flattens the per-index streamToolAcc map into the
// ordered ToolCall slice the agent loop expects. Extracted so the ctx-cancel
// early-return path can produce the same shape.
func collapseStreamTools(toolAcc map[int]streamToolAcc) []ToolCall {
	out := make([]ToolCall, 0, len(toolAcc))
	idxList := make([]int, 0, len(toolAcc))
	for idx := range toolAcc {
		idxList = append(idxList, idx)
	}
	sort.Ints(idxList)
	for _, idx := range idxList {
		acc := toolAcc[idx]
		args := acc.args.String()
		if args == "" {
			args = "{}"
		}
		out = append(out, ToolCall{
			ID:   acc.id,
			Type: "function",
			Function: ToolCallFunc{
				Name:      acc.name,
				Arguments: args,
			},
		})
	}
	return out
}

var llmProgressHeartbeatInterval = 5 * time.Second

func upstreamRequestID(header http.Header) string {
	for _, name := range []string{"X-Request-Id", "Request-Id", "X-Correlation-Id"} {
		if value := strings.TrimSpace(header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

// CallLLMStream calls the LLM with stream:true (OpenAI-compatible SSE). Each
// visible text delta is forwarded to onDelta as it arrives; tool_call deltas
// are accumulated here and returned in the final response together with the
// full content, usage and finish reason.
func CallLLMStream(cfg *APIConfig, messages []Message, toolDefs []ToolDef, onDelta func(string)) (*APIResponse, error) {
	return CallLLMStreamCtxWithProgress(context.Background(), cfg, messages, toolDefs, onDelta, nil)
}

// CallLLMStreamCtx is the cancellable streaming variant. When ctx is
// cancelled mid-flight the read loop exits immediately and the partial
// response (Content/ToolCalls so far) is returned with ctx.Err(). The SSE
// connection is closed by the http.Client when ctx is cancelled.
func CallLLMStreamCtx(ctx context.Context, cfg *APIConfig, messages []Message, toolDefs []ToolDef, onDelta func(string)) (*APIResponse, error) {
	return CallLLMStreamCtxWithProgress(ctx, cfg, messages, toolDefs, onDelta, nil)
}

// CallLLMStreamCtxWithProgress adds a low-frequency heartbeat and captures
// upstream request/stream diagnostics for a cancellable streaming call.
func CallLLMStreamCtxWithProgress(
	ctx context.Context,
	cfg *APIConfig,
	messages []Message,
	toolDefs []ToolDef,
	onDelta func(string),
	onProgress LLMProgressFunc,
) (out *APIResponse, retErr error) {
	start := time.Now()

	cleanMsgs := apiMessagesForRequest(messages)

	reqBody := APIRequest{
		Model:         cfg.Model,
		Messages:      cleanMsgs,
		MaxTokens:     cfg.MaxTokens,
		Temperature:   cfg.Temperature,
		Stream:        true,
		StreamOptions: &APIStreamOptions{IncludeUsage: true},
	}
	if len(toolDefs) > 0 {
		reqBody.Tools = toolDefs
	}

	bodyJSON, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	var progressMu sync.Mutex
	progress := LLMStreamProgress{Phase: "waiting_response"}
	var lastDeltaAt time.Time
	emitProgressLocked := func() {
		if onProgress == nil {
			return
		}
		progress.ElapsedMs = time.Since(start).Milliseconds()
		if lastDeltaAt.IsZero() {
			progress.SinceLastDeltaMs = progress.ElapsedMs
		} else {
			progress.SinceLastDeltaMs = time.Since(lastDeltaAt).Milliseconds()
		}
		onProgress(progress)
	}
	emitProgress := func() {
		progressMu.Lock()
		emitProgressLocked()
		progressMu.Unlock()
	}
	progressDone := make(chan struct{})
	var progressWG sync.WaitGroup
	var progressStopOnce sync.Once
	stopProgress := func() {
		progressStopOnce.Do(func() {
			close(progressDone)
			progressWG.Wait()
		})
	}
	defer stopProgress()

	if onProgress != nil {
		emitProgress()
		progressWG.Add(1)
		go func() {
			defer progressWG.Done()
			ticker := time.NewTicker(llmProgressHeartbeatInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					emitProgress()
				case <-progressDone:
					return
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	requestSentAtMs := time.Now().UnixMilli()
	resp, err := doChatCompletionCtx(ctx, cfg, bodyJSON)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	requestID := upstreamRequestID(resp.Header)
	var firstDeltaAt time.Time
	var lastToolArgsEmit time.Time
	var emittedToolArguments bool
	var emittedStreamingResponse bool
	var responseCompleteAtMs int64
	defer func() {
		responseCompleteAtMs = time.Now().UnixMilli()
		progressMu.Lock()
		if retErr != nil {
			progress.Phase = "response_error"
		} else {
			progress.Phase = "response_complete"
		}
		emitProgressLocked()
		receivedBytes := progress.ReceivedBytes
		contentBytes := progress.ContentBytes
		toolArgumentsBytes := progress.ToolArgumentsBytes
		chunkCount := progress.ChunkCount
		progressMu.Unlock()
		stopProgress()

		if out != nil {
			out.UpstreamRequestID = requestID
			out.RequestSentAtMs = requestSentAtMs
			out.ResponseCompleteAtMs = responseCompleteAtMs
			out.StreamChunkCount = chunkCount
			out.ReceivedBytes = receivedBytes
			out.ContentBytes = contentBytes
			out.ToolArgumentsBytes = toolArgumentsBytes
			out.Streaming = true
			if !firstDeltaAt.IsZero() {
				out.FirstDeltaMs = firstDeltaAt.Sub(start).Milliseconds()
			}
		}
	}()

	var content strings.Builder
	var reasoningContent strings.Builder
	toolAcc := make(map[int]streamToolAcc)
	finish := ""
	var usage *UsageInfo

	reader := bufio.NewReader(resp.Body)
	for {
		if err := ctx.Err(); err != nil {
			return &APIResponse{
				Content:          content.String(),
				ReasoningContent: reasoningContent.String(),
				ToolCalls:        collapseStreamTools(toolAcc),
				Usage:            usage,
				DurationMs:       time.Since(start).Milliseconds(),
				FinishStop:       finish,
			}, err
		}
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			progressMu.Lock()
			progress.ReceivedBytes += len(line)
			progressMu.Unlock()
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "data:") {
				payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
				if payload == "[DONE]" {
					break
				}
				var chunk streamChunk
				if json.Unmarshal([]byte(payload), &chunk) != nil {
					continue
				}
				progressMu.Lock()
				progress.ChunkCount++
				progressMu.Unlock()
				if chunk.Usage != nil {
					usage = chunk.Usage
				}
				hadContentDelta := false
				hadToolArgumentDelta := false
				for _, choice := range chunk.Choices {
					if choice.FinishReason != "" {
						finish = choice.FinishReason
					}
					if d := choice.Delta.Content; d != "" {
						hadContentDelta = true
						content.WriteString(d)
						progressMu.Lock()
						progress.ContentBytes += len(d)
						progressMu.Unlock()
						if onDelta != nil {
							onDelta(d)
						}
					}
					if d := choice.Delta.ReasoningContent; d != "" {
						reasoningContent.WriteString(d)
					}
					for _, tc := range choice.Delta.ToolCalls {
						acc, ok := toolAcc[tc.Index]
						if !ok {
							acc = streamToolAcc{id: tc.ID, name: tc.Function.Name}
						}
						if tc.ID != "" {
							acc.id = tc.ID
						}
						if tc.Function.Name != "" {
							acc.name = tc.Function.Name
						}
						if tc.Function.Arguments != "" {
							acc.args.WriteString(tc.Function.Arguments)
							hadToolArgumentDelta = true
							progressMu.Lock()
							progress.ToolArgumentsBytes += len(tc.Function.Arguments)
							progressMu.Unlock()
						}
						toolAcc[tc.Index] = acc
					}
				}
				if hadContentDelta || hadToolArgumentDelta {
					now := time.Now()
					progressMu.Lock()
					firstDelta := firstDeltaAt.IsZero()
					if firstDelta {
						firstDeltaAt = now
						lastDeltaAt = now
					} else {
						lastDeltaAt = now
					}
					if firstDelta {
						progress.Phase = "first_delta"
						emitProgressLocked()
					}
					if hadToolArgumentDelta {
						progress.Phase = "tool_arguments"
						progress.ToolCalls = collapseStreamTools(toolAcc)
						if !emittedToolArguments || now.Sub(lastToolArgsEmit) >= 50*time.Millisecond {
							emittedToolArguments = true
							lastToolArgsEmit = now
							emitProgressLocked()
						}
					} else {
						progress.Phase = "streaming_response"
						if !emittedStreamingResponse {
							emittedStreamingResponse = true
							emitProgressLocked()
						}
					}
					progressMu.Unlock()
				}
			}
		}
		if err != nil {
			break
		}
	}

	duration := time.Since(start).Milliseconds()

	toolCalls := collapseStreamTools(toolAcc)

	return &APIResponse{
		Content:          content.String(),
		ReasoningContent: reasoningContent.String(),
		ToolCalls:        toolCalls,
		Usage:            usage,
		DurationMs:       duration,
		FinishStop:       finish,
	}, nil
}
