package main

// 规则式工具结果清理打分器。
//
// 为什么不用分类器：这个仓库原本把打分外包给一个远端分类服务，实测那个端点
// 连续返回 429（每日额度按 IP 计），叠加 fail_open=true 与该文件零日志，于是
// 清理机制静默失效了很久——配置看着是开的，实际一次都没生效。更糟的是换成本地
// 模型后实测：两种 checkpoint 的最佳精度只有 30%/42%（危险错误 48%/58%），
// 比规则还差。所以改回规则，并且把判断依据写死成可解释的若干条。
//
// 核心认识：要判断的不是「这段文字和后面的话题像不像」，而是「这条结果驱动了
// 下一步动作吗」。后者是因果关系，trace 里现成；前者才是语义模型的活，而语义
// 模型恰恰不擅长因果——离线回放 40 条真实样本时，规则危险错误 2.5%，两个模型
// checkpoint 分别是 48% 和 58%。
//
// 评分方向与原分类器一致：分数越高越可能被清理，阈值仍由 cfg.Threshold 控制。

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// cleanupKeepTools 的结果永不清理：它们要么是计划证据，要么是主人明确要的东西。
var cleanupKeepTools = map[string]bool{
	"plan": true, "show_result": true, "memory_search": true,
	"skill_search": true, "ask_user": true,
}

// cleanupNavTools 的产出本身就是给下一步导航用的（grep 命中哪一行、glob 有哪些
// 文件）。它不会被复述——真正被使用的是它指向的位置，所以词面相似度看不见它。
var cleanupNavTools = map[string]bool{"grep": true, "glob": true}

// cleanupCheapTools 是机械输出：文件内容、命令回显、页面快照。内容一旦被消化成
// 推理结论就不再需要原文，最容易过期。
var cleanupCheapTools = map[string]bool{
	"bash": true, "read_file": true, "browser": true,
	"document_parser": true, "image_vqa": true,
}

// opaqueSourceKeepRunes 是 read_file 的默认保留门槛。工具结果里不记录自己的调用
// 参数（Args 常常是空的），所以 read_file 是唯一无法自证来源的工具。低于这个
// 体量清掉不心疼；超过它却可能删掉正在据此改代码的那一份。
// 门槛来自离线回放：4000 是精度拐点（危险错误 2→1，精度反升到 80%）。
const opaqueSourceKeepRunes = 4000

// cleanupSpecificRe 认的是「辨识度高」的标识：驼峰（handleModelChange）、
// 带扩展名的文件名（SidePanel.jsx）、路径片段、长随机串。
//
// 为什么需要它：R2 原本把「后续对话里出现过同名标识」当作还在引用。但 config、
// server、timeout 这类普通词在不同任务里撞名极频繁——读一遍 A 项目源码，
// 几轮后聊 B 项目又提到同名词，R2 就误判成还在引用。实测这一条拦下了 1262 条
// 候选里的 565 条，是已完成任务结果清不掉的头号原因。
//
// snake_case 也要算高辨识度：测试直接抓到了这个缺口——fallback_model 是配置键，
// 只因为它是下划线而非驼峰就被当成了普通词。补上之后，判据变成一句话：
// **带内部下划线或驼峰的、或者带点/斜杠的，是标识符；一个光秃秃的英文单词不是。**
// 这个方向偏保守（认下的更多 → 拦得更多 → 删得更少），宁可少删。
//
// 区分「撞名」和「真引用」靠的是词形辨识度，不是语义——所以这一条不需要模型。
var cleanupSpecificRe = regexp.MustCompile(
	`^[A-Za-z_][a-z0-9]*(?:[A-Z][A-Za-z0-9]*){1,}$` + // 驼峰 handleModelChange
		`|^[a-z][a-z0-9]*(?:_[a-z0-9]+)+$` + // snake_case fallback_model
		`|^[\w-]+\.[a-z]{2,4}$` + // 文件名 SidePanel.jsx
		`|/[\w.-]{3,}/` + // 路径片段
		`|^[A-Za-z0-9_-]{16,}$`) // 长 hash

func cleanupTokenIsSpecific(tok string) bool { return cleanupSpecificRe.MatchString(tok) }

// cleanupJSONKeyRe 匹配 `"exit_code": 0` 这种 JSON 字段位。
var cleanupJSONKeyRe = regexp.MustCompile(`"[^"]*"\s*:\s*`)

// cleanupTokenIsJSONKey 报告 tok 在 text 里是否落在 JSON 字段名位置上。
//
// 实测依据：R2 拦下的碰撞里 85% 是 JSON 字段名（exit_code / truncated / content /
// encoding / line_count）。它们在每个工具结果里长得一模一样，压根不是「还在引用
// 这个实体」——是字段名撞名。这类用正则就能认；剩下 15% 是路径、代码符号、提交
// hash，那是真引用，删了就是误伤。也正因为如此，这一层没有语义模型可做：真正要分
// 辨的那部分，结构特征已经足够明显。
func cleanupTokenIsJSONKey(text, tok string) bool {
	quoted := `"` + tok + `"`
	i := strings.Index(text, quoted)
	if i < 0 {
		return false
	}
	for _, m := range cleanupJSONKeyRe.FindAllStringIndex(text, -1) {
		if m[0] <= i && i < m[1] {
			return true
		}
	}
	return false
}

var cleanupTokenRe = regexp.MustCompile(`/[\w./-]{6,}|[A-Za-z_][A-Za-z0-9_.-]{6,}`)

// 不用 (?<!\w) 这类回顾断言：Go 的 RE2 不支持，build 阶段不报错、go test 在
// package init 时才 panic。原来从 Python 版直接抄过来就是这么翻车的。
var cleanupPathRe = regexp.MustCompile(`[A-Za-z]:[\\/][\w\\./-]{4,}|/[A-Za-z0-9_.][\w./-]{3,}`)

// cleanupStopTokens 是出现即视为无信息量的常见词。它们的出现不代表后续真的
// 引用了这条结果。
var cleanupStopTokens = map[string]bool{
	"function": true, "const": true, "return": true, "import": true,
	"export": true, "default": true, "class": true, "console": true,
	"undefined": true, "arguments": true, "prototype": true,
	"constructor": true, "async": true, "await": true, "require": true,
	"module": true, "exports": true, "createElement": true,
	"addEventListener": true,
}

func cleanupTokens(text string) []string {
	matches := cleanupTokenRe.FindAllString(text, -1)
	out := make([]string, 0, len(matches))
	for _, t := range matches {
		if len(t) < 7 || cleanupStopTokens[strings.ToLower(t)] {
			continue
		}
		out = append(out, t)
	}
	return out
}

func cleanupPaths(text string) []string {
	matches := cleanupPathRe.FindAllString(text, -1)
	seen := map[string]bool{}
	out := make([]string, 0, len(matches)*2)
	add := func(s string) {
		s = strings.ToLower(s)
		if len(s) >= 6 && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, p := range matches {
		p = strings.ReplaceAll(p, "\\", "/")
		add(p)
		if idx := strings.LastIndex(p, "/"); idx >= 0 {
			add(p[idx+1:]) // 只要文件名也能对上
		}
	}
	return out
}

// sharesToken 做双向包含比较而不是精确相等。这一点是被离线回放逼出来的：
// 结果里出现的是 settings-trigger-copy，下一步 Select-String 的 pattern 里是
// settings-trigger，两者只是前缀关系，精确 token 相交会漏掉，危险错误降不下来。
func sharesToken(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
			if len(x) >= 8 && len(y) >= 8 && (strings.Contains(y, x) || strings.Contains(x, y)) {
				return true
			}
		}
	}
	return false
}

// containsToken 判断 needle 是否在 haystack 里（双向包含）。
func containsToken(haystack []string, needle string) bool {
	for _, x := range haystack {
		if x == needle || (len(needle) >= 8 && len(x) >= 8 && (strings.Contains(x, needle) || strings.Contains(needle, x))) {
			return true
		}
	}
	return false
}

func sharesPath(a, b []string) bool {
	set := make(map[string]bool, len(b))
	for _, p := range b {
		set[p] = true
	}
	for _, p := range a {
		if set[p] {
			return true
		}
	}
	return false
}

// cleanupDecision 是单条候选的判定结果。Rule 会被写进日志和清理摘要——
// 规则能说出「为什么清」，模型给不出，这是它更值得信任的原因之一。
type cleanupDecision struct {
	Clear bool
	Rule  string
	// NeedTiebreak 表示「C1 曾经命中，但已超窗」，该不该清交给嵌入模型判一次。
	// 这是模型唯一的入口——其余判定仍全部是结构规则。
	NeedTiebreak bool
}

const (
	scoreKeep       = 0.0
	scoreClearable  = 1.0
	cleanupRuleKeep = "保留"
)

// c1ProtectWindow 是 C1 保护的步数上限。
//
// C1 判的是「下一步调用用到了它」，这只证明当时需要，不证明现在需要。超窗之后
// C1 就不再保护，结果转入待裁决名单交给嵌入模型判一次（见 stale_tiebreak.go）。
// 窗口取 3：一轮循环里读→改→跑通常在 3 步内完成；更远的引用说明这份结果仍被
// 反复依赖，不该只凭一次早期引用就判死。
const c1ProtectWindow = 3

// staleSimilarityThreshold 是待裁决结果的相似度阈值，**低于它才清理**。
//
// 方向容易记反：阈值越高，落进清理区的相似度越多，清理越激进。
//
// 当前值 0.65 由主人指定。已知代价，写在这里以免后人误读成「模型判定很可靠」：
//
//	· 实测 163 条超窗候选在 0.65 下清 160 条（98%），相似度最高的也只有 0.674
//	  —— 阈值一旦 ≥0.68 就是无条件清光，模型等于不存在
//	· 25 条人工标注里那 4 条「不可清」（0.643/0.644/0.644/0.652）有 3 条会被
//	  0.65 清掉
//	· 那 4 条正样本极少，且标注口径（与当前话题相关）与嵌入判据同源，存在循环
//	  论证，所以这份证据不足以支撑「98% 都是安全的」
//
// 想回退只改这一个常数，或设 FAIRY_STALE_TIEBREAK_URL="" 一键关掉裁决（退回每轮
// 清理 1 条的结构规则行为）。实测各档：0.55→45 条、0.60→119 条、0.65→160 条。
const staleSimilarityThreshold = 0.65

func decideCleanup(c ToolScoreCandidate, seenDigest map[string]bool) cleanupDecision {
	if cleanupKeepTools[c.Name] {
		return cleanupDecision{false, "R1 证据类工具", false}
	}

	head := truncateToolScoreRunes(strings.ReplaceAll(c.Result, "\n", " "), 400)
	headToks := cleanupTokens(head)

	// C1：下一步调用的参数里用到了本结果里的标识。这是因果依赖里最强的信号——
	// agent 读到某个符号，紧接着就用它去 grep/改。
	//
	// 但它只在时间窗内成立。超窗后这份因果早已过去，转入待裁决名单，由嵌入模型
	// 判「跟当前正在做的事还有没有关系」——结构判据对这一组完全失效：实测 163 条
	// 超窗候选里，按字符串重合判断一条都筛不出来。
	//
	// 以 C1Gap 为准，而不是只看重���邻的下一次调用：那条路径会漏掉绝大多数超窗
	// 条目——间隔远大于 1 时「紧邻下一次」里根本没有它的标识，C1 压根不触发，
	// 条目直接掉到 R2/R5a 去了。实测 583 条候选里只有 2 条因此进了裁决，
	// 而 C1Gap>3 的其实有 230 条。
	if c.C1Gap > c1ProtectWindow {
		return cleanupDecision{false, "C1 已超窗，待语义裁决", true}
	}
	if c.C1Gap >= 1 {
		// The result is still being referenced right after it was produced, so it
		// is inside the protection window. Marked "C1w" (window) to tell it apart
		// from the "C1" rule above, which is the one that reaches the tiebreak.
		return cleanupDecision{false, "C1w 下一步用到本结果的标识", false}
	}

	// C2：下一步操作的是本结果提到的同一个文件。对 read_file 基本无效（结果不带
	// 自身参数），只有结果正文里本身出现路径时才有意义。
	if len(headToks) > 0 && len(cleanupPaths(head)) > 0 && sharesPath(cleanupPaths(head), cleanupPaths(c.NextToolArgs)) {
		return cleanupDecision{false, "C2 下一步操作同一文件", false}
	}

	// C3：导航类工具的命中结果本身就是为了指向下一步。
	if cleanupNavTools[c.Name] {
		return cleanupDecision{false, "C3 导航类工具", false}
	}

	// R3：同工具同内容的重复结果，只清旧的。
	sum := sha1.Sum([]byte(fmt.Sprintf("%s:%s", c.Name, truncateToolScoreRunes(c.Result, 120))))
	digest := hex.EncodeToString(sum[:])[:12]
	if seenDigest[digest] {
		return cleanupDecision{true, "R3 重复结果", false}
	}
	seenDigest[digest] = true

	// R2：后续对话里明确引用了它的标识。
	//
	// 分档：任务已经结束的话，只认高辨识度的标识。任务做完了，它内部用到的
	// 依赖当时就消解了，后面再出现同名词多半是别的任务在聊同一类东西。
	// 进行中的任务保持全口径——那里的同名提及往往就是正在进行的引用。
	//
	// 这个闸门在只有单回合的会话（全部定时任务会话）上永远不触发，所以对它们
	// 行为完全不变；它只在真正的多轮聊天里才起作用。
	if len(headToks) > 0 {
		followToks := cleanupTokens(c.FollowUp)
		for _, tok := range headToks {
			if !containsToken(followToks, tok) {
				continue
			}
			if c.TaskComplete && !cleanupTokenIsSpecific(tok) {
				continue // 任务已完成，普通词的同名不算引用
			}
			if cleanupTokenIsJSONKey(c.FollowUp, tok) {
				continue // 后续那处落在 JSON 字段名上，不是实体引用
			}
			return cleanupDecision{false, "R2 后续引用了标识", false}
		}
	}

	// R4：对话尾部的结果还在用。
	if c.TailRank >= 8 {
		return cleanupDecision{false, "R4 对话尾部", false}
	}

	// R5a：来源不明的读取结果，保守保留。
	if c.Name == "read_file" && c.Runes >= opaqueSourceKeepRunes {
		return cleanupDecision{false, "R5a 来源不明的读取结果", false}
	}

	if cleanupCheapTools[c.Name] {
		return cleanupDecision{true, "R5 机械输出且无引用", false}
	}
	return cleanupDecision{false, cleanupRuleKeep, false}
}

// ScoreToolCandidatesLocal 用规则给每条候选打分，返回与分类器同形状的分数。
//
// keptReasons 便于日志/测试看清每条为什么被留或被清。分类器只吐一个概率，
// 出了问题根本无从归因——这正是它长期静默失效却没人发现的原因之一。
func ScoreToolCandidatesLocal(cands []ToolScoreCandidate, cfg *Config) (scores []float64, keptReasons []string) {
	return ScoreToolCandidatesLocalCtx(context.Background(), nil, cands, cfg)
}

// ScoreToolCandidatesLocalCtx 是带语义裁决的版本。msgs 是当前会话消息，用于取
// 「当前在做什么」作为裁决参照；为 nil 时跳过裁决（所有超窗候选一律保留）。
func ScoreToolCandidatesLocalCtx(ctx context.Context, msgs []Message, cands []ToolScoreCandidate, cfg *Config) (scores []float64, keptReasons []string) {
	if len(cands) == 0 {
		return nil, nil
	}
	scores = make([]float64, 0, len(cands))
	keptReasons = make([]string, 0, len(cands))
	seen := map[string]bool{}
	decisions := make([]cleanupDecision, 0, len(cands))
	for _, c := range cands {
		d := decideCleanup(c, seen)
		decisions = append(decisions, d)
	}

	// 语义裁决：只处理 C1 已超窗的那一组，其余判定一律不动。
	var stale []ToolScoreCandidate
	for i, d := range decisions {
		if d.NeedTiebreak {
			stale = append(stale, cands[i])
		}
	}
	staleScores := map[string]float64{}
	if len(stale) > 0 {
		nowCtx := nowContextText(msgs, 800)
		staleScores = scoreStaleTiebreak(ctx, staleTiebreakEndpoint(), stale, nowCtx)
		cleared, judged := 0, 0
		for i, d := range decisions {
			if !d.NeedTiebreak {
				continue
			}
			judged++
			sim, ok := staleScores[cands[i].CallID]
			if !ok {
				decisions[i].Rule += "（裁决无结果，保留）"
				continue
			}
			if sim < staleSimilarityThreshold {
				decisions[i] = cleanupDecision{true, fmt.Sprintf("C1 超窗且语义无关 sim=%.3f", sim), false}
				cleared++
			} else {
				decisions[i].Rule = fmt.Sprintf("C1 超窗但语义相关 sim=%.3f", sim)
			}
		}
		fmt.Fprintf(os.Stderr, "[tool-score-cleanup] 语义裁决：超窗候选 %d 条，参与裁决 %d 条，清除 %d 条（阈值 %.2f，服务给出结果 %d 条，保留率 %d%%）\n",
			len(stale), judged, cleared, staleSimilarityThreshold, len(staleScores), keepPercent(len(stale), cleared))
	}

	for _, d := range decisions {
		if d.Clear {
			scores = append(scores, scoreClearable)
		} else {
			scores = append(scores, scoreKeep)
		}
		keptReasons = append(keptReasons, d.Rule)
	}
	if cfg != nil {
		fmt.Fprintf(os.Stderr, "[tool-score-cleanup] 本地规则打分：候选 %d 条（待裁决 %d 条）%v\n", len(cands), len(stale), summarizeReasons(keptReasons))
	}
	return scores, keptReasons
}

// keepPercent reports the share of judged candidates that were kept, i.e. the
// ones whose similarity was still at or above the threshold. The previous
// expression (100-judged/len(stale)*100) used integer division and measured
// the wrong thing entirely: it reported 0 whenever every candidate reached the
// tiebreak, which is exactly the case worth reporting.
func keepPercent(judged, cleared int) int {
	if judged <= 0 {
		return 0
	}
	return (judged - cleared) * 100 / judged
}

func summarizeReasons(reasons []string) string {
	counts := map[string]int{}
	order := []string{}
	for _, r := range reasons {
		key := strings.Fields(r)
		label := cleanupRuleKeep
		if len(key) > 0 {
			label = key[0]
		}
		if counts[label] == 0 {
			order = append(order, label)
		}
		counts[label]++
	}
	parts := make([]string, 0, len(order))
	for _, k := range order {
		parts = append(parts, fmt.Sprintf("%s×%d", k, counts[k]))
	}
	return strings.Join(parts, " ")
}

// UseRemoteClassifier 报告是否显式启用了远端分类器。
//
// 默认关闭。留这个开关不是为了继续用它，而是为了让「到底走哪条路」这件事在配置
// 层面可见——之前 endpoint 一直配着、enabled 一直是 true，看起来在跑，实际每
// 次都被 fail_open 吞掉。
func (c *ToolScoreCleanupConfig) UseRemoteClassifier() bool {
	return c != nil && strings.TrimSpace(c.Endpoint) != "" && c.RemoteClassifier == true
}
