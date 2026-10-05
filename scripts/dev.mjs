import { spawn, spawnSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import net from "node:net";
import { closeSync, existsSync, mkdirSync, openSync, readFileSync, renameSync, rmSync, statSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { ensureAgent, repositoryRoot } from "./agent_runtime.mjs";
import { resolvePythonInvocation, withAgentPythonEnvironment } from "./python_runtime.mjs";

const scriptFile = fileURLToPath(import.meta.url);
const serviceStateFilename = "dev-service-state.json";
const instanceLockFilename = "dev-instance.lock";
let heldInstanceLockPath = null;

export function serviceStatePath(repoRoot) {
  return path.join(repoRoot, ".tools", serviceStateFilename);
}

export function writeServiceState(repoRoot, state) {
  const target = serviceStatePath(repoRoot);
  const temporary = `${target}.${process.pid}.tmp`;
  mkdirSync(path.dirname(target), { recursive: true });
  writeFileSync(temporary, `${JSON.stringify(state, null, 2)}\n`, "utf8");
  renameSync(temporary, target);
}

export function removeOwnedServiceState(repoRoot, launcherPid = process.pid) {
  const target = serviceStatePath(repoRoot);
  try {
    const state = JSON.parse(readFileSync(target, "utf8"));
    if (state.launcher?.pid === launcherPid) rmSync(target, { force: true });
  } catch {
    // A missing or superseded state file belongs to another lifecycle.
  }
}

export function resolvePnpmInvocation(env = process.env, platform = process.platform) {
  // pnpm sets npm_execpath when this script is invoked via `pnpm dev`.
  // Running that JS entry with Node avoids Windows .cmd/Powershell handling.
  if (env.npm_execpath) {
    return { command: process.execPath, prefix: [env.npm_execpath] };
  }
  return { command: platform === "win32" ? "pnpm.cmd" : "pnpm", prefix: [] };
}

function run(command, args, options) {
  const result = spawnSync(command, args, { stdio: "inherit", ...options });
  if (result.error) {
    throw new Error(`cannot start ${command}: ${result.error.message}`);
  }
  if (result.status !== 0) {
    throw new Error(`${command} exited with code ${result.status ?? 1}`);
  }
}

function start(command, args, options) {
  const child = spawn(command, args, {
    stdio: "inherit",
    windowsHide: true,
    ...options,
  });
  child.on("error", error => {
    console.error(`[dev] cannot start ${command}: ${error.message}`);
  });
  return child;
}

function terminateChildTree(child) {
  if (!child || child.exitCode !== null || child.killed) return;
  if (process.platform === "win32" && child.pid) {
    try {
      spawnSync("taskkill", ["/PID", String(child.pid), "/T", "/F"], {
        stdio: "ignore",
        timeout: 15_000,
      });
    } catch {}
    return;
  }
  try {
    child.kill("SIGTERM");
  } catch {}
}

function runPnpm(pnpm, args, options) {
  run(pnpm.command, [...pnpm.prefix, ...args], options);
}

function isPortOpen(host, port) {
  return new Promise(resolve => {
    const sock = net.connect({ host, port });
    sock.once("connect", () => { sock.destroy(); resolve(true); });
    sock.once("error", () => resolve(false));
  });
}

function vitePortFromArgs(args, fallback = 5173) {
  for (let i = 0; i < args.length; i++) {
    const arg = String(args[i] || "");
    if (arg === "--port") {
      const parsed = Number(args[i + 1]);
      return Number.isFinite(parsed) && parsed > 0 ? parsed : fallback;
    }
    if (arg.startsWith("--port=")) {
      const parsed = Number(arg.slice("--port=".length));
      return Number.isFinite(parsed) && parsed > 0 ? parsed : fallback;
    }
  }
  return fallback;
}

async function waitForPortOpen(host, port, timeoutMs, child = null) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (child && child.exitCode !== null) return false;
    if (await isPortOpen(host, port)) return true;
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  return false;
}

async function findFreePort(host, preferred, reserved, label) {
  for (let port = preferred; port < preferred + 200; port++) {
    if (reserved.has(port)) continue;
    if (!(await isPortOpen(host, port))) return port;
  }
  throw new Error(`could not find a free ${label} port near ${preferred}`);
}

function withVitePort(args, port) {
  const next = [];
  let replaced = false;
  for (let i = 0; i < args.length; i++) {
    const arg = String(args[i] || "");
    if (arg === "--port") {
      next.push("--port", String(port));
      i++;
      replaced = true;
    } else if (arg.startsWith("--port=")) {
      next.push(`--port=${port}`);
      replaced = true;
    } else {
      next.push(arg);
    }
  }
  if (!replaced) next.push("--port", String(port));
  if (!next.includes("--strictPort")) next.push("--strictPort");
  return next;
}

function isProcessAlive(pid) {
  const value = Number(pid);
  if (!Number.isInteger(value) || value <= 0) return false;
  try {
    process.kill(value, 0);
    return true;
  } catch (error) {
    return error?.code === "EPERM";
  }
}

export function acquireDevInstanceLock(repoRoot) {
  if (heldInstanceLockPath) {
    throw new Error("another Fairy dev instance is already running");
  }
  const target = path.join(repoRoot, ".tools", instanceLockFilename);
  mkdirSync(path.dirname(target), { recursive: true });
  try {
    const fd = openSync(target, "wx");
    writeFileSync(fd, `${JSON.stringify({ pid: process.pid, started_at: new Date().toISOString() }, null, 2)}\n`, "utf8");
    closeSync(fd);
    heldInstanceLockPath = target;
    return true;
  } catch (error) {
    if (error?.code !== "EEXIST") throw error;
  }

  try {
    const owner = JSON.parse(readFileSync(target, "utf8"));
    if (isProcessAlive(owner?.pid) && Number(owner.pid) !== process.pid) {
      throw new Error(`another Fairy dev instance is already running (pid ${owner.pid})`);
    }
  } catch (error) {
    if (String(error?.message || "").includes("already running")) throw error;
  }
  rmSync(target, { force: true });
  return acquireDevInstanceLock(repoRoot);
}

export function releaseDevInstanceLock() {
  if (!heldInstanceLockPath) return;
  try { rmSync(heldInstanceLockPath, { force: true }); } catch {}
  heldInstanceLockPath = null;
}

export function frontendDependenciesReady(frontendDir) {
  const packageFile = path.join(frontendDir, "package.json");
  const lockFile = path.join(frontendDir, "pnpm-lock.yaml");
  const installMarker = path.join(frontendDir, "node_modules", ".modules.yaml");
  try {
    const manifest = JSON.parse(readFileSync(packageFile, "utf8"));
    const dependencies = {
      ...manifest.dependencies,
      ...manifest.devDependencies,
      ...manifest.optionalDependencies,
    };
    for (const name of Object.keys(dependencies)) {
      const packageDir = path.join(frontendDir, "node_modules", ...name.split("/"));
      if (!existsSync(packageDir)) return false;
    }
    if (!existsSync(installMarker)) return false;
    const newestManifest = Math.max(
      statSync(packageFile).mtimeMs,
      existsSync(lockFile) ? statSync(lockFile).mtimeMs : 0,
    );
    return statSync(installMarker).mtimeMs >= newestManifest;
  } catch {
    return false;
  }
}

function ensureFrontendDependencies(repoRoot, pnpm) {
  const frontendDir = path.join(repoRoot, "frontend");
  if (frontendDependenciesReady(frontendDir)) {
    console.log("[2/3] Frontend dependencies already installed");
    return;
  }
  console.log("[2/3] Installing frontend dependencies");
  runPnpm(pnpm, ["--dir", frontendDir, "install", "--frozen-lockfile"], { cwd: repoRoot });
}

function pythonHasModules(candidate, modules) {
  const code = [
    "import importlib.util as u, sys",
    `mods = ${JSON.stringify(modules)}`,
    "raise SystemExit(0 if all(u.find_spec(m) for m in mods) else 1)",
  ].join("; ");
  const result = spawnSync(candidate.command, [...candidate.prefix, "-c", code], {
    stdio: "ignore",
    env: process.env,
  });
  return !result.error && result.status === 0;
}

export function requiredPythonModules(version) {
  const modules = ["requests", "bs4", "PIL"];
  if (version && version.major === 3 && version.minor >= 11 && version.minor < 14) {
    modules.push("cua_auto", "pywinctl");
  }
  return modules;
}

function ensurePythonDependencies(repoRoot, pnpm) {
  let python;
  try {
    python = resolvePythonInvocation({ ...process.env, AGENT_REPO_ROOT: repoRoot });
  } catch {
    console.log("[python] Python runtime missing; running setup:python");
    runPnpm(pnpm, ["setup:python"], { cwd: repoRoot });
    python = resolvePythonInvocation({ ...process.env, AGENT_REPO_ROOT: repoRoot });
  }
  const required = requiredPythonModules(python.version);
  if (pythonHasModules(python, required)) {
    console.log("[python] Dependencies already installed");
    return python;
  }
  console.log(`[python] Missing modules (${required.join(", ")}); installing project dependencies`);
  runPnpm(pnpm, ["setup:python"], { cwd: repoRoot });
  python = resolvePythonInvocation({ ...process.env, AGENT_REPO_ROOT: repoRoot });
  if (!pythonHasModules(python, required)) {
    throw new Error(`Python dependencies are still incomplete after setup: ${required.join(", ")}`);
  }
  return python;
}
function condaPythonCandidates(platform = process.platform) {
  const file = path.join(os.homedir(), ".conda", "environments.txt");
  if (!existsSync(file)) return [];
  try {
    return readFileSync(file, "utf8")
      .split(/\r?\n/)
      .map(line => line.trim())
      .filter(Boolean)
      .map(root => ({
        command: platform === "win32" ? path.join(root, "python.exe") : path.join(root, "bin", "python"),
        prefix: [],
      }))
      .filter(candidate => existsSync(candidate.command));
  } catch {
    return [];
  }
}

function resolveVoicePython(repoRoot, basePython, env = process.env) {
  const requested = [
    env.FAIRY_VOICE_PYTHON?.trim() && { command: env.FAIRY_VOICE_PYTHON.trim(), prefix: [] },
    env.FAIRY_STT_PYTHON?.trim() && { command: env.FAIRY_STT_PYTHON.trim(), prefix: [] },
    basePython,
    ...condaPythonCandidates(),
  ].filter(Boolean);
  const seen = new Set();
  const ttsRequired = ["numpy", "onnxruntime", "sentencepiece", "websockets"];
  const sttRequired = ["faster_whisper"];
  let ttsOnly = null;
  for (const candidate of requested) {
    const key = `${candidate.command}\0${candidate.prefix.join("\0")}`;
    if (seen.has(key)) continue;
    seen.add(key);
    if (!pythonHasModules(candidate, ttsRequired)) continue;
    if (pythonHasModules(candidate, sttRequired)) {
      return { ...candidate, hasStt: true };
    }
    if (!ttsOnly) ttsOnly = { ...candidate, hasStt: false };
  }
  // TTS and STT have independent dependency sets. A missing Faster-Whisper
  // should disable microphone STT, not prevent the HTTP/TTS service from
  // starting on a Python environment that already has the MOSS runtime.
  return ttsOnly || { ...basePython, hasStt: false };
}

export async function main() {
  const repoRoot = repositoryRoot();
  acquireDevInstanceLock(repoRoot);
  rmSync(serviceStatePath(repoRoot), { force: true });
  const instanceId = process.env.FAIRY_INSTANCE_ID?.trim() || `${process.pid}-${randomUUID()}`;
  const frontendDir = path.join(repoRoot, "frontend");
  const pnpm = resolvePnpmInvocation();
  console.log("[1/3] Preparing native Agent");
  const agentPath = ensureAgent(repoRoot, process.env, message => console.log(`[1/3] ${message}`));
  ensureFrontendDependencies(repoRoot, pnpm);

  const env = {
    ...process.env,
    AGENT_LOOP_PATH: agentPath,
    AGENT_REPO_ROOT: repoRoot,
  };
  let python = null;
  try {
    python = ensurePythonDependencies(repoRoot, pnpm);
    Object.assign(env, withAgentPythonEnvironment(env, python));
  } catch (error) {
    console.warn(`[dev] ${error.message}; Python tools and voice will be unavailable until dependency setup succeeds.`);
  }
  const host = process.env.AGENT_DEV_HOST || "127.0.0.1";

  // Vite is launched with an explicit `--host`, and a CLI flag beats
  // `server.host` in vite.config.js. So the bind address has to be decided here -
  // setting it only in the config file silently does nothing, which is how a
  // "bind to the LAN" change looked applied while the socket stayed on loopback.
  //
  // `host` above stays loopback on purpose: it is the address used to probe for
  // free ports and to wait for readiness, and 0.0.0.0 is not a connectable
  // target.
  const lanBind = String(process.env.FAIRY_LAN_BIND || "").trim();
  const viteHost = lanBind === "1" || lanBind === "0.0.0.0" ? "0.0.0.0" : lanBind || host;
  const preferredApiPort = Number(process.env.AGENT_API_PORT || "8081");
  const preferredVoicePort = Number(process.env.FAIRY_VOICE_PORT || "8787");
  const preferredVoiceWsPort = Number(process.env.FAIRY_VOICE_WS_PORT || String(preferredVoicePort + 1));
  const preferredFrontendPort = vitePortFromArgs(process.argv.slice(2));
  const reservedPorts = new Set();
  const apiPort = await findFreePort(host, preferredApiPort, reservedPorts, "API");
  reservedPorts.add(apiPort);
  const frontendPort = await findFreePort(host, preferredFrontendPort, reservedPorts, "frontend");
  reservedPorts.add(frontendPort);
  const voicePort = await findFreePort(host, preferredVoicePort, reservedPorts, "voice");
  reservedPorts.add(voicePort);
  const voiceWsPort = await findFreePort(host, preferredVoiceWsPort, reservedPorts, "voice WebSocket");
  reservedPorts.add(voiceWsPort);
  const viteArgs = withVitePort(process.argv.slice(2), frontendPort);
  Object.assign(env, {
    AGENT_API_PORT: String(apiPort),
    FAIRY_FRONTEND_PORT: String(frontendPort),
    FAIRY_VOICE_PORT: String(voicePort),
    FAIRY_VOICE_WS_PORT: String(voiceWsPort),
    FAIRY_INSTANCE_ID: instanceId,
    FAIRY_REPO_ROOT: repoRoot,
  });

  for (const [label, preferred, selected] of [
    ["API", preferredApiPort, apiPort],
    ["frontend", preferredFrontendPort, frontendPort],
    ["voice", preferredVoicePort, voicePort],
    ["voice WebSocket", preferredVoiceWsPort, voiceWsPort],
  ]) {
    if (selected !== preferred) console.warn(`[dev] ${label} port ${preferred} busy; using ${selected}`);
  }

  console.log(`[3/3] Starting API server on http://127.0.0.1:${apiPort}`);
  const apiStartedAt = new Date().toISOString();
  const api = start(process.execPath, [path.join(frontendDir, "server.cjs"), apiPort], { cwd: repoRoot, env });
  if (!(await waitForPortOpen(host, Number(apiPort), 15_000, api))) {
    terminateChildTree(api);
    throw new Error(`API server did not bind ${host}:${apiPort}`);
  }
  console.log(`Frontend: http://${host}:${frontendPort}`);
  const viteStartedAt = new Date().toISOString();
  const vite = start(process.execPath, [path.join(frontendDir, "node_modules", "vite", "bin", "vite.js"), "--host", viteHost, ...viteArgs], { cwd: frontendDir, env });

  let voice = null;
  let voiceStartedAt = null;
  if (!python) {
    console.warn("[dev] voice service skipped: no usable Python runtime");
  } else {
    const voicePython = resolveVoicePython(repoRoot, python, env);
    if (voicePython.command !== python.command) {
      console.log(`[dev] Voice runtime: ${voicePython.command}`);
    }
    if (!voicePython.hasStt) {
      console.warn("[dev] STT unavailable: set FAIRY_VOICE_PYTHON to a Python env with faster-whisper");
    }
    voiceStartedAt = new Date().toISOString();
    voice = start(voicePython.command, [...voicePython.prefix, path.join(repoRoot, "voice", "voice_service.py")], { cwd: repoRoot, env: withAgentPythonEnvironment(env, voicePython) });
    console.log(`Voice service: http://${host}:${voicePort} (pid ${voice.pid})`);
  }

  writeServiceState(repoRoot, {
    version: 3,
    instance_id: instanceId,
    repository_root: repoRoot,
    launcher: { pid: process.pid, started_at: new Date(Date.now() - process.uptime() * 1000).toISOString() },
    api: { pid: api.pid, port: apiPort, started_at: apiStartedAt },
    frontend: { pid: vite.pid, port: frontendPort, url: `http://${host}:${frontendPort}`, started_at: viteStartedAt },
    ...(voice ? { voice: { pid: voice.pid, port: voicePort, ws_port: voiceWsPort, started_at: voiceStartedAt } } : {}),
  });

  let shuttingDown = false;
  const stop = (exitCode = 0) => {
    if (shuttingDown) return;
    shuttingDown = true;
    for (const child of [vite, api, voice]) {
      terminateChildTree(child);
    }
    removeOwnedServiceState(repoRoot);
    releaseDevInstanceLock();
    process.exitCode = exitCode;
  };

  api.on("exit", code => {
    if (!shuttingDown) {
      console.error(`[dev] API server exited unexpectedly (${code ?? "signal"})`);
      stop(code || 1);
    }
  });
  vite.on("exit", code => stop(code || 0));
  if (voice) {
    voice.on("exit", code => {
      if (!shuttingDown) console.error(`[dev] voice service exited unexpectedly (${code ?? "signal"})`);
    });
  }
  process.once("SIGINT", () => stop(0));
  process.once("SIGTERM", () => stop(0));
}

if (path.resolve(process.argv[1] || "") === scriptFile) {
  main().catch(error => {
    releaseDevInstanceLock();
    console.error(`[dev] ${error.message}`);
    process.exitCode = 1;
  });
}
