package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// askArgsWithTimeout builds a select_mode ask whose backend deadline is short,
// so the tests exercise the real waiting path without waiting 180s.
func askArgsWithTimeout(questions string, seconds float64) string {
	return `{"ask_type":"select_mode","__wait_timeout_seconds":` +
		jsonNumber(seconds) + `,"questions":` + questions + `}`
}

func jsonNumber(v float64) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func readAskEntries(t *testing.T, workspace string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(workspace, "ask_user", "ask_results.json"))
	if err != nil {
		t.Fatal(err)
	}
	var entries []map[string]any
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatal(err)
	}
	return entries
}

const recommendedQuestion = `[{"id":"mode","question":"Choose","options":[` +
	`{"label":"first","description":"d1"},` +
	`{"label":"second","recommended":true,"description":"d2"}]}]`

// The deadline expires with no human reply: the backend must resolve by itself
// and pick the recommended option.
func TestAskUserAutoSelectsRecommendedOnTimeout(t *testing.T) {
	workspace := t.TempDir()
	start := time.Now()
	result := executeLocalAskUser(t, workspace, askArgsWithTimeout(recommendedQuestion, 1))
	elapsed := time.Since(start)

	if result.IsError {
		t.Fatalf("unexpected error: %#v", result.Value)
	}
	if result.WaitingReply {
		t.Fatal("backend must resolve before returning, not hand back an open wait")
	}
	if result.Value[askUserAutoSelectedParam] != true {
		t.Fatalf("expected auto_selected=true, got %#v", result.Value[askUserAutoSelectedParam])
	}
	answer, ok := result.Value["answer"].(map[string]any)
	if !ok {
		t.Fatalf("answer must be a map, got %T", result.Value["answer"])
	}
	labels, ok := answer["mode"].([]any)
	if !ok || len(labels) != 1 || labels[0] != "second" {
		t.Fatalf("must pick the recommended option, got %#v", answer["mode"])
	}
	if answer["mode_desc"] != "d2" {
		t.Fatalf("description of the chosen option must travel with it: %#v", answer)
	}
	if elapsed < time.Second {
		t.Fatalf("returned before the deadline (%v) — the wait is not real", elapsed)
	}
	resultInfo := result.Value["result"].(map[string]any)
	if resultInfo["status"] != "auto_selected" {
		t.Fatalf("status must be auto_selected, got %#v", resultInfo["status"])
	}
}

// The persisted entry must record the timeout resolution so the transcript can
// tell an auto-pick from a real answer.
func TestAskUserPersistsAutoSelection(t *testing.T) {
	workspace := t.TempDir()
	executeLocalAskUser(t, workspace, askArgsWithTimeout(recommendedQuestion, 1))

	entries := readAskEntries(t, workspace)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	entry := entries[0]
	if entry["answered"] != true {
		t.Fatalf("entry must be marked answered after the deadline: %#v", entry)
	}
	if entry[askUserAutoSelectedParam] != true {
		t.Fatalf("entry must carry auto_selected: %#v", entry)
	}
	if _, ok := entry["answered_at"]; !ok {
		t.Fatalf("entry must record when it resolved: %#v", entry)
	}
}

// A human answering before the deadline must win: no auto-selection, and the
// stored answer is the one they submitted.
func TestAskUserReturnsHumanAnswerBeforeTimeout(t *testing.T) {
	workspace := t.TempDir()
	tool := NewLocalAskUserTool(localFileTestSchema("ask_user"))
	args := json.RawMessage(askArgsWithTimeout(recommendedQuestion, 10))

	type outcome struct {
		result ToolResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := tool.Execute(context.Background(), ToolInvocation{
			Name:      "ask_user",
			Workspace: workspace,
			Args:      args,
		})
		done <- outcome{res, err}
	}()

	// Wait for the question to be persisted, then answer it the way the UI does.
	waitForEntry(t, workspace)
	answerAsk(t, workspace, map[string]any{"mode": []any{"first"}})

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.result.Value[askUserAutoSelectedParam] != false {
			t.Fatalf("human answer must not be flagged auto_selected: %#v", got.result.Value)
		}
		answer := got.result.Value["answer"].(map[string]any)
		labels := answer["mode"].([]any)
		if labels[0] != "first" {
			t.Fatalf("must return the human's choice, got %#v", labels)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("tool did not return after the answer was written")
	}
}

// The deadline must not overwrite an answer that landed first. Simulates the
// old double-timer hazard: frontend fires at 30s, backend at 180s.
func TestAskUserTimeoutDoesNotOverwriteHumanAnswer(t *testing.T) {
	workspace := t.TempDir()
	resultPath := filepath.Join(workspace, "ask_user", "ask_results.json")
	entries := []map[string]any{
		{"timestamp": time.Now().Unix(), "ask_type": "select_mode", "questions": []any{}, "answered": false},
	}
	if err := writeJSONFileAtomically(resultPath, entries); err != nil {
		t.Fatal(err)
	}

	// A human answers entry 0 before any wait starts.
	waitForEntry(t, workspace)
	answerAsk(t, workspace, map[string]any{"mode": []any{"first"}})

	payload, answered, err := waitForAskReply(context.Background(), resultPath, 0, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !answered {
		t.Fatal("waitForAskReply must observe the existing answer instead of timing out")
	}
	answer := payload.(map[string]any)
	if answer["mode"].([]any)[0] != "first" {
		t.Fatalf("answer was overwritten: %#v", answer)
	}
}

func TestWaitForAskReplyHonoursExtendedDeadline(t *testing.T) {
	workspace := t.TempDir()
	resultPath := filepath.Join(workspace, "ask_user", "ask_results.json")
	entries := []map[string]any{{
		"timestamp":                 time.Now().Unix(),
		"ask_type":                  "select_mode",
		"questions":                 []any{},
		"answered":                  false,
		"call_id":                   "call_renew",
		"auto_select_after_seconds": 1,
		"deadline_at_ms":            time.Now().Add(200 * time.Millisecond).UnixMilli(),
	}}
	if err := writeJSONFileAtomically(resultPath, entries); err != nil {
		t.Fatal(err)
	}

	type outcome struct {
		answered bool
		err      error
	}
	done := make(chan outcome, 1)
	started := time.Now()
	go func() {
		_, answered, err := waitForAskReply(context.Background(), resultPath, 0, 200*time.Millisecond)
		done <- outcome{answered: answered, err: err}
	}()

	// Simulate the renew route pushing the persisted deadline forward after
	// the user interacts with the modal.
	time.Sleep(80 * time.Millisecond)
	entries[0]["deadline_at_ms"] = time.Now().Add(900 * time.Millisecond).UnixMilli()
	if err := writeJSONFileAtomically(resultPath, entries); err != nil {
		t.Fatal(err)
	}

	// This lands after the original deadline but before the renewed one.
	time.Sleep(250 * time.Millisecond)
	answerAsk(t, workspace, map[string]any{"mode": []any{"first"}})

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if !got.answered {
			t.Fatal("backend ignored the renewed deadline and auto-selected too early")
		}
		if elapsed := time.Since(started); elapsed < 300*time.Millisecond {
			t.Fatalf("wait returned before the human answered: %v", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("wait did not observe the renewed answer")
	}
}

// No questions at all (a bare confirmation) still resolves on the deadline.
func TestAskUserAutoSelectsBareConfirmation(t *testing.T) {
	workspace := t.TempDir()
	start := time.Now()
	result := executeLocalAskUser(t, workspace, `{"ask_type":"confirm","__wait_timeout_seconds":1,"questions":[]}`)
	if result.IsError {
		t.Fatalf("unexpected error: %#v", result.Value)
	}
	if result.Value[askUserAutoSelectedParam] != true {
		t.Fatalf("bare confirmation must auto-resolve: %#v", result.Value)
	}
	if result.Value["answer"].(map[string]any) == nil {
		t.Fatal("auto-selected bare confirmation needs an answer map")
	}
	if time.Since(start) < time.Second {
		t.Fatal("returned before the deadline")
	}
}

// Context cancellation must abort the wait instead of burning the deadline.
func TestAskUserHonoursContextCancellation(t *testing.T) {
	workspace := t.TempDir()
	tool := NewLocalAskUserTool(localFileTestSchema("ask_user"))
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := tool.Execute(ctx, ToolInvocation{
		Name:      "ask_user",
		Workspace: workspace,
		Args:      json.RawMessage(askArgsWithTimeout(recommendedQuestion, 60)),
	})
	if err == nil {
		t.Fatal("expected the cancelled context to surface as an error")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("cancellation was ignored, waited %v", elapsed)
	}
}

// autoSelectAskAnswers must fall back to the first option when nothing is
// marked recommended, and tolerate malformed input rather than panicking.
func TestAutoSelectAskAnswersEdgeCases(t *testing.T) {
	if got := autoSelectAskQuestions(t); len(got) != 1 {
		t.Fatalf("unexpected: %#v", got)
	}
	// No recommended flag -> first option wins.
	plain := []any{map[string]any{
		"id": "q", "options": []any{
			map[string]any{"label": "a"},
			map[string]any{"label": "b"},
		},
	}}
	got := autoSelectAskAnswers(plain)
	if got["q"].([]any)[0] != "a" {
		t.Fatalf("must fall back to the first option: %#v", got)
	}
	// Malformed shapes must not panic.
	for _, bad := range []any{nil, []any{}, "text", 42, []any{map[string]any{"id": "x"}}} {
		if out := autoSelectAskAnswers(bad); out == nil {
			t.Fatalf("autoSelectAskAnswers(%#v) returned nil map", bad)
		}
	}
}

func autoSelectAskQuestions(t *testing.T) map[string]any {
	t.Helper()
	return autoSelectAskAnswers([]any{map[string]any{
		"id": "q", "options": []any{map[string]any{"label": "only"}},
	}})
}

// The frontend used to own a 30s timer; the backend must publish its deadline
// so a single clock drives every surface.
func TestAskUserPublishesItsDeadline(t *testing.T) {
	workspace := t.TempDir()
	result := executeLocalAskUser(t, workspace, askArgsWithTimeout(recommendedQuestion, 2))
	if result.Value["auto_select_after_seconds"] != 2 {
		t.Fatalf("deadline must be published for the UI, got %#v", result.Value["auto_select_after_seconds"])
	}
}

func waitForEntry(t *testing.T, workspace string) {
	t.Helper()
	path := filepath.Join(workspace, "ask_user", "ask_results.json")
	for i := 0; i < 100; i++ {
		if raw, err := os.ReadFile(path); err == nil {
			var entries []map[string]any
			if json.Unmarshal(raw, &entries) == nil && len(entries) > 0 {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("question entry was never persisted")
}

// answerAsk mimics the client writing its reply into the results file.
func answerAsk(t *testing.T, workspace string, answers map[string]any) {
	t.Helper()
	path := filepath.Join(workspace, "ask_user", "ask_results.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var entries []map[string]any
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no entry to answer")
	}
	entries[len(entries)-1]["answered"] = true
	entries[len(entries)-1]["answer"] = answers
	if err := writeJSONFileAtomically(path, entries); err != nil {
		t.Fatal(err)
	}
}
