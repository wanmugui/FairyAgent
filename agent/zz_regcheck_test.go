package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRealSkillRootNaming 用仓库里真实的 skills/ 目录做一次回归。
// 之前的两个缺陷（YAML 块标量、声明名被目录名覆盖）都只在这个真实目录上暴露过：
// vendor/SKILL.md 的 description 是 ">-"、name 是 vocaloid-style-mv，
// 但注册表里出现的是 name="vendor"、description=">-"。
func TestRealSkillRootNaming(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "skills"))
	if err != nil {
		t.Skip(err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Skip("skills 目录不在，跳过真实目录回归")
	}
	regs := DiscoverSkillRegistry(root)
	for _, r := range regs {
		if r.Name == "vocaloid-style-mv" {
			if len(r.Description) < 100 {
				t.Fatalf("描述被截断成 %q", r.Description)
			}
			if !strings.Contains(r.Description, "music video") {
				t.Fatalf("描述内容不对: %.100s", r.Description)
			}
			return
		}
		if r.Name == "vendor" {
			t.Fatalf("仍然按目录名注册成 vendor：描述=%q", r.Description)
		}
	}
	t.Fatal("没有发现 vocaloid-style-mv")
}
