package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// resolveSkillBodyPath：上一轮多根改造埋下的真 bug 的守卫。
// discoverSkillRegistryAt 会产出 "local://C:/Users/.../SKILL.md" 这类绝对
// location，而 resolveSkillBodyPath 的 switch 只认 local:///skills/、/skills/
// 和真正的绝对路径。local:// 开头的绝对 location 会掉进 default 分支，被
// Join 到 RepoRoot 下面，得到一个根本不存在的路径，readSkillBody 返回空，
// 技能就永远只能是 card-only。
// ---------------------------------------------------------------------------

func TestResolveSkillBodyPathResolvesLocalSchemeAbsoluteLocation(t *testing.T) {
	dir := t.TempDir()
	skillFile := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(skillFile, []byte("# hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	location := "local://" + filepath.ToSlash(skillFile)
	reg := SkillReg{Name: "x", Location: location}
	cfg := &Config{RepoRoot: t.TempDir()} // 故意给一个错误的 RepoRoot

	got := resolveSkillBodyPath(cfg, reg)
	if got == "" {
		t.Fatalf("local:// 绝对 location 未被解析，location=%q", location)
	}
	if filepath.Clean(got) != filepath.Clean(skillFile) {
		t.Fatalf("解析到错误路径：期望 %q，实际 %q", skillFile, got)
	}
	if body := readSkillBody(cfg, reg); body != "# hello" {
		t.Fatalf("readSkillBody 读不到正文，实际=%q", body)
	}
}

func TestResolveSkillBodyPathResolvesPlainAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	skillFile := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(skillFile, []byte("# ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := resolveSkillBodyPath(&Config{RepoRoot: t.TempDir()}, SkillReg{Location: skillFile})
	if filepath.Clean(got) != filepath.Clean(skillFile) {
		t.Fatalf("纯绝对路径解析错误：期望 %q，实际 %q", skillFile, got)
	}
}

func TestResolveSkillBodyPathRejectsEmptyLocation(t *testing.T) {
	if got := resolveSkillBodyPath(&Config{RepoRoot: t.TempDir()}, SkillReg{}); got != "" {
		t.Fatalf("空 location 应返回空串，实际=%q", got)
	}
}

// ---------------------------------------------------------------------------
// localSkillScore：打分公式
// ---------------------------------------------------------------------------

func TestLocalSkillScoreAlwaysFlagAddsHundred(t *testing.T) {
	score := localSkillScore("任意请求", SkillReg{Name: "unrelated", Always: true})
	if score < 100 {
		t.Fatalf("Always 技能应至少 100 分，实际=%d", score)
	}
}

func TestLocalSkillScorePriorityIsTiebreakerNotBaseScore(t *testing.T) {
	// 高优先级但完全不相关的技能必须得 0，否则会把真正匹配者挤下去。
	got := localSkillScore("zzz 完全无关的请求文本", SkillReg{Name: "alpha", Priority: 300})
	if got != 0 {
		t.Fatalf("不相关技能应得 0 分，实际=%d（Priority 被误当成基础分）", got)
	}
}

// TestLocalSkillScoreAccumulatesIndependentSignals locks the property that
// matters rather than the internal weights: trigger, tag, name and priority
// each contribute on top of one another. Asserting exact points would just
// re-state the constants.
func TestLocalSkillScoreAccumulatesIndependentSignals(t *testing.T) {
	const request = "帮我做PPT"
	triggerOnly := SkillReg{Name: "unnamed-thing", Triggers: []string{"做PPT"}}
	withTag := SkillReg{Name: "unnamed-thing", Triggers: []string{"做PPT"}, Tags: []string{"ppt"}}
	withPriority := SkillReg{Name: "unnamed-thing", Triggers: []string{"做PPT"}, Tags: []string{"ppt"}, Priority: 30}
	withName := SkillReg{Name: "ppt-maker", Triggers: []string{"做PPT"}, Tags: []string{"ppt"}, Priority: 30}

	base := localSkillScore(request, triggerOnly)
	tag := localSkillScore(request, withTag)
	priority := localSkillScore(request, withPriority)

	if base <= 0 {
		t.Fatalf("命中 trigger 应得正分，实际=%d", base)
	}
	if tag <= base {
		t.Fatalf("tag 命中未累加：trigger=%d -> tag=%d", base, tag)
	}
	if priority <= tag {
		t.Fatalf("priority 未累加：tag=%d -> priority=%d", tag, priority)
	}
	// A name that literally appears in the request must outscore one that does not.
	literal := localSkillScore("帮我用 ppt-maker 做PPT", withName)
	nonLiteral := localSkillScore(request, withName)
	if literal <= nonLiteral {
		t.Fatalf("请求中出现技能名时应额外加分：字面=%d 非字面=%d", literal, nonLiteral)
	}
}

func TestLocalSkillScoreFairyEngineeringHardcodedBoost(t *testing.T) {
	got := localSkillScore("请修复这个前端代码的回归测试", SkillReg{Name: "fairy-engineering"})
	if got < 1000 {
		t.Fatalf("实现类请求应给 fairy-engineering 至少 1000 分，实际=%d", got)
	}
	plain := localSkillScore("请修复这个前端代码的回归测试", SkillReg{Name: "other-skill"})
	if plain >= 1000 {
		t.Fatalf("硬编码加成不应波及其他技能，实际=%d", plain)
	}
}

func TestLocalSkillScoreEmptyRequest(t *testing.T) {
	if got := localSkillScore("   ", SkillReg{Name: "x"}); got != 0 {
		t.Fatalf("空请求应得 0 分，实际=%d", got)
	}
}

// ---------------------------------------------------------------------------
// recallSkillCandidates：过滤、排序、截断
// ---------------------------------------------------------------------------

func TestRecallSkillCandidatesDropsZeroScore(t *testing.T) {
	registry := []SkillReg{
		{Name: "match", Triggers: []string{"做PPT"}},
		{Name: "nope", Description: "完全无关"},
	}
	got := recallSkillCandidates("帮我做PPT", registry, 0)
	if len(got) != 1 || got[0].Reg.Name != "match" {
		t.Fatalf("0 分候选应被丢弃，实际=%v", names(got))
	}
}

func TestRecallSkillCandidatesSortsByScoreThenPriority(t *testing.T) {
	registry := []SkillReg{
		{Name: "low", Triggers: []string{"做PPT"}, Priority: 0},
		{Name: "high", Triggers: []string{"做PPT"}, Priority: 90},
	}
	got := recallSkillCandidates("帮我做PPT", registry, 0)
	if len(got) != 2 {
		t.Fatalf("期望 2 个候选，实际=%d", len(got))
	}
	if got[0].Reg.Name != "high" {
		t.Fatalf("同分时应按 Priority 降序，期望 high 在前，实际=%v", names(got))
	}
}

func TestRecallSkillCandidatesRespectsLimit(t *testing.T) {
	var registry []SkillReg
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		registry = append(registry, SkillReg{Name: n, Triggers: []string{"做PPT"}})
	}
	if got := recallSkillCandidates("帮我做PPT", registry, 3); len(got) != 3 {
		t.Fatalf("limit=3 未生效，实际=%d", len(got))
	}
	if got := recallSkillCandidates("帮我做PPT", registry, 0); len(got) != 5 {
		t.Fatalf("limit=0 应表示不截断，实际=%d", len(got))
	}
}

func TestRecallSkillCandidatesInitialisesJEVScoreToMinusOne(t *testing.T) {
	got := recallSkillCandidates("做PPT", []SkillReg{{Name: "a", Triggers: []string{"做PPT"}}}, 0)
	if len(got) != 1 || got[0].JEVScore != -1 {
		t.Fatalf("JEVScore 应初始化为 -1 表示未打分，实际=%v", got)
	}
}

// ---------------------------------------------------------------------------
// 强制技能
// ---------------------------------------------------------------------------

func TestForcedSkillNamesForcesEngineeringOnImplementationTask(t *testing.T) {
	if !forcedSkillNames("请实现一个新功能", nil)["fairy-engineering"] {
		t.Fatal("实现类请求应强制 fairy-engineering")
	}
	if forcedSkillNames("今天天气怎么样", nil)["fairy-engineering"] {
		t.Fatal("非实现类请求不应强制 fairy-engineering")
	}
	if !forcedSkillNames("随便聊聊", []string{" Custom-Skill "})["custom-skill"] {
		t.Fatal("配置的强制技能应被小写化收录")
	}
}

func TestEnsureForcedSkillCandidatesPromotesAndAppends(t *testing.T) {
	registry := []SkillReg{
		{Name: "ppt-maker", Triggers: []string{"做PPT"}},
		{Name: "fairy-engineering", Description: "工程改动"},
	}
	candidates := recallSkillCandidates("做PPT", registry, 0) // 只召回 ppt-maker
	forced := forcedSkillNames("请实现一个新功能", nil)

	got := ensureForcedSkillCandidates(candidates, registry, forced)
	if len(got) != 2 {
		t.Fatalf("强制技能应被补进候选池，实际=%d 个：%v", len(got), names(got))
	}
	if got[0].Reg.Name != "fairy-engineering" || !got[0].Forced {
		t.Fatalf("fairy-engineering 应以 Forced 身份排在最前，实际=%v", names(got))
	}
	if got[0].LocalScore < 1000 {
		t.Fatalf("强制技能 LocalScore 应抬到 1000 以上，实际=%d", got[0].LocalScore)
	}
}

func TestEnsureForcedSkillCandidatesNoopWithoutForcedNames(t *testing.T) {
	candidates := []skillRouteCandidate{{Reg: SkillReg{Name: "a"}, LocalScore: 10}}
	got := ensureForcedSkillCandidates(candidates, nil, nil)
	if len(got) != 1 || got[0].Forced {
		t.Fatalf("无强制技能时应原样返回，实际=%v", got)
	}
}

// ---------------------------------------------------------------------------
// shouldRerankSkills：远程调用的成本闸门
// ---------------------------------------------------------------------------

func TestShouldRerankSkillsGatesRemoteCall(t *testing.T) {
	forced := map[string]bool{"a": true}
	twoNonForced := []skillRouteCandidate{
		{Reg: SkillReg{Name: "a"}, Forced: true},
		{Reg: SkillReg{Name: "b"}},
		{Reg: SkillReg{Name: "c"}},
	}
	oneNonForced := []skillRouteCandidate{
		{Reg: SkillReg{Name: "a"}, Forced: true},
		{Reg: SkillReg{Name: "b"}},
	}
	onlyForced := []skillRouteCandidate{
		{Reg: SkillReg{Name: "a"}, Forced: true},
		{Reg: SkillReg{Name: "c"}},
	}
	if !shouldRerankSkills(twoNonForced, forced) {
		t.Fatal("2 个非强制候选应触发 JEV 重排")
	}
	if shouldRerankSkills(oneNonForced, forced) {
		t.Fatal("1 个非强制候选不应触发远程调用")
	}
	if shouldRerankSkills(onlyForced, forced) {
		t.Fatal("全是强制技能时不应触发远程调用")
	}
}

// ---------------------------------------------------------------------------
// selectSkillCandidates：排序、阈值、注入上限
// ---------------------------------------------------------------------------

func TestSelectSkillCandidatesOrdersForcedThenJEVThenLocal(t *testing.T) {
	candidates := []skillRouteCandidate{
		{Reg: SkillReg{Name: "low-jev-high-local"}, LocalScore: 999, JEVScore: 0.2},
		{Reg: SkillReg{Name: "high-jev-low-local"}, LocalScore: 1, JEVScore: 0.9},
		{Reg: SkillReg{Name: "forced-lowest"}, LocalScore: 0, JEVScore: -1, Forced: true},
	}
	cfg := SkillRoutingConfig{InjectLimit: 3, Threshold: 0.55}
	got := selectSkillCandidates(candidates, cfg)
	if len(got) != 2 {
		t.Fatalf("阈值应滤掉 0.2 的候选，实际保留=%v", names(got))
	}
	if got[0].Reg.Name != "forced-lowest" {
		t.Fatalf("强制技能应排最前，实际=%v", names(got))
	}
	if got[1].Reg.Name != "high-jev-low-local" {
		t.Fatalf("非强制应按 JEV 降序（而非 LocalScore），实际=%v", names(got))
	}
}

func TestSelectSkillCandidatesDropsWeakCandidatesWithoutJEV(t *testing.T) {
	cfg := SkillRoutingConfig{InjectLimit: 3, Threshold: 0.55}
	got := selectSkillCandidates([]skillRouteCandidate{
		{Reg: SkillReg{Name: "weak"}, LocalScore: 7, JEVScore: -1},
		{Reg: SkillReg{Name: "strong"}, LocalScore: 8, JEVScore: -1},
	}, cfg)
	if len(got) != 1 || got[0].Reg.Name != "strong" {
		t.Fatalf("无 JEV 时应按 LocalScore>=8 过滤，实际=%v", names(got))
	}
}

func TestSelectSkillCandidatesAlwaysKeepsForced(t *testing.T) {
	cfg := SkillRoutingConfig{InjectLimit: 3, Threshold: 0.99}
	got := selectSkillCandidates([]skillRouteCandidate{
		{Reg: SkillReg{Name: "forced"}, LocalScore: 0, JEVScore: -1, Forced: true},
	}, cfg)
	if len(got) != 1 {
		t.Fatalf("强制技能不受阈值影响，实际=%v", names(got))
	}
}

func TestSelectSkillCandidatesRespectsInjectLimit(t *testing.T) {
	var candidates []skillRouteCandidate
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		candidates = append(candidates, skillRouteCandidate{Reg: SkillReg{Name: n}, LocalScore: 20, JEVScore: 0.9})
	}
	got := selectSkillCandidates(candidates, SkillRoutingConfig{InjectLimit: 2, Threshold: 0.55})
	if len(got) != 2 {
		t.Fatalf("InjectLimit=2 未生效，实际=%d", len(got))
	}
}

// ---------------------------------------------------------------------------
// classifierUseScore
// ---------------------------------------------------------------------------

func TestClassifierUseScoreFallbackChain(t *testing.T) {
	if got := classifierUseScore(classifierScoredResult{Scores: map[string]float64{skillUseLabel: 0.8}}); got != 0.8 {
		t.Fatalf("精确 label 命中失败，实际=%v", got)
	}
	if got := classifierUseScore(classifierScoredResult{Scores: map[string]float64{"应该使用这个技能": 0.7}}); got != 0.7 {
		t.Fatalf("label 前缀兜底失败，实际=%v", got)
	}
	if got := classifierUseScore(classifierScoredResult{Label: "应该使用", Confidence: 0.6}); got != 0.6 {
		t.Fatalf("Label 字段兜底失败，实际=%v", got)
	}
	if got := classifierUseScore(classifierScoredResult{Scores: map[string]float64{skillSkipLabel: 0.1}}); got != 0 {
		t.Fatalf("应使用 0 作为最终兜底，实际=%v", got)
	}
}

// ---------------------------------------------------------------------------
// 渲染与截断
// ---------------------------------------------------------------------------

func TestRenderSkillRoutingContextEmitsFullAndCardModes(t *testing.T) {
	got := renderSkillRoutingContext([]skillRouteCandidate{
		{Reg: SkillReg{Name: "full-one", Description: "描述", Location: "/skills/a"}, FullBody: true, Body: "正文内容"},
		{Reg: SkillReg{Name: "card-only", Description: "只有描述", Location: "/skills/b"}, JEVScore: -1},
	})
	if !strings.Contains(got, `mode="full"`) {
		t.Fatalf("缺 full 模式标记：\n%s", got)
	}
	if !strings.Contains(got, `mode="card"`) {
		t.Fatalf("缺 card 模式标记：\n%s", got)
	}
	if !strings.Contains(got, "正文内容") || !strings.Contains(got, "只有描述") {
		t.Fatalf("正文或描述未渲染：\n%s", got)
	}
	if !strings.HasPrefix(got, "<dynamic_skill_context>") || !strings.HasSuffix(got, "</dynamic_skill_context>") {
		t.Fatalf("包裹标签不完整：\n%s", got)
	}
}

func TestRenderSkillRoutingContextEmptyIsEmpty(t *testing.T) {
	if got := renderSkillRoutingContext(nil); got != "" {
		t.Fatalf("空候选应渲染为空串，实际=%q", got)
	}
}

func TestTruncateSkillRunesNeverExceedsLimit(t *testing.T) {
	input := strings.Repeat("技", 100)
	got := truncateSkillRunes(input, 10)
	runes := []rune(got)
	if len(runes) > 11 {
		t.Fatalf("截断后长度 %d 超过 limit+1", len(runes))
	}
	if !strings.HasPrefix(got, strings.Repeat("技", 10)) {
		t.Fatalf("截断应保留前 10 个 rune，实际=%q", got)
	}
	if short := truncateSkillRunes("短", 10); short != "短" {
		t.Fatalf("未超限应原样返回，实际=%q", short)
	}
}

func TestTruncateSkillRunesDoesNotSplitMultiByte(t *testing.T) {
	input := strings.Repeat("🎯", 50)
	got := []rune(truncateSkillRunes(input, 5))
	if len(got) > 6 {
		t.Fatalf("emoji 被按字节切断，长度=%d", len(got))
	}
	for _, r := range got {
		if r == '\uFFFD' {
			t.Fatalf("出现替换字符，说明切在多字节中间：%q", string(got))
		}
	}
}

func TestStripSkillFrontmatterVariants(t *testing.T) {
	withFM := stripSkillFrontmatter("---\nname: a\ndescription: b\n---\n正文")
	if strings.Contains(withFM, "name: a") || strings.TrimSpace(withFM) != "正文" {
		t.Fatalf("frontmatter 未被剥离：%q", withFM)
	}
	crlf := stripSkillFrontmatter("---\r\nname: a\r\n---\r\n正文")
	if strings.TrimSpace(crlf) != "正文" {
		t.Fatalf("CRLF frontmatter 未被剥离：%q", crlf)
	}
	none := stripSkillFrontmatter("直接就是正文")
	if none != "直接就是正文" {
		t.Fatalf("无 frontmatter 时应原样返回：%q", none)
	}
	unclosed := stripSkillFrontmatter("---\nname: a\n正文")
	if strings.TrimSpace(unclosed) == "" {
		t.Fatalf("未闭合 frontmatter 不应吞掉全部内容：%q", unclosed)
	}
}

// ---------------------------------------------------------------------------
// 缓存键：注册表或配置变化必须让缓存失效
// ---------------------------------------------------------------------------

func TestSkillRouteCacheKeyIsSensitiveToRegistryAndConfig(t *testing.T) {
	base := &Config{RepoRoot: "D:/Fairy"}
	base.SkillRouting.ApplyDefaults()

	reqA := skillRouteCacheKey(base, "做PPT")
	reqB := skillRouteCacheKey(base, "做视频")
	if reqA == reqB {
		t.Fatal("不同请求不应共用缓存键")
	}

	other := &Config{RepoRoot: "D:/Fairy"}
	other.SkillRouting = base.SkillRouting
	other.SkillRouting.InjectLimit = 1
	if skillRouteCacheKey(other, "做PPT") == reqA {
		t.Fatal("配置变化必须让缓存键失效，否则调了 InjectLimit 也不生效")
	}
}

// ---------------------------------------------------------------------------

func names(candidates []skillRouteCandidate) []string {
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, c.Reg.Name)
	}
	return out
}
