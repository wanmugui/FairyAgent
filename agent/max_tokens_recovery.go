package main

import "strings"

const maxTokensRetryMultiplier = 2

// responseHitMaxTokens prefers the provider's terminal reason. Some gateways
// omit it, so usage is only a compatibility fallback when the reason is absent
// or unknown. An explicit normal stop must never be retried merely because the
// completion count happens to equal the requested budget.
func responseHitMaxTokens(response *APIResponse, requestedMaxTokens int) bool {
	if response == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(response.FinishStop)) {
	case "length", "max_tokens", "max_tokens_exceeded":
		return true
	case "stop", "tool_calls", "end_turn":
		return false
	}
	return requestedMaxTokens > 0 && response.Usage != nil &&
		response.Usage.CompletionTokens >= requestedMaxTokens
}

func doubledMaxTokens(maxTokens int) (int, bool) {
	if maxTokens <= 0 || maxTokens > int(^uint(0)>>1)/maxTokensRetryMultiplier {
		return 0, false
	}
	return maxTokens * maxTokensRetryMultiplier, true
}

// configWithMaxTokens copies the call configuration so a temporary escalation
// cannot leak into the next logical model turn.
func configWithMaxTokens(cfg *Config, maxTokens int) *Config {
	copy := *cfg
	copy.API = cfg.API
	copy.API.MaxTokens = maxTokens
	return &copy
}
