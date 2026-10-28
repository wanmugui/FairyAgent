package mcp

import (
	"fmt"
	"strings"
)

func newTransport(options Options) (transport, error) {
	kind := strings.ToLower(strings.TrimSpace(options.Transport))
	if kind == "" {
		if strings.TrimSpace(options.Command) != "" {
			kind = "stdio"
		} else {
			kind = "http"
		}
	}
	switch kind {
	case "stdio":
		return newStdioTransport(options)
	case "http", "streamable-http", "streamable_http":
		return newHTTPTransport(options)
	default:
		return nil, fmt.Errorf("MCP server %q uses unsupported transport %q", options.Name, options.Transport)
	}
}
