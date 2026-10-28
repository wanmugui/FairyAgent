package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	skillUseLabel  = "应该使用该 Skill"
	skillSkipLabel = "不应该使用该 Skill"
)

type skillRouteCandidate struct {
	Reg        SkillReg
	LocalScore int
	// SemanticScore is the cosine similarity from the local embedding service,
	// or 0 when it was unavailable. Kept alongside LocalScore (which already
	// has the semantic term folded in) so diagnostics can show what the model
	// contributed versus what the lexical pass found on its own.
	SemanticScore float64
	Forced        bool
	FullBody      bool
	Body          string
}

type skillRouteCacheEntry struct {
	context string
	expires time.Time
}

var skillRouteCache = struct {
	sync.Mutex
	entries map[string]skillRouteCacheEntry
}{entries: make(map[string]skillRouteCacheEntry)}

func (c *SkillRoutingConfig) ApplyDefaults() {
	if c.RecallLimit <= 0 {
		c.RecallLimit = 20
	}
	if c.InjectLimit <= 0 {
		c.InjectLimit = 3
	}
	if c.FullBodyLimit <= 0 {
		c.FullBodyLimit = 2
	}
	if c.CacheTTLSec <= 0 {
		c.CacheTTLSec = 600
	}
	if c.MaxBodyRunes <= 0 {
		c.MaxBodyRunes = 12000
	}
}

// BuildSkillRoutingContext returns the per-turn dynamic skill block. It is
// deliberately best-effort: the lexical recall always runs, the local embedding
// service only adds semantic evidence on top of it, and an unreachable service
// falls back to lexical-only rather than failing the turn.
func BuildSkillRoutingContext(cfg *Config, request string) string {
	if cfg == nil || !cfg.SkillRouting.Enabled {
		return ""
	}
	request = strings.TrimSpace(request)
	if request == "" {
		return ""
	}
	cfg.SkillRouting.ApplyDefaults()
	cacheKey := skillRouteCacheKey(cfg, request)
	if cached, ok := getCachedSkillRoute(cacheKey); ok {
		return cached
	}
	contextText := routeSkills(context.Background(), cfg, request)
	setCachedSkillRoute(cacheKey, contextText, cfg.SkillRouting.CacheTTLSec)
	return contextText
}

func routeSkills(ctx context.Context, cfg *Config, request string) string {
	registry := DiscoverSkillRegistriesAll(cfg)
	if len(registry) == 0 {
		return ""
	}
	candidates := recallSkillCandidates(request, registry, cfg.SkillRouting.RecallLimit)
	forcedNames := forcedSkillNames(request, cfg.SkillRouting.Always)
	candidates = ensureForcedSkillCandidates(candidates, registry, forcedNames)
	if len(candidates) == 0 {
		return ""
	}

	// Reranking is local.
	//
	// This used to hand the candidates to a remote JEV classifier. That
	// endpoint has been unreachable in practice — the same fail_open failure
	// mode already documented in tool_score_cleanup.go — so every turn paid a
	// network timeout and then kept all the candidates anyway. The local score
	// is not a weaker stand-in: it weighs the skill name, its tags, its
	// triggers and a Chinese-aware reverse match, which is exactly what the
	// classifier was being asked to approximate over the wire.
	// Semantic evidence comes from the local embedding service; the lexical
	// score already in LocalScore leads, and this only adds what lexical missed.
	// A down service is not an error — applySemanticRerank returns silently and
	// routing continues on the lexical score alone.
	applySemanticRerank(ctx, request, candidates, registry)

	selected := selectSkillCandidates(candidates, cfg.SkillRouting)
	if len(selected) == 0 {
		return ""
	}
	for i := range selected {
		if !selected[i].Forced && i >= cfg.SkillRouting.FullBodyLimit {
			continue
		}
		body := readSkillBody(cfg, selected[i].Reg)
		if body == "" {
			continue
		}
		selected[i].FullBody = true
		selected[i].Body = truncateSkillRunes(stripSkillFrontmatter(body), cfg.SkillRouting.MaxBodyRunes)
	}
	return renderSkillRoutingContext(selected)
}

func recallSkillCandidates(request string, registry []SkillReg, limit int) []skillRouteCandidate {
	candidates := make([]skillRouteCandidate, 0, len(registry))
	for _, reg := range registry {
		score := localSkillScore(request, reg)
		if score <= 0 {
			continue
		}
		candidates = append(candidates, skillRouteCandidate{Reg: reg, LocalScore: score})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].LocalScore != candidates[j].LocalScore {
			return candidates[i].LocalScore > candidates[j].LocalScore
		}
		return candidates[i].Reg.Priority > candidates[j].Reg.Priority
	})
	if limit > 0 && len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates
}

func localSkillScore(request string, reg SkillReg) int {
	req := strings.ToLower(strings.TrimSpace(request))
	name := strings.ToLower(reg.Name)
	desc := strings.ToLower(reg.Description)
	// Priority is a tiebreaker, never a base score: a high-priority skill that
	// does not match the request must not outrank one that does.
	score := 0
	if reg.Always {
		score += 100
	}
	if req == "" {
		return score
	}
	if strings.Contains(req, name) {
		score += 12
	}
	for _, tag := range reg.Tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag != "" && strings.Contains(req, tag) {
			score += 8
		}
	}
	for _, trigger := range reg.Triggers {
		trigger = strings.ToLower(strings.TrimSpace(trigger))
		if trigger != "" && strings.Contains(req, trigger) {
			score += 10
		}
	}
	for _, term := range skillQueryTerms(req) {
		if len([]rune(term)) < 2 {
			continue
		}
		if strings.Contains(name, term) {
			score += 4
		}
		score += 2 * strings.Count(desc, term)
	}
	// Reverse match: name/description keywords that appear in the request.
	// This rescues Chinese requests, where forward token matching against
	// English metadata and CJK descriptions previously scored zero.
	score += skillReverseMatchScore(req, reg)
	if score > 0 {
		score += reg.Priority / 10
	}
	if likelyImplementationTask(request) && name == "fairy-engineering" {
		score += 1000
	}
	if strings.Contains(req, "代码") || strings.Contains(req, "前端") || strings.Contains(req, "回归") || strings.Contains(req, "点击测试") {
		if name == "fairy-engineering" {
			score += 300
		}
	}
	return score
}

// skillReverseStopTerms are high-frequency CJK bigrams that carry no routing
// signal; ignoring them keeps generic phrases from matching every skill.
var skillReverseStopTerms = map[string]bool{
	"这个": true, "那个": true, "什么": true, "没有": true, "可以": true,
	"需要": true, "使用": true, "用户": true, "以及": true, "或者": true,
	"如果": true, "因为": true, "所以": true, "进行": true, "通过": true,
	"相关": true, "时候": true, "一个": true, "我们": true, "就是": true,
	"还是": true, "这些": true, "那些": true, "是否": true, "如何": true,
	"以下": true, "以上": true, "并且": true, "用于": true, "然后": true,
}

// skillReverseMatchScore matches skill metadata keywords against the request.
// It emits ASCII words plus CJK bigrams so a Chinese request can hit a Chinese
// (or English) name/description that a plain forward token match misses.
func skillReverseMatchScore(req string, reg SkillReg) int {
	score := 0
	seen := map[string]bool{}
	for _, term := range skillReverseTerms(strings.ToLower(reg.Name + "\n" + reg.Description)) {
		if seen[term] || skillReverseStopTerms[term] {
			continue
		}
		seen[term] = true
		if strings.Contains(req, term) {
			score += 4
		}
	}
	if score > 24 {
		score = 24
	}
	return score
}

// skillReverseTerms splits text into ASCII words (>=2 chars) and CJK bigrams.
func skillReverseTerms(text string) []string {
	out := make([]string, 0, 64)
	var word []rune
	var han []rune
	flushWord := func() {
		if len(word) >= 2 {
			out = append(out, string(word))
		}
		word = word[:0]
	}
	flushHan := func() {
		for i := 0; i+1 < len(han); i++ {
			out = append(out, string(han[i:i+2]))
		}
		han = han[:0]
	}
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r):
			flushWord()
			han = append(han, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushHan()
			word = append(word, r)
		default:
			flushWord()
			flushHan()
		}
	}
	flushWord()
	flushHan()
	return out
}

func skillQueryTerms(text string) []string {
	terms := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(terms))
	for _, term := range terms {
		term = strings.ToLower(strings.TrimSpace(term))
		if term != "" {
			out = append(out, term)
		}
	}
	return out
}

func forcedSkillNames(request string, configured []string) map[string]bool {
	forced := make(map[string]bool, len(configured)+1)
	for _, name := range configured {
		if name = strings.TrimSpace(name); name != "" {
			forced[strings.ToLower(name)] = true
		}
	}
	if likelyImplementationTask(request) {
		forced["fairy-engineering"] = true
	}
	return forced
}

func ensureForcedSkillCandidates(candidates []skillRouteCandidate, registry []SkillReg, forced map[string]bool) []skillRouteCandidate {
	if len(forced) == 0 {
		return candidates
	}
	seen := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		seen[strings.ToLower(candidate.Reg.Name)] = true
	}
	for _, reg := range registry {
		key := strings.ToLower(reg.Name)
		if !forced[key] || seen[key] {
			continue
		}
		candidates = append(candidates, skillRouteCandidate{Reg: reg, LocalScore: 1000, Forced: true})
		seen[key] = true
	}
	for i := range candidates {
		if forced[strings.ToLower(candidates[i].Reg.Name)] {
			candidates[i].Forced = true
			if candidates[i].LocalScore < 1000 {
				candidates[i].LocalScore = 1000
			}
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].LocalScore > candidates[j].LocalScore })
	return candidates
}

func classifierUseScore(result classifierScoredResult) float64 {
	if value, ok := result.Scores[skillUseLabel]; ok {
		return value
	}
	for label, value := range result.Scores {
		if strings.HasPrefix(label, "应该使用") {
			return value
		}
	}
	if strings.HasPrefix(result.Label, "应该使用") {
		return result.Confidence
	}
	return 0
}

func selectSkillCandidates(candidates []skillRouteCandidate, cfg SkillRoutingConfig) []skillRouteCandidate {
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Forced != candidates[j].Forced {
			return candidates[i].Forced
		}
		if candidates[i].LocalScore != candidates[j].LocalScore {
			return candidates[i].LocalScore > candidates[j].LocalScore
		}
		return candidates[i].Reg.Priority > candidates[j].Reg.Priority
	})
	selected := make([]skillRouteCandidate, 0, cfg.InjectLimit)
	for _, candidate := range candidates {
		if len(selected) >= cfg.InjectLimit {
			break
		}
		// Reranking is local now, so the relevance floor applies to the local
		// score directly. The remote classifier this replaced ranked on its
		// own use/skip verdict and its threshold (0.55) was in that scale,
		// not this one — keeping the comparison would have made every candidate
		// pass, because local scores are integers in a different unit.
		if !candidate.Forced && candidate.LocalScore < localSkillFloor {
			continue
		}
		selected = append(selected, candidate)
	}
	return selected
}

// localSkillFloor is the minimum local score a candidate needs to be injected.
//
// Calibrated against what localSkillScore actually produces: a skill whose name
// or one of its triggers appears in the request scores well above this, while a
// candidate that only matched an incidental substring of a description does not.
// Without a floor, weak matches would consume the injection budget ahead of the
// one skill that genuinely matched.
const localSkillFloor = 8

// localSkillCeiling is the local score a strong match reaches. It exists only
// to render relevance as 0..1 in the injected markup, replacing the remote
// classifier's probability. Calibrated above a literal name/trigger hit so a
// perfect match reports as 1.0.
const localSkillCeiling = 30

func renderSkillRoutingContext(selected []skillRouteCandidate) string {
	if len(selected) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<dynamic_skill_context>\n")
	b.WriteString("以下 Skill 已按当前请求动态选择。full=\"true\" 的内容必须遵守；card-only 的 Skill 只有相关时再用 read_file 读取其 SKILL.md。\n")
	for _, candidate := range selected {
		// relevance is now the local score. Normalise to 0..1 so the injected
		// markup keeps the shape the prompt template documents.
		score := float64(candidate.LocalScore) / localSkillCeiling
		if score > 1 {
			score = 1
		}
		b.WriteString(fmt.Sprintf("<skill name=%q location=%q relevance=\"%.3f\" mode=%q forced=\"%t\">\n", candidate.Reg.Name, candidate.Reg.Location, score, map[bool]string{true: "full", false: "card"}[candidate.FullBody], candidate.Forced))
		if candidate.FullBody {
			b.WriteString(candidate.Body)
			if !strings.HasSuffix(candidate.Body, "\n") {
				b.WriteByte('\n')
			}
		} else {
			b.WriteString(candidate.Reg.Description)
			b.WriteByte('\n')
		}
		b.WriteString("</skill>\n")
	}
	b.WriteString("</dynamic_skill_context>")
	return b.String()
}

func readSkillBody(cfg *Config, reg SkillReg) string {
	path := resolveSkillBodyPath(cfg, reg)
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func resolveSkillBodyPath(cfg *Config, reg SkillReg) string {
	location := strings.TrimSpace(reg.Location)
	if location == "" {
		return ""
	}
	// A registered location may be written with a leading "~" (e.g.
	// ~/.fairy/skills/gsap). filepath.IsAbs rejects that form, so it used to
	// fall through to the default branch and get joined onto RepoRoot,
	// producing "<repo>/~/.fairy/skills/gsap" — a path that does not exist,
	// silently downgrading the skill to card-only. Expand "~" up front so
	// user-level skills keep their full body.
	if location == "~" || strings.HasPrefix(location, "~/") || strings.HasPrefix(location, `~\`) {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			rest := strings.TrimPrefix(location[1:], "/")
			rest = strings.TrimPrefix(rest, `\`)
			if rest == "" {
				location = home
			} else {
				location = filepath.Join(home, filepath.FromSlash(rest))
			}
		}
	}
	var path string
	switch {
	case strings.HasPrefix(location, "local:///skills/"):
		rel := strings.TrimPrefix(location, "local:///")
		if cfg == nil {
			return ""
		}
		path = filepath.Join(cfg.RepoRoot, filepath.FromSlash(rel))
	case strings.HasPrefix(location, "/skills/"):
		if cfg == nil {
			return ""
		}
		path = filepath.Join(cfg.RepoRoot, filepath.FromSlash(strings.TrimPrefix(location, "/")))
	case strings.HasPrefix(location, "local://"):
		// Skills discovered outside SkillsRoot carry a local:// absolute
		// location, e.g. local://C:/Users/.../.agents/skills/x/SKILL.md.
		// Without this case the value is not itself absolute, so it would fall
		// through to the default branch and be joined onto RepoRoot, producing
		// a path that does not exist and silently downgrading the skill to
		// card-only.
		if cfg == nil {
			return ""
		}
		native := filepath.FromSlash(strings.TrimPrefix(location, "local://"))
		if filepath.IsAbs(native) {
			path = native
		} else {
			path = filepath.Join(cfg.RepoRoot, native)
		}
	case filepath.IsAbs(location):
		path = location
	default:
		if cfg == nil {
			return ""
		}
		path = filepath.Join(cfg.RepoRoot, filepath.FromSlash(location))
	}
	if strings.HasSuffix(strings.ToLower(path), ".md") {
		return path
	}
	return filepath.Join(path, "SKILL.md")
}

func stripSkillFrontmatter(content string) string {
	text := strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return strings.TrimSpace(text)
	}
	if end := strings.Index(text[4:], "\n---\n"); end >= 0 {
		return strings.TrimSpace(text[4+end+5:])
	}
	return strings.TrimSpace(text)
}

func truncateSkillRunes(value string, limit int) string {
	if limit <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

func skillRouteCacheKey(cfg *Config, request string) string {
	registry := DiscoverSkillRegistriesAll(cfg)
	meta, _ := json.Marshal(registry)
	h := sha256.New()
	h.Write([]byte(request))
	h.Write([]byte{0})
	h.Write(meta)
	h.Write([]byte{0})
	routingJSON, _ := json.Marshal(cfg.SkillRouting)
	h.Write(routingJSON)
	return hex.EncodeToString(h.Sum(nil))
}

func getCachedSkillRoute(key string) (string, bool) {
	skillRouteCache.Lock()
	defer skillRouteCache.Unlock()
	entry, ok := skillRouteCache.entries[key]
	if !ok || time.Now().After(entry.expires) {
		delete(skillRouteCache.entries, key)
		return "", false
	}
	return entry.context, true
}

func setCachedSkillRoute(key, contextText string, ttlSec int) {
	skillRouteCache.Lock()
	defer skillRouteCache.Unlock()
	now := time.Now()
	for existingKey, entry := range skillRouteCache.entries {
		if now.After(entry.expires) {
			delete(skillRouteCache.entries, existingKey)
		}
	}
	if len(skillRouteCache.entries) >= 256 {
		for existingKey := range skillRouteCache.entries {
			delete(skillRouteCache.entries, existingKey)
			if len(skillRouteCache.entries) < 256 {
				break
			}
		}
	}
	skillRouteCache.entries[key] = skillRouteCacheEntry{context: contextText, expires: now.Add(time.Duration(ttlSec) * time.Second)}
}
