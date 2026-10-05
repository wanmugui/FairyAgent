package main

import (
	"errors"
	"testing"
)

func TestProbeReasoningFallback(t *testing.T) {
	err := errors.New("API error 400: {\"error\":{\"message\":\"The `reasoning_content` in the thinking mode must be passed back to the API.\",\"code\":\"invalid_request_error\"}}")
	info := classifyLLMCallError(err)
	reason, text, ok := shouldFallbackToAnotherModel(err, info)
	t.Logf("分类: %+v | 判定: ok=%v reason=%q text=%q", info, ok, reason, text)
	if !ok {
		t.Fatalf("这个错误应该触发备用模型")
	}
}

func TestProbeActivateFallback(t *testing.T) {
	cfg, err := LoadConfig("/home/user/Fairy", "/home/user/Fairy/config/config.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.SelectModel("user_minimax-m3-1-flash"); err != nil {
		t.Fatal(err)
	}
	t.Logf("主模型=%s key=%s", cfg.SelectedModelID, mask(cfg.API.APIKey))
	cfg.FallbackModel = "deepseek-v4-flash"
	prev, switched := activateFallbackModel(cfg, cfg.FallbackModel)
	t.Logf("切换: switched=%v prev=%s now=%s key=%s", switched, prev, cfg.SelectedModelID, mask(cfg.API.APIKey))
	if !switched {
		t.Fatalf("切不过去：说明 SelectModel 或 key 解析有问题")
	}
}

func mask(k string) string {
	if len(k) > 12 {
		return k[:12] + "…(len=" + string(rune('0'+len(k)%10)) + ")"
	}
	return k
}
