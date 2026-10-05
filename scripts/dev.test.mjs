import assert from "node:assert/strict";
import path from "node:path";
import test from "node:test";

import { agentBinaryIsCurrent, agentBuildEnvironment, agentExecutablePath, goCommand } from "./agent_runtime.mjs";
import { existsSync, mkdirSync, readFileSync, utimesSync, writeFileSync } from "node:fs";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";

import {
  acquireDevInstanceLock,
  frontendDependenciesReady,
  releaseDevInstanceLock,
  removeOwnedServiceState,
  requiredPythonModules,
  resolvePnpmInvocation,
  serviceStatePath,
  writeServiceState,
} from "./dev.mjs";

test("Agent build output is platform-specific but never a repository artifact", () => {
  const repo = path.join(path.sep, "repo");
  assert.equal(agentExecutablePath(repo, "darwin", "arm64"), path.join(repo, ".tools", "agent-loop-darwin-arm64"));
  assert.equal(agentExecutablePath(repo, "linux", "x64"), path.join(repo, ".tools", "agent-loop-linux-x64"));
  assert.equal(agentExecutablePath(repo, "win32", "x64"), path.join(repo, ".tools", "agent-loop-win32-x64.exe"));
});

test("pnpm launcher reuses pnpm's JavaScript entry when available", () => {
  const invocation = resolvePnpmInvocation({ npm_execpath: "/runtime/pnpm.cjs" }, "win32");
  assert.equal(invocation.command, process.execPath);
  assert.deepEqual(invocation.prefix, ["/runtime/pnpm.cjs"]);
});

test("direct launcher fallback uses the platform command", () => {
  assert.deepEqual(resolvePnpmInvocation({}, "darwin"), { command: "pnpm", prefix: [] });
  assert.deepEqual(resolvePnpmInvocation({}, "win32"), { command: "pnpm.cmd", prefix: [] });
});

test("Agent build uses an explicitly resolved Go runtime", () => {
  assert.equal(goCommand({ AGENT_GO_BIN: "C:\\project\\.tools\\go\\bin\\go.exe" }), "C:\\project\\.tools\\go\\bin\\go.exe");
  assert.equal(goCommand({}), "go");
});

test("Agent build defaults Go cache to the writable project tools directory", () => {
  const repo = path.join(path.sep, "repo");
  assert.equal(agentBuildEnvironment(repo, {}).GOCACHE, path.join(repo, ".tools", "go-build-cache"));
  assert.equal(agentBuildEnvironment(repo, { GOCACHE: "/custom/cache" }).GOCACHE, "/custom/cache");
});

test("Agent binary is reused until Go sources become newer", () => {
  const repo = mkdtempSync(path.join(tmpdir(), "fairy-agent-build-"));
  const agentDir = path.join(repo, "agent");
  const output = agentExecutablePath(repo);
  mkdirSync(agentDir, { recursive: true });
  mkdirSync(path.dirname(output), { recursive: true });
  const source = path.join(agentDir, "main.go");
  writeFileSync(source, "package main\n");
  writeFileSync(output, "binary\n");

  const base = Date.now() / 1000;
  utimesSync(source, base + 1, base + 1);
  utimesSync(output, base + 2, base + 2);
  assert.equal(agentBinaryIsCurrent(repo, output), true);

  utimesSync(source, base + 3, base + 3);
  assert.equal(agentBinaryIsCurrent(repo, output), false);
});

test("service state is atomically written and only removed by its owner", () => {
  const repo = mkdtempSync(path.join(tmpdir(), "agent-loop-service-state-"));
  const state = {
    version: 3,
    instance_id: "instance-123",
    repository_root: repo,
    launcher: { pid: 123, started_at: "2026-01-01T00:00:00.000Z" },
    api: { pid: 124, started_at: "2026-01-01T00:00:01.000Z" },
    frontend: { pid: 125, started_at: "2026-01-01T00:00:02.000Z" },
  };
  writeServiceState(repo, state);
  assert.deepEqual(JSON.parse(readFileSync(serviceStatePath(repo), "utf8")), state);
  removeOwnedServiceState(repo, 999);
  assert.deepEqual(JSON.parse(readFileSync(serviceStatePath(repo), "utf8")), state);
  removeOwnedServiceState(repo, 123);
  assert.throws(() => readFileSync(serviceStatePath(repo), "utf8"));
});

test("dev instance lock is exclusive and released explicitly", () => {
  const repo = mkdtempSync(path.join(tmpdir(), "fairy-dev-lock-"));
  const lock = path.join(repo, ".tools", "dev-instance.lock");
  acquireDevInstanceLock(repo);
  assert.equal(existsSync(lock), true);
  assert.throws(() => acquireDevInstanceLock(repo), /already running/);
  releaseDevInstanceLock();
  assert.equal(existsSync(lock), false);
});

test("frontend dependency check detects packages added after install", () => {
  const frontend = mkdtempSync(path.join(tmpdir(), "fairy-frontend-deps-"));
  const packageFile = path.join(frontend, "package.json");
  const nodeModules = path.join(frontend, "node_modules");
  mkdirSync(path.join(nodeModules, "lucide-react"), { recursive: true });
  writeFileSync(path.join(frontend, "pnpm-lock.yaml"), "lockfileVersion: '9.0'\n");
  writeFileSync(packageFile, JSON.stringify({ dependencies: { "lucide-react": "^0.468.0" } }));
  writeFileSync(path.join(nodeModules, ".modules.yaml"), "hoistPattern: []\n");
  assert.equal(frontendDependenciesReady(frontend), true);

  writeFileSync(packageFile, JSON.stringify({ dependencies: {
    "lucide-react": "^0.468.0",
    "new-package": "^1.0.0",
  } }));
  assert.equal(frontendDependenciesReady(frontend), false);
});
test("desktop automation modules follow the supported Python matrix", () => {
  assert.deepEqual(requiredPythonModules({ major: 3, minor: 10 }), ["requests", "bs4", "PIL"]);
  assert.deepEqual(requiredPythonModules({ major: 3, minor: 11 }), ["requests", "bs4", "PIL", "cua_auto", "pywinctl"]);
  assert.deepEqual(requiredPythonModules({ major: 3, minor: 13 }), ["requests", "bs4", "PIL", "cua_auto", "pywinctl"]);
  assert.deepEqual(requiredPythonModules({ major: 3, minor: 14 }), ["requests", "bs4", "PIL"]);
});
