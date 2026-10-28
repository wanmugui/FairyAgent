package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression guard for the ask_user popup disappearing.
//
// ask_user blocks inside the tool while it waits for a human, so publishing
// `waiting_user_input` after Execute (the generic WaitingReply path) fires it
// only once the deadline has passed and the tool has already auto-selected —
// the modal would never be usable. The question therefore has to be published
// BEFORE dispatch. That ordering is the bug, so it is what we assert here.
func TestPreEmitAskUserEventsFiresBeforeDispatch(t *testing.T) {
	out := captureStdout(t, func() {
		preEmitAskUserEvents([]ToolInvocation{{
			CallID:   "call-1",
			Name:     "ask_user",
			Args:     json.RawMessage(`{"ask_type":"select_mode","questions":[{"id":"q1","question":"选哪个？"}]}`),
			Workspace: t.TempDir(),
		}}, false)
	})

	if !strings.Contains(out, `"waiting_user_input"`) {
		t.Fatalf("expected waiting_user_input to be emitted, got: %s", out)
	}
	if !strings.Contains(out, `"call_id":"call-1"`) {
		t.Fatalf("expected call_id in the event, got: %s", out)
	}
	if !strings.Contains(out, "选哪个？") {
		t.Fatalf("expected the question payload, got: %s", out)
	}
}

// The countdown is what the modal renders; it must be advertised up front too.
func TestPreEmitAskUserEventsAdvertisesDeadline(t *testing.T) {
	out := captureStdout(t, func() {
		preEmitAskUserEvents([]ToolInvocation{{
			CallID:    "call-2",
			Name:      "ask_user",
			Args:      json.RawMessage(`{"ask_type":"select_mode","questions":[{"id":"q1","question":"q"}]}`),
			Workspace: t.TempDir(),
		}}, false)
	})

	if !strings.Contains(out, `"auto_select_after_seconds":30`) {
		t.Fatalf("expected a 30s deadline in the event, got: %s", out)
	}
	if strings.Contains(out, `"answered":true`) {
		t.Fatalf("event must not claim an answer yet, got: %s", out)
	}
}

// Only ask_user gets this treatment; a normal tool call must stay silent so we
// do not spam the event stream.
func TestPreEmitAskUserEventsIgnoresOtherTools(t *testing.T) {
	out := captureStdout(t, func() {
		preEmitAskUserEvents([]ToolInvocation{{
			CallID:    "call-3",
			Name:      "bash",
			Args:      json.RawMessage(`{"command":"ls"}`),
			Workspace: t.TempDir(),
		}}, false)
	})
	if strings.Contains(out, "waiting_user_input") {
		t.Fatalf("non-ask_user tool must not emit the ask event, got: %s", out)
	}
}

// Headless/batch mode has no human to answer. Announcing the question there
// would pop a modal nobody can act on, and the tool still burned its full
// deadline — so batch mode must not announce, and must not reserve an entry.
func TestPreEmitAskUserEventsSkippedWhenAutoAnswering(t *testing.T) {
	workspace := t.TempDir()
	out := captureStdout(t, func() {
		preEmitAskUserEvents([]ToolInvocation{{
			CallID:    "call-5",
			Name:      "ask_user",
			Args:      json.RawMessage(`{"ask_type":"ppt_mode.confirm_outline","questions":[]}`),
			Workspace: workspace,
		}}, true)
	})
	if strings.Contains(out, "waiting_user_input") {
		t.Fatalf("auto-answer mode must not announce a question, got: %s", out)
	}
	if _, err := os.Stat(filepath.Join(workspace, "ask_user", "ask_results.json")); err == nil {
		t.Fatalf("auto-answer mode must not reserve a results entry")
	}
}

// A reply can arrive before the tool has written its own entry — the event is
// published first, so a fast click beats the tool to the file. Without the
// reservation the reply found nothing unanswered and the user silently got the
// timeout fallback instead of the option they picked.
func TestPreEmitAskUserEventsReservesAnswerableEntry(t *testing.T) {
	workspace := t.TempDir()
	captureStdout(t, func() {
		preEmitAskUserEvents([]ToolInvocation{{
			CallID:    "call-6",
			Name:      "ask_user",
			Args:      json.RawMessage(`{"ask_type":"select_mode","questions":[{"id":"q1","question":"q"}]}`),
			Workspace: workspace,
		}}, false)
	})

	raw, err := os.ReadFile(filepath.Join(workspace, "ask_user", "ask_results.json"))
	if err != nil {
		t.Fatalf("expected a reserved entry: %v", err)
	}
	var entries []map[string]any
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("reserved file is not valid json: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 entry, got %d", len(entries))
	}
	if entries[0]["answered"] == true {
		t.Fatalf("the reserved entry must start unanswered")
	}
}

// A missing workspace must not stop the question from reaching the surface.
// Losing the modal is far worse than losing the fast-reply optimisation.
func TestPreEmitAskUserEventsSurvivesBadArgsAndMissingWorkspace(t *testing.T) {
	out := captureStdout(t, func() {
		preEmitAskUserEvents([]ToolInvocation{{
			CallID:    "call-4",
			Name:      "ask_user",
			Args:      json.RawMessage(`{not json`),
			Workspace: "",
		}}, false)
	})
	if !strings.Contains(out, `"waiting_user_input"`) {
		t.Fatalf("malformed args must still emit (empty modal), got: %s", out)
	}
}

func TestDecodeAskUserArgsReadsTypeAndQuestions(t *testing.T) {
	questions, askType := decodeAskUserArgs(json.RawMessage(
		`{"ask_type":"ppt_mode.confirm_params","questions":[{"id":"p1","question":"ok?"}]}`,
	))
	if askType != "ppt_mode.confirm_params" {
		t.Fatalf("ask_type = %q", askType)
	}
	if len(questions) != 1 {
		t.Fatalf("questions = %d, want 1", len(questions))
	}
}

func TestDecodeAskUserArgsOnEmptyInput(t *testing.T) {
	questions, askType := decodeAskUserArgs(nil)
	if len(questions) != 0 {
		t.Fatalf("questions = %d, want 0", len(questions))
	}
	if askType != "" {
		t.Fatalf("ask_type = %q, want empty", askType)
	}
}

func TestAskWaitingStatus(t *testing.T) {
	if got := askWaitingStatus([]any{map[string]any{"id": "q"}}); got != "waiting_questions" {
		t.Fatalf("got %q", got)
	}
	if got := askWaitingStatus(nil); got != "waiting_confirmation" {
		t.Fatalf("got %q", got)
	}
}

// captureStdout runs fn while stdout is redirected, and returns what it wrote.
// emitEvent writes straight to stdout, which is the only seam available here.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()
	fn()
	_ = w.Close()
	os.Stdout = original
	out := <-done
	_ = r.Close()
	return out
}
