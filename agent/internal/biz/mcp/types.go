package mcp

import (
	"strings"

	"agentloop/agent/internal/dtypes"
)

const DefaultProtocolVersion = "2025-06-18"

type Options struct {
	Name              string
	Transport         string
	Command           string
	Args              []string
	Env               map[string]string
	Cwd               string
	URL               string
	Headers           map[string]string
	ProtocolVersion   string
	StartupTimeoutSec int
	ToolPrefix        string
}

type ToolDescriptor struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema,omitempty"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

func (d ToolDescriptor) ToolDef(name string) dtypes.ToolDef {
	parameters := d.InputSchema
	if parameters == nil {
		parameters = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return dtypes.ToolDef{
		Type: "function",
		Function: map[string]any{
			"name":        name,
			"description": d.Description,
			"parameters":  parameters,
		},
	}
}

type ToolCallResult struct {
	Content           []map[string]any `json:"content,omitempty"`
	StructuredContent any              `json:"structuredContent,omitempty"`
	IsError           bool             `json:"isError,omitempty"`
}

type listToolsResponse struct {
	Tools      []ToolDescriptor `json:"tools"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

func NamespacedToolName(server, tool, prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = strings.TrimSpace(server)
	}
	return sanitizeToolName(prefix) + "__" + sanitizeToolName(tool)
}

func sanitizeToolName(value string) string {
	value = strings.TrimSpace(value)
	var b strings.Builder
	b.Grow(len(value))
	lastUnderscore := false
	for _, r := range value {
		valid := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-'
		if valid {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	result := strings.Trim(b.String(), "_-")
	if result == "" {
		return "tool"
	}
	return result
}
