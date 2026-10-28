package main

// 超窗工具结果的语义裁决客户端。
//
// 这是嵌入模型在清理链路里**唯一的入口**，而且只做裁判不做判官：
//   · 只有「C1 曾经命中、但已超出时间窗」的那一组会送到这里
//   · R1/R2/R3/R5a/C3 等结构护栏全部原样保留
//   · 服务不可达、超时、返回异常、字段缺失 —— 一律按「不可清」处理
//
// 也就是说这个模型最坏的情况是**完全不干活**，退回改造前的行为（每轮清 1 条），
// 而不是误删。这一点是刻意的：实测证据只有 25 条人工标注、正样本仅 4 条，
// 不值得让一个统计上很薄的信号去独当一面。
//
// 为什么需要它：结构判据（后续是否出现同标识）对超窗候选完全失效——实测 163 条
// 候选里按字符串重合判断一条都筛不出来，全被判成仍在使用。判断「几十步前的结果
// 跟我现在在做的事还有没有关系」靠的是语义关联，不是字符串重合。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	// staleTiebreakEndpoint 默认与 tools/stale_tiebreak_service.py 里的 PORT 一致。
	defaultStaleTiebreakEndpoint = "http://127.0.0.1:8137/score"
	staleTiebreakTimeout         = 8 * time.Second
)

type staleTiebreakItem struct {
	CallID string `json:"call_id"`
	Head   string `json:"head"`
}

type staleTiebreakRequest struct {
	Items      []staleTiebreakItem `json:"items"`
	NowContext string              `json:"now_context"`
}

type staleTiebreakScore struct {
	CallID     string  `json:"call_id"`
	Similarity float64 `json:"similarity"`
}

type staleTiebreakResponse struct {
	Scores []staleTiebreakScore `json:"scores"`
	Error  string               `json:"error"`
}

// staleTiebreakEndpoint 解析裁决服务地址。FAIRY_STALE_TIEBREAK_URL 可覆盖；
// 置空串可以彻底关掉裁决（此时所有超窗候选一律保留）。
func staleTiebreakEndpoint() string {
	if v, ok := os.LookupEnv("FAIRY_STALE_TIEBREAK_URL"); ok {
		return strings.TrimSpace(v)
	}
	return defaultStaleTiebreakEndpoint
}

// nowContextText 从消息尾部取一段作为「当前在做什么」的参照。
// 只取尾部若干条：超窗结果的判定问题是「跟现在还相关吗」，参照物就该是现在。
func nowContextText(msgs []Message, maxChars int) string {
	var parts []string
	for i := len(msgs) - 1; i >= 0 && len(parts) < 8; i-- {
		m := msgs[i]
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		if t := strings.TrimSpace(m.Content); t != "" {
			parts = append([]string{strings.Join(strings.Fields(t), " ")}, parts...)
		}
	}
	out := strings.Join(parts, " ")
	if maxChars > 0 && len([]rune(out)) > maxChars {
		out = string([]rune(out)[:maxChars])
	}
	return out
}

// scoreStaleTiebreak 向裁决服务要一批相似度。
//
// 返回的 map 只包含服务明确给出结果的条目；调用方必须把「map 里没有」当成不可清，
// 绝不能当成可清。任何错误都返回空 map（外加一行 stderr 原因）。
func scoreStaleTiebreak(ctx context.Context, endpoint string, cands []ToolScoreCandidate, nowCtx string) map[string]float64 {
	out := map[string]float64{}
	if endpoint == "" || len(cands) == 0 || strings.TrimSpace(nowCtx) == "" {
		return out
	}
	items := make([]staleTiebreakItem, 0, len(cands))
	for _, c := range cands {
		if c.CallID == "" {
			continue // 没有 CallID 就没法把分数对回候选，保守放弃
		}
		items = append(items, staleTiebreakItem{
			CallID: c.CallID,
			Head:   strings.Join(strings.Fields(strings.ReplaceAll(c.Result, "\n", " ")), " "),
		})
	}
	if len(items) == 0 {
		return out
	}
	body, err := json.Marshal(staleTiebreakRequest{Items: items, NowContext: nowCtx})
	if err != nil {
		fmt.Fprintf(os.Stderr, "[tool-score-cleanup] 裁决请求编码失败: %v\n", err)
		return out
	}
	callCtx, cancel := context.WithTimeout(ctx, staleTiebreakTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[tool-score-cleanup] 裁决请求构造失败: %v\n", err)
		return out
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[tool-score-cleanup] 裁决服务不可达（全部按不可清处理）: %v\n", err)
		return out
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "[tool-score-cleanup] 裁决服务返回 %d（全部按不可清处理）\n", resp.StatusCode)
		return out
	}
	var parsed staleTiebreakResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		fmt.Fprintf(os.Stderr, "[tool-score-cleanup] 裁决响应解析失败（全部按不可清处理）: %v\n", err)
		return out
	}
	if parsed.Error != "" {
		fmt.Fprintf(os.Stderr, "[tool-score-cleanup] 裁决服务报告错误（全部按不可清处理）: %s\n", parsed.Error)
		return out
	}
	for _, s := range parsed.Scores {
		out[s.CallID] = s.Similarity
	}
	return out
}

// scoreToolCandidatesWithTiebreak 是带裁决的入口。远端分类器显式启用时仍走那条路，
// 否则走本地规则 + 语义裁决。
func scoreToolCandidatesWithTiebreak(ctx context.Context, msgs []Message, cands []ToolScoreCandidate, cfg *Config) ([]float64, error) {
	if cfg != nil && cfg.ToolScoreCleanup.UseRemoteClassifier() {
		return ScoreToolCandidatesContext(ctx, cands, cfg)
	}
	scores, _ := ScoreToolCandidatesLocalCtx(ctx, msgs, cands, cfg)
	return scores, nil
}
