import assert from "node:assert/strict";
import test from "node:test";

import { resolvePythonInvocation, supportsMinimumPython, withAgentPythonEnvironment } from "./python_runtime.mjs";

const python310 = () => ({ major: 3, minor: 10 });

test("configured Python wins over platform discovery", () => {
  const calls = [];
  const result = resolvePythonInvocation(
    { AGENT_PYTHON_BIN: "/opt/project/python" },
    "darwin",
    (command, prefix) => { calls.push([command, prefix]); return python310(); },
  );
  assert.deepEqual(result, { command: "/opt/project/python", prefix: [], version: { major: 3, minor: 10 } });
  assert.deepEqual(calls, [["/opt/project/python", []]]);
});

test("invalid configured Python does not silently fall back", () => {
  assert.throws(
    () => resolvePythonInvocation({ AGENT_PYTHON_BIN: "/missing/python" }, "darwin", () => null),
    /AGENT_PYTHON_BIN must provide Python 3\.10\+/,
  );
});

test("Unix discovery prefers an activated python over python3", () => {
  const result = resolvePythonInvocation({}, "linux", command => command === "python" ? python310() : null);
  assert.deepEqual(result, { command: "python", prefix: [], version: { major: 3, minor: 10 } });
});

test("project virtual environment precedes system Python", () => {
  const result = resolvePythonInvocation(
    { AGENT_REPO_ROOT: "/repo" },
    "darwin",
    command => command === "/repo/.tools/venv/bin/python" ? python310() : null,
  );
  assert.deepEqual(result, { command: "/repo/.tools/venv/bin/python", prefix: [], version: { major: 3, minor: 10 } });
});

test("Windows discovery prefers py -3", () => {
  const result = resolvePythonInvocation({}, "win32", (command, prefix) => command === "py" && prefix[0] === "-3" ? python310() : null);
  assert.deepEqual(result, { command: "py", prefix: ["-3"], version: { major: 3, minor: 10 } });
});

test("discovery skips an unsupported Python 3.9", () => {
  const result = resolvePythonInvocation({}, "darwin", command => {
    if (command === "python") return { major: 3, minor: 9 };
    if (command === "python3") return python310();
    return null;
  });
  assert.deepEqual(result, { command: "python3", prefix: [], version: { major: 3, minor: 10 } });
});

test("minimum Python support begins at 3.10", () => {
  assert.equal(supportsMinimumPython({ major: 3, minor: 9 }), false);
  assert.equal(supportsMinimumPython({ major: 3, minor: 10 }), true);
  assert.equal(supportsMinimumPython({ major: 3, minor: 14 }), true);
});

test("resolved direct Python is inherited by child Agents", () => {
  const env = withAgentPythonEnvironment({ EXAMPLE: "ok" }, { command: "/repo/.tools/venv/bin/python", prefix: [], version: python310() });
  assert.equal(env.AGENT_PYTHON_BIN, "/repo/.tools/venv/bin/python");
  assert.equal(env.EXAMPLE, "ok");
});

test("Python launcher does not misrepresent py -3 as a direct executable", () => {
  const env = withAgentPythonEnvironment({}, { command: "py", prefix: ["-3"], version: python310() });
  assert.equal(env.AGENT_PYTHON_BIN, undefined);
});
