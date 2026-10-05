package main

import (
	toolruntime "agentloop/agent/internal/biz/tool"
	"agentloop/agent/internal/dtypes"
)

type ToolRegistry = toolruntime.Registry
type ToolDispatcher = toolruntime.Dispatcher
type ToolInvocationResult = dtypes.ToolInvocationResult

func NewToolRegistry() *ToolRegistry {
	return toolruntime.NewRegistry()
}
