package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func scoreCleanupConfig() *Config {
	cfg := &Config{}
	cfg.SummaryThresholdTokens = 163840
	cfg.ToolScoreCleanup = ToolScoreCleanupConfig{
		Enabled: true, MinRunes: 20, Threshold: 0.7,
	}
	cfg.ToolScoreCleanup.ApplyDefaults(cfg.SummaryThresholdTokens)
	return cfg
}

func scoreToolMsg(id, name, body string) Message {
	return NewMessage("tool", body, nil, id, name)
}

func scoreAssistantCall(id, name, args string) Message {
	return NewMessage("assistant", "", []ToolCall{{
		ID: id, Type: "function", Function: ToolCallFunc{Name: name, Arguments: args},
	}}, "", "")
}

func TestCollectToolScoreCandidatesSkipsExcludedAndTiny(t *testing.T) {
	cfg := scoreCleanupConfig()
	body := strings.Repeat("A", 500)
	msgs := []Message{
		scoreAssistantCall("c1", "read_file", `{"path":"/tmp/a.md"}`),
		scoreToolMsg("c1", "read_file", body),
		scoreAssistantCall("c2", "create_subtask", `{"task":"x"}`),
		scoreToolMsg("c2", "create_subtask", strings.Repeat("S", 500)),
		scoreAssistantCall("c3", "bash", `{"command":"ls"}`),
		scoreToolMsg("c3", "bash", "tiny"),
		scoreAssistantCall("c4", "bash", `{"command":"wc"}`),
		scoreToolMsg("c4", "bash", strings.Repeat("B", 500)),
	}
	cands := CollectToolScoreCandidates(msgs, cfg)
	if len(cands) != 2 || cands[0].CallID != "c1" || cands[1].CallID != "c4" {
		t.Fatalf("candidates = %#v, want c1 and c4 (subtask excluded, tiny skipped)", cands)
	}
	if cands[0].Runes != 500 || !strings.HasPrefix(cands[0].Args, `{"path"`) {
		t.Fatalf("candidate not populated: %#v", cands[0])
	}
}

func TestCollectToolScoreCandidatesSkipsPinnedEvidence(t *testing.T) {
	cfg := scoreCleanupConfig()
	body := strings.Repeat("A", 500)
	msgs := []Message{
		scoreAssistantCall("c1", "read_file", `{"path":"/tmp/a.md"}`),
		scoreToolMsg("c1", "read_file", body),
		scoreAssistantCall("c2", "bash", `{"command":"node --test"}`),
		scoreToolMsg("c2", "bash", body),
	}
	cands := CollectToolScoreCandidatesPinned(msgs, cfg, map[string]bool{"c2": true})
	if len(cands) != 1 || cands[0].CallID != "c1" {
		t.Fatalf("pinned tool result leaked into cleanup candidates: %#v", cands)
	}
}

func TestCollectToolScoreCandidatesNeverCleansPlanState(t *testing.T) {
	cfg := scoreCleanupConfig()
	body := strings.Repeat("A", 500)
	msgs := []Message{
		scoreAssistantCall("p1", "plan", `{"action":"audit","id":"step-1"}`),
		scoreToolMsg("p1", "plan", body),
		scoreAssistantCall("p2", "read_file", `{"path":"E:\\Fairy\\memory\\sessions\\2026-09-27\\plans\\2026-09-27.plan.json"}`),
		scoreToolMsg("p2", "read_file", body),
		scoreAssistantCall("c3", "bash", `{"command":"ls"}`),
		scoreToolMsg("c3", "bash", body),
	}
	cands := CollectToolScoreCandidates(msgs, cfg)
	if len(cands) != 1 || cands[0].CallID != "c3" {
		t.Fatalf("plan state leaked into cleanup candidates: %#v", cands)
	}
}

func TestCollectToolScoreCandidatesFeedsFollowUpContext(t *testing.T) {
	cfg := scoreCleanupConfig()
	msgs := []Message{
		scoreAssistantCall("c1", "read_file", `{"path":"/tmp/a.md"}`),
		scoreToolMsg("c1", "read_file", strings.Repeat("A", 500)),
		NewMessage("assistant", "我已经根据该文件写出 summary.md，不再需要原文。", nil, "", ""),
		scoreAssistantCall("c2", "bash", `{"command":"ls"}`),
		scoreToolMsg("c2", "bash", strings.Repeat("B", 500)),
	}
	cands := CollectToolScoreCandidates(msgs, cfg)
	if len(cands) != 2 {
		t.Fatalf("candidates = %d, want 2", len(cands))
	}
	if cands[0].CallID != "c1" || !strings.Contains(cands[0].FollowUp, "summary.md") {
		t.Fatalf("follow-up context missing: %#v", cands[0])
	}
}

func TestScoreToolCandidatesMapsClassifierScores(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		resp := map[string]any{"model": "jev-test", "results": []map[string]any{
			{"label": labelScoreClear, "confidence": 0.9, "scores": map[string]float64{labelScoreKeep: 0.1, labelScoreClear: 0.9}},
			{"label": labelScoreKeep, "confidence": 0.8, "scores": map[string]float64{labelScoreKeep: 0.8, labelScoreClear: 0.2}},
		}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	cfg := scoreCleanupConfig()
	cfg.ToolScoreCleanup.Endpoint = srv.URL
	cands := []ToolScoreCandidate{{CallID: "c1", Name: "read_file", Runes: 500}, {CallID: "c2", Name: "bash", Runes: 500}}
	scores, err := ScoreToolCandidates(cands, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(scores) != 2 || scores[0] != 0.9 || scores[1] != 0.2 {
		t.Fatalf("scores = %v, want [0.9 0.2]", scores)
	}
	if gotBody["tier"] != "fast" {
		t.Fatalf("tier not forwarded: %v", gotBody["tier"])
	}
	inputs, _ := gotBody["inputs"].([]any)
	if len(inputs) != 2 {
		t.Fatalf("inputs = %d, want 2", len(inputs))
	}
}

func TestScoreToolCandidatesSurfacesHTTPFailureForFailOpen(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	cfg := scoreCleanupConfig()
	cfg.ToolScoreCleanup.Endpoint = srv.URL
	if _, err := ScoreToolCandidates([]ToolScoreCandidate{{CallID: "c1", Name: "bash", Runes: 500}}, cfg); err == nil {
		t.Fatal("expected error so the caller can fail open")
	}
}

func TestApplyToolScoreCleanupThresholdAndPairing(t *testing.T) {
	cfg := scoreCleanupConfig()
	body := strings.Repeat("A", 500)
	msgs := []Message{
		scoreAssistantCall("c1", "read_file", `{"path":"/tmp/a.md"}`),
		scoreToolMsg("c1", "read_file", body),
		scoreAssistantCall("c2", "read_file", `{"path":"/tmp/b.md"}`),
		scoreToolMsg("c2", "read_file", body),
	}
	cands := CollectToolScoreCandidates(msgs, cfg)
	if len(cands) != 2 {
		t.Fatalf("candidates = %d, want 2", len(cands))
	}
	cleared, saved, cleaned := ApplyToolScoreCleanup(msgs, cands, []float64{0.95, 0.2}, cfg)
	if cleared != 1 || saved <= 0 || len(cleaned) != 1 {
		t.Fatalf("cleared=%d saved=%d cleaned=%v", cleared, saved, cleaned)
	}
	if !strings.HasPrefix(msgs[1].Content, defaultScoreMarker) {
		t.Fatalf("payload not replaced: %q", msgs[1].Content)
	}
	if !strings.Contains(msgs[1].Content, "path=/tmp/a.md") || !strings.Contains(msgs[1].Content, "0.95") {
		t.Fatalf("placeholder lost hints: %q", msgs[1].Content)
	}
	if msgs[1].Role != "tool" || msgs[1].ToolCallID != "c1" || msgs[1].Name != "read_file" {
		t.Fatalf("pairing fields must survive: %#v", msgs[1])
	}
	if cleaned[0].Score != 0.95 || cleaned[0].Name != "read_file" {
		t.Fatalf("trace payload wrong: %#v", cleaned[0])
	}
	msgs2 := []Message{msgs[0], scoreToolMsg("c1", "read_file", body), scoreAssistantCall("c2", "bash", `{"command":"ls"}`), scoreToolMsg("c2", "bash", body)}
	cands2 := CollectToolScoreCandidates(msgs2, cfg)
	if cleared, _, _ := ApplyToolScoreCleanup(msgs2, cands2, []float64{0.69, 0.2}, cfg); cleared != 0 {
		t.Fatalf("below-threshold candidate must stay, cleared=%d", cleared)
	}
}

func TestCollectSkipsAlreadyCleanedResults(t *testing.T) {
	cfg := scoreCleanupConfig()
	msgs := []Message{
		scoreAssistantCall("c1", "bash", `{"command":"ls"}`),
		scoreToolMsg("c1", "bash", defaultScoreMarker+" bash 共 500 字符（评分 0.91）；需要该内容时请重新调用 bash。"),
		scoreAssistantCall("c2", "bash", `{"command":"wc"}`),
		scoreToolMsg("c2", "bash", strings.Repeat("B", 500)),
	}
	if cands := CollectToolScoreCandidates(msgs, cfg); len(cands) != 1 || cands[0].CallID != "c2" {
		t.Fatalf("already-cleaned payload must be skipped and c2 retained, got %#v", cands)
	}
}

func TestCollectSkipsCustomCleanedResultsByInternalType(t *testing.T) {
	cfg := scoreCleanupConfig()
	cfg.ToolScoreCleanup.Placeholder = "custom-cleaned %[1]s %[3]d %[4]s"
	msgs := []Message{
		scoreAssistantCall("c1", "read_file", `{"path":"/tmp/a.md"}`),
		scoreToolMsg("c1", "read_file", strings.Repeat("A", 500)),
		scoreAssistantCall("c2", "bash", `{"command":"wc"}`),
		scoreToolMsg("c2", "bash", strings.Repeat("B", 500)),
	}
	cands := CollectToolScoreCandidates(msgs, cfg)
	if len(cands) != 2 {
		t.Fatalf("candidates = %d, want 2", len(cands))
	}
	if cleared, _, _ := ApplyToolScoreCleanup(msgs, cands, []float64{0.95, 0.2}, cfg); cleared != 1 {
		t.Fatalf("cleared = %d, want 1", cleared)
	}
	if msgs[1].InternalType != toolScoreCleanedInternalType {
		t.Fatalf("cleaned tool result must carry internal marker: %#v", msgs[1])
	}
	if cands := CollectToolScoreCandidates(msgs, cfg); len(cands) != 1 || cands[0].CallID != "c2" {
		t.Fatalf("custom-cleaned payload must be skipped and c2 retained, got %#v", cands)
	}
}

func TestRollingCleanupReScoresOnlySurvivorsAtLaterBoundary(t *testing.T) {
	cfg := scoreCleanupConfig()
	msgs := []Message{
		scoreAssistantCall("c1", "read_file", `{"path":"/tmp/a.md"}`),
		scoreToolMsg("c1", "read_file", strings.Repeat("A", 500)),
		scoreAssistantCall("c2", "bash", `{"command":"wc"}`),
		scoreToolMsg("c2", "bash", strings.Repeat("B", 500)),
	}
	cands := CollectToolScoreCandidates(msgs, cfg)
	if len(cands) != 2 || cands[0].CallID != "c1" || cands[1].CallID != "c2" {
		t.Fatalf("first boundary candidates = %#v, want all live results", cands)
	}
	if cleared, _, _ := ApplyToolScoreCleanup(msgs, cands, []float64{0.95, 0.2}, cfg); cleared != 1 {
		t.Fatalf("cleared = %d, want 1", cleared)
	}

	msgs = append(msgs,
		scoreAssistantCall("c3", "bash", `{"command":"cat /tmp/b"}`),
		scoreToolMsg("c3", "bash", strings.Repeat("C", 500)),
		NewMessage("assistant", "new context for c2", nil, "", ""),
	)
	cands = CollectToolScoreCandidates(msgs, cfg)
	if len(cands) != 2 || cands[0].CallID != "c2" || cands[1].CallID != "c3" {
		t.Fatalf("second boundary candidates = %#v, want surviving c2 plus new c3", cands)
	}
	if !strings.Contains(cands[0].FollowUp, "new context for c2") {
		t.Fatalf("survivor must be re-scored with the later boundary context: %q", cands[0].FollowUp)
	}
}

func TestToolScoreCleanupDisabledByDefault(t *testing.T) {
	cfg := &Config{}
	msgs := []Message{scoreToolMsg("c1", "bash", strings.Repeat("X", 500))}
	if cands := CollectToolScoreCandidates(msgs, cfg); cands != nil {
		t.Fatalf("must stay off unless enabled, got %#v", cands)
	}
	if cfg.ToolScoreCleanup.Threshold != 0 {
		t.Fatal("zero-value config must not silently enable cleanup")
	}
}

func TestToolScoreCleanupDefaultsDeriveTrigger(t *testing.T) {
	c := ToolScoreCleanupConfig{Enabled: true}
	c.ApplyDefaults(163840)
	if c.TriggerTokens != 81920 || c.Threshold != defaultScoreThreshold {
		t.Fatalf("defaults = trigger %d thr %.2f", c.TriggerTokens, c.Threshold)
	}
	if c.Tier != defaultScoreTier || c.Endpoint != defaultScoreEndpoint {
		t.Fatalf("classifier defaults wrong: %s %s", c.Tier, c.Endpoint)
	}
}

func TestToolScoreCleanupStageBoundaryTrigger(t *testing.T) {
	cfg := scoreCleanupConfig()
	if !IsStageBoundaryTool(cfg, "create_subtask") {
		t.Fatal("create_subtask must be a stage boundary tool by default")
	}
	if IsStageBoundaryTool(cfg, "read_file") {
		t.Fatal("read_file must not be a stage boundary")
	}
	if !ShouldRunToolScoreCleanup(cfg, true, 10) {
		t.Fatal("stage boundary must trigger cleanup below the token trigger")
	}
	if ShouldRunToolScoreCleanup(cfg, false, cfg.ToolScoreCleanup.TriggerTokens-1) {
		t.Fatal("must not run below the token trigger without a boundary")
	}
	if !ShouldRunToolScoreCleanup(cfg, false, cfg.ToolScoreCleanup.TriggerTokens) {
		t.Fatal("token trigger must fire at the threshold")
	}
	off := false
	cfg.ToolScoreCleanup.TriggerOnStageBoundary = &off
	if ShouldRunToolScoreCleanup(cfg, true, 10) {
		t.Fatal("boundary trigger must be disableable")
	}
	cfg.ToolScoreCleanup.Enabled = false
	if ShouldRunToolScoreCleanup(cfg, true, 1<<30) {
		t.Fatal("disabled cleanup must never run")
	}
}

func TestCollectToolScoreCandidatesUnlimitedWhenNegative(t *testing.T) {
	cfg := scoreCleanupConfig()
	cfg.ToolScoreCleanup.MaxCandidates = -1
	cfg.ToolScoreCleanup.MinRunes = 20
	body := strings.Repeat("A", 500)
	msgs := []Message{}
	for i := 0; i < 40; i++ {
		id := fmt.Sprintf("c%d", i)
		msgs = append(msgs, scoreAssistantCall(id, "read_file", `{"path":"/tmp/a.md"}`))
		msgs = append(msgs, scoreToolMsg(id, "read_file", body))
	}
	if got := len(CollectToolScoreCandidates(msgs, cfg)); got != 40 {
		t.Fatalf("negative max_candidates must not truncate, got %d", got)
	}
	cfg.ToolScoreCleanup.MaxCandidates = 0
	cfg.ToolScoreCleanup.ApplyDefaults(cfg.SummaryThresholdTokens)
	cfg.ToolScoreCleanup.MinRunes = 20
	if got := len(CollectToolScoreCandidates(msgs, cfg)); got != defaultScoreMaxCands {
		t.Fatalf("zero max_candidates must fall back to default %d, got %d", defaultScoreMaxCands, got)
	}
}

func TestApplyToolScoreCleanupByCallIDUpdatesWorkingAndTranscript(t *testing.T) {
	original := []Message{
		scoreAssistantCall("c1", "read_file", `{"path":"/tmp/a.md"}`),
		scoreToolMsg("c1", "read_file", strings.Repeat("A", 500)),
		scoreAssistantCall("c2", "bash", `{"command":"echo keep"}`),
		scoreToolMsg("c2", "bash", strings.Repeat("B", 500)),
	}
	working := append([]Message(nil), original...)
	transcript := append([]Message(nil), original...)
	replacements := []CleanedToolResult{{
		CallID: "c1", Name: "read_file", Runes: 500, Score: 0.95,
		Content: "[工具结果已按分类器评分清理] read_file 共 500 字符（评分 0.95）",
	}}

	cleared, saved, applied := ApplyToolScoreCleanupByCallID(working, replacements)
	if cleared != 1 || saved <= 0 || len(applied) != 1 {
		t.Fatalf("working cleanup = cleared %d saved %d applied %#v", cleared, saved, applied)
	}
	if !strings.HasPrefix(working[1].Content, defaultScoreMarker) || working[1].InternalType != toolScoreCleanedInternalType {
		t.Fatalf("working history was not cleaned: %#v", working[1])
	}
	if working[1].ToolCallID != "c1" || working[1].Name != "read_file" {
		t.Fatalf("tool pairing fields changed: %#v", working[1])
	}
	if working[3].Content != original[3].Content {
		t.Fatalf("unmatched result changed: %#v", working[3])
	}

	transcriptCleared, _, _ := ApplyToolScoreCleanupByCallID(transcript, replacements)
	if transcriptCleared != 1 || !strings.HasPrefix(transcript[1].Content, defaultScoreMarker) {
		t.Fatalf("transcript was not cleaned: cleared=%d message=%#v", transcriptCleared, transcript[1])
	}
}

func TestToolScoreCleanupJobDoesNotBlockAndDrainsByCallID(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseScorer := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseScorer()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		resp := map[string]any{"model": "jev-test", "results": []map[string]any{
			{"label": labelScoreClear, "confidence": 0.95, "scores": map[string]float64{labelScoreKeep: 0.05, labelScoreClear: 0.95}},
		}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	cfg := scoreCleanupConfig()
	cfg.ToolScoreCleanup.Endpoint = srv.URL
	cands := []ToolScoreCandidate{{CallID: "c1", Name: "read_file", Runes: 500, Result: strings.Repeat("A", 500)}}
	started := time.Now()
	job := startToolScoreCleanupJob(context.Background(), cands, cfg, "stage_boundary", 3)
	if elapsed := time.Since(started); elapsed > 150*time.Millisecond {
		t.Fatalf("starting async cleanup blocked for %s", elapsed)
	}

	working := []Message{
		scoreAssistantCall("c1", "read_file", `{"path":"/tmp/a.md"}`),
		scoreToolMsg("c1", "read_file", strings.Repeat("A", 500)),
	}
	transcript := append([]Message(nil), working...)
	if done, _, _, _, _ := drainToolScoreCleanupJob(job, working, transcript, nil); done {
		t.Fatal("cleanup must not be considered done before the classifier returns")
	}

	releaseScorer()
	select {
	case <-job.done:
	case <-time.After(2 * time.Second):
		t.Fatal("background cleanup did not finish after the classifier returned")
	}

	done, cleared, saved, cleaned, err := drainToolScoreCleanupJob(job, working, transcript, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !done || cleared != 1 || saved <= 0 || len(cleaned) != 1 || cleaned[0].CallID != "c1" {
		t.Fatalf("drain = done %v cleared %d saved %d cleaned %#v", done, cleared, saved, cleaned)
	}
	if !strings.HasPrefix(working[1].Content, defaultScoreMarker) || !strings.HasPrefix(transcript[1].Content, defaultScoreMarker) {
		t.Fatalf("drain must update both histories: working=%q transcript=%q", working[1].Content, transcript[1].Content)
	}
}

func TestToolScoreCleanupDrainHonorsNewEvidencePins(t *testing.T) {
	doneCh := make(chan struct{})
	close(doneCh)
	job := &toolScoreCleanupJob{
		done: doneCh,
		replacements: []CleanedToolResult{{
			CallID: "call-evidence", Name: "bash", Runes: 500, Score: 0.99,
			Content: defaultScoreMarker + " bash 共 500 字符",
		}},
	}
	working := []Message{scoreToolMsg("call-evidence", "bash", strings.Repeat("A", 500))}
	transcript := append([]Message(nil), working...)
	done, cleared, _, _, err := drainToolScoreCleanupJob(job, working, transcript, map[string]bool{"call-evidence": true})
	if err != nil || !done || cleared != 0 {
		t.Fatalf("pinned cleanup drain = done %v cleared %d err %v", done, cleared, err)
	}
	if strings.HasPrefix(working[0].Content, defaultScoreMarker) || strings.HasPrefix(transcript[0].Content, defaultScoreMarker) {
		t.Fatal("pinned evidence was cleaned during drain")
	}
}
