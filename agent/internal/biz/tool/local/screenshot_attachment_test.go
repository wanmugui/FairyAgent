package local

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestScreenshotResultCarriesAttachment(t *testing.T) {
	path := writeLocalTestPNG(t, 80, 40)
	result := withScreenshotAttachment("computer_observe", "screenshot", ToolResult{
		Value: map[string]any{
			"path":   path,
			"width":  float64(80),
			"height": float64(40),
		},
	})
	if len(result.Attachments) != 1 {
		t.Fatalf("attachments = %#v, want one screenshot", result.Attachments)
	}
	attachment := result.Attachments[0]
	if attachment.Path != path || attachment.MIME != "image/png" || attachment.Width != 80 || attachment.Height != 40 {
		t.Fatalf("unexpected attachment: %#v", attachment)
	}
}

func TestBrowserScreenshotResultCarriesAttachment(t *testing.T) {
	path := writeLocalTestPNG(t, 120, 60)
	result := withScreenshotAttachment("browser", "screenshot", ToolResult{
		Value: map[string]any{"path": path, "artifact": map[string]any{"kind": "image", "path": path}},
	})
	if len(result.Attachments) != 1 {
		t.Fatalf("attachments = %#v, want one browser screenshot", result.Attachments)
	}
	attachment := result.Attachments[0]
	if attachment.Path != path || attachment.MIME != "image/png" || attachment.Label != "browser screenshot" {
		t.Fatalf("unexpected attachment: %#v", attachment)
	}
}

func TestScreenshotAttachmentSkipsErrorsAndOtherActions(t *testing.T) {
	result := ToolResult{Value: map[string]any{"path": writeLocalTestPNG(t, 10, 10)}, IsError: true}
	if got := withScreenshotAttachment("computer_observe", "screenshot", result); len(got.Attachments) != 0 {
		t.Fatalf("error result gained attachments: %#v", got.Attachments)
	}
	result.IsError = false
	if got := withScreenshotAttachment("computer_observe", "screen_info", result); len(got.Attachments) != 0 {
		t.Fatalf("non-screenshot result gained attachments: %#v", got.Attachments)
	}
	if got := withScreenshotAttachment("bash", "screenshot", result); len(got.Attachments) != 0 {
		t.Fatalf("unsupported tool gained attachments: %#v", got.Attachments)
	}
}

func writeLocalTestPNG(t *testing.T, width, height int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	path := filepath.Join(t.TempDir(), "screen.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := png.Encode(file, img); err != nil {
		t.Fatal(err)
	}
	return path
}
