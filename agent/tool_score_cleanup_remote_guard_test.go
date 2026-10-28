package main

import (
	"testing"
)

// TestRemoteClassifierStaysUnreachable guards the shipped configuration
// against drifting back to the remote classifier. The plan audit action was
// removed, and the tool score cleanup remote branch is only reachable when
// remote_classifier is explicitly enabled. Neither may turn back on by
// accident, because both would send conversation content off the machine.
func TestRemoteClassifierStaysUnreachable(t *testing.T) {
	configPath := "config/config.json"
	repoRoot := findRepoRoot(configPath)
	cfg, err := LoadConfig(repoRoot, configPath)
	if err != nil {
		t.Skipf("repo config unavailable: %v", err)
	}
	cleanup := cfg.ToolScoreCleanup
	t.Logf("tool_score_cleanup: endpoint=%q remote_classifier=%v",
		cleanup.Endpoint, cleanup.RemoteClassifier)
	if cleanup.RemoteClassifier {
		t.Fatal("tool_score_cleanup.remote_classifier must stay disabled: the remote path sends content off the machine")
	}
	if cleanup.UseRemoteClassifier() {
		t.Fatal("the remote classifier must not be reachable from the shipped config")
	}
}
