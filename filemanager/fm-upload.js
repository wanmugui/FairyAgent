// 路径编码兜底：encodeURIComponent 会把 / 编成 %2F，nginx/Cloudflare 拒绝该形式，
// 公网表现为「打不开这个文件夹 HTTP 404」。这里按 / 分段编码，斜杠保持原样。
// index-fm.html 里已有同名实现，此处仅在它尚未加载时兜底，避免依赖脚本执行顺序。
if (typeof window.encPath !== 'function') {
  window.encPath = function (p) {
    if (p === undefined || p === null) return '';
    return String(p).split('/').map(function (seg) {
      return encodeURIComponent(seg);
    }).join('/');
  };
}
// P1-5 前端续传上传器。在 fm-lock.js 之后加载。
//
// 【为什么覆盖而不是改 index-fm.html 里的 upload()】第 705 行是
// `fileInput.addEventListener('change', upload)`——这会把当时的函数**引用**存进
// 监听器。之后再改 window.upload 也不会生效，那个监听器还握着旧函数。
// 所以这里先 removeEventListener 摘掉旧的（此刻 window.upload 就是它握着的那个
// 引用），再挂自己的。比改动 1400 行文件里那段老代码干净。
//
// 【大文件才走分片】小于阈值继续用原来的整文件路径：分片要算 CRC32C、要多几次
// 往返，为几百 KB 的图片付这个代价不划算。阈值以上必须改道——原来那条路是
// f.arrayBuffer() 整个读进内存，2GB 会直接 OOM。

const UP_API = (typeof API !== 'undefined' ? API : '/api');
const UP_CHUNK_THRESHOLD = 8 * 1024 * 1024; // 小于此值走原路径
const UP_CHUNK_SIZE = 4 * 1024 * 1024;
// 小文件单请求直传的体积上限。与 UP_CHUNK_THRESHOLD 同值：8MB 以下一次 PUT 传得完，
// 再大就交给分片协议（可续传、内存占用可控）。
const UP_DIRECT_MAX = 8 * 1024 * 1024;
const UP_MAX_RETRY = 5;

// 分片并发度。1 = 退回原来的串行行为。
//
// 【并发为什么可能更快】单条 TCP 流在长肥管道（高 RTT + 高带宽）上会被
// 拥塞窗口卡住：带宽时延积远大于窗口时，发送速率上不去，链路利用率很低。
// 拆成多条独立 TCP 流各自带一个窗口，总吞吐就能上去。这是「分段加速」的
// 真实物理基础，不是玄学。
//
// 【但也可能更慢】在局域网 / 本机回环上，磁盘和网络早就是瓶颈，多流只是
// 互相抢 CPU 和带宽，还多出 N-1 次 init 之外的调度开销。所以做成可配的，
// 并且允许用户在设置里调回 1。
//
// 【为什么默认 4 而不是 8】每个 in-flight 分片在浏览器里占 4MB 内存、
// 在服务端占 4MB 请求体。fm-drop.js 还会同时跑 8 个文件（MAX_CONCURRENCY=8），
// 8×8=64 路会把单线程 Node 服务端的内存和事件循环打满。4 是「提速明显」
// 与「不压垮服务端」之间较稳的折中。
const UP_PARALLEL_DEFAULT = 4;
let UP_PARALLEL = (function () {
  let v = UP_PARALLEL_DEFAULT;
  try {
    const raw = localStorage.getItem('fm.upload.parallel');
    if (raw !== null && raw !== undefined && raw !== '') {
      const n = parseInt(raw, 10);
      if (Number.isFinite(n)) v = Math.max(1, Math.min(16, n));
    }
  } catch (_) { /* 隐私模式等，忽略 */ }
  return v;
})();
// 运行时可改，供设置面板与测试使用
window.__fmSetUploadParallel = function (n) {
  // 显式判 NaN 而不是靠 `parseInt(x) || 默认值`：
  // 0 在这里既可能是「用户想要 0」也可能是「没设置」，用 || 会把 0 当成没设置
  // 悄悄回退到默认 4，用户以为自己设了 0（退回串行），实际跑的是 4 路。
  const parsed = parseInt(n, 10);
  const v = Math.max(1, Math.min(16, Number.isFinite(parsed) ? parsed : UP_PARALLEL_DEFAULT));
  UP_PARALLEL = v;
  try { localStorage.setItem('fm.upload.parallel', String(v)); } catch (_) { /* 忽略 */ }
  return v;
};
window.__fmGetUploadParallel = () => UP_PARALLEL;

// ---------------------------------------------------------------------------
// CRC32C（Castagnoli）——与后端 fm-upload.cjs 同一张表。
// 浏览器没有 Web Crypto 的 CRC，只有 SHA 系列；要「传输中校验」就得自己算。
// ---------------------------------------------------------------------------
const _crcTable = (() => {
  const t = new Uint32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) c = (c & 1) ? ((c >>> 1) ^ 0x82f63b78) : (c >>> 1);
    t[n] = c >>> 0;
  }
  return t;
})();

function crc32cHex(u8) {
  let c = 0xffffffff;
  for (let i = 0; i < u8.length; i++) c = _crcTable[(c ^ u8[i]) & 0xff] ^ (c >>> 8);
  return ((c ^ 0xffffffff) >>> 0).toString(16).padStart(8, '0');
}

function _upToast(msg, opts) {
  try { if (typeof window.__fmToast === 'function') return window.__fmToast(msg, opts); } catch (_) { /* 忽略 */ }
  console.warn('[upload] ' + msg);
}

function _upProgress(el, text) {
  const t = document.getElementById('progress-text');
  if (t) t.innerHTML = text;
  if (el) el.innerHTML = text;
}

// 大文件上传的进度展示。
//
// 【为什么要小数】4MB 分片 / 3.5GB 文件，每完成一片只推进 0.11%。
// 取整后要传满 9 片才跳 1%，而 9 片在千兆网上只要几秒——用户看到的就是
// 「卡在 0% 不动」，然后突然跳一大截。整数百分比在这个量级是有害的。
// 一位小数让每片都有可见增量，同时不会像两位那样抖得读不清。
//
// 【为什么要绝对量】纯百分比在超大文件上是失真的：99% 可能还剩 35MB。
// 并排给出「已传 / 总量」，用户能自己判断还要等多久，也便于对账。
function _upFmtPct(done, size) {
  if (!size) return '0.0%';
  return Math.min(100, done / size * 100).toFixed(1) + '%';
}

function _upHuman(n) {
  if (!Number.isFinite(n) || n < 0) return '?';
  const u = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (i === 0 ? n : n.toFixed(2)) + ' ' + u[i];
}

// 节流：并发分片下每片都回调，4 路 × 875 片 ≈ 3500 次/s，
// 不节流会把主线程刷爆，反而拖慢真正的上传。用 rAF 对齐到每帧一次。
function _upThrottle(fn) {
  let pending = null, raf = 0;
  const flush = () => { raf = 0; const p = pending; pending = null; if (p) fn(p); };
  return (v) => {
    pending = v;
    if (!raf) raf = (typeof requestAnimationFrame === 'function')
      ? requestAnimationFrame(flush) : setTimeout(flush, 60);
  };
}

// 进度统一出口。
//
// 【为什么必须有回调】原来进度只写 progressEl 和全局 #progress-text 两个 DOM。
// 拖拽上传面板走的是另一套渲染（fm-drop.js 里每行独立的 el），它把
// progressEl 传成了 null —— 于是所有进度更新都落在一个不存在的元素上，
// 面板那行从头到尾没人改。用户看到的「0% · 3.5 GB」静止 10 分钟是**真实行为**，
// 不是渲染太慢。传回调让每条 UI 路径都能挂自己的渲染逻辑，不用去猜全局 id。
//
// 【节流放在这里】所有 UI 调用方都拿到节流后的版本，各写各的不会漏。
function _upEmit(progressEl, onProgress) {
  const push = _upThrottle((s) => {
    const pct = _upFmtPct(s.done, s.size);
    // text 带文件名：旧浮层只有一个元素（progressEl / #progress-text），
    // 文件名和进度挤在同一行里，必须自带名字。
    const text = s.name + ' ' + pct +
      ' · ' + _upHuman(s.done) + ' / ' + _upHuman(s.size) + (s.tag || '');
    // bare 是**不含文件名**的进度文本。拖拽面板的每一行有自己的 .n 槽位显示文件名，
    // 再把名字塞进 .p 会重复一次，而且长文件名会把进度挤出面板 ——
    // 用户只能看到「...3rip.AAC.1080p 3.3% · 116.00 MB」这种尾部残留。
    // 所以给结构化 UI 单独提供 bare，text 只留给单元素旧浮层。
    const bare = pct + ' · ' + _upHuman(s.done) + ' / ' + _upHuman(s.size) + (s.tag || '');
    if (onProgress) { try { onProgress({ done: s.done, size: s.size, pct, text, bare }); } catch (_) { /* 渲染失败不该中断上传 */ } }
    if (progressEl) progressEl.innerHTML = text;
    const t = document.getElementById('progress-text');
    if (t) t.innerHTML = text;
  });
  return (done, size, name, tag) => push({ done, size, name, tag });
}

// ---------------------------------------------------------------------------
// 断点记录：让「关掉页面再回来还能续」成立，spec 明确要求客户端持久化 offset
// ---------------------------------------------------------------------------
const UP_STORE = 'fm-uploads';
function _upLoadStore() { try { return JSON.parse(localStorage.getItem(UP_STORE) || '{}'); } catch (_) { return {}; } }
function _upSaveStore(s) { try { localStorage.setItem(UP_STORE, JSON.stringify(s)); } catch (_) { /* 隐私模式，放弃持久化 */ } }
function _upKey(dir, file) { return JSON.stringify([dir, file.name, file.size, file.lastModified]); }
function _upRemember(dir, file, rec) { const s = _upLoadStore(); s[_upKey(dir, file)] = rec; _upSaveStore(s); }
function _upForget(dir, file) { const s = _upLoadStore(); delete s[_upKey(dir, file)]; _upSaveStore(s); }

/** 问服务端真实进度。spec 的硬要求：offset 打架时先对齐，绝不盲重发。 */
async function _upAlign(uploadId) {
  const r = await fetch(UP_API + '/upload/status?uploadId=' + encodeURIComponent(uploadId), { method: 'GET' });
  if (!r.ok) return null;
  const j = await r.json();
  return typeof j.offset === 'number' ? j.offset : null;
}

/**
 * 分片上传主体。带 409 对齐与有限重试。
 * @returns {{ok:boolean, offset:number, uploadId?:string, resumed?:boolean, error?:string}}
 */
async function __fmChunkedUpload(file, dir, progressEl, signal, onProgress) {
  const size = file.size;
  // 所有进度都从这里出去：拖拽面板的行走 onProgress，旧浮层走 progressEl。
  // 之前这两条路是断的——拖拽路径把 progressEl 传成 null，于是面板那行
  // 永远没人更新，3.5GB 传完前一直显示 0%。统一到一个出口才不会重演。
  const emit = _upEmit(progressEl, onProgress);

  // 【小文件快通道】小于 UP_DIRECT_MAX 时，整个文件一次 PUT 到 /upload，
  // 省掉 init / part / commit 三次往返。
  if (size <= UP_DIRECT_MAX) {
    emit(0, size, file.name, ' 0 B');
    let dr;
    try {
      dr = await fetch(UP_API + '/upload?path=' + encodeURIComponent(dir) +
                       '&name=' + encodeURIComponent(file.name) + '&unique=1',
                       { method: 'PUT', body: file, signal });
    } catch (e) {
      if (signal && signal.aborted) throw e;
      return { ok: false, offset: 0, error: String(e && e.message || e) };
    }
    const dj = await dr.json().catch(() => null);
    if (!dr.ok) return { ok: false, offset: 0, error: (dj && dj.error) || ('直传失败 ' + dr.status) };
    emit(size, size, file.name);
    // 直传走 unique=1，同名时服务端会自动改名，所以要回传**真实落盘名**，
    // 调用方才能刷新后指向正确的文件，而不是它请求时拼的那个原名。
    return { ok: true, offset: size, uploadId: null, resumed: false,
             name: (dj && (dj.name || dj.path)) || file.name,
             checksum: (dj && dj.checksum) || null };
  }

  let rec = _upLoadStore()[_upKey(dir, file)];
  let uploadId = null, offset = 0, resumed = false;

  if (rec && rec.uploadId) {
    // 上次断过。先问服务端还剩多少——本地记的 offset 只是**线索**，不是事实。
    const off = await _upAlign(rec.uploadId).catch(() => null);
    if (off !== null && off < size) { uploadId = rec.uploadId; offset = off; resumed = true; }
    else _upForget(dir, file);
  }

  // 并发只在「全新会话」上开。
  // 续传拿到的 uploadId 是旧协议建的，服务端按严格顺序记账，
  // 贸然并发发会撞 409 —— 续传路径老老实实走串行。
  const parallel = !resumed && UP_PARALLEL > 1;

  if (!uploadId) {
    const r = await fetch(UP_API + '/upload/init', {
      signal,
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        path: dir + '/' + file.name, size, chunkSize: UP_CHUNK_SIZE,
        // 显式告知服务端「我会乱序并发发」，服务端才肯放弃顺序记账。
        // 不传就是 false，服务端走老的严格顺序分支，旧客户端零影响。
        parallel,
      }),
    });
    const j = await r.json().catch(() => null);
    if (!r.ok) return { ok: false, offset: 0, error: (j && j.error) || ('init 失败 ' + r.status) };
    uploadId = j.uploadId; offset = j.offset || 0;
  }
  _upRemember(dir, file, { uploadId, size, name: file.name });
  emit(offset, size, file.name, resumed ? '（续传）' : '');

  if (parallel) {
    const res = await _upRunParallel(file, uploadId, size, signal, emit, resumed);
    if (!res.ok) return { ok: false, offset: res.done, uploadId, error: res.error, resumed };
    offset = size;
  } else {
    const res = await _upRunSerial(file, uploadId, size, signal, emit, offset, resumed);
    if (!res.ok) return { ok: false, offset: res.offset, uploadId, error: res.error, resumed };
    offset = res.offset;
  }

  const c = await fetch(UP_API + '/upload/commit', {
    signal,
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ uploadId }),
  });
  const cj = await c.json().catch(() => null);
  if (!c.ok) return { ok: false, offset, uploadId, error: (cj && cj.error) || ('commit 失败 ' + c.status), resumed };
  _upForget(dir, file);
  emit(size, size, file.name, resumed ? '（续传完成）' : '');
  return { ok: true, offset, uploadId, resumed, checksum: cj && cj.checksum };
}

// 串行分片：一次一片，offset 必须接续。原逻辑原样保留，只把进度换成带小数的。
async function _upRunSerial(file, uploadId, size, signal, emit, offset, resumed) {
  let fail = 0;
  while (offset < size) {
    const end = Math.min(offset + UP_CHUNK_SIZE, size);
    let sent = false;
    let lastErr = null;

    for (let attempt = 0; attempt < UP_MAX_RETRY && !sent; attempt++) {
      const buf = new Uint8Array(await file.slice(offset, end).arrayBuffer());
      try {
        const r = await fetch(UP_API + '/upload/part?uploadId=' + encodeURIComponent(uploadId) + '&offset=' + offset, {
          signal,
          method: 'PUT', headers: { 'Content-Type': 'application/octet-stream', 'X-Checksum-Crc32c': crc32cHex(buf) },
          body: buf,
        });
        if (r.ok) {
          const j = await r.json().catch(() => null);
          offset = (j && typeof j.received === 'number') ? j.received : end;
          sent = true; fail = 0;
          emit(offset, size, file.name, resumed && offset < size ? '（续传）' : '');
          break;
        }
        if (r.status === 409) {
          const off = await _upAlign(uploadId);
          if (off === null) { lastErr = '会话已失效'; break; }
          offset = off;
          lastErr = null;
          continue;
        }
        if (r.status === 422) {
          const j = await r.json().catch(() => null);
          if (j && typeof j.received === 'number') offset = j.received;
          lastErr = (j && j.error) || 'CRC 校验失败';
          continue;
        }
        const j = await r.json().catch(() => null);
        lastErr = (j && j.error) || ('HTTP ' + r.status);
      } catch (e) {
        if (signal && signal.aborted) throw e;   // 用户主动取消：立即退出，不再重试
        lastErr = '网络中断';
        const off = await _upAlign(uploadId).catch(() => null);
        if (off !== null) offset = off;
        else break;
      }
      if (attempt < UP_MAX_RETRY - 1) await new Promise((r2) => setTimeout(r2, 300 * (attempt + 1)));
    }

    if (!sent) {
      fail++;
      if (fail >= 2) return { ok: false, offset, error: lastErr || '分片上传失败' };
      continue;
    }
  }
  return { ok: true, offset };
}

// 并发分片：UP_PARALLEL 个 worker 抢同一个分片游标。
//
// 【为什么不能简单 for 循环 Promise.all】875 片一次性全发出去，
// 浏览器会排队 875 个请求，每个还占 4MB 内存，直接把标签页拖死。
// 必须是有界的 worker 池：固定 UP_PARALLEL 个 worker，各自 while 取下一个分片。
//
// 【为什么每片独立重试】服务端在并行模式下按「区间 + CRC」记账，
// 不依赖到达顺序，所以单片失败重传同一个 offset 是幂等的，
// 不会像串行模式那样需要重新对齐整个游标。
async function _upRunParallel(file, uploadId, size, signal, emit, resumed) {
  const total = Math.ceil(size / UP_CHUNK_SIZE);
  const width = Math.min(UP_PARALLEL, total);
  let done = 0;                 // 已确认落盘的字节数，只增不减
  let next = 0;                 // 下一个待发分片的下标
  const dead = [];              // 重试用尽仍失败的分片
  const inflight = new Set();   // in-flight 计数，仅用于观测

  const sendOne = async (idx) => {
    const off = idx * UP_CHUNK_SIZE;
    const end = Math.min(off + UP_CHUNK_SIZE, size);
    let sent = false, lastErr = null;

    for (let attempt = 0; attempt < UP_MAX_RETRY && !sent; attempt++) {
      if (signal && signal.aborted) return false;
      try {
        const buf = new Uint8Array(await file.slice(off, end).arrayBuffer());
        const r = await fetch(UP_API + '/upload/part?uploadId=' + encodeURIComponent(uploadId) + '&offset=' + off, {
          signal,
          method: 'PUT', headers: { 'Content-Type': 'application/octet-stream', 'X-Checksum-Crc32c': crc32cHex(buf) },
          body: buf,
        });
        if (r.ok) {
          done += end - off;
          sent = true;
          emit(done, size, file.name, resumed ? '（续传）' : '');
          return true;
        }
        const j = await r.json().catch(() => null);
        lastErr = (j && j.error) || ('HTTP ' + r.status);
        // 422 = CRC 不过，本片已被服务端判为未收到；并行模式不会 truncate
        // 高 offset 的分片，所以原地重传同一个 offset 就是正确的修复方式。
        if (r.status === 422 || r.status === 409) { /* 重试即可，不动游标 */ }
      } catch (e) {
        if (signal && signal.aborted) throw e;
        lastErr = '网络中断';
      }
      if (attempt < UP_MAX_RETRY - 1) await new Promise((r2) => setTimeout(r2, 300 * (attempt + 1)));
    }
    dead.push(idx);
    return false;
  };

  const worker = async () => {
    for (;;) {
      if (signal && signal.aborted) return;
      const idx = next++;
      if (idx >= total) return;
      inflight.add(idx);
      try { await sendOne(idx); } finally { inflight.delete(idx); }
    }
  };

  const pool = [];
  for (let i = 0; i < width; i++) pool.push(worker());
  await Promise.all(pool);

  if (dead.length) {
    return { ok: false, done, error: dead.length + ' 个分片重试 ' + UP_MAX_RETRY + ' 次仍失败' };
  }
  if (done !== size) {
    return { ok: false, done, error: '分片不完整：' + done + '/' + size + ' 字节' };
  }
  return { ok: true, done: size };
}

// ---------------------------------------------------------------------------
// 接管原来的 upload()：大文件改道分片，小文件保留原路径
// ---------------------------------------------------------------------------
const _oldUpload = window.upload;
async function __fmUpload(e) {
  const files = Array.from(e.target.files || []);
  const dir = (typeof currentPath !== 'undefined' && currentPath) ? currentPath : '';
  const progressDiv = document.createElement('div');
  progressDiv.style.cssText = 'position:fixed;top:50%;left:50%;transform:translate(-50%,-50%);background:#fff;padding:20px;border-radius:8px;box-shadow:0 4px 20px rgba(0,0,0,.2);z-index:9999';
  progressDiv.innerHTML = '<div style="margin-bottom:10px;">上传文件中...</div><div id="progress-text" style="color:#666;">0%</div>';
  document.body.appendChild(progressDiv);

  try {
    for (const f of files) {
      if (f.size < UP_CHUNK_THRESHOLD) { await _oldUpload({ target: { files: [f] } }); continue; }
      _upProgress(null, f.name + ': 分片上传 0%');
      let r;
      try { r = await __fmChunkedUpload(f, dir, null); }
      catch (e) { r = { ok: false, error: String(e && e.message || e) }; }
      if (r.ok) {
        _upProgress(null, f.name + ' ✓ 上传完成（' + Math.round(f.size / 1048576) + 'MB' + (r.resumed ? '，续传' : '') + '）');
        _upToast('已上传 ' + f.name + '，SHA-256 ' + ((r.checksum && r.checksum.sha256) || '').slice(0, 12));
      } else {
        _upProgress(null, f.name + ': 失败 - ' + (r.error || '未知错误'));
        _upToast('「' + f.name + '」上传失败：' + (r.error || '未知错误'), { bad: true, action: { label: '重试', onClick: () => __fmUpload({ target: { files: [f] } }) } });
      }
    }
    if (typeof loadFiles === 'function') loadFiles();
  } finally {
    setTimeout(() => progressDiv.remove(), 2500);
    e.target.value = '';
  }
}

window.__fmChunkedUpload = __fmChunkedUpload; // 供测试直接驱动
(function takeover() {
  const input = document.getElementById('fileInput');
  if (!input) return;
  if (typeof _oldUpload === 'function') input.removeEventListener('change', _oldUpload);
  input.addEventListener('change', __fmUpload);
  window.upload = __fmUpload;
})();
