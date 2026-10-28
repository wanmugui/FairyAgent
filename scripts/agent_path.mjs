import { ensureAgent, repositoryRoot } from "./agent_runtime.mjs";

try {
  const agentPath = ensureAgent(repositoryRoot(), process.env, message => process.stderr.write(`[agent] ${message}\n`));
  process.stdout.write(`${agentPath}\n`);
} catch (error) {
  console.error(`[agent] ${error.message}`);
  process.exitCode = 1;
}
