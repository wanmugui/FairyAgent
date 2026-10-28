/**
 * Check the toolchain before the launcher starts building anything.
 *
 * Without this the first sign of a missing tool is an error from deep inside a
 * build: "go build exited with code 1", or "AGENT_LOOP_PATH does not point to
 * a file", neither of which says what to do. This turns each missing tool into
 * one line naming the tool, the version and where to get it.
 *
 * Warnings do not stop the start; only a missing tool does, and only when the
 * app genuinely cannot run without it.
 */
import { spawnSync } from "node:child_process";

/**
 * Run a command and return its trimmed stdout, or null when it cannot run.
 * A non-zero exit still counts as "installed" — the version is unreadable but
 * the tool exists, which is not what this check is about.
 */
function probe(command, args) {
  const result = spawnSync(command, args, {
    encoding: "utf8",
    timeout: 15_000,
    windowsHide: true,
  });
  if (result.error) return null;
  return `${result.stdout || ""}${result.stderr || ""}`.trim();
}

/** First number in a version string, e.g. "go1.23.2" -> 1.23.2 */
function parseVersion(text) {
  const match = /(\d+)\.(\d+)(?:\.(\d+))?/.exec(String(text || ""));
  if (!match) return null;
  return [Number(match[1]), Number(match[2]), Number(match[3] || 0)];
}

/** True when actual >= required, comparing each component. */
function atLeast(actual, required) {
  if (!actual) return false;
  for (let i = 0; i < required.length; i++) {
    if ((actual[i] || 0) > required[i]) return true;
    if ((actual[i] || 0) < required[i]) return false;
  }
  return true;
}

/** Whether this platform is expected to have python3 in a specific name. */
function pythonCandidates() {
  return process.platform === "win32" ? ["python", "python3"] : ["python3", "python"];
}

export function toolchainPnpmInvocation(env = process.env, platform = process.platform) {
  if (env.npm_execpath) {
    return { command: process.execPath, args: [env.npm_execpath, "--version"] };
  }
  return {
    command: platform === "win32" ? "pnpm.cmd" : "pnpm",
    args: ["--version"],
  };
}

/**
 * Inspect the toolchain.
 *
 * @returns {{ok: boolean, tools: Array, missing: Array}}
 */
export function checkToolchain() {
  const tools = [];

  // node — the launcher itself is running, so this only reports the version.
  const nodeVersion = parseVersion(process.versions.node);
  tools.push({
    id: "node",
    label: "Node.js",
    required: [22, 5, 0],
    found: process.versions.node,
    installed: true,
    ok: atLeast(nodeVersion, [22, 5, 0]),
    hint: "https://nodejs.org/",
  });

  // pnpm — the frontend install depends on it.
  const pnpmInvocation = toolchainPnpmInvocation();
  const pnpmText = probe(pnpmInvocation.command, pnpmInvocation.args);
  const pnpmVersion = parseVersion(pnpmText);
  tools.push({
    id: "pnpm",
    label: "pnpm",
    required: [9, 0, 0],
    found: pnpmText || "",
    installed: !!pnpmText,
    ok: !!pnpmText,
    hint: "npm install -g pnpm",
  });

  // go — the agent binary is built from source on first start.
  const goText = probe("go", ["version"]);
  const goVersion = parseVersion(goText);
  tools.push({
    id: "go",
    label: "Go",
    required: [1, 24, 0],
    found: goText || "",
    installed: !!goText,
    ok: !!goText && atLeast(goVersion, [1, 24, 0]),
    hint: "https://go.dev/dl/",
  });

  // python — the voice engines and the tool gateway run on it.
  let pythonInstalled = false;
  let pythonFound = "";
  for (const candidate of pythonCandidates()) {
    const text = probe(candidate, ["--version"]);
    if (text) {
      pythonInstalled = true;
      pythonFound = `${candidate} (${text})`;
      break;
    }
  }
  tools.push({
    id: "python",
    label: "Python",
    required: [3, 10, 0],
    found: pythonFound,
    installed: pythonInstalled,
    // Python is optional: without it the voice features and the tool gateway
    // are unavailable, but chat and everything else still runs.
    optional: true,
    ok: pythonInstalled,
    hint: "https://www.python.org/downloads/",
  });

  const missing = tools.filter((tool) => !tool.ok);
  return { ok: missing.every((tool) => tool.optional), tools, missing };
}

/** Print a readable report. Returns the same shape as checkToolchain(). */
export function reportToolchain() {
  const result = checkToolchain();
  console.log("[toolchain] 运行环境检查");
  for (const tool of result.tools) {
    const mark = tool.ok ? "✓" : tool.optional ? "!" : "✗";
    const version = tool.found ? ` ${tool.found}` : "";
    console.log(`  ${mark} ${tool.label}${version}${tool.optional ? "（可选）" : ""}`);
  }
  return result;
}

/**
 * Stop the start when a required tool is missing.
 *
 * @returns {boolean} true when the start may continue
 */
export function requireToolchain() {
  const result = checkToolchain();

  const broken = result.missing.filter((tool) => !tool.optional);
  if (broken.length) {
    console.error("");
    console.error("缺少必需的环境，无法启动：");
    for (const tool of broken) {
      const need = tool.required.join(".");
      console.error(`  · ${tool.label} ${need} 或更高版本 —— ${tool.hint}`);
    }
    console.error("");
    console.error("装好之后重新执行启动命令即可。");
    console.error("");
    return false;
  }

  const optionalMissing = result.missing.filter((tool) => tool.optional);
  if (optionalMissing.length) {
    const names = optionalMissing.map((tool) => tool.label).join("、");
    console.log(`[toolchain] 未检测到 ${names}，语音与工具网关将不可用，其余功能正常。`);
  }

  return true;
}

// Direct invocation: `node scripts/check-toolchain.mjs`
if (process.argv[1] && process.argv[1].endsWith("check-toolchain.mjs")) {
  const result = reportToolchain();
  const broken = result.missing.filter((tool) => !tool.optional);
  process.exit(broken.length ? 1 : 0);
}
