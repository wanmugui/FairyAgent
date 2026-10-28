package builtin

import "testing"

// The agent loop reserves an entry before dispatching so a fast reply can be
// written before the tool starts. If the tool appended its own entry anyway,
// two unanswered entries would exist: the reply lands on the reserved one, the
// tool watches the new one, and the human's answer is thrown away in favour of
// the timeout fallback. These tests pin the claim behaviour that prevents it.
func TestClaimReservedAskEntryAdoptsMatchingReservation(t *testing.T) {
	questions := []any{map[string]any{"id": "q1", "question": "选哪个？"}}
	entries := []map[string]any{
		{"ask_type": "select_mode", "answered": true, "answer": map[string]any{}},
		{"ask_type": "select_mode", "questions": questions, "answered": false, "reserved": true},
	}

	index := claimReservedAskEntry(entries, "select_mode", questions)
	if index != 1 {
		t.Fatalf("index = %d, want 1 (the reserved entry)", index)
	}
}

// The reservation is only usable if it is still unanswered. An already-answered
// one means the reply landed, and the caller must append a fresh entry for the
// retry instead of adopting a spent one.
func TestClaimReservedAskEntrySkipsAnswered(t *testing.T) {
	questions := []any{map[string]any{"id": "q1"}}
	entries := []map[string]any{
		{"ask_type": "select_mode", "questions": questions, "answered": true, "reserved": true},
	}

	if index := claimReservedAskEntry(entries, "select_mode", questions); index != -1 {
		t.Fatalf("index = %d, want -1 (answered reservation must not be reused)", index)
	}
}

// Questions that differ must not share a reservation: adopting the wrong one
// would attribute this ask's answer to a different question.
func TestClaimReservedAskEntryRejectsDifferentQuestions(t *testing.T) {
	entries := []map[string]any{
		{
			"ask_type":  "select_mode",
			"questions": []any{map[string]any{"id": "other"}},
			"answered":  false,
			"reserved":  true,
		},
	}

	index := claimReservedAskEntry(entries, "select_mode", []any{map[string]any{"id": "q1"}})
	if index != -1 {
		t.Fatalf("index = %d, want -1", index)
	}
}

func TestClaimReservedAskEntryRejectsDifferentType(t *testing.T) {
	entries := []map[string]any{
		{"ask_type": "ppt_mode.confirm_outline", "questions": []any{}, "answered": false, "reserved": true},
	}

	if index := claimReservedAskEntry(entries, "select_mode", []any{map[string]any{"id": "q1"}}); index != -1 {
		t.Fatalf("index = %d, want -1", index)
	}
}

// A plain confirmation carries no questions at all, so an id-less reservation of
// the same ask type is the right one to adopt.
func TestClaimReservedAskEntryMatchesBareConfirmation(t *testing.T) {
	entries := []map[string]any{
		{"ask_type": "confirm", "answered": false, "reserved": true},
	}

	if index := claimReservedAskEntry(entries, "confirm", []any{}); index != 0 {
		t.Fatalf("index = %d, want 0", index)
	}
}

// Without a reservation the tool must append, exactly as it always did.
func TestClaimReservedAskEntryWithoutReservation(t *testing.T) {
	entries := []map[string]any{
		{"ask_type": "select_mode", "questions": []any{map[string]any{"id": "q1"}}, "answered": false},
	}

	if index := claimReservedAskEntry(entries, "select_mode", []any{map[string]any{"id": "q1"}}); index != -1 {
		t.Fatalf("index = %d, want -1 (nothing reserved)", index)
	}
}

// Two reservations for the same ask: the newest wins, so a repeated or resumed
// ask claims the entry that was made for it.
func TestClaimReservedAskEntryPicksNewest(t *testing.T) {
	questions := []any{map[string]any{"id": "q1"}}
	entries := []map[string]any{
		{"ask_type": "select_mode", "questions": questions, "answered": true, "reserved": true},
		{"ask_type": "select_mode", "questions": questions, "answered": false, "reserved": true},
	}

	if index := claimReservedAskEntry(entries, "select_mode", questions); index != 1 {
		t.Fatalf("index = %d, want 1", index)
	}
}

func TestAskEntryIDsMatchIgnoresPayloadDifferences(t *testing.T) {
	entry := map[string]any{
		"questions": []any{map[string]any{"id": "q1", "question": "旧文案"}},
	}
	if !askEntryIDsMatch(entry, []string{"q1"}) {
		t.Fatalf("ids match even when the question text differs")
	}
	if askEntryIDsMatch(entry, []string{"q1", "q2"}) {
		t.Fatalf("a different question count must not match")
	}
}
