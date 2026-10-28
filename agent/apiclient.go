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
	// Model is the model id that produced this response. It lets the usage
	// log and the statistics panel group requests by model and provider.
	Model string `json:"model,omitempty"`
	// Provider is the configured model id the request was routed through.
	Provider string `json:"provider,omitempty"`
}

// ContentPart is an OpenAI-compatible multimodal content part. It is only
// used in model requests; session persistence keeps Message.Content as text.
type ContentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *ImageURLPart `json:"image_url,omitempty"`
	// ImageRef is a placeholder for an image that lives on disk. It is
	// resolved into a real data URL only at the wire boundary
	// (apiMessagesForRequest), so the conversation context carries a path
	// instead of a full base64 payload.
	ImageRef *ImageRefPart `json:"image_ref,omitempty"`
}

type ImageURLPart struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// ImageRefPart points at an image file plus the parameters needed to encode
// it. The hash lets the materialiser skip re-encoding an image it has
// already produced, and the label keeps the human-readable identity of the
// attachment in the text alongside the reference.
type ImageRefPart struct {
	Path         string `json:"path"`
	Hash         string `json:"hash,omitempty"`
	Label        string `json:"label,omitempty"`
	MaxDimension int    `json:"max_dimension,omitempty"`
	Quality      int    `json:"quality,omitempty"`
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

// UsageInfo carries the token accounting reported by the upstream provider.
//
// Upstream providers report more than the two headline counters. Anthropic
// compatible gateways and the OpenAI compatible ones differ in the field
// name, so the common shapes are all decoded and normalised here:
//   - prompt_tokens_details.cached_tokens  (OpenAI style)
//   - cache_read_input_tokens              (Anthropic style)
//   - prompt_cache_hit_tokens               (DeepSeek style)
//   - completion_tokens_details.reasoning_tokens
//
// Anything upstream does not report stays at zero, so a provider without
// cache support simply reports a zero cache hit rate.
type UsageInfo struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	// CachedTokens is the part of PromptTokens that was served from the
	// provider's prompt cache.
	CachedTokens int `json:"cached_tokens,omitempty"`
	// ReasoningTokens is the part of CompletionTokens spent on hidden
	// reasoning.
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
	// PromptTokensDetails is the raw OpenAI style detail object, kept so the
	// provider specific shapes survive a round trip through the session log.
	PromptTokensDetails *UsageTokenDetails `json:"prompt_tokens_details,omitempty"`
	// CompletionTokensDetails is the raw OpenAI style detail object for the
	// completion side.
	CompletionTokensDetails *UsageTokenDetails `json:"completion_tokens_details,omitempty"`
	// CacheReadInputTokens is the Anthropic style cache counter.
	CacheReadInputTokens int `json:"cache_read_input_tokens,omitempty"`
	// CacheCreationInputTokens is the Anthropic style cache write counter.
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	// PromptCacheHitTokens is the DeepSeek style cache counter.
	PromptCacheHitTokens int `json:"prompt_cache_hit_tokens,omitempty"`
}

// UsageTokenDetails holds the per side breakdown some providers attach to
// their usage object.
type UsageTokenDetails struct {
	CachedTokens    int `json:"cached_tokens,omitempty"`
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

// Normalize folds the provider specific cache and reasoning counters into the
// single CachedTokens and ReasoningTokens fields. It is safe to call more than
// once, and safe on a zero valued UsageInfo.
func (u *UsageInfo) Normalize() {
	if u == nil {
		return
	}
	if u.PromptTokensDetails != nil && u.CachedTokens == 0 {
		u.CachedTokens = u.PromptTokensDetails.CachedTokens
	}
	if u.CompletionTokensDetails != nil && u.ReasoningTokens == 0 {
		u.ReasoningTokens = u.CompletionTokensDetails.ReasoningTokens
	}
	if u.CachedTokens == 0 {
		if u.CacheReadInputTokens > 0 {
			u.CachedTokens = u.CacheReadInputTokens
		} else if u.PromptCacheHitTokens > 0 {
			u.CachedTokens = u.PromptCacheHitTokens
		}
	}
	if u.CachedTokens > u.PromptTokens {
		// Defensive: a provider that reports a cache counter larger than the
		// total prompt count would otherwise produce a hit rate above 100%.
		u.CachedTokens = u.PromptTokens
	}
}

// CacheHitRate returns the share of the prompt that was served from cache.
func (u *UsageInfo) CacheHitRate() float64 {
	if u == nil || u.PromptTokens <= 0 {
		return 0
	}
	return float64(u.CachedTokens) / float64(u.PromptTokens)
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
	Model       string       `json:"model"`
	Messages    []APIMessage `json:"messages"`
	Tools       []ToolDef    `json:"tools,omitempty"`
	MaxTokens   int          `json:"max_tokens,omitempty"`
	Temperature float64      `json:"temperature,omitempty"`
	// ReasoningEffort is the OpenAI-style knob. Measured against MiniMax and
	// DeepSeek: both endpoints accept the field without erroring, but neither
	// showed a reproducible change in reasoning length, so it is opt-in and
	// empty means "do not send the field at all" rather than a disabled value.
	ReasoningEffort string            `json:"reasoning_effort,omitempty"`
	Stream          bool              `json:"stream,omitempty"`
	StreamOptions   *APIStreamOptions `json:"stream_options,omitempty"`
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
	apiURL := upstreamURL(cfg)
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
		applyUpstreamAuth(httpReq, cfg)

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

// reasoningKeepRecent 是尾部保留 reasoning 的消息条数。
// reasoning 不参与相关性评分，也不走向量检索：它对后续步骤几乎不提供信息，
// 唯一的价值是让「当前这一步」的思考在紧随其后的请求里保持连贯，
// 因此只需一个很小的固定窗口 —— 覆盖一轮里相邻的几次工具往返即可。
//
// 它不再是一个写死的编译期常量：config.json 的 reasoning.keep_recent 可以在
// 设置面板里调。包级默认值仍是 defaultReasoningKeepRecent（5），配置文件里
// 缺这个字段时行为与改动前逐字节一致；配置加载后由 setReasoningKeepRecent 覆盖。
var reasoningKeepRecent = defaultReasoningKeepRecent

// setReasoningKeepRecent 让 Config 里的 reasoning.keep_recent 生效。
// 只在启动加载配置时调用一次，运行期改配置需要重启 agent。
func setReasoningKeepRecent(n int) {
	if n > 0 {
		reasoningKeepRecent = n
	}
}

// stripHistoryReasoning 为 false 时恢复旧行为：所有历史 reasoning 都随请求重发。
var stripHistoryReasoning = true

// setReasoningEnabled 让 Config 里的 reasoning.enabled 生效。
// 关闭后思考窗口清理整个不跑，历史 reasoning 原样重发。
func setReasoningEnabled(on bool) {
	stripHistoryReasoning = on
}

// reasoningForRequest 决定一条历史消息的 reasoning 是否进入请求载荷。
// reasoning 是长会话里体积最大的单一上下文块，窗口外重发它几乎没有信息增益，
// 却会在之后每一轮被重复计费。这里只影响请求载荷，已存储的历史与会话记录
// 中的思考内容不受影响。
func reasoningForRequest(m Message, tailRank int) string {
	if m.ReasoningContent == "" {
		return ""
	}
	if !stripHistoryReasoning {
		return m.ReasoningContent
	}
	if tailRank > reasoningKeepRecent {
		return ""
	}
	return m.ReasoningContent
}

// contentForRequest 决定发给模型的历史消息正文里要不要保留 <thinking> 块。
//
// 背景：部分模型（MiniMax 等）把思考写在 Content 的 <thinking> 标签里，
// 而不是独立的 reasoning_content 字段。原先的窗口清理只处理
// reasoning_content，于是 Content 里的 thinking 被原样送出——
// 每条历史 assistant 消息最多带 capThinking 上限（3000 字符）的思考，
// 几十轮下来请求体就会膨胀到几十万 token，工具与思考历史的清理形同虚设。
//
// 这里与 reasoningForRequest 用同一个窗口：窗口内保留，窗口外剥离。
// 只影响请求载荷，不改动已存储的历史与会话记录。
func contentForRequest(content string, tailRank int) string {
	if content == "" {
		return ""
	}
	if !stripHistoryReasoning {
		return content
	}
	if tailRank > reasoningKeepRecent {
		return stripThinking(content)
	}
	return content
}

func apiMessagesForRequest(messages []Message) []APIMessage {
	clean := make([]APIMessage, len(messages))
	for i, message := range messages {
		// tailRank: 从消息尾部数的排名，越小越新。窗口判定统一用它。
		tailRank := len(messages) - i
		body := contentForRequest(message.Content, tailRank)
		wire := APIMessage{
			Role:             message.Role,
			ReasoningContent: reasoningForRequest(message, tailRank),
			ToolCalls:        message.ToolCalls,
			ToolCallID:       message.ToolCallID,
			Name:             message.Name,
		}
		if len(message.ContentParts) > 0 {
			parts := make([]ContentPart, 0, len(message.ContentParts)+1)
			if body != "" {
				parts = append(parts, ContentPart{Type: "text", Text: body})
			}
			// Placeholders become real images here, at the last moment
			// before the request goes out. The conversation context keeps
			// only the path, so a long transcript does not carry a base64
			// copy of every screenshot it ever saw.
			for _, part := range message.ContentParts {
				if part.Type == "image_ref" && part.ImageRef != nil {
					materialized, ok := materializeImageRef(*part.ImageRef)
					if !ok {
						// The file is gone or unreadable. Skip the image
						// rather than failing the whole request; the label
						// in the text still says what was attached.
						continue
					}
					parts = append(parts, materialized)
					continue
				}
				parts = append(parts, part)
			}
			wire.Content = parts
		} else if body != "" {
			wire.Content = body
		}
		clean[i] = wire
	}
	return clean
}

func CallLLM(cfg *APIConfig, messages []Message, toolDefs []ToolDef) (*APIResponse, error) {
	start := time.Now()

	// Strip usage/duration_ms from messages before sending (API rejects them)
	cleanMsgs := apiMessagesForRequest(messages)

	// The body shape depends on the upstream dialect, so it is built here
	// rather than in one shared struct.
	dialect := NormalizeDialect(cfg.Format)
	var bodyJSON []byte
	switch dialect {
	case DialectAnthropicMessages:
		anReq, err := buildAnthropicRequest(cfg, cleanMsgs, toolDefs, false)
		if err != nil {
			return nil, fmt.Errorf("build anthropic request: %w", err)
		}
		bodyJSON, err = json.Marshal(anReq)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
	case DialectResponses:
		respReq, err := buildResponsesRequest(cfg, cleanMsgs, toolDefs, false)
		if err != nil {
			return nil, fmt.Errorf("build responses request: %w", err)
		}
		bodyJSON, err = json.Marshal(respReq)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
	default:
		reqBody := APIRequest{
			Model:           cfg.Model,
			Messages:        cleanMsgs,
			MaxTokens:       cfg.MaxTokens,
			Temperature:     cfg.Temperature,
			ReasoningEffort: cfg.ReasoningEffort,
		}
		if len(toolDefs) > 0 {
			reqBody.Tools = toolDefs
		}
		raw, mErr := json.Marshal(reqBody)
		if mErr != nil {
			return nil, fmt.Errorf("marshal request: %w", mErr)
		}
		bodyJSON = raw
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

	// The response envelope differs per dialect too.
	switch dialect {
	case DialectAnthropicMessages:
		parsed, err := parseAnthropicResponse(respBody)
		if err != nil {
			return nil, err
		}
		parsed.DurationMs = duration
		return parsed, nil
	case DialectResponses:
		parsed, err := parseResponsesResponse(respBody)
		if err != nil {
			return nil, err
		}
		parsed.DurationMs = duration
		return parsed, nil
	}

	var realResp RealAPIResponse
	if err := json.Unmarshal(respBody, &realResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if len(realResp.Choices) == 0 {
		return nil, fmt.Errorf("empty choices")
	}

	// Some Responses-compatible APIs return an empty arguments string when no
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

	// The streaming body shape depends on the dialect, same as the
	// non-streaming path.
	dialect := NormalizeDialect(cfg.Format)
	var bodyJSON []byte
	switch dialect {
	case DialectAnthropicMessages:
		anReq, aErr := buildAnthropicRequest(cfg, cleanMsgs, toolDefs, true)
		if aErr != nil {
			return nil, fmt.Errorf("build anthropic request: %w", aErr)
		}
		raw, mErr := json.Marshal(anReq)
		if mErr != nil {
			return nil, fmt.Errorf("marshal request: %w", mErr)
		}
		bodyJSON = raw
	case DialectResponses:
		respReq, rErr := buildResponsesRequest(cfg, cleanMsgs, toolDefs, true)
		if rErr != nil {
			return nil, fmt.Errorf("build responses request: %w", rErr)
		}
		raw, mErr := json.Marshal(respReq)
		if mErr != nil {
			return nil, fmt.Errorf("marshal request: %w", mErr)
		}
		bodyJSON = raw
	default:
		reqBody := APIRequest{
			Model:           cfg.Model,
			Messages:        cleanMsgs,
			MaxTokens:       cfg.MaxTokens,
			Temperature:     cfg.Temperature,
			ReasoningEffort: cfg.ReasoningEffort,
			Stream:          true,
			StreamOptions:   &APIStreamOptions{IncludeUsage: true},
		}
		if len(toolDefs) > 0 {
			reqBody.Tools = toolDefs
		}
		raw, mErr := json.Marshal(reqBody)
		if mErr != nil {
			return nil, fmt.Errorf("marshal request: %w", mErr)
		}
		bodyJSON = raw
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

	// Non-OpenAI dialects get their own parser; the Chat Completions shape
	// is handled inline below.
	var dialectParser streamDialectParser
	if dialect != DialectChatCompletions {
		dialectParser = newStreamDialectParser(dialect)
	}

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

				// Anthropic and Responses carry their event type inside the
				// payload, so they are handled by their own parser.
				if dialectParser != nil {
					textDelta, argBytes, pErr := dialectParser.feedPayload([]byte(payload))
					if pErr != nil {
						return nil, pErr
					}
					progressMu.Lock()
					progress.ChunkCount++
					progressMu.Unlock()
					if textDelta != "" {
						content.WriteString(textDelta)
						progressMu.Lock()
						progress.ContentBytes += len(textDelta)
						progressMu.Unlock()
						if onDelta != nil {
							onDelta(textDelta)
						}
					}
					if argBytes > 0 {
						progressMu.Lock()
						progress.ToolArgumentsBytes += argBytes
						progressMu.Unlock()
					}
					if textDelta != "" || argBytes > 0 {
						now := time.Now()
						progressMu.Lock()
						if firstDeltaAt.IsZero() {
							firstDeltaAt = now
							lastDeltaAt = now
							progress.Phase = "first_delta"
						} else {
							lastDeltaAt = now
							progress.Phase = "streaming_response"
						}
						emitProgressLocked()
						progressMu.Unlock()
					}
					continue
				}

				var chunk streamChunk
				if json.Unmarshal([]byte(payload), &chunk) != nil {
					continue
				}
				progressMu.Lock()
				progress.ChunkCount++
				progressMu.Unlock()
				if chunk.Usage != nil {
					// Fold the provider specific cache and reasoning
					// counters into the normalised fields, so the usage
					// log and the statistics panel see one shape.
					chunk.Usage.Normalize()
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

	// Non-OpenAI dialects assembled their response in their own parser.
	if dialectParser != nil {
		out := dialectParser.result()
		out.DurationMs = duration
		out.Streaming = true
		return out, nil
	}

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
