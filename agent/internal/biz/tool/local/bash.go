package local

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type localBashTool struct {
	schema         ToolDef
	cfg            *Config
	resolver       localExecutableResolver
	runner         localProcessRunner
	name           string
	powerShellOnly bool
}

// localBashPythonEnvironment makes a bare `python` command inside a skill use
// the same direct Python executable used for running Python. This keeps
// existing skill commands portable without rewriting each command line.
// Launcher-style executables such as Windows `py -3` have no stable directory
// to prepend, so they deliberately retain the shell's normal PATH behavior.
func localBashPythonEnvironment(resolver localExecutableResolver, cfg *Config) []string {
	env := localBashEnvironment(cfg)
	python, err := resolver.resolvePython(toolRuntimeExecutables(cfg).Python)
	if err != nil || len(python.PrefixArgs) > 0 {
		return env
	}
	dir := filepath.Dir(python.Path)
	if dir == "." || strings.TrimSpace(dir) == "" {
		return env
	}
	return append(env, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func localBashEnvironment(cfg *Config) []string {
	env := localPptToolEnvironment(cfg)
	env = append(env,
		"NO_COLOR=1",
		"PAGER=cat",
		"GIT_PAGER=cat",
		"PYTHONIOENCODING=utf-8",
	)
	return env
}

func NewLocalBashTool(schema ToolDef, cfg *Config) Tool {
	return newLocalShellTool("bash", false, schema, cfg, newLocalExecutableResolver(), osLocalProcessRunner{})
}

// NewLocalPowerShellTool exposes a Windows-native tool name so the model does
// not have to infer that `bash` is actually PowerShell on that platform.
func NewLocalPowerShellTool(schema ToolDef, cfg *Config) Tool {
	return newLocalShellTool("powershell", true, schema, cfg, newLocalExecutableResolver(), osLocalProcessRunner{})
}

func newLocalBashTool(schema ToolDef, cfg *Config, resolver localExecutableResolver, runner localProcessRunner) Tool {
	return newLocalShellTool("bash", false, schema, cfg, resolver, runner)
}

func newLocalShellTool(name string, powerShellOnly bool, schema ToolDef, cfg *Config, resolver localExecutableResolver, runner localProcessRunner) Tool {
	if runner == nil {
		runner = osLocalProcessRunner{}
	}
	return &localBashTool{schema: schema, cfg: cfg, resolver: resolver, runner: runner, name: name, powerShellOnly: powerShellOnly}
}

func (t *localBashTool) Name() string {
	return t.name
}

func (t *localBashTool) Schema() ToolDef {
	return t.schema
}

func (t *localBashTool) Execute(ctx context.Context, invocation ToolInvocation) (ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}
	args, err := decodeLocalToolArgs(invocation)
	if err != nil {
		return localErrorResult(t.Name(), err), nil
	}
	command := localStringArg(args, "command")
	if strings.TrimSpace(command) == "" {
		return localErrorResult(t.Name(), fmt.Errorf("command is required")), nil
	}
	localContext, err := localToolContext(invocation)
	if err != nil {
		return localErrorResult(t.Name(), err), nil
	}
	workingDir := localContext.Workspace
	if requested := localStringArg(args, "working_dir", "workdir"); strings.TrimSpace(requested) != "" {
		workingDir, err = resolveLocalWorkspacePath(localContext.Workspace, requested)
		if err != nil {
			return localErrorResult(t.Name(), err), nil
		}
		info, statErr := os.Stat(workingDir)
		if statErr != nil {
			return localErrorResult(t.Name(), fmt.Errorf("working directory is unavailable: %w", statErr)), nil
		}
		if !info.IsDir() {
			return localErrorResult(t.Name(), fmt.Errorf("working directory is not a directory: %s", requested)), nil
		}
	}

	shell, err := t.resolver.resolveShell(toolRuntimeExecutables(t.cfg).Shell)
	if err != nil {
		return localUnavailableResult(t.Name(), err), nil
	}
	if t.powerShellOnly && !isPowerShellExecutable(shell.Path) {
		return localUnavailableResult(t.Name(), fmt.Errorf("PowerShell is unavailable; resolved shell %q is not pwsh or powershell", shell.Path)), nil
	}
	command = replaceLocalPathReferences(command, localContext.Workspace, t.cfg.SkillsRoot)
	timeout := localToolTimeout(invocation, localIntArg(args, "timeout", 120), 5*time.Second, 10*time.Minute)

	// Policy check runs before any process spawn. Escalation follows the Codex
	// shape: the model must name a wider permission mode and explain why. The
	// host approval layer can accept or reject that request; the local tool
	// never prompts the user directly.
	policy := t.cfg.BashPolicy
	decision := policy.Check(command)
	sandboxPermissions, justification, escalationErr := localBashEscalation(args)
	if escalationErr != nil {
		return localErrorResult(t.Name(), escalationErr), nil
	}
	canEscalate := sandboxPermissions == "danger-full-access"
	if !decision.Allowed && canEscalate {
		decision = decision.WithEscalation()
		decision.Allowed = true
	}
	if !decision.Allowed {
		return ToolResult{Value: map[string]any{
			"tool":    t.Name(),
			"ok":      false,
			"blocked": true,
			"reason":  decision.Reason,
			"hint":    "this command is blocked by the bash policy. Pick a safer command, or request sandbox_permissions=danger-full-access together with a one-line justification so the host approval layer can decide.",
			"command": command,
		}, IsError: true}, nil
	}

	if localBoolArg(args, "run_in_background") {
		return t.startBackground(ctx, workingDir, shell, command, localBashPythonEnvironment(t.resolver, t.cfg))
	}

	processArgs := append([]string{}, shell.PrefixArgs...)
	processArgs = append(processArgs, commandForShell(shell, command))
	processResult, err := t.runner.Run(ctx, localProcessRequest{
		Path:           shell.Path,
		Args:           processArgs,
		Dir:            workingDir,
		Env:            localBashPythonEnvironment(t.resolver, t.cfg),
		Timeout:        timeout,
		StdoutMaxBytes: defaultLocalOutputLimitBytes,
		StderrMaxBytes: defaultLocalOutputLimitBytes,
	})
	if err != nil {
		return localErrorResult(t.Name(), err), nil
	}
	stdout := strings.TrimSpace(processResult.Stdout)
	stderr := strings.TrimSpace(processResult.Stderr)
	ok := processResult.ExitCode == 0 && !processResult.TimedOut
	value := map[string]any{
		"tool":      t.Name(),
		"ok":        ok,
		"exit_code": processResult.ExitCode,
		"stdout":    stdout,
		"stderr":    stderr,
		"result":    stdout,
	}
	if processResult.StdoutTruncated {
		value["stdout_truncated"] = true
		value["stdout_path"] = processResult.StdoutPath
		value["stdout_bytes"] = processResult.StdoutBytes
	}
	if processResult.StderrTruncated {
		value["stderr_truncated"] = true
		value["stderr_path"] = processResult.StderrPath
		value["stderr_bytes"] = processResult.StderrBytes
	}
	if decision.Escalated {
		value["policy_escalated"] = true
		if sandboxPermissions != "" {
			value["sandbox_permissions"] = sandboxPermissions
		}
		if justification != "" {
			value["justification"] = justification
		}
	}
	if processResult.TimedOut {
		value["message"] = fmt.Sprintf("timed out after %s; partial output is preserved", timeout)
		value["timed_out"] = true
	} else if !ok {
		value["message"] = fmt.Sprintf("command exited with code %d", processResult.ExitCode)
	}
	// A non-zero exit status is a normal command result. Only spawn/abort
	// failures are tool errors, matching Codex and DSH behavior.
	return ToolResult{Value: value}, nil
}

// startBackground spawns the shell command in detached mode and returns a
// job_id immediately. The model reads output / kills the process via the
// bash_job tool, mirroring dsh `tool-bash` background jobs + ctx.jobs.
func (t *localBashTool) startBackground(
	ctx context.Context,
	workingDir string,
	shell localExecutable,
	command string,
	env []string,
) (ToolResult, error) {
	jobDir, err := ensureJobDir(workingDir)
	if err != nil {
		return localErrorResult(t.Name(), fmt.Errorf("create job dir: %w", err)), nil
	}
	jobID := newJobID()
	stdoutPath := filepath.Join(jobDir, jobID+".stdout")
	stderrPath := filepath.Join(jobDir, jobID+".stderr")

	processArgs := append([]string{}, shell.PrefixArgs...)
	processArgs = append(processArgs, commandForShell(shell, command))
	result, err := t.runner.Run(ctx, localProcessRequest{
		Path:       shell.Path,
		Args:       processArgs,
		Dir:        workingDir,
		Env:        env,
		Timeout:    0,
		Detach:     true,
		OutputPath: stdoutPath,
		ErrorPath:  stderrPath,
		JobID:      jobID,
	})
	if err != nil {
		return localErrorResult(t.Name(), fmt.Errorf("spawn background job: %w", err)), nil
	}
	return ToolResult{Value: map[string]any{
		"tool":        t.Name(),
		"ok":          true,
		"background":  true,
		"job_id":      jobID,
		"pid":         result.PID,
		"stdout_path": stdoutPath,
		"stderr_path": stderrPath,
		"job_dir":     jobDir,
		"hint":        "use the bash_job tool (action=read|kill|status, job_id=...) to interact with this job",
	}}, nil
}

func localBashEscalation(args map[string]any) (string, string, error) {
	mode := strings.ToLower(strings.TrimSpace(localStringArg(args, "sandbox_permissions")))
	justification := strings.TrimSpace(localStringArg(args, "justification"))
	if mode == "" && justification != "" {
		return "", "", fmt.Errorf("justification is only valid together with sandbox_permissions")
	}
	if mode != "" && justification == "" {
		return "", "", fmt.Errorf("sandbox_permissions requires a justification")
	}
	switch mode {
	case "", "workspace-write", "danger-full-access":
		return mode, justification, nil
	default:
		return "", "", fmt.Errorf("unsupported sandbox_permissions %q (use workspace-write or danger-full-access)", mode)
	}
}

func ensureJobDir(workingDir string) (string, error) {
	dir := filepath.Join(workingDir, ".bash_jobs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func newJobID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// Fallback to timestamp if entropy is unavailable; collision is
		// acceptable because the directory is per-workspace and short-lived.
		return fmt.Sprintf("job-%d", time.Now().UnixNano())
	}
	return "job-" + hex.EncodeToString(buf[:])
}
