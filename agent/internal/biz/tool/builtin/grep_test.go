package builtin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalGrepFindsHits(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "a.go"), []byte("package a\n// TODO hello\nfunc F() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "b.txt"), []byte("TODO world\nplain\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := executeLocalFileTool(t, NewLocalGrepTool(localFileTestSchema("grep")), workspace, `{"pattern":"TODO","path":"."}`)
	if result.IsError {
		t.Fatalf("unexpected error: %#v", result.Value)
	}
	hits, ok := result.Value["hits"].([]grepHit)
	if !ok {
		t.Fatalf("hits missing or wrong type: %#v", result.Value)
	}
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits, got %d (%#v)", len(hits), hits)
	}
}

func TestLocalGrepGlobFiltersFiles(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "a.go"), []byte("TODO x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "b.txt"), []byte("TODO y\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := executeLocalFileTool(t, NewLocalGrepTool(localFileTestSchema("grep")), workspace, `{"pattern":"TODO","path":".","glob":"*.go"}`)
	if result.IsError {
		t.Fatalf("unexpected error: %#v", result.Value)
	}
	hits := result.Value["hits"].([]grepHit)
	if len(hits) != 1 || hits[0].Path != "a.go" {
		t.Fatalf("expected only a.go hit, got %#v", hits)
	}
}

func TestLocalGrepRejectsBadRegex(t *testing.T) {
	workspace := t.TempDir()
	result := executeLocalFileTool(t, NewLocalGrepTool(localFileTestSchema("grep")), workspace, `{"pattern":"(unbalanced","path":"."}`)
	if !result.IsError {
		t.Fatalf("expected error for bad regex, got %#v", result.Value)
	}
}
