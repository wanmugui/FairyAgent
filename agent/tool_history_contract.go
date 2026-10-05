package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

const invalidToolCallRecoveryPrompt = "上一轮工具调用参数不是合法的 JSON，因此没有执行任何工具。请重新发出完整工具调用；每个 function.arguments 都必须是完整的 JSON object。"

func normalizeToolArguments(call ToolCall) (string, error) {
	raw := strings.TrimSpace(call.Function.Arguments)
	if raw == "" {
		raw = "{}"
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(raw), &object); err != nil {
		return "", fmt.Errorf("tool call %q (%s) arguments are invalid JSON: %w", call.ID, call.Function.Name, err)
	}
	if object == nil {
		return "", fmt.Errorf("tool call %q (%s) arguments must be a JSON object", call.ID, call.Function.Name)
	}
	return raw, nil
}

// normalizeToolCalls validates only the transport-level tool-call contract.
// Required business fields remain the responsibility of each tool, whose
// ordinary validation error must be returned to the model as a tool result.
func normalizeToolCalls(calls []ToolCall) ([]ToolCall, error) {
	if len(calls) == 0 {
		return nil, nil
	}
	normalized := append([]ToolCall(nil), calls...)
	seenIDs := make(map[string]struct{}, len(normalized))
	for index := range normalized {
		call := &normalized[index]
		call.ID = strings.TrimSpace(call.ID)
		call.Function.Name = strings.TrimSpace(call.Function.Name)
		if call.ID == "" {
			return nil, fmt.Errorf("tool call at index %d has an empty id", index)
		}
		if _, exists := seenIDs[call.ID]; exists {
			return nil, fmt.Errorf("tool call id %q is duplicated", call.ID)
		}
		seenIDs[call.ID] = struct{}{}
		if call.Function.Name == "" {
			return nil, fmt.Errorf("tool call %q has an empty function name", call.ID)
		}
		if call.Type == "" {
			call.Type = "function"
		} else if call.Type != "function" {
			return nil, fmt.Errorf("tool call %q has unsupported type %q", call.ID, call.Type)
		}

		raw, err := normalizeToolArguments(*call)
		if err != nil {
			return nil, err
		}
		call.Function.Arguments = raw
	}
	return normalized, nil
}

// validateToolMessageHistory is the final outbound guard. A model request may
// contain only complete assistant tool-call rounds followed by exactly one
// tool result for every call id. Pending ask_user calls are persisted while the
// process waits, but RunAgentLoop must never invoke the model in that state.
func validateToolMessageHistory(messages []Message) error {
	pending := map[string]struct{}{}
	for index, message := range messages {
		if message.Role == "assistant" && len(message.ToolCalls) > 0 {
			if len(pending) > 0 {
				return fmt.Errorf("message %d starts a new assistant tool round before the previous round completed", index)
			}
			normalized, err := normalizeToolCalls(message.ToolCalls)
			if err != nil {
				return fmt.Errorf("message %d: %w", index, err)
			}
			for _, call := range normalized {
				pending[call.ID] = struct{}{}
			}
			continue
		}
		if message.Role == "tool" {
			if message.ToolCallID == "" {
				return fmt.Errorf("message %d is a tool result without tool_call_id", index)
			}
			if _, exists := pending[message.ToolCallID]; !exists {
				return fmt.Errorf("message %d is an orphaned tool result for %q", index, message.ToolCallID)
			}
			delete(pending, message.ToolCallID)
			continue
		}
		if len(pending) > 0 {
			return fmt.Errorf("message %d appears before all tool results from the previous assistant round", index)
		}
	}
	if len(pending) > 0 {
		return fmt.Errorf("history ends with %d unresolved tool call(s)", len(pending))
	}
	return nil
}
