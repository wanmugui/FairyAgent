package tool

import (
	"agentloop/agent/internal/dtypes"
	"context"
)

type testTool struct {
	name   string
	schema dtypes.ToolDef
	run    func(context.Context, dtypes.ToolInvocation) (map[string]any, error)
}

func (t *testTool) Name() string { return t.name }

func (t *testTool) Schema() dtypes.ToolDef { return t.schema }

func (t *testTool) Execute(ctx context.Context, invocation dtypes.ToolInvocation) (dtypes.ToolResult, error) {
	value, err := t.run(ctx, invocation)
	if err != nil {
		return dtypes.ToolResult{}, err
	}
	return dtypes.ToolResult{Value: value}, nil
}

func newTestTool(name string, run func(context.Context, dtypes.ToolInvocation) (map[string]any, error)) dtypes.Tool {
	return &testTool{name: name, schema: dtypes.ToolDef{Type: "function", Function: map[string]any{"name": name}}, run: run}
}
