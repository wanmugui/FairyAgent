package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalImageGenerateMiniMaxSmoke(t *testing.T) {
	if os.Getenv("RUN_IMAGE_GENERATE_SMOKE") != "1" {
		t.Skip("set RUN_IMAGE_GENERATE_SMOKE=1 to call the configured MiniMax image API")
	}
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(repoRoot, "config/config.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.SelectModel(""); err != nil {
		t.Fatal(err)
	}
	registry, err := NewToolFactory().BuildRegistry(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	if backend, ok := registry.GetBackend("image_generate"); !ok || backend != BackendLocal {
		t.Fatalf("image_generate backend = %q, found=%v; want local", backend, ok)
	}
	tool, ok := registry.Get("image_generate")
	if !ok {
		t.Fatal("image_generate was not registered")
	}
	workspace := t.TempDir()
	result, err := tool.Execute(context.Background(), ToolInvocation{
		Name:      "image_generate",
		Workspace: workspace,
		Args:      json.RawMessage(`{"prompt":"minimal flat illustration of a single blue circle centered on a plain white background, no text","aspect_ratio":"1:1","result_image_path":"generated/blue-circle.png"}`),
		Timeout:   240 * time.Second,
	})
	if err != nil || result.IsError {
		t.Fatalf("image_generate smoke failed: result=%#v err=%v", result, err)
	}
	localPath, _ := result.Value["local_path"].(string)
	if localPath == "" {
		t.Fatalf("image_generate returned no local_path: %#v", result.Value)
	}
	info, err := os.Stat(localPath)
	if err != nil {
		t.Fatalf("generated image is missing: %v", err)
	}
	if info.Size() == 0 {
		t.Fatalf("generated image is empty: %s", localPath)
	}
	if result.Value["model"] != cfg.Tools.ImageGenerate.ModelName {
		t.Fatalf("unexpected model in result: %#v", result.Value["model"])
	}
}
