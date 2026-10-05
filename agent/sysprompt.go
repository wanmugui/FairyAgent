package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// BuildSystemPrompt renders the modular system-prompt template
// (cfg.SystemPath(), normally config/system/zh.md) with the runtime
// feature flags and the configured skill registry index. Full skill contents
// are intentionally NOT appended here: the agent reads SKILL.md on demand via
// read_file when a registry entry matches (see the skill-registry block).
func BuildSystemPrompt(cfg *Config) string {
	tmpl := ""
	if useModularSystemParts(cfg) {
		tmpl = assembleSystemPrompt(cfg)
	}
	if tmpl == "" {
		tmpl = readTextFile(cfg.SystemPath())
	}
	if tmpl == "" {
		tmpl = "You are a helpful assistant."
	}
	registryJSON := buildMergedSkillRegistryJSON(cfg)
	rendered, err := renderJinja(tmpl, systemTemplateVars(cfg, registryJSON))
	if err != nil {
		// Never send raw template tags to the model; degrade by removing them.
		rendered = stripTemplateTags(tmpl)
	}
	// The template provides "Today: {{ CURRENT_TIME }}"; keep a fallback so a
	// legacy fully-rendered system file still gets a Today line.
	if !strings.HasPrefix(rendered, "Today:") {
		rendered = fmt.Sprintf("Today: %s\n\n%s", time.Now().Format("2006-01-02 15:04:05 -07:00"), rendered)
	}
	// Recent date-memory digests as background context (OpenClaw daily memory).
	if dm := readRecentDateMemory(cfg); dm != "" {
		if tpl := ReadModulePrompt(cfg, "date_memory", "zh"); tpl != "" {
			tpl = strings.ReplaceAll(tpl, "{{ DATE_MEMORY_BLOCK }}", dm)
			rendered += "\n\n" + strings.TrimSpace(tpl)
		}
	}
	if block := buildSegmentedMemoryPromptBlock(cfg, segmentedMemorySessionID(time.Now())); block != "" {
		rendered += "\n\n" + block
	}
	return rendered
}

// BuildSubtaskSystemPrompt renders the worker-only contract for delegated runs.
// The work package and its skill registry travel in the subtask's user message,
// so a subtask never receives the main agent's system prompt.
func BuildSubtaskSystemPrompt(cfg *Config) string {
	tmpl := readTextFile(cfg.SubtaskSystemPath())
	if strings.TrimSpace(tmpl) == "" {
		tmpl = "你是主线程委派的子任务执行者。只完成被委派的工作包，并在 <subtask_result> 中返回可核验的成果。"
	}
	vars := systemTemplateVars(cfg, "[]")
	vars["enable_skill_registry"] = false
	vars["is_delegate"] = true
	rendered, err := renderJinja(tmpl, vars)
	if err != nil {
		rendered = stripTemplateTags(tmpl)
	}
	if !strings.HasPrefix(rendered, "Today:") {
		rendered = fmt.Sprintf("Today: %s\n\n%s", time.Now().Format("2006-01-02 15:04:05 -07:00"), rendered)
	}
	return rendered
}

// useModularSystemParts enables parts assembly only for the default modular
// system prompt files; custom system_path values keep their single-file behavior.
func useModularSystemParts(cfg *Config) bool {
	if cfg == nil || strings.TrimSpace(cfg.SystemPartsDir) == "" {
		return false
	}
	path := strings.ReplaceAll(strings.TrimSpace(cfg.SystemPath()), "\\", "/")
	return strings.HasSuffix(path, "config/system/zh.md") || strings.HasSuffix(path, "config/system/en.md")
}

// assembleSystemPrompt builds the modular system prompt from parts/{lang}/
// using manifest.yml ordering when available. Falls back to sorted .md files.
func assembleSystemPrompt(cfg *Config) string {
	if cfg == nil || strings.TrimSpace(cfg.SystemPartsDir) == "" {
		return ""
	}
	partsDir := cfg.ResolvePath(cfg.SystemPartsDir)
	manifestPath := filepath.Join(filepath.Dir(partsDir), "manifest.yml")
	lang := filepath.Base(partsDir)
	var names []string
	if data, err := os.ReadFile(manifestPath); err == nil {
		names = systemPromptManifestNames(string(data), lang)
	}
	if len(names) == 0 {
		entries, err := os.ReadDir(partsDir)
		if err != nil {
			return ""
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
				names = append(names, entry.Name())
			}
		}
		sort.Strings(names)
	}
	var builder strings.Builder
	for _, name := range names {
		if name == "" || strings.Contains(name, "..") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(partsDir, name))
		if err != nil {
			continue
		}
		content := strings.TrimSpace(strings.ReplaceAll(string(data), "\r\n", "\n"))
		if content == "" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteString("\n\n")
		}
		builder.WriteString(content)
	}
	return builder.String()
}

func systemPromptManifestNames(manifest, lang string) []string {
	var names []string
	inSection := false
	for _, raw := range strings.Split(manifest, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasSuffix(line, ":") && strings.TrimSuffix(line, ":") == lang {
			inSection = true
			continue
		}
		if inSection {
			if strings.HasPrefix(line, "- ") {
				names = append(names, strings.TrimSpace(strings.TrimPrefix(line, "- ")))
				continue
			}
			if line != "" {
				break
			}
		}
	}
	return names
}

func systemTemplateVars(cfg *Config, registryJSON string) map[string]any {
	tool := func(name string) bool {
		if t, ok := cfg.HTTPTools[name]; ok {
			return t.Enabled
		}
		return true
	}
	return map[string]any{
		"enable_web_search":              tool("web_search"),
		"enable_web_fetch":               tool("web_fetch"),
		"enable_browser":                 tool("browser"),
		"enable_image_vqa":               tool("image_vqa"),
		"enable_document_parser":         tool("document_parser"),
		"enable_read_file":               tool("read_file"),
		"enable_write_file":              tool("write_file"),
		"enable_edit_file":               tool("edit_file"),
		"enable_glob":                    tool("glob"),
		"enable_ask_user":                tool("ask_user"),
		"enable_bash":                    tool("bash"),
		"enable_create_subtask":          tool("create_subtask"),
		"enable_plan":                    tool("plan"),
		"enable_report":                  true,
		"enable_show_result":             tool("show_result"),
		"enable_reflection":              tool("reflection"),
		"enable_memory_search":           tool("memory_search"),
		"enable_memory":                  tool("memory_search"),
		"enable_skill_registry":          registryJSON != "[]",
		"enable_date_memory":             tool("memory_search"),
		"enable_os_mac_linux":            false,
		"enable_result_dir":              true,
		"enable_agent_file_allow_list":   false,
		"is_delegate":                    false,
		"max_consecutive_web_tool_calls": 10,
		"CURRENT_TIME":                   time.Now().Format("2006-01-02 15:04:05 -07:00"),
		"REPO_ROOT":                      cfg.RepoRoot,
		"SKILL_REGISTRY_JSON":            registryJSON,
		"USER_PROFILE":                   readMemoryFileCapped(configuredMemoryRoot(cfg), "user.md", 3000),
		"USER_PROFILE_PATH":              "memory://user.md",
		"DATE_MEMORY_BLOCK":              readRecentDateMemory(cfg),
	}
}

// buildSkillRegistryJSON serializes the configured skill registry for the
// system template's {{ SKILL_REGISTRY_JSON|safe }} slot.
func buildSkillRegistryJSON(skills []SkillReg) string {
	if len(skills) == 0 {
		return "[]"
	}
	b, err := json.MarshalIndent(skills, "", "  ")
	if err != nil {
		return "[]"
	}
	return string(b)
}

func buildMergedSkillRegistryJSON(cfg *Config) string {
	if cfg == nil {
		return "[]"
	}
	return buildSkillRegistryJSON(DiscoverSkillRegistriesAll(cfg))
}

// ReadTextFile reads a UTF-8 file, strips BOM, returns content or empty

// readRecentDateMemory returns the most recent date-memory digests (last 7
// days) as a markdown block for the system prompt's background context.
func readRecentDateMemory(cfg *Config) string {
	root := configuredMemoryRoot(cfg)
	if strings.TrimSpace(root) == "" {
		return ""
	}
	dir := filepath.Join(root, "date-memory")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if len(name) != len("2006-01-02.md") || !strings.HasSuffix(name, ".md") {
			continue
		}
		if _, err := time.Parse("2006-01-02", name[:10]); err != nil {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > 7 {
		names = names[len(names)-7:]
	}
	var sb strings.Builder
	for _, name := range names {
		content := strings.TrimSpace(readTextFile(filepath.Join(dir, name)))
		if content == "" {
			continue
		}
		runes := []rune(content)
		if len(runes) > 4000 {
			content = string(runes[:4000]) + "…"
		}
		sb.WriteString("### " + name[:10] + "\n" + content + "\n\n")
	}
	out := strings.TrimSpace(sb.String())
	if out == "" {
		return ""
	}
	runes := []rune(out)
	if len(runes) > 6000 {
		out = string(runes[:6000]) + "…"
	}
	return out
}

func readMemoryFileCapped(memoryRoot, name string, maxRunes int) string {
	if strings.TrimSpace(memoryRoot) == "" {
		return ""
	}
	content := readTextFile(filepath.Join(memoryRoot, name))
	runes := []rune(content)
	if len(runes) > maxRunes {
		content = string(runes[:maxRunes])
	}
	return content
}
func readTextFile(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		data = data[3:]
	}
	return string(data)
}

// ReadModulePrompt reads an on-demand prompt from config/modules/<type>/<lang>.md.
func ReadModulePrompt(cfg *Config, promptType, lang string) string {
	if cfg == nil {
		return ""
	}
	p := filepath.Join(cfg.ModulesPath(), promptType, lang+".md")
	return readTextFile(p)
}

// ReadSummaryPrompt returns summary prompt
func ReadSummaryPrompt(cfg *Config) string {
	t := ReadModulePrompt(cfg, "summary", "zh")
	return t
}

// ReadGenerateTitlePrompt returns title generation prompt
func ReadGenerateTitlePrompt(cfg *Config) string {
	return ReadModulePrompt(cfg, "generate_title", "zh")
}

// ReadReflectionPrompt returns reflection prompt
func ReadReflectionPrompt(cfg *Config) string {
	return ReadModulePrompt(cfg, "reflection", "zh")
}

// ReadFinalizePrompt returns the subtask finalization prompt. It is used when
// a bounded subtask exhausts its step budget and still owes the parent agent a
// production-format <subtask_result>.
func ReadFinalizePrompt(cfg *Config) string {
	return ReadModulePrompt(cfg, "finalize", "zh")
}
