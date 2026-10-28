// 前端上传器测试：在 Node 里用最小 DOM stub 驱动真实的 fm-upload.js。
//
// 【为什么必须测前端】fm-upload.js 之前从没有任何自动化测试，
// 所以「拖拽面板进度永远停在 0%」这个 bug 能一直躺着——
// 它在测试里不会失败，因为它根本没被测过。
//
// 【测的是真代码，不是复刻】这里把 fm-upload.js 原文用 vm 跑起来，
// 只对 document / localStorage / requestAnimationFrame 打桩。
// 断言直接打真实上传路径，包括落盘后的 SHA-256 比对。
const { spawn } = require('child_process');
const fs = require('fs');
const os = require('os');
const path = require('path');
const vm = require('vm');
const crypto = require('crypto');

const PORT = 8098;
const BASE = `http://127.0.0.1:${PORT}`;
const ROOT = path.resolve(__dirname, '..');

let failed = 0;
const ok = (c, n, x) => { console.log((c ? '  ok   ' : '  FAIL ') + n + (x ? `  — ${x}` : '')); if (!c) failed++; };
const sleep = (ms) => new Promise(r => setTimeout(r, ms));
const sha = (b) => crypto.createHash('sha256').update(b).digest('hex');

/**
 * 最小 DOM stub。只实现 fm-upload.js 实际碰到的那几个 API，
 * 多写没用的桩只会让测试本身变得难验证。
 */
function makeSandbox({ parallel } = {}) {
  const store = new Map();
  const el = (id) => ({
    id, _html: '', style: {}, className: '',
    get innerHTML() { return this._html; },
    set innerHTML(v) { this._html = v; },
    appendChild() {}, remove() {}, addEventListener() {}, removeEventListener() {},
  });
  const els = { 'progress-text': el('progress-text') };
  const rafQueue = [];
  const sandbox = {
    console,
    setTimeout, clearTimeout, setInterval, clearInterval,
    AbortController, URL, URLSearchParams, Uint8Array, ArrayBuffer, Blob, Request, Response,
    TextDecoder, TextEncoder, Number, Math, JSON, Date, Object, Array, String, Boolean, Error, Promise,
    // 浏览器里相对 URL 由页面 origin 解析；Node 的 fetch 没有 base，
    // 直接喂 '/api/upload/init' 会报 Failed to parse URL。补上基准 origin。
    fetch: (...a) => {
      const url = a[0].startsWith('/') ? BASE + a[0] : a[0];
      const init = a[1];
      if (init && 'signal' in init && init.signal === null) init.signal = undefined;
      return fetch(url, init);
    },
    crypto: { randomUUID: () => crypto.randomUUID() },
    localStorage: {
      getItem: (k) => (store.has(k) ? store.get(k) : null),
      setItem: (k, v) => store.set(k, String(v)),
      removeItem: (k) => store.delete(k),
    },
    document: {
      getElementById: (id) => els[id] || null,
      createElement: () => el('created'),
      body: { appendChild() {} },
    },
    requestAnimationFrame: (fn) => { rafQueue.push(fn); return rafQueue.length; },
    localStorageSet: store,
  };
  sandbox.window = sandbox;
  sandbox.globalThis = sandbox;
  sandbox.requestAnimationFrame = sandbox.requestAnimationFrame;
  if (parallel !== undefined) store.set('fm.upload.parallel', String(parallel));

  const src = fs.readFileSync(path.join(__dirname, 'fm-upload.js'), 'utf8');
  vm.createContext(sandbox);
  vm.runInContext(src, sandbox, { filename: 'fm-upload.js' });
  // 节流用的是 rAF，测试里手动把排队的帧跑掉，否则回调永远不触发。
  sandbox.__flushRaf = () => { const q = rafQueue.splice(0); for (const fn of q) fn(); };
  return sandbox;
}

/**
 * 造一个替身 File —— 浏览器 File 的最小必要面。
 *
 * 【关键：必须是真正的 Blob 实例，不能是普通对象】
 * 直传快通道是 `fetch(url, {body: file})`。浏览器把 File 当可读流发送；
 * Node 的 undici 靠 brand check 识别 Blob，普通对象会被 String() 成
 * "[object Object]" —— 实测服务端只收到 15 字节却返回 200。
 * 也就是说，用普通对象做替身测出来的是**假象**：接口看着成功，文件是坏的。
 * 这类「假绿」比失败更危险，所以这里让替身真正 instanceof Blob。
 */
function makeFile(name, bytes) {
  const blob = new Blob([bytes]);
  // 挂 name：真实 File 有，Blob 没有；上传器用它做 key 和落盘名。
  Object.defineProperty(blob, 'name', { value: name, enumerable: true });
  return blob;
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
  const TMP = fs.mkdtempSync(path.join(os.tmpdir(), 'fm-upfe-'));
  const XDG = path.join(TMP, 'xdg');
  const FILES = path.join(TMP, 'files');
  fs.mkdirSync(FILES, { recursive: true });

  const child = spawn(process.execPath, [path.join(ROOT, 'frontend', 'server.cjs'), String(PORT)], {
    cwd: path.join(ROOT, 'frontend'),
    env: { ...process.env, XDG_DATA_HOME: XDG, FM_UPLOAD_TTL_MS: '600000' },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  let log = ''; child.stdout.on('data', d => { log += d; }); child.stderr.on('data', d => { log += d; });

  // 相对路径 <-> 绝对路径：前端用 /api/fs/... 走相对路径，测试要读到落盘文件
  // init 的 path 走绝对路径（后端 resolveFMRoot 直接用文件系统路径），
  // 而旧的 /upload 直传快通道用的是 URL 形式的 dir 相对路径 —— 两者不能混。
  const REL = FILES;

  try {
    if (!await waitUp()) { console.log('FAIL 服务未启动\n' + log.slice(-2000)); process.exit(1); }
    console.log('\n=== 前端上传器：小数进度 + 并发分片 ===\n');

    // ---- 1. 并发上传，落盘必须逐字节一致 ----
    console.log('[1] 并发度=4 上传 24MB，校验落盘');
    const sb = makeSandbox({ parallel: 4 });
    const size = 24 * 1024 * 1024;
    const data = crypto.randomBytes(size);
    const srcSha = sha(data);
    const seen = [];
    const rafTick = setInterval(() => sb.__flushRaf(), 5);
    const t0 = Date.now();
    const res = await sb.__fmChunkedUpload(makeFile('big.bin', data), REL, null, undefined, (p) => { seen.push(p); });
    clearInterval(rafTick);
    const dt = (Date.now() - t0) / 1000;
    ok(res.ok === true, '上传成功', res.ok ? `${dt.toFixed(1)}s` : String(res.error));
    const saved = path.join(FILES, 'big.bin');
    ok(fs.existsSync(saved), '落盘文件存在');
    if (fs.existsSync(saved)) {
      const got = fs.readFileSync(saved);
      ok(got.length === size, '字节数一致', `${got.length}`);
      ok(sha(got) === srcSha, 'SHA-256 与源逐字节一致');
    }

    // ---- 2. 进度回调真的被调用，且是小数 ----
    console.log('\n[2] 进度回调：小数 + 绝对量');
    ok(seen.length > 0, 'onProgress 被调用', `共 ${seen.length} 次`);
    const mid = seen.filter(p => parseFloat(p.pct) > 0 && parseFloat(p.pct) < 100);
    ok(mid.length > 0, '存在非 0 非 100 的中间进度', `${mid.length} 个`);
    const sample = mid[Math.floor(mid.length / 2)];
    ok(/\d+\.\d%/.test(sample.pct), '百分比带一位小数', `pct=${sample.pct}`);
    // 关键回归点：3.5GB 量级下整数百分比会长时间不动，这里锁死「必须有非整数刻度」
    const hasDecimal = seen.some(p => /\.\d%/.test(p.pct) && !/\.0%/.test(p.pct));
    ok(hasDecimal, '存在非整数百分比刻度（不只是 .0%）');
    ok(/\d+(\.\d+)?\s?(B|KB|MB|GB)\s?\/\s?\d+(\.\d+)?\s?(B|KB|MB|GB)/.test(sample.text),
       '文本含「已传 / 总量」', sample.text);
    const last = seen[seen.length - 1];
    ok(parseFloat(last.pct) === 100 || parseFloat(last.pct) > 99, '收尾进度接近 100', last.pct);
    // 进度必须单调不减：并发乱序完成时如果直接用 in-flight 就会回退，视觉上是抖动的
    let mono = true, prev = -1;
    for (const p of seen) { const v = parseFloat(p.pct); if (v < prev - 0.001) { mono = false; break; } prev = Math.max(prev, v); }
    ok(mono, '进度单调不减（并发乱序不回退）');

    // ---- 3. 并发度=1 时退回串行，且结果一致 ----
    console.log('\n[3] 并发度=1 退回串行路径');
    const sb1 = makeSandbox({ parallel: 1 });
    const data1 = crypto.randomBytes(12 * 1024 * 1024);
    const seen1 = [];
    const t1 = setInterval(() => sb1.__flushRaf(), 5);
    const r1 = await sb1.__fmChunkedUpload(makeFile('seq.bin', data1), REL, null, undefined, (p) => { seen1.push(p); });
    clearInterval(t1);
    ok(r1.ok === true, '串行上传成功', r1.ok ? '' : String(r1.error));
    const s1 = path.join(FILES, 'seq.bin');
    ok(fs.existsSync(s1) && sha(fs.readFileSync(s1)) === sha(data1), '串行落盘 SHA-256 一致');
    ok(r1.resumed === false, '未误判为续传');

    // ---- 4. 并发实测真的 > 1（不是假并发）----
    console.log('\n[4] 并发实测 > 1');
    let inflight = 0, peak = 0;
    const realFetch = globalThis.fetch;
    globalThis.fetch = async (...a) => {
      const url = String(a[0]);
      if (url.includes('/upload/part')) { inflight++; peak = Math.max(peak, inflight); }
      try { return await realFetch(...a); } finally { if (url.includes('/upload/part')) inflight--; }
    };
    const sb4 = makeSandbox({ parallel: 4 });
    const data4 = crypto.randomBytes(20 * 1024 * 1024);
    const t4 = setInterval(() => sb4.__flushRaf(), 5);
    const r4 = await sb4.__fmChunkedUpload(makeFile('conc.bin', data4), REL, null, undefined, () => {});
    clearInterval(t4);
    globalThis.fetch = realFetch;
    ok(r4.ok === true, '并发上传成功', r4.ok ? '' : String(r4.error));
    ok(peak > 1, '实测 in-flight 分片 > 1（确实并发）', `peak=${peak}`);
    const s4 = path.join(FILES, 'conc.bin');
    ok(fs.existsSync(s4) && sha(fs.readFileSync(s4)) === sha(data4), '并发落盘 SHA-256 一致');

    // ---- 5. 小文件走直传快通道 ----
    console.log('\n[5] 小文件直传不受影响');
    const sb5 = makeSandbox({ parallel: 4 });
    const small = crypto.randomBytes(1024);
    const seen5 = [];
    const t5 = setInterval(() => sb5.__flushRaf(), 5);
    const r5 = await sb5.__fmChunkedUpload(makeFile('small.bin', small), REL, null, undefined, (p) => { seen5.push(p); });
    clearInterval(t5);
    // 小文件只有「开始」和「完成」两次 emit，而节流把同一帧内的多次合并成一次。
    // 不手动排干 rAF 队列，最后一次进度就永远停在 pending 里看不到。
    sb5.__flushRaf();
    ok(r5.ok === true, '小文件上传成功', r5.ok ? '' : String(r5.error) + ' | 落盘=' + fs.readdirSync(FILES).join(','));
    // 直传走 unique=1：同名文件不会被覆盖，服务端会自动改名。
    // 所以不能按原名找落盘文件，得用服务端返回的真实路径/文件名。
    const s5 = (r5 && r5.path) ? r5.path : path.join(FILES, 'small.bin');
    const got5 = fs.existsSync(s5) ? fs.readFileSync(s5) : null;
    ok(!!got5 && sha(got5) === sha(small),
       '小文件落盘一致',
       'exists=' + !!got5 +
       ' 实际大小=' + (got5 ? got5.length : '-') + ' 期望=' + small.length +
       ' sha实际=' + (got5 ? sha(got5).slice(0,12) : '-') + ' sha期望=' + sha(small).slice(0,12));
    ok(seen5.length > 0, '小文件也有进度回调', `${seen5.length} 次`);

    // ---- 6. 并发度设置可读可写 ----
    console.log('\n[6] 并发度配置');
    const sb6 = makeSandbox();
    ok(sb6.__fmGetUploadParallel() === 4, '默认并发度 4', String(sb6.__fmGetUploadParallel()));
    ok(sb6.__fmSetUploadParallel(8) === 8, '可设为 8', String(sb6.__fmGetUploadParallel()));
    ok(sb6.__fmSetUploadParallel(999) === 16, '超上限被夹到 16', String(sb6.__fmGetUploadParallel()));
    ok(sb6.__fmSetUploadParallel(0) === 1, '下限夹到 1（退回串行）', String(sb6.__fmGetUploadParallel()));

  } catch (e) {
    console.log('FAIL 测试异常: ' + e.stack);
    failed++;
  } finally {
    child.kill();
    try { fs.rmSync(TMP, { recursive: true, force: true }); } catch { /* 清理 */ }
  }

  console.log(`\n${failed === 0 ? '全部通过' : failed + ' 项失败'}`);
  process.exit(failed === 0 ? 0 : 1);
})();
