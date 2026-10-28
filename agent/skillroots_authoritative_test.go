package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// config/config.json 是 skill 注册表的**唯一真相源**。
// 目录扫描（config.SkillsDir、~/.fairy/skills）只允许做两件事：
//   1. 给 config 里已有的条目补 metadata（description/tags/triggers）
//   2. 报告一个「磁盘上有但 config 没登记」的差异，让主人能补登
//
// 它**不允许**独立新增条目 —— 那样注册表就变成「目录扫到什么算什么」，
// config 里精心维护的分类/核心/启用状态会被新出现的目录条目静默绕过，
// 而且主人改 config 也改不动运行时行为。
//
// 这组测试盯的就是这条语义。上一版把 config.skills 删到只剩 1 条「消除重复」，
// 恰好是把真相源删空 —— 测试就是为了让那种方向回不来。

// 造一个最小 config + skill 目录树
func mkSkillTree(t *testing.T, root, name, desc string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n正文\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// config 里登记了 → 必须出现在注册表里
func TestRegistryIncludesConfiguredSkill(t *testing.T) {
	root := t.TempDir()
	mkSkillTree(t, root, "alpha", "配置登记的 skill")

	cfg := &Config{
		SkillsDir: root,
		Skills: []SkillReg{{Name: "alpha", Location: filepath.Join(root, "alpha", "SKILL.md")}},
	}

	got := DiscoverSkillRegistriesAll(cfg)
	if !hasSkill(got, "alpha") {
		t.Fatalf("config 登记的 alpha 没进注册表: %v", skillNames(got))
	}
}

// 目录里有但 config 没登记 → 不应静默进入注册表
func TestRegistryExcludesUnconfiguredDiskSkill(t *testing.T) {
	root := t.TempDir()
	mkSkillTree(t, root, "alpha", "配置登记的")
	mkSkillTree(t, root, "ghost", "磁盘上多出来的、config 未登记")

	cfg := &Config{
		SkillsDir: root,
		Skills:    []SkillReg{{Name: "alpha", Location: filepath.Join(root, "alpha", "SKILL.md")}},
	}

	got := DiscoverSkillRegistriesAll(cfg)
	if hasSkill(got, "ghost") {
		t.Fatalf("ghost 未在 config 登记却进了注册表 —— config 就不是唯一真相源了: %v", skillNames(got))
	}
	if !hasSkill(got, "alpha") {
		t.Fatalf("alpha 被误删: %v", skillNames(got))
	}
}

// config 登记了但磁盘文件不存在 → 仍然保留条目，
// 主人可能还没写文件，不该让配置凭空消失
func TestRegistryKeepsConfiguredWithoutFile(t *testing.T) {
	root := t.TempDir()
	cfg := &Config{
		SkillsDir: root,
		Skills:    []SkillReg{{Name: "planned", Location: filepath.Join(root, "planned", "SKILL.md")}},
	}

	got := DiscoverSkillRegistriesAll(cfg)
	if !hasSkill(got, "planned") {
		t.Fatalf("config 登记但文件缺失的 planned 被丢弃: %v", skillNames(got))
	}
}

// 目录扫描必须能补 metadata，否则注册表就只剩 name，太薄没法召回
func TestRegistryBackfillsMetadataFromDisk(t *testing.T) {
	root := t.TempDir()
	mkSkillTree(t, root, "alpha", "磁盘上的完整描述")

	cfg := &Config{
		SkillsDir: root,
		Skills:    []SkillReg{{Name: "alpha", Location: filepath.Join(root, "alpha", "SKILL.md")}},
	}

	got := DiscoverSkillRegistriesAll(cfg)
	found := findSkill(got, "alpha")
	if found == nil {
		t.Fatalf("alpha 不在注册表")
	}
	if found.Description == "" {
		t.Fatalf("目录扫描没补上 description，注册表太薄无法召回: %+v", found)
	}
}

// config 里的人工设定（enabled/priority）不能被磁盘扫描覆盖
func TestRegistryKeepsConfiguredOverrides(t *testing.T) {
	root := t.TempDir()
	mkSkillTree(t, root, "alpha", "磁盘描述")

	off := false
	cfg := &Config{
		SkillsDir: root,
		Skills: []SkillReg{{
			Name:     "alpha",
			Location: filepath.Join(root, "alpha", "SKILL.md"),
			Enabled:  &off,
			Priority: 99,
		}},
	}

	found := findSkill(DiscoverSkillRegistriesAll(cfg), "alpha")
	if found == nil {
		t.Fatalf("alpha 不在注册表")
	}
	if found.Enabled == nil || *found.Enabled {
		t.Fatalf("config 里 enabled=false 被磁盘扫描覆盖了: %+v", found.Enabled)
	}
	if found.Priority != 99 {
		t.Fatalf("config 里 priority=99 被覆盖成 %d", found.Priority)
	}
}

// 真实 config.json 必须自身完备：每条都在磁盘上有对应文件，
// 且磁盘上没有「未登记」的漏网之鱼。这条会挡住上次那种删法。
func TestRealConfigIsCompleteAndAuthoritative(t *testing.T) {
	cfg, err := LoadConfig("..", "config/config.json")
	if err != nil {
		t.Skipf("config 不可用: %v", err)
	}

	if len(cfg.Skills) == 0 {
		t.Fatalf("config.skills 为空 —— 它是唯一真相源，不能空")
	}

	// 每个已登记的都要能在磁盘上找到文件
	var missing []string
	for _, s := range cfg.Skills {
		if s.Name == "" {
			missing = append(missing, "(空 name)")
			continue
		}
		loc := resolveSkillBodyPath(cfg, s)
		if loc == "" {
			continue
		}
		if _, err := os.Stat(loc); err != nil {
			missing = append(missing, s.Name+" ("+loc+")")
		}
	}
	if len(missing) > 0 {
		t.Fatalf("config 登记了但磁盘上没有文件: %v", missing)
	}

	// 磁盘上不能有未登记的：否则说明有人改了目录却没更新 config
	primary := cfg.ResolvePath(cfg.SkillsDir)
	disk := map[string]bool{}
	for _, s := range DiscoverSkillRegistry(primary) {
		disk[s.Name] = true
	}
	for _, root := range userLevelSkillRoots() {
		for _, s := range discoverSkillRegistryAt(root) {
			disk[s.Name] = true
		}
	}
	var unregistered []string
	for name := range disk {
		if !hasSkill(cfg.Skills, name) {
			unregistered = append(unregistered, name)
		}
	}
	if len(unregistered) > 0 {
		t.Fatalf("磁盘上有 %d 个 skill 未登记进 config（config 就不是唯一真相源）: %v",
			len(unregistered), unregistered)
	}
}

// 注册表必须仍是合法 JSON 且能往返（前端要读它）
func TestRealRegistryRoundTripsJSON(t *testing.T) {
	cfg, err := LoadConfig("..", "config/config.json")
	if err != nil {
		t.Skipf("config 不可用: %v", err)
	}
	all := DiscoverSkillRegistriesAll(cfg)
	b, err := json.Marshal(all)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var back []SkillReg
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	if len(back) != len(all) {
		t.Fatalf("往返后条目数变了: %d -> %d", len(all), len(back))
	}
}

func hasSkill(list []SkillReg, name string) bool {
	return findSkill(list, name) != nil
}

func findSkill(list []SkillReg, name string) *SkillReg {
	for i := range list {
		if list[i].Name == name {
			return &list[i]
		}
	}
	return nil
}

func skillNames(list []SkillReg) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		out = append(out, s.Name)
	}
	return out
}
