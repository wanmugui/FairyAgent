import { spawnSync } from "node:child_process";
import { mkdirSync, readdirSync, statSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));

export function repositoryRoot() {
  return path.resolve(scriptDir, "..");
}

export function agentExecutablePath(repoRoot, platform = process.platform, arch = process.arch) {
  const suffix = platform === "win32" ? ".exe" : "";
  return path.join(repoRoot, ".tools", `agent-loop-${platform}-${arch}${suffix}`);
}

export function goCommand(env = process.env) {
  return env.AGENT_GO_BIN || "go";
}

export function agentBuildEnvironment(repoRoot, env = process.env) {
  return {
    ...env,
    GOCACHE: env.GOCACHE || path.join(repoRoot, ".tools", "go-build-cache"),
  };
}

function isRegularFile(filePath) {
  try {
    return statSync(filePath).isFile();
  } catch {
    return false;
  }
}

function newestAgentSourceMtime(agentDir) {
  let newest = 0;
  let entries = [];
  try {
    entries = readdirSync(agentDir, { withFileTypes: true });
  } catch {
    return newest;
  }
  for (const entry of entries) {
    const entryPath = path.join(agentDir, entry.name);
    if (entry.isDirectory()) {
      newest = Math.max(newest, newestAgentSourceMtime(entryPath));
      continue;
    }
    if (!entry.isFile()) continue;
    if (!(entry.name.endsWith(".go") || entry.name === "go.mod" || entry.name === "go.sum")) continue;
    try {
      newest = Math.max(newest, statSync(entryPath).mtimeMs);
    } catch {
      // A source file can disappear during an editor save. The next restart
      // will observe the stable tree and decide whether a rebuild is needed.
    }
  }
  return newest;
}

export function agentBinaryIsCurrent(repoRoot, output = agentExecutablePath(repoRoot)) {
  try {
    const binaryMtime = statSync(output).mtimeMs;
    const sourceMtime = newestAgentSourceMtime(path.join(repoRoot, "agent"));
    return sourceMtime > 0 && binaryMtime >= sourceMtime;
  } catch {
    return false;
  }
}

export function ensureAgent(repoRoot, env = process.env, report = console.error) {
  const output = env.AGENT_LOOP_PATH || agentExecutablePath(repoRoot);
  if (env.AGENT_LOOP_PATH && !isRegularFile(output)) {
    throw new Error(`AGENT_LOOP_PATH does not point to a file: ${output}`);
  }
  if (env.AGENT_LOOP_PATH) {
    report(`Using configured Agent: ${output}`);
    return output;
  }
  mkdirSync(path.dirname(output), { recursive: true });
  if (env.FAIRY_AGENT_FORCE_BUILD !== "1" && agentBinaryIsCurrent(repoRoot, output)) {
    report(`Using up-to-date Agent: ${path.basename(output)}`);
    return output;
  }
  report(`Building native Agent: ${path.basename(output)}`);
  const command = goCommand(env);
  const buildEnv = agentBuildEnvironment(repoRoot, env);
  mkdirSync(buildEnv.GOCACHE, { recursive: true });
  const result = spawnSync(command, ["build", "-o", output], {
    cwd: path.join(repoRoot, "agent"),
    env: buildEnv,
    stdio: "inherit",
  });
  if (result.error) {
    throw new Error(`cannot start Go runtime ${command}: ${result.error.message}`);
  }
  if (result.status !== 0) {
    throw new Error(`go build exited with code ${result.status ?? 1}`);
  }
  if (!isRegularFile(output)) {
    throw new Error(`Agent build did not produce a regular file: ${output}`);
  }
  return output;
}
