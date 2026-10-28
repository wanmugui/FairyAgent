package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalImageVQADeepSeekSmoke(t *testing.T) {
	if os.Getenv("RUN_IMAGE_VQA_SMOKE") != "1" {
		t.Skip("set RUN_IMAGE_VQA_SMOKE=1 to call the configured DeepSeek vision API")
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
	if backend, ok := registry.GetBackend("image_vqa"); !ok || backend != BackendLocal {
		t.Fatalf("image_vqa backend = %q, found=%v; want local", backend, ok)
	}
	tool, ok := registry.Get("image_vqa")
	if !ok {
		t.Fatal("image_vqa was not registered")
	}
	imagePath := filepath.Join(repoRoot, "src-tauri", "icons", "icon.png")
	result, err := tool.Execute(context.Background(), ToolInvocation{
		Name:      "image_vqa",
		Workspace: filepath.Join(repoRoot, "workspace"),
		Args:      json.RawMessage(`{"image_path":` + mustJSONString(t, imagePath) + `,"query":"请用一句中文描述这张图片的主体"}`),
		Timeout:   120 * time.Second,
	})
	if err != nil || result.IsError {
		t.Fatalf("DeepSeek vision smoke failed: result=%#v err=%v", result, err)
	}
	answer, _ := result.Value["answer"].(string)
	if strings.TrimSpace(answer) == "" {
		t.Fatalf("DeepSeek vision returned an empty answer: %#v", result.Value)
	}
	if result.Value["model"] != "deepseek-flash" {
		t.Fatalf("unexpected vision model: %#v", result.Value)
	}
}

func mustJSONString(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
