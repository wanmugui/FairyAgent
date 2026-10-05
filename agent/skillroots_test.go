package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBridgeSkillRegistryAddsChineseAliasesForEnglishSkill(t *testing.T) {
	// This is the exact shape of a skill installed by `npx skills add -g`:
	// an English-only description, therefore zero CJK bigrams for the
	// reverse matcher to hit.
	in := []SkillReg{{
		Name:        "ui-ux-pro-max",
		Description: "Your AI design intelligence system. Covers UI styles, industry color palettes, font pairings and UX rules.",
		Triggers:    []string{"design system", "ui ux"},
	}}
	out := bridgeSkillRegistry(in)
	got := out[0].Triggers
	for _, want := range []string{"设计感", "美化", "太丑", "好看", "设计系统", "字体", "配色"} {
		if !containsString(got, want) {
			t.Fatalf("ui-ux-pro-max 触发词缺少中文别名 %q，实际=%v", want, got)
		}
	}
	// Original authored triggers must survive, never be replaced.
	for _, want := range []string{"design system", "ui ux"} {
		if !containsString(got, want) {
			t.Fatalf("原有触发词 %q 被覆盖了，实际=%v", want, got)
		}
	}
	// Input slice must not be mutated in place.
	if containsString(in[0].Triggers, "设计感") {
		t.Fatal("bridgeSkillRegistry 修改了入参切片，注册表原值被污染")
	}
}

func TestBridgeSkillRegistryMapsAccessibilityConcepts(t *testing.T) {
	in := []SkillReg{{
		Name:        "web-accessibility",
		Description: "WCAG 2.1 audit: color contrast, keyboard navigation, screen reader support and ARIA attributes.",
		Triggers:    []string{"wcag aa", "a11y"},
	}}
	got := bridgeSkillRegistry(in)[0].Triggers
	for _, want := range []string{"无障碍", "对比度", "读屏", "键盘导航"} {
		if !containsString(got, want) {
			t.Fatalf("web-accessibility 触发词缺少 %q，实际=%v", want, got)
		}
	}
}

func TestBridgeSkillRegistrySkipsUnrelatedSkill(t *testing.T) {
	in := []SkillReg{{
		Name:        "go-testing-guide",
		Description: "Guidance for writing Go unit tests using the standard library.",
		Triggers:    []string{"go test"},
	}}
	out := bridgeSkillRegistry(in)
	if len(out[0].Triggers) != len(in[0].Triggers) {
		t.Fatalf("无关技能不应被加词，实际=%v", out[0].Triggers)
	}
}

func TestBridgeSkillRegistryDoesNotDuplicateExistingChineseTrigger(t *testing.T) {
	in := []SkillReg{{
		Name:        "ui-ux-pro-max",
		Description: "UI ux design system reference.",
		Triggers:    []string{"设计感", "ui ux"},
	}}
	out := bridgeSkillRegistry(in)
	count := 0
	for _, trigger := range out[0].Triggers {
		if trigger == "设计感" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("设计感 出现 %d 次，应去重。实际=%v", count, out[0].Triggers)
	}
}

func TestDiscoverSkillRegistryAtReadsSkillFromUserLevelRoot(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "frontend-design")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: frontend-design\ndescription: Layout, component structure and modern web design patterns.\n---\n\n# Frontend Design\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	registry := discoverSkillRegistryAt(root)
	if len(registry) != 1 {
		t.Fatalf("期望发现 1 个技能，实际=%d", len(registry))
	}
	got := registry[0]
	if got.Name != "frontend-design" {
		t.Fatalf("名称解析错误，实际=%q", got.Name)
	}
	// The location must be directly readable, otherwise the router would tell
	// the agent to load a file that does not exist.
	if !strings.HasPrefix(got.Location, "local://") {
		t.Fatalf("location 应为 local:// 绝对形式，实际=%q", got.Location)
	}
	if !strings.Contains(got.Location, "frontend-design") {
		t.Fatalf("location 丢失技能目录，实际=%q", got.Location)
	}
	if got.Description == "" {
		t.Fatal("description 未从 frontmatter 读出")
	}
}

func TestDiscoverSkillRegistryAtToleratesMissingRoot(t *testing.T) {
	if got := discoverSkillRegistryAt(filepath.Join(t.TempDir(), "nope")); len(got) != 0 {
		t.Fatalf("缺失目录应返回空，实际=%v", got)
	}
	if got := discoverSkillRegistryAt(""); len(got) != 0 {
		t.Fatalf("空字符串应返回空，实际=%v", got)
	}
}

func TestUserLevelSkillRootsOnlyReturnsExistingDirs(t *testing.T) {
	for _, root := range userLevelSkillRoots() {
		info, err := os.Stat(root)
		if err != nil {
			t.Fatalf("返回了不存在的根 %q: %v", root, err)
		}
		if !info.IsDir() {
			t.Fatalf("返回的不是目录 %q", root)
		}
		if !filepath.IsAbs(root) {
			t.Fatalf("根应为绝对路径，实际=%q", root)
		}
	}
}

// TestUserLevelSkillRootsDiscoversGloballyInstalledSkills is the end-to-end
// check: skills installed with `npx skills add -g` must now be discoverable.
// Before the multi-root change these files were on disk and completely invisible.
func TestUserLevelSkillRootsDiscoversGloballyInstalledSkills(t *testing.T) {
	roots := userLevelSkillRoots()
	if len(roots) == 0 {
		t.Skip("本机没有用户级技能根目录，跳过")
	}
	found := map[string]string{}
	for _, root := range roots {
		for _, reg := range discoverSkillRegistryAt(root) {
			found[reg.Name] = reg.Location
		}
	}
	for _, want := range []string{"ui-ux-pro-max", "frontend-design", "ui-animation", "web-accessibility"} {
		loc, ok := found[want]
		if !ok {
			t.Logf("未安装 %s（可能未装），已发现=%v", want, keysOf(found))
			continue
		}
		if !strings.HasPrefix(loc, "local://") {
			t.Fatalf("%s 的 location 不可直接读取：%q", want, loc)
		}
		t.Logf("发现已安装技能 %s -> %s", want, loc)
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
