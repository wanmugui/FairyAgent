package main

import "testing"

func TestShouldNudgeToExecuteDetectsTutorialAnswer(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: "你能修改我们这个项目前端的侧边栏，让他能收起折叠吗？"},
		{Role: "assistant", Content: "<report>要实现前端侧边栏的可折叠功能，通常需要在前端代码中进行一些修改。假设前提是使用 React 和 Ant Design...</report>"},
	}
	if !shouldNudgeToExecute(messages, messages[1].Content, 0) {
		t.Fatal("expected actionable tutorial to trigger nudge")
	}
}

func TestShouldNudgeToExecuteSkipsExplanationRequests(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: "解释一下如何实现侧边栏折叠"},
		{Role: "assistant", Content: "通常需要给 Sidebar 加一个 collapsed 状态。"},
	}
	if shouldNudgeToExecute(messages, messages[1].Content, 0) {
		t.Fatal("explanation request should not trigger nudge")
	}
}

func TestShouldNudgeToExecuteDetectsPlanAnnouncement(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: "给侧边栏加一个删除分支会话按钮"},
	}
	content := "结构清楚了。开始实施 delete branch session 按钮。先 update plan，再下代码。"
	if !shouldNudgeToExecute(messages, content, 0) {
		t.Fatal("plan announcement without tool calls should trigger nudge")
	}
}

func TestShouldNudgeToExecuteSkipsCompletedWork(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: "给侧边栏加一个删除分支会话按钮"},
	}
	if shouldNudgeToExecute(messages, "已完成删除分支会话按钮，并验证构建通过。", 0) {
		t.Fatal("completed work should not trigger nudge")
	}
}
