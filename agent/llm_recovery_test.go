package main

import (
	"errors"
	"testing"
)

func TestClassifyLLMCallErrorIsNarrow(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		status    int
		retryable bool
	}{
		{"gateway 504", errors.New("API error 504: Gateway Time-out"), 504, true},
		{"exact clotho bad request", errors.New(`API error 400: {"error":{"code":3,"message":"bad request","details":[]}}`), 400, true},
		{"ordinary 400", errors.New(`API error 400: {"error":{"message":"invalid tools"}}`), 400, false},
		{"ordinary 422", errors.New("API error 422: invalid request"), 422, false},
		{"transport timeout", errors.New("http call: context deadline exceeded (Client.Timeout exceeded)"), 0, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := classifyLLMCallError(test.err)
			if got.HTTPStatus != test.status || got.Retryable != test.retryable {
				t.Fatalf("classify=%+v, want status=%d retryable=%v", got, test.status, test.retryable)
			}
		})
	}
}

func TestClassifyLLMCallErrorDetectsContextLength(t *testing.T) {
	info := classifyLLMCallError(errors.New(`API error 400: {"error":{"message":"This model's maximum context length is 1048576 tokens. However, you requested 1536867 tokens"}}`))
	if info.Category != "llm_context_length" || info.HTTPStatus != 400 {
		t.Fatalf("context length error was not classified: %+v", info)
	}
}

func TestReasoningContentContractErrorCanUseFallback(t *testing.T) {
	err := errors.New(`API error 400: {"error":{"message":"The ` + "`reasoning_content`" + ` in the thinking mode must be passed back to the API."}}`)
	if !isReasoningContentContractError(err) {
		t.Fatalf("reasoning contract error was not recognized: %v", err)
	}
	if isReasoningContentContractError(errors.New("API error 400: invalid tools")) {
		t.Fatal("ordinary 400 must not trigger fallback as a reasoning contract error")
	}
}

func TestProviderFailuresOtherThanRateLimitsUseTheFallbackModel(t *testing.T) {
	// The turn that died at step 15 was killed by this one:
	//   http call: Post "https://api.minimaxi.com/v1/chat/completions":
	//   read tcp ...: read: connection reset by peer
	// It is retryable, the fallback model was configured, and nothing switched
	// because the code only asked about HTTP 429.
	tests := []struct {
		name   string
		err    error
		reason string
		ok     bool
	}{
		{"connection reset", errors.New(`http call: Post "https://api.minimaxi.com/v1/chat/completions": read tcp 10.0.0.1:1->2.2.2.2:443: read: connection reset by peer`), "provider_unavailable", true},
		{"timeout", errors.New("http call: context deadline exceeded (Client.Timeout exceeded)"), "provider_unavailable", true},
		{"gateway 503", errors.New("API error 503: Service Unavailable"), "provider_unavailable", true},
		{"provider 500", errors.New("API error 500: internal error"), "provider_unavailable", true},
		{"rate limit", errors.New("API error 429: too many requests"), "rate_limit", true},
		{"ordinary 400", errors.New(`API error 400: {"error":{"message":"invalid tools"}}`), "", false},
		{"no error", nil, "", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reason, text, ok := shouldFallbackToAnotherModel(test.err, classifyLLMCallError(test.err))
			if ok != test.ok || reason != test.reason {
				t.Fatalf("reason=%q ok=%v, want reason=%q ok=%v", reason, ok, test.reason, test.ok)
			}
			if ok && text == "" {
				t.Fatal("a fallback switch must tell the user why it happened")
			}
		})
	}
}

func TestCallConfiguredLLMWithRecoveryRetriesOnceOnlyWhenEnabled(t *testing.T) {
	calls := 0
	caller := func(*Config, []Message, []ToolDef) (*APIResponse, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("API error 504: Gateway Time-out")
		}
		return &APIResponse{}, nil
	}
	resp, err, recovered, initial := callConfiguredLLMWithRecovery(
		&Config{}, nil, nil, true, caller,
	)
	if err != nil || resp == nil || !recovered || !initial.Retryable || calls != 2 {
		t.Fatalf("resp=%v err=%v recovered=%v initial=%+v calls=%d", resp, err, recovered, initial, calls)
	}
}

func TestCallConfiguredLLMWithRecoveryDoesNotRetryMainOrPermanentError(t *testing.T) {
	for _, test := range []struct {
		name    string
		enabled bool
		err     error
	}{
		{"main task", false, errors.New("API error 504: Gateway Time-out")},
		{"permanent subtask error", true, errors.New("API error 422: invalid request")},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			caller := func(*Config, []Message, []ToolDef) (*APIResponse, error) {
				calls++
				return nil, test.err
			}
			_, _, recovered, _ := callConfiguredLLMWithRecovery(
				&Config{}, nil, nil, test.enabled, caller,
			)
			if recovered || calls != 1 {
				t.Fatalf("recovered=%v calls=%d", recovered, calls)
			}
		})
	}
}

func TestCallConfiguredLLMWithRecoveryStopsAfterSecondFailure(t *testing.T) {
	calls := 0
	caller := func(*Config, []Message, []ToolDef) (*APIResponse, error) {
		calls++
		return nil, errors.New("API error 504: Gateway Time-out")
	}
	_, err, recovered, initial := callConfiguredLLMWithRecovery(
		&Config{}, nil, nil, true, caller,
	)
	if err == nil || !recovered || !initial.Retryable || calls != 2 {
		t.Fatalf("err=%v recovered=%v initial=%+v calls=%d", err, recovered, initial, calls)
	}
}

func TestActivateFallbackModelSwitchesOnlyToConfiguredModel(t *testing.T) {
	cfg := &Config{
		DefaultModel: "primary",
		Models: map[string]ModelProfile{
			"primary": {API: APIConfig{Model: "primary-model", APIKey: "primary-key"}},
			"backup":  {API: APIConfig{Model: "backup-model", APIKey: "backup-key"}},
			"missing": {API: APIConfig{Model: "missing-model", APIKey: "READ_FROM_MISSING_KEY_TXT"}},
		},
	}
	if err := cfg.SelectModel("primary"); err != nil {
		t.Fatal(err)
	}
	if previous, ok := activateFallbackModel(cfg, "backup"); !ok || previous != "primary" || cfg.SelectedModelID != "backup" {
		t.Fatalf("fallback switch failed: previous=%q ok=%v selected=%q", previous, ok, cfg.SelectedModelID)
	}
	if _, ok := activateFallbackModel(cfg, "backup"); ok {
		t.Fatal("must not switch to the already active fallback model")
	}
	if err := cfg.SelectModel("primary"); err != nil {
		t.Fatal(err)
	}
	if _, ok := activateFallbackModel(cfg, "missing"); ok {
		t.Fatal("must not switch to a model without a resolved API key")
	}
}
