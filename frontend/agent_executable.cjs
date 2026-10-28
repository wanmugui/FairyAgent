const fs = require("fs");
const os = require("os");
const path = require("path");

function resolveAgentExecutable(repoRoot, platform = process.platform, arch = process.arch) {
  const override = process.env.AGENT_LOOP_PATH;
  if (override) {
    try {
      if (fs.statSync(override).isFile()) return override;
    } catch {}
  }
  const suffix = platform === "win32" ? ".exe" : "";
  const candidate = path.join(repoRoot, ".tools", `agent-loop-${platform}-${arch}${suffix}`);
  try {
    if (fs.statSync(candidate).isFile()) return candidate;
  } catch {}
  throw new Error("agent executable not found; run pnpm dev or pnpm agent:build, or set AGENT_LOOP_PATH: " + candidate);
}

// 本机代理端口。mihomo 一直在跑（默认 mixed 端口 7897），但没有任何地方把它接进
// agent 的进程环境——仓库里搜不到代理配置，shell profile 里也没有。结果就是每次访问
// 外网都是裸连：github.com 直接返回 000，得临时去找 ghproxy 这类加速通道绕路，
// 而且速度差 14 倍（1.5MB/s vs 106KB/s）。
//
// 这里补上那道缺口。所有 agent 进程都从这里 spawn，所以改这一处就够。
const LOCAL_PROXY_CANDIDATES = [7897, 7890, 7891, 1080, 10809];
const NO_PROXY_HOSTS = "localhost,127.0.0.1,::1,*.local";

// 端口通着才注入：mihomo 没启动时保持裸连，比挂着一个死代理把所有请求卡死要好。
function detectLocalProxy() {
  const explicit = (process.env.FAIRY_PROXY || "").trim();
  if (explicit) return explicit.replace(/^https?:\/\//, "");
  const net = require("net");
  const fs2 = require("fs");
  for (const port of LOCAL_PROXY_CANDIDATES) {
    try {
      const fd = fs2.openSync(`/proc/net/tcp`, "r");
      const text = fs2.readFileSync(`/proc/net/tcp`, "utf-8");
      fs2.closeSync(fd);
      const hexPort = port.toString(16).toUpperCase().padStart(4, "0");
      if (text.includes(":" + hexPort + " ")) return `127.0.0.1:${port}`;
    } catch {}
    try {
      // /proc/net/tcp 读不到（非 Linux）时退化成真实连接探测
      const s = net.connect({ host: "127.0.0.1", port });
      s.destroy();
      return `127.0.0.1:${port}`;
    } catch {}
  }
  return "";
}

function withProxyEnv(env) {
  const proxy = detectLocalProxy();
  if (!proxy) return env;
  const url = proxy.startsWith("http") ? proxy : `http://${proxy}`;
  return {
    ...env,
    http_proxy: env.http_proxy || url,
    https_proxy: env.https_proxy || url,
    HTTP_PROXY: env.HTTP_PROXY || url,
    HTTPS_PROXY: env.HTTPS_PROXY || url,
    no_proxy: env.no_proxy || NO_PROXY_HOSTS,
    NO_PROXY: env.NO_PROXY || NO_PROXY_HOSTS,
  };
}

// server.cjs 的启动时刻。注入给 agent 是为了让运行态提示词能回答「上次重启是什么时候」
// ——这个事实此前无处可查，模型每次只能猜，猜错就返工（2026-10-16 至少三次）。
const SERVER_STARTED_AT = new Date();
const SERVER_STARTED_AT_TEXT = SERVER_STARTED_AT.toLocaleString("sv-SE", { timeZone: "Asia/Shanghai" });

// 服务世代记录：落盘到 ~/.fairy/state/server-generation.json
//
// 为什么必须落盘：启动时刻原先只经环境变量传给 agent 进程。可整条服务链
// （dev.mjs → server.cjs → agent）是一起重启的，agent 跟着重启就会继承
// 「新的」启动时刻 —— 基线和现状同时被重置，模型根本无从判断
// 「相对上一轮到底重启没有」。2026-10-24 就是这么踩的：服务被重启后，
// agent 只能靠 ps 对比 pid 去推断。
//
// 落盘之后，磁盘上的记录活过任何进程重启。agent 每次启动读它，
// 就能知道「我这一轮看到的服务，是第几代服务」以及「上一轮看到的是哪一代」。
// 两者不同 = 服务在两轮之间重启过。
const SERVER_GENERATION_FILE = path.join(
  process.env.HOME || os.homedir() || ".",
  ".fairy", "state", "server-generation.json"
);

function readServerGeneration() {
  try {
    const raw = fs.readFileSync(SERVER_GENERATION_FILE, "utf-8");
    const parsed = JSON.parse(raw);
    if (parsed && typeof parsed.generation === "number") return parsed;
  } catch (e) { /* 首次启动：文件还不存在 */ }
  return null;
}

function writeServerGeneration() {
  const prev = readServerGeneration();
  const record = {
    generation: (prev ? prev.generation : 0) + 1,
    startedAtUnix: Math.floor(SERVER_STARTED_AT.getTime() / 1000),
    startedAt: SERVER_STARTED_AT_TEXT,
    pid: process.pid,
  };
  try {
    fs.mkdirSync(path.dirname(SERVER_GENERATION_FILE), { recursive: true });
    fs.writeFileSync(SERVER_GENERATION_FILE, JSON.stringify(record, null, 2), "utf-8");
  } catch (e) {
    // 落盘失败不能挡住服务启动：注入降级成"只知道当前启动时刻"。
    try { console.error("[fairy] 无法写入服务世代记录：", e && e.message); } catch (_) {}
  }
  return record;
}

// reconcileServerGeneration 在每次 spawn agent 前校正世代记录。
//
// 为什么需要这道校正：模块加载时写的那次只覆盖"用新代码启动"的 server。
// 如果 server 是用旧代码起来的（本次升级前就在跑），磁盘上的记录要么没有、
// 要么 pid 对不上 —— 这时必须补登记，否则 agent 会拿一个错误的基线去比对。
//
// 判据用 pid：pid 变了就说明 server 进程换过了，也就是服务重启过。
// 这比时间戳可靠 —— 时间戳会被时钟调整影响，pid 不会。
function reconcileServerGeneration() {
  const prev = readServerGeneration();
  if (prev && prev.pid === process.pid) {
    return prev;   // 同一个 server 进程，没重启
  }
  // pid 对不上：要么是首次登记，要么 server 换过进程（重启过）。
  const record = {
    generation: (prev ? prev.generation : 0) + 1,
    startedAtUnix: Math.floor(SERVER_STARTED_AT.getTime() / 1000),
    startedAt: SERVER_STARTED_AT_TEXT,
    pid: process.pid,
  };
  try {
    fs.mkdirSync(path.dirname(SERVER_GENERATION_FILE), { recursive: true });
    fs.writeFileSync(SERVER_GENERATION_FILE, JSON.stringify(record, null, 2), "utf-8");
  } catch (e) { /* 写不了就退回 env 里的启动时刻 */ }
  return record;
}

let serverGeneration = null;

function buildAgentProcessOptions(repoRoot, options = {}) {
  // 每次 spawn 都校正一次：这样即便 server 是旧代码起来的、模块加载时
  // 没写过记录，也会在第一次 spawn 时补上；server 重启后同理。
  const gen = reconcileServerGeneration();
  serverGeneration = gen;
  return {
    ...options,
    cwd: repoRoot,
    env: withProxyEnv({
      ...process.env,
      AGENT_REPO_ROOT: repoRoot,
      FAIRY_SERVER_STARTED_AT: SERVER_STARTED_AT_TEXT,
      FAIRY_SERVER_STARTED_UNIX: String(Math.floor(SERVER_STARTED_AT.getTime() / 1000)),
      // 服务世代号：agent 拿它和磁盘上的记录对比，判断服务是否重启过。
      FAIRY_SERVER_GENERATION: String(gen.generation),
      FAIRY_SERVER_GENERATION_FILE: SERVER_GENERATION_FILE,
    }),
  };
}

module.exports = {
  buildAgentProcessOptions,
  resolveAgentExecutable,
  withProxyEnv,
  detectLocalProxy,
  readServerGeneration,
  reconcileServerGeneration,
  SERVER_GENERATION_FILE,
};
