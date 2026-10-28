package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func localFileTestSchema(name string) ToolDef {
	return ToolDef{Type: "function", Function: map[string]any{"name": name}}
}

func executeLocalFileTool(t *testing.T, tool Tool, workspace string, args string) ToolResult {
	t.Helper()
	result, err := tool.Execute(context.Background(), ToolInvocation{
		Name:      tool.Name(),
		Workspace: workspace,
		Args:      json.RawMessage(args),
	})
	if err != nil {
		t.Fatalf("execute %s: %v", tool.Name(), err)
	}
	return result
}

func TestLocalReadFilePreservesLineNumbersAndTruncation(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "notes.txt")
	if err := os.WriteFile(path, []byte("zero\r\none\r\ntwo\r\nthree"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := executeLocalFileTool(t, NewLocalReadFileTool(localFileTestSchema("read_file")), workspace, `{"file_path":"notes.txt","offset":1,"limit":2}`)
	if result.IsError {
		t.Fatalf("unexpected error result: %#v", result.Value)
	}
	if got, want := result.Value["content"], "   2: one\n   3: two"; got != want {
		t.Fatalf("unexpected content: got %q want %q", got, want)
	}
	if got, want := result.Value["line_count"], 4; got != want {
		t.Fatalf("unexpected line count: got %#v want %d", got, want)
	}
	if got, want := result.Value["truncated"], true; got != want {
		t.Fatalf("unexpected truncation flag: got %#v want %v", got, want)
	}
}

func TestLocalReadFileRejectsDirectory(t *testing.T) {
	workspace := t.TempDir()
	result := executeLocalFileTool(t, NewLocalReadFileTool(localFileTestSchema("read_file")), workspace, `{"file_path":"."}`)
	if !result.IsError || result.Value["error"] == nil {
		t.Fatalf("expected directory error, got %#v", result)
	}
}

func TestLocalReadFileEnforcesConfiguredMaximumFileSize(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "large.txt")
	if err := os.WriteFile(path, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := NewLocalReadFileToolWithConfig(localFileTestSchema("read_file"), ReadFileToolConfig{MaxReadFileSizeBytes: 4})
	result := executeLocalFileTool(t, tool, workspace, `{"file_path":"large.txt"}`)
	if !result.IsError || result.Value["error"] == nil {
		t.Fatalf("expected configured file-size rejection, got %#v", result)
	}
}

func TestLocalReadFileReadsBundledSkillThroughProductionPath(t *testing.T) {
	workspace := t.TempDir()
	skillsRoot := t.TempDir()
	skillPath := filepath.Join(skillsRoot, "ppt-maker", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillPath, []byte("# PPT maker\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tool := NewLocalReadFileToolWithConfig(localFileTestSchema("read_file"), ReadFileToolConfig{SkillsRoot: skillsRoot})
	result := executeLocalFileTool(t, tool, workspace, `{"file_path":"local:///skills/ppt-maker/SKILL.md"}`)
	if result.IsError {
		t.Fatalf("unexpected error result: %#v", result.Value)
	}
	if got, want := result.Value["path"], "local:///skills/ppt-maker/SKILL.md"; got != want {
		t.Fatalf("unexpected result path: got %#v want %q", got, want)
	}
	if got := result.Value["content"]; got != "   1: # PPT maker\n   2: " {
		t.Fatalf("unexpected content: %#v", got)
	}

	result = executeLocalFileTool(t, tool, workspace, `{"file_path":"/skills/ppt-maker/SKILL.md"}`)
	if result.IsError {
		t.Fatalf("legacy registry path must remain readable: %#v", result.Value)
	}
}

func TestLocalReadFileDispatchesBinaryTargets(t *testing.T) {
	workspace := t.TempDir()
	pngPath := filepath.Join(workspace, "shot.png")
	if err := os.WriteFile(pngPath, []byte{0x89, 0x50, 0x4E, 0x47}, 0o600); err != nil {
		t.Fatal(err)
	}
	pdfPath := filepath.Join(workspace, "report.pdf")
	if err := os.WriteFile(pdfPath, []byte("%PDF-1.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := NewLocalReadFileTool(localFileTestSchema("read_file"))

	pngResult := executeLocalFileTool(t, tool, workspace, `{"file_path":"shot.png"}`)
	if pngResult.IsError {
		t.Fatalf("unexpected error: %#v", pngResult.Value)
	}
	deferred, ok := pngResult.Value["deferred_to"].(map[string]any)
	if !ok || deferred["tool"] != "image_vqa" {
		t.Fatalf("expected deferred_to=image_vqa, got %#v", pngResult.Value)
	}

	pdfResult := executeLocalFileTool(t, tool, workspace, `{"file_path":"report.pdf"}`)
	deferred, ok = pdfResult.Value["deferred_to"].(map[string]any)
	if !ok || deferred["tool"] != "document_parser" {
		t.Fatalf("expected deferred_to=document_parser, got %#v", pdfResult.Value)
	}
}
