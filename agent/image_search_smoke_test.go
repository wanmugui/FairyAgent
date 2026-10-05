package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalImageSearchSmoke(t *testing.T) {
	if os.Getenv("RUN_IMAGE_SEARCH_SMOKE") != "1" {
		t.Skip("set RUN_IMAGE_SEARCH_SMOKE=1 to call the public Bing Images endpoint")
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
	if backend, ok := registry.GetBackend("image_search"); !ok || backend != BackendLocal {
		t.Fatalf("image_search backend = %q, found=%v; want local", backend, ok)
	}
	tool, ok := registry.Get("image_search")
	if !ok {
		t.Fatal("image_search was not registered")
	}
	workspace := t.TempDir()
	result, err := tool.Execute(context.Background(), ToolInvocation{
		Name:      "image_search",
		Workspace: workspace,
		Args:      json.RawMessage(`{"query":"red apple product photo","top_k":3,"result_image_path":"downloads"}`),
		Timeout:   120 * time.Second,
	})
	if err != nil || result.IsError {
		t.Fatalf("image search smoke failed: result=%#v err=%v", result, err)
	}
	results, _ := result.Value["results"].([]map[string]any)
	if len(results) == 0 {
		t.Fatalf("image search returned no results: %#v", result.Value)
	}
	for _, item := range results {
		if localPath, _ := item["local_path"].(string); localPath != "" {
			if _, err := os.Stat(localPath); err == nil {
				return
			}
		}
	}
	t.Fatalf("image search did not download any image: %#v", result.Value)
}
