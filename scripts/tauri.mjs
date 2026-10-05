#!/usr/bin/env node
// Tauri CLI wrapper: loads the MSVC environment on Windows, then runs
// `cargo tauri`. After a successful `build`, the desktop binary is published
// to the repository root so a desktop shortcut can point at a stable path on
// any platform.
// Usage: node scripts/tauri.mjs <dev|build|...>
import { spawn } from 'node:child_process';
import { chmodSync, copyFileSync, existsSync } from 'node:fs';
import path from 'node:path';
import process from 'node:process';
import { fileURLToPath } from 'node:url';

const args = process.argv.slice(2);
if (args.length === 0) {
  console.error('usage: node scripts/tauri.mjs <dev|build|...>');
  process.exit(2);
}

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const isWin = process.platform === 'win32';

function publishDesktopBinary() {
  const releaseDir = path.join(repoRoot, 'src-tauri', 'target', 'release');
  const candidates = isWin
    ? [path.join(releaseDir, 'fairy.exe'), path.join(releaseDir, 'Fairy.exe')]
    : [path.join(releaseDir, 'fairy'), path.join(releaseDir, 'Fairy')];
  const source = candidates.find((candidate) => existsSync(candidate));
  if (!source) {
    console.warn('[tauri.mjs] build succeeded but no desktop binary was found in src-tauri/target/release');
    return;
  }
  const target = path.join(repoRoot, isWin ? 'Fairy.exe' : 'Fairy');
  copyFileSync(source, target);
  if (!isWin) chmodSync(target, 0o755);
  console.log(`[tauri.mjs] desktop binary published: ${path.relative(repoRoot, target)}`);
}

function finish(code) {
  const exitCode = code ?? 1;
  if (exitCode === 0 && args[0] === 'build') publishDesktopBinary();
  process.exit(exitCode);
}

if (!isWin) {
  // macOS / Linux: call cargo tauri directly.
  const child = spawn('cargo', ['tauri', ...args], { stdio: 'inherit' });
  child.on('exit', finish);
  process.on('SIGINT', () => child.kill('SIGINT'));
  process.on('SIGTERM', () => child.kill('SIGTERM'));
} else {
  // Windows: load vcvars64.bat first so the Rust linker can find MSVC.
  const candidates = [
    'D:\\Microsoft VS Code\\CTool\\VC\\Auxiliary\\Build\\vcvars64.bat',
    'C:\\Program Files\\Microsoft Visual Studio\\2022\\Community\\VC\\Auxiliary\\Build\\vcvars64.bat',
    'C:\\Program Files\\Microsoft Visual Studio\\2022\\BuildTools\\VC\\Auxiliary\\Build\\vcvars64.bat',
    'C:\\Program Files\\Microsoft Visual Studio\\2022\\Enterprise\\VC\\Auxiliary\\Build\\vcvars64.bat',
    'C:\\Program Files (x86)\\Microsoft Visual Studio\\2022\\BuildTools\\VC\\Auxiliary\\Build\\vcvars64.bat',
  ];
  const vcvars = candidates.find((candidate) => existsSync(candidate));
  if (!vcvars) {
    console.error('[tauri.mjs] vcvars64.bat not found - install Visual Studio 2022 Build Tools with the C++ workload');
    process.exit(1);
  }
  console.log(`[tauri.mjs] loading MSVC env from: ${vcvars}`);
  const cmd = `"${vcvars}" >NUL && cargo tauri ${args.join(' ')}`;
  // Pass the working directory through spawn instead of `cd /d "..."`, which
  // collides with cmd.exe quote stripping around the vcvars path.
  const child = spawn('cmd.exe', ['/c', cmd], {
    stdio: 'inherit',
    cwd: repoRoot,
    windowsVerbatimArguments: true,
  });
  child.on('exit', finish);
  process.on('SIGINT', () => child.kill('SIGINT'));
  process.on('SIGTERM', () => child.kill('SIGTERM'));
}
