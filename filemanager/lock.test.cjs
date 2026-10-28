#!/usr/bin/env node
// P1-10 租约锁验收。起隔离实例，FM_LOCK_TTL_MS 调短，好让「孤儿锁过期」可测——
// 那是 spec 点名的主要复杂度，60s 的话等一轮太慢。
const { spawn } = require('child_process');
const fs = require('fs');
const os = require('os');
const path = require('path');

const PORT = 8098;
const BASE = `http://127.0.0.1:${PORT}`;
const ROOT = path.resolve(__dirname, '..');
const TMP = fs.mkdtempSync(path.join(os.tmpdir(), 'fm-lock-'));
const XDG = path.join(TMP, 'xdg');
const FILES = path.join(TMP, 'files');
const TTL = 1500;

let failed = 0;
const ok = (c, n, x) => { console.log((c ? '  ok   ' : '  FAIL ') + n + (x ? `  — ${x}` : '')); if (!c) failed++; };
const sleep = (ms) => new Promise(r => setTimeout(r, ms));

async function post(p, body) {
  const r = await fetch(BASE + p, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  });
  let j = null; try { j = await r.json(); } catch { /* 非 JSON */ }
  return { status: r.status, body: j };
}
// PUT /api/file 是裸字节流（P0-1/P0-2 为了不弄坏 PNG/ZIP/字体改的），不是 JSON。
// 早先这里发 JSON，服务端 200 但把 `{"text":...}` 原样写进了文件——
// 接口没错，是测试把字节流接口当 JSON 接口调了。
async function putFile(p, text, lock) {
  const u = new URL(BASE + '/api/file');
  u.searchParams.set('path', p);
  if (lock) u.searchParams.set('lock', lock);
  const r = await fetch(u, { method: 'PUT', headers: { 'Content-Type': 'application/octet-stream' }, body: text });
  let j = null; try { j = await r.json(); } catch { /* 非 JSON */ }
  return { status: r.status, body: j };
}
async function waitUp(t = 25000) {
  const t0 = Date.now();
  while (Date.now() - t0 < t) {
    try { const r = await fetch(BASE + '/api/usage'); if (r.ok) return true; } catch { /* 等 */ }
    await sleep(300);
  }
  return false;
}

(async () => {
  fs.mkdirSync(FILES, { recursive: true });
  const f = path.join(FILES, 'doc.md');
  fs.writeFileSync(f, 'v0');

  const child = spawn('node', [path.join(ROOT, 'frontend', 'server.cjs'), String(PORT)], {
    cwd: path.join(ROOT, 'frontend'),
    env: { ...process.env, XDG_DATA_HOME: XDG, FM_LOCK_TTL_MS: String(TTL) },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  let log = '';
  child.stdout.on('data', d => { log += d; });
  child.stderr.on('data', d => { log += d; });

  try {
    if (!await waitUp()) { console.error('服务没起来:\n' + log.slice(-600)); process.exit(1); }

    // 1. 取锁
    const a1 = await post('/api/lock/acquire', { path: f, owner: 'alice' });
    ok(a1.status === 200 && a1.body.ok && a1.body.token, 'alice 取锁成功');
    const tok = a1.body.token;
    ok(typeof tok === 'string' && tok.length >= 16, 'token 不是弱值', tok && tok.slice(0, 8));

    // 2. 第二个人被挡（spec 的核心验收：不是静默互相覆盖）
    const b1 = await post('/api/lock/acquire', { path: f, owner: 'bob' });
    ok(b1.status === 409, 'bob 取锁被拒 409', `实际 ${b1.status}`);
    ok(/alice/.test(b1.body && b1.body.error || ''), '拒绝信息里有占用者名字', JSON.stringify(b1.body && b1.body.error));
    ok(b1.body && b1.body.holder && b1.body.holder.owner === 'alice', '响应带 holder 结构');

    // 3. 同一 owner 重复取 = 续期，且拿到同一个 token
    const a2 = await post('/api/lock/acquire', { path: f, owner: 'alice' });
    ok(a2.status === 200 && a2.body.token === tok, '同 owner 重复取锁幂等（同一 token）');
    ok(a2.body.reentrant === true, '标记为 reentrant');

    // 4. 续期
    const rn = await post('/api/lock/renew', { path: f, token: tok });
    ok(rn.status === 200 && rn.body.ok, '正确 token 续期成功');

    // 5. 拿别人的 token 续期/释放都必须被拒
    const rnBad = await post('/api/lock/renew', { path: f, token: 'not-the-token' });
    ok(rnBad.status === 409, '错误 token 续期被拒 409', `实际 ${rnBad.status}`);
    const relBad = await post('/api/lock/release', { path: f, token: 'not-the-token' });
    ok(relBad.status === 409, '错误 token 释放被拒 409（否则锁形同虚设）', `实际 ${relBad.status}`);

    // 6. 持锁保存成功
    const s1 = await putFile(f, 'v1-by-alice', tok);
    ok(s1.status === 200, '持锁保存成功', `实际 ${s1.status}`);
    ok(fs.readFileSync(f, 'utf8') === 'v1-by-alice', '内容确实写进去了');

    // 7. 没有 token 也能存（兼容既有行为），但错误 token 会被拦
    const s2 = await putFile(f, 'v2-no-token');
    ok(s2.status === 200, '不带 token 保存仍可用（不破坏既有调用）', `实际 ${s2.status}`);
    const s3 = await putFile(f, 'v3-bad-token', 'bogus');
    ok(s3.status === 409, '错误 token 保存被拦 409', `实际 ${s3.status}`);

    // 8. 主动释放后可被他人接管
    const rel = await post('/api/lock/release', { path: f, token: tok });
    ok(rel.status === 200 && rel.body.ok, '释放成功');
    const b2 = await post('/api/lock/acquire', { path: f, owner: 'bob' });
    ok(b2.status === 200 && b2.body.token !== tok, '释放后 bob 能接管，且拿到新 token');
    await post('/api/lock/release', { path: f, token: b2.body.token });

    // 9. 【孤儿锁】拿锁后不续期也不释放，租约到期自动失效
    //    这是 spec 点名的主要复杂度：客户端崩了就再也不能把人永久挡住。
    const c1 = await post('/api/lock/acquire', { path: f, owner: 'crashed-client' });
    ok(c1.status === 200, '模拟崩掉的客户端取锁');
    const c3 = await post('/api/lock/acquire', { path: f, owner: 'dave' });
    ok(c3.status === 409, '到期前确实挡着别人');
    await sleep(TTL + 700);
    const c4 = await post('/api/lock/acquire', { path: f, owner: 'dave' });
    ok(c4.status === 200, '租约到期后孤儿锁自动释放，dave 能取到', `实际 ${c4.status}`);
    ok(c4.body.token !== c1.body.token, '是新的锁，不是复用了旧的');
    await post('/api/lock/release', { path: f, token: c4.body.token });

    // 10. 心跳续期能把锁一直续命（到期前续就不该被抢）
    const e1 = await post('/api/lock/acquire', { path: f, owner: 'keepalive' });
    for (let i = 0; i < 3; i++) {
      await sleep(Math.floor(TTL / 2));
      await post('/api/lock/renew', { path: f, token: e1.body.token });
      const probe = await post('/api/lock/acquire', { path: f, owner: 'thief' });
      ok(probe.status === 409, `第 ${i + 1} 次心跳后锁仍然有效（没被抢走）`, `实际 ${probe.status}`);
    }
    await post('/api/lock/release', { path: f, token: e1.body.token });

    // 11. 过期 token 保存被拒
    const g1 = await post('/api/lock/acquire', { path: f, owner: 'expiring' });
    await sleep(TTL + 700);
    const s4 = await putFile(f, 'v-should-not-write', g1.body.token);
    ok(s4.status === 409, '用已过期的 token 保存被拒 409', `实际 ${s4.status}`);
    ok(fs.readFileSync(f, 'utf8') !== 'v-should-not-write', '过期 token 确实没写进去');

    // 12. 目录不支持锁（否则整棵树会被锁死）
    const d = await post('/api/lock/acquire', { path: FILES, owner: 'alice' });
    ok(d.status === 400, '对目录取锁被拒 400', `实际 ${d.status}`);

    // 13. 锁沿用 resolveFMRoot 的整盘语义：这是本地文件管理器，侧边栏都能列 C:/D:/，
    //     所以绝对路径本来就允许。早先这里断言 /etc/passwd 应 403，那是凭空造了
    //     不存在的约束（FILE_CONTENT_ALLOWED_ROOTS 只管 assistant 读文件，不管这里）。
    //     真正该守的是「不能越出 resolveFMRoot 给的路径」，改为验同名文件互不串锁。
    const other = path.join(FILES, 'other.md');
    fs.writeFileSync(other, 'other-v0');
    const o1 = await post('/api/lock/acquire', { path: f, owner: 'alice' });
    const o2 = await post('/api/lock/acquire', { path: other, owner: 'bob' });
    ok(o2.status === 200, '不同文件可以各持一把锁（锁按路径隔离，不是一把全局锁）');
    ok(o2.body.token !== o1.body.token, '两把锁是不同 token');
    await post('/api/lock/release', { path: f, token: o1.body.token });
    await post('/api/lock/release', { path: other, token: o2.body.token });

    // 14. 锁列表可观测（测试要能看见自己造的状态）
    const l = await (await fetch(BASE + '/api/lock')).json();
    ok(Array.isArray(l.locks), 'GET /api/lock 返回列表');
  } catch (e) {
    console.error('测试异常：', e.stack);
    failed++;
  } finally {
    child.kill('SIGKILL');
  }

  console.log(failed ? `\n租约锁：${failed} 项失败` : '\n租约锁：全部通过');
  process.exit(failed ? 1 : 0);
})();
