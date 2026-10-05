import { spawnSync } from "node:child_process";
import { mkdirSync } from "node:fs";
import path from "node:path";

import { repositoryRoot } from "./agent_runtime.mjs";
import { resolvePnpmInvocation } from "./dev.mjs";

function run(label, command, args, options = {}) {
  console.log(`\n[check] ${label}`);
  const result = spawnSync(command, args, { cwd: repositoryRoot(), stdio: "inherit", ...options });
  if (result.error) throw new Error(`${label}: cannot start ${command}: ${result.error.message}`);
  if (result.status !== 0) throw new Error(`${label}: exited with code ${result.status ?? 1}`);
}

function runPnpm(label, args) {
  const pnpm = resolvePnpmInvocation();
  run(label, pnpm.command, [...pnpm.prefix, ...args]);
}

try {
  const repoRoot = repositoryRoot();
  mkdirSync(path.join(repoRoot, ".tools"), { recursive: true });
  runPnpm("Node launcher tests", ["test:launcher"]);
  run("Frontend agent-path tests", process.execPath, ["--test", "frontend/agent_executable.test.cjs"]);
  run("Agent race tests", "go", ["-C", "agent", "test", "./...", "-race"]);
  run("Windows Agent cross-build", "go", ["-C", "agent", "build", "-o", path.join(repoRoot, ".tools", "ci-agent-loop-windows.exe"), "."], {
    env: { ...process.env, GOOS: "windows", GOARCH: "amd64" },
  });
  run("Linux Agent cross-build", "go", ["-C", "agent", "build", "-o", path.join(repoRoot, ".tools", "ci-agent-loop-linux-amd64"), "."], {
    env: { ...process.env, GOOS: "linux", GOARCH: "amd64" },
  });
  runPnpm("Native Agent cache build", ["agent:build"]);
  runPnpm("Reproducible Python setup", ["setup:python"]);
  runPnpm("Python capability smoke", ["test:python"]);
  run("Python source syntax", process.execPath, ["scripts/python.mjs", "-c", "import pathlib, py_compile; files = {p for root in (pathlib.Path('skills'), pathlib.Path('workspace/skills'), pathlib.Path('tool_gateway')) for p in root.rglob('*.py')}; [py_compile.compile(str(p), doraise=True) for p in sorted(files)]; print(f'Compiled {len(files)} Python files')"]);
  runPnpm("Frontend build", ["--dir", "frontend", "build"]);
  console.log("\n[check] Cross-platform checks passed.");
} catch (error) {
  console.error(`\n[check] ${error.message}`);
  process.exitCode = 1;
}
