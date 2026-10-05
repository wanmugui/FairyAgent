import { spawnSync } from "node:child_process";
import { existsSync, statSync } from "node:fs";
import path from "node:path";

import { repositoryRoot } from "./agent_runtime.mjs";
import { projectPythonPath, resolvePythonInvocation, supportsMinimumPython, inspectPython } from "./python_runtime.mjs";

function isRegularFile(filePath) {
  try {
    return statSync(filePath).isFile();
  } catch {
    return false;
  }
}

function run(command, args, cwd) {
  const result = spawnSync(command, args, { cwd, stdio: "inherit" });
  if (result.error) throw new Error(`cannot start ${command}: ${result.error.message}`);
  if (result.status !== 0) throw new Error(`${command} exited with code ${result.status ?? 1}`);
}

try {
  const repoRoot = repositoryRoot();
  const platform = process.platform;
  const venvPython = projectPythonPath(repoRoot, platform);
  if (!isRegularFile(venvPython) || !supportsMinimumPython(inspectPython(venvPython))) {
    // Do not use an old project venv as its own source. An explicit override
    // or the active shell environment remains the reproducible bootstrap.
    const base = resolvePythonInvocation({ ...process.env, AGENT_REPO_ROOT: "" }, platform);
    console.log(`[python] Creating project venv with ${base.command} (${base.version.major}.${base.version.minor})`);
    run(base.command, [...base.prefix, "-m", "venv", "--clear", path.join(repoRoot, ".tools", "venv")], repoRoot);
  } else {
    console.log(`[python] Reusing project venv: ${venvPython}`);
  }
  const lockFile = existsSync(path.join(repoRoot, "requirements.lock")) ? "requirements.lock" : "requirements.txt";
  console.log(`[python] Installing locked dependencies from ${lockFile}`);
  run(venvPython, ["-m", "pip", "install", "--disable-pip-version-check", "-r", lockFile], repoRoot);
  const venvVersion = inspectPython(venvPython);
  if (venvVersion && venvVersion.major === 3 && venvVersion.minor >= 11 && venvVersion.minor < 14) {
    run(venvPython, ["-c", "import requests, bs4, cua_auto, pywinctl; from PIL import Image; print('Python environment ready (desktop automation enabled)')"], repoRoot);
  } else {
    run(venvPython, ["-c", "import requests, bs4; from PIL import Image; print('Python environment ready (desktop automation requires Python 3.11-3.13)')"], repoRoot);
  }
  console.log(`[python] Use ${venvPython}; pnpm dev and scripts/python.mjs will select it automatically.`);
} catch (error) {
  console.error(`[python] ${error.message}`);
  process.exitCode = 1;
}
