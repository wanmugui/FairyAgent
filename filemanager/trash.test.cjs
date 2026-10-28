#!/usr/bin/env node
// P1-2 回收站端到端验收。
// 起一个隔离实例（XDG_DATA_HOME 指临时目录），跑 delete→list→restore→purge/empty。
// 关键断言都对着用户真正在意的后果写，尤其是「还原不得覆盖」和「同名两次删除不互相吞」。
const { spawn } = require('child_process');
const fs = require('fs');
const fsp = require('fs/promises');
const os = require('os');
const path = require('path');
const crypto = require('crypto');

const PORT = 8094;
const BASE = `http://127.0.0.1:${PORT}`;
const ROOT = path.resolve(__dirname, '..');
const TMP = fs.mkdtempSync(path.join(os.tmpdir(), 'fm-trash-'));
const XDG = path.join(TMP, 'xdg');
const FILES = path.join(TMP, 'files');

let failed = 0;
const ok = (c, n, x) => { console.log((c ? '  ok   ' : '  FAIL ') + n + (x ? `  — ${x}` : '')); if (!c) failed++; };
const sleep = (ms) => new Promise(r => setTimeout(r, ms));
const md5 = (p) => crypto.createHash('md5').update(fs.readFileSync(p)).digest('hex');

async function api(method, p, body) {
  const r = await fetch(BASE + p, {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  let j = null;
  try { j = await r.json(); } catch { /* 非 JSON 也照样记状态码 */ }
  return { status: r.status, body: j };
}

async function waitUp(timeoutMs = 25000) {
  const t0 = Date.now();
  while (Date.now() - t0 < timeoutMs) {
    try { const r = await fetch(BASE + '/api/usage'); if (r.ok) return true; } catch { /* 还没起来 */ }
    await sleep(300);
  }
  return false;
}

function write(name, content) {
  const p = path.join(FILES, name);
  fs.mkdirSync(path.dirname(p), { recursive: true });
  fs.writeFileSync(p, content);
  return p;
}

(async () => {
  fs.mkdirSync(FILES, { recursive: true });
  const child = spawn('node', [path.join(ROOT, 'frontend', 'server.cjs'), String(PORT)], {
    cwd: path.join(ROOT, 'frontend'),
    env: { ...process.env, XDG_DATA_HOME: XDG },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  let serverLog = '';
  child.stdout.on('data', d => { serverLog += d; });
  child.stderr.on('data', d => { serverLog += d; });

  try {
    if (!await waitUp()) { console.error('服务没起来:\n' + serverLog.slice(-800)); process.exit(1); }

    // --- 1. 删除变成移入回收站，而不是消失 ---
    const p1 = write('a.txt', 'hello-trash');
    const h1 = md5(p1);
    const d1 = await api('DELETE', '/api/files?path=' + encodeURIComponent(p1));
    ok(d1.status === 200 && d1.body && d1.body.ok === true, '删除返回 ok:true', JSON.stringify(d1.body));
    ok(d1.body && d1.body.trashed === true, '响应标明是移入回收站而非硬删');
    ok(!fs.existsSync(p1), '原路径已不存在');

    // --- 2. 列表能看到它，且带原始路径与删除时间 ---
    const l1 = await api('GET', '/api/trash');
    const it1 = (l1.body.items || []).find(i => i.originalPath === p1);
    ok(!!it1, '回收站里能列出刚删的文件');
    ok(it1 && it1.name === 'a.txt', '条目名正确', it1 && it1.name);
    ok(it1 && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}$/.test(it1.deletionDate || ''), '删除时间格式合规', it1 && it1.deletionDate);

    // --- 3. 还原后内容必须逐字节一致 ---
    const r1 = await api('POST', '/api/trash/restore', { id: it1.id });
    ok(r1.status === 200 && r1.body.ok === true, '还原成功', JSON.stringify(r1.body));
    ok(fs.existsSync(p1), '文件回到原路径');
    ok(fs.existsSync(p1) && md5(p1) === h1, '还原后内容与原文件 md5 一致');
    ok((await api('GET', '/api/trash')).body.items.length === 0, '还原后回收站已空');

    // --- 4. 同名文件删两次，两条都得在（否则第二次会吞掉第一次） ---
    const ca = write('dup.txt', 'first');
    await api('DELETE', '/api/files?path=' + encodeURIComponent(ca));
    const cb = write('dup.txt', 'second');
    await api('DELETE', '/api/files?path=' + encodeURIComponent(cb));
    const dups = (await api('GET', '/api/trash')).body.items.filter(i => i.originalPath === cb);
    ok(dups.length === 2, '同名删两次留下两条，互不覆盖', `实际 ${dups.length} 条`);

    // --- 5. 还原不得覆盖已占用的路径（这是最容易做错、后果最不可逆的一条） ---
    write('dup.txt', 'occupying');
    const collide = await api('POST', '/api/trash/restore', { id: dups[0].id });
    ok(collide.status === 409, '原路径被占用时还原返回 409', `实际 ${collide.status}`);
    ok(fs.readFileSync(path.join(FILES, 'dup.txt'), 'utf8') === 'occupying',
      '被占用的文件内容没被动过（没被覆盖）');
    ok((await api('GET', '/api/trash')).body.items.length === 2, '还原失败时条目仍留在回收站');

    // --- 6. 彻底删除单个条目 ---
    const before = (await api('GET', '/api/trash')).body.items.length;
    const pg = await api('POST', '/api/trash/purge', { id: dups[0].id });
    ok(pg.status === 200 && pg.body.ok === true, '彻底删除成功');
    ok((await api('GET', '/api/trash')).body.items.length === before - 1, '列表少了一条');

    // --- 7. 清空 ---
    const em = await api('POST', '/api/trash/empty', {});
    ok(em.status === 200 && em.body.ok === true, '清空成功', JSON.stringify(em.body));
    ok((await api('GET', '/api/trash')).body.items.length === 0, '清空后列表为空');
    const dirs = fs.existsSync(path.join(XDG, 'Fairy', 'filemanager', 'trash', 'files'))
      ? fs.readdirSync(path.join(XDG, 'Fairy', 'filemanager', 'trash', 'files')) : [];
    ok(dirs.length === 0, '回收站 files 目录确实空了', `${dirs.length} 项残留`);

    // --- 8. 目录也能删能还原 ---
    write('sub/inner.txt', 'deep');
    const dp = path.join(FILES, 'sub');
    const dd = await api('DELETE', '/api/files?path=' + encodeURIComponent(dp));
    ok(dd.status === 200 && dd.body.ok === true, '目录删除成功');
    const l2 = await api('GET', '/api/trash');
    const dirItem = l2.body.items.find(i => i.isDirectory === true);
    ok(!!dirItem, '目录条目标记为 isDirectory');
    ok(dirItem && !fs.existsSync(path.join(dirItem.originalPath, 'inner.txt')), '目录已离开原位置');
    const rr = await api('POST', '/api/trash/restore', { id: dirItem.id });
    ok(rr.status === 200, '目录还原成功');
    ok(fs.readFileSync(path.join(FILES, 'sub', 'inner.txt'), 'utf8') === 'deep', '目录还原后内容完整');

    // --- 9. id 穿越必须被拒 ---
    await api('DELETE', '/api/files?path=' + encodeURIComponent(write('x.txt', 'x')));
    const trav = await api('POST', '/api/trash/restore', { id: '../../../etc/passwd' });
    ok(trav.status === 404, 'id 含 .. 被拒', `实际 ${trav.status}`);
    ok(fs.existsSync('/etc/passwd'), '系统文件未被触碰');

    // --- 10. 原本就不存在的路径删除，仍走原有 400 语义 ---
    const gone = await api('DELETE', '/api/files?path=' + encodeURIComponent(path.join(FILES, 'nope.txt')));
    ok(gone.status === 400, '删不存在的路径仍返回 400', `实际 ${gone.status}`);

    // --- 11. 桌面真实回收站绝不能被动 ---
    const deskTrash = path.join(process.env.XDG_DATA_HOME || path.join(os.homedir(), '.local', 'share'), 'Trash');
    ok(!fs.existsSync(path.join(deskTrash, 'files', 'a.txt')), '没有写进桌面共享的 Trash 目录');
  } catch (e) {
    console.error('测试异常：', e.stack);
    failed++;
  } finally {
    child.kill('SIGKILL');
  }

  console.log(failed ? `\n回收站：${failed} 项失败` : '\n回收站：全部通过');
  process.exit(failed ? 1 : 0);
})();
