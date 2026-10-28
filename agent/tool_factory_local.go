package main

import (
	"agentloop/agent/internal/biz/tool/builtin"
	localtool "agentloop/agent/internal/biz/tool/local"
	"agentloop/agent/internal/biz/tool/webfetch"
	"agentloop/agent/internal/biz/tool/websearch"
	"fmt"
	"os"
	"strings"
	"time"
)

// builtinLocalToolBuilders returns the local and builtin tools that run within
// this Agent process. Service-backed tools are constructed by the HTTP backend.
func builtinLocalToolBuilders() map[string]LocalToolBuilder {
	return map[string]LocalToolBuilder{
		"read_file": func(schema ToolDef, cfg *Config) (Tool, error) {
			return builtin.NewLocalReadFileToolWithConfig(schema, builtin.ReadFileToolConfig{
				SegmentReadMaxTokens: cfg.Tools.ReadFile.SegmentReadMaxTokens,
				SegmentReadMinTokens: cfg.Tools.ReadFile.SegmentReadMinTokens,
				MaxReadFileSizeBytes: cfg.Tools.ReadFile.MaxReadFileSizeBytes,
				SkillsRoot:           configuredSkillsRoot(cfg),
				MemoryRoot:           configuredMemoryRoot(cfg),
			}), nil
		},
		"write_file": func(schema ToolDef, cfg *Config) (Tool, error) {
			return builtin.NewLocalWriteFileToolWithConfig(schema, builtin.WritableFileToolConfig{MemoryRoot: configuredMemoryRoot(cfg)}), nil
		},
		"edit_file": func(schema ToolDef, cfg *Config) (Tool, error) {
			return builtin.NewLocalEditFileToolWithConfig(schema, builtin.WritableFileToolConfig{MemoryRoot: configuredMemoryRoot(cfg)}), nil
		},
		"glob": func(schema ToolDef, cfg *Config) (Tool, error) {
			return builtin.NewLocalGlobToolWithConfigAndMemory(schema, configuredSkillsRoot(cfg), configuredMemoryRoot(cfg)), nil
		},
		"grep": func(schema ToolDef, _ *Config) (Tool, error) {
			return builtin.NewLocalGrepTool(schema), nil
		},
		"get_current_time": func(schema ToolDef, _ *Config) (Tool, error) {
			return builtin.NewLocalTimeTool(schema), nil
		},
		"ask_user": func(schema ToolDef, _ *Config) (Tool, error) {
			return builtin.NewLocalAskUserTool(schema), nil
		},
		"skill_search": func(schema ToolDef, cfg *Config) (Tool, error) {
			return builtin.NewLocalSkillSearchTool(schema, configuredSkillsRoot(cfg)), nil
		},
		"memory_search": func(schema ToolDef, cfg *Config) (Tool, error) {
			return builtin.NewLocalMemorySearchTool(schema, configuredMemoryRoot(cfg)), nil
		},
		"memory_invalidate_segment": func(schema ToolDef, cfg *Config) (Tool, error) {
			return NewSegmentedMemoryManagementTool("memory_invalidate_segment", schema, cfg), nil
		},
		"memory_invalidate_interaction": func(schema ToolDef, cfg *Config) (Tool, error) {
			return NewSegmentedMemoryManagementTool("memory_invalidate_interaction", schema, cfg), nil
		},
		"memory_delete_segment": func(schema ToolDef, cfg *Config) (Tool, error) {
			return NewSegmentedMemoryManagementTool("memory_delete_segment", schema, cfg), nil
		},
		"memory_delete_interaction": func(schema ToolDef, cfg *Config) (Tool, error) {
			return NewSegmentedMemoryManagementTool("memory_delete_interaction", schema, cfg), nil
		},
		"create_subtask": func(schema ToolDef, cfg *Config) (Tool, error) {
			return localtool.NewLocalCreateSubtaskTool(schema, localToolConfig(cfg)), nil
		},
		"bash": func(schema ToolDef, cfg *Config) (Tool, error) {
			return localtool.NewLocalBashTool(schema, localToolConfig(cfg)), nil
		},
		"powershell": func(schema ToolDef, cfg *Config) (Tool, error) {
			return localtool.NewLocalPowerShellTool(schema, localToolConfig(cfg)), nil
		},
		"bash_job": func(schema ToolDef, cfg *Config) (Tool, error) {
			return localtool.NewLocalBashJobTool(schema, localToolConfig(cfg)), nil
		},
		"document_parser": func(schema ToolDef, cfg *Config) (Tool, error) {
			return localtool.NewLocalDocumentParserTool(schema, localToolConfig(cfg)), nil
		},
		"web_search": func(schema ToolDef, cfg *Config) (Tool, error) {
			budget := effectiveWebBudget(cfg)
			return websearch.NewToolWithOutputOptions(schema, websearch.OutputOptions{
				MaxResults:      budget.SearchMaxResults,
				MaxSnippetChars: budget.SearchSnippetChars,
			}), nil
		},
		"web_fetch": func(schema ToolDef, cfg *Config) (Tool, error) {
			budget := effectiveWebBudget(cfg)
			opts := webfetch.DefaultOptions()
			opts.MaxBodyChars = budget.FetchMaxChars
			opts.SummaryChars = budget.FetchSummaryChars
			return webfetch.NewToolWithOptions(schema, opts), nil
		},
		"image_search": func(schema ToolDef, cfg *Config) (Tool, error) {
			return websearch.NewImageToolWithOptions(schema, imageSearchOptions(cfg)), nil
		},
		"image_generate": func(schema ToolDef, cfg *Config) (Tool, error) {
			return localtool.NewLocalImageGenerateTool(schema, localToolConfig(cfg)), nil
		},
		"show_result": func(schema ToolDef, cfg *Config) (Tool, error) {
			root := ""
			if cfg != nil {
				root = cfg.RepoRoot
			}
			return builtin.NewLocalShowResultTool(schema, root), nil
		},
		"plan": func(schema ToolDef, cfg *Config) (Tool, error) {
			workspace := ""
			if cfg != nil {
				workspace = cfg.WorkspaceDir
			}
			return builtin.NewLocalPlanTool(schema, workspace), nil
		},
		"html_to_png": func(schema ToolDef, cfg *Config) (Tool, error) {
			return localtool.NewLocalHTMLToPNGTool(schema, localToolConfig(cfg)), nil
		},
		"image_vqa": func(schema ToolDef, cfg *Config) (Tool, error) {
			return localtool.NewLocalImageVQATool(schema, localToolConfig(cfg)), nil
		},
		"computer_observe": func(schema ToolDef, cfg *Config) (Tool, error) {
			return localtool.NewLocalComputerTool("computer_observe", schema, localToolConfig(cfg)), nil
		},
		"computer_pointer": func(schema ToolDef, cfg *Config) (Tool, error) {
			return localtool.NewLocalComputerTool("computer_pointer", schema, localToolConfig(cfg)), nil
		},
		"computer_keyboard": func(schema ToolDef, cfg *Config) (Tool, error) {
			return localtool.NewLocalComputerTool("computer_keyboard", schema, localToolConfig(cfg)), nil
		},
		"computer_window": func(schema ToolDef, cfg *Config) (Tool, error) {
			return localtool.NewLocalComputerTool("computer_window", schema, localToolConfig(cfg)), nil
		},
		"computer_clipboard": func(schema ToolDef, cfg *Config) (Tool, error) {
			return localtool.NewLocalComputerTool("computer_clipboard", schema, localToolConfig(cfg)), nil
		},
		"browser": func(schema ToolDef, cfg *Config) (Tool, error) {
			return localtool.NewLocalBrowserTool(schema, localToolConfig(cfg)), nil
		},
	}
}

// imageSearchOptions turns the on-disk imageSearch config into runtime search
// options. Nil or partial config falls back to the multi-source defaults.
func imageSearchOptions(cfg *Config) websearch.ImageOptions {
	options := websearch.DefaultImageOptions()
	if cfg == nil {
		return options
	}
	imageSearch := cfg.Tools.ImageSearch
	if len(imageSearch.SourcePriority) > 0 {
		options.SourcePriority = imageSearch.SourcePriority
	}
	if imageSearch.TimeoutSec > 0 {
		options.Timeout = time.Duration(imageSearch.TimeoutSec) * time.Second
	}
	if imageSearch.MaxImageBytes > 0 {
		options.MaxImageBytes = imageSearch.MaxImageBytes
	}
	return options
}

// configuredSkillsRoot returns every skills root the local tools may read, in
// priority order, joined with os.PathListSeparator.
//
// It stays a single string on purpose: read_file, glob, bash and skill_search
// all take a plain string parameter, and shared.SplitRoots expands this spec
// back into the individual roots at the point of use. A configured SkillsDir
// still comes first, so a deployment with no user-level roots behaves exactly
// as before.
func configuredSkillsRoot(cfg *Config) string {
	var roots []string
	if cfg != nil && strings.TrimSpace(cfg.SkillsDir) != "" {
		roots = append(roots, cfg.ResolvePath(cfg.SkillsDir))
	}
	roots = append(roots, userLevelSkillRoots()...)
	return strings.Join(roots, string(os.PathListSeparator))
}

func configuredMemoryRoot(cfg *Config) string {
	if cfg == nil || strings.TrimSpace(cfg.MemoryDir) == "" {
		return ""
	}
	return cfg.ResolvePath(cfg.MemoryDir)
}

func localToolConfig(cfg *Config) *localtool.Config {
	if cfg == nil {
		return &localtool.Config{}
	}
	return &localtool.Config{
		RepoRoot:        cfg.RepoRoot,
		ConfigPath:      cfg.ConfigPath,
		SelectedModelID: cfg.SelectedModelID,
		SkillsRoot:      configuredSkillsRoot(cfg),
		UseMock:         cfg.UseMock,
		BashPolicy:      resolveBashPolicy(cfg.BashPolicy),
		PptTools: localtool.PptToolsConfig{
			BaseURL: cfg.Tools.PptTools.BaseUrl,
			APIPath: cfg.Tools.PptTools.ApiPath,
			HostPin: cfg.Tools.PptTools.HostPin,
		},
		DocumentParser: localtool.DocumentParserConfig{
			OutputMaxTokens:        cfg.Tools.DocumentParser.OutputMaxTokens,
			OutputTruncateStrategy: cfg.Tools.DocumentParser.OutputTruncateStrategy,
		},
		ImageGenerate: localtool.ImageGenerateConfig{
			ModelName:     cfg.Tools.ImageGenerate.ModelName,
			BaseURL:       cfg.Tools.ImageGenerate.BaseURL,
			APIKey:        cfg.Tools.ImageGenerate.APIKey,
			AspectRatio:   cfg.Tools.ImageGenerate.AspectRatio,
			TimeoutSec:    cfg.Tools.ImageGenerate.TimeoutSec,
			MaxRetries:    cfg.Tools.ImageGenerate.MaxRetries,
			RetryBaseMs:   cfg.Tools.ImageGenerate.RetryBaseMs,
			MaxImages:     cfg.Tools.ImageGenerate.MaxImages,
			MaxImageBytes: cfg.Tools.ImageGenerate.MaxImageBytes,
		},
		ImageVQA: localtool.ImageVQAConfig{
			ModelName:     cfg.Tools.ImageVQA.ModelName,
			BaseURL:       cfg.Tools.ImageVQA.BaseURL,
			APIKey:        cfg.Tools.ImageVQA.APIKey,
			Mode:          cfg.Tools.ImageVQA.Mode,
			TimeoutSec:    cfg.Tools.ImageVQA.TimeoutSec,
			MaxTokens:     cfg.Tools.ImageVQA.MaxTokens,
			Temperature:   cfg.Tools.ImageVQA.Temperature,
			MaxRetries:    cfg.Tools.ImageVQA.MaxRetries,
			RetryBaseMs:   cfg.Tools.ImageVQA.RetryBaseMs,
			MaxImageBytes: cfg.Tools.ImageVQA.MaxImageBytes,
		},
		ToolRuntime: &localtool.ToolRuntimeConfig{Executables: localtool.LocalExecutableConfig{
			Python:  cfg.ToolRuntime.Executables.Python,
			Shell:   cfg.ToolRuntime.Executables.Shell,
			Node:    cfg.ToolRuntime.Executables.Node,
			Browser: cfg.ToolRuntime.Executables.Browser,
		}},
		BuildSubtaskPrompt: func(task string) (string, error) {
			return renderLocalSubtaskPrompt(cfg, task)
		},
	}
}

// resolveBashPolicy turns the on-disk BashPolicyConfig into a runtime
// localtool.BashPolicy. When the policy is disabled we return an empty
// (allow-all) policy so the agent has zero friction.
func resolveBashPolicy(cfg BashPolicyConfig) localtool.BashPolicy {
	if !cfg.Enabled {
		return localtool.BashPolicy{}
	}
	denyCommands := cfg.DenyCommands
	denyPaths := cfg.DenyPaths
	if !cfg.StrictDenyOnly && len(denyCommands) == 0 && len(denyPaths) == 0 {
		// Use the conservative defaults when the user enabled the policy
		// but didn't customize it. This matches the documented "open by
		// default unless you turn it on" semantics.
		def := localtool.DefaultBashPolicy()
		denyCommands = def.DenyCommands
		denyPaths = def.DenyPaths
	}
	return localtool.BashPolicy{
		AllowCommands: cfg.AllowCommands,
		DenyCommands:  denyCommands,
		DenyPaths:     denyPaths,
	}
}

func renderLocalSubtaskPrompt(cfg *Config, task string) (string, error) {
	template := readTextFile(cfg.SubtaskUserPath())
	if strings.TrimSpace(template) == "" {
		return task + "\n\n请在 <subtask_result> 中返回可核验的成果。", nil
	}
	vars := systemTemplateVars(cfg, buildMergedSkillRegistryJSON(cfg))
	vars["Task"] = task
	rendered, err := renderJinja(template, vars)
	if err != nil {
		return "", fmt.Errorf("render subtask prompt: %w", err)
	}
	return strings.TrimSpace(rendered), nil
}
