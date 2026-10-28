package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestManualTriggerCompaction 手动触发一次真实的上下文压缩。
// 用真实配置、真实模型、真实会话消息，验证压缩链路真的能跑通。
func TestManualTriggerCompaction(t *testing.T) {
	// 走真实的配置加载路径（LoadConfig 内部会解析 api_key 的间接引用，
	// 直接 json.Unmarshal 拿不到密钥，摘要模型会 401）。
	cfg, err := LoadConfig("..", "config/config.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.SelectModel("user_minimax-m3-1-flash"); err != nil {
		t.Fatal(err)
	}
	summaryPrompt, err := os.ReadFile("../config/modules/summary/zh.md")
	if err != nil {
		t.Fatal(err)
	}

	// 构造一段足够长的历史：模拟真实会话里被工具结果撑大的上下文。
	// tool 消息必须带 tool_call_id 并且前面有配对的 assistant tool_calls，
	// 否则摘要模型会拒绝这个请求体（422 missing field tool_call_id）。
	var msgs []Message
	msgs = append(msgs, NewMessage("system", "你是 Fairy，一个在真实操作系统上运行的助手。", nil, "", ""))
	for i := 0; i < 40; i++ {
		user := fmt.Sprintf("第 %d 轮提问：请检查第 %d 号配置文件的读取链路，并给出结论。", i, i)
		msgs = append(msgs, NewMessage("user", user, nil, "", ""))
		callID := fmt.Sprintf("call_%03d", i)
		calls := []ToolCall{{
			ID:   callID,
			Type: "function",
			Function: ToolCallFunc{
				Name:      "bash",
				Arguments: fmt.Sprintf(`{"command":"cat config-%d.json"}`, i),
			},
		}}
		msgs = append(msgs, NewMessage("assistant", "", calls, "", ""))
		toolOut := fmt.Sprintf(`{"tool":"bash","round":%d,"output":%q}`, i, strings.Repeat("配置项读取正常，链路完整，无异常。", 60))
		msgs = append(msgs, NewMessage("tool", toolOut, nil, callID, ""))
		msgs = append(msgs, NewMessage("assistant", fmt.Sprintf("第 %d 轮我已经查完了，结论如上。", i), nil, "", ""))
	}

	before := estimateMessagesContextTokens(msgs)
	threshold := effectiveContextBudget(cfg)
	t.Logf("压缩前: 消息数=%d 估算 tokens=%d 生效阈值=%d", len(msgs), before, threshold)

	res := preflightWorkingContext(msgs, cfg, string(summaryPrompt), nil, nil, true, 1.0)
	after := estimateMessagesContextTokens(res.Messages)
	t.Logf("压缩后: 消息数=%d 估算 tokens=%d Summarized=%v Degraded=%v", len(res.Messages), after, res.Summarized, res.Degraded)
	if res.DegradeReason != "" {
		t.Logf("DegradeReason: %s", res.DegradeReason)
	}
	if res.SummaryMessage != nil {
		s := res.SummaryMessage.Content
		if len(s) > 400 {
			s = s[:400] + "..."
		}
		t.Logf("摘要消息(前 400 字): %s", s)
	}
	if !res.Summarized {
		t.Fatalf("压缩未触发")
	}
	if after >= before {
		t.Fatalf("压缩后体积没有下降: before=%d after=%d", before, after)
	}
	t.Logf("压缩比: %d -> %d (降 %.1f%%)", before, after, float64(before-after)/float64(before)*100)
}
