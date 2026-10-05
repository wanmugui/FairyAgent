package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTempPNG(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// 最小合法 PNG（1x1）
	data := []byte{
		0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89, 0x00, 0x00, 0x00, 0x0a, 'I', 'D', 'A', 'T',
		0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05,
		0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00,
		0x00, 0x00, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func newTestConfig(t *testing.T) *Config {
	t.Helper()
	root := t.TempDir()
	return &Config{
		RepoRoot:     root,
		WorkspaceDir: "workspace",
		API:          APIConfig{},
	}
}

func TestUserUploadVision_NoFileContext(t *testing.T) {
	cfg := newTestConfig(t)
	msgs := []Message{{Role: "user", Content: "你好"}}
	if _, ok := userUploadVisionContextMessage(cfg, msgs); ok {
		t.Fatal("没有 file_context 时不应产出视觉消息")
	}
}

func TestUserUploadVision_NativeVisionDisabled(t *testing.T) {
	cfg := newTestConfig(t)
	disabled := false
	cfg.API.NativeVision = &disabled
	img := filepath.Join(cfg.RepoRoot, "workspace", "upload", "a.png")
	writeTempPNG(t, img)
	msgs := []Message{{
		Role:    "user",
		Content: `<file_context>[{"path":"` + img + `","name":"a.png","type":"png"}]</file_context>`,
	}}
	if _, ok := userUploadVisionContextMessage(cfg, msgs); ok {
		t.Fatal("native vision 关闭时不应产出视觉消息")
	}
}

func TestUserUploadVision_ProducesImagePart(t *testing.T) {
	cfg := newTestConfig(t)
	img := filepath.Join(cfg.RepoRoot, "workspace", "upload", "shot.png")
	writeTempPNG(t, img)
	msgs := []Message{{
		Role:    "user",
		Content: `看看这张图` + "\n" + `<file_context>[{"path":"` + img + `","name":"shot.png","type":"png"}]</file_context>`,
	}}
	msg, ok := userUploadVisionContextMessage(cfg, msgs)
	if !ok {
		t.Fatal("应当产出视觉消息")
	}
	if len(msg.ContentParts) != 1 {
		t.Fatalf("期望 1 个视觉附件，实际 %d", len(msg.ContentParts))
	}
	part := msg.ContentParts[0]
	if part.Type != "image_url" {
		t.Errorf("Type = %q, 期望 image_url", part.Type)
	}
	if part.ImageURL == nil || part.ImageURL.URL == "" {
		t.Fatal("ImageURL 为空")
	}
	if part.ImageURL.Detail != "high" {
		t.Errorf("Detail = %q, 期望 high", part.ImageURL.Detail)
	}
	if msg.Role != "user" {
		t.Errorf("Role = %q, 期望 user", msg.Role)
	}
	if msg.InternalType != internalTypeVisionContext {
		t.Errorf("InternalType = %q, 期望 %q", msg.InternalType, internalTypeVisionContext)
	}
}

// 路径闸门：workspace 之外的路径绝不能被读进上下文。
func TestUserUploadVision_RejectsOutsideWorkspace(t *testing.T) {
	cfg := newTestConfig(t)
	outside := filepath.Join(cfg.RepoRoot, "secret.png")
	writeTempPNG(t, outside)

	cases := map[string]string{
		"仓库内但 workspace 外": outside,
		"绝对路径穿越":         filepath.Join(cfg.RepoRoot, "workspace", "..", "secret.png"),
		"非图片扩展名":         filepath.Join(cfg.RepoRoot, "workspace", "upload", "notes.txt"),
		"相对路径":             "workspace/upload/shot.png",
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok := uploadedImagePathAllowed(cfg, p); ok {
				t.Fatalf("路径 %q 应被拒绝", p)
			}
		})
	}
	// 合法路径必须放行，否则闸门过紧
	ok := filepath.Join(cfg.RepoRoot, "workspace", "upload", "ok.png")
	writeTempPNG(t, ok)
	if _, allowed := uploadedImagePathAllowed(cfg, ok); !allowed {
		t.Fatal("workspace 内的图片应被放行")
	}
}

// 读不到的文件必须跳过，不能让整轮对话失败。
func TestUserUploadVision_MissingFileSkipped(t *testing.T) {
	cfg := newTestConfig(t)
	missing := filepath.Join(cfg.RepoRoot, "workspace", "upload", "gone.png")
	msgs := []Message{{
		Role:    "user",
		Content: `<file_context>[{"path":"` + missing + `","name":"gone.png","type":"png"}]</file_context>`,
	}}
	msg, ok := userUploadVisionContextMessage(cfg, msgs)
	if ok {
		t.Fatalf("文件不存在时不应产出视觉消息，实际 %+v", msg.ContentParts)
	}
}

// 遵守 vision_max_images 上限。
func TestUserUploadVision_RespectsMax(t *testing.T) {
	cfg := newTestConfig(t)
	limit := 2
	cfg.API.VisionMaxImages = limit
	var content string
	for i := 0; i < 5; i++ {
		p := filepath.Join(cfg.RepoRoot, "workspace", "upload", string(rune('a'+i))+".png")
		writeTempPNG(t, p)
		content += `<file_context>[{"path":"` + p + `","type":"png"}]</file_context>`
	}
	msgs := []Message{{Role: "user", Content: content}}
	msg, ok := userUploadVisionContextMessage(cfg, msgs)
	if !ok {
		t.Fatal("应当产出视觉消息")
	}
	if len(msg.ContentParts) != limit {
		t.Fatalf("期望最多 %d 个附件，实际 %d", limit, len(msg.ContentParts))
	}
}
