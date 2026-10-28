package main

import (
	"testing"

	"agentloop/agent/internal/dtypes"
)

func TestDynamicWebBudgetTokensShrinksWithPrompt(t *testing.T) {
	cfg := &Config{SummaryThresholdTokens: 60000}
	early := dynamicWebBudgetTokens(cfg, 10000)
	late := dynamicWebBudgetTokens(cfg, 50000)
	if early != 25000 {
		t.Fatalf("early budget = %d, want 25000", early)
	}
	if late != 5000 {
		t.Fatalf("late budget = %d, want 5000", late)
	}
	if late >= early {
		t.Fatalf("late budget should be smaller: early=%d late=%d", early, late)
	}
}

func TestDynamicWebBudgetFloorsAtMinimum(t *testing.T) {
	cfg := &Config{SummaryThresholdTokens: 60000}
	got := dynamicWebBudgetTokens(cfg, 59900)
	if got != 2000 {
		t.Fatalf("minimum budget = %d, want 2000", got)
	}
}

func TestWebBudgetMetadata(t *testing.T) {
	meta := webBudgetMetadata(4321)
	if meta[dtypes.ToolMetadataWebBudgetTokens] != "4321" {
		t.Fatalf("unexpected metadata: %#v", meta)
	}
	if webBudgetMetadata(0) != nil {
		t.Fatal("zero budget should not create metadata")
	}
}

func TestWebSearchContextSize(t *testing.T) {
	cases := map[int]string{1000: "low", 8000: "medium", 24000: "high"}
	for tokens, want := range cases {
		if got := webSearchContextSize(tokens); got != want {
			t.Fatalf("context size for %d = %q, want %q", tokens, got, want)
		}
	}
}
