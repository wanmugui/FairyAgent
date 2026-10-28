package main

// Upstream dialect adapters.
//
// Fairy talks to three kinds of upstream, and they differ in more than the
// URL: the request body shape, the auth header, the tool-call encoding and
// the response envelope all change. Historically the client only spoke
// OpenAI Chat Completions, so a provider that offered anything else was
// misconfigured no matter what the settings panel said.
//
// This file holds the wire shapes for the other two dialects and the
// translation between them and the internal Message/ToolCall model:
//   - chat_completions   OpenAI Chat Completions (the original)
//   - responses          OpenAI Responses
//   - anthropic_messages Anthropic Messages (Claude)
//
// The dialect is chosen per provider and carried on APIConfig.Format.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Upstream dialect identifiers. These strings are what config.json stores
// in provider.format, so they must stay stable.
const (
	DialectChatCompletions   = "chat_completions"
	DialectResponses         = "responses"
	DialectAnthropicMessages = "anthropic_messages"
)

// NormalizeDialect maps a stored value onto a known dialect. An empty or
// unknown value falls back to Chat Completions, which is what every
// provider spoke before dialects existed.
func NormalizeDialect(v string) string {
	switch strings.TrimSpace(strings.ToLower(v)) {
	case DialectResponses:
		return DialectResponses
	case DialectAnthropicMessages:
		return DialectAnthropicMessages
	default:
		return DialectChatCompletions
	}
}

// dialectEndpoint returns the path segment for a dialect. The base url is
// expected to stop at the version prefix (for example ".../v1").
func dialectEndpoint(dialect string) string {
	switch NormalizeDialect(dialect) {
	case DialectResponses:
		return "/responses"
	case DialectAnthropicMessages:
		return "/messages"
	default:
		return "/chat/completions"
	}
}

// dialectOutputLimitField returns the request field that carries the output
// limit. Anthropic and Chat Completions use max_tokens; Responses uses
// max_output_tokens.
func dialectOutputLimitField(dialect string) string {
	if NormalizeDialect(dialect) == DialectResponses {
		return "max_output_tokens"
	}
	return "max_tokens"
}

// upstreamURL returns the full endpoint for a provider.
//
// The base url is expected to stop at the version prefix (".../v1"), so the
// dialect's path is appended. A base url that already carries the endpoint
// is used as-is, because users routinely paste the full URL their docs
// show and duplicating the suffix would 404.
func upstreamURL(cfg *APIConfig) string {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	endpoint := dialectEndpoint(NormalizeDialect(cfg.Format))
	if strings.HasSuffix(base, endpoint) {
		return base
	}
	return base + endpoint
}

// applyUpstreamAuth sets the authentication headers for a dialect.
//
// OpenAI-style upstreams take a bearer token. Anthropic takes the key in
// the x-api-key header and additionally requires an API version header.
func applyUpstreamAuth(req *http.Request, cfg *APIConfig) {
	switch NormalizeDialect(cfg.Format) {
	case DialectAnthropicMessages:
		req.Header.Set("x-api-key", cfg.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")
		// Some Anthropic-compatible gateways also accept a bearer token;
		// sending it does no harm and helps those gateways.
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	default:
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
}

// ---------------------------------------------------------------------------
// Anthropic Messages
// ---------------------------------------------------------------------------

// anthropicRequest is the Claude Messages API request body.
type anthropicRequest struct {
	Model       string             `json:"model"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature *float64           `json:"temperature,omitempty"`
	Stream      bool               `json:"stream,omitempty"`
}

// anthropicMessage is one turn. Claude has no "tool" role: a tool result is
// a user turn whose content is a tool_result block, and a tool invocation
// is an assistant turn whose content is a tool_use block.
type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

// anthropicBlock is one content block inside a turn. The type tag decides
// how the rest of the struct is interpreted.
type anthropicBlock struct {
	Type string `json:"type"`

	// text block
	Text string `json:"text,omitempty"`

	// image block
	Source *anthropicImageSource `json:"source,omitempty"`

	// tool_use block
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result block
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

type anthropicImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// anthropicResponse is the non-streaming response envelope.
type anthropicResponse struct {
	ID         string           `json:"id"`
	Model      string           `json:"model"`
	Content    []anthropicBlock `json:"content"`
	StopReason string           `json:"stop_reason"`
	Usage      *anthropicUsage  `json:"usage,omitempty"`
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// buildAnthropicRequest translates the internal message list into the
// Claude Messages shape.
//
// The streaming flag is off by default: the non-streaming call path asks
// for a single JSON document, and a stream would come back as SSE frames
// instead. The streaming call path turns it on.
//
// The three structural differences from OpenAI that matter:
//  1. The system prompt is a top-level field, not a message.
//  2. A tool result is a user turn carrying a tool_result block.
//  3. A tool call is an assistant turn carrying a tool_use block.
func buildAnthropicRequest(cfg *APIConfig, messages []APIMessage, toolDefs []ToolDef, stream bool) (*anthropicRequest, error) {
	req := &anthropicRequest{
		Model:     cfg.Model,
		MaxTokens: cfg.MaxTokens,
		Stream:    stream,
	}
	// Claude rejects max_tokens of 0, and the upstream always needs a ceiling.
	if req.MaxTokens <= 0 {
		req.MaxTokens = 4096
	}
	if cfg.Temperature > 0 {
		t := cfg.Temperature
		req.Temperature = &t
	}

	for _, m := range messages {
		text, parts := splitAPIContent(m.Content)
		switch m.Role {
		case "system", "developer":
			// System text is hoisted to the top level. Claude allows a single
			// system string, so multiple system turns are concatenated.
			if s := strings.TrimSpace(text); s != "" {
				if req.System == "" {
					req.System = s
				} else {
					req.System += "\n\n" + s
				}
			}
		case "tool":
			// A tool result is a user turn with a tool_result block.
			if text == "" {
				text = "(empty tool result)"
			}
			req.Messages = append(req.Messages, anthropicMessage{
				Role: "user",
				Content: []anthropicBlock{{
					Type:      "tool_result",
					ToolUseID: m.ToolCallID,
					Content:   text,
				}},
			})
		case "assistant":
			blocks := []anthropicBlock{}
			if s := strings.TrimSpace(text); s != "" {
				blocks = append(blocks, anthropicBlock{Type: "text", Text: s})
			}
			for _, tc := range m.ToolCalls {
				input := json.RawMessage(tc.Function.Arguments)
				if len(input) == 0 {
					input = json.RawMessage("{}")
				}
				blocks = append(blocks, anthropicBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Function.Name,
					Input: input,
				})
			}
			if len(blocks) > 0 {
				req.Messages = append(req.Messages, anthropicMessage{Role: "assistant", Content: blocks})
			}
		default: // "user" and anything else
			blocks := []anthropicBlock{}
			if s := strings.TrimSpace(text); s != "" {
				blocks = append(blocks, anthropicBlock{Type: "text", Text: s})
			}
			// Native vision: image parts become Claude image blocks.
			for _, part := range parts {
				if part.Type != "image" {
					continue
				}
				blk, ok := anthropicImageBlock(part)
				if ok {
					blocks = append(blocks, blk)
				}
			}
			if len(blocks) > 0 {
				req.Messages = append(req.Messages, anthropicMessage{Role: "user", Content: blocks})
			}
		}
	}

	for _, td := range toolDefs {
		name, desc, schema := toolDefParts(td)
		req.Tools = append(req.Tools, anthropicTool{
			Name:        name,
			Description: desc,
			InputSchema: schema,
		})
	}

	return req, nil
}

// splitAPIContent unwraps the polymorphic Content field of an APIMessage.
//
// Text-only turns carry a plain string; native-vision turns carry a slice
// of content parts. The dialects need the text and the parts separately,
// so they are pulled apart once here.
func splitAPIContent(content any) (text string, parts []ContentPart) {
	switch v := content.(type) {
	case string:
		return v, nil
	case []ContentPart:
		return "", v
	case []any:
		for _, item := range v {
			switch p := item.(type) {
			case ContentPart:
				parts = append(parts, p)
			case map[string]any:
				// A part that crossed a JSON boundary.
				raw, err := json.Marshal(p)
				if err != nil {
					continue
				}
				var cp ContentPart
				if err := json.Unmarshal(raw, &cp); err == nil {
					parts = append(parts, cp)
				}
			}
		}
		return "", parts
	case nil:
		return "", nil
	default:
		return fmt.Sprint(v), nil
	}
}

// toolDefParts unpacks a ToolDef into the pieces every dialect needs.
//
// ToolDef.Function is an interface{} that carries the OpenAI function
// declaration as a map (see tool.Schema.ToolDef), so the values are read
// out defensively: a tool built by hand in a test may omit them.
func toolDefParts(td ToolDef) (name, description string, schema json.RawMessage) {
	schema = json.RawMessage(`{"type":"object","properties":{}}`)
	fn, ok := td.Function.(map[string]interface{})
	if !ok {
		return "", "", schema
	}
	if v, ok := fn["name"].(string); ok {
		name = v
	}
	if v, ok := fn["description"].(string); ok {
		description = v
	}
	if params, ok := fn["parameters"]; ok && params != nil {
		if raw, err := json.Marshal(params); err == nil && len(raw) > 0 {
			schema = raw
		}
	}
	return name, description, schema
}

// anthropicImageBlock converts an OpenAI-style image part into a Claude
// image block. Claude wants base64 data inline, or a URL for remote images.
func anthropicImageBlock(part ContentPart) (anthropicBlock, bool) {
	if part.ImageURL != nil && part.ImageURL.URL != "" {
		url := part.ImageURL.URL
		if strings.HasPrefix(url, "data:") {
			// data:<media type>;base64,<payload>
			meta, payload := splitDataURL(url)
			return anthropicBlock{
				Type: "image",
				Source: &anthropicImageSource{
					Type:      "base64",
					MediaType: meta,
					Data:      payload,
				},
			}, true
		}
		return anthropicBlock{
			Type:   "image",
			Source: &anthropicImageSource{Type: "url", URL: url},
		}, true
	}
	return anthropicBlock{}, false
}

func splitDataURL(url string) (mediaType, payload string) {
	// "data:image/png;base64,AAAABBB"
	rest := strings.TrimPrefix(url, "data:")
	idx := strings.Index(rest, ",")
	if idx < 0 {
		return "", ""
	}
	meta := rest[:idx]
	payload = rest[idx+1:]
	mediaType = strings.TrimSuffix(meta, ";base64")
	return mediaType, payload
}

// parseAnthropicResponse turns a Claude response into the internal
// APIResponse shape.
func parseAnthropicResponse(raw []byte) (*APIResponse, error) {
	var ar anthropicResponse
	if err := json.Unmarshal(raw, &ar); err != nil {
		return nil, fmt.Errorf("parse anthropic response: %w", err)
	}
	out := &APIResponse{}
	var texts []string
	for _, blk := range ar.Content {
		switch blk.Type {
		case "text":
			texts = append(texts, blk.Text)
		case "tool_use":
			out.ToolCalls = append(out.ToolCalls, ToolCall{
				ID:   blk.ID,
				Type: "function",
				Function: ToolCallFunc{
					Name:      blk.Name,
					Arguments: string(blk.Input),
				},
			})
		}
	}
	out.Content = strings.Join(texts, "")
	if ar.Usage != nil {
		out.Usage = &UsageInfo{
			PromptTokens:     ar.Usage.InputTokens,
			CompletionTokens: ar.Usage.OutputTokens,
			CachedTokens:     ar.Usage.CacheReadInputTokens,
		}
	}
	switch ar.StopReason {
	case "end_turn", "stop_sequence":
		out.FinishStop = "stop"
	case "max_tokens":
		out.FinishStop = "length"
	case "tool_use":
		out.FinishStop = "tool_calls"
	default:
		out.FinishStop = ar.StopReason
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Anthropic streaming
// ---------------------------------------------------------------------------

// anthropicStreamEvent is one SSE payload from Claude. The event type is
// carried in the SSE "event:" line, but the body also has a "type" field
// that is enough for our purposes.
type anthropicStreamEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`

	// content_block_start
	ContentBlock *anthropicBlock `json:"content_block,omitempty"`

	// content_block_delta
	Delta *anthropicDelta `json:"delta,omitempty"`

	// message_delta
	Delta2 *anthropicDelta `json:"-"` // placeholder to keep struct tags tidy
	Usage  *anthropicUsage `json:"usage,omitempty"`

	// message_start carries the full message with usage
	Message *anthropicResponse `json:"message,omitempty"`

	// error
	Error *anthropicError `json:"error,omitempty"`
}

type anthropicDelta struct {
	Type        string `json:"type,omitempty"`
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"`
}

type anthropicError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// anthropicStreamAccumulator reassembles a Claude stream into the internal
// response shape. Claude streams tool arguments as partial_json fragments,
// so they are concatenated until the block is complete.
type anthropicStreamAccumulator struct {
	text        strings.Builder
	toolCalls   map[int]*ToolCall
	toolOrder   []int
	toolJSONBuf map[int]*strings.Builder
	finish      string
	usage       *UsageInfo
	model       string
}

func newAnthropicStreamAccumulator() *anthropicStreamAccumulator {
	return &anthropicStreamAccumulator{
		toolCalls:   map[int]*ToolCall{},
		toolJSONBuf: map[int]*strings.Builder{},
	}
}

// feedPayload consumes one SSE payload and reports what changed, so the
// caller can stream the text out as it arrives.
func (a *anthropicStreamAccumulator) feedPayload(payload []byte) (textDelta string, argBytes int, err error) {
	var ev anthropicStreamEvent
	if uErr := json.Unmarshal(payload, &ev); uErr != nil {
		// An unparsable frame is skipped rather than aborting the stream:
		// the OpenAI path does the same.
		return "", 0, nil
	}
	before := a.text.Len()
	if fErr := a.feed(ev); fErr != nil {
		return "", 0, fErr
	}
	textDelta = a.text.String()[before:]
	if ev.Delta != nil && ev.Delta.PartialJSON != "" {
		argBytes = len(ev.Delta.PartialJSON)
	}
	return textDelta, argBytes, nil
}

// feed consumes one SSE event.
func (a *anthropicStreamAccumulator) feed(ev anthropicStreamEvent) error {
	switch ev.Type {
	case "message_start":
		if ev.Message != nil {
			a.model = ev.Message.Model
			if ev.Message.Usage != nil {
				a.usage = &UsageInfo{
					PromptTokens:     ev.Message.Usage.InputTokens,
					CompletionTokens: ev.Message.Usage.OutputTokens,
					CachedTokens:     ev.Message.Usage.CacheReadInputTokens,
				}
			}
		}
	case "content_block_start":
		if ev.ContentBlock != nil && ev.ContentBlock.Type == "tool_use" {
			tc := &ToolCall{
				ID:   ev.ContentBlock.ID,
				Type: "function",
				Function: ToolCallFunc{
					Name:      ev.ContentBlock.Name,
					Arguments: "",
				},
			}
			a.toolCalls[ev.Index] = tc
			a.toolOrder = append(a.toolOrder, ev.Index)
			buf := &strings.Builder{}
			a.toolJSONBuf[ev.Index] = buf
		}
	case "content_block_delta":
		if ev.Delta == nil {
			return nil
		}
		if ev.Delta.Text != "" {
			a.text.WriteString(ev.Delta.Text)
		}
		if ev.Delta.PartialJSON != "" {
			if buf, ok := a.toolJSONBuf[ev.Index]; ok {
				buf.WriteString(ev.Delta.PartialJSON)
			}
		}
	case "content_block_stop":
		if buf, ok := a.toolJSONBuf[ev.Index]; ok {
			if tc, ok := a.toolCalls[ev.Index]; ok {
				tc.Function.Arguments = buf.String()
			}
		}
	case "message_delta":
		if ev.Delta != nil && ev.Delta.StopReason != "" {
			a.finish = normalizeAnthropicStopReason(ev.Delta.StopReason)
		}
		if ev.Usage != nil {
			if a.usage == nil {
				a.usage = &UsageInfo{}
			}
			if ev.Usage.InputTokens > 0 {
				a.usage.PromptTokens = ev.Usage.InputTokens
			}
			if ev.Usage.OutputTokens > 0 {
				a.usage.CompletionTokens = ev.Usage.OutputTokens
			}
		}
	case "error":
		if ev.Error != nil {
			return fmt.Errorf("anthropic stream error: %s: %s", ev.Error.Type, ev.Error.Message)
		}
	}
	return nil
}

// result assembles the final response.
func (a *anthropicStreamAccumulator) result() *APIResponse {
	out := &APIResponse{Content: a.text.String(), FinishStop: a.finish, Usage: a.usage}
	for _, idx := range a.toolOrder {
		if tc, ok := a.toolCalls[idx]; ok {
			out.ToolCalls = append(out.ToolCalls, *tc)
		}
	}
	if out.FinishStop == "" {
		if len(out.ToolCalls) > 0 {
			out.FinishStop = "tool_calls"
		} else {
			out.FinishStop = "stop"
		}
	}
	return out
}

func normalizeAnthropicStopReason(s string) string {
	switch s {
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	default:
		return s
	}
}

// streamDialectParser is the interface the streaming loop uses to consume
// SSE payloads from a non-OpenAI upstream. The OpenAI Chat Completions
// shape is handled inline in the caller because it is the original path
// and has the most behaviour baked into it.
type streamDialectParser interface {
	// feedPayload consumes one SSE payload and reports the text that
	// arrived and how many bytes of tool arguments arrived.
	feedPayload(payload []byte) (textDelta string, argBytes int, err error)
	// result assembles the final response.
	result() *APIResponse
}

// newStreamDialectParser returns the parser for a dialect, or nil for
// Chat Completions (which the caller handles itself).
func newStreamDialectParser(dialect string) streamDialectParser {
	switch NormalizeDialect(dialect) {
	case DialectAnthropicMessages:
		return newAnthropicStreamAccumulator()
	case DialectResponses:
		return newResponsesStreamParser()
	default:
		return nil
	}
}

// ---------------------------------------------------------------------------
// OpenAI Responses streaming
// ---------------------------------------------------------------------------

// responsesStreamEvent is one SSE payload from the Responses API. The event
// type lives in the "type" field of the body.
type responsesStreamEvent struct {
	Type string `json:"type"`

	// response.output_text.delta
	Delta string `json:"delta,omitempty"`

	// response.output_item.added
	Item *responsesStreamItem `json:"item,omitempty"`

	// response.function_call_arguments.delta
	ItemID    string `json:"item_id,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`

	// response.created / response.completed
	Response *responsesResponse `json:"response,omitempty"`
}

type responsesStreamItem struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// responsesStreamParser reassembles a Responses stream.
//
// The Responses API streams text as output_text deltas and tool arguments
// as function_call_arguments deltas, each tied to an output item index.
type responsesStreamParser struct {
	text      strings.Builder
	tools     map[string]*ToolCall
	toolOrder []string
	usage     *UsageInfo
	finish    string
}

func newResponsesStreamParser() *responsesStreamParser {
	return &responsesStreamParser{tools: map[string]*ToolCall{}}
}

// feedPayload consumes one SSE payload and reports what changed, so the
// caller can stream the text out as it arrives.
func (p *responsesStreamParser) feedPayload(payload []byte) (textDelta string, argBytes int, err error) {
	var ev responsesStreamEvent
	if uErr := json.Unmarshal(payload, &ev); uErr != nil {
		return "", 0, nil // an unparsable frame is skipped, like the OpenAI path
	}
	switch ev.Type {
	case "response.output_text.delta":
		if ev.Delta != "" {
			p.text.WriteString(ev.Delta)
			return ev.Delta, 0, nil
		}
	case "response.output_item.added":
		if ev.Item != nil && ev.Item.Type == "function_call" {
			key := ev.Item.ID
			if key == "" {
				key = ev.Item.CallID
			}
			if _, ok := p.tools[key]; !ok {
				p.tools[key] = &ToolCall{
					ID:   ev.Item.CallID,
					Type: "function",
					Function: ToolCallFunc{
						Name:      ev.Item.Name,
						Arguments: "",
					},
				}
				p.toolOrder = append(p.toolOrder, key)
			}
		}
	case "response.function_call_arguments.delta":
		key := ev.ItemID
		if tc, ok := p.tools[key]; ok && ev.Delta != "" {
			tc.Function.Arguments += ev.Delta
			return "", len(ev.Delta), nil
		}
	case "response.function_call_arguments.done":
		if tc, ok := p.tools[ev.ItemID]; ok {
			if ev.Name != "" {
				tc.Function.Name = ev.Name
			}
			if ev.Arguments != "" {
				tc.Function.Arguments = ev.Arguments
			}
		}
	case "response.created", "response.completed", "response.incomplete":
		if ev.Response != nil && ev.Response.Usage != nil {
			p.usage = &UsageInfo{
				PromptTokens:     ev.Response.Usage.InputTokens,
				CompletionTokens: ev.Response.Usage.OutputTokens,
			}
		}
		if ev.Type == "response.completed" {
			p.finish = "stop"
		}
	}
	return "", 0, nil
}

// result assembles the final response.
func (p *responsesStreamParser) result() *APIResponse {
	out := &APIResponse{Content: p.text.String(), Usage: p.usage, FinishStop: p.finish}
	for _, key := range p.toolOrder {
		out.ToolCalls = append(out.ToolCalls, *p.tools[key])
	}
	if len(out.ToolCalls) > 0 && out.FinishStop == "" {
		out.FinishStop = "tool_calls"
	}
	if out.FinishStop == "" {
		out.FinishStop = "stop"
	}
	return out
}

// ---------------------------------------------------------------------------
// OpenAI Responses
// ---------------------------------------------------------------------------

// responsesRequest is the OpenAI Responses API request body.
type responsesRequest struct {
	Model           string           `json:"model"`
	Input           []responsesInput `json:"input"`
	Tools           []responsesTool  `json:"tools,omitempty"`
	MaxOutputTokens int              `json:"max_output_tokens,omitempty"`
	Temperature     *float64         `json:"temperature,omitempty"`
	Stream          bool             `json:"stream,omitempty"`
	Reasoning       *responsesReason `json:"reasoning,omitempty"`
}

type responsesReason struct {
	Effort string `json:"effort,omitempty"`
}

type responsesInput struct {
	// Message items use role/content. Function call items use type and the
	// function fields directly; Responses rejects those item types when they
	// are nested inside message content.
	Type    string          `json:"type,omitempty"`
	Role    string          `json:"role,omitempty"`
	Content []responsesPart `json:"content,omitempty"`

	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Output    string `json:"output,omitempty"`
}

type responsesPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// responsesResponse is the non-streaming response envelope.
type responsesResponse struct {
	ID     string `json:"id"`
	Model  string `json:"model"`
	Output []struct {
		Type      string `json:"type"`
		Role      string `json:"role"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
		Content   []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// buildResponsesRequest translates the internal messages into the
// Responses API shape.
//
// The streaming flag is off by default: the non-streaming call path asks
// for a single JSON document, and a stream would come back as SSE frames
// instead. The streaming call path turns it on.
func buildResponsesRequest(cfg *APIConfig, messages []APIMessage, toolDefs []ToolDef, stream bool) (*responsesRequest, error) {
	req := &responsesRequest{
		Model:           cfg.Model,
		MaxOutputTokens: cfg.MaxTokens,
		Stream:          stream,
	}
	if cfg.Temperature > 0 {
		t := cfg.Temperature
		req.Temperature = &t
	}
	if eff := strings.TrimSpace(cfg.ReasoningEffort); eff != "" {
		req.Reasoning = &responsesReason{Effort: eff}
	}

	for _, m := range messages {
		text, _ := splitAPIContent(m.Content)
		switch m.Role {
		case "tool":
			req.Input = append(req.Input, responsesInput{
				Type:   "function_call_output",
				CallID: m.ToolCallID,
				Output: text,
			})
		case "assistant":
			if s := strings.TrimSpace(text); s != "" {
				req.Input = append(req.Input, responsesInput{
					Role: "assistant",
					Content: []responsesPart{{
						Type: "output_text",
						Text: s,
					}},
				})
			}
			for _, tc := range m.ToolCalls {
				req.Input = append(req.Input, responsesInput{
					Type:      "function_call",
					CallID:    tc.ID,
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				})
			}
		default:
			parts := []responsesPart{}
			if s := strings.TrimSpace(text); s != "" {
				parts = append(parts, responsesPart{Type: "input_text", Text: s})
			}
			if len(parts) > 0 {
				req.Input = append(req.Input, responsesInput{Role: "user", Content: parts})
			}
		}
	}

	for _, td := range toolDefs {
		name, desc, params := toolDefParts(td)
		req.Tools = append(req.Tools, responsesTool{
			Type:        "function",
			Name:        name,
			Description: desc,
			Parameters:  params,
		})
	}
	return req, nil
}

// parseResponsesResponse turns a Responses payload into the internal shape.
func parseResponsesResponse(raw []byte) (*APIResponse, error) {
	var rr responsesResponse
	if err := json.Unmarshal(raw, &rr); err != nil {
		return nil, fmt.Errorf("parse responses payload: %w", err)
	}
	out := &APIResponse{}
	var texts []string
	for _, item := range rr.Output {
		switch item.Type {
		case "message":
			for _, c := range item.Content {
				if c.Text != "" {
					texts = append(texts, c.Text)
				}
			}
		case "function_call":
			out.ToolCalls = append(out.ToolCalls, ToolCall{
				ID:   item.CallID,
				Type: "function",
				Function: ToolCallFunc{
					Name:      item.Name,
					Arguments: item.Arguments,
				},
			})
		}
	}
	out.Content = strings.Join(texts, "")
	if len(out.ToolCalls) > 0 {
		out.FinishStop = "tool_calls"
	} else {
		out.FinishStop = "stop"
	}
	if rr.Usage != nil {
		out.Usage = &UsageInfo{
			PromptTokens:     rr.Usage.InputTokens,
			CompletionTokens: rr.Usage.OutputTokens,
		}
	}
	return out, nil
}
