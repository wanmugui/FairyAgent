import { spawnSync } from "node:child_process";
import path from "node:path";

export const MINIMUM_PYTHON = Object.freeze({ major: 3, minor: 10 });

export function inspectPython(command, prefix = []) {
  const result = spawnSync(command, [...prefix, "-c", "import sys; print(f'{sys.version_info.major}.{sys.version_info.minor}')"], {
    encoding: "utf8",
    stdio: ["ignore", "pipe", "ignore"],
  });
  if (result.error || result.status !== 0) return null;
  const match = String(result.stdout).trim().match(/^(\d+)\.(\d+)$/);
  if (!match) return null;
  return { major: Number(match[1]), minor: Number(match[2]) };
}

export function supportsMinimumPython(version) {
  return version !== null && (version.major > MINIMUM_PYTHON.major ||
    (version.major === MINIMUM_PYTHON.major && version.minor >= MINIMUM_PYTHON.minor));
}

function selectPython(command, prefix, inspect) {
  const version = inspect(command, prefix);
  return supportsMinimumPython(version) ? { command, prefix, version } : null;
}

export function resolvePythonInvocation(env = process.env, platform = process.platform, inspect = inspectPython) {
  const configured = env.AGENT_PYTHON_BIN?.trim();
  if (configured) {
    const selected = selectPython(configured, [], inspect);
    if (!selected) {
      throw new Error(`AGENT_PYTHON_BIN must provide Python ${MINIMUM_PYTHON.major}.${MINIMUM_PYTHON.minor}+: ${configured}`);
    }
    return selected;
  }

  const projectPython = projectPythonPath(env.AGENT_REPO_ROOT || "", platform);
  if (projectPython) {
    const selected = selectPython(projectPython, [], inspect);
    if (selected) return selected;
  }
  const candidates = platform === "win32"
    ? [{ command: "py", prefix: ["-3"] }, { command: "python", prefix: [] }]
    : [{ command: "python", prefix: [] }, { command: "python3", prefix: [] }];
  for (const candidate of candidates) {
    const selected = selectPython(candidate.command, candidate.prefix, inspect);
    if (selected) return selected;
  }
  throw new Error(`Python ${MINIMUM_PYTHON.major}.${MINIMUM_PYTHON.minor}+ is required; set AGENT_PYTHON_BIN or install a supported Python`);
}

export function projectPythonPath(repoRoot, platform = process.platform) {
  if (!repoRoot) return "";
  if (platform === "win32") {
    return path.join(repoRoot, ".tools", "venv", "Scripts", "python.exe");
  }
  return path.join(repoRoot, ".tools", "venv", "bin", "python");
}

export function projectPythonAvailable(repoRoot, platform = process.platform, inspect = inspectPython) {
  const python = projectPythonPath(repoRoot, platform);
  return python !== "" && supportsMinimumPython(inspect(python, []));
}

export function withAgentPythonEnvironment(env, python) {
  if (env.AGENT_PYTHON_BIN || python.prefix.length > 0) return { ...env };
  return { ...env, AGENT_PYTHON_BIN: python.command };
}
