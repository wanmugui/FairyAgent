package main

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentloop/agent/internal/dtypes"
)

func TestAPIMessagesForRequestSerializesNativeVisionParts(t *testing.T) {
	message := NewMessage("user", "inspect this frame", nil, "", "")
	message.ContentParts = []ContentPart{{
		Type: "image_url",
		ImageURL: &ImageURLPart{
			URL:    "data:image/jpeg;base64,AAAA",
			Detail: "high",
		},
	}}

	wire := apiMessagesForRequest([]Message{message})
	if len(wire) != 1 {
		t.Fatalf("wire messages = %d, want 1", len(wire))
	}
	parts, ok := wire[0].Content.([]ContentPart)
	if !ok || len(parts) != 2 {
		t.Fatalf("content = %#v, want text plus image parts", wire[0].Content)
	}
	if parts[0].Type != "text" || parts[0].Text != "inspect this frame" {
		t.Fatalf("text part = %#v", parts[0])
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL == nil || parts[1].ImageURL.Detail != "high" {
		t.Fatalf("image part = %#v", parts[1])
	}
}

func TestEncodeImageAttachmentDataURLResizesScreenshot(t *testing.T) {
	path := writeTestPNG(t, 2400, 1200)
	dataURL, hash, width, height, err := encodeImageAttachmentDataURL(path, 1600, 82)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(dataURL, "data:image/jpeg;base64,") {
		t.Fatalf("data URL prefix = %q", dataURL[:minInt(len(dataURL), 40)])
	}
	if len(hash) != 64 {
		t.Fatalf("sha256 length = %d, want 64", len(hash))
	}
	if width != 1600 || height != 800 {
		t.Fatalf("resized dimensions = %dx%d, want 1600x800", width, height)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(dataURL, "data:image/jpeg;base64,"))
	if err != nil {
		t.Fatal(err)
	}
	decoded, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Bounds().Dx() != 1600 || decoded.Bounds().Dy() != 800 {
		t.Fatalf("decoded dimensions = %v", decoded.Bounds())
	}
}

func TestTrimVisionContextMessagesKeepsOnlyRecentImages(t *testing.T) {
	messages := []Message{
		NewMessage("system", "system", nil, "", ""),
		visionTestMessage("old-a", "old-b"),
		NewMessage("tool", `{}`, nil, "call-1", "computer_observe"),
		visionTestMessage("new-a", "new-b"),
	}
	got := trimVisionContextMessages(messages, 2)
	if len(got) != 3 {
		t.Fatalf("message count = %d, want 3: %#v", len(got), got)
	}
	if got[1].Role != "tool" {
		t.Fatalf("oldest vision message was not dropped: %#v", got)
	}
	if count := contentPartImageCount(got[2].ContentParts); count != 2 {
		t.Fatalf("recent image count = %d, want 2", count)
	}
}

func TestToolVisionContextMessageUsesToolAttachment(t *testing.T) {
	path := writeTestPNG(t, 320, 200)
	results := []ToolInvocationResult{{
		Name: "computer_observe",
		Result: ToolResult{Attachments: []dtypes.ToolAttachment{{
			Path:  path,
			MIME:  "image/png",
			Label: "desktop screenshot",
		}}},
	}}
	message, ok := toolVisionContextMessage(&APIConfig{}, results)
	if !ok {
		t.Fatal("expected a native-vision message")
	}
	if !isVisionContextMessage(message) {
		t.Fatalf("message is not marked as vision context: %#v", message)
	}
	if count := contentPartImageCount(message.ContentParts); count != 1 {
		t.Fatalf("image count = %d, want 1", count)
	}
	if !strings.Contains(message.Content, "desktop screenshot") {
		t.Fatalf("attachment label missing from message: %q", message.Content)
	}
}

func TestToolVisionContextMessageHonorsNativeVisionSwitch(t *testing.T) {
	disabled := false
	_, ok := toolVisionContextMessage(&APIConfig{NativeVision: &disabled}, []ToolInvocationResult{{
		Result: ToolResult{Attachments: []dtypes.ToolAttachment{{Path: writeTestPNG(t, 10, 10), MIME: "image/png"}}},
	}})
	if ok {
		t.Fatal("native vision must stay disabled when native_vision=false")
	}
}

func visionTestMessage(urls ...string) Message {
	message := NewMessage("user", "[vision_attachment]", nil, "", "")
	message.InternalType = internalTypeVisionContext
	for _, url := range urls {
		message.ContentParts = append(message.ContentParts, ContentPart{
			Type:     "image_url",
			ImageURL: &ImageURLPart{URL: url, Detail: "high"},
		})
	}
	return message
}

func writeTestPNG(t *testing.T, width, height int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 180, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "screenshot.png")
	if err := os.WriteFile(path, encoded.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
