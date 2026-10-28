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
		waitFor := askUserAutoSelectSeconds(args, askType)

		// Claim the entry the agent loop reserved for us, if there is one.
		//
		// agentloop publishes `waiting_user_input` BEFORE dispatching so the
		// modal appears while we are still waiting rather than 30s late. That
		// means the surface can answer before we get here, so agentloop
		// pre-appends an entry for this exact question (agentloop.go,
		// preEmitAskUserEvents). Appending our own entry anyway would leave two
		// unanswered entries: the reply lands on the reserved one, we watch the
		// freshly appended one, and the human's answer is silently discarded in
		// favour of the timeout fallback.
		//
		// So: adopt the reserved entry when it matches what we are about to ask,
		// otherwise append as before.
		entryIndex := claimReservedAskEntry(entries, askType, questions)

		if entryIndex < 0 {
			entries = append(entries, map[string]any{
				"timestamp":                   time.Now().Unix(),
				"ask_type":                    askType,
				"questions":                   questions,
				"answered":                    false,
				"call_id":                     invocation.CallID,
				askUserAutoSelectSecondsParam: int(waitFor / time.Second),
				askUserDeadlineAtParam:        time.Now().Add(waitFor).UnixMilli(),
			})
			if err := writeJSONFileAtomically(resultPath, entries); err != nil {
				return localErrorResult("ask_user", err), nil
			}
			entryIndex = len(entries) - 1
		} else {
			entries[entryIndex]["call_id"] = invocation.CallID
			entries[entryIndex][askUserAutoSelectSecondsParam] = int(waitFor / time.Second)
			entries[entryIndex][askUserDeadlineAtParam] = time.Now().Add(waitFor).UnixMilli()
			if err := writeJSONFileAtomically(resultPath, entries); err != nil {
				return localErrorResult("ask_user", err), nil
			}
		}

		questionCount := 0
		if list, ok := questions.([]any); ok {
			questionCount = len(list)
		}
		confirmType := "confirmation"
		if askType == "ppt_mode.confirm_params" || askType == "ppt_mode.confirm_outline" {
			confirmType = "ppt_confirmation"
		}

		// The backend owns the clock: wait here for the reply so a channel
		// without a countdown (WeChat, voice, a closed tab) still unblocks,
		// and so there is exactly one deadline rather than one per surface.
		answers, answered, waitErr := waitForAskReply(ctx, resultPath, entryIndex, waitFor)
		if waitErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ToolResult{}, ctxErr
			}
			return localErrorResult("ask_user", waitErr), nil
		}

		autoSelected := !answered
		if autoSelected {
			answers = autoSelectAskAnswers(questions)
			_ = markAskEntryAnswered(resultPath, entryIndex, answers, true)
		} else {
			answers = normalizeAnswerPayload(answers)
		}

		status := "answered"
		if autoSelected {
			status = "auto_selected"
		} else if questionCount > 0 {
			status = "waiting_questions"
		} else {
			status = "waiting_confirmation"
		}
		return ToolResult{
			WaitingReply: false,
			Value: map[string]any{
				"tool":          "ask_user",
				"ok":            true,
				"ask_type":      askType,
				"questions":     questions,
				"stored":        resultPath,
				"waiting_reply": false,
				"answer":        answers,
				// Surfaced so the frontend can render a countdown from one
				// source instead of keeping its own timer.
				"auto_select_after_seconds": int(waitFor / time.Second),
				askUserAutoSelectedParam:    autoSelected,
				"result": map[string]any{
					"ask_type":               askType,
					"type":                   confirmType,
					"status":                 status,
					"answered":               true,
					askUserAutoSelectedParam: autoSelected,
				},
			},
		}, nil
	})
}

// askUserAutoSelectSeconds resolves the deadline for this ask.
//
// An explicit __wait_timeout_seconds wins so tests can run instantly.
// PPT confirmations are a different flow (the deck must be regenerated if the
// user walks away) and keep the default; nothing else is special-cased.
func askUserAutoSelectSeconds(args map[string]any, _ string) time.Duration {
	if raw, ok := args[askUserWaitTimeoutParam]; ok {
		switch value := raw.(type) {
		case float64:
			if value > 0 {
				return time.Duration(value) * time.Second
			}
		case string:
			if seconds, err := time.ParseDuration(value + "s"); err == nil && seconds > 0 {
				return seconds
			}
		}
	}
	return AskUserAutoSelectSeconds * time.Second
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

// askUserReservedFlag marks the entry agentloop pre-appends before dispatching
// ask_user. See NewLocalAskUserTool for why the tool must adopt it instead of
// appending a second one.
const askUserReservedFlag = "reserved"

// claimReservedAskEntry returns the index of an unanswered, reserved entry that
// matches this ask, or -1 when there is none (the caller then appends).
//
// Matching is by ask type plus question ids rather than exact question payload:
// the loop and the tool receive the same arguments, but the loop decodes them
// from the raw call while the tool receives them through the normal decode
// path, and a stricter comparison would miss a legitimate reservation. The
// newest match wins so a resumed or repeated ask claims its own entry.
func claimReservedAskEntry(entries []map[string]any, askType string, questions any) int {
	ids := askQuestionIDs(questions)
	found := -1
	for index, entry := range entries {
		if entry == nil || entry[askUserReservedFlag] != true {
			continue
		}
		if answered, _ := entry["answered"].(bool); answered {
			continue
		}
		if entryType, _ := entry["ask_type"].(string); entryType != askType {
			continue
		}
		if !askEntryIDsMatch(entry, ids) {
			continue
		}
		found = index
	}
	return found
}

// askQuestionIDs extracts the question ids from the normalised payload so two
// representations of the same ask can be compared cheaply.
func askQuestionIDs(questions any) []string {
	list, ok := questions.([]any)
	if !ok {
		return nil
	}
	ids := make([]string, 0, len(list))
	for _, raw := range list {
		question, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id, _ := question["id"].(string)
		ids = append(ids, id)
	}
	return ids
}

// askEntryIDsMatch compares the ids stored on a reserved entry with the ones we
// are asking. A reservation with no ids at all (a bare confirmation, which has
// no questions) matches any confirmation of the same ask type.
func askEntryIDsMatch(entry map[string]any, ids []string) bool {
	stored, ok := entry["questions"].([]any)
	if !ok {
		return len(ids) == 0
	}
	if len(stored) != len(ids) {
		return false
	}
	for index, raw := range stored {
		question, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		id, _ := question["id"].(string)
		if id != ids[index] {
			return false
		}
	}
	return true
}
