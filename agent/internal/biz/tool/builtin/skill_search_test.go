package builtin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSkillSearchDiscoversLocalSkills(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, filepath.Join(root, "report-builder", "SKILL.md"), "# report-builder\n生成结构化报告。")
	writeSkillFile(t, filepath.Join(root, "image-tool", "plugin.json"), `{"name":"image-tool","description":"图片处理插件"}`)

	entries := discoverSkillEntries(root)
	if len(entries) != 2 {
		t.Fatalf("expected 2 skill entries, got %d", len(entries))
	}
	tool := NewLocalSkillSearchTool(localFileTestSchema("skill_search"), root)
	result := executeLocalFileTool(t, tool, t.TempDir(), `{"query":"report"}`)
	if result.IsError {
		t.Fatalf("skill_search failed: %v", result.Value)
	}
	results, ok := result.Value["results"].([]skillEntry)
	if !ok {
		t.Fatalf("results has unexpected type: %T", result.Value["results"])
	}
	if len(results) == 0 || results[0].Name != "report-builder" {
		t.Fatalf("unexpected skill results: %#v", results)
	}
}

func TestSkillSearchReturnsAllWhenQueryEmpty(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, filepath.Join(root, "ppt-maker", "SKILL.md"), "# ppt-maker\n制作 PPT。")
	tool := NewLocalSkillSearchTool(localFileTestSchema("skill_search"), root)
	result := executeLocalFileTool(t, tool, t.TempDir(), `{"query":""}`)
	if result.IsError {
		t.Fatalf("skill_search failed: %v", result.Value)
	}
	results, ok := result.Value["results"].([]skillEntry)
	if !ok {
		t.Fatalf("results has unexpected type: %T", result.Value["results"])
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func writeSkillFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
