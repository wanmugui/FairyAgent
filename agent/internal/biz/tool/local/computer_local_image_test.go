package local

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Native single-image viewing reuses computer_observe instead of adding a new tool:
// action=screenshot with image_path=<file> attaches that file straight into the
// model context so the main model looks at it without an extra VQA round trip.
func TestAttachmentForLocalImage(t *testing.T) {
	workspace := t.TempDir()
	relative := "shot.png"
	absolute := filepath.Join(workspace, relative)
	payload := []byte("\x89PNG\r\n\x1a\n local image bytes")
	if err := os.WriteFile(absolute, payload, 0o644); err != nil {
		t.Fatalf("seed image: %v", err)
	}

	tool := &localComputerTool{name: "computer_observe"}

	t.Run("workspace-relative path becomes one image attachment", func(t *testing.T) {
		result, err := tool.attachmentForLocalImage(workspace, relative)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.IsError {
			t.Fatalf("expected success, got error result: %#v", result.Value)
		}
		if len(result.Attachments) != 1 {
			t.Fatalf("expected exactly 1 attachment, got %d", len(result.Attachments))
		}
		attachment := result.Attachments[0]
		if attachment.Path != absolute {
			t.Fatalf("attachment path = %q, want %q", attachment.Path, absolute)
		}
		// vision_context.go only injects attachments whose MIME starts with "image/".
		if !strings.HasPrefix(attachment.MIME, "image/") {
			t.Fatalf("MIME = %q, must start with image/ to reach the vision context", attachment.MIME)
		}
		if attachment.SizeBytes != int64(len(payload)) {
			t.Fatalf("SizeBytes = %d, want %d", attachment.SizeBytes, len(payload))
		}
	})

	t.Run("absolute path is used as-is", func(t *testing.T) {
		result, err := tool.attachmentForLocalImage(workspace, absolute)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result.Attachments) != 1 || result.Attachments[0].Path != absolute {
			t.Fatalf("absolute path was not preserved: %#v", result.Attachments)
		}
	})

	t.Run("remote url is rejected in favour of image_vqa", func(t *testing.T) {
		result, err := tool.attachmentForLocalImage(workspace, "https://example.com/a.png")
		if err != nil {
			t.Fatalf("unexpected transport error: %v", err)
		}
		if !result.IsError {
			t.Fatal("expected a remote URL to be refused")
		}
		if len(result.Attachments) != 0 {
			t.Fatalf("refused URL must not attach anything, got %#v", result.Attachments)
		}
		if message, _ := result.Value["error"].(string); !strings.Contains(message, "image_vqa") {
			t.Fatalf("error should point the caller at image_vqa, got %q", message)
		}
	})

	t.Run("missing file fails without attaching", func(t *testing.T) {
		result, err := tool.attachmentForLocalImage(workspace, "absent.png")
		if err != nil {
			t.Fatalf("unexpected transport error: %v", err)
		}
		if !result.IsError {
			t.Fatal("expected a missing file to fail")
		}
		if len(result.Attachments) != 0 {
			t.Fatalf("missing file must not attach anything, got %#v", result.Attachments)
		}
	})

	t.Run("directory is rejected", func(t *testing.T) {
		result, err := tool.attachmentForLocalImage(workspace, "subdir")
		if err != nil {
			t.Fatalf("unexpected transport error: %v", err)
		}
		if !result.IsError {
			t.Fatal("expected a directory to be rejected")
		}
		if len(result.Attachments) != 0 {
			t.Fatalf("directory must not attach anything, got %#v", result.Attachments)
		}
	})

	t.Run("relative path without workspace fails", func(t *testing.T) {
		result, err := tool.attachmentForLocalImage("", relative)
		if err != nil {
			t.Fatalf("unexpected transport error: %v", err)
		}
		if !result.IsError {
			t.Fatal("expected relative path without workspace to fail")
		}
	})
}

// Execute must short-circuit screenshot+image_path before the process is launched,
// so viewing a local file never spins up a screen capture and never needs image_vqa.
func TestExecuteScreenshotWithImagePathShortCircuits(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "frame.png"), []byte("not a real png"), 0o644); err != nil {
		t.Fatalf("seed image: %v", err)
	}

	tool := &localComputerTool{name: "computer_observe"}
	invocation := ToolInvocation{
		Index:     0,
		CallID:    "call-1",
		Name:      "computer_observe",
		Args:      json.RawMessage(`{"action":"screenshot","image_path":"frame.png"}`),
		Timeout:   5 * time.Second,
		Workspace: workspace,
	}

	result, err := tool.Execute(context.Background(), invocation)
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error result: %#v", result.Value)
	}
	if len(result.Attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d: %#v", len(result.Attachments), result.Value)
	}
	if !strings.HasPrefix(result.Attachments[0].MIME, "image/") {
		t.Fatalf("MIME = %q, must start with image/ to reach the vision context", result.Attachments[0].MIME)
	}
	if result.Attachments[0].Path != filepath.Join(workspace, "frame.png") {
		t.Fatalf("path = %q, want the workspace-relative file resolved", result.Attachments[0].Path)
	}
}
