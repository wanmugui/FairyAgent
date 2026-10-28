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
	// 上下文里只放占位符：base64 要到 wire 边界才生成。
	if part.Type != "image_ref" {
		t.Errorf("Type = %q, 期望 image_ref", part.Type)
	}
	if part.ImageRef == nil || part.ImageRef.Path != img {
		t.Fatalf("ImageRef 应指向文件，实际 %#v", part.ImageRef)
	}
	if part.ImageURL != nil {
		t.Error("上下文里不应出现 image_url")
	}
	if part.ImageRef.Hash == "" {
		t.Error("占位符应带 hash，用于给模型标注图片身份")
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
		"绝对路径穿越":           filepath.Join(cfg.RepoRoot, "workspace", "..", "secret.png"),
		"非图片扩展名":           filepath.Join(cfg.RepoRoot, "workspace", "upload", "notes.txt"),
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

// TestUserUploadVision_NoCrossTurnReattach 复现真实事故：
// 用户第 1 轮发了 image-15.png，第 3 轮只发文字。旧实现倒着扫整个
// workingMsgs 历史，于是把第 1 轮那张老图在第 3 轮又挂成视觉附件——
// 模型看到一张"本轮刚上传"的图，其实是三天前的，于是把旧截图当新证据引用。
func TestUserUploadVision_NoCrossTurnReattach(t *testing.T) {
	cfg := newTestConfig(t)
	old := filepath.Join(cfg.RepoRoot, "workspace", "upload", "2026-10-05", "image-15.png")
	writeTempPNG(t, old)

	// 历史：第 1 轮带图，第 2 轮纯文本
	msgs := []Message{
		{Role: "user", Content: `<file_context>[{"path":"` + old + `","name":"image-15.png","type":"png"}]</file_context>`},
		{Role: "assistant", Content: "好的"},
		// 本轮：用户只发文字，没有任何 file_context
		{Role: "user", Content: "把剩余改动收尾"},
	}
	if _, ok := userUploadVisionContextMessage(cfg, msgs); ok {
		t.Fatal("本轮没有上传文件时，不应从历史消息里重新挂视觉附件")
	}
}

// TestUserUploadVision_CurrentTurnStillWorks 守住正常路径：
// 本轮确实上传了图，就必须挂上。
func TestUserUploadVision_CurrentTurnStillWorks(t *testing.T) {
	cfg := newTestConfig(t)
	cur := filepath.Join(cfg.RepoRoot, "workspace", "upload", "2026-10-06", "image-2.png")
	writeTempPNG(t, cur)
	msgs := []Message{
		{Role: "assistant", Content: "上一轮回复"},
		{Role: "user", Content: `<file_context>[{"path":"` + cur + `","name":"image-2.png","type":"png"}]</file_context>`},
	}
	if _, ok := userUploadVisionContextMessage(cfg, msgs); !ok {
		t.Fatal("本轮上传了图片时必须挂视觉附件")
	}
}

// TestUserUploadVision_NoTagNoImage 守住路径守卫：
// 没有 <file_context> 标记时，即使正文里出现 .png 路径，也不该挂成视觉附件。
// 误挂的后果很隐蔽：模型会拿到一张用户根本没在本轮发过的图。
func TestUserUploadVision_NoTagNoImage(t *testing.T) {
	cfg := newTestConfig(t)
	p := filepath.Join(cfg.RepoRoot, "workspace", "upload", "2026-10-06", "image.png")
	writeTempPNG(t, p)

	cases := []string{
		"看看这个 " + p + " 有问题吗",
		"文件在 upload/2026-10-06/image.png",
		`{"path":"` + p + `","name":"image.png","type":"png"}`, // 裸 JSON，无 tag
	}
	for _, c := range cases {
		msgs := []Message{{Role: "user", Content: c}}
		if _, ok := userUploadVisionContextMessage(cfg, msgs); ok {
			t.Fatalf("无 <file_context> 标记时不应挂图：%q", c)
		}
	}
}
