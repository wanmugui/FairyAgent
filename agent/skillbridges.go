package main

import (
	"strings"
	"unicode"
)

// conceptWords tokenises a concept into lowercase word tokens. Phrases are
// matched as a conjunction of their words rather than as a contiguous
// substring: the real ui-ux-pro-max description reads "UI styles ... UX rules",
// where "ui ux" never appears as one literal substring but both words do.
func conceptWords(concept string) []string {
	fields := strings.FieldsFunc(strings.ToLower(concept), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	return fields
}

func conceptMatches(haystack map[string]bool, concept string) bool {
	words := conceptWords(concept)
	if len(words) == 0 {
		return false
	}
	for _, word := range words {
		if !haystack[word] {
			return false
		}
	}
	return true
}

func textWords(texts ...string) map[string]bool {
	words := map[string]bool{}
	for _, text := range texts {
		for _, word := range conceptWords(text) {
			words[word] = true
		}
	}
	return words
}

// skillConceptBridge maps a concept that appears in a skill's authored metadata
// to the Chinese words users actually type.
//
// Why this exists: skillReverseTerms derives CJK bigrams from a skill's name and
// description and matches them against the request. That works only when the
// metadata already contains Chinese. A globally installed skill such as
// ui-ux-pro-max ships an English description and English triggers, so there is
// not a single Chinese bigram to match, and a request like
// "这个界面太丑了帮我美化一下" can never recall it - no matter how good the skill is.
//
// Appending Chinese terms to the triggers keeps the scoring formula, thresholds
// and ranking completely untouched: the existing CJK bigram matcher simply
// starts having something to hit.
var skillConceptBridge = []struct {
	Concept string
	Chinese []string
}{
	{"ui ux", []string{"设计感", "界面设计", "视觉设计", "美化", "好看", "太丑", "不好看", "不美观", "排版"}},
	{"ui design", []string{"设计感", "界面设计", "视觉设计", "美化", "好看", "太丑"}},
	{"design system", []string{"设计系统", "设计规范", "视觉规范", "设计语言"}},
	{"design", []string{"设计感", "设计规范", "视觉设计"}},
	{"typography", []string{"字体", "排版", "字型", "字重"}},
	{"font", []string{"字体", "字型", "字重"}},
	{"color", []string{"配色", "颜色", "色板", "色彩"}},
	{"palette", []string{"配色", "色板", "色彩"}},
	{"accessibility", []string{"无障碍", "读屏", "屏幕阅读器", "对比度", "键盘导航"}},
	{"a11y", []string{"无障碍", "读屏"}},
	{"wcag", []string{"无障碍", "无障碍合规", "对比度"}},
	{"animation", []string{"动效", "动画", "过渡", "缓动", "动起来"}},
	{"motion", []string{"动效", "动图", "动画"}},
	{"transition", []string{"过渡", "动效"}},
	{"micro interaction", []string{"微交互", "动效"}},
	{"sketch", []string{"原型", "草图"}},
	{"figma", []string{"设计稿", "原型"}},
	{"visualization", []string{"数据可视化", "图表"}},
	{"dashboard", []string{"仪表盘", "看板"}},
	{"responsive", []string{"响应式", "适配", "移动端"}},
	{"component", []string{"组件", "组件库"}},
	{"react", []string{"前端", "组件"}},
	{"layout", []string{"布局", "排版"}},
	{"icon", []string{"图标"}},
	{"gradient", []string{"渐变"}},
	{"glassmorphism", []string{"玻璃拟态", "毛玻璃"}},
	{"brutalist", []string{"野兽派"}},
	{"minimal", []string{"极简", "简约"}},
	{"retro", []string{"复古", "怀旧"}},
	{"pixel", []string{"像素", "像素风"}},
	{"parchment", []string{"羊皮纸"}},
	{"game ui", []string{"游戏界面"}},
	{"reverse engineer", []string{"逆向", "反编译"}},
}

// bridgeSkillRegistry returns a copy of the registry with Chinese aliases
// appended to the triggers of every skill whose authored metadata mentions a
// bridged concept.
func bridgeSkillRegistry(registry []SkillReg) []SkillReg {
	if len(registry) == 0 {
		return registry
	}
	out := make([]SkillReg, len(registry))
	copy(out, registry)
	for i := range out {
		haystack := textWords(out[i].Name, out[i].Description, strings.Join(out[i].Triggers, " "))
		existing := map[string]bool{}
		for _, trigger := range out[i].Triggers {
			existing[strings.ToLower(strings.TrimSpace(trigger))] = true
		}
		var added []string
		for _, entry := range skillConceptBridge {
			if !conceptMatches(haystack, entry.Concept) {
				continue
			}
			for _, term := range entry.Chinese {
				key := strings.ToLower(term)
				if existing[key] {
					continue
				}
				existing[key] = true
				added = append(added, term)
			}
		}
		if len(added) > 0 {
			out[i].Triggers = append(append([]string{}, out[i].Triggers...), added...)
		}
	}
	return out
}
