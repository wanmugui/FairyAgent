package local

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type localBashJobTool struct {
	schema ToolDef
	cfg    *Config
}

func NewLocalBashJobTool(schema ToolDef, cfg *Config) Tool {
	return &localBashJobTool{schema: schema, cfg: cfg}
}

func (t *localBashJobTool) Name() string {
	return "bash_job"
}

func (t *localBashJobTool) Schema() ToolDef {
	return t.schema
}

func (t *localBashJobTool) Execute(ctx context.Context, invocation ToolInvocation) (ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}
	args, err := decodeLocalToolArgs(invocation)
	if err != nil {
		return localErrorResult(t.Name(), err), nil
	}
	jobID := strings.TrimSpace(localStringArg(args, "job_id"))
	action := strings.TrimSpace(strings.ToLower(localStringArg(args, "action")))
	if jobID == "" {
		return localErrorResult(t.Name(), fmt.Errorf("job_id is required")), nil
	}
	if action == "" {
		action = "status"
	}
	localContext, err := localToolContext(invocation)
	if err != nil {
		return localErrorResult(t.Name(), err), nil
	}
	jobDir := filepath.Join(localContext.Workspace, ".bash_jobs")
	stdoutPath := filepath.Join(jobDir, jobID+".stdout")
	stderrPath := filepath.Join(jobDir, jobID+".stderr")

	switch action {
	case "status":
		return t.statusOf(jobID, stdoutPath, stderrPath), nil
	case "read":
		tail := localIntArg(args, "tail_lines", 200)
		if tail <= 0 {
			tail = 200
		}
		offset := localIntArg(args, "offset_bytes", -1)
		maxBytes := localIntArg(args, "max_bytes", int(defaultLocalOutputLimitBytes))
		if maxBytes <= 0 {
			maxBytes = int(defaultLocalOutputLimitBytes)
		}
		var stdoutChunk, stderrChunk string
		var stdoutNext, stderrNext int
		if offset >= 0 {
			stdoutChunk, stdoutNext = readFileFrom(stdoutPath, offset, maxBytes)
			stderrChunk, stderrNext = readFileFrom(stderrPath, offset, maxBytes)
		} else {
			stdoutChunk, _ = tailFile(stdoutPath, tail)
			stderrChunk, _ = tailFile(stderrPath, tail)
			stdoutNext = int(fileSizeOrZero(stdoutPath))
			stderrNext = int(fileSizeOrZero(stderrPath))
		}
		value := map[string]any{
			"tool":        t.Name(),
			"ok":          true,
			"job_id":      jobID,
			"status":      jobStatusString(stdoutPath, stderrPath, lookupJobPresence(jobID)),
			"stdout":      stdoutChunk,
			"stderr":      stderrChunk,
			"next_offset": maxInt(stdoutNext, stderrNext),
		}
		if exitCode, ok := readJobExitCode(stdoutPath); ok {
			value["exit_code"] = exitCode
		}
		return ToolResult{Value: value}, nil
	case "kill":
		if err := killDetachedJob(jobID); err != nil {
			return localErrorResult(t.Name(), fmt.Errorf("kill job: %w", err)), nil
		}
		return ToolResult{Value: map[string]any{
			"tool":   t.Name(),
			"ok":     true,
			"job_id": jobID,
			"killed": true,
		}}, nil
	default:
		return localErrorResult(t.Name(), fmt.Errorf("unknown action %q (use status|read|kill)", action)), nil
	}
}

func (t *localBashJobTool) statusOf(jobID, stdoutPath, stderrPath string) ToolResult {
	present := lookupJobPresence(jobID)
	stdoutBytes := fileSizeOrZero(stdoutPath)
	stderrBytes := fileSizeOrZero(stderrPath)
	value := map[string]any{
		"tool":         t.Name(),
		"ok":           true,
		"job_id":       jobID,
		"status":       jobStatusString(stdoutPath, stderrPath, present),
		"running":      present,
		"stdout_path":  stdoutPath,
		"stderr_path":  stderrPath,
		"stdout_bytes": stdoutBytes,
		"stderr_bytes": stderrBytes,
	}
	if pid, ok := readJobPID(stdoutPath); ok {
		value["pid"] = pid
	}
	if exitCode, ok := readJobExitCode(stdoutPath); ok {
		value["exit_code"] = exitCode
	}
	return ToolResult{Value: value}
}

// lookupJobPresence is a thin wrapper so the tool can ask the registry
// without importing sync internals.
func lookupJobPresence(id string) bool {
	_, ok := lookupDetachedJob(id)
	return ok
}

func jobStatusString(stdoutPath, stderrPath string, running bool) string {
	if running {
		return "running"
	}
	// Job is gone from the registry; assume completed. The bash_job tool
	// can still read stdout/stderr files which persist on disk.
	if _, err := os.Stat(stdoutPath + ".exit"); err == nil {
		return "completed"
	}
	if fileSizeOrZero(stdoutPath) == 0 && fileSizeOrZero(stderrPath) == 0 {
		return "missing"
	}
	return "completed"
}

func tailFile(path string, lines int) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	if lines <= 0 {
		return string(data), nil
	}
	// Tail from the end, scanning backwards for '\n'. O(lines) work.
	if len(data) == 0 {
		return "", nil
	}
	count := 0
	end := len(data)
	for i := len(data) - 1; i >= 0; i-- {
		if data[i] == '\n' {
			count++
			if count == lines {
				return string(data[i+1 : end]), nil
			}
		}
	}
	return string(data), nil
}

func fileSizeOrZero(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func readFileFrom(path string, offset, maxBytes int) (string, int) {
	data, err := os.ReadFile(path)
	if err != nil || offset >= len(data) {
		return "", offset
	}
	if offset < 0 {
		offset = 0
	}
	end := len(data)
	if maxBytes > 0 && offset+maxBytes < end {
		end = offset + maxBytes
	}
	return string(data[offset:end]), end
}

func readJobExitCode(stdoutPath string) (int, bool) {
	data, err := os.ReadFile(stdoutPath + ".exit")
	if err != nil {
		return 0, false
	}
	exitCode, err := strconv.Atoi(strings.TrimSpace(string(data)))
	return exitCode, err == nil
}

func readJobPID(stdoutPath string) (int, bool) {
	data, err := os.ReadFile(stdoutPath + ".pid")
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	return pid, err == nil
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

// compile-time guard: keep time import used for future timeout/poll features.
var _ = time.Second
