package builtin

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"
)

// Ask-user reply waiting.
//
// The backend owns the clock. Previously the only countdown lived in the
// frontend hook (ASK_USER_AUTO_CONFIRM_SECONDS in
// frontend/src/components/useAskAutoConfirm.js), which meant:
//
//   - closing the tab, using WeChat/voice, or crashing the client killed the
//     timer and the turn hung forever;
//   - adding a backend timer as well would have produced two competing clocks
//     that could disagree about who answered first.
//
// So there is exactly one deadline, computed here, and the frontend only
// *renders* the remaining seconds it is told about. When the deadline passes
// the backend resolves the same way the UI would have: pick the recommended
// option, or the first option, and mark the entry auto-selected so logs can
// distinguish a real answer from a timeout fallback.

// AskUserAutoSelectSeconds is the single source of truth for how long the
// backend waits for a human before resolving the ask itself.
//
// 30s matches what the UI has always shown, so moving the clock into the
// backend is not a visible behaviour change: the modal says "30s" either way,
// it just stops being the thing that decides. A longer window was considered
// so that a channel with no UI (WeChat, voice) would have time to notice the
// message, but that quietly turned a deliberate UI affordance into a three
// minute hang, which is not the same product.
const AskUserAutoSelectSeconds = 30

// askUserPollInterval is how often we re-read the results file while waiting.
// Short enough that answering feels immediate, long enough not to spin.
const askUserPollInterval = 300 * time.Millisecond

// askUserWaitTimeoutParam lets tests shrink the deadline. It is read from the
// tool args and stripped before the entry is persisted, so it never leaks into
// the user-visible questions payload.
const askUserWaitTimeoutParam = "__wait_timeout_seconds"

// askUserAutoSelectedParam is set on the persisted entry (and surfaced in the
// result) when the deadline expired rather than a human answering.
const askUserAutoSelectedParam = "auto_selected"

// askUserDeadlineAtParam is the persisted absolute deadline. The web renew
// route extends this field when the user interacts with the modal, so the
// backend wait and the visible countdown use the same clock.
const askUserDeadlineAtParam = "deadline_at_ms"

// askUserAutoSelectSecondsParam is persisted alongside the deadline so a
// renewal knows how far to extend without trusting a browser-supplied value.
const askUserAutoSelectSecondsParam = "auto_select_after_seconds"

// waitForAskReply polls the results file until the matching entry is answered
// or the deadline expires.
//
// It returns the answer payload and true when a human answered, or
// autoSelected=true when the deadline won. ctx cancellation aborts the wait
// and is reported as an error so the caller can propagate it.
func waitForAskReply(ctx context.Context, resultPath string, entryIndex int, timeout time.Duration) (answers any, answered bool, err error) {
	if timeout <= 0 {
		timeout = AskUserAutoSelectSeconds * time.Second
	}
	deadlineAt := time.Now().Add(timeout)
	if persisted, ok := readAskDeadline(resultPath, entryIndex); ok && persisted.After(deadlineAt) {
		deadlineAt = persisted
	}
	deadline := time.NewTimer(time.Until(deadlineAt))
	defer deadline.Stop()
	ticker := time.NewTicker(askUserPollInterval)
	defer ticker.Stop()

	for {
		// Check once before waiting so an answer that landed while we were
		// still writing the entry is not missed.
		if payload, ok := readAskAnswer(resultPath, entryIndex); ok {
			return payload, true, nil
		}
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-deadline.C:
			// The renew request writes the new deadline to the same file. Check
			// once more when the old timer fires so an extension racing the
			// expiry is not lost.
			if persisted, ok := readAskDeadline(resultPath, entryIndex); ok && persisted.After(deadlineAt) {
				deadlineAt = persisted
				deadline.Reset(time.Until(deadlineAt))
				continue
			}
			return nil, false, nil
		case <-ticker.C:
			if persisted, ok := readAskDeadline(resultPath, entryIndex); ok && persisted.After(deadlineAt) {
				deadlineAt = persisted
				if !deadline.Stop() {
					select {
					case <-deadline.C:
					default:
					}
				}
				deadline.Reset(time.Until(deadlineAt))
			}
		}
	}
}

// readAskDeadline reads the absolute deadline persisted for an ask entry.
func readAskDeadline(resultPath string, index int) (time.Time, bool) {
	raw, err := os.ReadFile(resultPath)
	if err != nil {
		return time.Time{}, false
	}
	var entries []map[string]any
	if err := json.Unmarshal(raw, &entries); err != nil {
		return time.Time{}, false
	}
	if index < 0 || index >= len(entries) {
		return time.Time{}, false
	}
	value, ok := entries[index][askUserDeadlineAtParam]
	if !ok {
		return time.Time{}, false
	}
	var millis int64
	switch typed := value.(type) {
	case float64:
		millis = int64(typed)
	case int64:
		millis = typed
	case int:
		millis = int64(typed)
	default:
		return time.Time{}, false
	}
	if millis <= 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(millis), true
}

// readAskAnswer returns the stored answer for entry at index, if any.
func readAskAnswer(resultPath string, index int) (any, bool) {
	raw, err := os.ReadFile(resultPath)
	if err != nil {
		return nil, false
	}
	var entries []map[string]any
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, false
	}
	if index < 0 || index >= len(entries) {
		return nil, false
	}
	entry := entries[index]
	if answered, _ := entry["answered"].(bool); !answered {
		return nil, false
	}
	if payload, ok := entry["answer"]; ok && payload != nil {
		return payload, true
	}
	// Older clients only flip `answered` without writing a payload; treat
	// that as an empty-but-valid answer rather than a timeout.
	return map[string]any{}, true
}

// autoSelectAskAnswers builds the answer payload the backend would have
// produced on timeout: for every question, the option marked recommended, or
// the first option when none is marked. Mirrors AskModal.autoProceed.
func autoSelectAskAnswers(questions any) map[string]any {
	answers := map[string]any{}
	list, ok := questions.([]any)
	if !ok {
		return answers
	}
	for _, rawQuestion := range list {
		question, ok := rawQuestion.(map[string]any)
		if !ok {
			continue
		}
		id, _ := question["id"].(string)
		if id == "" {
			continue
		}
		options, ok := question["options"].([]any)
		if !ok || len(options) == 0 {
			continue
		}
		var chosen map[string]any
		for _, rawOption := range options {
			option, ok := rawOption.(map[string]any)
			if !ok {
				continue
			}
			if recommended, _ := option["recommended"].(bool); recommended {
				chosen = option
				break
			}
			if chosen == nil {
				chosen = option
			}
		}
		if chosen == nil {
			continue
		}
		label := ""
		if value, ok := chosen["label"].(string); ok {
			label = value
		}
		if label == "" {
			continue
		}
		answers[id] = []any{label}
		if description, ok := chosen["description"].(string); ok && description != "" {
			answers[id+"_desc"] = description
		}
	}
	return answers
}

// markAskEntryAnswered rewrites the entry at index with the resolved answer,
// preserving every other field (including the questions payload) so the
// transcript keeps the full context of what was asked.
func markAskEntryAnswered(resultPath string, index int, answers any, autoSelected bool) error {
	raw, err := os.ReadFile(resultPath)
	if err != nil {
		return err
	}
	var entries []map[string]any
	if err := json.Unmarshal(raw, &entries); err != nil {
		return err
	}
	if index < 0 || index >= len(entries) {
		return os.ErrNotExist
	}
	entries[index]["answered"] = true
	entries[index]["answer"] = answers
	entries[index]["answered_at"] = time.Now().Unix()
	if autoSelected {
		entries[index][askUserAutoSelectedParam] = true
	}
	return writeJSONFileAtomically(resultPath, entries)
}

// askQuestionsFromPayload digs the questions back out of a persisted entry so
// the timeout path can auto-select from exactly what was shown to the user.
func askQuestionsFromPayload(entry map[string]any) any {
	if entry == nil {
		return nil
	}
	return entry["questions"]
}

// normalizeAnswerPayload accepts both the structured map AskModal sends and a
// bare JSON string from simpler clients.
func normalizeAnswerPayload(value any) any {
	if value == nil {
		return map[string]any{}
	}
	if text, ok := value.(string); ok {
		var decoded map[string]any
		if json.Unmarshal([]byte(text), &decoded) == nil {
			return decoded
		}
		return map[string]any{"free_text": strings.TrimSpace(text)}
	}
	return value
}
