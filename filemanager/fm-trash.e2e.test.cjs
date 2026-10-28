'use strict';
/**
 * 回收站周期清理的 HTTP 端到端验证。
 *
 * 关键安全约束：XDG_DATA_HOME 指向临时目录。
 * 模块认的是 XDG_DATA_HOME，不是 FAIRY_DATA_HOME —— 写错会导致
 * 测试直接在主人真实回收站上操作（本项目已因此出过一次事故）。
 */
const { spawn } = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');
const os = require('node:os');
const assert = require('node:assert');

const ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'fm-purge-e2e-'));
const PORT = 8096;
const BASE = `http://127.0.0.1:${PORT}`;
const D = (days) => {
  const d = new Date(Date.now() - days * 86400000);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}T${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}:${String(d.getSeconds()).padStart(2, '0')}`;
};

const T = path.join(ROOT, 'Fairy', 'filemanager', 'trash');
function seed(name, daysAgo, bytes = 5000) {
  fs.mkdirSync(path.join(T, 'files'), { recursive: true });
  fs.mkdirSync(path.join(T, 'info'), { recursive: true });
  fs.writeFileSync(path.join(T, 'files', name), Buffer.alloc(bytes, 7));
  fs.writeFileSync(path.join(T, 'info', name + '.trashinfo'),
    `[Trash Info]\nPath=/tmp/${name}\nDeletionDate=${D(daysAgo)}\n`);
}

/** 造一条删除时间缺失/不可解析的条目 —— seed() 总会写合法日期，用不了。 */
function seedUndated(name, bytes = 1000) {
  fs.mkdirSync(path.join(T, 'files'), { recursive: true });
  fs.mkdirSync(path.join(T, 'info'), { recursive: true });
  fs.writeFileSync(path.join(T, 'files', name), Buffer.alloc(bytes, 7));
  fs.writeFileSync(path.join(T, 'info', name + '.trashinfo'),
    `[Trash Info]\nPath=/tmp/${name}\nDeletionDate=not-a-real-date\n`);
}

(async () => {
  // 先种两条「启动清理不该动」的：未过期 + 无日期。
  // 过期条目必须等服务起来之后再种 —— 否则 startAutoPurge 会在启动时
  // 就把它删掉，测试就测不到 purge-expired 这个接口本身了。
  seed('new-item', 2);
  seedUndated('nodate-item');

  const srv = spawn(process.execPath, [path.join(__dirname, '..', 'frontend', 'server.cjs'), String(PORT)], {
    env: { ...process.env, XDG_DATA_HOME: ROOT },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  let log = '';
  srv.stdout.on('data', d => { log += d; });
  srv.stderr.on('data', d => { log += d; });

  try {
    // 等端口就绪
    for (let i = 0; i < 40; i++) {
      try { await fetch(BASE + '/api/trash'); break; } catch { await new Promise(r => setTimeout(r, 150)); }
    }

    // 服务已就绪，此时补种一条 40 天前的过期条目供后续断言使用
    seed('old-item', 40);

    console.log('=== POST 不带 dryRun → 必须默认预演 ===');
    const r1 = await (await fetch(BASE + '/api/trash/purge-expired', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}',
    })).json();
    console.log('  ', JSON.stringify(r1));
    assert.strictEqual(r1.ok, true);
    assert.strictEqual(r1.dryRun, true, 'GET 必须强制预演，不能有删除副作用');
    assert.strictEqual(r1.removed, 1, '应报告将删除 1 条');
    assert.strictEqual(r1.undated, 1, '无日期条目应被跳过并如实报告');

    console.log('=== 预演后文件必须都还在 ===');
    const left = fs.readdirSync(path.join(T, 'files')).sort();
    console.log('   剩余:', left.join(', '));
    assert.strictEqual(left.length, 3, '预演竟然真的删了');

    console.log('=== POST dryRun:true 显式预演 ===');
    const r2 = await (await fetch(BASE + '/api/trash/purge-expired', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ retentionDays: 30, dryRun: true }),
    })).json();
    assert.strictEqual(r2.dryRun, true);
    assert.strictEqual(r2.removed, 1);
    console.log('   removed =', r2.removed, 'undated =', r2.undated);

    console.log('=== POST 实删（dryRun:false）===');
    const r3 = await (await fetch(BASE + '/api/trash/purge-expired', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ retentionDays: 30, dryRun: false }),
    })).json();
    console.log('  ', JSON.stringify(r3));
    assert.strictEqual(r3.removed, 1);
    const after = fs.readdirSync(path.join(T, 'files')).sort();
    console.log('   删除后剩余:', after.join(', '));
    assert.ok(!after.includes('old-item'), '过期条目应被删');
    assert.ok(after.includes('new-item'), '未过期条目必须保留');
    assert.ok(after.includes('nodate-item'), '无日期条目必须保留');

    console.log('=== GET 必须 404（有意只收 POST，杜绝「点链接就删」）===');
    const rg = await fetch(BASE + '/api/trash/purge-expired');
    console.log('   GET →', rg.status);
    assert.notStrictEqual(rg.status, 200, 'GET 不该触发清理入口');

    console.log('\n✅ HTTP 端到端全部通过');
  } finally {
    srv.kill();                       // 只杀自己拉起的子进程
    await new Promise(r => setTimeout(r, 300));
    fs.rmSync(ROOT, { recursive: true, force: true });
    const tl = log.split('\n').filter(l => /trash|purge|listen/i.test(l));
    if (tl.length) { console.log('--- 服务端日志相关行 ---'); tl.slice(0, 5).forEach(l => console.log('   ' + l)); }
  }
})().catch(e => { console.error('❌ 失败:', e.message); process.exit(1); });
