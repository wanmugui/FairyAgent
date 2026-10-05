package main

import (
	"testing"
	"time"
)

func TestToolTimeoutResolution(t *testing.T) {
	cfg := &Config{API: APIConfig{TimeoutSec: 60}, ToolRuntime: &ToolRuntimeConfig{
		Timeouts: map[string]int{"bash": 5, "create_subtask": 3600},
	}}
	global := 60 * time.Second
	if got := cfg.ToolTimeout("bash", global); got != 5*time.Second {
		t.Fatalf("bash timeout = %v, want 5s", got)
	}
	if got := cfg.ToolTimeout("create_subtask", global); got != 3600*time.Second {
		t.Fatalf("create_subtask timeout = %v, want 3600s", got)
	}
	if got := cfg.ToolTimeout("read_file", global); got != global {
		t.Fatalf("unlisted timeout = %v, want global %v", got, global)
	}
	// nil ToolRuntime falls back to global
	if got := (&Config{}).ToolTimeout("bash", global); got != global {
		t.Fatalf("nil runtime timeout = %v", got)
	}
}

func TestToolNeedsApproval(t *testing.T) {
	cfg := &Config{ToolRuntime: &ToolRuntimeConfig{
		Approval:     map[string]string{"bash": "ask"},
		ApprovalMode: "ask",
	}}
	if !cfg.ToolNeedsApproval("bash") {
		t.Fatal("bash should need approval in ask mode")
	}
	if cfg.ToolNeedsApproval("read_file") {
		t.Fatal("read_file should not need approval")
	}
	cfg.ToolRuntime.ApprovalMode = "allow"
	if cfg.ToolNeedsApproval("bash") {
		t.Fatal("allow mode must not block")
	}
	cfg.ToolRuntime.ApprovalMode = "deny"
	if !cfg.ToolNeedsApproval("bash") {
		t.Fatal("deny mode must block listed tools")
	}
	if cfg.ToolNeedsApproval("read_file") {
		t.Fatal("deny mode must not block unlisted tools")
	}
	// default: no approval configured => allow
	if (&Config{ToolRuntime: &ToolRuntimeConfig{}}).ToolNeedsApproval("bash") {
		t.Fatal("no approval config should never block")
	}
}