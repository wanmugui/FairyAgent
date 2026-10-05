package main

import (
	"strings"
	"testing"
)

func TestVisibleStreamerStripsReportAndPreservesMsg(t *testing.T) {
	s := &visibleStreamer{}
	steps := []string{
		"<msg>",
		"<msg>思",
		"<msg>思考中",
		"<msg>思考中</msg>",
		"<msg>思考中</msg><report>最终",
		"<msg>思考中</msg><report>最终回答</report>",
	}
	emitted := ""
	for _, raw := range steps {
		emitted += s.Update(raw)
	}
	if emitted != "<msg>思考中</msg>最终回答" {
		t.Fatalf("visible text = %q, want %q", emitted, "<msg>思考中</msg>最终回答")
	}
}

func TestVisibleStreamerPlainText(t *testing.T) {
	s := &visibleStreamer{}
	if d := s.Update("你好"); d != "你好" {
		t.Fatalf("first delta = %q", d)
	}
	if d := s.Update("你好世界"); d != "世界" {
		t.Fatalf("second delta = %q, want 世界", d)
	}
}

func TestVisibleStreamerRebaselinesOnTagFormation(t *testing.T) {
	// A "<" that later becomes a tag must not emit the tag text.
	s := &visibleStreamer{}
	if d := s.Update("5 <"); d != "5 <" {
		t.Fatalf("first delta = %q", d)
	}
	if d := s.Update("5 <report>最终</report>"); d != "最终" {
		t.Fatalf("rebaseline delta = %q, want 最终", d)
	}
}

func TestVisibleStreamerStripsReflectionBlocks(t *testing.T) {
	s := &visibleStreamer{}
	steps := []string{
		"前面的话",
		"前面的话<reflection>",
		"前面的话<reflection>这是反思内容",
		"前面的话<reflection>这是反思内容</reflection>",
		"前面的话<reflection>这是反思内容</reflection>后面的话",
	}
	emitted := ""
	for _, raw := range steps {
		emitted += s.Update(raw)
	}
	if emitted != "前面的话后面的话" {
		t.Fatalf("visible text = %q, want %q", emitted, "前面的话后面的话")
	}
}

func TestThinkStreamerKeepsReflectionOutOfVisibleText(t *testing.T) {
	s := &thinkStreamer{}
	raw := "<thinking>先想一下</thinking>前面的话<reflection>自检内容</reflection>后面的话"
	vis, think := s.Update(raw)
	if vis != "前面的话后面的话" {
		t.Fatalf("visible = %q, want 前面的话后面的话", vis)
	}
	if think != "先想一下" {
		t.Fatalf("thinking = %q, want 先想一下", think)
	}
}

func TestVisibleStreamerHoldsBackPartialTags(t *testing.T) {
	s := &visibleStreamer{}
	steps := []string{
		"<msg",
		"<msg>我已经读取了侧边栏的代码文件，接下来我会添加一个可收起",
		"<msg>我已经读取了侧边栏的代码文件，接下来我会添加一个可收起的功能。</msg>",
	}
	emitted := ""
	for _, raw := range steps {
		emitted += s.Update(raw)
	}
	// The literal <msg> tag is intentionally preserved for the frontend.
	if got := strings.TrimSpace(emitted); got != "<msg>我已经读取了侧边栏的代码文件，接下来我会添加一个可收起的功能。</msg>" {
		t.Fatalf("visible = %q", got)
	}
}

func TestVisibleStreamerMasksInlineToolCalls(t *testing.T) {
	s := &visibleStreamer{}
	raw := `我已经读取了。
接下来修改文件：
functions.edit_file({"file_path": "C:/project/frontend/src/Sidebar.jsx", "old_text": "a", "new_text": "b"})
修改完成。`
	emitted := s.Update(raw)
	if emitted != "我已经读取了。\n接下来修改文件：\n\n修改完成。" {
		t.Fatalf("visible = %q", emitted)
	}
}

func TestVisibleStreamerHoldsBackIncompleteInlineCall(t *testing.T) {
	s := &visibleStreamer{}
	// While the call is still streaming, nothing after "functions." is emitted.
	if d := s.Update("我先改一下\nfunctions.edit_file({"); d != "我先改一下\n" {
		t.Fatalf("incomplete delta = %q", d)
	}
	// Once the call completes, it is masked and following text streams.
	raw := `我先改一下
functions.edit_file({"file_path": "x.js"})
改完了`
	if d := s.Update(raw); strings.TrimSpace(d) != "改完了" {
		t.Fatalf("complete delta = %q", d)
	}
}
