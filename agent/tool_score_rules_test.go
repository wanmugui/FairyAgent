package main

import (
	"strings"
	"testing"
)

// 下面是规则打分器的行为约定。这些用例把「为什么保留 / 为什么清理」钉死，
// 因为这套规则最怕的不是精度不够，而是有一天悄悄变了却没人发现。

func TestLocalScorerDefaultsToRulesNotClassifier(t *testing.T) {
	// 默认路径不碰网络：Endpoint 即使配着也不该被调用。
	cfg := &Config{}
	cfg.ToolScoreCleanup.Enabled = true
	cfg.ToolScoreCleanup.Endpoint = "http://127.0.0.1:1/should-not-be-called"
	cfg.ToolScoreCleanup.Threshold = 0.5

	cands := []ToolScoreCandidate{
		{CallID: "c1", Name: "bash", Runes: 900, Result: strings.Repeat("x", 900)},
	}
	scores, err := ScoreToolCandidatesContext(nil, cands, cfg)
	if err != nil {
		t.Fatalf("local scoring must not fail: %v", err)
	}
	if len(scores) != 1 {
		t.Fatalf("scores = %v", scores)
	}
}

func TestRuleKeepsEvidenceToolsForever(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range []string{"plan", "show_result", "ask_user"} {
		if d := decideCleanup(ToolScoreCandidate{Name: name, Runes: 5000, Result: strings.Repeat("y", 5000)}, seen); d.Clear {
			t.Fatalf("%s must never be cleared, got %q", name, d.Rule)
		}
	}
}

func TestRuleKeepsNavigationToolResults(t *testing.T) {
	// C3：grep/glob 的命中不会被复述，真正被用的是它指向的下一步。
	seen := map[string]bool{}
	d := decideCleanup(ToolScoreCandidate{
		Name: "grep", Runes: 1200, Result: `{"hits":[{"path":"gnkit.py","line":58,"match":"class GNError(RuntimeError):"}]}`,
	}, seen)
	if d.Clear {
		t.Fatal("grep result must be kept; its value is navigation, not quotation")
	}
}

func TestRuleKeepsResultWhenNextCallUsesItsSymbol(t *testing.T) {
	// C1：下一步的 Search/grep 模式里出现了本结果的符号。这是因果依赖，
	// 词面相似度看不见它。
	seen := map[string]bool{}
	// C1Gap 由采集端 c1ReferenceGap 算出：后续第一次用到这里标识的工具调用隔了
	// 多少条消息。语义已从「看重邻的下一次调用」改为「看 C1Gap」，所以用例必须
	// 给这个字段，否则 C1 根本不触发（默认值 0 表示没找到引用）。
	d := decideCleanup(ToolScoreCandidate{
		Name: "read_file", Runes: 1500, C1Gap: 1,
		Result:       `<span className="settings-trigger-copy">FILES</span>`,
		NextToolArgs: `{"command":"Select-String -Path App.jsx -Pattern \"settings-trigger|settings-button\""}`,
	}, seen)
	if d.Clear {
		t.Fatalf("result feeding the next call must be kept, got %q", d.Rule)
	}
}

func TestRuleKeepsLargeReadFileBecauseSourceIsUnknown(t *testing.T) {
	// R5a：read_file 的结果不带自身调用参数，是唯一无法自证来源的工具。
	seen := map[string]bool{}
	d := decideCleanup(ToolScoreCandidate{
		Name: "read_file", Runes: opaqueSourceKeepRunes + 1,
		Result: strings.Repeat("z", opaqueSourceKeepRunes+1),
	}, seen)
	if d.Clear {
		t.Fatalf("large read_file must be kept by default, got %q", d.Rule)
	}
}

func TestRuleClearsSupersededDuplicate(t *testing.T) {
	// R3：同工具同内容的重复结果，后一份取代前一份。
	// 用 edit_file 而不是 bash：bash 会被 R5（机械输出且无引用）清掉，那样就测不到
	// R3 了，测试要证明的是「重复」这个条件本身在起作用。
	seen := map[string]bool{}
	same := ToolScoreCandidate{Name: "edit_file", Runes: 900, Result: strings.Repeat("q", 900)}
	if d := decideCleanup(same, seen); d.Clear {
		t.Fatalf("first occurrence must not be treated as duplicate, got %q", d.Rule)
	}
	if d := decideCleanup(same, seen); !d.Clear {
		t.Fatalf("second occurrence is the superseded one and must be cleared, got %q", d.Rule)
	}
}

func TestRemoteClassifierIsOptIn(t *testing.T) {
	cfg := &ToolScoreCleanupConfig{Endpoint: "http://example.invalid/"}
	if cfg.UseRemoteClassifier() {
		t.Fatal("an endpoint alone must not turn the remote path back on")
	}
	cfg.RemoteClassifier = true
	if !cfg.UseRemoteClassifier() {
		t.Fatal("remote_classifier=true with an endpoint must enable the remote path")
	}
}

// 血缘链：已完成任务的结果不该再被普通词的同名提及保护住。

func TestRuleKeepsGenericWordReferenceWhileTaskIsOngoing(t *testing.T) {
	seen := map[string]bool{}
	d := decideCleanup(ToolScoreCandidate{
		Name: "bash", Runes: 900, Result: `reading timeout settings response from the server`,
		FollowUp: "还在改 timeout 与 settings 的配置", TaskComplete: false,
	}, seen)
	if d.Clear {
		t.Fatalf("in-progress task must keep the generic match, got %q", d.Rule)
	}
}

func TestRuleIgnoresGenericWordReferenceAfterTaskCompleted(t *testing.T) {
	// 任务做完了，后面又出现 config/timeout 这种普通词，多半是别的任务在聊
	// 同类东西，不该继续把这份结果钉在上下文里。
	seen := map[string]bool{}
	d := decideCleanup(ToolScoreCandidate{
		Name: "bash", Runes: 900, Result: `reading timeout settings response from the server`,
		FollowUp: "新任务也提到了 timeout 与 settings", TaskComplete: true,
	}, seen)
	if !d.Clear {
		t.Fatalf("completed task must not be pinned by a generic word, got %q", d.Rule)
	}
}

func TestRuleStillKeepsSpecificReferenceAfterTaskCompleted(t *testing.T) {
	// 但高辨识度的标识（驼峰 / 文件名）即使任务完成也算真引用，不能误删。
	seen := map[string]bool{}
	d := decideCleanup(ToolScoreCandidate{
		Name: "bash", Runes: 900,
		Result:   `export const SidePanel = () => { return <div className="panel"/> }`,
		FollowUp: "回到刚才那个 SidePanel 组件继续改", TaskComplete: true,
	}, seen)
	if d.Clear {
		t.Fatalf("a specific identifier in a later turn is a real reference, got %q", d.Rule)
	}
}

func TestSpecificityJudgeSeparatesIdentifiersFromCommonWords(t *testing.T) {
	specific := []string{"handleModelChange", "SidePanel.jsx", "/path/to/Fairy/skills/procedural-3d/", "a1b2c3d4e5f6a7b8"}
	for _, tok := range specific {
		if !cleanupTokenIsSpecific(tok) {
			t.Fatalf("%q should count as high-specificity", tok)
		}
	}
	common := []string{"config", "timeout", "settings", "response", "function"}
	for _, tok := range common {
		if cleanupTokenIsSpecific(tok) {
			t.Fatalf("%q should not count as high-specificity", tok)
		}
	}
}

// 回归：第一版把判据写成「interaction_id 为空」，而实测所有 user 消息都带
// id，导致闸门恒不触发。这条用例钉住真实形状。

func TestTaskCompleteNeedsIDChangeNotEmptyID(t *testing.T) {
	msgs := []Message{
		{Role: "user", InteractionID: "req-001"},
		{Role: "assistant"},
		{Role: "tool", Name: "bash", Content: "body"},
		{Role: "user", InteractionID: "req-001"}, // 同一回合被拆成两条，仍是同一回合
		{Role: "assistant"},
	}
	if hasLaterRealTurn(msgs, 2) {
		t.Fatal("same interaction_id is the same turn, task is not complete")
	}
	msgs = append(msgs, Message{Role: "user", InteractionID: "req-002"})
	if !hasLaterRealTurn(msgs, 2) {
		t.Fatal("a new interaction_id means a new turn, task is complete")
	}
}

func TestTaskCompleteStaysOffWithoutInteractionIDs(t *testing.T) {
	msgs := []Message{
		{Role: "user"},
		{Role: "tool", Name: "bash", Content: "body"},
		{Role: "user"},
	}
	if hasLaterRealTurn(msgs, 1) {
		t.Fatal("without ids the gate must stay off rather than guess")
	}
}

func TestJSONFieldNameIsNotAnEntityReference(t *testing.T) {
	// exit_code 在每个工具结果里都长得一样，它不是「还在引用这个实体」。
	seen := map[string]bool{}
	d := decideCleanup(ToolScoreCandidate{
		Name:         "bash",
		Runes:        900,
		Result:       `checking that {"exit_code": 0} is set in the runner`,
		FollowUp:     `next command returned {"exit_code": 1}`,
		TaskComplete: true,
	}, seen)
	if !d.Clear {
		t.Fatalf("a JSON field name must not pin a completed task's result, got %q", d.Rule)
	}
}

func TestRealSymbolAfterTaskCompletedStillBlocks(t *testing.T) {
	// 但代码符号即使任务完成后也算真引用，不能被这条豁免误伤。
	seen := map[string]bool{}
	d := decideCleanup(ToolScoreCandidate{
		Name:         "bash",
		Runes:        900,
		Result:       `const fallback_model = "x" in config`,
		FollowUp:     `再确认一下 fallback_model 有没有写进去`,
		TaskComplete: true,
	}, seen)
	if d.Clear {
		t.Fatalf("a real symbol reference must still keep the result, got %q", d.Rule)
	}
}
