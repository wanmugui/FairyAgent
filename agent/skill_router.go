package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	JEVScore   float64
	Forced     bool
	FullBody   bool
	Body       string
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
	if c.Threshold <= 0 {
		c.Threshold = 0.55
	}
	if strings.TrimSpace(c.Endpoint) == "" {
		c.Endpoint = defaultScoreEndpoint
	}
	if strings.TrimSpace(c.Tier) == "" {
		c.Tier = defaultScoreTier
	}
	if c.TimeoutSec <= 0 {
		c.TimeoutSec = 30
	}
	if c.FailOpen == nil {
		open := true
		c.FailOpen = &open
	}
	if c.CacheTTLSec <= 0 {
		c.CacheTTLSec = 600
	}
	if c.MaxBodyRunes <= 0 {
		c.MaxBodyRunes = 12000
	}
}

// BuildSkillRoutingContext returns the per-turn dynamic skill block. It is
// deliberately best-effort: local recall always runs, JEV only reranks the
// small candidate set, and a classifier failure falls back to local results.
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
	contextText := routeSkills(cfg, request)
	setCachedSkillRoute(cacheKey, contextText, cfg.SkillRouting.CacheTTLSec)
	return contextText
}

func routeSkills(cfg *Config, request string) string {
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

	// JEV is a reranker, not a recall engine. Avoid a remote call when the
	// local pass already produced only deterministic forced skills.
	if shouldRerankSkills(candidates, forcedNames) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.SkillRouting.TimeoutSec)*time.Second)
		scores, err := rerankSkillsWithJEV(ctx, request, candidates, cfg)
		cancel()
		if err == nil && len(scores) == len(candidates) {
			for i := range candidates {
				candidates[i].JEVScore = scores[i]
			}
		} else if cfg.SkillRouting.FailOpen == nil || *cfg.SkillRouting.FailOpen {
			fmt.Fprintf(os.Stderr, "[skill-router] JEV rerank fallback: %v\n", err)
		} else {
			candidates = forcedCandidatesOnly(candidates)
		}
	}

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
		candidates = append(candidates, skillRouteCandidate{Reg: reg, LocalScore: score, JEVScore: -1})
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
		candidates = append(candidates, skillRouteCandidate{Reg: reg, LocalScore: 1000, JEVScore: 1, Forced: true})
		seen[key] = true
	}
	for i := range candidates {
		if forced[strings.ToLower(candidates[i].Reg.Name)] {
			candidates[i].Forced = true
			candidates[i].JEVScore = 1
			if candidates[i].LocalScore < 1000 {
				candidates[i].LocalScore = 1000
			}
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].LocalScore > candidates[j].LocalScore })
	return candidates
}

func shouldRerankSkills(candidates []skillRouteCandidate, forced map[string]bool) bool {
	nonForced := 0
	for _, candidate := range candidates {
		if !forced[strings.ToLower(candidate.Reg.Name)] {
			nonForced++
		}
	}
	return nonForced > 1
}

func rerankSkillsWithJEV(ctx context.Context, request string, candidates []skillRouteCandidate, cfg *Config) ([]float64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	inputs := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Forced {
			continue
		}
		inputs = append(inputs, fmt.Sprintf(
			"用户请求:\n%s\n\n候选 Skill:\n名称: %s\n描述: %s\n标签: %s\n触发词: %s\n位置: %s\n正文片段: %s",
			request,
			candidate.Reg.Name,
			candidate.Reg.Description,
			strings.Join(candidate.Reg.Tags, ", "),
			strings.Join(candidate.Reg.Triggers, ", "),
			candidate.Reg.Location,
			truncateSkillRunes(readSkillBody(cfg, candidate.Reg), 500),
		))
	}
	if len(inputs) == 0 {
		return nil, nil
	}
	payload := map[string]any{
		"inputs": inputs,
		"labels": []string{skillUseLabel, skillSkipLabel},
		"tier":   cfg.SkillRouting.Tier,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.SkillRouting.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: time.Duration(cfg.SkillRouting.TimeoutSec) * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("classifier http %d: %s", resp.StatusCode, truncateSkillRunes(string(raw), 200))
	}
	var parsed classifierScoreResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	if len(parsed.Results) != len(inputs) {
		return nil, fmt.Errorf("classifier returned %d results for %d inputs", len(parsed.Results), len(inputs))
	}
	out := make([]float64, 0, len(candidates))
	inputIndex := 0
	for _, candidate := range candidates {
		if candidate.Forced {
			out = append(out, 1)
			continue
		}
		if inputIndex >= len(parsed.Results) {
			return nil, fmt.Errorf("classifier result index out of range")
		}
		result := parsed.Results[inputIndex]
		inputIndex++
		score := classifierUseScore(result)
		out = append(out, score)
	}
	return out, nil
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
		if candidates[i].JEVScore != candidates[j].JEVScore {
			return candidates[i].JEVScore > candidates[j].JEVScore
		}
		return candidates[i].LocalScore > candidates[j].LocalScore
	})
	selected := make([]skillRouteCandidate, 0, cfg.InjectLimit)
	for _, candidate := range candidates {
		if len(selected) >= cfg.InjectLimit {
			break
		}
		if !candidate.Forced {
			if candidate.JEVScore >= 0 && candidate.JEVScore < cfg.Threshold {
				continue
			}
			if candidate.JEVScore < 0 && candidate.LocalScore < 8 {
				continue
			}
		}
		selected = append(selected, candidate)
	}
	return selected
}

func forcedCandidatesOnly(candidates []skillRouteCandidate) []skillRouteCandidate {
	out := make([]skillRouteCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Forced {
			out = append(out, candidate)
		}
	}
	return out
}

func renderSkillRoutingContext(selected []skillRouteCandidate) string {
	if len(selected) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<dynamic_skill_context>\n")
	b.WriteString("以下 Skill 已按当前请求动态选择。full=\"true\" 的内容必须遵守；card-only 的 Skill 只有相关时再用 read_file 读取其 SKILL.md。\n")
	for _, candidate := range selected {
		score := candidate.JEVScore
		if score < 0 {
			score = 0
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
