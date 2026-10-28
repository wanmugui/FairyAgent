// 并发分片上传（parallel 模式）专项测试。
//
// 【为什么单独一个文件】upload.test.cjs 覆盖的是串行路径（offset 必须严格递增，
// 哈希增量累积）。并发是**另一套协议**：乱序到达、区间去重、commit 时重算哈希。
// 两套协议的判据不同，混在一个文件里会让失败时难以定位到底是哪条路径坏了。
//
// 【核心不变量】并发不改变正确性，只改变到达顺序。落盘字节必须与源文件
// 逐字节一致——这正是「乱序 + 增量哈希」最容易悄悄破坏的地方。
const { spawn } = require('child_process');
const fs = require('fs');
const os = require('os');
const path = require('path');
const crypto = require('crypto');

const PORT = 8097;
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
async function waitUp(t = 25000) {
  const t0 = Date.now();
  while (Date.now() - t0 < t) {
    try { const r = await fetch(BASE + '/api/usage'); if (r.ok) return true; } catch { /* 等 */ }
    await sleep(300);
  }
  return false;
}

(async () => {
  const TMP = fs.mkdtempSync(path.join(os.tmpdir(), 'fm-uppar-'));
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
    if (!await waitUp()) { console.log('FAIL 服务未启动\n' + log.slice(-2000)); process.exit(1); }
    console.log('\n=== 并发分片上传（parallel 模式）===\n');

    // ---- 1. init 接受并回传 parallel 标记 ----
    console.log('[1] init 透传 parallel');
    const data = crypto.randomBytes(6 * 1024 * 1024);
    const srcSha = sha(data);
    const CH = 1024 * 1024;                 // 1MB 一片 → 6 片，足够制造乱序
    let r = await post('/api/upload/init', {
      path: path.join(FILES, 'parallel.bin'), size: data.length, chunkSize: CH, parallel: true,
    });
    ok(r.status === 200, 'init(parallel) 返回 200', `status=${r.status}`);
    ok(r.body && r.body.parallel === true, '响应回传 parallel=true', JSON.stringify(r.body && r.body.parallel));
    const sid = r.body && r.body.uploadId;
    ok(!!sid, '拿到 uploadId');

    // ---- 2. 乱序并发 PUT 全部 200 ----
    console.log('\n[2] 乱序并发 PUT 6 片（顺序倒过来发）');
    const parts = [];
    for (let o = 0, i = 0; o < data.length; o += CH, i++) {
      parts.push({ i, off: o, buf: data.subarray(o, Math.min(o + CH, data.length)) });
    }
    const shuffled = parts.slice().reverse();      // 完全倒序：第 6 片先到
    let inflight = 0, peak = 0;
    const res = await Promise.all(shuffled.map(async (p) => {
      inflight++; peak = Math.max(peak, inflight);
      const out = await putPart(sid, p.off, p.buf, crc32cHex(p.buf));
      inflight--;
      return out;
    }));
    ok(res.every(x => x.status === 200), '乱序 6 片全部 200',
      res.map(x => x.status).join(','));
    ok(peak > 1, '请求确实并发（峰值 in-flight > 1）', `peak=${peak}`);

    // ---- 3. commit 成功且落盘逐字节一致 ----
    console.log('\n[3] commit 与落盘一致性');
    r = await post('/api/upload/commit', { uploadId: sid });
    ok(r.status === 200, 'commit 返回 200', JSON.stringify(r.body).slice(0, 200));
    const saved = path.join(FILES, 'parallel.bin');
    ok(fs.existsSync(saved), '落盘文件存在');
    if (fs.existsSync(saved)) {
      const got = fs.readFileSync(saved);
      ok(got.length === data.length, '落盘字节数一致', `${got.length} vs ${data.length}`);
      ok(sha(got) === srcSha, '落盘 SHA-256 与源逐字节一致');
      ok(r.body && r.body.sha256 === srcSha, 'commit 返回的 sha256 与源一致',
        `server=${r.body && r.body.sha256} src=${srcSha}`);
    }

    // ---- 4. 缺片时 commit 必须拒绝 ----
    console.log('\n[4] 缺片 commit 被拒（不能静默产出坏文件）');
    r = await post('/api/upload/init', {
      path: path.join(FILES, 'gap.bin'), size: data.length, chunkSize: CH, parallel: true,
    });
    const gid = r.body.uploadId;
    // 只发 5 片，漏掉最后一片
    await Promise.all(parts.slice(0, 5).map(p => putPart(gid, p.off, p.buf, crc32cHex(p.buf))));
    r = await post('/api/upload/commit', { uploadId: gid });
    ok(r.status !== 200, '缺片 commit 被拒', `status=${r.status}`);
    ok(!fs.existsSync(path.join(FILES, 'gap.bin')), '缺片文件没有落盘');

    // ---- 5. 并行模式下 CRC 失败不得破坏已落盘的其他分片 ----
    // 这是并行最容易出事的点：串行模式 CRC 失败会 truncate 回 offset，
    // 并行模式下 offset 之后可能已经躺着别的并发分片，一刀切会抹掉别人的数据。
    console.log('\n[5] 并行模式 CRC 失败不误伤已落盘分片');
    r = await post('/api/upload/init', {
      path: path.join(FILES, 'crc.bin'), size: data.length, chunkSize: CH, parallel: true,
    });
    const cid = r.body.uploadId;
    // 先落最后一片（offset 最大），再发一片 CRC 错的
    await putPart(cid, parts[5].off, parts[5].buf, crc32cHex(parts[5].buf));
    const bad = putPart(cid, parts[0].off, parts[0].buf, 'deadbeef');
    const badRes = await bad;
    ok(badRes.status === 422 || badRes.status === 400, 'CRC 错误被拒', `status=${badRes.status}`);
    // 补齐其余片后 commit，应能成功（证明高 offset 的片没被 truncate 掉）
    await Promise.all(parts.slice(1, 5).map(p => putPart(cid, p.off, p.buf, crc32cHex(p.buf))));
    // 重传被拒的那一片（真实客户端的重试行为）
    await putPart(cid, parts[0].off, parts[0].buf, crc32cHex(parts[0].buf));
    r = await post('/api/upload/commit', { uploadId: cid });
    ok(r.status === 200, 'CRC 失败重传后 commit 成功', `status=${r.status}`);
    if (r.status === 200) {
      const g = fs.readFileSync(path.join(FILES, 'crc.bin'));
      ok(sha(g) === srcSha, 'CRC 重试后落盘 SHA-256 与源一致');
    }

    // ---- 6. 串行模式行为未变（回归护栏）----
    console.log('\n[6] 串行模式行为未变（回归护栏）');
    r = await post('/api/upload/init', {
      path: path.join(FILES, 'seq.bin'), size: data.length, chunkSize: CH,
    });
    const qid = r.body.uploadId;
    ok(!(r.body && r.body.parallel), '未声明时 parallel 为 false');
    await putPart(qid, 0, parts[0].buf, crc32cHex(parts[0].buf));
    // 串行模式下 offset 不接续必须 409
    const gapRes = await putPart(qid, parts[2].off, parts[2].buf, crc32cHex(parts[2].buf));
    ok(gapRes.status === 409, '串行模式跳 offset 仍返回 409', `status=${gapRes.status}`);

    // ---- 7. 重复 PUT 同一片不虚增 covered ----
    console.log('\n[7] 重复重传不虚增覆盖度');
    r = await post('/api/upload/init', {
      path: path.join(FILES, 'dup.bin'), size: 2 * CH, chunkSize: CH, parallel: true,
    });
    const did = r.body.uploadId;
    const p0 = data.subarray(0, CH), p1 = data.subarray(CH, 2 * CH);
    await putPart(did, 0, p0, crc32cHex(p0));
    // 同一片重传 3 次
    await putPart(did, 0, p0, crc32cHex(p0));
    await putPart(did, 0, p0, crc32cHex(p0));
    await putPart(did, 0, p0, crc32cHex(p0));
    await putPart(did, CH, p1, crc32cHex(p1));
    r = await post('/api/upload/commit', { uploadId: did });
    ok(r.status === 200, '重传 3 次后 commit 仍成功（covered 未被虚增）', `status=${r.status}`);
    if (r.status === 200) {
      const g = fs.readFileSync(path.join(FILES, 'dup.bin'));
      ok(g.length === 2 * CH, '重传后字节数正确', `${g.length}`);
      ok(sha(g) === sha(data.subarray(0, 2 * CH)), '重传后内容与源一致');
    }

  } catch (e) {
    // 必须连服务端日志一起打：只报 "fetch failed" 的话，
    // 根本分不清是测试等待逻辑的问题，还是被测服务自己崩了。
    console.log('FAIL 测试异常: ' + e.stack);
    console.log('--- 服务端日志（崩溃原因通常在这里）---\n' + log.slice(-3000));
    failed++;
  } finally {
    child.kill();
    try { fs.rmSync(TMP, { recursive: true, force: true }); } catch { /* 清理 */ }
  }

  console.log(`\n${failed === 0 ? '全部通过' : failed + ' 项失败'}`);
  process.exit(failed === 0 ? 0 : 1);
})();
