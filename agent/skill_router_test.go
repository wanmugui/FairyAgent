package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRouterSkill(t *testing.T, root, name, frontmatter, body string) {
	t.Helper()
	dir := filepath.Join(root, "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\n" + frontmatter + "---\n\n# " + name + "\n\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func routerTestConfig(root string) *Config {
	return &Config{
		RepoRoot:  root,
		SkillsDir: "skills",
		SkillRouting: SkillRoutingConfig{
			Enabled:       true,
			RecallLimit:   10,
			InjectLimit:   3,
			FullBodyLimit: 2,
			Threshold:     0.5,
			Endpoint:      "http://127.0.0.1:1",
			Tier:          "fast",
			TimeoutSec:    1,
			CacheTTLSec:   1,
			MaxBodyRunes:  2000,
		},
	}
}

func TestBuildSkillRoutingContextForcesEngineeringForImplementationRequest(t *testing.T) {
	root := t.TempDir()
	writeRouterSkill(t, root, "fairy-engineering", "tags:\n  - code\n  - frontend\n", "REGRESSION_AND_CLICK_RULES")
	cfg := routerTestConfig(root)
	context := BuildSkillRoutingContext(cfg, "修改前端代码并做点击测试")
	if !strings.Contains(context, "fairy-engineering") {
		t.Fatalf("expected fairy-engineering in context: %s", context)
	}
	if !strings.Contains(context, "REGRESSION_AND_CLICK_RULES") {
		t.Fatalf("expected full engineering body in context: %s", context)
	}
}

func TestSkillRouterUsesJEVRerank(t *testing.T) {
	root := t.TempDir()
	writeRouterSkill(t, root, "alpha-skill", "tags:\n  - alpha\n", "ALPHA_BODY")
	writeRouterSkill(t, root, "beta-skill", "tags:\n  - beta\n", "BETA_BODY")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Inputs []string `json:"inputs"`
			Labels []string `json:"labels"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		results := make([]map[string]any, 0, len(payload.Inputs))
		for _, input := range payload.Inputs {
			score := 0.1
			if strings.Contains(input, "beta-skill") {
				score = 0.95
			}
			results = append(results, map[string]any{
				"label":      "应该使用该 Skill",
				"confidence": score,
				"scores": map[string]float64{
					"应该使用该 Skill":  score,
					"不应该使用该 Skill": 1 - score,
				},
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	}))
	defer server.Close()

	cfg := routerTestConfig(root)
	cfg.SkillRouting.Endpoint = server.URL
	cfg.SkillRouting.InjectLimit = 1
	context := BuildSkillRoutingContext(cfg, "请处理 beta-skill 的专门任务")
	if !strings.Contains(context, "beta-skill") || !strings.Contains(context, "BETA_BODY") {
		t.Fatalf("expected beta skill selected: %s", context)
	}
	if strings.Contains(context, "ALPHA_BODY") {
		t.Fatalf("alpha should be reranked out: %s", context)
	}
}

func TestSkillRouterFailOpenFallsBackToLocalRecall(t *testing.T) {
	root := t.TempDir()
	writeRouterSkill(t, root, "alpha-skill", "tags:\n  - alpha\n", "ALPHA_BODY")
	writeRouterSkill(t, root, "beta-skill", "tags:\n  - beta\n", "BETA_BODY")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "classifier down", http.StatusInternalServerError)
	}))
	defer server.Close()

	cfg := routerTestConfig(root)
	cfg.SkillRouting.Endpoint = server.URL
	cfg.SkillRouting.InjectLimit = 1
	context := BuildSkillRoutingContext(cfg, "请处理 beta-skill 的专门任务")
	if !strings.Contains(context, "beta-skill") {
		t.Fatalf("expected local fallback to keep beta skill: %s", context)
	}
}
func TestSkillRouterLiveJEV(t *testing.T) {
	if os.Getenv("FAIRY_SKILL_ROUTER_LIVE") != "1" {
		t.Skip("set FAIRY_SKILL_ROUTER_LIVE=1 to call classifier.dev")
	}
	root := t.TempDir()
	writeRouterSkill(t, root, "ppt-maker", "description: 制作 PPT 演示文稿\n", "PPT_BODY")
	writeRouterSkill(t, root, "daily-news", "description: 今天的新闻和当日热点\n", "NEWS_BODY")
	cfg := routerTestConfig(root)
	cfg.SkillRouting.Endpoint = "https://classifier.dev/"
	cfg.SkillRouting.TimeoutSec = 30
	candidates := []skillRouteCandidate{
		{Reg: SkillReg{Name: "ppt-maker", Description: "制作 PPT 演示文稿", Location: "local:///skills/ppt-maker/SKILL.md"}},
		{Reg: SkillReg{Name: "daily-news", Description: "今天的新闻和当日热点", Location: "local:///skills/daily-news/SKILL.md"}},
	}
	scores, err := rerankSkillsWithJEV(context.Background(), "请制作一套 PPT 演示文稿", candidates, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(scores) != 2 {
		t.Fatalf("expected 2 live JEV scores, got %#v", scores)
	}
	if scores[0] < 0 || scores[1] < 0 {
		t.Fatalf("invalid live JEV scores: %#v", scores)
	}
}
func TestFairyReferenceAnalysisSkillRoutesLocally(t *testing.T) {
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
	var reference SkillReg
	for _, skill := range registry {
		if skill.Name == "fairy-reference-analysis" {
			reference = skill
			break
		}
	}
	if reference.Name == "" {
		t.Fatal("fairy-reference-analysis not found in registry")
	}
	if len(reference.Tags) == 0 || len(reference.Triggers) == 0 {
		t.Fatalf("reference skill metadata missing: %#v", reference)
	}
	if score := localSkillScore("参考这个游戏的 UI 和机制设计", reference); score <= 0 {
		t.Fatalf("reference skill did not match local recall: %d", score)
	}
}
