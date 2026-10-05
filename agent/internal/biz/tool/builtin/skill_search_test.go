package builtin

import (
	"os"
	"strings"
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

// readSkillMeta must return the frontmatter description only. The previous
// implementation skipped lines starting with "---" but kept every "key: value"
// line, so results[].Description was polluted with name:/tags:/triggers: rows.
func TestReadSkillMetaReturnsFrontmatterDescriptionOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SKILL.md")
	content := "---\n" +
		"name: sample-skill\n" +
		"description: Use when 处理三维资产导出。触发：「导出 GLB」。\n" +
		"priority: 70\n" +
		"tags:\n" +
		"  - 3d\n" +
		"  - glb\n" +
		"triggers:\n" +
		"  - 导出 GLB\n" +
		"---\n" +
		"\n# sample-skill\n\n正文。\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	desc, _ := readSkillMeta(path)
	want := "Use when 处理三维资产导出。触发：「导出 GLB」。"
	if desc != want {
		t.Fatalf("description polluted\n got: %q\nwant: %q", desc, want)
	}
	for _, leak := range []string{"name:", "priority:", "tags:", "triggers:", "sample-skill"} {
		if strings.Contains(desc, leak) {
			t.Fatalf("description leaked %q: %q", leak, desc)
		}
	}
}

// A body description (no frontmatter) must keep working.
func TestReadSkillMetaFallsBackToBody(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(path, []byte("# body-skill\n生成结构化报告。\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	desc, _ := readSkillMeta(path)
	if desc != "生成结构化报告。" {
		t.Fatalf("body fallback broken: %q", desc)
	}
}
