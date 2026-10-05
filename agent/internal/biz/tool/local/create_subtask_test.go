package local

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type recordingSubtaskRunner struct {
	request localProcessRequest
	result  localProcessResult
	inspect func(localProcessRequest)
}

func (r *recordingSubtaskRunner) Run(_ context.Context, request localProcessRequest) (localProcessResult, error) {
	r.request = request
	if r.inspect != nil {
		r.inspect(request)
	}
	return r.result, nil
}

func subtaskRequestValue(t *testing.T, args []string, name string) string {
	t.Helper()
	for index := 0; index+1 < len(args); index++ {
		if args[index] == name {
			return args[index+1]
		}
	}
	t.Fatalf("subtask request is missing %s: %#v", name, args)
	return ""
}

func subtaskRequestHasEnv(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestLocalCreateSubtaskUsesNativeAgentAndPreservesSessionProtocol(t *testing.T) {
	repoRoot := t.TempDir()
	configPath := filepath.Join(repoRoot, "config", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	templateDir := filepath.Join(repoRoot, "config", "modules", "subtask", "user")
	if err := os.MkdirAll(templateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(templateDir, "zh.md"), []byte("任务：{{ Task|safe }}"), 0o600); err != nil {
		t.Fatal(err)
	}

	parentSession := filepath.Join(repoRoot, "frontend", "sessions", "chat-1", "chat-1.json")
	if err := os.MkdirAll(filepath.Dir(parentSession), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{RepoRoot: repoRoot, ConfigPath: configPath, BuildSubtaskPrompt: func(task string) (string, error) {
		return "任务：" + task + "\n\n请根据上面的被委派任务执行工作，完成后在 <subtask_result> 中输出结果。", nil
	}}
	runner := &recordingSubtaskRunner{result: localProcessResult{ExitCode: 0}}
	var userPrompt string
	runner.inspect = func(request localProcessRequest) {
		userFile := subtaskRequestValue(t, request.Args, "-UserOverrideFile")
		data, err := os.ReadFile(userFile)
		if err != nil {
			t.Errorf("read delegated prompt: %v", err)
			return
		}
		userPrompt = string(data)
		sessionFile := subtaskRequestValue(t, request.Args, "-SessionFile")
		session := map[string]any{"messages": []map[string]any{
			{"role": "system", "content": "hidden"},
			{"role": "assistant", "content": "<subtask_result>done</subtask_result>", "duration_ms": 12, "usage": map[string]any{"prompt_tokens": 7, "completion_tokens": 3}},
		}}
		data, err = json.Marshal(session)
		if err != nil {
			t.Errorf("marshal session: %v", err)
			return
		}
		if err := os.WriteFile(sessionFile, data, 0o600); err != nil {
			t.Errorf("write child session: %v", err)
		}
		_, _ = fmt.Fprintln(request.StdoutWriter, `{"type":"assistant","content":"working"}`)
	}
	tool := newLocalCreateSubtaskTool(ToolDef{}, cfg, runner, func(*Config) (string, error) {
		return "/native/agent-loop", nil
	})

	result, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace:   filepath.Join(repoRoot, "workspace"),
		SessionFile: parentSession,
		Timeout:     45 * time.Second,
		Args:        json.RawMessage(`{"title":"资料整理","goal":"整理已有资料","plan":"阅读并提炼","resume_session":"/previous/subtask.json","background":false}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || result.Value["ok"] != true || result.Value["title"] != "资料整理" {
		t.Fatalf("unexpected local subtask result: %#v", result)
	}
	if runner.request.Path != "/native/agent-loop" || runner.request.Dir != repoRoot || runner.request.Timeout != 45*time.Second {
		t.Fatalf("unexpected native agent request: %#v", runner.request)
	}
	if got := subtaskRequestValue(t, runner.request.Args, "-ConfigPath"); got != configPath {
		t.Fatalf("child did not inherit config path: %q", got)
	}
	if got := subtaskRequestValue(t, runner.request.Args, "-UseMock"); got != "false" {
		t.Fatalf("child did not inherit mock mode: %q", got)
	}
	if got := subtaskRequestValue(t, runner.request.Args, "-ResumeSessionFile"); got != "/previous/subtask.json" {
		t.Fatalf("child did not receive resume session: %q", got)
	}
	if !subtaskRequestHasEnv(runner.request.Env, "WORKSPACE_DIR="+filepath.Join(repoRoot, "workspace")) ||
		!subtaskRequestHasEnv(runner.request.Env, "AGENT_REPO_ROOT="+repoRoot) ||
		!subtaskRequestHasEnv(runner.request.Env, "AGENT_RUN_KIND=subtask") ||
		!subtaskRequestHasEnv(runner.request.Env, "AGENT_SUBTASK_TITLE=资料整理") {
		t.Fatalf("child did not receive required environment: %#v", runner.request.Env)
	}
	if !strings.Contains(userPrompt, "整理已有资料") || !strings.Contains(userPrompt, "<subtask_result>") {
		t.Fatalf("unexpected delegated user prompt: %q", userPrompt)
	}
	sessionFile, _ := result.Value["session"].(string)
	if !strings.Contains(sessionFile, "chat-1__资料整理") {
		t.Fatalf("child session was not created as a branch: %q", sessionFile)
	}
	if result.Value["branch_session"] != "chat-1__资料整理" || result.Value["parent_session"] != "chat-1" {
		t.Fatalf("branch metadata missing from result: %#v", result.Value)
	}
	streamData, readErr := os.ReadFile(sessionFile + ".stream")
	if readErr != nil || !strings.Contains(string(streamData), `"type":"subtask_start"`) || !strings.Contains(string(streamData), `"type":"assistant"`) {
		t.Fatalf("child stream protocol was not preserved: data=%q err=%v", streamData, readErr)
	}
	stats, ok := result.Value["agent_stats"].(map[string]any)
	if !ok || stats["duration_ms"] != int64(12) || stats["prompt_tokens"] != int64(7) || stats["completion_tokens"] != int64(3) {
		t.Fatalf("unexpected child stats: %#v", result.Value["agent_stats"])
	}
}

func TestLocalCreateSubtaskInheritsEnabledMockMode(t *testing.T) {
	repoRoot := t.TempDir()
	configPath := filepath.Join(repoRoot, "config.json")
	cfg := &Config{RepoRoot: repoRoot, ConfigPath: configPath, UseMock: true}
	runner := &recordingSubtaskRunner{result: localProcessResult{ExitCode: 0}}
	tool := newLocalCreateSubtaskTool(ToolDef{}, cfg, runner, func(*Config) (string, error) {
		return "/native/agent-loop", nil
	})
	_, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace: t.TempDir(),
		Args:      json.RawMessage(`{"goal":"verify mock inheritance","background":false}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := subtaskRequestValue(t, runner.request.Args, "-UseMock"); got != "true" {
		t.Fatalf("child did not inherit enabled mock mode: %q", got)
	}
}

func TestLocalCreateSubtaskPassesPptToolEnvironment(t *testing.T) {
	repoRoot := t.TempDir()
	cfg := &Config{
		RepoRoot: repoRoot,
		PptTools: PptToolsConfig{
			BaseURL: "https://ppt.example/",
			APIPath: "/api/agent/tool_call",
			HostPin: "ppt.example=127.0.0.1",
		},
	}
	runner := &recordingSubtaskRunner{result: localProcessResult{ExitCode: 0}}
	tool := newLocalCreateSubtaskTool(ToolDef{}, cfg, runner, func(*Config) (string, error) {
		return "/native/agent-loop", nil
	})
	_, err := tool.Execute(context.Background(), ToolInvocation{Workspace: t.TempDir(), Args: json.RawMessage(`{"goal":"verify PPT environment","background":false}`)})
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
		if !subtaskRequestHasEnv(runner.request.Env, want) {
			t.Fatalf("missing %q in environment: %#v", want, runner.request.Env)
		}
	}
}

func TestLocalCreateSubtaskBackgroundDetachesAndWritesManifest(t *testing.T) {
	repoRoot := t.TempDir()
	cfg := &Config{RepoRoot: repoRoot, BuildSubtaskPrompt: func(task string) (string, error) {
		return task, nil
	}}
	runner := &recordingSubtaskRunner{result: localProcessResult{Detached: true, PID: 4321}}
	tool := newLocalCreateSubtaskTool(ToolDef{}, cfg, runner, func(*Config) (string, error) {
		return "/native/agent-loop", nil
	})
	parentSession := filepath.Join(repoRoot, "memory", "sessions", "day", "day.json")
	if err := os.MkdirAll(filepath.Dir(parentSession), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := tool.Execute(context.Background(), ToolInvocation{
		Workspace:   filepath.Join(repoRoot, "workspace"),
		SessionFile: parentSession,
		Args:        json.RawMessage(`{"title":"后台调研","goal":"收集资料"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || result.Value["status"] != "running" || result.Value["background"] != true {
		t.Fatalf("unexpected background result: %#v", result)
	}
	if !runner.request.Detach || runner.request.OutputPath == "" || runner.request.ErrorPath == "" || runner.request.JobID == "" {
		t.Fatalf("background request was not detached: %#v", runner.request)
	}
	manifestPath, _ := result.Value["manifest_path"].(string)
	raw, readErr := os.ReadFile(manifestPath)
	if readErr != nil || !strings.Contains(string(raw), `"status": "running"`) || !strings.Contains(string(raw), `"pid": 4321`) {
		t.Fatalf("background manifest missing: path=%q err=%v data=%s", manifestPath, readErr, raw)
	}
	promptPath, _ := result.Value["prompt_path"].(string)
	userFile := subtaskRequestValue(t, runner.request.Args, "-UserOverrideFile")
	if promptPath != userFile {
		t.Fatalf("prompt path result mismatch: result=%q request=%q", promptPath, userFile)
	}
	if _, statErr := os.Stat(userFile); statErr != nil {
		t.Fatalf("background prompt was not persisted: %v", statErr)
	}
}

func TestLocalSubtaskResultReturnsOnlyFinalDelivery(t *testing.T) {
	result := localSubtaskResult("结果瘦身", writeLocalSubtaskSession(t, []map[string]any{
		{"role": "system", "content": "hidden"},
		{"role": "assistant", "content": "tool progress", "duration_ms": 4.0},
		{"role": "assistant", "content": "<report>intermediate delivery</report>", "duration_ms": 6.0},
		{"role": "assistant", "content": "<report><subtask_result>final delivery</subtask_result></report>", "duration_ms": 8.0},
	}))
	if result.IsError || result.Value["result"] != "<report><subtask_result>final delivery</subtask_result></report>" {
		t.Fatalf("unexpected slim result: %#v", result.Value)
	}
	messages, ok := result.Value["messages"].([]map[string]any)
	if !ok || len(messages) != 1 || messages[0]["content"] != result.Value["result"] {
		t.Fatalf("expected exactly one final delivery, got %#v", result.Value["messages"])
	}
	stats := result.Value["agent_stats"].(map[string]any)
	if stats["duration_ms"] != int64(18) {
		t.Fatalf("full subtask statistics must survive result slimming: %#v", stats)
	}
}

func TestLocalSubtaskAgentStatsPrefersPersistedUsage(t *testing.T) {
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "subtask.json")
	if err := os.WriteFile(filepath.Join(dir, "usage.json"), []byte(`{"duration_ms":9,"prompt_tokens":100,"completion_tokens":20}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stats := localSubtaskAgentStats(sessionFile, []map[string]any{
		{"role": "assistant", "content": "cached", "duration_ms": 1.0, "usage": map[string]any{"prompt_tokens": 7.0, "completion_tokens": 3.0}},
	})
	if stats["duration_ms"] != int64(9) || stats["prompt_tokens"] != int64(100) || stats["completion_tokens"] != int64(20) {
		t.Fatalf("persisted usage should be authoritative: %#v", stats)
	}
}

func TestLocalSubtaskAgentStatsDeduplicatesRetainedTail(t *testing.T) {
	duplicate := map[string]any{
		"role": "assistant", "content": "same response", "ts": 123.0, "step": 4.0,
		"duration_ms": 11.0, "usage": map[string]any{"prompt_tokens": 50.0, "completion_tokens": 5.0},
	}
	stats := localSubtaskAgentStats(filepath.Join(t.TempDir(), "subtask.json"), []map[string]any{
		duplicate,
		duplicate,
		{"role": "assistant", "content": "new response", "ts": 124.0, "step": 5.0, "duration_ms": 3.0, "usage": map[string]any{"prompt_tokens": 20.0, "completion_tokens": 2.0}},
	})
	if stats["duration_ms"] != int64(14) || stats["prompt_tokens"] != int64(70) || stats["completion_tokens"] != int64(7) {
		t.Fatalf("retained-tail duplicates must not be counted: %#v", stats)
	}
}

func writeLocalSubtaskSession(t *testing.T, messages []map[string]any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "subtask.json")
	raw, err := json.Marshal(map[string]any{"messages": messages})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLocalCreateSubtaskReturnsStructuredErrorForMissingGoal(t *testing.T) {
	tool := NewLocalCreateSubtaskTool(ToolDef{}, &Config{})
	result, err := tool.Execute(context.Background(), ToolInvocation{Workspace: t.TempDir(), Args: json.RawMessage(`{"title":"x","plan":"y"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(result.Value["error"].(string), "goal is required") {
		t.Fatalf("unexpected missing-goal result: %#v", result)
	}
}

func TestResolveLocalAgentExecutableHonorsExplicitOverride(t *testing.T) {
	agentPath := filepath.Join(t.TempDir(), "agent-loop")
	if err := os.WriteFile(agentPath, []byte("agent"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_LOOP_PATH", agentPath)

	got, err := resolveLocalAgentExecutable(&Config{RepoRoot: t.TempDir()})
	if err != nil || got != agentPath {
		t.Fatalf("unexpected explicit executable resolution: got=%q err=%v", got, err)
	}
}

func TestResolveLocalAgentExecutableRejectsInvalidExplicitOverride(t *testing.T) {
	t.Setenv("AGENT_LOOP_PATH", filepath.Join(t.TempDir(), "missing-agent"))

	_, err := resolveLocalAgentExecutable(&Config{RepoRoot: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "missing-agent") {
		t.Fatalf("invalid override must not silently fall back: %v", err)
	}
}
