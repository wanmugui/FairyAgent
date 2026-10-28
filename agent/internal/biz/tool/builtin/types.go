package builtin

import "agentloop/agent/internal/dtypes"

type Tool = dtypes.Tool
type ToolDef = dtypes.ToolDef
type ToolInvocation = dtypes.ToolInvocation
type ToolResult = dtypes.ToolResult

type ReadFileToolConfig struct {
	SegmentReadMaxTokens int
	SegmentReadMinTokens int
	MaxReadFileSizeBytes int64
	SkillsRoot           string
	MemoryRoot           string
}

type WritableFileToolConfig struct {
	MemoryRoot string
}
