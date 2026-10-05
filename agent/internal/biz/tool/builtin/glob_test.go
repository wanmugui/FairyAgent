package builtin

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLocalGlobReturnsWorkspaceRelativeSortedMatches(t *testing.T) {
	workspace := t.TempDir()
	for _, relative := range []string{"z.txt", "nested/a.txt", "nested/b.go"} {
		path := filepath.Join(workspace, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(relative), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result := executeLocalFileTool(t, NewLocalGlobTool(localFileTestSchema("glob")), workspace, `{"pattern":"**/*.txt","path":"."}`)
	if result.IsError {
		t.Fatalf("unexpected glob error: %#v", result.Value)
	}
	got, ok := result.Value["matches"].([]string)
	if !ok {
		t.Fatalf("unexpected matches type: %T (%#v)", result.Value["matches"], result.Value)
	}
	want := []string{"nested/a.txt", "z.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected matches: got %#v want %#v", got, want)
	}
}

func TestLocalGlobBraceExpansion(t *testing.T) {
	workspace := t.TempDir()
	for _, relative := range []string{"src/App.js", "src/Sidebar.jsx", "src/main.ts", "src/style.css"} {
		path := filepath.Join(workspace, filepath.FromSlash(relative))
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		_ = os.WriteFile(path, []byte(relative), 0o600)
	}
	result := executeLocalFileTool(t, NewLocalGlobTool(localFileTestSchema("glob")), workspace, `{"pattern":"**/*.{js,jsx,ts}","path":"."}`)
	if result.IsError {
		t.Fatalf("unexpected glob error: %#v", result.Value)
	}
	got, _ := result.Value["matches"].([]string)
	want := []string{"src/App.js", "src/Sidebar.jsx", "src/main.ts"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("brace matches: got %#v want %#v", got, want)
	}
}

func TestLocalGlobCaseInsensitiveFallback(t *testing.T) {
	workspace := t.TempDir()
	_ = os.MkdirAll(filepath.Join(workspace, "src"), 0o755)
	_ = os.WriteFile(filepath.Join(workspace, "src", "Sidebar.jsx"), []byte("x"), 0o600)
	result := executeLocalFileTool(t, NewLocalGlobTool(localFileTestSchema("glob")), workspace, `{"pattern":"**/sidebar.jsx","path":"."}`)
	if result.IsError {
		t.Fatalf("unexpected glob error: %#v", result.Value)
	}
	got, _ := result.Value["matches"].([]string)
	want := []string{"src/Sidebar.jsx"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("case-insensitive matches: got %#v want %#v", got, want)
	}
}

func TestLocalGlobReturnsBundledSkillPaths(t *testing.T) {
	workspace := t.TempDir()
	skillsRoot := t.TempDir()
	skillPath := filepath.Join(skillsRoot, "ppt-maker", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillPath, []byte("# skill"), 0o600); err != nil {
		t.Fatal(err)
	}

	tool := NewLocalGlobToolWithConfig(localFileTestSchema("glob"), skillsRoot)
	result := executeLocalFileTool(t, tool, workspace, `{"pattern":"*.md","path":"local:///skills/ppt-maker"}`)
	if result.IsError {
		t.Fatalf("unexpected error result: %#v", result.Value)
	}
	got, ok := result.Value["matches"].([]string)
	if !ok || !reflect.DeepEqual(got, []string{"local:///skills/ppt-maker/SKILL.md"}) {
		t.Fatalf("unexpected skill matches: %#v", result.Value)
	}
}
