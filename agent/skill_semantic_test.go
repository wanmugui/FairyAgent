package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// The local embedding service is optional infrastructure. When it is down the
// turn must still route, on the lexical score alone — routing is an
// optimisation and must never be able to block a request.
func TestApplySemanticRerankSurvivesUnreachableService(t *testing.T) {
	t.Setenv("FAIRY_EMBED_ENDPOINT", "http://127.0.0.1:1/score")
	candidates := []skillRouteCandidate{
		{Reg: SkillReg{Name: "ppt-maker", Triggers: []string{"PPT"}}, LocalScore: 10},
	}
	applySemanticRerank(context.Background(), "做一份PPT", candidates, nil)
	if candidates[0].LocalScore != 10 {
		t.Fatalf("服务不可用时分数不应改变，实际=%d", candidates[0].LocalScore)
	}
}

func TestApplySemanticRerankUsesLocalScores(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"scores":[{"call_id":"video-summary","similarity":0.72}]}`))
	}))
	defer srv.Close()
	t.Setenv("FAIRY_EMBED_ENDPOINT", srv.URL)

	candidates := []skillRouteCandidate{
		{Reg: SkillReg{Name: "video-summary"}, LocalScore: 0},
		{Reg: SkillReg{Name: "unrelated"}, LocalScore: 4},
	}
	applySemanticRerank(context.Background(), "把这个视频总结一下", candidates, nil)

	if candidates[0].SemanticScore != 0.72 {
		t.Fatalf("应记录语义分，实际=%f", candidates[0].SemanticScore)
	}
	// 0 * 14 + 0.72*14 = 10.08 -> 10, which must overtake a weak lexical match.
	if candidates[0].LocalScore <= candidates[1].LocalScore {
		t.Fatalf("语义命中应超过弱词法命中：%d vs %d",
			candidates[0].LocalScore, candidates[1].LocalScore)
	}
}

// Lexical evidence must not be overturned by a paraphrase match: a skill the
// user literally named keeps its full lexical weight.
func TestBlendSemanticScoreKeepsLexicalLead(t *testing.T) {
	strong := blendSemanticScore(22, 0.30) // two trigger hits, weak semantic
	weak := blendSemanticScore(0, 0.95)    // no lexical hit, very strong semantic
	if strong <= weak {
		t.Fatalf("字面命中(22)应压过纯语义命中(%.0f)：%d vs %d", 0.95*semanticWeight, strong, weak)
	}
}

func TestBlendSemanticScoreNeverNegative(t *testing.T) {
	if got := blendSemanticScore(0, -1); got != 0 {
		t.Fatalf("got %d, want 0", got)
	}
}

func TestSkillEmbeddingTextExcludesRoutingMetadata(t *testing.T) {
	text := skillEmbeddingText(SkillReg{
		Name:        "ppt-maker",
		Description: "新建整套 PPT",
		Triggers:    []string{"做PPT"},
		Tags:        []string{"ppt"},
		Category:    "办公", // routing metadata, must not be embedded
		Priority:    90,
	})
	if strings.Contains(text, "办公") {
		t.Fatalf("分类不该进嵌入文本：%q", text)
	}
	if !strings.Contains(text, "新建整套 PPT") {
		t.Fatalf("描述应保留：%q", text)
	}
	if !strings.Contains(text, "做PPT") {
		t.Fatalf("触发词应保留：%q", text)
	}
}

func TestSkillEmbeddingTextFallsBackToName(t *testing.T) {
	if got := skillEmbeddingText(SkillReg{Name: "solo"}); got != "solo" {
		t.Fatalf("got %q", got)
	}
}

// The bge query instruction is what makes ranking correct on mixed-script
// abbreviations; dropping it silently degrades quality rather than failing.
func TestSemanticSkillScoresSendsQueryInstruction(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = readAll(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"scores":[]}`))
	}))
	defer srv.Close()
	t.Setenv("FAIRY_EMBED_ENDPOINT", srv.URL)

	semanticSkillScores(context.Background(), "做PPT", []string{"ppt-maker"}, []string{"PPT"})

	if !strings.Contains(string(body), embedQueryInstruction) {
		t.Fatalf("query 指令前缀必须发送，实际=%s", body)
	}
}

func readAll(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	buf := make([]byte, 0, 512)
	tmp := make([]byte, 512)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			return buf, nil
		}
	}
}

func TestSkillEmbedEndpointHonoursOverride(t *testing.T) {
	os.Unsetenv("FAIRY_EMBED_ENDPOINT")
	if got := skillEmbedEndpoint(); got != defaultEmbeddingEndpoint {
		t.Fatalf("默认应指向本地服务 %q，实际=%q", defaultEmbeddingEndpoint, got)
	}
	t.Setenv("FAIRY_EMBED_ENDPOINT", "http://example.test/score")
	if got := skillEmbedEndpoint(); got != "http://example.test/score" {
		t.Fatalf("got %q", got)
	}
}
