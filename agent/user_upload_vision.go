package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// 用户上传的图片目前只是以 <file_context> JSON 的形式拼在用户消息文本里，
// 模型拿到的仅是一个文件路径 —— 于是「我发的图你看不见」只能靠 image_vqa
// 二次转述，而 image_vqa 走的是另一个模型，既慢又会丢信息。
//
// 这里把上传的图片转成真正的 image_url 视觉附件，直接挂到本轮消息上，
// 让主模型像看工具截图一样直接看图。image_vqa 退回到它该在的位置：
// 纯文本模型的兜底，或明确要求的离线 OCR/坐标查询。

type fileContextEntry struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Type string `json:"type"`
}

var fileContextBlockRe = regexp.MustCompile(`(?s)<file_context[^>]*>(.*?)</file_context>`)

// 少数情况 file_context 以裸 JSON 数组出现，没有标签包裹。
var bareFileContextRe = regexp.MustCompile(`(?s)\[\s*\{[^\[\]]*"path"[^\[\]]*\}\s*\]`)

var allowedUploadExt = map[string]bool{
	".png":  true,
	".jpg":  true,
	".jpeg": true,
	".gif":  true,
	".webp": true,
	".bmp":  true,
}

// uploadedImagePathAllowed 只放行 workspace 目录下的图片。
// 没有这道闸，任何 path 字段都能把机器上的文件读出来塞进模型上下文。
func uploadedImagePathAllowed(cfg *Config, raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || !filepath.IsAbs(raw) {
		return "", false
	}
	clean := filepath.Clean(raw)
	if !allowedUploadExt[strings.ToLower(filepath.Ext(clean))] {
		return "", false
	}
	root := strings.TrimSpace(cfg.RepoRoot)
	if root == "" {
		return "", false
	}
	// 跟随 workspace_dir 配置，不写死 "workspace"。
	wsDir := strings.TrimSpace(cfg.WorkspaceDir)
	if wsDir == "" {
		wsDir = "workspace"
	}
	uploadRoot := filepath.Clean(filepath.Join(root, wsDir))
	rel, err := filepath.Rel(uploadRoot, clean)
	if err != nil {
		return "", false
	}
	// 必须在 workspace 之内，且不能是 workspace 本身。
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return clean, true
}

// extractFileContextPaths 抽出一条消息里所有 <file_context> 里的图片路径。
func extractFileContextPaths(cfg *Config, content string) []string {
	if content == "" || !strings.Contains(content, "file_context") {
		return nil
	}
	blobs := fileContextBlockRe.FindAllStringSubmatch(content, -1)
	if len(blobs) == 0 {
		for _, m := range bareFileContextRe.FindAllString(content, -1) {
			blobs = append(blobs, []string{"", m})
		}
	}
	var out []string
	for _, b := range blobs {
		var entries []fileContextEntry
		if err := json.Unmarshal([]byte(strings.TrimSpace(b[1])), &entries); err != nil {
			continue
		}
		for _, e := range entries {
			if p, ok := uploadedImagePathAllowed(cfg, e.Path); ok {
				out = append(out, p)
			}
		}
	}
	return out
}

// userUploadVisionContextMessage 扫描对话里的用户消息，把上传的图片转成视觉附件。
// 与 toolVisionContextMessage 一样复用 ContentParts、VisionMaxDimensionOrDefault
// 与 VisionJPEGQualityOrDefault，并打上 internalTypeVisionContext，
// 这样 trimVisionContextMessages 会自动按 vision_max_images 裁剪。
func userUploadVisionContextMessage(cfg *Config, messages []Message) (Message, bool) {
	if cfg == nil || !cfg.API.NativeVisionEnabled() {
		return Message{}, false
	}
	limit := cfg.API.VisionMaxImagesOrDefault()
	if limit <= 0 {
		return Message{}, false
	}

	seen := map[string]struct{}{}
	var parts []ContentPart
	var labels []string

	for i := len(messages) - 1; i >= 0 && len(parts) < limit; i-- {
		m := messages[i]
		if m.Role != "user" {
			continue
		}
		for _, p := range extractFileContextPaths(cfg, m.Content) {
			if len(parts) >= limit {
				break
			}
			if _, dup := seen[p]; dup {
				continue
			}
			seen[p] = struct{}{}
			dataURL, hash, _, _, err := encodeImageAttachmentDataURL(
				p,
				cfg.API.VisionMaxDimensionOrDefault(),
				cfg.API.VisionJPEGQualityOrDefault(),
			)
			if err != nil {
				// 读不到就跳过，绝不能让一张坏图打断整轮对话。
				continue
			}
			parts = append(parts, ContentPart{
				Type: "image_url",
				ImageURL: &ImageURLPart{
					URL:    dataURL,
					Detail: "high",
				},
			})
			label := filepath.Base(p)
			if len(hash) > 12 {
				hash = hash[:12]
			}
			if hash != "" {
				label = fmt.Sprintf("%s sha256=%s", label, hash)
			}
			labels = append(labels, label)
		}
	}
	if len(parts) == 0 {
		return Message{}, false
	}

	content := "[vision_attachment]\nThe image file(s) the user uploaded are attached directly to this turn. Inspect them natively and answer or act from what you see. Do not call image_vqa for ordinary image questions, descriptions, analysis, layout, fonts, colors, comparison, or reasoning; image_vqa is only for text-only models or an explicitly requested offline OCR/coordinate lookup."
	if len(labels) > 0 {
		content += "\n" + strings.Join(labels, "\n")
	}
	message := NewMessage("user", content, nil, "", "")
	message.InternalType = internalTypeVisionContext
	message.ContentParts = parts
	return message, true
}
