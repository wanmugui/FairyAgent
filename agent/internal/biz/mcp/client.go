package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

type transport interface {
	Call(context.Context, string, any) (json.RawMessage, error)
	Notify(context.Context, string, any) error
	Close() error
}

type Client struct {
	name            string
	transport       transport
	protocolVersion string
	closeOnce       sync.Once
	closeErr        error
}

func Connect(ctx context.Context, options Options) (*Client, error) {
	tr, err := newTransport(options)
	if err != nil {
		return nil, err
	}
	client := &Client{name: options.Name, transport: tr}
	if err := client.initialize(ctx, options.ProtocolVersion); err != nil {
		_ = tr.Close()
		return nil, err
	}
	return client, nil
}

func (c *Client) initialize(ctx context.Context, protocolVersion string) error {
	if strings.TrimSpace(protocolVersion) == "" {
		protocolVersion = DefaultProtocolVersion
	}
	raw, err := c.transport.Call(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "fairy",
			"version": "1.0.0",
		},
	})
	if err != nil {
		return fmt.Errorf("initialize MCP server %q: %w", c.name, err)
	}
	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("decode MCP initialize response from %q: %w", c.name, err)
	}
	c.protocolVersion = strings.TrimSpace(result.ProtocolVersion)
	if c.protocolVersion == "" {
		c.protocolVersion = protocolVersion
	}
	if setter, ok := c.transport.(interface{ setProtocolVersion(string) }); ok {
		setter.setProtocolVersion(c.protocolVersion)
	}
	if err := c.transport.Notify(ctx, "notifications/initialized", map[string]any{}); err != nil {
		return fmt.Errorf("send initialized notification to MCP server %q: %w", c.name, err)
	}
	return nil
}

func (c *Client) ListTools(ctx context.Context) ([]ToolDescriptor, error) {
	var tools []ToolDescriptor
	cursor := ""
	seen := map[string]bool{}
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.transport.Call(ctx, "tools/list", params)
		if err != nil {
			return nil, fmt.Errorf("list tools from MCP server %q: %w", c.name, err)
		}
		var result listToolsResponse
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, fmt.Errorf("decode tools/list response from %q: %w", c.name, err)
		}
		tools = append(tools, result.Tools...)
		next := strings.TrimSpace(result.NextCursor)
		if next == "" {
			break
		}
		if seen[next] {
			return nil, fmt.Errorf("MCP server %q returned a repeated tools/list cursor", c.name)
		}
		seen[next] = true
		cursor = next
	}
	return tools, nil
}

func (c *Client) CallTool(ctx context.Context, name string, arguments any) (ToolCallResult, error) {
	raw, err := c.transport.Call(ctx, "tools/call", map[string]any{
		"name":      name,
		"arguments": arguments,
	})
	if err != nil {
		return ToolCallResult{}, fmt.Errorf("call MCP tool %q on %q: %w", name, c.name, err)
	}
	var result ToolCallResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return ToolCallResult{}, fmt.Errorf("decode MCP tool %q result: %w", name, err)
	}
	return result, nil
}

func (c *Client) Close() error {
	if c == nil || c.transport == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		c.closeErr = c.transport.Close()
	})
	return c.closeErr
}
