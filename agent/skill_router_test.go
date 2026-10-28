package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRouterSkill(t *testing.T, root, name, frontmatter, body string) {
	t.Helper()
	dir := filepath.Join(root, "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\n" + frontmatter + "---\n\n# " + name + "\n\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func routerTestConfig(root string) *Config {
	return &Config{
		RepoRoot:  root,
		SkillsDir: "skills",
		SkillRouting: SkillRoutingConfig{
			Enabled:       true,
			RecallLimit:   10,
			InjectLimit:   3,
			FullBodyLimit: 2,
			CacheTTLSec:   1,
			MaxBodyRunes:  2000,
		},
	}
}

func TestBuildSkillRoutingContextForcesEngineeringForImplementationRequest(t *testing.T) {
	root := t.TempDir()
	writeRouterSkill(t, root, "fairy-engineering", "tags:\n  - code\n  - frontend\n", "REGRESSION_AND_CLICK_RULES")
	cfg := routerTestConfig(root)
	context := BuildSkillRoutingContext(cfg, "修改前端代码并做点击测试")
	if !strings.Contains(context, "fairy-engineering") {
		t.Fatalf("expected fairy-engineering in context: %s", context)
	}
	if !strings.Contains(context, "REGRESSION_AND_CLICK_RULES") {
		t.Fatalf("expected full engineering body in context: %s", context)
	}
}
func TestFairyReferenceAnalysisSkillRoutesLocally(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(root, filepath.Join(root, "config", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	// DiscoverSkillRegistriesAll, not DiscoverSkillRegistry: configuredSkillsRoot
	// now returns a path-list spec, which a single-root walk cannot parse.
	registry := DiscoverSkillRegistriesAll(cfg)
	var reference SkillReg
	for _, skill := range registry {
		if skill.Name == "fairy-reference-analysis" {
			reference = skill
			break
		}
	}
	if reference.Name == "" {
		t.Fatal("fairy-reference-analysis not found in registry")
	}
	if len(reference.Tags) == 0 || len(reference.Triggers) == 0 {
		t.Fatalf("reference skill metadata missing: %#v", reference)
	}
	if score := localSkillScore("参考这个游戏的 UI 和机制设计", reference); score <= 0 {
		t.Fatalf("reference skill did not match local recall: %d", score)
	}
}
