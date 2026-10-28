package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverSkillRegistryFindsDiskSkills(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "ppt-maker"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ppt-maker", "SKILL.md"), []byte("# ppt-maker\n制作 PPT 的技能"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := DiscoverSkillRegistry(root)
	if len(registry) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(registry))
	}
	if registry[0].Name != "ppt-maker" {
		t.Fatalf("unexpected skill name: %q", registry[0].Name)
	}
	if registry[0].Location != "local:///skills/ppt-maker/SKILL.md" {
		t.Fatalf("unexpected skill location: %q", registry[0].Location)
	}
}

func TestMergeSkillRegistriesKeepsConfiguredFirst(t *testing.T) {
	configured := []SkillReg{{Name: "ppt-maker", Description: "configured", Location: "/skills/ppt-maker"}}
	discovered := []SkillReg{{Name: "ppt-maker", Description: "disk", Location: "local:///skills/ppt-maker/SKILL.md"}, {Name: "new-skill", Description: "new", Location: "local:///skills/new-skill/SKILL.md"}}
	merged := mergeSkillRegistries(configured, discovered)
	if len(merged) != 2 {
		t.Fatalf("expected 2 merged skills, got %d", len(merged))
	}
	if merged[0].Description != "configured" {
		t.Fatalf("configured skill should win: %#v", merged[0])
	}
	if merged[1].Name != "new-skill" {
		t.Fatalf("unexpected appended skill: %#v", merged[1])
	}
}

func TestMergeSkillRegistriesSkipsDisabledSkills(t *testing.T) {
	disabled := false
	configured := []SkillReg{{Name: "ppt-maker", Description: "configured", Location: "/skills/ppt-maker", Enabled: &disabled}}
	discovered := []SkillReg{{Name: "ppt-maker", Description: "disk", Location: "local:///skills/ppt-maker/SKILL.md"}}
	merged := mergeSkillRegistries(configured, discovered)
	if len(merged) != 0 {
		t.Fatalf("disabled skill leaked into registry: %#v", merged)
	}
}
func TestDiscoverSkillRegistryParsesFrontmatterMetadata(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "engineering")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: engineering\ndescription: \"工程任务\"\nmetadata:\n  tags:\n    - code\n    - frontend\n  triggers:\n    - 修改代码\n  always: true\n  priority: 80\n---\n\n# Engineering\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := DiscoverSkillRegistry(root)
	if len(registry) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(registry))
	}
	skill := registry[0]
	if skill.Description != "工程任务" {
		t.Fatalf("unexpected description: %q", skill.Description)
	}
	if len(skill.Tags) != 2 || skill.Tags[0] != "code" || skill.Tags[1] != "frontend" {
		t.Fatalf("unexpected tags: %#v", skill.Tags)
	}
	if len(skill.Triggers) != 1 || skill.Triggers[0] != "修改代码" {
		t.Fatalf("unexpected triggers: %#v", skill.Triggers)
	}
	if !skill.Always || skill.Priority != 80 {
		t.Fatalf("unexpected routing metadata: %#v", skill)
	}
}
