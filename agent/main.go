package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// estMsgTokens is a rough token estimate (content chars / 3) used to size the
// resumed subtask's initial context.

// buildResumeContext loads a previous (interrupted) subtask session and reduces it
// to a compact initial context: the last few <summary> markers (cumulative; the
// newest is most complete) + the recent tail after the last summary, trimmed to
// ~60% of the summary threshold. This avoids feeding the full raw history to the
// resumed subtask (which could be millions of tokens) while keeping its knowledge.
func buildResumeContext(oldSessionFile string, cfg *Config) []Message {
	// Minimal resume seed: just let the new subtask know what the previous one did.
	// The newest <summary> is the most complete cumulative compression; fall back
	// to the last message (often the final subtask_result / last action) if the
	// old run had no summary yet.
	_ = cfg
	oldMsgs, _ := loadExistingSession(oldSessionFile)
	if len(oldMsgs) == 0 {
		return nil
	}
	for i := len(oldMsgs) - 1; i >= 0; i-- {
		if strings.HasPrefix(oldMsgs[i].Content, "<summary>") {
			return []Message{oldMsgs[i]}
		}
	}
	return []Message{oldMsgs[len(oldMsgs)-1]}
}

func main() {
	// Command-line flags accepted by the native Agent executable.
	configPath := flag.String("ConfigPath", "config/config.json", "Config file path (relative to repo or absolute)")
	modelID := flag.String("ModelId", "", "Model id from the config's models map (defaults to default_model)")
	fallbackModel := flag.String("FallbackModel", "", "Optional model id used when the primary model is rate limited")
	useMock := flag.String("UseMock", "", "Override use_mock: true/false (empty = use config)")
	userOverrideFile := flag.String("UserOverrideFile", "", "File containing user prompt")
	sessionFile := flag.String("SessionFile", "", "Session JSON file for persistence")
	autoAnswerAskUser := flag.Bool("AutoAnswerAskUser", false, "Auto-answer ask_user (e.g. outline confirm) in headless/batch mode")
	resumeSessionFile := flag.String("ResumeSessionFile", "", "Session JSON of a previous (interrupted) subtask to continue from; its summaries + recent tail seed the new subtask context")
	injectStdin := flag.Bool("InjectStdin", false, "Read inject/cancel JSON-line commands from os.Stdin. The host process writes one line per command; useful for live interrupting the agent mid-turn.")
	flag.Parse()

	repoRoot := findRepoRoot(*configPath)
	if strings.TrimSpace(os.Getenv("AGENT_REPO_ROOT")) == "" {
		_ = os.Setenv("AGENT_REPO_ROOT", repoRoot)
	}

	// Propagate the session file path to tool subprocesses (incl. create_subtask)
	// via env var, so child agents can persist their session under the same chat dir.
	if *sessionFile != "" {
		_ = os.Setenv("AGENT_SESSION_FILE", *sessionFile)
	}

	// 1. Load config
	cfg, err := LoadConfig(repoRoot, *configPath)
	if err != nil {
		failF("config: %v", err)
	}
	if *useMock == "true" {
		cfg.UseMock = true
	} else if *useMock == "false" {
		cfg.UseMock = false
	}
	if err := cfg.SelectModel(*modelID); err != nil {
		failF("select model: %v", err)
	}
	if id := strings.TrimSpace(*fallbackModel); id != "" && id != cfg.SelectedModelID {
		if _, ok := cfg.Models[id]; !ok {
			failF("fallback model: unknown model %q", id)
		}
		cfg.FallbackModel = id
	}

	// 2. Build system prompt (modular locale template + skill registry; no skill full-text injection)
	systemPrompt := BuildSystemPrompt(cfg)
	if os.Getenv("AGENT_RUN_KIND") == "subtask" {
		// Delegated runs use an independent worker contract; the work package
		// itself arrives as the user message.
		systemPrompt = BuildSubtaskSystemPrompt(cfg)
	}

	// 4. Read user message
	userPrompt := readUserMessage(*userOverrideFile, cfg)
	if userPrompt == "" {
		fmt.Fprintln(os.Stderr, "[harness] user prompt is empty. Aborting.")
		os.Exit(2)
	}
	userPrompt, err = preparePPTDeckWorkspace(cfg, userPrompt)
	if err != nil {
		failF("prepare PPT workspace: %v", err)
	}

	// 5. 构建与平台无关的工具注册表。各 backend 的路径和进程细节
	// 由 ToolFactory 及具体 backend 实现内部负责。
	registry, err := NewToolFactory().BuildRegistry(cfg)
	if err != nil {
		failF("build tool registry: %v", err)
	}
	defer func() { _ = registry.Close() }()
	toolDefs := registry.ListSchemas()

	// 6. Load session if continuing
	var initialMsgs []Message
	var initialUsage *SessionUsage
	if *resumeSessionFile != "" {
		// Continue from a previous (interrupted) subtask: seed the new subtask
		// with the old session\u2019s compressed view (its <summary> markers + a
		// recent tail trimmed to ~60% of the summary threshold) so it starts with
		// the old knowledge but with headroom, instead of the full raw history.
		initialMsgs = buildResumeContext(*resumeSessionFile, cfg)
		_, initialUsage = loadExistingSession(*resumeSessionFile)
	} else if *sessionFile != "" {
		initialMsgs, initialUsage = loadExistingSession(*sessionFile)
	}

	// 6.5 Prepend Fairy's active plan, when one exists, to the LLM-bound user
	// message. Plans are the only per-turn execution-state prefix.
	// An empty checklist injects [], and a continuation run with no user message
	// injects nothing. Subtasks own no main-thread state, so they are skipped.
	if os.Getenv("AGENT_RUN_KIND") != "subtask" {
		userPrompt = injectRequestContext(userPrompt, cfg)
	}

	// 7. Locale prompts
	summaryPrompt := ReadSummaryPrompt(cfg)
	genTitlePrompt := ReadGenerateTitlePrompt(cfg)
	if cfg.GenerateTitle != nil && !*cfg.GenerateTitle {
		genTitlePrompt = ""
	}
	reflectionPrompt := ReadReflectionPrompt(cfg)

	// 7.5 Optional live-injection stdin reader (lets the host process push a
	// new user message into the agent while it is still running). The host
	// writes one JSON line per command ("inject" / "cancel") to our stdin.
	controller := NewAgentController(context.Background())
	defer controller.Close()
	stdinInjectFlag = *injectStdin
	if err := runInjectStdinReader(controller.Context(), controller); err != nil {
		fmt.Fprintf(os.Stderr, "[harness] WARN: inject listener: %v\n", err)
	}

	// 8. Run agent loop
	result, err := RunAgentLoopCtx(controller.Context(), controller, cfg, registry, systemPrompt, userPrompt,
		initialMsgs, initialUsage, summaryPrompt, genTitlePrompt, reflectionPrompt,
		*sessionFile, cfg.API.Model, *autoAnswerAskUser)
	if err != nil {
		if err == ErrAgentInterrupted {
			// Interrupted by the host — persist what we have so the next turn
			// can resume without losing context, then exit cleanly.
			fmt.Fprintf(os.Stderr, "[harness] agent interrupted at step %d\n", result.Steps)
		} else {
			failF("agent loop: %v", err)
		}
	}

	// 9. Save session
	if *sessionFile != "" {
		if err := SaveSession(*sessionFile, result.Messages, cfg.API.Model); err != nil {
			fmt.Fprintf(os.Stderr, "[harness] WARN: save session: %v\n", err)
		}
		_ = SaveUsage(*sessionFile, result.Usage, result.Messages, result.PerMessageUsage)
		// The trace is what the workbench's trace panel reads; without it the
		// panel shows nothing at all - including the context-compaction entries
		// the operator has been asking to see.
		if err := SaveTrace(*sessionFile, result.Trace); err != nil {
			fmt.Fprintf(os.Stderr, "[harness] WARN: save trace: %v\n", err)
		}
	}

	// 10. Save run history
	saveRunLog(cfg, result, systemPrompt, toolDefs)

	// 11. Silent memory summarization (OpenClaw-style: USER.md / MEMORY.md / date digests)
	if err := SummarizeAndStoreMemory(cfg, result.Messages, cfg.API.Model); err != nil {
		fmt.Fprintf(os.Stderr, "[harness] WARN: memory summary: %v\n", err)
	}

	// 12. Print final
	fmt.Fprintf(os.Stderr, "\n[harness] steps=%d\n", result.Steps)
	printFinalMessage(result.Messages)
}

const localPPTDeckRoot = "/mnt/data/result"

type localPPTConfig struct {
	DeckDir string `xml:"deck_dir"`
}

// preparePPTDeckWorkspace mirrors the production PPT entrypoint's one job that
// matters to this local harness: a deck_dir included in <ppt_config> already
// exists before the Skill starts. The logical production root /mnt/data maps to
// the local configured workspace, never to the host's real /mnt directory.
func preparePPTDeckWorkspace(cfg *Config, userPrompt string) (string, error) {
	start := strings.Index(userPrompt, "<ppt_config>")
	if start < 0 {
		return userPrompt, nil
	}
	endRelative := strings.Index(userPrompt[start:], "</ppt_config>")
	if endRelative < 0 {
		return "", fmt.Errorf("ppt_config is missing its closing tag")
	}
	end := start + endRelative + len("</ppt_config>")
	configXML := userPrompt[start:end]
	var config localPPTConfig
	if err := xml.Unmarshal([]byte(configXML), &config); err != nil {
		return "", fmt.Errorf("parse ppt_config: %w", err)
	}

	if strings.TrimSpace(config.DeckDir) != "" {
		workspace := cfg.ResolvePath(cfg.WorkspaceDir)
		deckDir, err := localPPTDeckPath(workspace, config.DeckDir)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(deckDir, 0o755); err != nil {
			return "", fmt.Errorf("create deck directory: %w", err)
		}
	}

	return userPrompt, nil
}

func localPPTDeckPath(workspace, logicalDeckDir string) (string, error) {
	logicalDeckDir = strings.TrimSpace(strings.ReplaceAll(logicalDeckDir, "\\", "/"))
	prefix := localPPTDeckRoot + "/"
	if !strings.HasPrefix(logicalDeckDir, prefix) {
		return "", fmt.Errorf("ppt deck_dir %q must be under %s", logicalDeckDir, localPPTDeckRoot)
	}
	relative := filepath.Clean(filepath.FromSlash(strings.TrimPrefix(logicalDeckDir, prefix)))
	if relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("ppt deck_dir %q is invalid", logicalDeckDir)
	}
	return filepath.Join(workspace, "result", relative), nil
}

// findRepoRoot locates the repository root from, in priority order:
//  1. the AGENT_REPO_ROOT environment override;
//  2. an absolute -ConfigPath (<root>/config/xxx.json, or a path whose dir is the root);
//  3. the native executable's location (exe in <root>/.tools/, or a dir that looks like a root);
//  4. walking up from the current directory until a repo marker is found;
//  5. the current directory as a last resort.
//
// This keeps the agent working across environments (different clone locations,
// build dirs, or launch cwds) as long as the config file or a repo marker exists.
// isWindowsAbsPath 判断是否是 "C:\..." / "C:/..." 形态的 Windows 绝对路径。
// 不依赖宿主 OS，这样从 Windows 环境变量抄来的配置在 POSIX 上也能原样生效。
func isWindowsAbsPath(p string) bool {
	if len(p) < 3 {
		return false
	}
	c := p[0]
	if !(('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')) {
		return false
	}
	if p[1] != ':' {
		return false
	}
	return p[2] == '\\' || p[2] == '/'
}

func findRepoRoot(configPath string) string {
	if configuredRoot := strings.TrimSpace(os.Getenv("AGENT_REPO_ROOT")); configuredRoot != "" {
		// AGENT_REPO_ROOT 可能是从 Windows 环境抄来的盘符路径。
		// filepath.IsAbs 在 POSIX 上不认 "C:\..."，直接 Abs 会把它当相对路径
		// 拼到当前工作目录后面，凭空造出 "/cwd/C:\fake\override" 这样的路径。
		// 因此先把盘符形态当作绝对路径处理，不再拼接 cwd。
		if isWindowsAbsPath(configuredRoot) {
			return filepath.Clean(configuredRoot)
		}
		if absRoot, err := filepath.Abs(configuredRoot); err == nil {
			return filepath.Clean(absRoot)
		}
	}
	if configPath != "" {
		if filepath.IsAbs(configPath) {
			cfgDir := filepath.Dir(configPath)
			if strings.EqualFold(filepath.Base(cfgDir), "config") {
				if root := filepath.Dir(cfgDir); isRepoRoot(root) {
					return root
				}
			} else if isRepoRoot(cfgDir) {
				return cfgDir
			}
		} else if cwd, err := os.Getwd(); err == nil {
			// Relative config path: find the ancestor that contains config/<path>.
			rel := filepath.FromSlash(configPath)
			for d := cwd; ; d = filepath.Dir(d) {
				if _, err := os.Stat(filepath.Join(d, rel)); err == nil {
					return filepath.Clean(d)
				}
				if filepath.Dir(d) == d {
					break
				}
			}
		}
	}
	exe, err := os.Executable()
	if err == nil {
		dir := filepath.Dir(exe)
		parent := filepath.Dir(dir)
		if isRepoRoot(parent) {
			return parent
		}
		if isRepoRoot(dir) {
			return dir
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		for d := cwd; ; d = filepath.Dir(d) {
			if isRepoRoot(d) {
				return filepath.Clean(d)
			}
			if filepath.Dir(d) == d {
				break
			}
		}
	}
	cwd, _ := os.Getwd()
	return cwd
}

// isRepoRoot reports whether dir looks like a repository root (config dir with
// config.json, a .git marker, or a package.json).
func isRepoRoot(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, "config", "config.json")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err == nil {
		return true
	}
	return false
}

// readUserMessage reads user prompt from file, override, or config
func readUserMessage(overrideFile string, cfg *Config) string {
	if overrideFile != "" {
		if _, err := os.Stat(overrideFile); err == nil {
			data, err := os.ReadFile(overrideFile)
			if err == nil {
				return strings.TrimSpace(string(data))
			}
		}
	}
	return readTextFile(cfg.UserPath())
}

// loadExistingSession reads previous session messages and usage
func loadExistingSession(sessionFile string) ([]Message, *SessionUsage) {
	data, err := os.ReadFile(sessionFile)
	if err != nil {
		return nil, nil
	}
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		data = data[3:]
	}
	var session struct {
		Messages []Message `json:"messages"`
		Model    string    `json:"model"`
	}
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, nil
	}

	// Filter out invalid messages: assistant with no content and no tool_calls (API rejects these)
	var cleanMsgs []Message
	systemSeen := false
	for _, m := range session.Messages {
		if m.Role == "assistant" && m.Content == "" && len(m.ToolCalls) == 0 {
			continue
		}
		if isHarnessErrorMessage(m) {
			// Harness stderr is diagnostic UI data, not conversation context.
			// Older sessions may already contain the demoted user-role copy;
			// drop both forms so the diagnostic never reaches the model.
			continue
		}
		if m.Role == "user" && !isSummaryMarker(m) {
			m.Content = sanitizeUserInputProtocol(m.Content)
			if strings.TrimSpace(m.Content) == "" {
				continue
			}
		}
		// system role is only valid at the very start of the conversation for
		// Claude/Claude-compatible APIs. Frontend error lines were historically
		// appended as mid-stream system messages, which the qn gateway rejects
		// with 400. Keep the first system message (the real system prompt) and
		// demote any later non-error system note to user role.
		if m.Role == "system" {
			if !systemSeen {
				systemSeen = true
				cleanMsgs = append(cleanMsgs, m)
				continue
			}
			m.Role = "user"
			cleanMsgs = append(cleanMsgs, m)
			continue
		}
		cleanMsgs = append(cleanMsgs, m)
	}
	// NOTE: must assign unconditionally ? demotion (system->user) does not
	// change the slice length, so a length-based guard would silently discard
	// the fixed messages (bug observed: mid-stream system errors stayed system
	// and qn gateway rejected them).
	session.Messages = cleanMsgs

	// Repair orphaned tool_calls / tool responses. If the main thread crashed
	// while a tool (e.g. create_subtask) was still running, the assistant
	// message keeps its tool_calls but no matching tool response is persisted;
	// the API then rejects the sequence. Strip tool_calls from such assistant
	// messages and drop orphaned tool responses.
	session.Messages = repairToolPairing(session.Messages)

	// repairToolPairing can leave an assistant message with BOTH empty content
	// AND empty tool_calls (it strips tool_calls from the last orphaned call).
	// Claude/qn gateways reject such messages with 400 "field Content invalid".
	// Drop them now (the second pass catches what the pre-repair filter missed).
	afterRepair := session.Messages[:0]
	for _, m := range session.Messages {
		if m.Role == "assistant" && m.Content == "" && len(m.ToolCalls) == 0 {
			continue
		}
		afterRepair = append(afterRepair, m)
	}
	session.Messages = afterRepair

	// Accumulate usage from loaded messages. Summary checkpoints persist a
	// retained tail in the display transcript; those duplicated assistant
	// messages must not be counted as fresh model calls when a session resumes.
	usage := &SessionUsage{}
	seenUsage := map[string]bool{}
	for _, m := range session.Messages {
		if m.Usage == nil {
			continue
		}
		key := usageMessageKey(m)
		if seenUsage[key] {
			continue
		}
		seenUsage[key] = true
		usage.PromptTokens += m.Usage.PromptTokens
		usage.CompletionTokens += m.Usage.CompletionTokens
		usage.DurationMs += m.DurationMs
	}
	return session.Messages, usage
}

func isHarnessErrorMessage(message Message) bool {
	if strings.EqualFold(strings.TrimSpace(message.InternalType), "harness_error") {
		return true
	}
	content := strings.TrimSpace(message.Content)
	return strings.HasPrefix(content, "SYSTEM ERROR:") && !strings.Contains(content, "<file_context>")
}

// repairToolPairing ensures every assistant tool_calls message is followed by
// its tool responses and no tool response is orphaned.
func repairToolPairing(msgs []Message) []Message {
	var out []Message
	var pending []string // tool call IDs awaiting a response, in order
	stripLastCalls := func() {
		for j := len(out) - 1; j >= 0; j-- {
			if out[j].Role == "assistant" && len(out[j].ToolCalls) > 0 {
				out[j].ToolCalls = nil
				break
			}
		}
	}
	for _, m := range msgs {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			// A new assistant message means any previous pending calls were
			// orphaned (no tool responses arrived before the next turn).
			if len(pending) > 0 {
				stripLastCalls()
				pending = nil
			}
			malformedArguments := false
			for _, tc := range m.ToolCalls {
				if _, err := normalizeToolArguments(tc); err != nil {
					malformedArguments = true
					break
				}
			}
			if malformedArguments {
				// Sessions written before tool-call validation may contain a
				// syntactically broken arguments payload. Keep any useful prose,
				// but do not replay this call batch or its following results.
				if strings.TrimSpace(m.Content) != "" {
					m.ToolCalls = nil
					out = append(out, m)
				}
				continue
			}
			for _, tc := range m.ToolCalls {
				if tc.ID != "" {
					pending = append(pending, tc.ID)
				}
			}
			out = append(out, m)
			continue
		}
		if m.Role == "tool" {
			if len(pending) == 0 {
				continue // orphaned tool response: drop
			}
			if m.ToolCallID != "" {
				found := false
				for i, id := range pending {
					if id == m.ToolCallID {
						pending = append(pending[:i], pending[i+1:]...)
						found = true
						break
					}
				}
				if !found {
					pending = pending[1:] // mismatch: consume the oldest anyway
				}
			} else {
				pending = pending[1:]
			}
			out = append(out, m)
			continue
		}
		// user / system / assistant-without-calls: boundary; any pending calls
		// that never got responses must be stripped.
		if len(pending) > 0 {
			stripLastCalls()
			pending = nil
		}
		out = append(out, m)
	}
	if len(pending) > 0 {
		stripLastCalls()
	}
	return out
}

// printFinalMessage outputs the last assistant message
func printFinalMessage(messages []Message) {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" && messages[i].Content != "" {
			fmt.Println(messages[i].Content)
			return
		}
	}
}

// saveRunLog writes the run history JSON + text dump
func saveRunLog(cfg *Config, result *AgentResult, systemPrompt string, toolDefs []ToolDef) {
	stamp := time.Now().Format("20060102_150405")
	histDir := cfg.ResolvePath(cfg.HistoryDir)
	os.MkdirAll(histDir, 0755)

	// Build schema names
	var schemaNames []string
	for _, td := range toolDefs {
		if fn, ok := td.Function.(map[string]interface{}); ok {
			if n, ok := fn["name"]; ok {
				schemaNames = append(schemaNames, fmt.Sprintf("%v", n))
			}
		}
	}

	// JSON history
	// Categorized file name pattern:
	//   run_<chatName>_main_<ts>.json         (e.g. run_chat-20260803-094933-1_main_094948.json)
	//   run_<chatName>_subtask_<safeTitle>_<ts>.json
	//   run_orphan_main_<ts>.json             (no AGENT_SESSION_FILE set)
	//   run_orphan_subtask_<safeTitle>_<ts>.json
	chatName := ""
	if ps := os.Getenv("AGENT_SESSION_FILE"); ps != "" {
		chatDir := filepath.Dir(ps)
		// Child agents persist under memory/sessions/<chat>/subtasks/;
		// normalize back to the parent chat name for run classification.
		if filepath.Base(chatDir) == "subtasks" {
			chatDir = filepath.Dir(chatDir)
		}
		chatName = filepath.Base(chatDir)
	}
	kind := os.Getenv("AGENT_RUN_KIND")
	if kind != "subtask" {
		kind = "main"
	}
	prefix := "run"
	if chatName != "" {
		prefix = "run_" + chatName + "_" + kind
	} else {
		prefix = "run_orphan_" + kind
	}
	if kind == "subtask" {
		if t := os.Getenv("AGENT_SUBTASK_TITLE"); t != "" {
			prefix += "_" + sanitizeRunTitle(t)
		}
	}
	fileBase := prefix + "_" + stamp
	outJSON := filepath.Join(histDir, fileBase+".json")

	// Use trace from agent loop result
	trace := result.Trace
	if trace == nil {
		trace = []map[string]interface{}{}
	}

	payload := map[string]interface{}{
		"generated_at":  time.Now().Format(time.RFC3339),
		"repo_root":     cfg.RepoRoot,
		"model":         cfg.API.Model,
		"use_mock":      cfg.UseMock,
		"system_prompt": systemPrompt,
		"tool_schemas":  schemaNames,
		"usage":         result.Usage,
		"steps":         result.Steps,
		"messages":      result.Messages,
		"trace":         trace,
	}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err == nil {
		os.WriteFile(outJSON, data, 0644)
	}

	// Text dump
	outTXT := filepath.Join(histDir, fileBase+".txt")
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== AGENT LOOP RUN @ %s ===\n", stamp))
	sb.WriteString(fmt.Sprintf("steps=%d  use_mock=%v  model=%s\n", result.Steps, cfg.UseMock, cfg.API.Model))
	if result.Usage != nil {
		sb.WriteString(fmt.Sprintf("usage: prompt_tokens=%d  completion_tokens=%d  duration_ms=%d\n",
			result.Usage.PromptTokens, result.Usage.CompletionTokens, result.Usage.DurationMs))
	}
	sb.WriteString("\n--- messages ---\n")
	for i, m := range result.Messages {
		sb.WriteString(fmt.Sprintf("[%d] role=%s\n", i+1, m.Role))
		if m.Content != "" {
			sb.WriteString(m.Content + "\n")
		}
		if len(m.ToolCalls) > 0 {
			sb.WriteString("(tool_calls:)\n")
			for _, tc := range m.ToolCalls {
				sb.WriteString(fmt.Sprintf("  - id=%s name=%s args=%s\n", tc.ID, tc.Function.Name, tc.Function.Arguments))
			}
		}
		if m.ToolCallID != "" {
			sb.WriteString(fmt.Sprintf("tool_call_id=%s name=%s\n", m.ToolCallID, m.Name))
		}
		sb.WriteString("\n")
	}
	os.WriteFile(outTXT, []byte(sb.String()), 0644)

	fmt.Fprintf(os.Stderr, "[harness] history JSON: %s\n", outJSON)
	fmt.Fprintf(os.Stderr, "[harness] history TXT : %s\n", outTXT)
}

// sanitizeRunTitle keeps ASCII alphanumerics and CJK; other chars become _.
// Length capped at 60 to keep total filename within Windows path limits.
func sanitizeRunTitle(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_' || r == '-' || r == '.' || r == ' ':
			b.WriteRune(r)
		case r > 0x7f:
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := strings.TrimSpace(b.String())
	// collapse underscores/spaces
	for strings.Contains(out, "  ") {
		out = strings.ReplaceAll(out, "  ", " ")
	}
	out = strings.ReplaceAll(out, " ", "_")
	for strings.Contains(out, "__") {
		out = strings.ReplaceAll(out, "__", "_")
	}
	if len(out) > 60 {
		out = out[:60]
	}
	return strings.Trim(out, "_.-")
}

func failF(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "[harness] ERROR: "+format+"\n", args...)
	os.Exit(1)
}
