package main

import (
	"regexp"
	"strconv"
	"strings"
)

type llmCallErrorInfo struct {
	Category   string
	HTTPStatus int
	Retryable  bool
}

type configuredLLMCaller func(*Config, []Message, []ToolDef) (*APIResponse, error)

func classifyLLMCallError(err error) llmCallErrorInfo {
	info := llmCallErrorInfo{Category: "llm_api_error"}
	if err == nil {
		return info
	}
	message := err.Error()
	lower := strings.ToLower(message)
	if strings.Contains(lower, "maximum context length") ||
		strings.Contains(lower, "context length") ||
		strings.Contains(lower, "context_length_exceeded") ||
		strings.Contains(lower, "too many tokens") {
		info.Category = "llm_context_length"
		if match := regexp.MustCompile(`API error (\d{3}):`).FindStringSubmatch(message); len(match) == 2 {
			info.HTTPStatus, _ = strconv.Atoi(match[1])
		}
		return info
	}
	if match := regexp.MustCompile(`API error (\d{3}):`).FindStringSubmatch(message); len(match) == 2 {
		info.HTTPStatus, _ = strconv.Atoi(match[1])
		info.Retryable = info.HTTPStatus == 408 || info.HTTPStatus == 429 ||
			info.HTTPStatus == 502 || info.HTTPStatus == 503 || info.HTTPStatus == 504
		if info.HTTPStatus == 400 &&
			strings.Contains(message, `"code":3`) &&
			strings.Contains(message, `"message":"bad request"`) &&
			strings.Contains(message, `"details":[]`) {
			info.Retryable = true
		}
		return info
	}

	if strings.Contains(lower, "http call:") && (strings.Contains(lower, "timeout") ||
		strings.Contains(lower, "connection reset") ||
		strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "unexpected eof") ||
		strings.HasSuffix(lower, "eof")) {
		info.Category = "llm_transport_error"
		info.Retryable = true
	}
	return info
}

func isReasoningContentContractError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "reasoning_content") &&
		(strings.Contains(lower, "must be passed back") ||
			strings.Contains(lower, "must be sent back") ||
			strings.Contains(lower, "thinking mode"))
}

// shouldFallbackToAnotherModel decides whether a failed call is the *provider's*
// fault, so that reaching the configured secondary model is a real option.
//
// It used to ask only "was this a 429", which is the narrowest form of the
// question. A provider that resets the connection mid-stream, times out, or
// answers 5xx has failed just as completely, and those errors carry no HTTP
// status to match on - so a turn died at step 15 with the fallback model
// configured, reachable, and never used. The classification one line up already
// knows which failures are worth another attempt; that judgement is what the
// fallback should follow.
func shouldFallbackToAnotherModel(err error, info llmCallErrorInfo) (reason, statusText string, ok bool) {
	switch {
	case info.HTTPStatus == 429:
		return "rate_limit", "主模型触发速率限制，正在切换到备用模型继续本轮...", true
	case isReasoningContentContractError(err):
		return "reasoning_content_contract", "主模型拒绝当前推理历史，正在切换到备用模型继续本轮...", true
	case info.Retryable || info.HTTPStatus >= 500:
		return "provider_unavailable",
			"主模型接口不稳定（" + info.Category + "），正在切换到备用模型继续本轮...", true
	}
	return "", "", false
}

func activateFallbackModel(cfg *Config, fallbackModel string) (string, bool) {
	if cfg == nil {
		return "", false
	}
	id := strings.TrimSpace(fallbackModel)
	if id == "" || id == cfg.SelectedModelID {
		return "", false
	}
	previous := cfg.SelectedModelID
	if err := cfg.SelectModel(id); err != nil {
		return "", false
	}
	key := strings.TrimSpace(cfg.API.APIKey)
	if key == "" || strings.HasPrefix(key, "READ_FROM_") {
		_ = cfg.SelectModel(previous)
		return "", false
	}
	return previous, true
}

func callConfiguredLLMWithRecovery(
	cfg *Config,
	messages []Message,
	tools []ToolDef,
	enabled bool,
	caller configuredLLMCaller,
) (*APIResponse, error, bool, llmCallErrorInfo) {
	resp, err := caller(cfg, messages, tools)
	info := classifyLLMCallError(err)
	if err == nil || !enabled || !info.Retryable {
		return resp, err, false, info
	}
	resp, err = caller(cfg, messages, tools)
	return resp, err, true, info
}
