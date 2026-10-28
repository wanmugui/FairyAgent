package main

import (
	"strings"
	"testing"
)

// TestCleanupRuleLabelsDistinguishWindowFromTiebreak locks the label split.
// Both rules used to start with "C1", so the aggregate log line showed the
// same token for two opposite states: a result still inside the protection
// window (kept, no tiebreak) and a result that already reached the semantic
// tiebreak. That made the log impossible to interpret.
func TestCleanupRuleLabelsDistinguishWindowFromTiebreak(t *testing.T) {
	labels := map[string]bool{}
	for _, rule := range []string{
		"C1w 下一步用到本结果的标识",
		"C1 已超窗，待语义裁决",
	} {
		word := strings.Fields(rule)[0]
		if labels[word] {
			t.Fatalf("cleanup rule label %q is used by more than one rule", word)
		}
		labels[word] = true
	}
	if !labels["C1w"] {
		t.Fatal("the protection-window rule must use its own label")
	}
	if !labels["C1"] {
		t.Fatal("the tiebreak rule must keep the C1 label")
	}
}

// TestKeepPercentMeasuresKeptShare locks the corrected percentage. The old
// expression (100-judged/len(stale)*100) used integer division and measured
// the share that had NOT been judged, so it reported 0 whenever every
// candidate reached the tiebreak - the exact case worth reporting.
func TestKeepPercentMeasuresKeptShare(t *testing.T) {
	cases := []struct {
		judged  int
		cleared int
		want    int
	}{
		{judged: 0, cleared: 0, want: 0},
		{judged: 10, cleared: 0, want: 100},
		{judged: 10, cleared: 10, want: 0},
		{judged: 10, cleared: 3, want: 70},
		{judged: 3, cleared: 1, want: 66},
	}
	for _, tc := range cases {
		if got := keepPercent(tc.judged, tc.cleared); got != tc.want {
			t.Fatalf("keepPercent(%d, %d) = %d, want %d", tc.judged, tc.cleared, got, tc.want)
		}
	}
}
