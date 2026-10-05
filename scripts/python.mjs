import { spawn } from "node:child_process";

import { resolvePythonInvocation, withAgentPythonEnvironment } from "./python_runtime.mjs";
import { repositoryRoot } from "./agent_runtime.mjs";

const args = process.argv.slice(2);
if (args.length === 0) {
  console.error("usage: node scripts/python.mjs <script-or-python-arguments>");
  process.exitCode = 2;
} else {
  try {
    const python = resolvePythonInvocation({ ...process.env, AGENT_REPO_ROOT: repositoryRoot() });
    const env = withAgentPythonEnvironment(process.env, python);
    const child = spawn(python.command, [...python.prefix, ...args], { stdio: "inherit", env });
    child.on("error", error => {
      console.error(`[python] cannot start ${python.command}: ${error.message}`);
      process.exitCode = 1;
    });
    child.on("exit", code => {
      process.exitCode = code ?? 1;
    });
  } catch (error) {
    console.error(`[python] ${error.message}`);
    process.exitCode = 1;
  }
}
