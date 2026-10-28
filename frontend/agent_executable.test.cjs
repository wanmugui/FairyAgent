const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');

const { buildAgentProcessOptions, resolveAgentExecutable } = require('./agent_executable.cjs');

test('macOS uses the cached extensionless agent binary', () => {
  const repo = fs.mkdtempSync(path.join(os.tmpdir(), 'agent-exe-test-'));
  fs.mkdirSync(path.join(repo, '.tools'));
  fs.writeFileSync(path.join(repo, '.tools', 'agent-loop-darwin-arm64'), 'mach-o');

  assert.equal(resolveAgentExecutable(repo, 'darwin', 'arm64'), path.join(repo, '.tools', 'agent-loop-darwin-arm64'));
});

test('Windows uses the cached .exe agent binary', () => {
  const repo = fs.mkdtempSync(path.join(os.tmpdir(), 'agent-exe-test-'));
  fs.mkdirSync(path.join(repo, '.tools'));
  fs.writeFileSync(path.join(repo, '.tools', 'agent-loop-win32-x64.exe'), 'pe');

  assert.equal(resolveAgentExecutable(repo, 'win32', 'x64'), path.join(repo, '.tools', 'agent-loop-win32-x64.exe'));
});

test('missing agent binaries produce a diagnostic error', () => {
  const repo = fs.mkdtempSync(path.join(os.tmpdir(), 'agent-exe-test-'));
  fs.mkdirSync(path.join(repo, '.tools'));

  assert.throws(() => resolveAgentExecutable(repo, 'darwin', 'arm64'), /pnpm agent:build/);
});

test('AGENT_LOOP_PATH overrides repository discovery', () => {
  const repo = fs.mkdtempSync(path.join(os.tmpdir(), 'agent-exe-test-'));
  fs.mkdirSync(path.join(repo, '.tools'));
  const override = path.join(repo, 'custom-agent');
  fs.writeFileSync(override, 'custom');
  const previous = process.env.AGENT_LOOP_PATH;
  process.env.AGENT_LOOP_PATH = override;
  try {
    assert.equal(resolveAgentExecutable(repo, 'darwin', 'arm64'), override);
  } finally {
    if (previous === undefined) delete process.env.AGENT_LOOP_PATH;
    else process.env.AGENT_LOOP_PATH = previous;
  }
});

test('agent process options carry repository root and working directory', () => {
  const repo = fs.mkdtempSync(path.join(os.tmpdir(), 'agent-exe-test-'));
  const options = buildAgentProcessOptions(repo, { stdio: ['ignore', 'pipe', 'pipe'] });

  assert.equal(options.cwd, repo);
  assert.equal(options.env.AGENT_REPO_ROOT, repo);
  assert.deepEqual(options.stdio, ['ignore', 'pipe', 'pipe']);
});
