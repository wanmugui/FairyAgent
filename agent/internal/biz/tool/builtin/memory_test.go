package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMemoryPathResolutionAndSearch(t *testing.T) {
	memoryRoot := t.TempDir()
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(memoryRoot, "date-memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memoryRoot, "memory.md"), []byte("# 长期记忆\n用户偏好 Go 语言。"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memoryRoot, "date-memory", "2026-08-19.md"), []byte("今天完成了上下文压缩机制。"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, root, err := resolveLocalReadablePathWithMemory(workspace, "", memoryRoot, "memory://date-memory/2026-08-19.md")
	if err != nil {
		t.Fatal(err)
	}
	if root != "memory" {
		t.Fatalf("unexpected root kind: %q", root)
	}
	if want := filepath.Join(memoryRoot, "date-memory", "2026-08-19.md"); got != want {
		t.Fatalf("unexpected memory path: got %q want %q", got, want)
	}

	tool := NewLocalMemorySearchTool(localFileTestSchema("memory_search"), memoryRoot)
	result := executeLocalFileTool(t, tool, workspace, `{"query":"上下文压缩"}`)
	if result.IsError {
		t.Fatalf("memory_search failed: %v", result.Value)
	}
	hits, ok := result.Value["results"].([]memoryHit)
	if !ok {
		t.Fatalf("results has unexpected type: %T", result.Value["results"])
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one memory hit")
	}
	if !strings.HasPrefix(hits[0].Path, "memory://") {
		t.Fatalf("unexpected memory result path: %q", hits[0].Path)
	}
}

func TestWriteAndReadMemoryScheme(t *testing.T) {
	memoryRoot := t.TempDir()
	workspace := t.TempDir()
	writeTool := NewLocalWriteFileToolWithConfig(localFileTestSchema("write_file"), WritableFileToolConfig{MemoryRoot: memoryRoot})
	result := executeLocalFileTool(t, writeTool, workspace, `{"file_path":"memory://memory.md","content":"hello memory"}`)
	if result.IsError {
		t.Fatalf("write_file memory failed: %v", result.Value)
	}
	if result.Value["path"] != "memory://memory.md" {
		t.Fatalf("unexpected write path: %v", result.Value["path"])
	}
	raw, err := os.ReadFile(filepath.Join(memoryRoot, "memory.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "hello memory" {
		t.Fatalf("unexpected memory content: %q", string(raw))
	}

	readTool := NewLocalReadFileToolWithConfig(localFileTestSchema("read_file"), ReadFileToolConfig{MemoryRoot: memoryRoot})
	readResult := executeLocalFileTool(t, readTool, workspace, `{"file_path":"memory://memory.md"}`)
	if readResult.IsError {
		t.Fatalf("read_file memory failed: %v", readResult.Value)
	}
	if !strings.Contains(readResult.Value["content"].(string), "hello memory") {
		t.Fatalf("unexpected read content: %v", readResult.Value["content"])
	}
}
