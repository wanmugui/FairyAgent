package main

import (
	"context"
	"encoding/json"
	"time"
)

func processToolCalls(ctx context.Context, registry *ToolRegistry, calls []ToolCall, workspace, sessionFile string, timeout time.Duration) []ToolInvocationResult {
	invocations := make([]ToolInvocation, 0, len(calls))
	for index, call := range calls {
		invocations = append(invocations, ToolInvocation{
			Index:       index,
			CallID:      call.ID,
			Name:        call.Function.Name,
			Args:        json.RawMessage(call.Function.Arguments),
			Timeout:     timeout,
			Workspace:   workspace,
			SessionFile: sessionFile,
		})
	}
	return (&ToolDispatcher{Registry: registry, MaxConcurrency: 4}).Execute(ctx, invocations)
}
