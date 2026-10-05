package main

import (
	"strconv"

	"agentloop/agent/internal/dtypes"
)

// effectiveWebBudget returns the configured dynamic web budget with defaults
// applied. A nil config keeps the defaults because web_budget is a runtime
// optimization, not a required config section.
func effectiveWebBudget(cfg *Config) WebBudgetToolConfig {
	if cfg == nil {
		return WebBudgetToolConfig{}.withDefaults()
	}
	return cfg.Tools.WebBudget.withDefaults()
}

func webBudgetEnabled(cfg *Config) bool {
	return effectiveWebBudget(cfg).IsEnabled()
}

// dynamicWebBudgetTokens mirrors the useful part of Codex's search budget
// model: first reserve model output and safety space, then cap by the
// conversation compression threshold, and finally allocate only a share of
// what remains to the current web step. The caller splits this across parallel
// web calls, so one burst of searches cannot consume the whole context.
func dynamicWebBudgetTokens(cfg *Config, promptTokens int) int {
	budgetCfg := effectiveWebBudget(cfg)
	if !budgetCfg.IsEnabled() {
		return 0
	}
	if promptTokens < 0 {
		promptTokens = 0
	}

	contextRemaining := budgetCfg.ContextWindowTokens - promptTokens - budgetCfg.OutputReserveTokens - budgetCfg.SafetyReserveTokens
	softRemaining := contextRemaining
	if cfg != nil && cfg.SummaryThresholdTokens > 0 {
		softRemaining = cfg.SummaryThresholdTokens - promptTokens
	}
	remaining := contextRemaining
	if softRemaining < remaining {
		remaining = softRemaining
	}
	if remaining <= 0 {
		return budgetCfg.MinTurnTokens
	}

	budget := remaining * budgetCfg.TurnSharePercent / 100
	if budget < budgetCfg.MinTurnTokens {
		budget = budgetCfg.MinTurnTokens
	}
	if budget > budgetCfg.MaxTurnTokens {
		budget = budgetCfg.MaxTurnTokens
	}
	return budget
}

func webSearchContextSize(tokens int) string {
	switch {
	case tokens >= 24000:
		return "high"
	case tokens >= 8000:
		return "medium"
	default:
		return "low"
	}
}

func webBudgetMetadata(tokens int) map[string]string {
	if tokens <= 0 {
		return nil
	}
	return map[string]string{
		dtypes.ToolMetadataWebBudgetTokens: strconv.Itoa(tokens),
	}
}

func isWebTextTool(name string) bool {
	return name == "web_search" || name == "web_fetch"
}
