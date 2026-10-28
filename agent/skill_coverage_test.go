package main

import (
	"path/filepath"
	"testing"
)

func loadRealSkillRegistry(t *testing.T) []SkillReg {
	t.Helper()
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
	if len(registry) == 0 {
		t.Fatal("skill registry is empty")
	}
	return registry
}

// Every registered skill must carry tags/triggers/priority, otherwise local
// recall can never surface it (priority is also the tiebreaker signal).
func TestSkillMetadataCoverage(t *testing.T) {
	for _, reg := range loadRealSkillRegistry(t) {
		if len(reg.Tags) == 0 {
			t.Errorf("skill %q missing tags", reg.Name)
		}
		if len(reg.Triggers) == 0 {
			t.Errorf("skill %q missing triggers", reg.Name)
		}
		if reg.Priority <= 0 {
			t.Errorf("skill %q missing positive priority", reg.Name)
		}
	}
}

// A JSON-shaped skill manifest must declare a name to be registered at all.
//
// Regression: skills/mv-render/vendor/template/engine/fonts/manifest.json is a
// *font inventory* (a top-level array with no "name" key). skillDeclaredName
// returned "" for it, so discovery fell back to the directory name and
// registered a bogus skill called "fonts" — which both polluted skill_search
// (an empty skill you could match) and failed TestSkillMetadataCoverage with
// "skill \"fonts\" missing tags/triggers/priority".
//
// The guard matters in both directions: files that DO declare a name must still
// be registered, including ones inside a vendor/ subtree. vendor/SKILL.md
// declares vocaloid-style-mv and is registered on purpose — a naive "skip all of
// vendor/" fix would silently drop that skill.
func TestJSONSkillManifestMustDeclareName(t *testing.T) {
	byName := map[string]SkillReg{}
	for _, reg := range loadRealSkillRegistry(t) {
		byName[reg.Name] = reg
	}
	if _, ok := byName["fonts"]; ok {
		t.Errorf("font inventory manifest.json must not be registered as a skill \"fonts\"")
	}
	if _, ok := byName["vocaloid-style-mv"]; !ok {
		t.Errorf("vendor/SKILL.md declares vocaloid-style-mv and must stay registered; " +
			"a skip-all-of-vendor fix would drop it")
	}
}

// Natural Chinese requests must be able to recall the matching skill locally,
// i.e. score >= 8 so it survives the fail-open select threshold.
func TestChineseRequestRecallsExpectedSkill(t *testing.T) {
	byName := map[string]SkillReg{}
	for _, reg := range loadRealSkillRegistry(t) {
		byName[reg.Name] = reg
	}
	cases := []struct {
		request string
		want    string
	}{
		{"帮我逆向分析这个 APK", "apk-reverse"},
		{"js 逆向 签名定位", "js-reverse"},
		{"下载这个 B站视频", "video-downloader"},
		{"总结这个视频讲了什么", "video-summary"},
		{"今天的新闻", "daily-news"},
		{"帮我做一份深度调研报告", "deep-research"},
		{"制作一套 PPT", "ppt-maker"},
		{"看看这个固件有没有漏洞", "firmware-pentest"},
		{"帮我审计这个 LLM 应用的安全性", "llm-security"},
		{"分析这个游戏的 UI 和机制设计", "fairy-reference-analysis"},
		{"协议的抓包和 protobuf 解析", "protocol-reverse"},
		{"把旧版本的符号迁移到新版本", "binary-diff"},
	}
	for _, c := range cases {
		reg, ok := byName[c.want]
		if !ok {
			t.Errorf("skill %q not found in registry", c.want)
			continue
		}
		if score := localSkillScore(c.request, reg); score < 8 {
			t.Errorf("request %q -> %q scored %d, want >= 8", c.request, c.want, score)
		}
	}
}
