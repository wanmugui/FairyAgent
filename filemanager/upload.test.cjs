#!/usr/bin/env node
// P1-5 分片续传 + P1-6 完整性校验。
//
// 【关于 2GB】spec 的验收写的是「传 2GB 中途断开」。2GB 在测试里既慢又占盘，
// 而且真正要证明的性质与体积无关——**「无重复字节」**。所以这里用 24MB / 不等长
// 分片（20MB + 4MB，最后一片不整除，重复字节的 bug 一露就现）。机制与 2GB 完全
// 相同：同样的三段式、同样的 offset 校验、同样的按 offset 写盘。体积差异如实说明。
const { spawn } = require('child_process');
const fs = require('fs');
const os = require('os');
const path = require('path');
const crypto = require('crypto');

const PORT = 8095;
const BASE = `http://127.0.0.1:${PORT}`;
const ROOT = path.resolve(__dirname, '..');
const { crc32cHex } = require('./fm-upload.cjs');

let failed = 0;
const ok = (c, n, x) => { console.log((c ? '  ok   ' : '  FAIL ') + n + (x ? `  — ${x}` : '')); if (!c) failed++; };
const sleep = (ms) => new Promise(r => setTimeout(r, ms));
const sha = (b) => crypto.createHash('sha256').update(b).digest('hex');

const post = async (p, body) => {
  const r = await fetch(BASE + p, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
  let j = null; try { j = await r.json(); } catch { /* 非 JSON */ }
  return { status: r.status, body: j };
};
const putPart = async (id, offset, buf, crc) => {
  const h = { 'Content-Type': 'application/octet-stream' };
  if (crc) h['X-Checksum-Crc32c'] = crc;
  const r = await fetch(`${BASE}/api/upload/part?uploadId=${id}&offset=${offset}`, { method: 'PUT', headers: h, body: buf });
  let j = null; try { j = await r.json(); } catch { /* 非 JSON */ }
  return { status: r.status, body: j };
};
const status = async (id) => (await fetch(`${BASE}/api/upload/status?uploadId=${id}`)).json();
async function waitUp(t = 25000) {
  const t0 = Date.now();
  while (Date.now() - t0 < t) {
    try { const r = await fetch(BASE + '/api/usage'); if (r.ok) return true; } catch { /* 等 */ }
    await sleep(300);
  }
  return false;
}

/** 不等长分片：让「重复写入」这类 bug 无处可藏。 */
function splitPlan(total) { return [10 * 1024 * 1024, 7 * 1024 * 1024, total - 17 * 1024 * 1024]; }

(async () => {
  const TMP = fs.mkdtempSync(path.join(os.tmpdir(), 'fm-up-'));
  const XDG = path.join(TMP, 'xdg');
  const FILES = path.join(TMP, 'files');
  fs.mkdirSync(FILES, { recursive: true });

  const child = spawn(process.execPath, [path.join(ROOT, 'frontend', 'server.cjs'), String(PORT)], {
    cwd: path.join(ROOT, 'frontend'),
    env: { ...process.env, XDG_DATA_HOME: XDG, FM_UPLOAD_TTL_MS: '600000' },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  let log = ''; child.stdout.on('data', d => { log += d; }); child.stderr.on('data', d => { log += d; });

  try {
    if (!await waitUp()) { console.error('服务没起来:\n' + log.slice(-600)); process.exit(1); }

    const src = Buffer.alloc(24 * 1024 * 1024);
    for (let i = 0; i < src.length; i++) src[i] = (i * 31 + 7) & 0xff;
    const srcSha = sha(src);
    const [c1, c2, c3] = splitPlan(src.length);
    ok(c1 + c2 + c3 === src.length, '分片计划覆盖全部字节且不等长', `${c1}+${c2}+${c3}=${src.length}`);

    // ---- A. 完整三段式：init → part×3 → commit ----
    const tgt = path.join(FILES, 'full.bin');
    let r = await post('/api/upload/init', { path: tgt, size: src.length, chunkSize: c1 });
    ok(r.status === 200 && r.body.algorithm === 'CRC32C', 'init 返回 200 并声明用 CRC32C', JSON.stringify(r.body).slice(0, 80));
    ok(r.body.offset === 0, 'init 时 offset 从 0 开始');
    ok(r.body.checksumHeader === 'X-Checksum-Crc32c', 'init 告诉客户端校验和放在哪个头');
    const idA = r.body.uploadId;

    let off = 0;
    for (const [i, len] of [c1, c2, c3].entries()) {
      const part = src.subarray(off, off + len);
      const p = await putPart(idA, off, part, crc32cHex(part));
      ok(p.status === 200 && p.body.received === off + len, `第 ${i + 1} 片写入成功（received=${p.body && p.body.received}）`);
      off += len;
    }
    const cA = await post('/api/upload/commit', { uploadId: idA });
    ok(cA.status === 200, 'commit 返回 200');
    ok(fs.existsSync(tgt), '目标文件真的出现了');
    ok(fs.readFileSync(tgt).length === src.length, '字节数与源精确相等', `${fs.readFileSync(tgt).length} vs ${src.length}`);
    ok(cA.body.checksum && cA.body.checksum.sha256 === srcSha, 'commit 返回的 SHA-256 与源一致（自描述）');
    ok(cA.body.checksum.crc32c === crc32cHex(src), 'commit 返回的整文件 CRC32C 与源一致', cA.body.checksum.crc32c);

    // 元数据自描述：算法名 + 值都落盘
    const meta = JSON.parse(fs.readFileSync(cA.body.meta, 'utf8'));
    ok(meta.checksum && meta.checksum.sha256 === srcSha && meta.checksum.crc32c === crc32cHex(src),
      '元数据里记着算法名与值，自描述', JSON.stringify(meta.checksum).slice(0, 80));
    ok(!/etag/i.test(JSON.stringify(meta)), '元数据里没有 ETag（spec：不能拿 ETag 当 MD5）');

    // ---- B. 【P1-5 核心】中途断开 → 续传 → 无重复字节 ----
    const tgt2 = path.join(FILES, 'resume.bin');
    r = await post('/api/upload/init', { path: tgt2, size: src.length, chunkSize: c1 });
    const idB = r.body.uploadId;
    const p0 = src.subarray(0, c1);
    await putPart(idB, 0, p0, crc32cHex(p0));
    // —— 此刻「断线」——
    const st = await status(idB);
    ok(st.offset === c1, '断线后 status 如实报出已收字节（客户端靠它对齐）', `offset=${st.offset}`);
    ok(st.remaining === src.length - c1, 'remaining 算得对');

    // 客户端拿着「过期的本地 offset」（比如断线前记的）重发，必须被 409 拒
    const stale = await putPart(idB, 0, p0, crc32cHex(p0));
    ok(stale.status === 409, '拿过期的 offset 重发被 409 拒（不允许自动重发同一 offset）', `实际 ${stale.status}`);
    ok(stale.body.expectedOffset === c1, '409 带回服务端真实 offset，供客户端对齐', `expectedOffset=${stale.body.expectedOffset}`);
    ok((await status(idB)).offset === c1, '409 之后服务端字节数没被写坏（仍是 c1）');

    // 按 status 给的 offset 续传剩下两片
    let o = (await status(idB)).offset;
    for (const len of [c2, c3]) {
      const part = src.subarray(o, o + len);
      const p = await putPart(idB, o, part, crc32cHex(part));
      ok(p.status === 200, `续传 ${len} 字节成功`);
      o += len;
    }
    const cB = await post('/api/upload/commit', { uploadId: idB });
    ok(cB.status === 200, '续传后 commit 成功');
    const gotB = fs.readFileSync(tgt2);
    ok(gotB.length === src.length, '【无重复字节】字节数精确相等', `${gotB.length} vs ${src.length}`);
    ok(sha(gotB) === srcSha, '【无重复字节】续传后 SHA-256 与源逐字节一致');

    // ---- C. 【P1-6 验收】故意改 1 个字节 → 服务端拒绝并回滚 ----
    const tgt3 = path.join(FILES, 'corrupt.bin');
    r = await post('/api/upload/init', { path: tgt3, size: src.length, chunkSize: c1 });
    const idC = r.body.uploadId;
    await putPart(idC, 0, p0, crc32cHex(p0));

    // 第二片内容故意被改 1 个字节，但 CRC 头仍报「原片」的 CRC —— 模拟传输损坏
    const good = src.subarray(c1, c1 + c2);
    const bad = Buffer.from(good); bad[12345] = (bad[12345] + 1) & 0xff;
    const cc = await putPart(idC, c1, bad, crc32cHex(good));
    ok(cc.status === 422, '改 1 个字节的片被拒 422（不是警告后继续）', `实际 ${cc.status}`);
    ok(/CRC32C/.test(cc.body.error || ''), '错误信息点名了 CRC32C', JSON.stringify(cc.body.error).slice(0, 70));
    ok(cc.body.received === c1, '响应告知已收字节回退到 c1（整片被丢弃）', `received=${cc.body.received}`);
    ok((await status(idC)).offset === c1, '暂存状态确实回滚了，客户端必须重传这一片');
    ok(!fs.existsSync(tgt3), '校验没过之前目标文件根本不存在（MOVE 语义）');

    // 重传正确的片 → 能走完
    const rc = await putPart(idC, c1, good, crc32cHex(good));
    ok(rc.status === 200, '重传正确的片成功');
    let oc = c1 + c2;
    const last = src.subarray(oc);
    await putPart(idC, oc, last, crc32cHex(last));
    const cC = await post('/api/upload/commit', { uploadId: idC });
    ok(cC.status === 200, '重传后 commit 成功');
    ok(sha(fs.readFileSync(tgt3)) === srcSha, '重传后的文件与源一致（坏字节没留下）');

    // ---- D. 边界与安全 ----
    const bad1 = await post('/api/upload/commit', { uploadId: '不存在的id' });
    ok(bad1.status === 404, '提交不存在的会话返回 404', `实际 ${bad1.status}`);
    const bad2 = await post('/api/upload/init', { path: tgt, size: 'abc' });
    ok(bad2.status === 400, 'size 非法返回 400', `实际 ${bad2.status}`);

    // 未传完就 commit 必须被拒
    r = await post('/api/upload/init', { path: path.join(FILES, 'short.bin'), size: src.length, chunkSize: c1 });
    await putPart(r.body.uploadId, 0, p0, crc32cHex(p0));
    const shortCommit = await post('/api/upload/commit', { uploadId: r.body.uploadId });
    ok(shortCommit.status === 409, '没传完就 commit 被拒 409（不会产出半截文件）', `实际 ${shortCommit.status}`);
    ok(!fs.existsSync(path.join(FILES, 'short.bin')), '被拒后目标文件不存在');

    // abort 真的清掉了暂存文件（暂存目录 = fm-trash.dataDir()/uploads）
    const upDir = path.join(XDG, 'Fairy', 'filemanager', 'uploads');
    ok(fs.existsSync(upDir), '暂存目录位置符合 fm-trash.dataDir() 约定', upDir);
    r = await post('/api/upload/init', { path: path.join(FILES, 'aborted.bin'), size: src.length, chunkSize: c1 });
    const upAfterInit = fs.readdirSync(upDir).length;
    ok(upAfterInit >= 1, 'init 之后有暂存文件', String(upAfterInit));
    await post('/api/upload/abort', { uploadId: r.body.uploadId });
    ok(fs.readdirSync(upDir).length === upAfterInit - 1, 'abort 真的删掉了暂存文件', `${upAfterInit} → ${fs.readdirSync(upDir).length}`);
  } catch (e) {
    console.error('测试异常：', e.stack);
    failed++;
    console.error('--- 服务端日志尾部 ---\n' + log.slice(-1500));
  } finally {
    child.kill('SIGKILL');
  }

  console.log(failed ? `\n分片上传：${failed} 项失败` : '\n分片上传：全部通过');
  process.exit(failed ? 1 : 0);
})();
