package local

import (
	"context"
	"runtime"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLocalBashBuildsUnixLoginCommand(t *testing.T) {
	workspace := t.TempDir()
	cfg := &Config{ToolRuntime: &ToolRuntimeConfig{Executables: LocalExecutableConfig{Shell: "/bin/zsh"}}}
	resolver := resolverForTest("darwin", nil, nil, map[string]bool{"/bin/zsh": true})
	runner := &recordingLocalProcessRunner{result: localProcessResult{Stdout: "done\n", ExitCode: 0}}
	tool := newLocalBashTool(ToolDef{}, cfg, resolver, runner)

	result, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: workspace,
		Args:      json.RawMessage(`{"command":"pwd"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || result.Value["ok"] != true || result.Value["result"] != "done" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if runner.request.Path != "/bin/zsh" || !reflect.DeepEqual(runner.request.Args, []string{"-lc", "pwd"}) || runner.request.Dir != workspace {
		t.Fatalf("unexpected Unix shell request: %#v", runner.request)
	}
}

func TestLocalBashPrependsResolvedPythonDirectory(t *testing.T) {
	workspace := t.TempDir()
	python := filepath.Join(workspace, ".tools", "venv", "bin", "python")
	resolver := resolverForTest("darwin", map[string]string{
		"AGENT_PYTHON_BIN": python,
	}, nil, map[string]bool{"/bin/zsh": true, python: true})
	runner := &recordingLocalProcessRunner{result: localProcessResult{ExitCode: 0}}
	tool := newLocalBashTool(ToolDef{}, &Config{ToolRuntime: &ToolRuntimeConfig{Executables: LocalExecutableConfig{Shell: "/bin/zsh"}}}, resolver, runner)

	_, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: workspace,
		Args:      json.RawMessage(`{"command":"python skill.py"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "PATH=" + filepath.Dir(python) + string(os.PathListSeparator)
	foundPath := false
	for _, entry := range runner.request.Env {
		if strings.HasPrefix(entry, want) {
			foundPath = true
			break
		}
	}
	if !foundPath {
		t.Fatalf("unexpected Python PATH override: %#v", runner.request.Env)
	}
}

func TestLocalBashPassesPptToolEnvironment(t *testing.T) {
	workspace := t.TempDir()
	cfg := &Config{
		ToolRuntime: &ToolRuntimeConfig{Executables: LocalExecutableConfig{Shell: "/bin/zsh"}},
		PptTools: PptToolsConfig{
			BaseURL: "https://ppt.example",
			APIPath: "api/agent/tool_call",
			HostPin: "ppt.example=127.0.0.1",
		},
	}
	resolver := resolverForTest("darwin", nil, nil, map[string]bool{"/bin/zsh": true})
	runner := &recordingLocalProcessRunner{result: localProcessResult{ExitCode: 0}}
	tool := newLocalBashTool(ToolDef{}, cfg, resolver, runner)

	_, err := tool.Execute(context.Background(), ToolInvocation{Workspace: workspace, Args: json.RawMessage(`{"command":"echo ok"}`)})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"PPT_TOOL_API_BASE=https://ppt.example",
		"BACKEND_TOOL_BASE=https://ppt.example",
		"CREATIVE_RENDER_API_URL=https://ppt.example/api/agent/tool_call",
		"PPT_TOOL_HOST_IP=ppt.example=127.0.0.1",
		"CREATIVE_RENDER_HOST_IP=ppt.example=127.0.0.1",
	} {
		if !containsLocalEnv(runner.request.Env, want) {
			t.Fatalf("missing %q in environment: %#v", want, runner.request.Env)
		}
	}
}

func TestLocalBashBuildsWindowsPowerShellCommand(t *testing.T) {
	// 这条断言的是"构造 Windows PowerShell 命令行"这一路径：
	// 前缀 -NoLogo/-NoProfile/-NonInteractive/-Command 加上 UTF-8 前导。
	// 构造过程会用宿主机的 runtime.GOOS 判断是否拼前导，在 Linux 上跑会得到
	// 不带前导的裸 "Get-Location"，属于平台差异而非逻辑回归，
	// 因此只在 Windows 上断言这一路径。
	if runtime.GOOS != "windows" {
		t.Skipf("Windows-only command construction; host=%s", runtime.GOOS)
	}
	workspace := t.TempDir()
	path := `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`
	cfg := &Config{ToolRuntime: &ToolRuntimeConfig{Executables: LocalExecutableConfig{Shell: path}}}
	resolver := resolverForTest("windows", nil, nil, map[string]bool{path: true})
	runner := &recordingLocalProcessRunner{result: localProcessResult{ExitCode: 0}}
	tool := newLocalBashTool(ToolDef{}, cfg, resolver, runner)

	_, err := tool.Execute(context.Background(), ToolInvocation{Workspace: workspace, Args: json.RawMessage(`{"command":"Get-Location"}`)})
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command"}
	if runner.request.Path != path || len(runner.request.Args) != 5 || !reflect.DeepEqual(runner.request.Args[:4], wantPrefix) {
		t.Fatalf("unexpected Windows shell request: %#v", runner.request)
	}
	if !strings.Contains(runner.request.Args[4], "[Console]::OutputEncoding") || !strings.HasSuffix(runner.request.Args[4], "Get-Location") {
		t.Fatalf("Windows command missing UTF-8 preamble: %q", runner.request.Args[4])
	}
}

func TestLocalPowerShellToolUsesPowerShellAndKeepsName(t *testing.T) {
	// 同上：这条走的是 Windows 下的 shell 解析与 PowerShell 启动路径，
	// 需要宿主上确实存在 PowerShell。Linux 上报 "PowerShell is unavailable"
	// 是正确行为，因此该路径只在 Windows 上断言。
	if runtime.GOOS != "windows" {
		t.Skipf("Windows-only PowerShell path; host=%s", runtime.GOOS)
	}
	workspace := t.TempDir()
	path := `C:\Program Files\PowerShell\7\pwsh.exe`
	cfg := &Config{ToolRuntime: &ToolRuntimeConfig{Executables: LocalExecutableConfig{Shell: path}}}
	resolver := resolverForTest("windows", nil, nil, map[string]bool{path: true})
	runner := &recordingLocalProcessRunner{result: localProcessResult{ExitCode: 0}}
	tool := newLocalShellTool("powershell", true, ToolDef{}, cfg, resolver, runner)

	if tool.Name() != "powershell" {
		t.Fatalf("unexpected tool name %q", tool.Name())
	}
	result, err := tool.Execute(context.Background(), ToolInvocation{Workspace: workspace, Args: json.RawMessage(`{"command":"Get-Location"}`)})
	if err != nil || result.IsError {
		t.Fatalf("powershell tool failed: result=%#v err=%v", result, err)
	}
	if runner.request.Path != path || len(runner.request.Args) != 5 || !strings.Contains(runner.request.Args[4], "Get-Location") {
		t.Fatalf("unexpected powershell request: %#v", runner.request)
	}
}

func TestLocalPowerShellToolRejectsNonPowerShellShell(t *testing.T) {
	cfg := &Config{ToolRuntime: &ToolRuntimeConfig{Executables: LocalExecutableConfig{Shell: "/bin/bash"}}}
	tool := newLocalShellTool("powershell", true, ToolDef{}, cfg, resolverForTest("darwin", nil, nil, map[string]bool{"/bin/bash": true}), &recordingLocalProcessRunner{})
	result, err := tool.Execute(context.Background(), ToolInvocation{Workspace: t.TempDir(), Args: json.RawMessage(`{"command":"Get-Location"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || result.Value["code"] != "unavailable" {
		t.Fatalf("expected unavailable powershell result, got %#v", result)
	}
}

func TestLocalBashUsesWorkspaceRelativeWorkingDirectoryAndMapsMntData(t *testing.T) {
	workspace := t.TempDir()
	workdir := filepath.Join(workspace, "nested")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{ToolRuntime: &ToolRuntimeConfig{Executables: LocalExecutableConfig{Shell: "/bin/bash"}}}
	resolver := resolverForTest("darwin", nil, nil, map[string]bool{"/bin/bash": true})
	runner := &recordingLocalProcessRunner{result: localProcessResult{ExitCode: 0}}
	tool := newLocalBashTool(ToolDef{}, cfg, resolver, runner)

	_, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: workspace,
		Args:      json.RawMessage(`{"command":"ls /mnt/data/report","working_dir":"local://nested"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if runner.request.Dir != workdir {
		t.Fatalf("unexpected working directory: %q", runner.request.Dir)
	}
	wantPath := filepath.ToSlash(filepath.Join(workspace, "report"))
	if !strings.Contains(runner.request.Args[len(runner.request.Args)-1], wantPath) {
		t.Fatalf("command did not map /mnt/data: %#v", runner.request.Args)
	}
}

func TestLocalBashMapsBareMntDataRoot(t *testing.T) {
	workspace := t.TempDir()
	cfg := &Config{ToolRuntime: &ToolRuntimeConfig{Executables: LocalExecutableConfig{Shell: "/bin/bash"}}}
	resolver := resolverForTest("darwin", nil, nil, map[string]bool{"/bin/bash": true})
	runner := &recordingLocalProcessRunner{result: localProcessResult{ExitCode: 0}}
	tool := newLocalBashTool(ToolDef{}, cfg, resolver, runner)

	_, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: workspace,
		Args:      json.RawMessage(`{"command":"ls /mnt/data && test -d /mnt/data/result && echo /mnt/database"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	command := runner.request.Args[len(runner.request.Args)-1]
	if !strings.Contains(command, "ls "+filepath.ToSlash(workspace)+" &&") ||
		!strings.Contains(command, filepath.ToSlash(filepath.Join(workspace, "result"))) ||
		!strings.Contains(command, "/mnt/database") {
		t.Fatalf("unexpected mapped command: %q", command)
	}
}

func TestLocalBashMapsProductionSkillsRoot(t *testing.T) {
	workspace := t.TempDir()
	skillsRoot := filepath.Join(t.TempDir(), "skills")
	cfg := &Config{
		SkillsRoot:  skillsRoot,
		ToolRuntime: &ToolRuntimeConfig{Executables: LocalExecutableConfig{Shell: "/bin/bash"}},
	}
	resolver := resolverForTest("darwin", nil, nil, map[string]bool{"/bin/bash": true})
	runner := &recordingLocalProcessRunner{result: localProcessResult{ExitCode: 0}}
	tool := newLocalBashTool(ToolDef{}, cfg, resolver, runner)

	_, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: workspace,
		Args:      json.RawMessage(`{"command":"ls /skills/ppt-maker/references /skills/ppt-maker/scripts && echo /skills-not-a-root"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	command := runner.request.Args[len(runner.request.Args)-1]
	if !strings.Contains(command, filepath.ToSlash(filepath.Join(skillsRoot, "ppt-maker", "references"))) ||
		!strings.Contains(command, filepath.ToSlash(filepath.Join(skillsRoot, "ppt-maker", "scripts"))) ||
		strings.Contains(command, filepath.ToSlash(skillsRoot)+"-not-a-root") {
		t.Fatalf("unexpected mapped command: %q", command)
	}
}

func TestLocalBashRejectsWorkingDirectoryOutsideWorkspace(t *testing.T) {
	cfg := &Config{ToolRuntime: &ToolRuntimeConfig{Executables: LocalExecutableConfig{Shell: "/bin/bash"}}}
	resolver := resolverForTest("darwin", nil, nil, map[string]bool{"/bin/bash": true})
	runner := &recordingLocalProcessRunner{}
	tool := newLocalBashTool(ToolDef{}, cfg, resolver, runner)

	result, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: t.TempDir(),
		Args:      json.RawMessage(`{"command":"pwd","working_dir":"../outside"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Post-loosen: paths outside the workspace are allowed; the only failure
	// here is that the directory does not exist.
	if !result.IsError || !strings.Contains(result.Value["error"].(string), "working directory is unavailable") {
		t.Fatalf("expected 'unavailable' error for missing dir, got %#v", result.Value)
	}
	if runner.request.Path != "" {
		t.Fatal("runner should not start for invalid working directory")
	}
}

func TestLocalBashPreservesFailureAndTimeoutResults(t *testing.T) {
	cfg := &Config{ToolRuntime: &ToolRuntimeConfig{Executables: LocalExecutableConfig{Shell: "/bin/bash"}}}
	resolver := resolverForTest("darwin", nil, nil, map[string]bool{"/bin/bash": true})
	for name, processResult := range map[string]localProcessResult{
		"failure": {Stdout: "partial", Stderr: "failed", ExitCode: 7},
		"timeout": {Stdout: "progress", ExitCode: -1, TimedOut: true},
	} {
		runner := &recordingLocalProcessRunner{result: processResult}
		tool := newLocalBashTool(ToolDef{}, cfg, resolver, runner)
		result, err := tool.Execute(context.Background(), ToolInvocation{Workspace: t.TempDir(), Args: json.RawMessage(`{"command":"work"}`)})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if result.Value["ok"] != false || result.Value["exit_code"] != processResult.ExitCode || result.IsError {
			t.Fatalf("%s: unexpected result %#v", name, result.Value)
		}
		if !processResult.TimedOut && result.Value["message"] != fmt.Sprintf("command exited with code %d", processResult.ExitCode) {
			t.Fatalf("%s: missing actionable exit error: %#v", name, result.Value)
		}
		if processResult.TimedOut && (result.Value["timed_out"] != true || runner.request.Timeout < 5*time.Second) {
			t.Fatalf("%s: timeout fields missing: %#v request=%#v", name, result.Value, runner.request)
		}
	}
}

func TestLocalBashRequiresStructuredPermissionForDeniedCommand(t *testing.T) {
	workspace := t.TempDir()
	cfg := &Config{
		BashPolicy: BashPolicy{DenyCommands: []string{"shutdown"}},
		ToolRuntime: &ToolRuntimeConfig{
			Executables: LocalExecutableConfig{Shell: "/bin/bash"},
		},
	}
	resolver := resolverForTest("darwin", nil, nil, map[string]bool{"/bin/bash": true})
	runner := &recordingLocalProcessRunner{result: localProcessResult{ExitCode: 0}}
	tool := newLocalBashTool(ToolDef{}, cfg, resolver, runner)

	blocked, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: workspace,
		Args:      json.RawMessage(`{"command":"shutdown now","sandbox_permissions":"danger-full-access"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !blocked.IsError || !strings.Contains(blocked.Value["error"].(string), "requires a justification") {
		t.Fatalf("expected missing-justification error, got %#v", blocked)
	}

	legacy, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: workspace,
		Args:      json.RawMessage(`{"command":"shutdown now","escalate":true}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !legacy.IsError || legacy.Value["blocked"] != true {
		t.Fatalf("legacy escalate bypass must not override policy: %#v", legacy)
	}

	allowed, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: workspace,
		Args:      json.RawMessage(`{"command":"shutdown now","sandbox_permissions":"danger-full-access","justification":"explicit host approval request"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if allowed.IsError || allowed.Value["policy_escalated"] != true || allowed.Value["sandbox_permissions"] != "danger-full-access" {
		t.Fatalf("expected structured escalation, got %#v", allowed)
	}
	if runner.request.Path != "/bin/bash" {
		t.Fatalf("escalated command did not run: %#v", runner.request)
	}
}

func TestLocalBashWorkspaceWriteDoesNotOverrideSystemDeny(t *testing.T) {
	cfg := &Config{
		BashPolicy: BashPolicy{DenyCommands: []string{"shutdown"}},
		ToolRuntime: &ToolRuntimeConfig{
			Executables: LocalExecutableConfig{Shell: "/bin/bash"},
		},
	}
	tool := newLocalBashTool(ToolDef{}, cfg, resolverForTest("darwin", nil, nil, map[string]bool{"/bin/bash": true}), &recordingLocalProcessRunner{})
	result, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: t.TempDir(),
		Args:      json.RawMessage(`{"command":"shutdown now","sandbox_permissions":"workspace-write","justification":"try wider access"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || result.Value["blocked"] != true {
		t.Fatalf("workspace-write must not widen a system deny: %#v", result)
	}
}

func TestLocalBashReturnsUnavailableWithoutShell(t *testing.T) {
	tool := newLocalBashTool(ToolDef{}, &Config{}, resolverForTest("linux", nil, nil, nil), &recordingLocalProcessRunner{})

	result, err := tool.Execute(context.Background(), ToolInvocation{Workspace: t.TempDir(), Args: json.RawMessage(`{"command":"pwd"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || result.Value["code"] != "unavailable" {
		t.Fatalf("unexpected unavailable result: %#v", result)
	}
}

func TestLocalBashRejectsEmptyCommand(t *testing.T) {
	tool := newLocalBashTool(ToolDef{}, &Config{}, resolverForTest("linux", nil, nil, nil), &recordingLocalProcessRunner{})

	result, err := tool.Execute(context.Background(), ToolInvocation{Workspace: t.TempDir(), Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || result.Value["tool"] != "bash" {
		t.Fatalf("unexpected empty command result: %#v", result)
	}
}
