package main

import "testing"

func TestRepairIntermediateContentPreservesModelText(t *testing.T) {
	cases := []string{
		"正在处理",
		"<msg>正在处理</msg>",
	}
	for _, input := range cases {
		if got := RepairIntermediateContent(input); got != input {
			t.Fatalf("RepairIntermediateContent(%q) = %q", input, got)
		}
	}
}

func TestGetUserVisibleTextPreservesMsgTag(t *testing.T) {
	input := "<msg>正在处理</msg>"
	if got := GetUserVisibleText(input); got != input {
		t.Fatalf("GetUserVisibleText(%q) = %q", input, got)
	}
	if got := GetUserVisibleText("<report>最终内容</report>"); got != "最终内容" {
		t.Fatalf("report extraction = %q", got)
	}
}

func TestFinalTurnAllowsPlainTextAndReport(t *testing.T) {
	for _, input := range []string{"已完成。", "<report># 结果\n\n完成。</report>"} {
		result := CheckFinalTurnCompliant(input)
		if !result.IsCompliant {
			t.Fatalf("final content %q should be compliant: %v", input, result.Violations)
		}
	}
}

func TestFinalTurnRequiresVisibleText(t *testing.T) {
	for _, input := range []string{
		"",
		"<think>只有内部推理</think>",
		"<reflection>只有反思</reflection>",
		"<msg></msg>",
		"<summary>内部压缩检查点，不是最终回复</summary>",
	} {
		if HasVisibleFinalContent(input) {
			t.Fatalf("final content %q should not count as a visible answer", input)
		}
	}
	for _, input := range []string{"完成。", "<msg>完成</msg>", "<report>结果</report>"} {
		if !HasVisibleFinalContent(input) {
			t.Fatalf("final content %q should count as a visible answer", input)
		}
	}
}

func TestMalformedMiniMaxThinkingCloseTag(t *testing.T) {
	input := "<thinking>先想一下</mm:think>正文"
	if got := extractThinking(input); got != "先想一下" {
		t.Fatalf("extractThinking(%q) = %q", input, got)
	}
	if got := stripThinking(input); got != "正文" {
		t.Fatalf("stripThinking(%q) = %q", input, got)
	}
}

func TestIntermediateTurnAllowsPlainTextAndMsg(t *testing.T) {
	for _, input := range []string{"", "正在处理", "<msg>正在处理</msg>"} {
		result := CheckIntermediateTurnCompliant(input)
		if !result.IsCompliant {
			t.Fatalf("intermediate content %q should be compliant: %v", input, result.Violations)
		}
	}
	if result := CheckIntermediateTurnCompliant("<report>不应提前交付</report>"); result.IsCompliant {
		t.Fatal("report in intermediate turn must be rejected")
	}
}

func TestLegacyProtocolIsNotRecognized(t *testing.T) {
	input := "<process><message>旧协议</message></process>"
	if got := RepairIntermediateContent(input); got != input {
		t.Fatalf("legacy tags should pass through unchanged, got %q", got)
	}
	if HasReportTag(input) {
		t.Fatal("legacy process content must not be treated as report")
	}
}
