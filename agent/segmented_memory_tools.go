package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

func NewSegmentedMemoryManagementTool(name string, schema ToolDef, cfg *Config) Tool {
	return NewLocalToolFunc(name, schema, func(ctx context.Context, invocation ToolInvocation) (map[string]any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if cfg == nil {
			return nil, fmt.Errorf("config is nil")
		}
		root := strings.TrimSpace(configuredMemoryRoot(cfg))
		if root == "" {
			return nil, fmt.Errorf("memory directory is not configured")
		}
		root = filepath.Join(root, segmentedMemoryDir)
		var args map[string]any
		if len(invocation.Args) > 0 {
			if err := json.Unmarshal(invocation.Args, &args); err != nil {
				return nil, fmt.Errorf("parse arguments: %w", err)
			}
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "memory_invalidate_segment":
			segmentID := memoryToolStringArg(args, "segment_id")
			targetInteractionID := memoryToolStringArg(args, "interaction_id")
			if targetInteractionID == "" {
				return nil, fmt.Errorf("interaction_id is required")
			}
			return invalidateSegmentedMemorySegment(
				root,
				targetInteractionID,
				segmentID,
				memoryToolStringArg(args, "reason"),
				memoryToolStringArg(args, "replacement_id"),
				"model",
			)
		case "memory_invalidate_interaction":
			return invalidateSegmentedMemoryInteraction(
				root,
				memoryToolStringArg(args, "interaction_id"),
				memoryToolStringArg(args, "reason"),
				memoryToolStringArg(args, "replacement_id"),
				"model",
			)
		case "memory_delete_segment":
			targetInteractionID := memoryToolStringArg(args, "interaction_id")
			if targetInteractionID == "" {
				return nil, fmt.Errorf("interaction_id is required")
			}
			return deleteSegmentedMemorySegment(
				root,
				targetInteractionID,
				memoryToolStringArg(args, "segment_id"),
				memoryToolStringArg(args, "reason"),
			)
		case "memory_delete_interaction":
			return deleteSegmentedMemoryInteraction(
				root,
				memoryToolStringArg(args, "interaction_id"),
				memoryToolStringArg(args, "reason"),
			)
		default:
			return nil, fmt.Errorf("unknown segmented memory tool %q", name)
		}
	})
}

func memoryToolStringArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	value, ok := args[key]
	if !ok || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}
