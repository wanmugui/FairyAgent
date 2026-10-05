const fs = require("fs");
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

function buildAgentProcessOptions(repoRoot, options = {}) {
  return {
    ...options,
    cwd: repoRoot,
    env: { ...process.env, AGENT_REPO_ROOT: repoRoot },
  };
}

module.exports = { buildAgentProcessOptions, resolveAgentExecutable };
