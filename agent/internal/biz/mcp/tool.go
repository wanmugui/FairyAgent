package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agentloop/agent/internal/dtypes"
)

type Tool struct {
	client     *Client
	serverName string
	localName  string
	remoteName string
	descriptor ToolDescriptor
	schema     dtypes.ToolDef
}

func NewTool(client *Client, serverName, localName string, descriptor ToolDescriptor) *Tool {
	return &Tool{
		client:     client,
		serverName: serverName,
		localName:  localName,
		remoteName: descriptor.Name,
		descriptor: descriptor,
		schema:     descriptor.ToolDef(localName),
	}
}

func (t *Tool) Name() string {
	return t.localName
}

func (t *Tool) Schema() dtypes.ToolDef {
	return t.schema
}

func (t *Tool) Execute(ctx context.Context, invocation dtypes.ToolInvocation) (dtypes.ToolResult, error) {
	arguments := any(map[string]any{})
	rawArguments := strings.TrimSpace(string(invocation.Args))
	if rawArguments != "" && rawArguments != "null" {
		var parsed map[string]any
		if err := json.Unmarshal(invocation.Args, &parsed); err != nil {
			return dtypes.ToolResult{Value: map[string]any{
				"tool":       t.localName,
				"mcp_server": t.serverName,
				"mcp_tool":   t.remoteName,
				"error":      fmt.Sprintf("decode MCP tool arguments: %v", err),
				"is_error":   true,
			}, IsError: true}, nil
		}
		arguments = parsed
	}
	result, err := t.client.CallTool(ctx, t.remoteName, arguments)
	if err != nil {
		return dtypes.ToolResult{Value: map[string]any{
			"tool":       t.localName,
			"mcp_server": t.serverName,
			"mcp_tool":   t.remoteName,
			"error":      err.Error(),
			"is_error":   true,
		}, IsError: true}, nil
	}
	value := map[string]any{
		"tool":       t.localName,
		"mcp_server": t.serverName,
		"mcp_tool":   t.remoteName,
		"content":    result.Content,
		"is_error":   result.IsError,
	}
	if text := firstTextContent(result.Content); text != "" {
		value["result"] = text
	}
	if result.StructuredContent != nil {
		value["structured_content"] = result.StructuredContent
	}
	return dtypes.ToolResult{Value: value, IsError: result.IsError}, nil
}

func firstTextContent(content []map[string]any) string {
	for _, item := range content {
		text, _ := item["text"].(string)
		if strings.TrimSpace(text) != "" {
			return text
		}
	}
	return ""
}

func (t *Tool) Close() error {
	if t == nil || t.client == nil {
		return nil
	}
	return t.client.Close()
}
