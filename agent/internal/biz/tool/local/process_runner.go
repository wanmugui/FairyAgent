package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type localProcessRequest struct {
	Path               string
	Args               []string
	Dir                string
	Env                []string
	Stdin              []byte
	Timeout            time.Duration
	SuccessFile        string
	SuccessFileMinSize int64
	StdoutWriter       io.Writer
	StderrWriter       io.Writer

	// Detach, when true, returns immediately after Start() with a populated
	// PID; stdout/stderr are streamed into OutputPath/ErrorPath and the caller's
	// notification channel receives the final exit error (nil on success).
	Detach         bool
	OutputPath     string
	ErrorPath      string
	JobID          string
	OnComplete     chan<- jobCompletion
	CancelProcess  func() error
	StdoutMaxBytes int64
	StderrMaxBytes int64
}

type jobCompletion struct {
	JobID    string
	PID      int
	ExitCode int
	Err      error
}

type localProcessResult struct {
	Stdout          string
	Stderr          string
	ExitCode        int
	TimedOut        bool
	CompletedByFile bool
	StdoutTruncated bool
	StderrTruncated bool
	StdoutPath      string
	StderrPath      string
	StdoutBytes     int64
	StderrBytes     int64

	// Populated only when Detach=true. The caller polls the corresponding
	// job_* tool to read the streaming files or to kill the process.
	Detached bool
	PID      int
}

type localProcessRunner interface {
	Run(context.Context, localProcessRequest) (localProcessResult, error)
}

type osLocalProcessRunner struct{}

func (osLocalProcessRunner) Run(ctx context.Context, request localProcessRequest) (localProcessResult, error) {
	if err := ctx.Err(); err != nil {
		return localProcessResult{}, err
	}
	if request.Path == "" {
		return localProcessResult{}, fmt.Errorf("process executable is required")
	}

	// Detached jobs must outlive the turn that started them. A context-bound
	// command is torn down as soon as the caller's turn ends - the parent
	// context is cancelled even though Run already returned - which would kill
	// the job almost immediately. Bind detached children to no context at all
	// and let the job manifest + detached job registry own their lifetime.
	if request.Detach {
		cmd := exec.Command(request.Path, request.Args...)
		cmd.Dir = request.Dir
		if len(request.Stdin) > 0 {
			cmd.Stdin = bytes.NewReader(request.Stdin)
		}
		if len(request.Env) > 0 {
			cmd.Env = mergeProcessEnvironment(os.Environ(), request.Env)
		}
		return startLocalDetachedProcess(cmd, request)
	}

	commandCtx := ctx
	cancel := func() {}
	if request.Timeout > 0 {
		commandCtx, cancel = context.WithTimeout(ctx, request.Timeout)
	}
	defer cancel()

	cmd := exec.CommandContext(commandCtx, request.Path, request.Args...)
	cmd.Dir = request.Dir
	if len(request.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(request.Stdin)
	}
	if len(request.Env) > 0 {
		cmd.Env = mergeProcessEnvironment(os.Environ(), request.Env)
	}
	cmd.Cancel = func() error {
		return terminateProcessTree(cmd.Process)
	}
	cmd.WaitDelay = 3 * time.Second

	stdoutCapture := newBoundedOutputCapture(request.StdoutMaxBytes)
	stderrCapture := newBoundedOutputCapture(request.StderrMaxBytes)
	cmd.Stdout = stdoutCapture
	cmd.Stderr = stderrCapture
	if request.StdoutWriter != nil {
		cmd.Stdout = io.MultiWriter(stdoutCapture, request.StdoutWriter)
	}
	if request.StderrWriter != nil {
		cmd.Stderr = io.MultiWriter(stderrCapture, request.StderrWriter)
	}

	if request.SuccessFile == "" {
		err := cmd.Run()
		return finishLocalProcess(request.Path, ctx, commandCtx, err, stdoutCapture, stderrCapture)
	}
	if err := cmd.Start(); err != nil {
		return localProcessResult{}, fmt.Errorf("start process %q: %w", request.Path, err)
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	lastSize := int64(-1)
	stableSamples := 0
	for {
		select {
		case err := <-done:
			// Screenshot-style tools may write a complete output file and then
			// let their browser process exit non-zero. The caller validates the
			// file itself, so a completed success file is the authoritative signal
			// unless the parent context or timeout already stopped the command.
			if ctx.Err() == nil && commandCtx.Err() == nil && completedLocalSuccessFile(request) {
				result := captureLocalProcessResult(stdoutCapture, stderrCapture)
				result.ExitCode = 0
				result.CompletedByFile = true
				return result, nil
			}
			return finishLocalProcess(request.Path, ctx, commandCtx, err, stdoutCapture, stderrCapture)
		case <-ticker.C:
			info, statErr := os.Stat(request.SuccessFile)
			if statErr != nil || !info.Mode().IsRegular() || info.Size() < localSuccessFileMinimumSize(request) {
				lastSize = -1
				stableSamples = 0
				continue
			}
			if info.Size() == lastSize {
				stableSamples++
			} else {
				lastSize = info.Size()
				stableSamples = 0
			}
			if stableSamples < 1 {
				continue
			}
			_ = terminateProcessTree(cmd.Process)
			<-done
			result := captureLocalProcessResult(stdoutCapture, stderrCapture)
			result.ExitCode = 0
			result.CompletedByFile = true
			return result, nil
		case <-commandCtx.Done():
			<-done
			result := captureLocalProcessResult(stdoutCapture, stderrCapture)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return result, ctxErr
			}
			result.ExitCode = -1
			result.TimedOut = true
			return result, nil
		}
	}
}

func completedLocalSuccessFile(request localProcessRequest) bool {
	info, err := os.Stat(request.SuccessFile)
	return err == nil && info.Mode().IsRegular() && info.Size() >= localSuccessFileMinimumSize(request)
}

// startLocalDetachedProcess launches the command and returns immediately with
// the PID. stdout/stderr stream into OutputPath/ErrorPath so the bash_job tool
// can tail them; OnComplete fires once when the process exits or is killed.
func startLocalDetachedProcess(cmd *exec.Cmd, request localProcessRequest) (localProcessResult, error) {
	if request.OutputPath == "" || request.ErrorPath == "" {
		return localProcessResult{}, fmt.Errorf("detach requires OutputPath and ErrorPath")
	}
	outFile, err := os.Create(request.OutputPath)
	if err != nil {
		return localProcessResult{}, fmt.Errorf("create stdout file: %w", err)
	}
	errFile, err := os.Create(request.ErrorPath)
	if err != nil {
		outFile.Close()
		return localProcessResult{}, fmt.Errorf("create stderr file: %w", err)
	}
	cmd.Stdout = outFile
	cmd.Stderr = errFile

	if err := cmd.Start(); err != nil {
		outFile.Close()
		errFile.Close()
		return localProcessResult{}, fmt.Errorf("start detached process %q: %w", request.Path, err)
	}
	pid := 0
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}
	if pid > 0 {
		_ = os.WriteFile(request.OutputPath+".pid", []byte(strconv.Itoa(pid)), 0o600)
	}

	if request.CancelProcess != nil {
		registerDetachedJob(request.JobID, cmd, outFile, errFile, request.CancelProcess)
	} else {
		registerDetachedJob(request.JobID, cmd, outFile, errFile, func() error { return terminateProcessTree(cmd.Process) })
	}

	completion := request.OnComplete
	// Always reap the child. Without a Wait() the process handle (and the
	// redirected stdout/stderr files) leak, and the job stays registered as
	// running forever even though the process already exited.
	go func() {
		waitErr := cmd.Wait()
		outFile.Close()
		errFile.Close()
		exit := 0
		if waitErr != nil {
			var exitErr *exec.ExitError
			if errors.As(waitErr, &exitErr) {
				exit = exitErr.ExitCode()
			} else {
				exit = -1
			}
		}
		_ = os.WriteFile(request.OutputPath+".exit", []byte(strconv.Itoa(exit)), 0o600)
		unregisterDetachedJob(request.JobID)
		if completion != nil {
			select {
			case completion <- jobCompletion{JobID: request.JobID, PID: pid, ExitCode: exit, Err: waitErr}:
			default:
			}
		}
	}()

	return localProcessResult{Detached: true, PID: pid}, nil
}

func localSuccessFileMinimumSize(request localProcessRequest) int64 {
	if request.SuccessFileMinSize > 0 {
		return request.SuccessFileMinSize
	}
	return 1
}

func finishLocalProcess(path string, ctx, commandCtx context.Context, err error, stdoutCapture, stderrCapture *boundedOutputCapture) (localProcessResult, error) {
	result := captureLocalProcessResult(stdoutCapture, stderrCapture)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return result, ctxErr
	}
	if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
		result.ExitCode = -1
		result.TimedOut = true
		return result, nil
	}
	if err == nil {
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	return result, fmt.Errorf("start process %q: %w", path, err)
}

func captureLocalProcessResult(stdoutCapture, stderrCapture *boundedOutputCapture) localProcessResult {
	stdout := stdoutCapture.Result()
	stderr := stderrCapture.Result()
	return localProcessResult{
		Stdout:          stdout.Text,
		Stderr:          stderr.Text,
		StdoutTruncated: stdout.Truncated,
		StderrTruncated: stderr.Truncated,
		StdoutPath:      stdout.Path,
		StderrPath:      stderr.Path,
		StdoutBytes:     stdout.Bytes,
		StderrBytes:     stderr.Bytes,
	}
}

func mergeProcessEnvironment(base, overrides []string) []string {
	if len(overrides) == 0 {
		return base
	}
	overridden := make(map[string]struct{}, len(overrides))
	for _, entry := range overrides {
		if key := processEnvironmentKey(entry); key != "" {
			overridden[key] = struct{}{}
		}
	}
	merged := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		key := processEnvironmentKey(entry)
		if _, ok := overridden[key]; ok {
			continue
		}
		merged = append(merged, entry)
	}
	return append(merged, overrides...)
}

func processEnvironmentKey(entry string) string {
	index := strings.IndexByte(entry, '=')
	if index <= 0 {
		return ""
	}
	key := entry[:index]
	if runtime.GOOS == "windows" {
		return strings.ToLower(key)
	}
	return key
}
