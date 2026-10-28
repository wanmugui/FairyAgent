#!/usr/bin/env node
// 线上前端构建：强制带 /agent/ 基址。
//
// 为什么必须有这个脚本：
// 线上 Caddy 把 /agent 前缀 strip 掉之后再交给 8081，产物里的资源引用必须
// 自带前缀。基址不对时浏览器会去请求根路径 /assets/*.js，那个请求落到
// Vite 兜底、返回 index.html（200 text/html），脚本因 MIME 不匹配被拒绝
// 执行 —— 表现就是「页面打开了但一片空白」。vite.config.js 早就写好了
// base = process.env.FAIRY_BASE || '/'，但仓库里没有任何入口传这个变量，
// 每次有人跑 `pnpm build` 都会把线上打回白屏。
//
// 这里用 node 写而不用 `FAIRY_BASE=/agent/ vite build`：后者是 POSIX shell
// 语法，在 Windows 的 cmd/PowerShell 下不成立，而开发环境是 Windows。
//
// 用法：node scripts/build_frontend_online.mjs
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import process from 'node:process';
import { fileURLToPath } from 'node:url';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const frontendDir = path.join(repoRoot, 'frontend');
const distDir = path.join(frontendDir, 'dist');
const base = process.env.FAIRY_BASE || '/agent/';

if (!base.startsWith('/')) {
  console.error(`[build:online] FAIRY_BASE 必须是以 / 开头的路径前缀，收到: ${base}`);
  process.exit(2);
}
// 根基址 '/' 正是白屏的根因，必须直接拒绝而不是放行。
// 放行它等于让自检变成摆设：构建会成功，产物引用 /assets/*.js，
// 线上请求落到 Vite 兜底拿到 text/html，页面全白 —— 而脚本还报 OK。
if (base === '/' || base === '') {
  console.error('[build:online] FAIRY_BASE 不能是根路径 "/"：线上挂在 /agent 前缀下，');
  console.error('  根基址会让产物去根路径取资源，浏览器拿到 HTML 当 JS 解析，直接白屏。');
  process.exit(2);
}

const isWin = process.platform === 'win32';
const child = spawn(isWin ? 'pnpm.cmd' : 'pnpm', ['run', 'build'], {
  cwd: frontendDir,
  stdio: 'inherit',
  env: { ...process.env, FAIRY_BASE: base },
  shell: isWin,
});

child.on('exit', (code) => {
  if (code !== 0) process.exit(code ?? 1);
  verify(base);
});

function verify(expectedBase) {
  // 构建完必须自检：产物里的引用没带前缀就等于白屏，不能靠肉眼。
  const indexPath = path.join(distDir, 'index.html');
  let html;
  try {
    html = fs.readFileSync(indexPath, 'utf8');
  } catch (err) {
    console.error(`[build:online] 构建结束但找不到 ${indexPath}: ${err.message}`);
    process.exit(1);
  }
  const refs = [...html.matchAll(/(?:src|href)="([^"]+)"/g)].map((m) => m[1]);
  const assetRefs = refs.filter((r) => r.startsWith('/') && !r.startsWith('//'));
  if (!assetRefs.length) {
    console.error('[build:online] index.html 里没找到任何绝对路径资源引用，无法确认基址');
    process.exit(1);
  }
  const prefix = expectedBase.endsWith('/') ? expectedBase : expectedBase + '/';
  const bad = assetRefs.filter((r) => !r.startsWith(prefix));
  if (bad.length) {
    console.error(`[build:online] 基址校验失败：以下引用缺少 ${prefix} 前缀，会导致线上白屏：`);
    for (const r of bad) console.error(`  - ${r}`);
    process.exit(1);
  }
  console.log(`[build:online] OK — 基址 ${prefix}，${assetRefs.length} 个资源引用均已带前缀`);
}
