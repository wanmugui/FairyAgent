package main

import "testing"

// A resumed interaction after a detached subtask finishes arrives as a
// runtime-generated turn. It must always see the active plan, otherwise the
// model loses the remaining skill steps and stops mid-flow. Regression: the
// normal "continuation cue" heuristic only matched short user messages, so a
// long runtime event suppressed plan injection.
func TestPlanRelatesToRuntimeContinuationEvent(t *testing.T) {
	items := []planItemForLLM{{ID: "1", Status: "pending", Action: "汇总子任务结论并生成最终报告"}}
	question := "整理川西大环线自驾攻略"
	continuation := "[会话状态] 后台子任务已完成。这不是用户的新请求，而是当前交互的异步工具结果。\n\n" +
		"## 已完成的后台任务\n\n### 核查沿途路况\nstatus: completed\nresult: 折多山段已恢复通行"
	if !planRelatesToMessage(question, items, continuation) {
		t.Fatalf("runtime continuation must always surface the active plan")
	}

	if planRelatesToMessage(question, items, "帮我看看明天上海的天气怎么样") {
		t.Fatalf("an unrelated request must not pull in a stale plan")
	}
}
