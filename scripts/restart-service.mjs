// 重启 Fairy 的前后端服务（vite + API + Go agent + voice），不碰桌面壳本身。
//
// 纯 Node 实现，Windows 与类 Unix 通吃：不用 .ps1，也不 shell 到 PowerShell。
// 端口->PID 探测在 Windows 走 netstat -ano，在 Unix 走 lsof；
// 进程类型在 Windows 走 tasklist，在 Unix 走 ps。
//
// 与 scripts/dev.mjs 的分工：dev.mjs 负责把服务拉起来并记录 .tools/dev-service-state.json，
// 本脚本负责按那份状态把旧服务停干净，再交回 dev.mjs 重新拉起。

import { execFile, spawn, spawnSync } from "node:child_process";
import { closeSync, existsSync, mkdirSync, openSync, readFileSync, readdirSync, rmSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);
const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const isWindows = process.platform === "win32";
const toolsDir = path.join(repoRoot, ".tools");
const stateFile = path.join(toolsDir, "dev-service-state.json");
const stdoutLog = path.join(toolsDir, "dev-service.stdout.log");
const stderrLog = path.join(toolsDir, "dev-service.stderr.log");

// 5173/8081 只是本仓库早期的默认端口，仅在没有状态文件时用于兜底探测。
// 有状态文件时必须以状态文件为准：否则别的环境（例如 WSL 里的旧仓库 relay）
// 占着 5173/8081 时会被判成「不属于本项目的监听者」，导致重启直接中断。
const legacyFallbackPorts = [5173, 8081];
// if Go sources changed, dev.mjs rebuilds the native agent before startup.
// Keep enough time for a cold rebuild, while ensureAgent skips unchanged builds.
const healthTimeoutMs = 180_000;
const portReleaseTimeoutMs = 15_000;

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const log = (message) => console.log(`[restart] ${message}`);

async function run(command, args, { allowFailure = false } = {}) {
  try {
    const { stdout, stderr } = await execFileAsync(command, args, {
      encoding: "utf8",
      windowsHide: true,
      maxBuffer: 8 * 1024 * 1024,
    });
    return { stdout, stderr, code: 0 };
  } catch (error) {
    if (!allowFailure) {
      throw new Error(`${command} ${args.join(" ")} failed: ${error.stderr || error.message}`);
    }
    return { stdout: error.stdout ?? "", stderr: error.stderr ?? "", code: error.code ?? 1 };
  }
}

function readState() {
  let raw;
  try {
    raw = readFileSync(stateFile, "utf8");
  } catch (error) {
    if (error?.code === "ENOENT") return null;
    throw error;
  }
  const state = JSON.parse(raw);
  // 状态文件来自另一个仓库时不能拿它当杀进程的依据。
  if (state?.repository_root && path.resolve(state.repository_root) !== repoRoot) {
    throw new Error(`服务状态属于其它仓库，拒绝操作：${state.repository_root}`);
  }
  return state;
}

function statePorts(state) {
  const ports = [];
  for (const entry of [state?.api, state?.frontend, state?.webui, state?.voice]) {
    for (const value of [entry?.port, entry?.ws_port]) {
      const port = Number(value);
      if (Number.isInteger(port) && port > 0 && port < 65536) ports.push(port);
    }
  }
  return ports;
}

function recordedPids(state) {
  const pids = new Set();
  for (const entry of [state?.launcher, state?.api, state?.frontend, state?.webui, state?.voice]) {
    const pid = Number(entry?.pid);
    if (Number.isInteger(pid) && pid > 0) pids.add(pid);
  }
  return pids;
}

// 返回 Map<端口, Set<PID>>，只统计处于 LISTEN 状态的 TCP。
async function listeningPortMap(ports) {
  const wanted = new Set(ports);
  const found = new Map();
  const add = (port, pid) => {
    if (!found.has(port)) found.set(port, new Set());
    found.get(port).add(pid);
  };

  if (isWindows) {
    const { stdout } = await run("netstat", ["-ano"], { allowFailure: true });
    for (const line of stdout.split(/\r?\n/)) {
      const fields = line.trim().split(/\s+/);
      if (fields.length < 5 || !/^TCP$/i.test(fields[0]) || !/^LISTENING$/i.test(fields[3])) continue;
      const local = fields[1];
      const port = Number(local.slice(local.lastIndexOf(":") + 1));
      if (!wanted.has(port)) continue;
      const pid = Number(fields[4]);
      if (Number.isInteger(pid) && pid > 0 && pid !== 0) add(port, pid);
    }
    return found;
  }

  for (const port of ports) {
    const { stdout } = await run("lsof", ["-nP", `-iTCP:${port}`, "-sTCP:LISTEN", "-t"], { allowFailure: true });
    for (const token of stdout.split(/\s+/)) {
      const pid = Number(token);
      if (Number.isInteger(pid) && pid > 0) add(port, pid);
    }
  }
  return found;
}

async function processName(pid) {
  if (isWindows) {
    const { stdout } = await run("tasklist", ["/FI", `PID eq ${pid}`, "/FO", "CSV", "/NH"], { allowFailure: true });
    const match = stdout.match(/^\s*"([^"]+)"/m);
    return match ? match[1].toLowerCase() : "";
  }
  const { stdout } = await run("ps", ["-p", String(pid), "-o", "comm="], { allowFailure: true });
  return stdout.trim().toLowerCase();
}

function isAlive(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    // EPERM 说明进程存在，只是不归当前用户管。
    return error?.code === "EPERM";
  }
}

// 只有 node / python 进程才可能是本项目自己的服务。
// 这条判断是防止误杀的最后一道闸：一个 nginx.exe 占着 5173 就该报错，而不是被清掉。
const isOwnServiceProcess = (name) => /node|python/i.test(name);

// 收集要停的进程。已登记在状态文件里的直接认；未登记但确实是 node/python
// 且占着本项目端口的，视为上次没收干净的残留（例如换地址族后遗留的 vite）。
async function collectTargets(ports, softPorts = new Set()) {
  const state = readState();
  const registered = recordedPids(state);
  const targets = new Set();
  const foreign = [];

  for (const [port, pids] of await listeningPortMap(ports)) {
    for (const pid of pids) {
      if (registered.has(pid)) {
        targets.add(pid);
        continue;
      }
      const name = await processName(pid);
      if (isOwnServiceProcess(name)) {
        log(`端口 ${port} 上是未登记的残留 ${name || "进程"} (pid ${pid})，一并停掉`);
        targets.add(pid);
      } else if (softPorts.has(port)) {
        log(`端口 ${port} 被非本项目进程占用，按兼容端口跳过：${name || "未知进程"} (pid ${pid})`);
      } else {
        foreign.push(`端口 ${port} <- pid ${pid} (${name || "未知进程"})`);
      }
    }
  }

  // 登记过但不监听任何端口的进程同样要停 —— launcher (dev.mjs) 就是这种：
  // 它握着 .tools 里的 dev 实例锁，留着会让新起的 dev.mjs 直接拒绝启动。
  for (const pid of registered) {
    if (targets.has(pid) || !isAlive(pid)) continue;
    const name = await processName(pid);
    if (isOwnServiceProcess(name)) {
      log(`停止状态文件登记的进程 (pid ${pid} ${name})`);
      targets.add(pid);
    }
  }

  if (foreign.length > 0) {
    throw new Error(`以下监听者不属于本项目，拒绝停止：\n  ${foreign.join("\n  ")}`);
  }
  return [...targets];
}

async function stopProcesses(pids) {
  for (const pid of pids) {
    if (isWindows) {
      // /T 连同子进程一起收：launcher 被停掉时，它的 api/vite/voice 子树也会跟着走。
      await run("taskkill", ["/PID", String(pid), "/T", "/F"], { allowFailure: true });
    } else {
      try {
        process.kill(pid, "SIGTERM");
      } catch {
        // 进程可能刚好自己退了。
      }
    }
  }
}

async function waitForPortsReleased(ports) {
  const deadline = Date.now() + portReleaseTimeoutMs;
  while (Date.now() < deadline) {
    const map = await listeningPortMap(ports);
    if ([...map.values()].every((pids) => pids.size === 0)) return;
    await sleep(300);
  }
  const map = await listeningPortMap(ports);
  const remaining = [...map.entries()].filter(([, pids]) => pids.size > 0);
  throw new Error(`端口未被释放：${remaining.map(([port, pids]) => `${port}(${[...pids].join(",")})`).join(" ")}`);
}

// AGENT_GO_BIN 优先，其次 .tools 下带版本号的 Go，最后交给 PATH。
function resolveGoCommand() {
  const candidates = [];
  if (process.env.AGENT_GO_BIN) candidates.push(process.env.AGENT_GO_BIN);
  try {
    for (const name of readdirSync(toolsDir).filter((entry) => /^go/i.test(entry)).sort().reverse()) {
      candidates.push(path.join(toolsDir, name, "go", "bin", isWindows ? "go.exe" : "go"));
    }
  } catch {
    // .tools 还不存在时忽略，后面的 PATH 兜底。
  }
  candidates.push(isWindows ? "go.exe" : "go");

  for (const candidate of candidates) {
    if (path.isAbsolute(candidate) && !existsSync(candidate)) continue;
    const probe = spawnSync(candidate, ["version"], { stdio: "ignore", windowsHide: true });
    if (!probe.error && probe.status === 0) return candidate;
  }
  return null;
}

function launch() {
  mkdirSync(toolsDir, { recursive: true });
  const stdout = openSync(stdoutLog, "a");
  const stderr = openSync(stderrLog, "a");

  const env = { ...process.env };
  // dev.mjs 里的 ensureAgent 一旦发现 AGENT_LOOP_PATH 被继承，就会把它当成
  // 「外部二进制」并跳过构建 —— 结果就是改完 Go 代码重启后仍跑旧逻辑。
  delete env.AGENT_LOOP_PATH;
  const go = resolveGoCommand();
  if (go) env.AGENT_GO_BIN = go;
  // 让 dev.mjs 及其子进程都用当前这个 Node，避免 PATH 里是另一个版本。
  env.PATH = `${path.dirname(process.execPath)}${path.delimiter}${env.PATH || ""}`;

  const child = spawn(process.execPath, [path.join(repoRoot, "scripts", "dev.mjs")], {
    cwd: repoRoot,
    detached: true,
    stdio: ["ignore", stdout, stderr],
    env,
    windowsHide: true,
  });
  child.unref();
  closeSync(stdout);
  closeSync(stderr);
  return child;
}

async function waitForFreshState(previousInstanceId, deadline) {
  while (Date.now() < deadline) {
    const state = readState();
    if (state?.instance_id && state.instance_id !== previousInstanceId) return state;
    await sleep(500);
  }
  return null;
}

async function waitForHttpOk(url, deadline) {
  let lastError = "无响应";
  while (Date.now() < deadline) {
    try {
      const response = await fetch(url, { signal: AbortSignal.timeout(3000) });
      if (response.ok) return;
      lastError = `HTTP ${response.status}`;
    } catch (error) {
      lastError = error?.message || String(error);
    }
    await sleep(500);
  }
  throw new Error(`健康检查超时：${url}（${lastError}）`);
}

async function main() {
  // --stop 只停不启：应用退出时用它收尾，也方便单独验证「要停哪些进程」。
  const stopOnly = process.argv.includes("--stop");
  // --dry-run 只报告不停：可以在不打断当前会话的前提下核对判定结果。
  const dryRun = process.argv.includes("--dry-run");
  const previous = readState();
  const previousInstanceId = previous?.instance_id ?? null;
  // 端口集合：优先用状态文件记录的端口（含 voice / webui）；只有在没有状态文件时
  // 才退回早期默认端口。默认端口一律按「软端口」处理 —— 被别的环境（如 WSL relay）
  // 占着就跳过，不因此中断重启。
  const recordedPorts = statePorts(previous);
  const softPorts = new Set(previous ? [] : legacyFallbackPorts);
  const ports = [...new Set([...recordedPorts, ...softPorts])];
  const hardPorts = ports.filter((port) => !softPorts.has(port));

  const targets = await collectTargets(ports, softPorts);
  if (dryRun) {
    log(`涉及端口：${ports.join(", ")}`);
    log(`将停止 ${targets.length} 个进程：${targets.length ? targets.join(", ") : "（无）"}`);
    return;
  }
  if (targets.length > 0) {
    log(`停止旧服务进程：${targets.join(", ")}`);
    await stopProcesses(targets);
    await waitForPortsReleased(ports);
  } else {
    log(stopOnly ? "没有正在运行的项目服务" : "没有发现正在运行的项目服务，直接启动");
  }
  rmSync(stateFile, { force: true });
  if (stopOnly) return;

  log("启动新的前后端服务…");
  launch();

  const deadline = Date.now() + healthTimeoutMs;
  // 端口可能被让给别人，所以以新写出的状态文件为准，而不是假定 5173/8081。
  const next = await waitForFreshState(previousInstanceId, deadline);
  const frontendUrl = next?.frontend?.port ? `http://127.0.0.1:${next.frontend.port}` : "http://127.0.0.1:5173";
  const apiUrl = next?.api?.port ? `http://127.0.0.1:${next.api.port}/api/models` : "http://127.0.0.1:8081/api/models";

  await waitForHttpOk(frontendUrl, deadline);
  await waitForHttpOk(apiUrl, deadline);

  log("服务已重启");
  log(`前端：${frontendUrl}`);
  log(`接口：${apiUrl}`);
  log(`日志：${stdoutLog}`);
}

main().catch((error) => {
  console.error(`[restart] ${error.message}`);
  process.exitCode = 1;
});
