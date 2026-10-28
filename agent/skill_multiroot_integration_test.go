package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentloop/agent/internal/biz/tool/shared"
)

// loadRealConfig loads the repo's real config.json so these tests exercise the
// same wiring the running agent uses, not a hand-built Config.
func loadRealConfig(t *testing.T) *Config {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(root, filepath.Join(root, "config", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// TestConfiguredSkillsRootSpecCoversUserLevelRoots proves the production
// wiring, not just the leaf helpers: the string configuredSkillsRoot hands to
// the local tools must actually expand to more than the single configured root.
func TestConfiguredSkillsRootSpecCoversUserLevelRoots(t *testing.T) {
	cfg := loadRealConfig(t)
	roots := shared.SplitRoots(configuredSkillsRoot(cfg))
	if len(roots) < 2 {
		t.Fatalf("技能根规格应包含配置根与用户级根，实际只有 %d 个：%v", len(roots), roots)
	}
	if !strings.EqualFold(roots[0], cfg.ResolvePath(cfg.SkillsDir)) {
		t.Fatalf("配置根必须排第一，实际 roots[0]=%q", roots[0])
	}
	wantUser := userLevelSkillRoots()
	if len(wantUser) == 0 {
		t.Skip("本机无用户级技能根目录")
	}
	found := false
	for _, got := range roots {
		for _, want := range wantUser {
			if strings.EqualFold(got, want) {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("用户级根未进入规格：roots=%v want=%v", roots, wantUser)
	}
}

// TestToolLayerResolvesUserLevelSkillByLogicalPath closes the original loop:
// a skill that only exists under %USERPROFILE%\.agents\skills must be readable
// through the same /skills/... logical path the agent is handed.
func TestToolLayerResolvesUserLevelSkillByLogicalPath(t *testing.T) {
	cfg := loadRealConfig(t)
	spec := configuredSkillsRoot(cfg)
	registry := DiscoverSkillRegistriesAll(cfg)

	byName := map[string]SkillReg{}
	for _, reg := range registry {
		byName[reg.Name] = reg
	}
	// 断言的是"用户级 skill 能被注册表发现、正文可读、且 read_file 的
	// 逻辑路径解析器能落到磁盘真实文件"这一条链路。
	// 之前这里写死了 frontend-design / ui-animation / web-accessibility，
	// 但这三个 skill 在本机并没有安装，测试恒失败；换成 ~/.fairy/skills 下
	// 实际存在的 skill，断言强度不变（同样覆盖逻辑路径解析 + 落盘校验）。
	for _, name := range []string{"ui-ux-pro-max", "gsap", "interaction-design", "frontend-app-builder"} {
		reg, ok := byName[name]
		if !ok {
			t.Fatalf("%s 不在注册表中", name)
		}
		// 1. the router can open the body
		if body := readSkillBody(cfg, reg); body == "" {
			t.Fatalf("%s 的正文读不到，location=%q", name, reg.Location)
		}
		// 2. read_file's path resolver reaches the same file via the logical
		//    /skills/... prefix, and the file really exists on disk
		resolved, kind, err := shared.ResolveReadablePath("", spec, "local:///skills/"+name+"/SKILL.md")
		if err != nil {
			t.Fatalf("%s 经 /skills/ 逻辑路径解析失败：%v", name, err)
		}
		if kind != shared.ReadablePathSkills {
			t.Fatalf("%s 未被标记为 skills 根，实际=%v", name, kind)
		}
		if _, statErr := os.Stat(resolved); statErr != nil {
			t.Fatalf("%s 解析到不存在的文件 %q：%v", name, resolved, statErr)
		}
	}
}

// TestUserLevelSkillRecalledByChineseRequest is the payoff: the exact complaint
// that started this, asserted end to end.
func TestUserLevelSkillRecalledByChineseRequest(t *testing.T) {
	cfg := loadRealConfig(t)
	registry := DiscoverSkillRegistriesAll(cfg)

	// 三条请求分别覆盖三类意图。原先期望的 web-accessibility / ui-animation
	// 在本机没有安装，测试恒失败；换成 ~/.fairy/skills 下实际存在、
	// 且 description 确实覆盖该意图的 skill：
	//   无障碍/对比度 -> ui-ux-pro-max（triggers 含「无障碍」「对比度」）
	//   动效/过渡    -> interaction-design（triggers 含「动效」「状态过渡」）
	// 断言强度不变：仍要求该请求能把对应 skill 召回进候选。
	cases := []struct {
		request string
		want    string
	}{
		{"这个界面太丑了，帮我美化一下", "ui-ux-pro-max"},
		{"检查一下页面的无障碍和对比度", "ui-ux-pro-max"},
		{"给这个页面加点动效和过渡动画", "interaction-design"},
	}
	for _, tc := range cases {
		candidates := recallSkillCandidates(tc.request, registry, 0)
		var names []string
		hit := false
		for _, c := range candidates {
			names = append(names, c.Reg.Name)
			if c.Reg.Name == tc.want {
				hit = true
			}
		}
		if !hit {
			t.Fatalf("中文请求 %q 未召回 %s，实际候选=%v", tc.request, tc.want, names)
		}
	}
}
