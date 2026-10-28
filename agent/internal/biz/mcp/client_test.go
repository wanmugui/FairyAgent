package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	mcpHelperEnv     = "FAIRY_MCP_TEST_HELPER"
	mcpHelperModeEnv = "FAIRY_MCP_TEST_HELPER_MODE"
)

func mcpStdioHelperOptions(t *testing.T, mode string) Options {
	t.Helper()
	return Options{
		Name:      "test-server",
		Transport: "stdio",
		Command:   os.Args[0],
		Args:      []string{"-test.run=^TestMCPStdioHelperProcess$"},
		Env: map[string]string{
			mcpHelperEnv:     "1",
			mcpHelperModeEnv: mode,
		},
	}
}

func TestMCPStdioClientPaginationAndCall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := Connect(ctx, mcpStdioHelperOptions(t, "normal"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 || tools[0].Name != "echo" || tools[1].Name != "second" {
		t.Fatalf("unexpected tools: %#v", tools)
	}
	result, err := client.CallTool(ctx, "echo", map[string]any{"value": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if got := firstTextContent(result.Content); got != `echo:{"value":"hello"}` {
		t.Fatalf("unexpected tool text: %q", got)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("second close must be idempotent: %v", err)
	}
}

func TestMCPStdioDisconnectReturnsError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := Connect(ctx, mcpStdioHelperOptions(t, "exit-on-list"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	_, err = client.ListTools(ctx)
	if err == nil || !strings.Contains(err.Error(), "disconnected") {
		t.Fatalf("expected disconnect error, got %v", err)
	}
}

func TestMCPStdioHelperProcess(t *testing.T) {
	if os.Getenv(mcpHelperEnv) != "1" {
		return
	}
	mode := os.Getenv(mcpHelperModeEnv)
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request rpcRequest
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			continue
		}
		if request.ID == nil {
			continue
		}
		var (
			result json.RawMessage
			rpcErr *rpcError
		)
		switch request.Method {
		case "initialize":
			result = json.RawMessage(`{"protocolVersion":"2025-06-18"}`)
		case "tools/list":
			if mode == "exit-on-list" {
				return
			}
			params, _ := request.Params.(map[string]any)
			if cursor, _ := params["cursor"].(string); cursor == "page-2" {
				result = json.RawMessage(`{"tools":[{"name":"second","inputSchema":{"type":"object"}}]}`)
			} else {
				result = json.RawMessage(`{"tools":[{"name":"echo","description":"echo arguments","inputSchema":{"type":"object"}}],"nextCursor":"page-2"}`)
			}
		case "tools/call":
			params, _ := request.Params.(map[string]any)
			arguments, _ := json.Marshal(params["arguments"])
			content, _ := json.Marshal([]map[string]any{{
				"type": "text",
				"text": "echo:" + string(arguments),
			}})
			result = json.RawMessage(`{"content":` + string(content) + `}`)
		default:
			rpcErr = &rpcError{Code: -32601, Message: "method not found"}
		}
		responseID, _ := json.Marshal(request.ID)
		response := rpcResponse{JSONRPC: "2.0", ID: responseID, Result: result, Error: rpcErr}
		if err := encoder.Encode(response); err != nil {
			return
		}
	}
}
