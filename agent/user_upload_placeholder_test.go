package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// The default image budget rose from 4 to 10 because images are now
// placeholders in the context rather than inlined base64. This test pins the
// new default so it cannot silently regress.
func TestVisionMaxImagesDefaultIsTen(t *testing.T) {
	cfg := APIConfig{}
	if got := cfg.VisionMaxImagesOrDefault(); got != 10 {
		t.Fatalf("默认图片上限 = %d，期望 10", got)
	}
	// An explicit setting still wins.
	cfg.VisionMaxImages = 3
	if got := cfg.VisionMaxImagesOrDefault(); got != 3 {
		t.Fatalf("显式配置应优先，实际 %d", got)
	}
}

// The user upload path must produce placeholders, and the wire boundary must
// turn them back into real images. This is the path that matters: a screenshot
// the user sends from WeChat.
func TestUserUploadPathUsesPlaceholdersAndMaterializes(t *testing.T) {
	visionEncodings.clear()
	cfg := newTestConfig(t)
	img := filepath.Join(cfg.RepoRoot, "workspace", "upload", "shot.png")
	writeTempPNG(t, img)

	content := `<file_context>[{"path":"` + img + `","type":"png"}]</file_context>`
	msgs := []Message{{Role: "user", Content: content}}

	msg, ok := userUploadVisionContextMessage(cfg, msgs)
	if !ok {
		t.Fatal("应当产出视觉消息")
	}
	if len(msg.ContentParts) != 1 {
		t.Fatalf("期望 1 个 part，实际 %d", len(msg.ContentParts))
	}
	part := msg.ContentParts[0]
	if part.Type != "image_ref" {
		t.Fatalf("Type = %q，期望 image_ref", part.Type)
	}
	if part.ImageURL != nil {
		t.Fatal("上下文里不应出现 image_url")
	}
	if part.ImageRef == nil || part.ImageRef.Path != img {
		t.Fatalf("占位符应指向原图，实际 %#v", part.ImageRef)
	}
	// 构建上下文时不应该做过任何编码工作。
	if entries, _ := visionEncodings.stats(); entries != 0 {
		t.Fatalf("构建上下文不应触发编码，缓存条目 %d", entries)
	}

	// wire 边界必须还原成真图片，否则模型看不见图。
	wire := apiMessagesForRequest([]Message{msg})
	parts, ok := wire[0].Content.([]ContentPart)
	if !ok {
		t.Fatalf("wire content 应为 parts，实际 %T", wire[0].Content)
	}
	var found bool
	for _, p := range parts {
		if p.Type != "image_url" {
			continue
		}
		found = true
		if p.ImageURL == nil || !strings.HasPrefix(p.ImageURL.URL, "data:image/jpeg;base64,") {
			t.Fatalf("wire 上应是 data URL，实际 %#v", p.ImageURL)
		}
	}
	if !found {
		t.Fatalf("wire 上没有 image_url：%#v", parts)
	}
}

// Ten images in one turn must all survive, and the context must stay small.
func TestUserUploadPathHandlesTenImages(t *testing.T) {
	visionEncodings.clear()
	cfg := newTestConfig(t)
	var content string
	for i := 0; i < 10; i++ {
		p := filepath.Join(cfg.RepoRoot, "workspace", "upload", string(rune('a'+i))+".png")
		writeTempPNG(t, p)
		content += `<file_context>[{"path":"` + p + `","type":"png"}]</file_context>`
	}
	msgs := []Message{{Role: "user", Content: content}}

	msg, ok := userUploadVisionContextMessage(cfg, msgs)
	if !ok {
		t.Fatal("应当产出视觉消息")
	}
	if len(msg.ContentParts) != 10 {
		t.Fatalf("期望 10 个 part，实际 %d", len(msg.ContentParts))
	}
	// 上下文里必须是占位符，不是 10 份 base64。
	for _, p := range msg.ContentParts {
		if p.Type != "image_ref" {
			t.Fatalf("Type = %q，期望 image_ref", p.Type)
		}
	}
	if entries, _ := visionEncodings.stats(); entries != 0 {
		t.Fatalf("构建上下文不应触发编码，缓存条目 %d", entries)
	}
}

// A missing file must be skipped, not fatal.
func TestUserUploadPathSkipsMissingFile(t *testing.T) {
	cfg := newTestConfig(t)
	missing := filepath.Join(cfg.RepoRoot, "workspace", "upload", "nope.png")
	content := `<file_context>[{"path":"` + missing + `","type":"png"}]</file_context>`
	msgs := []Message{{Role: "user", Content: content}}

	msg, ok := userUploadVisionContextMessage(cfg, msgs)
	if ok {
		t.Fatalf("文件不存在时不应产出视觉消息，实际 %+v", msg.ContentParts)
	}
}
