package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Skill 注册表是每轮系统提示的常驻内容，token 大头。
// 这组测试盯两件事：
//  1. 压缩后仍能被解析、字段语义不丢（name 必须在，description 要能用）
//  2. 压缩比确实达标 —— 回归到全量会让 token 悄悄翻回去，没有任何报错

func fixtureSkills() []SkillReg {
	return []SkillReg{
		{
			Name:        "ida-reverse",
			Description: strings.Repeat("IDA Pro 逆向分析辅助技能。", 12),
			Location:    "local:///skills/ida-reverse/SKILL.md",
			Tags:        []string{"ida", "reverse", "decompile", "apk"},
			Triggers:    []string{"IDA", "逆向", "反编译", "破解", "找密码", "分析 exe"},
			Priority:    66,
			Enabled:     boolPtr(true),
		},
		{
			Name:        "daily-news",
			Description: "当用户问今天的新闻时使用。",
			Location:    "local:///skills/daily-news/SKILL.md",
			Tags:        []string{"news"},
			Triggers:    []string{"今天的新闻"},
			Priority:    58,
			Enabled:     boolPtr(true),
		},
	}
}

// 压缩模式必须仍是合法 JSON，且每条都能取出 name
func TestSkillRegistrySlimJSONStaysParseable(t *testing.T) {
	out := buildSkillRegistryJSONSlim(fixtureSkills())

	var parsed []map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("压缩后的注册表不是合法 JSON: %v", err)
	}
	if len(parsed) != 2 {
		t.Fatalf("条目数变了: got %d want 2", len(parsed))
	}
	if parsed[0]["name"] != "ida-reverse" {
		t.Fatalf("name 字段丢失或错位: %v", parsed[0]["name"])
	}
}

// name 和 priority 是模型选 skill 的关键信号，压缩后必须保留
func TestSkillRegistrySlimKeepsSelectionSignals(t *testing.T) {
	out := buildSkillRegistryJSONSlim(fixtureSkills())

	var parsed []map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("不是合法 JSON: %v", err)
	}
	for _, e := range parsed {
		if _, ok := e["name"]; !ok {
			t.Fatalf("条目缺 name: %v", e)
		}
		if _, ok := e["priority"]; !ok {
			t.Fatalf("条目缺 priority: %v", e)
		}
		if d, ok := e["description"].(string); !ok || strings.TrimSpace(d) == "" {
			t.Fatalf("条目缺 description: %v", e)
		}
	}
}

// 压缩比必须显著 —— 全量是 70 条 skill 的常驻开销
func TestSkillRegistrySlimReducesSize(t *testing.T) {
	skills := fixtureSkills()
	full := buildSkillRegistryJSON(skills)
	slim := buildSkillRegistryJSONSlim(skills)

	if len(slim) >= len(full) {
		t.Fatalf("压缩后没有变小: slim=%d full=%d", len(slim), len(full))
	}
	// description 必须被截断
	var parsed []map[string]any
	_ = json.Unmarshal([]byte(slim), &parsed)
	if d, _ := parsed[0]["description"].(string); len(d) >= 12*len("IDA Pro 逆向分析辅助技能。") {
		t.Fatalf("description 未被截断: len=%d", len(d))
	}
}

// 不含 tags / triggers / location —— 这三个是 token 大头，
// 但召回在 skill_search 里另有来源，注入时不需要
func TestSkillRegistrySlimDropsHeavyFields(t *testing.T) {
	slim := buildSkillRegistryJSONSlim(fixtureSkills())
	for _, banned := range []string{`"tags"`, `"triggers"`, `"location"`} {
		if strings.Contains(slim, banned) {
			t.Fatalf("压缩后仍含重字段 %s", banned)
		}
	}
}

// 全量模式必须保持原样 —— 有别的调用方依赖完整字段
func TestSkillRegistryFullJSONUnchanged(t *testing.T) {
	full := buildSkillRegistryJSON(fixtureSkills())
	for _, want := range []string{`"tags"`, `"triggers"`, `"location"`} {
		if !strings.Contains(full, want) {
			t.Fatalf("全量模式丢失字段 %s", want)
		}
	}
}

// enabled=false 的条目在压缩视图里要能被看出来，
// 否则模型会推荐一个根本没启用的 skill
func TestSkillRegistrySlimMarksDisabled(t *testing.T) {
	skills := fixtureSkills()
	skills[0].Enabled = boolPtr(false)
	slim := buildSkillRegistryJSONSlim(skills)

	var parsed []map[string]any
	if err := json.Unmarshal([]byte(slim), &parsed); err != nil {
		t.Fatalf("不是合法 JSON: %v", err)
	}
	if v, ok := parsed[0]["enabled"]; !ok || v == true {
		t.Fatalf("disabled 条目未标记 enabled=false: %v", parsed[0])
	}
}

func TestBuildSkillRegistrySlimEmptyReturnsEmptyArray(t *testing.T) {
	if got := buildSkillRegistryJSONSlim(nil); got != "[]" {
		t.Fatalf("空注册表应返回 []，got %q", got)
	}
}

func boolPtr(b bool) *bool { return &b }
