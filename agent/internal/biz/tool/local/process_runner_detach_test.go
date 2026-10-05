package local

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	detachSurvivalHelperEnv = "FAIRY_DETACH_SURVIVAL_HELPER"
	detachSurvivalMarkerEnv = "FAIRY_DETACH_SURVIVAL_MARKER"
	detachSurvivalDelay     = 700 * time.Millisecond
)

// TestDetachedProcessSurvivesParentContextCancel pins the contract that a
// detached job is not bound to the turn that started it.
//
// Regression: the detached child was spawned with exec.CommandContext, so the
// parent context cancel (which happens as soon as the foreground turn ends)
// killed the background subtask almost immediately. The job never finished and
// the main thread was therefore never resumed.
func TestDetachedProcessSurvivesParentContextCancel(t *testing.T) {
	if os.Getenv(detachSurvivalHelperEnv) == "1" {
		time.Sleep(detachSurvivalDelay)
		if err := os.WriteFile(os.Getenv(detachSurvivalMarkerEnv), []byte("ok"), 0o600); err != nil {
			os.Exit(3)
		}
		return
	}

	jobDir := t.TempDir()
	marker := filepath.Join(jobDir, "survived.txt")
	stdoutPath := filepath.Join(jobDir, "job.stdout")
	stderrPath := filepath.Join(jobDir, "job.stderr")

	ctx, cancel := context.WithCancel(context.Background())
	result, err := (&osLocalProcessRunner{}).Run(ctx, localProcessRequest{
		Path:       os.Args[0],
		Args:       []string{"-test.run=^TestDetachedProcessSurvivesParentContextCancel$"},
		Env:        []string{detachSurvivalHelperEnv + "=1", detachSurvivalMarkerEnv + "=" + marker},
		Timeout:    30 * time.Second,
		Detach:     true,
		OutputPath: stdoutPath,
		ErrorPath:  stderrPath,
		JobID:      "test-detach-survival",
	})
	if err != nil {
		t.Fatalf("detached run: %v", err)
	}
	if !result.Detached || result.PID <= 0 {
		t.Fatalf("detached result = %#v, want a populated pid", result)
	}
	t.Cleanup(func() { _ = killDetachedJob("test-detach-survival") })

	// The turn that started the job is over: its context is cancelled.
	cancel()

	deadline := time.Now().Add(detachSurvivalDelay + 10*time.Second)
	seen := false
	for {
		if _, statErr := os.Stat(marker); statErr == nil {
			seen = true
		}
		// The completion goroutine closes the redirected stdout/stderr handles
		// and only then unregisters the job, so wait for that before returning:
		// the temp dir cannot be removed while the child still holds the files.
		if _, live := lookupDetachedJob("test-detach-survival"); seen && !live {
			return
		}
		if time.Now().After(deadline) {
			exitCode, _ := os.ReadFile(stdoutPath + ".exit")
			t.Fatalf("detached process did not survive parent context cancel (exit=%q)", string(exitCode))
		}
		time.Sleep(50 * time.Millisecond)
	}
}
