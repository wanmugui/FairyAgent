package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExtractInlineToolCallsParsesTextCall(t *testing.T) {
	content := "<report>接下来我需要修改文件。\n```typescript\nfunctions.read_file({\"file_path\": \"local://frontend/dist/index.html\"})\n```\n</report>"
	calls, cleaned := extractInlineToolCalls(content)
	if len(calls) != 1 {
		t.Fatalf("expected 1 inline call, got %d", len(calls))
	}
	if calls[0].Function.Name != "read_file" {
		t.Fatalf("unexpected tool name: %q", calls[0].Function.Name)
	}
	if !strings.Contains(calls[0].Function.Arguments, "local://frontend/dist/index.html") {
		t.Fatalf("unexpected args: %q", calls[0].Function.Arguments)
	}
	if strings.Contains(cleaned, "functions.read_file") {
		t.Fatalf("cleaned content still contains pseudo call: %q", cleaned)
	}
	if !strings.Contains(cleaned, "<report>") || !strings.Contains(cleaned, "</report>") {
		t.Fatalf("<report> must remain untouched, got: %q", cleaned)
	}
}

func TestExtractInlineToolCallsSkipsProse(t *testing.T) {
	calls, cleaned := extractInlineToolCalls("我建议先读取文件。")
	if len(calls) != 0 {
		t.Fatalf("unexpected calls: %#v", calls)
	}
	if cleaned != "我建议先读取文件。" {
		t.Fatalf("prose changed: %q", cleaned)
	}
}

func TestExtractInlineToolCallsParsesWriteFileCodeFenceWithWindowsPath(t *testing.T) {
	content := "你可以保存这个计划：\n\n```typescript\n" +
		"functions.write_file({\"file_path\": \"D:\\\\Fairy\\\\workspace\\\\result\\\\xinjiang_travel_plan.txt\", " +
		"\"content\": \"### 新疆9日游详细行程计划\n\n#### 第1天\n- 上午：抵达乌鲁木齐\"})\n" +
		"```\n\n文件路径为：D:\\\\Fairy\\\\workspace\\\\result\\\\xinjiang_travel_plan.txt"
	calls, cleaned := extractInlineToolCalls(content)
	if len(calls) != 1 {
		t.Fatalf("expected 1 inline call, got %d; cleaned=%q", len(calls), cleaned)
	}
	if calls[0].Function.Name != "write_file" {
		t.Fatalf("unexpected tool name: %q", calls[0].Function.Name)
	}
	if strings.Contains(cleaned, "functions.write_file") {
		t.Fatalf("cleaned content still contains pseudo call: %q", cleaned)
	}
	if !json.Valid([]byte(calls[0].Function.Arguments)) {
		t.Fatalf("repaired arguments are not valid JSON: %q", calls[0].Function.Arguments)
	}
}
