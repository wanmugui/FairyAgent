package main

// Local semantic scoring for skill routing.
//
// This replaces a remote JEV classifier that used to rank skill candidates.
// That endpoint had been unreachable in practice — the same fail_open failure
// mode already documented in tool_score_cleanup.go — so every turn paid a
// network timeout and then kept all the candidates anyway.
//
// The scores now come from the local embedding service that tool_score_cleanup
// already runs (tools/stale_tiebreak_service.py, bge-small-zh-v1.5). Reusing
// it rather than standing up a second model server is deliberate: the agent is
// spawned per request, loading the model costs ~2.1s, and that service keeps it
// warm.
//
// Why this is a *blend* and not a replacement for the lexical score:
//
// Measured against real skill descriptions with this model, pure cosine
// similarity gets a lot right — "帮我逆向一下这个apk" against an APK-reverse
// skill scores 0.57 against 0.27 for the alternatives — and catches synonym
// rewrites the lexical score cannot see ("把这个视频总结一下" → video-summary,
// 0.72).
//
// But it is weak exactly where skill triggers are strongest: mixed-script
// abbreviations. For "帮我做一份AI趋势PPT" it ranked an unrelated reverse
// engineering skill (0.383) *above* the right one (0.323). Those are not
// paraphrases, they are literal keyword hits — and the lexical score already
// nails them via the skill's own triggers field. So lexical evidence leads and
// semantic evidence catches what lexical misses.

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"strings"
	"time"
)

// bge retrieval models are trained with an asymmetric query instruction. Adding
// it to the query is what turns the ranking above from wrong to right, and it
// must be applied to the query only — never to the passages being indexed.
const embedQueryInstruction = "为这个句子生成表示以用于检索相关文章："

// defaultStaleTiebreakEndpoint is the local embedding service that ships with
// the repo. It is the same one tool cleanup scores against, so the two features
// share a single warm model instead of doubling the memory footprint.
const defaultEmbeddingEndpoint = "http://127.0.0.1:8137/score"

// semanticWeight scales the cosine score (roughly 0..1) into the same integer
// range as lexicalSkillScore. Chosen so a decisive semantic match can outrank a
// single lexical trigger hit, but two lexical hits still dominate — literal
// keyword matches should not be overturned by a plausible-looking paraphrase.
const semanticWeight = 14

// semanticTimeout is deliberately short. The service is local and answers in
// milliseconds; if it is not answering this fast it is not going to, and every
// turn pays the wait. Timing out early keeps routing responsive and falls back
// to the lexical score, which is always available.
const semanticTimeout = 2 * time.Second

// skillEmbedEndpoint resolves the endpoint, allowing an override for tests and
// for deployments that run the service on another port.
func skillEmbedEndpoint() string {
	if custom := strings.TrimSpace(os.Getenv("FAIRY_EMBED_ENDPOINT")); custom != "" {
		return custom
	}
	return defaultEmbeddingEndpoint
}

// embedScoreRequest is the wire shape the local service already speaks: a batch
// of texts plus one anchor to compare them against. It was built for tool
// cleanup ("is this result still relevant right now"), which is the same
// question here ("is this skill relevant to this request"), so no service
// change is needed.
type embedScoreRequest struct {
	Items []embedScoreItem `json:"items"`
	Now   string           `json:"now_context"`
}

type embedScoreItem struct {
	CallID string `json:"call_id"`
	Head   string `json:"head"`
}

type embedScoreResponse struct {
	Scores []embedScoreResult `json:"scores"`
}

type embedScoreResult struct {
	CallID     string  `json:"call_id"`
	Similarity float64 `json:"similarity"`
}

// semanticSkillScores returns a semantic score per skill name, or nil when the
// service is unavailable.
//
// Returning nil rather than an error is deliberate: the caller blends into a
// lexical score that always exists, so a down service must degrade to "purely
// lexical" rather than fail the turn. Routing is an optimisation; it must never
// be able to block a request.
func semanticSkillScores(ctx context.Context, request string, names, texts []string) map[string]float64 {
	if len(names) == 0 || len(names) != len(texts) {
		return nil
	}
	items := make([]embedScoreItem, 0, len(names))
	for i := range names {
		items = append(items, embedScoreItem{CallID: names[i], Head: texts[i]})
	}
	body, err := json.Marshal(embedScoreRequest{Items: items, Now: embedQueryInstruction + request})
	if err != nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, semanticTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, skillEmbedEndpoint(), bytes.NewReader(body))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var decoded embedScoreResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil
	}

	scores := make(map[string]float64, len(decoded.Scores))
	for _, item := range decoded.Scores {
		if math.IsNaN(item.Similarity) || math.IsInf(item.Similarity, 0) {
			continue
		}
		scores[item.CallID] = item.Similarity
	}
	if len(scores) == 0 {
		return nil
	}
	return scores
}

// blendSemanticScore folds a semantic score into the lexical total.
//
// Lexical evidence is kept whole rather than scaled down: a skill the user
// named or whose trigger literally appears in the request must not lose to a
// paraphrase match. The semantic term only has to be good enough to break ties
// and to lift skills the lexical pass scored zero.
func blendSemanticScore(lexical int, semantic float64) int {
	blended := float64(lexical) + semantic*semanticWeight
	if blended < 0 {
		return 0
	}
	return int(math.Round(blended))
}

// applySemanticRerank adds semantic evidence to the recalled candidates.
//
// Ordering is deliberately stable across calls: the final sort in
// selectSkillCandidates falls back to Priority and then name, so an embedding
// service that hiccups can change the ranking but never the candidate set.
func applySemanticRerank(ctx context.Context, request string, candidates []skillRouteCandidate, registry []SkillReg) {
	if len(candidates) == 0 {
		return
	}
	byName := make(map[string]SkillReg, len(registry))
	for _, reg := range registry {
		byName[reg.Name] = reg
	}
	names := make([]string, 0, len(candidates))
	texts := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		reg := candidate.Reg
		if byReg, ok := byName[reg.Name]; ok {
			reg = byReg
		}
		names = append(names, reg.Name)
		texts = append(texts, skillEmbeddingText(reg))
	}

	scores := semanticSkillScores(ctx, request, names, texts)
	if scores == nil {
		return
	}
	for i := range candidates {
		similarity, ok := scores[candidates[i].Reg.Name]
		if !ok {
			continue
		}
		candidates[i].LocalScore = blendSemanticScore(candidates[i].LocalScore, similarity)
		candidates[i].SemanticScore = similarity
	}
}

// skillEmbeddingText is what actually gets embedded: the parts a user would
// describe the skill with. Category and priority are routing metadata nobody
// phrases a request with, so including them only blurs the match.
func skillEmbeddingText(reg SkillReg) string {
	parts := make([]string, 0, 4)
	if strings.TrimSpace(reg.Name) != "" {
		parts = append(parts, strings.ReplaceAll(reg.Name, "-", " "))
	}
	if strings.TrimSpace(reg.Description) != "" {
		parts = append(parts, reg.Description)
	}
	if len(reg.Triggers) > 0 {
		parts = append(parts, strings.Join(reg.Triggers, " "))
	}
	if len(reg.Tags) > 0 {
		parts = append(parts, strings.Join(reg.Tags, " "))
	}
	if len(parts) == 0 {
		return reg.Name
	}
	return strings.Join(parts, " ")
}
