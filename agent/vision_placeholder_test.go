package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentloop/agent/internal/dtypes"
)

func refPart(path string) ContentPart {
	return ContentPart{
		Type: "image_ref",
		ImageRef: &ImageRefPart{
			Path:         path,
			MaxDimension: 1600,
			Quality:      82,
		},
	}
}

// The vision context must carry a placeholder, not a base64 payload.
func TestVisionContextStoresPlaceholderNotBase64(t *testing.T) {
	visionEncodings.clear()
	path := writeTestPNG(t, 200, 150)

	enabled := true
	cfg := &APIConfig{}
	cfg.NativeVision = &enabled
	cfg.VisionMaxDimension = 1600
	cfg.VisionJPEGQuality = 82

	result := ToolInvocationResult{
		Result: ToolResult{
			Attachments: []dtypes.ToolAttachment{
				{Path: path, MIME: "image/png", SHA256: "abcdef0123456789"},
			},
		},
	}

	message, ok := toolVisionContextMessage(cfg, []ToolInvocationResult{result})
	if !ok {
		t.Fatal("expected a vision context message")
	}
	if len(message.ContentParts) != 1 {
		t.Fatalf("want 1 content part, got %d", len(message.ContentParts))
	}
	part := message.ContentParts[0]
	if part.Type != "image_ref" {
		t.Fatalf("the context must hold an image_ref placeholder, got %q", part.Type)
	}
	if part.ImageRef == nil || part.ImageRef.Path != path {
		t.Fatalf("the placeholder must point at the file, got %#v", part.ImageRef)
	}
	if part.ImageURL != nil {
		t.Fatal("the context must not carry an image_url part")
	}
	// Nothing may have been encoded: the whole point is that the transcript
	// holds a path, not a megabyte of base64.
	if entries, _ := visionEncodings.stats(); entries != 0 {
		t.Fatalf("building the context must not encode anything, cache entries=%d", entries)
	}
	// The text still names the image so the model knows what is attached.
	if !strings.Contains(message.Content, "sha256=abcdef012345") {
		t.Fatalf("the label must survive in the text, got %q", message.Content)
	}
}

// The wire boundary must turn the placeholder back into a real image.
func TestAPIMessagesForRequestMaterializesImageRef(t *testing.T) {
	visionEncodings.clear()
	path := writeTestPNG(t, 120, 90)

	message := NewMessage("user", "look at this", nil, "", "")
	message.ContentParts = []ContentPart{refPart(path)}

	wire := apiMessagesForRequest([]Message{message})
	if len(wire) != 1 {
		t.Fatalf("want 1 wire message, got %d", len(wire))
	}
	parts, ok := wire[0].Content.([]ContentPart)
	if !ok {
		t.Fatalf("wire content must be parts, got %T", wire[0].Content)
	}
	var found bool
	for _, part := range parts {
		if part.Type != "image_url" {
			continue
		}
		found = true
		if part.ImageURL == nil || !strings.HasPrefix(part.ImageURL.URL, "data:image/jpeg;base64,") {
			t.Fatalf("the wire part must carry a data URL, got %#v", part.ImageURL)
		}
		if len(part.ImageURL.URL) < 100 {
			t.Fatalf("the data URL looks empty: %q", part.ImageURL.URL)
		}
	}
	if !found {
		t.Fatalf("no image_url part in the wire message: %#v", parts)
	}
}

// A missing file must not take the whole request down.
func TestMaterializeImageRefDegradesOnMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone.png")
	if _, ok := materializeImageRef(ImageRefPart{Path: missing}); ok {
		t.Fatal("a missing file must not materialise")
	}

	message := NewMessage("user", "text only", nil, "", "")
	message.ContentParts = []ContentPart{
		refPart(missing),
		{Type: "text", Text: "after the image"},
	}
	wire := apiMessagesForRequest([]Message{message})
	parts, ok := wire[0].Content.([]ContentPart)
	if !ok {
		t.Fatalf("wire content must be parts, got %T", wire[0].Content)
	}
	for _, part := range parts {
		if part.Type == "image_url" {
			t.Fatal("a missing image must not become an image part")
		}
	}
	// The rest of the message survives.
	if len(parts) == 0 {
		t.Fatal("the text parts must survive a missing image")
	}
}

// An empty path is rejected outright.
func TestMaterializeImageRefRejectsEmptyPath(t *testing.T) {
	if _, ok := materializeImageRef(ImageRefPart{Path: ""}); ok {
		t.Fatal("an empty path must not materialise")
	}
}

// The trim/count helpers must understand placeholders, otherwise the image
// limit silently stops working.
func TestContentPartImageCountCountsPlaceholders(t *testing.T) {
	parts := []ContentPart{
		{Type: "text", Text: "hello"},
		refPart("/tmp/a.png"),
		refPart("/tmp/b.png"),
		{Type: "image_url", ImageURL: &ImageURLPart{URL: "data:image/jpeg;base64,AAA"}},
		{Type: "image_ref", ImageRef: &ImageRefPart{}}, // no path: not an image
	}
	if got := contentPartImageCount(parts); got != 3 {
		t.Fatalf("want 3 images counted, got %d", got)
	}
}

func TestLastImagePartsKeepsPlaceholders(t *testing.T) {
	parts := []ContentPart{
		refPart("/tmp/a.png"),
		{Type: "text", Text: "middle"},
		refPart("/tmp/b.png"),
		refPart("/tmp/c.png"),
	}
	selected := lastImageParts(parts, 2)
	if len(selected) != 2 {
		t.Fatalf("want 2 parts, got %d", len(selected))
	}
	// The most recent two, in original order.
	if selected[0].ImageRef.Path != "/tmp/b.png" || selected[1].ImageRef.Path != "/tmp/c.png" {
		t.Fatalf("wrong parts kept: %q %q", selected[0].ImageRef.Path, selected[1].ImageRef.Path)
	}
}

// The transcript on disk must never carry the image. ContentParts is
// excluded from JSON entirely (json:"-"), so persisting a message drops the
// attachment and keeps only the text -- and the text names the file.
func TestPersistedVisionMessageCarriesNoImage(t *testing.T) {
	visionEncodings.clear()
	path := writeTestPNG(t, 80, 80)

	enabled := true
	cfg := &APIConfig{}
	cfg.NativeVision = &enabled
	result := ToolInvocationResult{
		Result: ToolResult{
			Attachments: []dtypes.ToolAttachment{{Path: path, MIME: "image/png"}},
		},
	}
	message, ok := toolVisionContextMessage(cfg, []ToolInvocationResult{result})
	if !ok {
		t.Fatal("expected a vision context message")
	}

	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "base64") || strings.Contains(string(raw), "data:image") {
		t.Fatalf("the persisted message must not embed the image: %s", string(raw))
	}
	if strings.Contains(string(raw), "image_ref") {
		t.Fatalf("the placeholder is not persisted either: %s", string(raw))
	}
	// The image itself is untouched on disk.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the original file must still be on disk: %v", err)
	}
	// And the text still names it, so the transcript is not silent about it.
	if !strings.Contains(message.Content, path) {
		t.Fatalf("the text must name the attachment, got %q", message.Content)
	}
}
