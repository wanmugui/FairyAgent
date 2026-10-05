package local

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// localAgentExecutableResolver chooses the current platform's Agent executable.
// Keeping it injectable lets the Local tool test the child-process contract without
// starting a real LLM session.
type localAgentExecutableResolver func(*Config) (string, error)

type localCreateSubtaskTool struct {
	schema       ToolDef
	cfg          *Config
	runner       localProcessRunner
	resolveAgent localAgentExecutableResolver
}

var localSubtaskSequence uint64

func NewLocalCreateSubtaskTool(schema ToolDef, cfg *Config) Tool {
	return newLocalCreateSubtaskTool(schema, cfg, osLocalProcessRunner{}, resolveLocalAgentExecutable)
}

func newLocalCreateSubtaskTool(schema ToolDef, cfg *Config, runner localProcessRunner, resolver localAgentExecutableResolver) Tool {
	if cfg == nil {
		cfg = &Config{}
	}
	if runner == nil {
		runner = osLocalProcessRunner{}
	}
	if resolver == nil {
		resolver = resolveLocalAgentExecutable
	}
	return &localCreateSubtaskTool{schema: schema, cfg: cfg, runner: runner, resolveAgent: resolver}
}

func (t *localCreateSubtaskTool) Name() string {
	return "create_subtask"
}

func (t *localCreateSubtaskTool) Schema() ToolDef {
	return t.schema
}

func (t *localCreateSubtaskTool) Execute(ctx context.Context, invocation ToolInvocation) (ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}
	args, err := decodeLocalToolArgs(invocation)
	if err != nil {
		return localErrorResult(t.Name(), err), nil
	}
	title := localStringArg(args, "title")
	goal := localStringArg(args, "goal")
	if strings.TrimSpace(goal) == "" {
		return localErrorResult(t.Name(), fmt.Errorf("goal is required")), nil
	}
	background := true
	if _, ok := args["background"]; ok {
		background = localBoolArg(args, "background")
	}
	localContext, err := localToolContext(invocation)
	if err != nil {
		return localErrorResult(t.Name(), err), nil
	}

	agentExecutable, err := t.resolveAgent(t.cfg)
	if err != nil {
		return localSubtaskUnavailableResult(err), nil
	}
	userPrompt, err := buildLocalSubtaskPrompt(t.cfg, title, goal, args)
	if err != nil {
		return localErrorResult(t.Name(), err), nil
	}

	sessionFile, branchName, parentSessionName, err := localBranchSessionFile(invocation.SessionFile, title)
	if err != nil {
		return localErrorResult(t.Name(), fmt.Errorf("create branch session: %w", err)), nil
	}
	userFilePath := ""
	if background {
		userFilePath = sessionFile + ".prompt.txt"
		if err := os.WriteFile(userFilePath, []byte(userPrompt), 0o600); err != nil {
			return localErrorResult(t.Name(), fmt.Errorf("write delegated prompt: %w", err)), nil
		}
	} else {
		userFile, err := os.CreateTemp("", "agent-subtask-user-*.txt")
		if err != nil {
			return localErrorResult(t.Name(), fmt.Errorf("create delegated prompt: %w", err)), nil
		}
		userFilePath = userFile.Name()
		defer os.Remove(userFilePath)
		if err := userFile.Chmod(0o600); err != nil {
			_ = userFile.Close()
			return localErrorResult(t.Name(), fmt.Errorf("protect delegated prompt: %w", err)), nil
		}
		if _, err := userFile.WriteString(userPrompt); err != nil {
			_ = userFile.Close()
			return localErrorResult(t.Name(), fmt.Errorf("write delegated prompt: %w", err)), nil
		}
		if err := userFile.Close(); err != nil {
			return localErrorResult(t.Name(), fmt.Errorf("close delegated prompt: %w", err)), nil
		}
	}

	streamPath := sessionFile + ".stream"
	stderrPath := sessionFile + ".stderr"
	processRequest := localProcessRequest{
		Path:    agentExecutable,
		Args:    nil,
		Dir:     t.cfg.RepoRoot,
		Env:     localSubtaskEnvironment(t.cfg, localContext.Workspace, title),
		Timeout: invocation.Timeout,
	}
	var streamFile *os.File
	if background {
		processRequest.Detach = true
		processRequest.OutputPath = streamPath
		processRequest.ErrorPath = stderrPath
		processRequest.JobID = branchName
	} else {
		streamFile, err = os.OpenFile(streamPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return localErrorResult(t.Name(), fmt.Errorf("create subtask stream: %w", err)), nil
		}
		defer streamFile.Close()
		streamHeader, _ := json.Marshal(map[string]any{"type": "subtask_start", "title": title, "ts": time.Now().Unix()})
		_, _ = streamFile.Write(append(streamHeader, '\n'))
		processRequest.StdoutWriter = streamFile
	}

	childArgs := []string{
		"-ConfigPath", localSubtaskConfigPath(t.cfg),
		"-UseMock", strconv.FormatBool(t.cfg.UseMock),
		"-UserOverrideFile", userFilePath,
		"-SessionFile", sessionFile,
	}
	if modelID := strings.TrimSpace(t.cfg.SelectedModelID); modelID != "" {
		childArgs = append(childArgs, "-ModelId", modelID)
	}
	if resumeSession := localStringArg(args, "resume_session"); resumeSession != "" {
		childArgs = append(childArgs, "-ResumeSessionFile", resumeSession)
	}
	processRequest.Args = childArgs
	processResult, err := t.runner.Run(ctx, processRequest)
	if err != nil {
		return localErrorResult(t.Name(), fmt.Errorf("run subtask agent: %w", err)), nil
	}
	if background {
		if !processResult.Detached {
			return localErrorResult(t.Name(), fmt.Errorf("subtask runner did not detach")), nil
		}
		manifestPath := sessionFile + ".job.json"
		manifest := map[string]any{
			"job_id":              branchName,
			"title":               title,
			"status":              "running",
			"background":          true,
			"pid":                 processResult.PID,
			"session":             sessionFile,
			"branch_session":      branchName,
			"parent_session":      parentSessionName,
			"parent_session_file": invocation.SessionFile,
			"prompt_path":         userFilePath,
			"stream_path":         streamPath,
			"stderr_path":         stderrPath,
			"manifest_path":       manifestPath,
			"created_at":          time.Now().UTC().Format(time.RFC3339),
		}
		data, marshalErr := json.MarshalIndent(manifest, "", "  ")
		if marshalErr != nil {
			return localErrorResult(t.Name(), fmt.Errorf("encode background subtask manifest: %w", marshalErr)), nil
		}
		if writeErr := os.WriteFile(manifestPath, append(data, '\n'), 0o600); writeErr != nil {
			return localErrorResult(t.Name(), fmt.Errorf("write background subtask manifest: %w", writeErr)), nil
		}
		return ToolResult{Value: map[string]any{
			"tool":           "create_subtask",
			"ok":             true,
			"status":         "running",
			"background":     true,
			"job_id":         branchName,
			"title":          title,
			"domain":         title,
			"session":        sessionFile,
			"branch_session": branchName,
			"parent_session": parentSessionName,
			"manifest_path":  manifestPath,
			"prompt_path":    userFilePath,
			"pid":            processResult.PID,
		}}, nil
	}
	if processResult.TimedOut {
		return localErrorResult(t.Name(), fmt.Errorf("subtask timed out after %s", invocation.Timeout)), nil
	}
	if processResult.ExitCode != 0 {
		return localSubtaskProcessError(sessionFile, processResult), nil
	}

	return localSubtaskBranchResult(title, sessionFile, branchName, parentSessionName), nil
}

func resolveLocalAgentExecutable(cfg *Config) (string, error) {
	if override := strings.TrimSpace(os.Getenv("AGENT_LOOP_PATH")); override != "" {
		return requireLocalAgentExecutable(override)
	}
	if executable, err := os.Executable(); err == nil {
		if path, statErr := requireLocalAgentExecutable(executable); statErr == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("agent executable is unavailable; start with pnpm dev or set AGENT_LOOP_PATH")
}

func requireLocalAgentExecutable(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("agent executable %q: %w", path, err)
	}
	if info.IsDir() || !info.Mode().IsRegular() {
		return "", fmt.Errorf("agent executable %q is not a regular file", path)
	}
	return path, nil
}

func localSubtaskUnavailableResult(err error) ToolResult {
	return ToolResult{
		Value: map[string]any{
			"tool":  "create_subtask",
			"ok":    false,
			"code":  "unavailable",
			"error": err.Error(),
		},
		IsError: true,
	}
}

func localSubtaskConfigPath(cfg *Config) string {
	if cfg != nil && strings.TrimSpace(cfg.ConfigPath) != "" {
		return cfg.ConfigPath
	}
	if cfg != nil && strings.TrimSpace(cfg.RepoRoot) != "" {
		return filepath.Join(cfg.RepoRoot, "config", "config.json")
	}
	return filepath.Join("config", "config.json")
}

func localSubtaskEnvironment(cfg *Config, workspace, title string) []string {
	repoRoot := ""
	if cfg != nil {
		repoRoot = cfg.RepoRoot
	}
	env := []string{
		"AGENT_REPO_ROOT=" + repoRoot,
		"WORKSPACE_DIR=" + workspace,
		"AGENT_RUN_KIND=subtask",
		"AGENT_SUBTASK_TITLE=" + title,
	}
	return append(env, localPptToolEnvironment(cfg)...)
}

func buildLocalSubtaskPrompt(cfg *Config, title, goal string, args map[string]any) (string, error) {
	taskParts := make([]string, 0, 16)
	if title != "" {
		taskParts = append(taskParts, "# Subtask: "+title)
	}
	taskParts = append(taskParts, "", "## Goal", goal, "", "## Plan", localStringArg(args, "plan"))
	for _, section := range []struct{ heading, key string }{
		{"Relevant Files", "relevant_files"},
		{"Criteria", "criteria"},
		{"Additional Info", "addition"},
	} {
		if value := localStringArg(args, section.key); value != "" {
			taskParts = append(taskParts, "", "## "+section.heading, value)
		}
	}
	if quickLook := localSubtaskQuickLook(localStringArg(args, "relevant_files"), cfg.RepoRoot, 4000); quickLook != "" {
		taskParts = append(taskParts, "", "## 关键规范速览", quickLook)
	}
	if localSubtaskIsResearch(title, goal, localStringArg(args, "plan")) {
		taskParts = append(taskParts, "", "## 收敛约束", "网络搜索(web_search) + 网页抓取(web_fetch) 合计最多 20 次；同一主题去重；信息足够即停止搜索，直接产出结论。")
	}
	taskContent := strings.Join(taskParts, "\n")

	if cfg.BuildSubtaskPrompt != nil {
		return cfg.BuildSubtaskPrompt(taskContent)
	}
	return taskContent + "\n\n请根据上面的被委派任务执行工作，完成后在 <subtask_result> 中输出结果。", nil
}

func localSubtaskIsResearch(parts ...string) bool {
	for _, part := range parts {
		for _, keyword := range []string{"调研", "深度研究", "研究", "资料", "搜集", "收集"} {
			if strings.Contains(part, keyword) {
				return true
			}
		}
	}
	return false
}

func localSubtaskQuickLook(relevantFiles, repoRoot string, budgetChars int) string {
	keywords := []string{"必须", "禁止", "不得", "硬约束", "字段", "校验", "输出", "产物", "绝对", "严禁", "只能"}
	var output []string
	used := 0
	for _, requested := range strings.FieldsFunc(relevantFiles, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ' ' || r == ',' || r == '\t'
	}) {
		path := strings.TrimSpace(requested)
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(repoRoot, path)
		}
		// 图片不能当文本读，但必须显式交给子任务：否则它只拿到一个 .jpg 路径，
		// 既不知道这是用户发来的图，也容易直接跳过。
		if ext := strings.ToLower(filepath.Ext(path)); isSubtaskImageExt(ext) {
			if _, err := os.Stat(path); err != nil {
				output = append(output, fmt.Sprintf("> %s（图片文件当前不可读：%v）", filepath.Base(path), err))
				continue
			}
			line := fmt.Sprintf(
				"> %s：用户发来的图片，真实路径 %s。用 computer_observe 的 image_path 参数读它"+
					"（主模型有 native vision 时截图会直接附给你）；需要文字/坐标时用 image_vqa，mode=local 只做 OCR。",
				filepath.Base(path), path)
			if used+len([]rune(line)) > budgetChars {
				break
			}
			output = append(output, line)
			used += len([]rune(line))
			continue
		}
		if !strings.HasSuffix(strings.ToLower(path), ".md") {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		header := "> " + filepath.Base(path)
		if used+len([]rune(header)) > budgetChars {
			break
		}
		output = append(output, header)
		used += len([]rune(header))
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || (!strings.HasPrefix(line, "#") && !containsAnyString(line, keywords)) {
				continue
			}
			if used+len([]rune(line)) > budgetChars {
				break
			}
			output = append(output, line)
			used += len([]rune(line))
		}
	}
	return strings.Join(output, "\n")
}

func isSubtaskImageExt(ext string) bool {
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".heic", ".heif", ".tiff":
		return true
	default:
		return false
	}
}

func containsAnyString(value string, candidates []string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}

func localBranchSessionFile(parentSession, title string) (sessionFile, branchName, parentSessionName string, err error) {
	sequence := atomic.AddUint64(&localSubtaskSequence, 1)
	if strings.TrimSpace(parentSession) == "" {
		sessionFile = filepath.Join(os.TempDir(), fmt.Sprintf("subtask_result_%d_%d.json", time.Now().UnixNano(), sequence))
		return sessionFile, "", "", nil
	}
	parentDir := filepath.Dir(parentSession)
	parentSessionName = filepath.Base(parentDir)
	sessionsRoot := filepath.Dir(parentDir)
	if parentSessionName == "" || parentSessionName == "." {
		return "", "", "", fmt.Errorf("cannot resolve parent session name from %q", parentSession)
	}
	slug := localSubtaskFilename(title)
	if slug == "" {
		slug = "subtask"
	}
	baseName := parentSessionName + "__" + slug
	for attempt := 0; ; attempt++ {
		candidate := baseName
		if attempt > 0 {
			candidate = fmt.Sprintf("%s-%d", baseName, attempt+1)
		}
		branchDir := filepath.Join(sessionsRoot, candidate)
		if mkdirErr := os.Mkdir(branchDir, 0o755); mkdirErr != nil {
			if os.IsExist(mkdirErr) {
				continue
			}
			return "", "", "", mkdirErr
		}
		branchName = candidate
		sessionFile = filepath.Join(branchDir, candidate+".json")
		meta := map[string]any{
			"messages":       []any{},
			"model":          nil,
			"kind":           "branch",
			"parent_session": parentSessionName,
			"domain":         title,
			"created_by":     "model",
			"created_at":     time.Now().UTC().Format(time.RFC3339),
		}
		data, marshalErr := json.MarshalIndent(meta, "", "  ")
		if marshalErr != nil {
			return "", "", "", marshalErr
		}
		if writeErr := os.WriteFile(sessionFile, append(data, '\n'), 0o644); writeErr != nil {
			return "", "", "", writeErr
		}
		return sessionFile, branchName, parentSessionName, nil
	}
}

// localSubtaskSessionFile is kept for callers/tests that only need a unique
// child session path. Production create_subtask uses localBranchSessionFile so
// the child is registered as a branch under the parent session.
func localSubtaskSessionFile(parentSession, title string) string {
	sessionFile, _, _, err := localBranchSessionFile(parentSession, title)
	if err == nil {
		return sessionFile
	}
	sequence := atomic.AddUint64(&localSubtaskSequence, 1)
	return filepath.Join(os.TempDir(), fmt.Sprintf("subtask_result_%d_%d.json", time.Now().UnixNano(), sequence))
}

func localSubtaskFilename(value string) string {
	var out strings.Builder
	for _, r := range strings.TrimSpace(value) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_' || r == '-' || r == '.':
			out.WriteRune(r)
		case r == ' ':
			out.WriteRune('_')
		case r > 0x7f:
			out.WriteRune(r)
		default:
			out.WriteRune('_')
		}
	}
	name := strings.Trim(out.String(), "_.-")
	if len([]rune(name)) > 80 {
		name = string([]rune(name)[:80])
	}
	return name
}

func localSubtaskProcessError(sessionFile string, result localProcessResult) ToolResult {
	logFile := sessionFile + ".log"
	logBody := result.Stdout + result.Stderr
	if err := os.WriteFile(logFile, []byte(logBody), 0o600); err != nil {
		logFile = ""
	}
	errorText := fmt.Sprintf("subtask agent exited with code %d", result.ExitCode)
	if logFile != "" {
		errorText += "; full log: " + logFile
	}
	return ToolResult{Value: map[string]any{"tool": "create_subtask", "ok": false, "error": errorText, "exit_code": result.ExitCode, "log": logFile}, IsError: true}
}

func localSubtaskResult(title, sessionFile string) ToolResult {
	return localSubtaskBranchResult(title, sessionFile, "", "")
}

func localSubtaskBranchResult(title, sessionFile, branchName, parentSessionName string) ToolResult {
	value := map[string]any{"ok": true, "title": title, "session": sessionFile}
	if branchName != "" {
		value["branch_session"] = branchName
	}
	if parentSessionName != "" {
		value["parent_session"] = parentSessionName
	}
	if title != "" {
		value["domain"] = title
	}
	raw, err := os.ReadFile(sessionFile)
	if err != nil {
		return ToolResult{Value: value}
	}
	var session struct {
		Messages []map[string]any `json:"messages"`
	}
	if json.Unmarshal(raw, &session) != nil {
		return ToolResult{Value: value}
	}
	value["agent_stats"] = localSubtaskAgentStats(sessionFile, session.Messages)
	if final := localSubtaskFinalDelivery(session.Messages); final != "" {
		// The full child conversation is already persisted in sessionFile. Return
		// only its final delivery to the parent so parallel PPT subtasks cannot
		// flood the main Agent context with their intermediate tool chatter.
		value["result"] = final
		value["messages"] = []map[string]any{{"role": "assistant", "content": final}}
	}
	return ToolResult{Value: value}
}

func localSubtaskAgentStats(sessionFile string, messages []map[string]any) map[string]any {
	usageFile := filepath.Join(filepath.Dir(sessionFile), "usage.json")
	if raw, err := os.ReadFile(usageFile); err == nil {
		var usage struct {
			DurationMs       int64 `json:"duration_ms"`
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		}
		if json.Unmarshal(raw, &usage) == nil &&
			(usage.DurationMs != 0 || usage.PromptTokens != 0 || usage.CompletionTokens != 0) {
			return map[string]any{
				"duration_ms":       usage.DurationMs,
				"prompt_tokens":     usage.PromptTokens,
				"completion_tokens": usage.CompletionTokens,
			}
		}
	}

	var duration, promptTokens, completionTokens int64
	seen := map[string]bool{}
	for _, message := range messages {
		if message["role"] != "assistant" {
			continue
		}
		itemDuration := int64(0)
		if value, ok := message["duration_ms"].(float64); ok {
			itemDuration = int64(value)
		}
		itemPrompt, itemCompletion := int64(0), int64(0)
		if usage, ok := message["usage"].(map[string]any); ok {
			if value, ok := usage["prompt_tokens"].(float64); ok {
				itemPrompt = int64(value)
			}
			if value, ok := usage["completion_tokens"].(float64); ok {
				itemCompletion = int64(value)
			}
		}
		key := fmt.Sprintf("%v|%v|%d|%d|%d|%v", message["ts"], message["step"], itemDuration, itemPrompt, itemCompletion, message["content"])
		if seen[key] {
			continue
		}
		seen[key] = true
		duration += itemDuration
		promptTokens += itemPrompt
		completionTokens += itemCompletion
	}
	return map[string]any{
		"duration_ms": duration, "prompt_tokens": promptTokens, "completion_tokens": completionTokens,
	}
}

func localSubtaskFinalDelivery(messages []map[string]any) string {
	var report string
	for _, message := range messages {
		if message["role"] != "assistant" {
			continue
		}
		content, _ := message["content"].(string)
		if strings.Contains(content, "<subtask_result>") {
			report = content
			continue
		}
		if report == "" && strings.Contains(content, "<report>") {
			report = content
		}
	}
	if report != "" {
		return report
	}
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index]["role"] != "assistant" {
			continue
		}
		if content, _ := messages[index]["content"].(string); content != "" {
			return content
		}
	}
	return ""
}
