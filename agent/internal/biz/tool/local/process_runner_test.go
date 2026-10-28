package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalProcessHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_LOCAL_PROCESS_HELPER") != "1" {
		return
	}
	switch os.Getenv("LOCAL_PROCESS_HELPER_MODE") {
	case "output":
		fmt.Fprint(os.Stdout, "helper stdout")
		fmt.Fprint(os.Stderr, "helper stderr")
		os.Exit(3)
	case "cwd":
		cwd, _ := os.Getwd()
		fmt.Fprint(os.Stdout, cwd)
		os.Exit(0)
	case "sleep":
		time.Sleep(5 * time.Second)
		os.Exit(0)
	case "write-file":
		path := os.Getenv("LOCAL_PROCESS_SUCCESS_FILE")
		if path == "" {
			os.Exit(2)
		}
		if err := os.WriteFile(path, []byte("12345678"), 0o600); err != nil {
			os.Exit(4)
		}
		time.Sleep(5 * time.Second)
		os.Exit(0)
	case "write-file-exit-error":
		path := os.Getenv("LOCAL_PROCESS_SUCCESS_FILE")
		if path == "" {
			os.Exit(2)
		}
		if err := os.WriteFile(path, []byte("12345678"), 0o600); err != nil {
			os.Exit(4)
		}
		os.Exit(5)
	default:
		os.Exit(2)
	}
}

func localProcessHelperRequest(t *testing.T, mode string) localProcessRequest {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return localProcessRequest{
		Path: executable,
		Args: []string{"-test.run=TestLocalProcessHelperProcess"},
		Env: []string{
			"GO_WANT_LOCAL_PROCESS_HELPER=1",
			"LOCAL_PROCESS_HELPER_MODE=" + mode,
		},
	}
}

func TestLocalProcessRunnerCapturesOutputAndExitCode(t *testing.T) {
	request := localProcessHelperRequest(t, "output")

	got, err := (osLocalProcessRunner{}).Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if got.ExitCode != 3 || got.Stdout != "helper stdout" || got.Stderr != "helper stderr" || got.TimedOut {
		t.Fatalf("unexpected process result: %#v", got)
	}
}

func TestLocalProcessRunnerMirrorsOutputToConfiguredWriters(t *testing.T) {
	request := localProcessHelperRequest(t, "output")
	var mirroredStdout, mirroredStderr bytes.Buffer
	request.StdoutWriter = &mirroredStdout
	request.StderrWriter = &mirroredStderr

	got, err := (osLocalProcessRunner{}).Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stdout != "helper stdout" || got.Stderr != "helper stderr" {
		t.Fatalf("runner stopped capturing output: %#v", got)
	}
	if mirroredStdout.String() != "helper stdout" || mirroredStderr.String() != "helper stderr" {
		t.Fatalf("runner did not mirror output: stdout=%q stderr=%q", mirroredStdout.String(), mirroredStderr.String())
	}
}

func TestLocalProcessRunnerUsesWorkingDirectory(t *testing.T) {
	request := localProcessHelperRequest(t, "cwd")
	request.Dir = t.TempDir()

	got, err := (osLocalProcessRunner{}).Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	gotInfo, gotErr := os.Stat(got.Stdout)
	wantInfo, wantErr := os.Stat(request.Dir)
	if got.ExitCode != 0 || gotErr != nil || wantErr != nil || !os.SameFile(gotInfo, wantInfo) {
		t.Fatalf("unexpected working directory result: %#v want=%q", got, request.Dir)
	}
}

func TestLocalProcessRunnerReturnsStructuredTimeout(t *testing.T) {
	request := localProcessHelperRequest(t, "sleep")
	request.Timeout = 30 * time.Millisecond

	got, err := (osLocalProcessRunner{}).Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !got.TimedOut || got.ExitCode != -1 {
		t.Fatalf("expected timeout result, got %#v", got)
	}
}

func TestLocalProcessRunnerPropagatesParentCancellation(t *testing.T) {
	request := localProcessHelperRequest(t, "sleep")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := (osLocalProcessRunner{}).Run(ctx, request)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestLocalProcessRunnerStopsAfterSuccessFileIsStable(t *testing.T) {
	successFile := filepath.Join(t.TempDir(), "success.bin")
	request := localProcessHelperRequest(t, "write-file")
	request.Env = append(request.Env, "LOCAL_PROCESS_SUCCESS_FILE="+successFile)
	request.SuccessFile = successFile
	request.SuccessFileMinSize = 8
	request.Timeout = 2 * time.Second

	started := time.Now()
	got, err := (osLocalProcessRunner{}).Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CompletedByFile || got.ExitCode != 0 {
		t.Fatalf("expected success-file completion, got %#v", got)
	}
	if elapsed := time.Since(started); elapsed >= request.Timeout {
		t.Fatalf("runner waited for process timeout despite success file: %v", elapsed)
	}
}

func TestLocalProcessRunnerAcceptsCompletedSuccessFileBeforeNonZeroExit(t *testing.T) {
	successFile := filepath.Join(t.TempDir(), "success.bin")
	request := localProcessHelperRequest(t, "write-file-exit-error")
	request.Env = append(request.Env, "LOCAL_PROCESS_SUCCESS_FILE="+successFile)
	request.SuccessFile = successFile
	request.SuccessFileMinSize = 8
	request.Timeout = 2 * time.Second

	got, err := (osLocalProcessRunner{}).Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CompletedByFile || got.ExitCode != 0 || got.TimedOut {
		t.Fatalf("completed output file must win over a later non-zero process exit: %#v", got)
	}
}
