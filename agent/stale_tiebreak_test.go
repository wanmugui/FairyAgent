package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 裁决层的核心约定：**模型只能决定「清更多」，永远不能决定「删掉原本要留的」**。
// 这些用例把边界钉死，因为一个统计上很薄的信号一旦越权，代价是不可逆的误删。

func staleCand(callID string) ToolScoreCandidate {
	return ToolScoreCandidate{
		CallID: callID, Name: "bash", Runes: 2000,
		Result: `const fallback_model = "x"; read config timeout settings from server`,
		C1Gap:  40, NextToolArgs: `{"command":"grep -n fallback_model src/app.js"}`,
	}
}

func TestStaleTiebreakServiceDownKeepsEverything(t *testing.T) {
	// 服务不可达必须全部保留——这是整条链路的安全底线。
	t.Setenv("FAIRY_STALE_TIEBREAK_URL", "http://127.0.0.1:1/nope")
	got := scoreStaleTiebreak(context.Background(), staleTiebreakEndpoint(),
		[]ToolScoreCandidate{staleCand("a"), staleCand("b")}, "现在在做别的���")
	if len(got) != 0 {
		t.Fatalf("服务不可达时不得返回任何分数，实际 %v", got)
	}
}

func TestStaleTiebreakMalformedResponseIsIgnored(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json at all"))
	}))
	defer srv.Close()
	got := scoreStaleTiebreak(context.Background(), srv.URL,
		[]ToolScoreCandidate{staleCand("a")}, "现在在做别的事")
	if len(got) != 0 {
		t.Fatalf("响应无法解析时不得返回分数，实际 %v", got)
	}
}

func TestStaleTiebreakServerErrorPayloadIsIgnored(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"scores":[{"call_id":"a","similarity":0.01}],"error":"model exploded"}`))
	}))
	defer srv.Close()
	got := scoreStaleTiebreak(context.Background(), srv.URL,
		[]ToolScoreCandidate{staleCand("a")}, "现在在做别的事")
	if len(got) != 0 {
		t.Fatal("服务自报错误时必须整体作废，否则等于采信了不可信的分数")
	}
}

func TestStaleTiebreakLowSimilarityIsClearable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"scores":[{"call_id":"a","similarity":0.31}]}`))
	}))
	defer srv.Close()
	got := scoreStaleTiebreak(context.Background(), srv.URL,
		[]ToolScoreCandidate{staleCand("a")}, "现在在做别的事")
	if v, ok := got["a"]; !ok || v >= staleSimilarityThreshold {
		t.Fatalf("低相似度应当返回分数且低于阈值，实际 %v", got)
	}
}

func TestEndpointEmptyDisablesTiebreakEntirely(t *testing.T) {
	// 置空 = 一键关掉裁决，所有超窗候选回到「保留」。
	t.Setenv("FAIRY_STALE_TIEBREAK_URL", "")
	if got := scoreStaleTiebreak(context.Background(), staleTiebreakEndpoint(),
		[]ToolScoreCandidate{staleCand("a")}, "现在在做别的事"); len(got) != 0 {
		t.Fatal("端点置空时不应发起任何裁决")
	}
}

func TestC1WindowExpiresAndHandsOverToTiebreak(t *testing.T) {
	seen := map[string]bool{}
	inside := decideCleanup(ToolScoreCandidate{
		Name: "bash", Runes: 2000, C1Gap: 1,
		Result:       `const fallback_model = "x"`,
		NextToolArgs: `{"command":"grep fallback_model app.js"}`,
	}, seen)
	if inside.NeedTiebreak {
		t.Fatal("窗口内的 C1 应当直接保留，不该送裁决")
	}
	outside := decideCleanup(ToolScoreCandidate{
		Name: "bash", Runes: 2000, C1Gap: c1ProtectWindow + 5,
		Result:       `const fallback_model = "x"`,
		NextToolArgs: `{"command":"grep fallback_model app.js"}`,
	}, map[string]bool{})
	if !outside.NeedTiebreak {
		t.Fatal("超窗的 C1 必须转入裁决，否则就是永久钉住")
	}
	if outside.Clear {
		t.Fatal("超窗本身不构成清理理由，要由裁决来决定")
	}
}

func TestNowContextUsesConversationTail(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "很久以前说的那件事"},
		{Role: "assistant", Content: "那时候我们讨论的是另一个话题"},
	}
	for i := 0; i < 10; i++ { // 填充：真实会话里"最近几轮"之外还有大量旧内容
		msgs = append(msgs,
			Message{Role: "assistant", Content: "中间某轮的工具输出与讨论"},
			Message{Role: "user", Content: "继续做手上的事"})
	}
	msgs = append(msgs, Message{Role: "user", Content: "现在改的是上下文治理"})
	got := nowContextText(msgs, 800)
	if !strings.Contains(got, "上下文治理") {
		t.Fatalf("参照物必须包含当前正在做的事，实际 %q", got)
	}
	if strings.Contains(got, "很久以前") {
		t.Fatalf("参照物不该包含会话早期的内容，实际 %q", got)
	}
}

// 阈值方向钉死：这是最容易记反的一处。sim 低于阈值才清理，所以阈值越高清得越多。
func TestStaleThresholdDirectionIsHigherClearsMore(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"scores":[{"call_id":"lo","similarity":0.40},{"call_id":"hi","similarity":0.64}]}`))
	}))
	defer srv.Close()
	got := scoreStaleTiebreak(context.Background(), srv.URL,
		[]ToolScoreCandidate{staleCand("lo"), staleCand("hi")}, "现在在做别的事")
	clears := func(t2 float64) []string {
		var out []string
		for _, id := range []string{"lo", "hi"} {
			if v := got[id]; v < t2 {
				out = append(out, id)
			}
		}
		return out
	}
	low := clears(0.50)
	high := clears(0.65)
	if len(low) != 1 || low[0] != "lo" {
		t.Fatalf("阈值 0.50 只应清掉 sim=0.40 那条，实际 %v", low)
	}
	// 0.64 < 0.65 所以清，0.66 > 0.65 则不清——「低于阈值才清理」是严格小于。
	if len(high) != 2 {
		t.Fatalf("阈值 0.65 应把 0.40 和 0.64 两条都清掉，实际 %v", high)
	}
	if staleSimilarityThreshold != 0.65 {
		t.Fatalf("当前阈值应为 0.65（主人指定），实际 %v", staleSimilarityThreshold)
	}
}
