package main

import "testing"

// Regression guard: the batch of skills registered on 2026-10-04 must be
// reachable from the Chinese phrasing a user would actually type.
//
// skill_coverage_test.go only proves tags/triggers are non-empty. This test
// proves they are *usable*: it scores realistic requests through
// localSkillScore and requires the intended skill to clear the fail-open
// select gate of 8. web-game-foundations scored 0 for 「做个游戏网页」 because
// its tags were architecture vocabulary with no game terms at all.
//
// Do not add cases that a sibling skill should own: this asserts each request
// reaches one intended skill, not that the routing is exclusive.
func TestRecallProbeNewSkills(t *testing.T) {
	byName := map[string]SkillReg{}
	for _, reg := range loadRealSkillRegistry(t) {
		byName[reg.Name] = reg
	}
	cases := []struct{ req, want string }{
		{"把图转成 3D", "3d-asset-pipeline"},
		{"这个图能生成模型吗", "planning-3d-asset-pipeline"},
		{"出三视图", "authoring-three-view-references"},
		{"三视图转模型", "generating-3d-models"},
		{"拿这个模型做动画", "animating-3d-assets"},
		{"模型没法用 贴图不对", "finalizing-3d-assets"},
		{"我要一个 3D 打印的支架", "3d-model"},
		{"做个仪表盘", "frontend-app-builder"},
		{"测试前端 控制台报错", "frontend-testing-debugging"},
		{"做个游戏网页", "web-game-foundations"},
		{"游戏 HUD 界面", "game-ui-frontend"},
		{"设计系统 主题切换", "design-system-patterns"},
		{"加个动效", "interaction-design"},
		{"排版不好看", "visual-design-foundations"},
		{"配色太丑了", "ui-ux-pro-max"},
		{"补间动画", "gsap"},
		{"把这个视频剪成竖屏加字幕", "video-edit"},
		{"X 分钟后提醒我", "scheduled-tasks"},
		{"接微信 把文件发到微信", "wechat-bot"},
		{"用 bpy 建模做个齿轮", "bpy-model"},
		{"导出 GLB", "web-3d-asset-pipeline"},
		{"three.js 运行时", "three-webgl-game"},
	}
	for _, c := range cases {
		reg, ok := byName[c.want]
		if !ok {
			t.Errorf("MISSING SKILL %q", c.want)
			continue
		}
		s := localSkillScore(c.req, reg)
		flag := "ok "
		if s < 8 {
			flag = "LOW"
		}
		t.Logf("%s score=%-4d %-26q -> %s  tags=%v", flag, s, c.req, c.want, reg.Tags)
		if s < 8 {
			t.Errorf("LOW RECALL %q -> %q scored %d (<8)", c.req, c.want, s)
		}
	}
}

// Regression guard: prompt-authoring must be reachable from the phrasings a
// user actually types when they want a prompt written or expanded for them.
// Same fail-open gate of 8 as the other recall cases.
func TestPromptAuthoringRecall(t *testing.T) {
	byName := map[string]SkillReg{}
	for _, reg := range loadRealSkillRegistry(t) {
		byName[reg.Name] = reg
	}
	reg, ok := byName["prompt-authoring"]
	if !ok {
		t.Fatal("prompt-authoring not registered")
	}
	cases := []string{
		"帮我写个 prompt", "这个 prompt 怎么写", "把需求扩写一下",
		"写详细点", "prompt 优化一下", "把这个要求整理成一段话",
		"怎么写才能出好效果", "prompt 模板", "帮我把 prompt 写专业点",
	}
	for _, q := range cases {
		s := localSkillScore(q, reg)
		flag := "ok "
		if s < 8 {
			flag = "LOW"
		}
		t.Logf("%s score=%-4d %q", flag, s, q)
		if s < 8 {
			t.Errorf("LOW RECALL %q scored %d", q, s)
		}
	}
}
