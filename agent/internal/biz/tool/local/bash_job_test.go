package local

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalBashJobReadsIncrementallyAndReportsExitCode(t *testing.T) {
	workspace := t.TempDir()
	jobDir := filepath.Join(workspace, ".bash_jobs")
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(jobDir, "job-test.stdout")
	stderrPath := filepath.Join(jobDir, "job-test.stderr")
	if err := os.WriteFile(stdoutPath, []byte("abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stdoutPath+".exit", []byte("7"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(stderrPath, nil, 0o600)

	tool := NewLocalBashJobTool(ToolDef{}, &Config{})
	result, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: workspace,
		Args:      json.RawMessage(`{"job_id":"job-test","action":"read","offset_bytes":2,"max_bytes":3}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || result.Value["stdout"] != "cde" || result.Value["next_offset"] != 5 || result.Value["exit_code"] != 7 {
		t.Fatalf("unexpected incremental read result: %#v", result)
	}
}
