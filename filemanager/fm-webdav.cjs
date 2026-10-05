// P4-1 WebDAV：把 filemanager 的目录树用 WebDAV 协议暴露出来，
// 这样 macOS「连接服务器」、Windows「映射网络驱动器」、rclone、各家手机/桌面
// App 都能直接读写，不用再单独装一个客户端。
//
// ── 安全边界（2026-10-03 主人已定）──────────────────────────────────────
//   1) 只绑 127.0.0.1 —— server.cjs 本来就只监听回环，这条是继承来的，
//      不是这里加的；换句话说外网根本连不进来。
//   2) 根目录白名单 —— 只有 FM_DAV_ROOTS 里列出的目录才可见。
//      这条**不能**照抄 filemanager.cjs 的 resolveFMRoot：那边对 FM 界面是
//      故意宽松的（同一个浏览器里，用户自己有权限打开任何绝对路径），
//      而 WebDAV 客户端是被别人指使的，恶意/误配的客户端会直接要
//      /etc/passwd。所以这里单独一套严格白名单。
//
//   白名单为空（FM_DAV_ROOTS=""）时整个端点关闭，不做任何事。
//
// ── 路径防穿越 ─────────────────────────────────────────────────────────
//   URL 段先 decode 再校验，逐段过 validateName（它拒绝 / \ . .. 和控制字符）；
//   之后再对**已存在的最深祖先**做 realpath 再判包含——这一步是为了挡软链：
//   只做字符串前缀比对的话，一个指向 /etc 的软链就能整块逃出白名单。
//
// 路由：/dav/            → 第一个白名单根
//       /dav/<根名>/...  → 其余白名单根（按 basename 挂虚拟集合）

'use strict';

const fs = require('fs');
const fsp = fs.promises;
const path = require('path');
const { validateName, atomicWriteStream } = require('./fm-path.cjs');
const trash = require('./fm-trash.cjs');

// Depth: infinity 的自我保护上限。RFC 4918 允许服务端拒绝，
// 我们拒绝时回 403 + DAV: propfind-finite-depth，客户端会自己降级成 1。
const MAX_DEPTH = 8;
const MAX_ENTRIES = 20000;

class DavError extends Error {
  constructor(status, message, extraHeaders) {
    super(message);
    this.status = status;
    this.extraHeaders = extraHeaders || null;
  }
}

// ---------------------------------------------------------------------------
// 白名单
// ---------------------------------------------------------------------------
function loadRoots() {
  const raw = process.env.FM_DAV_ROOTS;
  if (raw === undefined) {
    // 缺省只放 workspace/ —— filemanager 真正在管的产物目录。
    return [path.resolve(__dirname, '..', 'workspace')];
  }
  if (!raw.trim()) return []; // 显式置空 = 关闭
  const roots = [];
  const seen = new Map();
  for (const piece of raw.split(path.delimiter)) {
    const p = piece.trim();
    if (!p) continue;
    const abs = path.resolve(p);
    let real;
    try {
      real = fs.realpathSync(abs);
    } catch (_) {
      continue; // 配了但不存在的根直接跳过，不让整个端点挂掉
    }
    if (!fs.statSync(real).isDirectory()) continue;
    const name = path.basename(real);
    if (seen.has(name)) {
      // 两个根同名 → 虚拟目录名撞车，浏览器里没法区分，宁可少挂一个也不猜
      console.warn(`[dav] 根目录名重复，已跳过: ${real}（与 ${seen.get(name)} 冲突）`);
      continue;
    }
    seen.set(name, real);
    roots.push({ name, real, abs });
  }
  return roots;
}

let ROOTS = null;
function roots() {
  if (!ROOTS) ROOTS = loadRoots();
  return ROOTS;
}

/** 已存在的最深祖先做 realpath 后判包含，挡软链逃逸。 */
function containedReal(rootReal, abs) {
  let cur = abs;
  const tail = [];
  for (;;) {
    try {
      const real = fs.realpathSync(cur);
      const full = tail.length ? path.join(real, ...tail) : real;
      return full === rootReal || full.startsWith(rootReal + path.sep);
    } catch (e) {
      if (e.code !== 'ENOENT' && e.code !== 'ENOTDIR') throw e;
      const parent = path.dirname(cur);
      if (parent === cur) return false;
      tail.unshift(path.basename(cur));
      cur = parent;
    }
  }
}

// ---------------------------------------------------------------------------
// URL → 绝对路径
// ---------------------------------------------------------------------------
function resolveDav(rawPath) {
  const list = roots();
  if (!list.length) throw new DavError(404, 'WebDAV 未启用（FM_DAV_ROOTS 为空）');

  // 整条路径先按 / 切，**每段各自** decode —— 整条 decode 的话，
  // 一个段里的 %2F 会把分隔符也一起解出来，段校验就形同虚设。
  const rawSegs = String(rawPath || '').split('/').filter((s) => s !== '');
  const segs = [];
  for (const s of rawSegs) {
    let d;
    try {
      d = decodeURIComponent(s);
    } catch (_) {
      throw new DavError(400, 'URL 编码非法');
    }
    if (d === '.' || d === '..') throw new DavError(403, '路径穿越已拒绝');
    if (d.includes('/') || d.includes('\\') || d.includes('\0')) throw new DavError(403, '非法路径段');
    // validateName 失败时返回**原因字符串**，成功返回 null
    const reason = validateName(d);
    if (reason) throw new DavError(403, `文件名不合法: ${d}（${reason}）`);
    segs.push(d);
  }

  let root = list[0];
  let rest = segs;
  if (segs.length) {
    const named = list.find((r) => r.name === segs[0]);
    if (named) { root = named; rest = segs.slice(1); }
  }
  const abs = rest.length ? path.join(root.real, ...rest) : root.real;
  if (!containedReal(root.real, abs)) {
    throw new DavError(403, '越出白名单根目录（可能是软链指向，已拒绝）');
  }
  return { root, abs, rest };
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------
function xmlEsc(s) {
  return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;').replace(/'/g, '&apos;');
}
function mimeOf(name) {
  // 惰性 require：filemanager.cjs require 了这个模块，顶层再 require 回去
  // 就是循环依赖；等真正要发响应时 filemanager.cjs 早已加载完。
  const { MIME_BY_EXT } = require('./filemanager.cjs');
  const ext = path.extname(name).slice(1).toLowerCase();
  return MIME_BY_EXT[ext] || 'application/octet-stream';
}
function httpDate(d) { return d.toUTCString(); }
function isoDate(d) { return d.toISOString().replace(/\.\d{3}Z$/, 'Z'); }
function etagOf(st) { return `"${st.mtime.getTime().toString(16)}-${st.size.toString(16)}"`; }

function sendXml(res, status, body, extra) {
  const buf = Buffer.from(body, 'utf8');
  res.writeHead(status, Object.assign({
    'Content-Type': 'application/xml; charset="utf-8"',
    'Content-Length': buf.length,
    'DAV': '1, 2',
    'MS-Author-Via': 'DAV',
  }, extra || {}));
  res.end(buf);
}
function sendText(res, status, msg, extra) {
  const buf = Buffer.from(String(msg) + '\n', 'utf8');
  res.writeHead(status, Object.assign({
    'Content-Type': 'text/plain; charset=utf-8',
    'Content-Length': buf.length,
  }, extra || {}));
  res.end(buf);
}

// ---------------------------------------------------------------------------
// PROPFIND
// ---------------------------------------------------------------------------
// 注意：fs.Stats 上没有 name 字段（那是 Dirent 才有的），所以文件路径
// 必须单独传进来。早先在 collect/mimeOf 里写 st.name，stat 全是 undefined，
// 于是 scandir('/dav/') 和 extname(undefined) 各炸一半。
function responseXml(href, fsPath, st, isDir) {
  const base = path.basename(fsPath);
  const display = isDir ? (base || '/') : base;
  const rt = isDir ? '<D:collection/>' : '';
  return '  <D:response>\n' +
    `    <D:href>${xmlEsc(href)}</D:href>\n` +
    `    <D:propstat>\n      <D:prop>\n` +
    `        <D:displayname>${xmlEsc(display)}</D:displayname>\n` +
    `        <D:resourcetype>${rt}</D:resourcetype>\n` +
    `        <D:getlastmodified>${xmlEsc(httpDate(st.mtime))}</D:getlastmodified>\n` +
    `        <D:creationdate>${xmlEsc(isoDate(st.birthtime || st.mtime))}</D:creationdate>\n` +
    `        <D:getetag>${xmlEsc(etagOf(st))}</D:getetag>\n` +
    (isDir ? '' : `        <D:getcontentlength>${st.size}</D:getcontentlength>\n` +
             `        <D:getcontenttype>${xmlEsc(mimeOf(base))}</D:getcontenttype>\n`) +
    `      </D:prop>\n      <D:status>HTTP/1.1 200 OK</D:status>\n    </D:propstat>\n` +
    '  </D:response>\n';
}

function hrefFor(baseHref, name) {
  const leaf = encodeURIComponent(name) + (baseHref.endsWith('/') ? '/' : '/');
  return baseHref + leaf;
}

async function collect(fsPath, href, depth, budget, rootReal) {
  // 返回 [xml片段...]，budget 超了就抛，由上层转成 403
  if (budget.n++ > MAX_ENTRIES) throw new DavError(403, '条目过多，拒绝 Depth: infinity');
  const st = await fsp.stat(fsPath);
  const isDir = st.isDirectory();
  const out = [responseXml(href, fsPath, st, isDir)];
  if (!isDir || depth === '0') return out;
  const names = (await fsp.readdir(fsPath)).sort();
  for (const n of names) {
    const child = path.join(fsPath, n);
    // **每一层**都要重新判包含。顶层解析时查过包含不代表递归安全：
    // 目录里一个指向根外的软链，会让 infinity 顺着它把白名单外面的
    // 文件名和层级全列出来——resolveDav 只挡住了「直接请求」那条路。
    if (!containedReal(rootReal, child)) continue;
    let cst;
    try { cst = await fsp.stat(child); } catch (_) { continue; }
    out.push(...await collect(child, hrefFor(href, n), depth === 'infinity' ? 'infinity' : '0', budget, rootReal));
  }
  return out;
}

async function handlePropfind(req, res, davPath) {
  req.resume(); // 必须把请求体吃掉，否则 keep-alive 连接会被重置
  const { root, abs } = resolveDav(davPath);
  let st;
  try {
    st = await fsp.stat(abs);
  } catch (_) {
    throw new DavError(404, '资源不存在');
  }
  let depth = String(req.headers.depth || 'infinity').toLowerCase();
  if (depth !== '0' && depth !== '1' && depth !== 'infinity') depth = 'infinity';
  if (depth === 'infinity') depth = 'infinity';

  const href = req.url.split('?')[0];
  const budget = { n: 0 };
  let parts;
  try {
    parts = await collect(abs, href, depth, budget, root.real);
  } catch (e) {
    if (e instanceof DavError) {
      throw new DavError(e.status, e.message, { DAV: '1, 2, propfind-finite-depth' });
    }
    throw e;
  }
  sendXml(res, 207, '<?xml version="1.0" encoding="utf-8"?>\n<D:multistatus xmlns:D="DAV:">\n' + parts.join('') + '</D:multistatus>\n');
}

// ---------------------------------------------------------------------------
// GET / HEAD
// ---------------------------------------------------------------------------
function dirIndexHtml(abs, href) {
  const items = fs.readdirSync(abs, { withFileTypes: true })
    .map((d) => `<li><a href="${xmlEsc(href + encodeURIComponent(d.name) + (d.isDirectory() ? '/' : ''))}">${xmlEsc(d.name)}${d.isDirectory() ? '/' : ''}</a></li>`)
    .join('');
  return `<!doctype html><meta charset="utf-8"><title>${xmlEsc(href)}</title>` +
    `<h1>${xmlEsc(href)}</h1><ul>${items}</ul>`;
}

async function handleGet(req, res, davPath, isHead) {
  const { abs } = resolveDav(davPath);
  let st;
  try { st = await fsp.stat(abs); } catch (_) { throw new DavError(404, '资源不存在'); }

  if (st.isDirectory()) {
    const html = Buffer.from(dirIndexHtml(abs, req.url.split('?')[0]), 'utf8');
    res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8', 'Content-Length': html.length });
    return isHead ? res.end() : res.end(html);
  }

  const headers = {
    'Content-Type': mimeOf(path.basename(abs)),
    'Content-Length': st.size,
    'Last-Modified': httpDate(st.mtime),
    'ETag': etagOf(st),
    'Accept-Ranges': 'bytes',
  };

  // Range：只支持单段，形式 bytes=a-b / a- / -n，别的按整文件回
  const range = req.headers.range;
  if (range) {
    const m = /^bytes=(\d*)-(\d*)$/.exec(String(range).trim());
    if (m && (m[1] !== '' || m[2] !== '')) {
      let start = m[1] === '' ? st.size - Number(m[2]) : Number(m[1]);
      let end = m[1] === '' || m[2] === '' ? st.size - 1 : Number(m[2]);
      if (!(start >= 0) || !(end >= start) || start >= st.size) {
        res.writeHead(416, { 'Content-Range': `bytes */${st.size}` });
        return res.end();
      }
      if (end >= st.size) end = st.size - 1;
      res.writeHead(206, Object.assign({}, headers, {
        'Content-Length': end - start + 1,
        'Content-Range': `bytes ${start}-${end}/${st.size}`,
      }));
      if (isHead) return res.end();
      return fs.createReadStream(abs, { start, end }).pipe(res);
    }
  }

  res.writeHead(200, headers);
  if (isHead) return res.end();
  return fs.createReadStream(abs).pipe(res);
}

// ---------------------------------------------------------------------------
// PUT / DELETE / MKCOL
// ---------------------------------------------------------------------------
async function handlePut(req, res, davPath) {
  const { abs, rest } = resolveDav(davPath);
  if (!rest.length) throw new DavError(405, '不能 PUT 到集合根');
  const parent = path.dirname(abs);
  let pst;
  try { pst = await fsp.stat(parent); } catch (_) { throw new DavError(409, '父目录不存在'); }
  if (!pst.isDirectory()) throw new DavError(409, '父路径不是目录');

  const existed = fs.existsSync(abs);
  await atomicWriteStream(abs, req); // 原子落盘：先写临时文件再 rename
  sendText(res, existed ? 204 : 201, existed ? '已覆盖' : '已创建');
}

async function handleDelete(req, res, davPath) {
  const { abs, root, rest } = resolveDav(davPath);
  if (!rest.length || abs === root.real) throw new DavError(403, '不能删除白名单根');
  let st;
  try { st = await fsp.stat(abs); } catch (_) { throw new DavError(404, '资源不存在'); }

  if (st.isDirectory()) {
    const left = await fsp.readdir(abs);
    if (left.length) throw new DavError(409, '集合非空，拒绝递归删除（先自行清空）');
    await fsp.rmdir(abs);
  } else {
    // 与 FM 界面保持一致：进回收站而不是直接抹掉
    try { trash.trashPath(abs); } catch (_) { await fsp.unlink(abs); }
  }
  sendText(res, 204, '已删除');
}

async function handleMkcol(req, res, davPath) {
  const { abs, rest } = resolveDav(davPath);
  if (!rest.length) throw new DavError(405, '集合根已存在');
  // RFC 4918: 带请求体的 MKCOL 服务器可以返回 415
  const ct = String(req.headers['content-length'] || '0');
  if (Number(ct) > 0) { req.resume(); throw new DavError(415, 'MKCOL 不支持请求体'); }
  req.resume();
  if (fs.existsSync(abs)) throw new DavError(405, '已存在');
  const parent = path.dirname(abs);
  let pst;
  try { pst = await fsp.stat(parent); } catch (_) { throw new DavError(409, '父集合不存在'); }
  if (!pst.isDirectory()) throw new DavError(409, '父路径不是目录');
  await fsp.mkdir(abs);
  sendText(res, 201, '已创建集合');
}

// ---------------------------------------------------------------------------
// MOVE / COPY
// ---------------------------------------------------------------------------
function destFromHeader(req) {
  const h = req.headers.destination;
  if (!h) throw new DavError(400, '缺少 Destination 头');
  let raw = String(h);
  // Destination 是 URI，按规范客户端应当 percent-encode。可真客户端不一定照做：
  // Node 把请求头按 latin1 解出来，客户端若直接塞 UTF-8 原始字节（curl 就是），
  // 这里拿到的就是乱码 —— decodeURIComponent 之后仍然是一串「合法但完全错误」的
  // 字符名，于是 MOVE 回 201、文件却落在旁边的乱码名字上，客户端一脸问号。
  // 所以先按 latin1→utf8 还原字节，再当 URI 解析。
  if (/[\u0080-\u00ff]/.test(raw)) raw = Buffer.from(raw, 'latin1').toString('utf8');
  let u;
  try { u = new URL(raw); } catch (_) { throw new DavError(400, 'Destination 不是合法 URI'); }
  let p;
  // 畸形百分号编码（比如 %zz、末尾单个 %）会让 decodeURIComponent 直接抛，
  // 不兜住就变成 500 —— 客户端拿到的是「服务端崩了」而不是「你 URI 写错了」。
  try { p = decodeURIComponent(u.pathname); } catch (_) { throw new DavError(400, 'Destination 路径编码非法'); }
  if (p === '/dav' || p === '/dav/') return '/';
  if (!p.startsWith('/dav/')) throw new DavError(403, 'Destination 必须在 /dav/ 之下');
  return '/' + p.slice('/dav/'.length);
}

async function copyRecursive(src, dst) {
  const st = await fsp.stat(src);
  if (st.isDirectory()) {
    await fsp.mkdir(dst, { recursive: false });
    for (const n of await fsp.readdir(src)) await copyRecursive(path.join(src, n), path.join(dst, n));
  } else {
    await fsp.mkdir(path.dirname(dst), { recursive: true });
    await fsp.copyFile(src, dst);
  }
}

async function handleMoveCopy(req, res, davPath, isMove) {
  const src = resolveDav(davPath);
  const dst = resolveDav(destFromHeader(req));
  if (dst.abs === src.abs) throw new DavError(403, '源与目标相同');

  let sst;
  try { sst = await fsp.stat(src.abs); } catch (_) { throw new DavError(404, '源不存在'); }

  // 不能把集合搬进它自己的子目录 —— 那会造出一个自指的无限目录树
  if (sst.isDirectory() && (dst.abs + path.sep).startsWith(src.abs + path.sep)) {
    throw new DavError(403, '不能把集合移动/复制到其自身内部');
  }
  const dparent = path.dirname(dst.abs);
  let dpst;
  try { dpst = await fsp.stat(dparent); } catch (_) { throw new DavError(409, '目标父集合不存在'); }
  if (!dpst.isDirectory()) throw new DavError(409, '目标父路径不是目录');
  if (!dst.rest.length) throw new DavError(403, '不能覆盖白名单根');

  const exists = fs.existsSync(dst.abs);
  // RFC 4918: Overwrite 缺省是 F，目标已存在应回 412
  const overwrite = String(req.headers.overwrite || 'F').toUpperCase() !== 'F';
  if (exists && !overwrite) throw new DavError(412, '目标已存在，且 Overwrite: F');

  if (exists) await fsp.rm(dst.abs, { recursive: true, force: true });
  try {
    if (isMove) await fsp.rename(src.abs, dst.abs);
    else await copyRecursive(src.abs, dst.abs);
  } catch (e) {
    if (e.code !== 'EXDEV') throw e;
    if (isMove) { await copyRecursive(src.abs, dst.abs); await fsp.rm(src.abs, { recursive: true, force: true }); }
  }
  res.writeHead(exists ? 204 : 201, { 'Content-Length': 0 });
  res.end();
}

// ---------------------------------------------------------------------------
// 入口
// ---------------------------------------------------------------------------
async function handleWebdav(req, res, pathname) {
  const davPath = pathname.slice('/dav'.length) || '/';
  try {
    switch (req.method) {
      case 'OPTIONS':
        res.writeHead(200, {
          DAV: '1, 2',
          Allow: 'OPTIONS, GET, HEAD, PUT, DELETE, MKCOL, PROPFIND, MOVE, COPY',
          'MS-Author-Via': 'DAV',
          'Content-Length': 0,
        });
        return res.end();
      case 'PROPFIND': return await handlePropfind(req, res, davPath);
      case 'GET': return await handleGet(req, res, davPath, false);
      case 'HEAD': return await handleGet(req, res, davPath, true);
      case 'PUT': return await handlePut(req, res, davPath);
      case 'DELETE': return await handleDelete(req, res, davPath);
      case 'MKCOL': return await handleMkcol(req, res, davPath);
      case 'MOVE': return await handleMoveCopy(req, res, davPath, true);
      case 'COPY': return await handleMoveCopy(req, res, davPath, false);
      default:
        res.writeHead(405, { Allow: 'OPTIONS, GET, HEAD, PUT, DELETE, MKCOL, PROPFIND, MOVE, COPY', 'Content-Length': 0 });
        return res.end();
    }
  } catch (e) {
    if (e instanceof DavError) {
      return sendText(res, e.status, e.message, e.extraHeaders);
    }
    console.error('[dav]', e && e.stack || e);
    return sendText(res, 500, 'WebDAV 内部错误');
  }
}

module.exports = { handleWebdav, DavError, resolveDav, loadRoots, containedReal, _roots: roots };
