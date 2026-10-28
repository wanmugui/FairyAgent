package main

import (
	"agentloop/agent/internal/dtypes"
	"context"
)

type ToolBackend = dtypes.ToolBackend

const (
	BackendLocal = dtypes.BackendLocal
	BackendHTTP  = dtypes.BackendHTTP
	BackendMCP   = dtypes.BackendMCP
)

type ToolInvocation = dtypes.ToolInvocation
type ToolResult = dtypes.ToolResult
type Tool = dtypes.Tool

// LocalToolFunc 将进程内 Go 函数适配为 Tool。
type LocalToolFunc struct {
	name   string
	schema ToolDef
	run    func(context.Context, ToolInvocation) (map[string]any, error)
}

func NewLocalToolFunc(name string, schema ToolDef, run func(context.Context, ToolInvocation) (map[string]any, error)) *LocalToolFunc {
	return &LocalToolFunc{name: name, schema: schema, run: run}
}

func (t *LocalToolFunc) Name() string {
	return t.name
}

func (t *LocalToolFunc) Schema() ToolDef {
	return t.schema
}

func (t *LocalToolFunc) Execute(ctx context.Context, invocation ToolInvocation) (ToolResult, error) {
	value, err := t.run(ctx, invocation)
	if err != nil {
		return ToolResult{}, err
	}
	result := ToolResult{Value: value}
	if waiting, ok := value["waiting_reply"].(bool); ok {
		result.WaitingReply = waiting
	}
	if isError, ok := value["is_error"].(bool); ok {
		result.IsError = isError
	}
	return result, nil
}
