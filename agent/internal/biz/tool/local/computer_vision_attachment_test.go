package local

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Proves the whole local-image path end to end inside the tool layer: a real PNG on
// disk -> computer_observe(action=screenshot, image_path=...) -> a ToolResult whose
// attachment satisfies the contract vision_context.go enforces before injecting.
func TestLocalImageAttachmentIsVisionContextReady(t *testing.T) {
	workspace := t.TempDir()
	// A real 1x1 PNG so the bytes are a genuine image, not arbitrary content.
	pngBytes := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4, 0x89, 0x00, 0x00, 0x00,
		0x0A, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00, 0x00, 0x00, 0x00, 0x49,
		0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82,
	}
	name := "camera-frame.png"
	if err := os.WriteFile(filepath.Join(workspace, name), pngBytes, 0o644); err != nil {
		t.Fatalf("write png: %v", err)
	}

	tool := &localComputerTool{name: "computer_observe"}
	result, err := tool.Execute(context.Background(), ToolInvocation{
		Name:      "computer_observe",
		Args:      json.RawMessage(`{"action":"screenshot","image_path":"` + name + `"}`),
		Timeout:   5 * time.Second,
		Workspace: workspace,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("error result: %#v", result.Value)
	}
	if len(result.Attachments) != 1 {
		t.Fatalf("want 1 attachment, got %d", len(result.Attachments))
	}
	a := result.Attachments[0]
	// Exactly the two guards vision_context.go applies before injecting an image.
	if a.MIME[:6] != "image/" {
		t.Fatalf("vision_context requires image/* MIME, got %q", a.MIME)
	}
	if a.SizeBytes == 0 {
		t.Fatal("vision_context requires a non-zero SizeBytes to encode the payload")
	}
	if got, want := a.Path, filepath.Join(workspace, name); got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
	if len(a.Label) == 0 {
		t.Fatal("attachment should carry a label so the injection is identifiable")
	}
}
