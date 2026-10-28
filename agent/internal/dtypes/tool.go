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

// ToolName returns the function name declared by this tool definition.
func (t ToolDef) ToolName() string {
	return toolDefString(t, "name")
}

// ToolDescription returns the function description declared by this tool
// definition, or an empty string when it is absent.
func (t ToolDef) ToolDescription() string {
	return toolDefString(t, "description")
}

// toolDefString reads one string field from the function declaration. The
// declaration is stored as an untyped value, so every read has to tolerate a
// missing or wrongly typed value instead of panicking.
func toolDefString(t ToolDef, key string) string {
	function, ok := t.Function.(map[string]any)
	if !ok {
		return ""
	}
	value, ok := function[key]
	if !ok {
		return ""
	}
	text, _ := value.(string)
	return text
}

// Tool is the common execution contract for builtin, local and HTTP tools.
type Tool interface {
	Name() string
	Schema() ToolDef
	Execute(context.Context, ToolInvocation) (ToolResult, error)
}

// DeferredToolInfo describes a tool that is registered and callable but whose
// schema is not advertised to the model by default. tool_search surfaces these
// on demand so a large MCP catalogue does not occupy every request.
type DeferredToolInfo struct {
	// Name is the model-visible tool name, including any server namespace.
	Name string `json:"name"`
	// Description is the short human-readable summary used for search.
	Description string `json:"description"`
	// Group identifies the owning provider, e.g. an MCP server name. Tools in
	// the same group are siblings and are reported together by tool_search.
	Group string `json:"group"`
}

// DeferredIndex is the search surface a tool_search implementation queries.
// The tool package owns the registry; this interface keeps the search tool
// independent of how deferred tools are stored.
type DeferredIndex interface {
	// ListDeferred returns every deferred tool with its searchable summary.
	ListDeferred() []DeferredToolInfo
	// Expose promotes a deferred tool so its schema enters later requests.
	// It reports whether the tool exists and was deferred.
	Expose(name string) bool
}
