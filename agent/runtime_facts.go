package main

// 运行态事实注入。
//
// 起因：提示词是静态文件，所以「服务到底重启没有」「新二进制上线没有」「代理
// 接上没有」这些事实，模型每次都得靠猜。今晚在这上面栽了两次——误以为重启过了、
// 以为改了代码其实一条命令都没执行成功。配置看着是开的、实际没跑，这类问题光靠
// 静态提示词看不出来。
//
// 把这些事实动态注入，模型就不用猜了。这里只放「能自动查证的客观事实」，不放
// 需要推断的结论。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var agentProcessStart = time.Now()

// 源码最新修改时间与 git HEAD 变化很慢，按 TTL 缓存即可；agent 二进制可能被
// 外部替换（mv 覆盖），所以每次都实际 stat，不缓存。
type runtimeFactsCache struct {
	mu       sync.Mutex
	srcTime  time.Time
	gitHead  string
	fetched  time.Time
	hasValue bool
}

var factsCache runtimeFactsCache

const factsCacheTTL = 60 * time.Second

// ---------------------------------------------------------------------------
// 服务世代检测：告诉模型「服务在两轮之间重启过」
//
// 问题背景（2026-10-24）：服务端启动时刻原先只经环境变量 FAIRY_SERVER_STARTED_AT
// 传给 agent。可整条服务链（dev.mjs → server.cjs → agent）是一起重启的，agent
// 跟着重启就会继承「新的」启动时刻 —— 基线和现状同时被重置，模型无从判断
// 「相对上一轮到底重启没有」，只能靠 ps 对比 pid 去猜。
//
// 修法：server.cjs 启动时把世代号落盘到 ~/.fairy/state/server-generation.json。
// 磁盘上的记录活过任何进程重启。agent 把「自己这一轮看到的世代号」记在另一处
// 磁盘状态里（跨 agent 进程存活），两处不一致 = 服务重启过。
// ---------------------------------------------------------------------------

// serverGenerationFile 返回服务端世代记录文件路径。
func serverGenerationFile() string {
	if p := strings.TrimSpace(os.Getenv("FAIRY_SERVER_GENERATION_FILE")); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".fairy", "state", "server-generation.json")
}

// agentSeenGenerationFile 是 agent 自己的「我见过第几代服务」记录。
// 刻意与 server 侧的记录分开放：前者描述 agent 的观察，后者描述服务的事实。
// 支持 FAIRY_AGENT_SEEN_GENERATION_FILE 覆盖，便于测试隔离到临时目录。
func agentSeenGenerationFile() string {
	if p := strings.TrimSpace(os.Getenv("FAIRY_AGENT_SEEN_GENERATION_FILE")); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".fairy", "state", "agent-seen-generation.json")
}

type serverGeneration struct {
	Generation   int    `json:"generation"`
	StartedAtUnix int64 `json:"startedAtUnix"`
	StartedAt    string `json:"startedAt"`
	PID          int    `json:"pid"`
}

func readServerGeneration() (serverGeneration, bool) {
	f := serverGenerationFile()
	if f == "" {
		return serverGeneration{}, false
	}
	raw, err := os.ReadFile(f)
	if err != nil {
		return serverGeneration{}, false
	}
	var g serverGeneration
	if err := json.Unmarshal(raw, &g); err != nil || g.Generation <= 0 {
		return serverGeneration{}, false
	}
	return g, true
}

type seenGeneration struct {
	Generation int    `json:"generation"`
	At         string `json:"at"`
}

// readSeenGeneration 读出 agent 上一次记录的、服务是第几代。
func readSeenGeneration() (seenGeneration, bool) {
	f := agentSeenGenerationFile()
	if f == "" {
		return seenGeneration{}, false
	}
	raw, err := os.ReadFile(f)
	if err != nil {
		return seenGeneration{}, false
	}
	var s seenGeneration
	if err := json.Unmarshal(raw, &s); err != nil {
		return seenGeneration{}, false
	}
	return s, true
}

// writeSeenGeneration 记下「我现在看到的服务是第几代」。
// 写失败不致命：只是这一轮没法判断重启，注入里照实说明即可。
func writeSeenGeneration(g int) {
	f := agentSeenGenerationFile()
	if f == "" {
		return
	}
	payload := seenGeneration{Generation: g, At: time.Now().Format(time.RFC3339)}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return
	}
	tmp := f + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, f)
}

// restartNotice 描述「服务相对上一轮是否重启过」。
// 返回空串表示无法判断或确实没重启过。
func restartNotice() string {
	cur, ok := readServerGeneration()
	if !ok {
		return ""
	}
	prev, had := readSeenGeneration()
	if !had {
		// 第一次见这份记录：没有"上一轮"可比。只报当前世代，不猜。
		return fmt.Sprintf("服务世代 #%d（首次记录，无上一轮可比）", cur.Generation)
	}
	if prev.Generation == cur.Generation {
		return "" // 没重启
	}
	delta := cur.Generation - prev.Generation
	word := "重启过"
	if delta > 1 {
		word = fmt.Sprintf("重启过 %d 次", delta)
	}
	return fmt.Sprintf("⚠️ 服务在上一轮之后%s（世代 #%d → #%d，启动于 %s）",
		word, prev.Generation, cur.Generation, cur.StartedAt)
}

// syncSeenGeneration 在注入生成时调用：把当前世代记下来，供下一轮比对。
func syncSeenGeneration() {
	if cur, ok := readServerGeneration(); ok {
		writeSeenGeneration(cur.Generation)
	}
}

// newestGoSourceTime 返回 agent 目录下 .go 文件里最新的修改时间。
func newestGoSourceTime(dir string) time.Time {
	var newest time.Time
	entries, err := os.ReadDir(dir)
	if err != nil {
		return time.Time{}
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		if fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
	}
	return newest
}

func gitHead(repoRoot string) string {
	if repoRoot == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// runtimeFactsBlock 渲染运行态事实块。放在系统提示词**末尾**：前缀保持稳定，
// 提示词缓存才不会因为每次时间戳不同而整段失效。
func runtimeFactsBlock(cfg *Config) string {
	repoRoot := ""
	if cfg != nil {
		repoRoot = cfg.RepoRoot
	}
	if repoRoot == "" {
		repoRoot = os.Getenv("AGENT_REPO_ROOT")
	}
	if repoRoot == "" {
		if wd, err := os.Getwd(); err == nil {
			repoRoot = wd
		}
	}

	var b strings.Builder
	b.WriteString("\n## 运行态事实\n")
	b.WriteString("（每次请求由运行环境实测生成，不是估计值。需要确认某件事是否生效时以这里为准，不要凭印象假设。）\n")

	// 本 agent 进程
	up := time.Since(agentProcessStart).Truncate(time.Second)
	fmt.Fprintf(&b, "- 本 agent 进程启动于 %s（已运行 %s）\n",
		agentProcessStart.Format("2006-01-02 15:04:05"), up)

	// 前端服务（真正的「重启」指它）
	if v := strings.TrimSpace(os.Getenv("FAIRY_SERVER_STARTED_AT")); v != "" {
		fmt.Fprintf(&b, "- 前端服务(server.cjs)启动于 %s", v)
		if tp := strings.TrimSpace(os.Getenv("FAIRY_SERVER_STARTED_UNIX")); tp != "" {
			if ts, err := strconv.ParseInt(tp, 10, 64); err == nil {
				fmt.Fprintf(&b, "（%s 前）", time.Since(time.Unix(ts, 0)).Truncate(time.Minute))
			}
		}
		b.WriteString("\n")
	} else {
		b.WriteString("- 前端服务(server.cjs)启动时间：未知（server.cjs 未提供 FAIRY_SERVER_STARTED_AT）\n")
	}

	// 服务是否在两轮之间重启过。这一条要放在最显眼处：模型只有知道
	// 「服务刚重启」，才能解释「为什么我以为在的进程/会话不在了」，
	// 而不是花时间去排查一个其实已经被重启打断的场景。
	if notice := restartNotice(); notice != "" {
		fmt.Fprintf(&b, "- %s\n", notice)
	}
	syncSeenGeneration()

	// agent 二进制 vs 源码：这条专门用来防「以为改了其实没上线」
	exePath, _ := os.Executable()
	if exePath == "" {
		exePath = "/proc/self/exe"
	}
	binInfo, binErr := os.Stat(exePath)
	binTime := time.Time{}
	binSize := 0
	if binErr == nil {
		binTime = binInfo.ModTime()
		binSize = int(binInfo.Size())
	}
	if !binTime.IsZero() {
		fmt.Fprintf(&b, "- agent 二进制：构建于 %s，%d 字节\n",
			binTime.Format("2006-01-02 15:04:05"), binSize)
		srcTime := cachedSourceAndHead(repoRoot, &b)
		if !srcTime.IsZero() && srcTime.After(binTime) {
			fmt.Fprintf(&b, "  ⚠️ Go 源码最新修改于 %s，**晚于二进制**：说明有改动尚未编译上线，"+
				"涉及改动的结论必须先说明「未生效」，不要当成已生效。\n",
				srcTime.Format("2006-01-02 15:04:05"))
		} else if !srcTime.IsZero() {
			fmt.Fprintf(&b, "  Go 源码最新修改于 %s，不晚于二进制（源码改动已包含在当前二进制中）\n",
				srcTime.Format("2006-01-02 15:04:05"))
		}
	}

	// 代理
	proxy := firstNonEmpty(os.Getenv("https_proxy"), os.Getenv("HTTPS_PROXY"),
		os.Getenv("http_proxy"), os.Getenv("HTTP_PROXY"))
	if proxy != "" {
		fmt.Fprintf(&b, "- 出网代理：%s（已注入本进程，访问外网走它）\n", proxy)
	} else {
		b.WriteString("- 出网代理：未注入（访问境外站点可能不通，需要时先探测再决定是否换通道）\n")
	}

	// 仓库状态
	factsCache.mu.Lock()
	head := factsCache.gitHead
	factsCache.mu.Unlock()
	if head != "" {
		fmt.Fprintf(&b, "- 仓库 HEAD：%s\n", head)
	}
	if wd, err := os.Getwd(); err == nil {
		b.WriteString("- 工作目录：" + wd + "\n")
	}
	return b.String()
}

func cachedSourceAndHead(repoRoot string, b *strings.Builder) time.Time {
	factsCache.mu.Lock()
	defer factsCache.mu.Unlock()
	if factsCache.hasValue && time.Since(factsCache.fetched) < factsCacheTTL {
		return factsCache.srcTime
	}
	dir := repoRoot
	if dir == "" {
		dir = "."
	}
	factsCache.srcTime = newestGoSourceTime(filepath.Join(dir, "agent"))
	if factsCache.srcTime.IsZero() {
		factsCache.srcTime = newestGoSourceTime(dir)
	}
	factsCache.gitHead = gitHead(repoRoot)
	factsCache.fetched = time.Now()
	factsCache.hasValue = true
	return factsCache.srcTime
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// 仅供测试注入时间，避免测试依赖真实文件系统时间。
func timeNowForTest() (t time.Time) { return time.Now() }
func futureForTest() time.Time      { return time.Now().Add(48 * time.Hour) }
