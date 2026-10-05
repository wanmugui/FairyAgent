package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func NewLocalAskUserTool(schema ToolDef) Tool {
	return newLocalStructuredTool("ask_user", schema, func(ctx context.Context, invocation ToolInvocation) (ToolResult, error) {
		if err := ctx.Err(); err != nil {
			return ToolResult{}, err
		}
		args, err := decodeLocalToolArgs(invocation)
		if err != nil {
			return localErrorResult("ask_user", err), nil
		}
		askType := localStringArg(args, "ask_type")
		if askType == "" {
			return localErrorResult("ask_user", fmt.Errorf("ask_type is required")), nil
		}
		localContext, err := localToolContext(invocation)
		if err != nil {
			return localErrorResult("ask_user", err), nil
		}
		questions, err := normalizeLocalAskQuestions(args["questions"])
		if err != nil {
			return localErrorResult("ask_user", err), nil
		}
		askDir := filepath.Join(localContext.Workspace, "ask_user")
		resultPath := filepath.Join(askDir, "ask_results.json")
		entries := make([]map[string]any, 0)
		if raw, readErr := os.ReadFile(resultPath); readErr == nil {
			_ = json.Unmarshal(raw, &entries)
			if entries == nil {
				entries = make([]map[string]any, 0)
			}
		}
		entries = append(entries, map[string]any{
			"timestamp": time.Now().Unix(),
			"ask_type":  askType,
			"questions": questions,
			"answered":  false,
		})
		if err := writeJSONFileAtomically(resultPath, entries); err != nil {
			return localErrorResult("ask_user", err), nil
		}

		questionCount := 0
		if list, ok := questions.([]any); ok {
			questionCount = len(list)
		}
		confirmType := "confirmation"
		if askType == "ppt_mode.confirm_params" || askType == "ppt_mode.confirm_outline" {
			confirmType = "ppt_confirmation"
		}
		status := "waiting_confirmation"
		if questionCount > 0 {
			status = "waiting_questions"
		}
		return ToolResult{
			WaitingReply: true,
			Value: map[string]any{
				"tool":          "ask_user",
				"ok":            true,
				"ask_type":      askType,
				"questions":     questions,
				"stored":        resultPath,
				"waiting_reply": true,
				"result":        map[string]any{"ask_type": askType, "type": confirmType, "status": status},
			},
		}, nil
	})
}

func normalizeLocalAskQuestions(value any) (any, error) {
	if value == nil {
		return []any{}, nil
	}
	if list, ok := value.([]any); ok {
		return sanitizeLocalAskQuestions(list), nil
	}
	text, ok := value.(string)
	if !ok {
		return value, nil
	}
	var questions []any
	if json.Unmarshal([]byte(text), &questions) == nil {
		return sanitizeLocalAskQuestions(questions), nil
	}
	repaired := repairLocalQuotedJSON(text)
	if repaired != text && json.Unmarshal([]byte(repaired), &questions) == nil {
		return sanitizeLocalAskQuestions(questions), nil
	}
	return []any{}, nil
}

func sanitizeLocalAskQuestions(questions []any) []any {
	for _, rawQuestion := range questions {
		question, ok := rawQuestion.(map[string]any)
		if !ok {
			continue
		}
		options, ok := question["options"].([]any)
		if !ok {
			continue
		}
		for index, rawOption := range options {
			switch option := rawOption.(type) {
			case map[string]any:
				if label, ok := option["label"].(string); ok {
					option["label"] = sanitizeAskOptionLabel(label)
				}
			case string:
				// Kept for compatibility with older callers that send strings.
				options[index] = sanitizeAskOptionLabel(option)
			}
		}
	}
	return questions
}

func sanitizeAskOptionLabel(label string) string {
	cleaned := strings.Join(strings.Fields(label), " ")
	runes := []rune(cleaned)
	if len(runes) > 20 {
		return string(runes[:20])
	}
	return cleaned
}

func repairLocalQuotedJSON(text string) string {
	var builder strings.Builder
	inString := false
	escaped := false
	runes := []rune(text)
	for index, char := range runes {
		if inString {
			if escaped {
				builder.WriteRune(char)
				escaped = false
				continue
			}
			if char == '\\' {
				builder.WriteRune(char)
				escaped = true
				continue
			}
			if char == '"' {
				next := rune(0)
				if index+1 < len(runes) {
					next = runes[index+1]
				}
				if next == ',' || next == ']' || next == '}' || next == ':' || next == ' ' || next == '\t' || next == '\n' || next == '\r' {
					builder.WriteRune(char)
					inString = false
				} else {
					builder.WriteString(`\"`)
				}
				continue
			}
			builder.WriteRune(char)
			continue
		}
		if char == '"' {
			inString = true
		}
		builder.WriteRune(char)
	}
	return builder.String()
}
