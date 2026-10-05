package dtypes

import (
	"context"
	"encoding/json"
	"time"
)

// ToolDef is the OpenAI-compatible function declaration supplied to the LLM.
type ToolDef struct {
	Type     string      `json:"type"`
	Function interface{} `json:"function"`
}

// ToolInvocation is the platform-neutral input supplied to a tool execution.
type ToolInvocation struct {
	Index       int
	CallID      string
	Name        string
	Args        json.RawMessage
	Timeout     time.Duration
	Workspace   string
	SessionFile string
	Metadata    map[string]string
}

const (
	// ToolMetadataWebBudgetTokens carries the current per-call text budget for
	// web_search/web_fetch. It is computed by the agent loop from the remaining
	// model context, then enforced by the individual tool implementation.
	ToolMetadataWebBudgetTokens = "web_budget_tokens"
)

// ToolResult is the normalized result returned by every tool backend.
type ToolResult struct {
	Value        map[string]any
	IsError      bool
	WaitingReply bool
	UpstreamCode int
	Attachments  []ToolAttachment
}

// ToolAttachment is a non-text artifact produced by a tool. Attachments are
// deliberately separate from Value: Value is persisted and sent through the
// normal textual tool-result channel, while attachments can be projected into
// the model-visible context without polluting the durable transcript.
type ToolAttachment struct {
	Path      string
	MIME      string
	SHA256    string
	SizeBytes int64
	Width     int
	Height    int
	Label     string
}

type ToolBackend string

const (
	BackendLocal ToolBackend = "local"
	BackendHTTP  ToolBackend = "http"
	BackendMCP   ToolBackend = "mcp"
)

type ToolInvocationResult struct {
	Index  int
	CallID string
	Name   string
	Result ToolResult
	Err    error
}

func (r ToolResult) JSON() ([]byte, error) {
	if r.Value == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(r.Value)
}

// Tool is the common execution contract for builtin, local and HTTP tools.
type Tool interface {
	Name() string
	Schema() ToolDef
	Execute(context.Context, ToolInvocation) (ToolResult, error)
}
