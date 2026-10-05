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
const UP_MAX_RETRY = 5;

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
async function __fmChunkedUpload(file, dir, progressEl) {
  const size = file.size;
  let rec = _upLoadStore()[_upKey(dir, file)];
  let uploadId = null, offset = 0, resumed = false;

  if (rec && rec.uploadId) {
    // 上次断过。先问服务端还剩多少——本地记的 offset 只是**线索**，不是事实。
    const off = await _upAlign(rec.uploadId).catch(() => null);
    if (off !== null && off < size) { uploadId = rec.uploadId; offset = off; resumed = true; }
    else _upForget(dir, file);
  }

  if (!uploadId) {
    const r = await fetch(UP_API + '/upload/init', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ path: dir + '/' + file.name, size, chunkSize: UP_CHUNK_SIZE }),
    });
    const j = await r.json().catch(() => null);
    if (!r.ok) return { ok: false, offset: 0, error: (j && j.error) || ('init 失败 ' + r.status) };
    uploadId = j.uploadId; offset = j.offset || 0;
  }
  _upRemember(dir, file, { uploadId, size, name: file.name });

  let fail = 0;
  while (offset < size) {
    const end = Math.min(offset + UP_CHUNK_SIZE, size);
    let sent = false;
    let lastErr = null;

    for (let attempt = 0; attempt < UP_MAX_RETRY && !sent; attempt++) {
      const buf = new Uint8Array(await file.slice(offset, end).arrayBuffer());
      try {
        const r = await fetch(UP_API + '/upload/part?uploadId=' + encodeURIComponent(uploadId) + '&offset=' + offset, {
          method: 'PUT', headers: { 'Content-Type': 'application/octet-stream', 'X-Checksum-Crc32c': crc32cHex(buf) },
          body: buf,
        });
        if (r.ok) {
          const j = await r.json().catch(() => null);
          offset = (j && typeof j.received === 'number') ? j.received : end;
          sent = true; fail = 0;
          _upProgress(progressEl, file.name + ' ' + Math.floor(offset / size * 100) + '%' + (resumed && offset < size ? '（续传）' : ''));
          break;
        }
        if (r.status === 409) {
          // 位置不对：按 spec 先对齐，再从服务端说的地方继续。
          const off = await _upAlign(uploadId);
          if (off === null) { lastErr = '会话已失效'; break; }
          offset = off;
          lastErr = null;
          continue; // 立刻用新的 offset 重试这一轮
        }
        if (r.status === 422) {
          // CRC 不符：整片已被服务端丢弃并回滚，从回滚后的位置重发。
          const j = await r.json().catch(() => null);
          if (j && typeof j.received === 'number') offset = j.received;
          lastErr = (j && j.error) || 'CRC 校验失败';
          continue;
        }
        const j = await r.json().catch(() => null);
        lastErr = (j && j.error) || ('HTTP ' + r.status);
      } catch (e) {
        // 网络断了。下一轮先对齐再决定从哪继续。
        lastErr = '网络中断';
        const off = await _upAlign(uploadId).catch(() => null);
        if (off !== null) offset = off;
        else break;
      }
      if (attempt < UP_MAX_RETRY - 1) await new Promise((r2) => setTimeout(r2, 300 * (attempt + 1)));
    }

    if (!sent) {
      fail++;
      if (fail >= 2) return { ok: false, offset, uploadId, error: lastErr || '分片上传失败', resumed };
      continue;
    }
  }

  const c = await fetch(UP_API + '/upload/commit', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ uploadId }),
  });
  const cj = await c.json().catch(() => null);
  if (!c.ok) return { ok: false, offset, uploadId, error: (cj && cj.error) || ('commit 失败 ' + c.status), resumed };
  _upForget(dir, file);
  return { ok: true, offset, uploadId, resumed, checksum: cj && cj.checksum };
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
