#!/usr/bin/env node
'use strict';
/**
 * 手动清理回收站。
 *
 * 服务启动时也会自动清一次，但你想立刻腾空间、或者服务没跑的时候，
 * 用这个更直接。
 *
 *   node scripts/purge-trash.mjs --dry-run          # 只看会删什么，不动手
 *   node scripts/purge-trash.mjs --days 7           # 按 7 天保留期清理
 *   node scripts/purge-trash.mjs                    # 用默认 30 天
 *   FAIRY_TRASH_RETENTION_DAYS=3 node scripts/purge-trash.mjs
 */
import { createRequire } from 'node:module';

// fm-trash.cjs 是 CommonJS，本文件是 ESM —— 用 createRequire 桥接，
// 这样两边共用同一份模块实例，测试里的环境变量设置才生效。
const require = createRequire(import.meta.url);
const trash = require('../filemanager/fm-trash.cjs');

const argv = process.argv.slice(2);
const dryRun = argv.includes('--dry-run') || argv.includes('-n');

const daysIdx = argv.findIndex(a => a === '--days' || a === '-d');
const retentionDays = daysIdx >= 0 ? Number(argv[daysIdx + 1]) : undefined;
if (retentionDays !== undefined && !Number.isFinite(retentionDays)) {
  console.error('--days 需要一个数字');
  process.exit(2);
}

const r = trash.purgeExpired({ retentionDays, dryRun });

const effectiveDays = retentionDays
  ?? process.env.FAIRY_TRASH_RETENTION_DAYS
  ?? trash.DEFAULT_TRASH_RETENTION_DAYS;
const mib = (b) => (b / 1048576).toFixed(1) + ' MiB';

console.log(`[purge-trash] ${dryRun ? '[预演] ' : ''}保留期 ${effectiveDays} 天`);
console.log(`  删除 ${r.removed} 项，释放 ${mib(r.freedBytes)}，保留 ${r.kept} 项`);
if (r.undated > 0) {
  console.log(`  跳过 ${r.undated} 项：缺少可解析的删除时间，保守保留`);
}
if (r.errors.length > 0) {
  console.log(`  ${r.errors.length} 项删除失败：`);
  for (const e of r.errors) console.log(`    - ${e.id}: ${e.error}`);
}
if (dryRun && r.removed > 0) {
  console.log('  （预演模式，未实际删除）');
}