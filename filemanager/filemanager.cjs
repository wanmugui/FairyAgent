// filemanager/filemanager.cjs — standalone file-manager backend module.
//
// Everything the file browser needs lives here: static pages (index-fm.html,
// viewer.html), vendored preview libraries (/vendor/*), directory listing,
// download/save/delete/upload routes, and result-path scanning for the chat
// viewer.
// server.cjs routes requests through tryHandleFileManager() FIRST; any route
// this module claims never reaches the Fairy chat API handlers.
//
// File layout (this folder is self-contained and can be lifted out whole):
//   index-fm.html      file browser UI
//   viewer.html        music/video player + format previews
//   vendor/            pdf.js, xlsx, docx-preview (local copies, no CDN)
//   filemanager.cjs    this module

const fs = require("fs");
const path = require("path");

// P0-2..P0-8 的共享规则（原子落盘 / 软链环 / 文件名校验 / 字素簇截断 / 冲突检测）
const {
  validateName,
  truncateName,
  findNameConflict,
  detectSymlinkCycle,
  describeFsError,
  atomicWriteStream,
} = require("./fm-path.cjs");

const meta = require("./fm-meta.cjs");
const trash = require("./fm-trash.cjs");
const locks = require("./fm-lock.cjs");
const upload = require("./fm-upload.cjs");

const REPO = path.resolve(__dirname, "..");

// findViewerPaths scans assistant text for viewable result paths. Paths must
// stay inside the agent's allowed roots. The text-chat frontend consumes these
// paths over SSE and opens them in its embedded viewer.
const VIEWER_EXTS = new Set([
  "pdf", "docx", "xlsx", "xls",
  "md", "txt", "csv",
  "py", "js", "ts", "tsx", "jsx",
  "go", "rs", "java", "rb", "sh", "bash", "ps1", "bat",
  "json", "yaml", "yml", "html", "htm", "css",
  "xml",
]);
const PATH_RE_WIN = /[A-Za-z]:[\\\/][^\s"'<>|*?\n]+/g;
const PATH_RE_POSIX = /(?:\/[^\s"'<>|*?\n]+){2,}/g;
const viewerSeenThisSession = new Map(); // sessionName -> Set<path>
function findViewerPaths(text) {
  if (!text || !REPO) return [];
  const matches = new Set();
  for (const m of text.matchAll(PATH_RE_WIN)) matches.add(m[0].replace(/[\\\/]+/g, path.sep));
  for (const m of text.matchAll(PATH_RE_POSIX)) matches.add(m[0]);
  const paths = [];
  for (const candidate of matches) {
    let target = candidate;
    const logical = String(candidate || "").replace(/\\/g, "/");
    if (logical === "/mnt/data" || logical.startsWith("/mnt/data/")) {
      target = path.join(REPO, "workspace", logical.slice("/mnt/data".length));
    } else if (logical === "/skills" || logical.startsWith("/skills/")) {
      target = path.join(REPO, logical.slice(1));
    }
    target = String(target || "").replace(/^\/+([A-Za-z]:\/)/, "$1");
    let abs;
    try { abs = path.resolve(target); } catch { continue; }
    const ext = path.extname(abs).toLowerCase().replace(/^\./, "");
    if (!VIEWER_EXTS.has(ext)) continue;
    const inside = FILE_CONTENT_ALLOWED_ROOTS.some(root => {
      const r = root.toLowerCase();
      const a = abs.toLowerCase();
      return a === r || a.startsWith(r + path.sep);
    });
    if (!inside) continue;
    if (paths.includes(abs)) continue;
    paths.push(abs);
  }
  return paths;
}
function viewerAutoOpen(sessionName, text) {
  if (!text || !REPO) return;
  const seen = viewerSeenThisSession.get(sessionName) || new Set();
  viewerSeenThisSession.set(sessionName, seen);
  for (const abs of findViewerPaths(text)) {
    if (seen.has(abs)) continue;
    seen.add(abs);
    spawnViewerFor(abs);
  }
}
function openLocalBrowserUrl(url) {
  try {
    if (process.platform === "win32") {
      // Use cmd start to launch the OS default browser. /c waits for the
      // command to terminate; "" is an empty title placeholder required by
      // start's parsing rules when the URL starts with a quote.
      const { spawn } = require("child_process");
      spawn("cmd", ["/c", "start", "", "http://localhost:8081" + url], {
        detached: true,
        stdio: "ignore",
        windowsHide: true,
      }).unref();
    } else if (process.platform === "darwin") {
      const { spawn } = require("child_process");
      spawn("open", ["http://localhost:8081" + url], { detached: true, stdio: "ignore" }).unref();
    } else {
      const { spawn } = require("child_process");
      spawn("xdg-open", ["http://localhost:8081" + url], { detached: true, stdio: "ignore" }).unref();
    }
  } catch (e) {
    // Best-effort; do not surface to the user. Failing to open the viewer
    // must not break the chat pipeline.
  }
}
function spawnViewerFor(absPath) {
  openLocalBrowserUrl("/viewer.html?file=" + encodeURIComponent(absPath));
}
function spawnFileManagerFor(absPath) {
  openLocalBrowserUrl("/index-fm.html?file=" + encodeURIComponent(absPath));
}
function spawnPptPreviewFor(deckDir) {
  openLocalBrowserUrl("/api/ppt-preview?deck_dir=" + encodeURIComponent(deckDir || ""));
}

// viewer.html fetches file bytes via GET /api/file-content?path=...
// The path must resolve inside one of the agent's safe roots (REPO, output
// dirs). Any other host path is rejected so the viewer can't be coerced
// into reading unrelated files.
const FILE_CONTENT_ALLOWED_ROOTS = [
  REPO,
  path.resolve(REPO, "workspace"),
  path.resolve(REPO, "skills"),
  path.resolve(REPO, "frontend"),
];
function writeJsonError(res, status, error, details) {
  res.writeHead(status, { "Content-Type": "application/json; charset=utf-8" });
  res.end(JSON.stringify({ ok: false, error, ...(details || {}) }));
}

function serveFileContent(req, res) {
  // http.createServer does not auto-parse query strings; do it ourselves.
  const urlObj = new URL(req.url, "http://localhost");
  const raw = String(urlObj.searchParams.get("path") || "").trim();
  if (!raw) { writeJsonError(res, 400, "path is required"); return; }
  // The viewer.html route is the one place where we constrain reads to
  // FILE_CONTENT_ALLOWED_ROOTS. The file-manager routes have their own
  // resolveFMRoot, which is permissive (any absolute host path the user
  // can browse to in their own browser).
  const abs = resolveViewerRoot(raw);
  if (abs === null) {
    writeJsonError(res, 403, "path is outside allowed roots");
    return;
  }
  if (!fs.existsSync(abs)) {
    writeJsonError(res, 404, "file not found: " + abs);
    return;
  }
  const stat = fs.statSync(abs);
  if (stat.isDirectory()) {
    writeJsonError(res, 400, "path is a directory, not a file");
    return;
  }
  // Cap at 50 MB to keep the browser happy.
  if (stat.size > 50 * 1024 * 1024) {
    writeJsonError(res, 413, "file too large (>50MB) for viewer");
    return;
  }
  const ext = path.extname(abs).toLowerCase().replace(/^\./, "");
  const mime = MIME_BY_EXT[ext] || "application/octet-stream";
  res.writeHead(200, {
    "Content-Type": mime,
    "Content-Length": stat.size,
    "Cache-Control": "no-store",
  });
  if (req.method === "HEAD") { res.end(); return; }
  fs.createReadStream(abs).pipe(res);
}

// resolveFMRoot turns the URL path into an absolute host path. Unlike
// resolveViewerRoot (which gates the viewer.html service to
// FILE_CONTENT_ALLOWED_ROOTS), this is the file-manager root and is
// intentionally permissive: the user has explicitly opened the FM in
// their browser and we trust them to navigate anywhere they want on
// their own machine. We do still need to reject obvious garbage like
// NUL bytes so a bad path doesn't crash the server.
function resolveFMRoot(relPath) {
  const cleaned = String(relPath || "").trim();
  if (cleaned === "" || cleaned === "." || cleaned === "/") {
    // Empty path means "show me the drives". resolveFMRoot would otherwise
    // fall back to C:\ on Windows, hiding D: / E: until the user types
    // them. The caller (handleListFiles) checks the rawPath for the
    // __drives__ sentinel first and returns the virtual drive listing;
    // here we just need a non-null marker so handleListFiles can proceed
    // to a fallback branch.
    return process.platform === "win32" ? "__drives__" : "/";
  }
  // Absolute paths: respect them as-is (Windows: C:\..., D:\...; POSIX: /...).
  // A bare drive letter ("E:") means the drive root, not the process's current
  // directory on that drive (path.resolve("E:") would silently map to the CWD).
  if (/^[a-zA-Z]:$/.test(cleaned)) {
    try { return path.resolve(cleaned + path.sep); }
    catch (_) { return null; }
  }
  if (cleaned.match(/^[a-zA-Z]:[\\\/]/) || cleaned.startsWith("/") || cleaned.startsWith("\\\\")) {
    try { return path.resolve(cleaned); }
    catch (_) { return null; }
  }
  // Relative paths: treat as repo-relative.
  try { return path.resolve(REPO, cleaned); }
  catch (_) { return null; }
}

// P0-3：请求路径本身可能就落在软链环上。lstat 不跟随软链，自指环不会自己抛
// ELOOP，必须在解析阶段手工逐跳检查，否则后面 statSync 会抛裸 ELOOP → 500。
// 规格要求这种情况返回 200 + 文案含"循环"，而不是归进"无权限"或"不存在"。

// listWindowsDrives enumerates the file-system drive letters visible to
// this process, excluding the system drive (C:). The user explicitly asked
// to see only non-C drives here so the system volume doesn't clutter the
// sidebar.
function listWindowsDrives() {
  try {
    const { execSync } = require("child_process");
    const out = execSync(
      'powershell -NoProfile -Command "Get-PSDrive -PSProvider FileSystem | Select-Object -ExpandProperty Root"',
      { encoding: "utf-8", timeout: 5000 }
    );
    const roots = out.split(/\r?\n/).map(s => s.trim()).filter(s => /^[A-Z]:[\\/]?$/.test(s));
    return roots
      .filter(root => !/^C:/i.test(root))   // hide the system drive
      .map(root => {
        const norm = root.replace(/[\\/]+$/, "").replace(/\\/g, "/");
        return {
          name: norm + "/",
          isDirectory: true,
          path: norm + "/",
          relPath: norm + "/",
          size: 0,
          modified: 0,
        };
      });
  } catch (e) {
    return [];
  }
}
function resolveViewerRoot(absPath) {
  const logical = String(absPath || "").replace(/\\/g, "/").replace(/^\/+([A-Za-z]:\/)/, "$1");
  let target = logical;
  if (logical === "/mnt/data" || logical.startsWith("/mnt/data/")) {
    target = path.join(REPO, "workspace", logical.slice("/mnt/data".length));
  } else if (logical === "/skills" || logical.startsWith("/skills/")) {
    target = path.join(REPO, logical.slice(1));
  }
  let abs;
  try { abs = path.resolve(target); }
  catch (_) { return null; }
  return FILE_CONTENT_ALLOWED_ROOTS.some(root => {
    const r = root.toLowerCase();
    const a = abs.toLowerCase();
    return a === r || a.startsWith(r + path.sep);
  }) ? abs : null;
}

function handleListFiles(req, res) {
  const urlObj = new URL(req.url, "http://localhost");
  const rawPath = urlObj.searchParams.get("path") || "";
  const cursorParam = urlObj.searchParams.get("cursor");
  const limitParam = parseInt(urlObj.searchParams.get("limit") || "", 10);
  const hasLimit = Number.isFinite(limitParam) && limitParam > 0;
  const paged = hasLimit || cursorParam !== null;

  // Magic value: list every drive (Windows) or treat root as "/" (POSIX).
  // Returning a virtual listing means the sidebar always shows drives at the
  // top level and the user can jump between them without typing paths.
  if (rawPath === "__drives__" || rawPath === "" || rawPath === "/" || rawPath === ".") {
    if (process.platform === "win32") {
      const drives = listWindowsDrives();
      res.writeHead(200, { "Content-Type": "application/json; charset=utf-8" });
      res.end(JSON.stringify(drives));
      return;
    }
    // POSIX: fall through and list "/" below.
  }

  const abs = resolveFMRoot(rawPath);
  if (abs === null) { writeJsonError(res, 403, "path is outside allowed roots"); return; }

  // P0-3: a listing request can itself land on a symlink cycle. Say so
  // plainly (200 + message) instead of letting a bare ELOOP become a 500.
  const cycle = detectSymlinkCycle(abs);
  if (cycle) { writeJsonError(res, 200, cycle); return; }

  // P0-3: lstat, not stat -- statSync/existsSync throw ELOOP on a cycle.
  let dirStat;
  try { dirStat = fs.lstatSync(abs); }
  catch (e) { writeJsonError(res, 404, describeFsError(e)); return; }
  if (!dirStat.isDirectory()) { writeJsonError(res, 400, "path is not a directory"); return; }

  // P0-8: opendir + readSync so a million-entry directory streams instead of
  // materialising every Dirent at once.
  const items = [];
  let vanished = 0;
  let linkLoops = 0;
  const handle = fs.opendirSync(abs);
  try {
    for (;;) {
      const entry = handle.readSync();
      if (entry === null) break;
      const child = path.join(abs, entry.name);

      // P0-3: lstat the entry itself; do not follow the link.
      // d_type may be DT_UNKNOWN on some FUSE/NFS mounts, so the Dirent type
      // is only trusted when present and we fall back to lstat otherwise.
      let st = null;
      try { st = fs.lstatSync(child); }
      catch (e) {
        // ENOENT means it disappeared mid-walk: skip and count, never abort.
        // ELOOP is surfaced as a warning on the parent rather than swallowed.
        if (e.code === "ELOOP") linkLoops++; else vanished++;
        continue;
      }

      // P0-7: present NFC so macOS "é" and Linux "e" + U+0301 look identical.
      const name = entry.name.normalize("NFC");
      const isDirectory = entry.isDirectory() ||
        (entry.isSymbolicLink() ? false : st.isDirectory());

      const rel = path.relative(REPO, child);
      // index-fm.html uses f.path for navigation. Keep files strictly inside
      // the repo repo-relative (e.g. "workspace/output/foo.md") so the agent
      // path stays stable; the repo root itself or anything outside the repo
      // (other drives / folders) uses an absolute forward-slash path so the
      // tree can still navigate there (drive root -> folder -> ...).
      const relPath = (rel !== "" && !rel.startsWith("..") && !path.isAbsolute(rel))
        ? rel.replace(/\\/g, "/")
        : child.replace(/\\/g, "/");
      items.push({
        name,
        isDirectory,
        path: relPath,
        relPath,
        // P2-6：星标状态随列表下发，前端不必再发一次请求去问
        starred: meta.isStarred(child),
        size: st.size,
        modified: st.mtimeMs,
      });
    }
  } finally {
    try { handle.closeSync(); } catch (_) { /* already closed */ }
  }

  // Dirs first, then alphabetical. This ordering is what the keyset cursor
  // advances on -- a stable sort key is what makes paging without LIMIT/OFFSET
  // possible at all (a directory has no inherent order, so an offset would
  // duplicate or skip entries as soon as anything is written concurrently).
  // P2-1 排序：sort=name|size|mtime、order=asc|desc；目录恒排前面（UI 既有约定）。
  // P2-6 星标置顶：星标是用户显式表达"这个我天天用"，沉到按体积排序的末尾等于没标。
  const sortMode = urlObj.searchParams.get("sort") || "name";
  const orderDir = urlObj.searchParams.get("order") === "desc" ? -1 : 1;
  const byField = {
    name: (a, b) => a.name.localeCompare(b.name),
    size: (a, b) => a.size - b.size,
    mtime: (a, b) => a.modified - b.modified,
  }[sortMode] || ((a, b) => a.name.localeCompare(b.name));
  items.sort((a, b) => {
    if (a.starred !== b.starred) return a.starred ? -1 : 1;
    if (a.isDirectory !== b.isDirectory) return a.isDirectory ? -1 : 1;
    return byField(a, b) * orderDir;
  });
  const sortKey = (it) => (it.starred ? "0" : "1") + (it.isDirectory ? "0" : "1") + " " + it.name;

  if (!paged) {
    // Backwards compatible: bare array, exactly as the UI has always seen it.
    res.writeHead(200, { "Content-Type": "application/json; charset=utf-8" });
    res.end(JSON.stringify(items));
    return;
  }

  let start = 0;
  if (cursorParam) {
    let cursorKey;
    try { cursorKey = Buffer.from(cursorParam, "base64").toString("utf8"); }
    catch (_) { cursorKey = cursorParam; }
    // Keyset: first entry strictly greater than the last one we handed out.
    start = items.findIndex((it) => sortKey(it) > cursorKey);
    if (start < 0) start = items.length;
  }

  const limit = hasLimit ? limitParam : items.length - start;
  const page = items.slice(start, start + limit);
  const last = page[page.length - 1];
  const more = start + page.length < items.length;
  const body = {
    items: page,
    nextCursor: more && last ? Buffer.from(sortKey(last), "utf8").toString("base64") : null,
    total: items.length,
  };
  if (vanished) body.vanished = vanished;
  if (linkLoops) body.symlinkLoops = linkLoops;
  res.writeHead(200, { "Content-Type": "application/json; charset=utf-8" });
  res.end(JSON.stringify(body));
}

// streamFileWithRange serves a file with single-range HTTP support so
// <audio>/<video> elements can seek (Range: bytes=...). Falls back to a
// full 200 response when no (or an invalid) Range header is present.
// P1-4：Range 续传补全（多段 / If-Range / ETag / 小文件不发 Range）
// 单段 bytes=0-99 原本已支持；缺的是多段 multipart/byteranges、If-Range 语义，
// 以及 <1KB 文件按规格发 Accept-Ranges: none（断点续传对拇指大的文件没意义）。

/** 小于 1KB 不宣告支持 Range：分片请求的开销比省下的字节还大。 */
const RANGE_MIN_BYTES = 1024;

/**
 * 强 ETag。Range + ETag 是"断点续传拿到的是不是同一份内容"的唯一凭据，
 * 所以必须是强校验子（mtime-size），不能用弱校验子（mtime 单独）。
 */
function strongETag(stat) {
  return '"' + stat.mtimeMs.toString(16) + "-" + stat.size.toString(16) + '"';
}

/**
 * 解析 Range 头。
 * @returns {null|{type:"unsatisfiable"}|Array<{start:number,end:number}>} null 表示没有 Range
 */
function parseRangeHeader(range, size) {
  const raw = String(range || "").trim();
  const m = /^bytes=(.*)$/i.exec(raw);
  if (!m) return null;
  const specs = m[1].split(",");
  if (specs.length > 16) return null; // 防御：不让客户端用上百段拖垮自己
  const out = [];
  for (const spec of specs) {
    const sm = /^(\d*)-(\d*)$/.exec(spec.trim());
    if (!sm) return null; // 语法不合法 -> 交给上层当普通请求
    if (sm[1] === "" && sm[2] === "") return null;
    let start;
    let end;
    if (sm[1] === "") {
      // suffix form: bytes=-N -> 最后 N 字节
      const n = parseInt(sm[2], 10);
      if (Number.isNaN(n) || n <= 0) return { type: "unsatisfiable" };
      start = Math.max(size - n, 0);
      end = size - 1;
    } else {
      start = parseInt(sm[1], 10);
      if (Number.isNaN(start)) return null;
      if (sm[2] === "") end = size - 1;
      else {
        end = parseInt(sm[2], 10);
        if (Number.isNaN(end)) return null;
        if (end >= size) end = size - 1; // 超界截断，不报错（RFC 7233 允许）
      }
    }
    if (start > end || start >= size) return { type: "unsatisfiable" };
    out.push({ start, end });
  }
  return out.length ? out : null;
}

/**
 * 把若干段写成一个 multipart/byteranges 响应。
 * 段之间用 CRLF + boundary 分隔，每段自带 Content-Type 与 Content-Range。
 */
function writeMultipartRanges(res, abs, size, ranges, mime, boundary) {
  // RFC 7233: 每个 boundary 分隔符之前必须有一个 CRLF。第一段前面没有，
  // 之后每段前面都有——那个 CRLF 同时是上一段数据的结束符。少写它，
  // 严格解析的客户端会把上一段最后 2 个字节当成 boundary 的一部分。
  const parts = ranges.map((r) => {
    const header =
      `--${boundary}\r\nContent-Type: ${mime}\r\n` +
      `Content-Range: bytes ${r.start}-${r.end}/${size}\r\n\r\n`;
    return { header, len: Buffer.byteLength(header), start: r.start, end: r.end };
  });
  const trailer = `\r\n--${boundary}--\r\n`;
  const bodyLen =
    parts.reduce((n, p) => n + p.len + (p.end - p.start + 1), 0) +
    Buffer.byteLength(trailer) +
    (parts.length - 1) * 2; // 段间那些 CRLF

  res.writeHead(206, {
    "Content-Type": `multipart/byteranges; boundary=${boundary}`,
    "Content-Length": bodyLen,
    "Accept-Ranges": "bytes",
    "Cache-Control": "no-store",
  });
  // 串行写各段：并发 pipe 到同一个 res 会交错乱序
  let i = 0;
  const writeNext = () => {
    if (i >= parts.length) { res.end(trailer); return; }
    const p = parts[i++];
    res.write((i > 1 ? "\r\n" : "") + p.header);
    const s = fs.createReadStream(abs, { start: p.start, end: p.end });
    s.on("error", () => res.destroy());
    s.on("end", writeNext);
    s.pipe(res, { end: false });
  };
  writeNext();
}

function streamFileWithRange(req, res, abs, stat, mime) {
  const size = stat.size;
  const etag = strongETag(stat);
  const rangeable = size >= RANGE_MIN_BYTES;
  const common = {
    "Content-Type": mime,
    "ETag": etag,
    "Cache-Control": "no-store",
    // 小文件不宣告 Range：省不下几个字节，却让每个分片请求都多一次握手
    "Accept-Ranges": rangeable ? "bytes" : "none",
  };

  const range = req.headers.range;
  if (!range || !rangeable) {
    res.writeHead(200, { ...common, "Content-Length": size });
    fs.createReadStream(abs).pipe(res);
    return;
  }

  // If-Range：客户端拿它手里的 ETag 问"还是同一份吗"。不匹配就必须整份重发，
  // 否则续传会拼出一份内容错乱的"半新半旧"文件——这正是断点续传最恶心的故障。
  const ifRange = req.headers["if-range"];
  if (ifRange) {
    const match = String(ifRange).trim() === etag;
    if (!match) {
      res.writeHead(200, { ...common, "Content-Length": size });
      fs.createReadStream(abs).pipe(res);
      return;
    }
  }

  const parsed = parseRangeHeader(range, size);
  if (!parsed) {
    // 语法不认识的 Range 按整份返回（RFC：不得当错误处理）
    res.writeHead(200, { ...common, "Content-Length": size });
    fs.createReadStream(abs).pipe(res);
    return;
  }
  if (parsed.type === "unsatisfiable") {
    res.writeHead(416, { ...common, "Content-Range": "bytes */" + size });
    res.end();
    return;
  }

  if (parsed.length === 1) {
    const { start, end } = parsed[0];
    res.writeHead(206, {
      ...common,
      "Content-Range": `bytes ${start}-${end}/${size}`,
      "Content-Length": end - start + 1,
    });
    fs.createReadStream(abs, { start, end }).pipe(res);
    return;
  }

  writeMultipartRanges(res, abs, size, parsed, mime, "FairyRange" + Date.now().toString(16));
}

// P1-1 重命名/移动/复制 + P1-8 冲突自动改名。
//
// 三个操作共用同一套安全前置：路径解析 -> 软链环 -> 文件名校验 -> 冲突判定。
// 放在一处是因为它们的风险完全同构，分开写必然出现"rename 校验了、copy 忘了"。

const os = require("os");

/**
 * P1-8：生成不冲突的新名字，形如 `报告 (副本) (主机名) (2026-10-03 14-30-15).pdf`。
 * 主机名是必须的：多人共享目录时，「(副本)」会互相踩，标出机器才分得清是谁生成的。
 * @returns {string} 一定不与 existing 冲突（逐个递增序号直到空位）
 */
function makeConflictFreeName(dir, desired) {
  if (!fs.existsSync(path.join(dir, desired)) && !findNameConflict(dir, desired)) {
    return desired;
  }
  const dot = desired.lastIndexOf(".");
  const hasExt = dot > 0;
  const stem = hasExt ? desired.slice(0, dot) : desired;
  const ext = hasExt ? desired.slice(dot) : "";
  const host = os.hostname().split(".")[0].replace(/[<>:"/\\|?*\s]/g, "-").slice(0, 32);
  const now = new Date();
  const p2 = (n) => String(n).padStart(2, "0");
  const stamp = `${now.getFullYear()}-${p2(now.getMonth() + 1)}-${p2(now.getDate())} ${p2(now.getHours())}-${p2(now.getMinutes())}-${p2(now.getSeconds())}`;

  const base = `${stem} (副本) (${host}) (${stamp})${ext}`;
  const trimmed = truncateName(base);
  if (!fs.existsSync(path.join(dir, trimmed)) && !findNameConflict(dir, trimmed)) return trimmed;

  for (let i = 2; i < 1000; i++) {
    const cand = truncateName(`${stem} (副本) (${host}) (${stamp}) ${i}${ext}`);
    if (!fs.existsSync(path.join(dir, cand)) && !findNameConflict(dir, cand)) return cand;
  }
  return truncateName(`${stem} (副本) (${host}) (${stamp}) ${Date.now()}${ext}`);
}

/** 三个操作共用的前置校验。失败时直接写响应并返回 null。 */
function guardOpPath(res, p, label) {
  const abs = resolveFMRoot(p || "");
  if (abs === null) { writeJsonError(res, 403, `${label}：路径超出允许范围`); return null; }
  const cycle = detectSymlinkCycle(abs);
  if (cycle) { writeJsonError(res, 200, cycle); return null; }
  const nameErr = validateName(path.basename(abs));
  if (nameErr) { writeJsonError(res, 400, `${label}：${nameErr}`); return null; }
  return abs;
}

/** 源必须存在。存在性检查放在 lstat 上，避免软链环抛裸 ELOOP。 */
function requireSource(res, abs, label) {
  let st;
  try { st = fs.lstatSync(abs); }
  catch (e) {
    writeJsonError(res, 404, `${label}：${describeFsError(e)}`);
    return null;
  }
  return st;
}

function readJsonBody(req) {
  return new Promise((resolve) => {
    let raw = "";
    req.on("data", (c) => {
      raw += c;
      // P0-4 的教训：body 不该无限长。64KB 足够放一堆路径。
      if (raw.length > 64 * 1024) { req.destroy(); resolve({}); }
    });
    req.on("end", () => {
      try { resolve(JSON.parse(raw || "{}")); }
      catch { resolve({}); }
    });
    req.on("error", () => resolve({}));
  });
}

// P1-1 重命名 / 移动：同一目录内改名，或跨目录移动。
// 两者语义相同（rename(2)），只是一个 src 目录与 dst 目录是否相同而已。
// P3：把 errno 翻成合适的 HTTP 状态。
// 之前这里一律 500——"目标目录不存在"、"不能移动到自己里面"都是用户填错了，
// 回 500 既污染错误日志，前端也无法据此决定要不要提示重试。
function fsErrStatus(e) {
  const c = e && e.code;
  if (c === "ENOENT") return 404;
  if (c === "EACCES" || c === "EPERM") return 403;
  if (c === "EEXIST" || c === "ENOTEMPTY") return 409;
  if (c === "EINVAL" || c === "EXDEV" || c === "EBUSY" || c === "EISDIR" || c === "ENOTDIR") return 400;
  return 500;
}

async function handleRenameOrMove(req, res, isMove) {
  const label = isMove ? "移动" : "重命名";
  const body = await readJsonBody(req);
  const from = guardOpPath(res, body.from, label);
  if (from === null) return;
  const to = guardOpPath(res, body.to, label);
  if (to === null) return;
  if (from === to) { writeJsonError(res, 400, `${label}：源与目标相同`); return; }
  if (requireSource(res, from, label) === null) return;

  if (isMove && to.startsWith(from + path.sep)) {
    writeJsonError(res, 400, "移动：不能把目录移动到它自己或它的子目录里"); return;
  }
  if (fs.existsSync(to)) {
    writeJsonError(res, 409, `${label}：目标已存在`, { conflict: path.basename(to), options: ["rename", "overwrite", "keep-both"] });
    return;
  }
  const conflict = findNameConflict(path.dirname(to), path.basename(to));
  if (conflict && conflict.name !== path.basename(to)) {
    // P1-8：默认自动改名而不是报错，但把新名字回传给前端弹确认横幅
    const fixed = makeConflictFreeName(path.dirname(to), path.basename(to));
    try { fs.renameSync(from, path.join(path.dirname(to), fixed)); }
    catch (e) { writeJsonError(res, fsErrStatus(e), `${label}：${describeFsError(e)}`); return; }
    res.writeHead(200, { "Content-Type": "application/json; charset=utf-8" });
    res.end(JSON.stringify({ ok: true, autoRenamed: true, to: path.join(path.dirname(to), fixed), name: fixed }));
    return;
  }
  try { fs.renameSync(from, to); }
  catch (e) { writeJsonError(res, fsErrStatus(e), `${label}：${describeFsError(e)}`); return; }
  res.writeHead(200, { "Content-Type": "application/json; charset=utf-8" });
  res.end(JSON.stringify({ ok: true, to }));
}

// P1-1 复制：保留源文件。目录递归复制，跨设备时 fs.copyFile/rename 会报 EXDEV，
// 所以目录用逐项 copyFile 递归，而不是 rename。
async function handleCopy(req, res) {
  const body = await readJsonBody(req);
  const from = guardOpPath(res, body.from, "复制");
  if (from === null) return;
  const to = guardOpPath(res, body.to, "复制");
  if (to === null) return;
  if (from === to) { writeJsonError(res, 400, "复制：源与目标相同"); return; }
  if (requireSource(res, from, "复制") === null) return;

  let dest = to;
  if (fs.existsSync(to) || findNameConflict(path.dirname(to), path.basename(to))) {
    dest = path.join(path.dirname(to), makeConflictFreeName(path.dirname(to), path.basename(to)));
  }
  const destDir = path.dirname(dest);
  if (!fs.existsSync(destDir)) { writeJsonError(res, 400, "复制：目标目录不存在"); return; }

  try {
    copyRecursive(from, dest);
  } catch (e) {
    writeJsonError(res, 500, `复制：${describeFsError(e)}`);
    return;
  }
  res.writeHead(200, { "Content-Type": "application/json; charset=utf-8" });
  res.end(JSON.stringify({ ok: true, to: dest, autoRenamed: dest !== to }));
}

/** 递归复制文件或目录。符号链接按 lstat 判定：链接本身不跟随，免得复制出环。 */
function copyRecursive(src, dest) {
  const st = fs.lstatSync(src);
  if (st.isSymbolicLink()) {
    fs.symlinkSync(fs.readlinkSync(src), dest);
    return;
  }
  if (st.isDirectory()) {
    fs.mkdirSync(dest, { recursive: true });
    for (const name of fs.readdirSync(src)) copyRecursive(path.join(src, name), path.join(dest, name));
    return;
  }
  fs.copyFileSync(src, dest);
  fs.chmodSync(dest, st.mode & 0o777);
}

// P2-1 排序 / P2-2 搜索 / P2-6 星标 / P2-8 存储用量 的 HTTP 层。
// 数据与规则在 fm-meta.cjs，这里只做参数解析与响应。


/** P2-6 星标：GET 列出全部，POST 置/取消（body 或 query 带 starred=0|1）。 */
async function handleStar(req, res) {
  const urlObj = new URL(req.url, "http://localhost");
  if (req.method === "GET") {
    res.writeHead(200, { "Content-Type": "application/json; charset=utf-8" });
    res.end(JSON.stringify({ ok: true, starred: meta.listStarred() }));
    return;
  }
  if (req.method !== "POST") { writeJsonError(res, 405, "method not allowed"); return; }

  const body = await readJsonBody(req);
  const p = body.path || urlObj.searchParams.get("path");
  const abs = resolveFMRoot(p || "");
  if (abs === null) { writeJsonError(res, 403, "path is outside allowed roots"); return; }
  const nameErr = validateName(path.basename(abs));
  if (nameErr) { writeJsonError(res, 400, nameErr); return; }
  const cycle = detectSymlinkCycle(abs);
  if (cycle) { writeJsonError(res, 200, cycle); return; }
  if (!fs.existsSync(abs)) { writeJsonError(res, 404, "路径不存在"); return; }

  const on = body.starred === undefined ? true : Boolean(body.starred);
  await meta.setStar(abs, on);
  res.writeHead(200, { "Content-Type": "application/json; charset=utf-8" });
  res.end(JSON.stringify({ ok: true, starred: on }));
}

/** P2-2 搜索。q 为空直接 400，别白跑一趟全盘。 */
function handleSearch(req, res) {
  const urlObj = new URL(req.url, "http://localhost");
  const root = resolveFMRoot(urlObj.searchParams.get("path") || "");
  if (root === null) { writeJsonError(res, 403, "path is outside allowed roots"); return; }
  const q = urlObj.searchParams.get("q") || "";
  if (!q.trim()) { writeJsonError(res, 400, "缺少查询词 q"); return; }
  const cycle = detectSymlinkCycle(root);
  if (cycle) { writeJsonError(res, 200, cycle); return; }
  const content = urlObj.searchParams.get("content") !== "0";
  const out = meta.searchFiles(root, q, { content });
  res.writeHead(200, { "Content-Type": "application/json; charset=utf-8" });
  res.end(JSON.stringify({ ok: true, query: q, ...out }));
}

/** P2-8 存储用量。 */
function handleUsage(req, res) {
  const urlObj = new URL(req.url, "http://localhost");
  const root = resolveFMRoot(urlObj.searchParams.get("path") || "");
  if (root === null) { writeJsonError(res, 403, "path is outside allowed roots"); return; }
  const cycle = detectSymlinkCycle(root);
  if (cycle) { writeJsonError(res, 200, cycle); return; }
  if (!fs.existsSync(root)) { writeJsonError(res, 404, "路径不存在"); return; }
  const t0 = Date.now();
  const out = meta.usageOf(root);
  res.writeHead(200, { "Content-Type": "application/json; charset=utf-8" });
  res.end(JSON.stringify({ ok: true, path: root, tookMs: Date.now() - t0, ...out }));
}

/**
 * P2-1 排序。dirsFirst 是 UI 既有约定（目录恒排前面），不因 sort 而变。
 */

function handleDownload(req, res) {
  // /api/download is used by the file manager (index-fm.html) for the file
  // tree AND the preview pane. Unlike /api/file-content (which stays
  // restricted to the agent's safe roots for viewer.html), this route
  // intentionally follows resolveFMRoot so the user can preview ANY file
  // they browsed to on this machine - including other drives and
  // remote-mounted folders.
  const urlObj = new URL(req.url, "http://localhost");
  const raw = String(urlObj.searchParams.get("path") || "").trim();
  if (!raw) { writeJsonError(res, 400, "path is required"); return; }
  const abs = resolveFMRoot(raw);
  if (abs === null || abs === "__drives__") {
    writeJsonError(res, 403, "path is outside allowed roots");
    return;
  }
  if (!fs.existsSync(abs)) {
    writeJsonError(res, 404, "file not found: " + abs);
    return;
  }
  const stat = fs.statSync(abs);
  if (stat.isDirectory()) {
    writeJsonError(res, 400, "path is a directory, not a file");
    return;
  }
  const ext = path.extname(abs).toLowerCase().replace(/^\./, "");
  const mime = MIME_BY_EXT[ext] || "application/octet-stream";
  streamFileWithRange(req, res, abs, stat, mime);
}

function handleDeleteFile(req, res) {
  const urlObj = new URL(req.url, "http://localhost");
  const abs = resolveFMRoot(urlObj.searchParams.get("path") || "");
  if (abs === null) { writeJsonError(res, 403, "path is outside allowed roots"); return; }

  // P0-4：路径末段也要过文件名校验，避免借删除接口绕过写侧规则
  const nameErr = validateName(path.basename(abs));
  if (nameErr) { writeJsonError(res, 400, nameErr); return; }

  // P0-3：软链环明确回报，不抛裸 ELOOP 变 500
  const cycle = detectSymlinkCycle(abs);
  if (cycle) { writeJsonError(res, 200, cycle); return; }

  // lstat 而非 stat：环上的路径 existsSync 会直接抛 ELOOP
  let st = null;
  try { st = fs.lstatSync(abs); }
  catch (e) { writeJsonError(res, 400, describeFsError(e)); return; }
  if (!st) { writeJsonError(res, 400, "路径不存在"); return; }

  // P1-2：删除改为移入回收站，不再 fs.rmSync 硬删。
  // 硬删一旦落盘就无法撤销——这就是这条被排进 P1 的原因。
  let entry = null;
  try { entry = trash.trashPath(abs); }
  catch (e) { writeJsonError(res, 500, describeFsError(e)); return; }
  res.writeHead(200, { "Content-Type": "application/json; charset=utf-8" });
  res.end(JSON.stringify({ ok: true, trashed: true, id: entry.id, name: entry.name }));
}

// ---------------------------------------------------------------------------
// P1-2 回收站：列表 / 还原 / 彻底删除 / 清空
// ---------------------------------------------------------------------------

function sendJson(res, code, obj) {
  res.writeHead(code, { "Content-Type": "application/json; charset=utf-8" });
  res.end(JSON.stringify(obj));
}

function handleTrashList(req, res) {
  sendJson(res, 200, { ok: true, items: trash.listTrash() });
}

// 这几个端点只吃 body 里的 id/URL 参数，不碰 resolveFMRoot ——
// 回收站里的东西本来就是「已经不在任何根目录之下」的东西。
function readBody(req) {
  return new Promise((resolve) => {
    let raw = "";
    req.on("data", (c) => {
      raw += c;
      if (raw.length > 1e6) { req.destroy(); resolve(null); }
    });
    req.on("end", () => { try { resolve(JSON.parse(raw || "{}")); } catch { resolve(null); } });
    req.on("error", () => resolve(null));
  });
}

async function handleTrashAction(req, res, action) {
  const body = await readBody(req);
  if (!body) { writeJsonError(res, 400, "请求体不是合法 JSON"); return; }
  try {
    if (action === "restore") return sendJson(res, 200, { ok: true, ...trash.restoreEntry(body.id) });
    if (action === "purge") return sendJson(res, 200, { ok: true, ...trash.purgeEntry(body.id) });
    if (action === "empty") return sendJson(res, 200, { ok: true, ...trash.emptyTrash() });
  } catch (e) {
    // 模块用 fmCode 表达「这是预期内的拒绝」，其余才算真故障
    return writeJsonError(res, e.fmCode || 500, e.message);
  }
  writeJsonError(res, 404, "未知操作");
}

// ---------------------------------------------------------------------------
// P1-10 租约锁：acquire / renew / release
// ---------------------------------------------------------------------------

async function handleLockAction(req, res, action) {
  const body = await readBody(req);
  if (!body) { writeJsonError(res, 400, "请求体不是合法 JSON"); return; }
  const abs = resolveFMRoot(body.path || "");
  if (abs === null) { writeJsonError(res, 403, "path is outside allowed roots"); return; }
  // 锁只对普通文件有意义；目录级的锁会把整棵树锁死
  try {
    if (fs.existsSync(abs) && fs.statSync(abs).isDirectory()) {
      writeJsonError(res, 400, "目录不支持编辑锁"); return;
    }
  } catch (e) { writeJsonError(res, 500, describeFsError(e)); return; }

  if (action === "acquire") {
    const r = locks.acquire(abs, body.owner || "anonymous");
    if (!r.ok) {
      writeJsonError(res, 409, `「${path.basename(abs)}」正在被 ${r.holder.owner} 编辑`, { holder: r.holder, expiresIn: r.expiresIn });
      return;
    }
    sendJson(res, 200, { ok: true, token: r.token, expiresAt: r.expiresAt, reentrant: r.reentrant });
    return;
  }
  if (action === "renew") {
    const r = locks.renew(abs, body.token);
    if (!r.ok) {
      writeJsonError(res, 409,
        r.reason === "stolen" ? "锁已被别人接管" : "编辑锁已过期", r);
      return;
    }
    sendJson(res, 200, { ok: true, expiresAt: r.expiresAt });
    return;
  }
  if (action === "release") {
    const r = locks.release(abs, body.token);
    if (!r.ok) { writeJsonError(res, 409, "锁已被别人接管，释放被拒绝", r); return; }
    sendJson(res, 200, { ok: true });
    return;
  }
  writeJsonError(res, 404, "未知操作");
}

function handleLockList(req, res) { sendJson(res, 200, { ok: true, locks: locks.listLocks() }); }

function handleSaveFile(req, res) {
  const urlObj = new URL(req.url, "http://localhost");
  const abs = resolveFMRoot(urlObj.searchParams.get("path") || "");
  if (abs === null) { writeJsonError(res, 403, "path is outside allowed roots"); return; }

  // P0-3：环上的路径返回 200 + 文案，不抛裸 ELOOP 变 500
  const cycle = detectSymlinkCycle(abs);
  if (cycle) { writeJsonError(res, 200, cycle); return; }

  // P0-4：应用层拦非法名。这些名字在 POSIX 上完全合法，不能等 OS 报错
  const nameErr = validateName(path.basename(abs));
  if (nameErr) { writeJsonError(res, 400, nameErr); return; }

  const stat = fs.existsSync(abs) ? fs.statSync(abs) : null;
  if (stat && stat.isDirectory()) { writeJsonError(res, 400, "cannot save over a directory"); return; }

  // P1-10：保存前必须持锁。不校验的话锁只是个摆设——照样能互相覆盖，
  // 只是界面上多了一句「正在编辑」的提示而已。
  const lockToken = urlObj.searchParams.get("lock");
  if (lockToken) {
    const lk = locks.check(abs, lockToken);
    if (!lk.ok) {
      writeJsonError(res, 409,
        lk.reason === "stolen" ? "锁已被别人接管，保存被拒绝" : "编辑锁已过期，请重新打开文件", { lock: lk });
      return;
    }
  }

  // P0-7：显式 overwrite=1 才允许写等价名，否则 Foo 存在时写 foo 会在 macOS
  // 静默覆盖、在 Linux 变成两个文件。完全同名的就地覆盖始终允许。
  const forceOverwrite = urlObj.searchParams.get("overwrite") === "1";
  if (!forceOverwrite) {
    const conflict = findNameConflict(path.dirname(abs), path.basename(abs));
    if (conflict && conflict.name !== path.basename(abs)) {
      writeJsonError(res, 409, `名称冲突：已存在 "${conflict.name}"（大小写或 Unicode 等价）`, {
        conflict: conflict.name,
        options: ["rename", "overwrite", "keep-both"],
      });
      return;
    }
  }

  // P0-2 + P0-1：流式落盘 + 原子替换。
  // 字节全程不经过 JS 字符串：旧的 `body += chunk` 会把 Buffer 按 UTF-8 解码，
  // 非 UTF-8 序列（PNG/ZIP/字体）变 U+FFFD 且体积膨胀，还照样回 {"ok":true}。
  // 先写同目录临时文件再 rename，读者永远看不到半截文件。
  atomicWriteStream(abs, req).then(() => {
    if (res.writableEnded || res.destroyed) return;
    res.writeHead(200, { "Content-Type": "application/json; charset=utf-8" });
    res.end(JSON.stringify({ ok: true }));
  }).catch((err) => {
    if (res.headersSent || res.writableEnded) return;
    writeJsonError(res, 500, describeFsError(err));
  });
}

// ---------------------------------------------------------------------------
// P1-5 分片上传 + 断点续传 / P1-6 完整性校验
// ---------------------------------------------------------------------------

const MAX_PART_BYTES = 64 * 1024 * 1024; // 单片上限，防止有人把整文件塞进一片

/** 读一个分片的原始字节，带上限。超限直接掐断，不吃满内存。 */
function readPartBody(req) {
  return new Promise((resolve, reject) => {
    const chunks = []; let total = 0;
    req.on("data", (c) => {
      total += c.length;
      if (total > MAX_PART_BYTES) { reject(new Error("分片超过上限 " + MAX_PART_BYTES)); req.destroy(); return; }
      chunks.push(c);
    });
    req.on("end", () => resolve(Buffer.concat(chunks, total)));
    req.on("error", reject);
  });
}

/** 自描述元数据：算法名与值都要落盘，换台机器也读得出这是怎么校验过的。 */
function writeUploadMeta(dataDir, abs, info) {
  const dir = path.join(dataDir, "meta");
  fs.mkdirSync(dir, { recursive: true });
  // 复用 fm-upload 的 sha256（这里只是要个稳定的文件名键，不需要额外 import crypto）
  const key = upload.sha256Hex(abs).slice(0, 32);
  const file = path.join(dir, key + ".json");
  // 只记算法名与值，不碰 ETag —— spec 明确说过 ETag 不能拿来当 MD5。
  fs.writeFileSync(file, JSON.stringify({
    path: abs, size: info.size, updatedAt: new Date().toISOString(),
    checksum: { "sha256": info.sha256, "crc32c": info.crc32c },
  }, null, 2));
  return file;
}

function handleUploadInit(req, res) {
  readBody(req).then((body) => {
    const abs = resolveFMRoot(body.path || "");
    if (abs === null) { writeJsonError(res, 403, "path is outside allowed roots"); return; }
    const size = Number(body.size);
    if (!Number.isFinite(size) || size < 0) { writeJsonError(res, 400, "size 必须是非负数字"); return; }
    // 清掉上次进程崩掉留下的暂存残留；不清理的话 dataDir 会随崩溃次数涨。
    upload.sweepOrphans(trash.dataDir());
    const s = upload.initSession({ uploadId: body.uploadId, target: abs, size, chunkSize: body.chunkSize, dataDir: trash.dataDir() });
    sendJson(res, 200, {
      ok: true, uploadId: s.id, offset: s.received, size: s.size,
      chunkSize: s.chunkSize, algorithm: "CRC32C", checksumHeader: "X-Checksum-Crc32c",
    });
  }).catch((e) => writeJsonError(res, 400, describeFsError(e)));
}

/** 客户端重新对齐用：断线重连后先问服务端「你到底收到多少」。 */
function handleUploadStatus(req, res) {
  // 本函数的作用域里没有 urlObj（tryHandleFileManager 不传），与 handleSaveFile
  // 等保持一致，自己从 req.url 解析 query。
  const urlObj = new URL(req.url, "http://localhost");
  const id = urlObj.searchParams.get("uploadId");
  try {
    const s = upload.getSession(id);
    sendJson(res, 200, { ok: true, uploadId: s.id, offset: s.received, size: s.size, remaining: upload.remaining(s) });
  } catch (e) {
    writeJsonError(res, 404, e.message, { code: e.code });
  }
}

function handleUploadPart(req, res) {
  const urlObj = new URL(req.url, "http://localhost");
  const id = urlObj.searchParams.get("uploadId");
  const offset = Number(urlObj.searchParams.get("offset"));
  const declared = req.headers["x-checksum-crc32c"];
  let s;
  try { s = upload.getSession(id); }
  catch (e) { writeJsonError(res, 404, e.message, { code: e.code }); return; }

  readPartBody(req).then((buf) => {
    try {
      const r = upload.putPart(s, offset, buf, declared);
      sendJson(res, 200, { ok: true, offset: r.received, received: r.received, remaining: upload.remaining(s) });
    } catch (e) {
      // offset 打架是**可恢复**冲突：把服务端真实进度回给客户端，让它先对齐。
      // CRC 不符是**不可恢复**的数据错误：整片已丢弃，客户端必须重传这一片。
      if (e.code === "OFFSET") { writeJsonError(res, 409, e.message, { code: e.code, expectedOffset: e.expected }); return; }
      if (e.code === "CRC") { writeJsonError(res, 422, e.message, { code: e.code, received: s.received }); return; }
      writeJsonError(res, 400, e.message, { code: e.code });
    }
  }).catch((e) => writeJsonError(res, 413, describeFsError(e)));
}

function handleUploadCommit(req, res) {
  readBody(req).then((body) => {
    let s;
    try { s = upload.getSession(body.uploadId); }
    catch (e) { writeJsonError(res, 404, e.message, { code: e.code }); return; }
    try {
      const info = upload.commitSession(s);
      const meta = writeUploadMeta(trash.dataDir(), s.target, info);
      sendJson(res, 200, { ok: true, ...info, meta, checksum: { "sha256": info.sha256, "crc32c": info.crc32c } });
    } catch (e) {
      writeJsonError(res, 409, e.message, { code: e.code });
    }
  }).catch((e) => writeJsonError(res, 400, describeFsError(e)));
}

function handleUploadAbort(req, res) {
  readBody(req).then((body) => {
    upload.abortSession(body.uploadId);
    sendJson(res, 200, { ok: true });
  }).catch((e) => writeJsonError(res, 400, describeFsError(e)));
}

function handleUploadFile(req, res) {
  const urlObj = new URL(req.url, "http://localhost");
  const dir = resolveFMRoot(urlObj.searchParams.get("path") || "");
  const name = String(urlObj.searchParams.get("name") || "").trim();
  if (dir === null || dir === "__drives__") { writeJsonError(res, 403, "path is outside allowed roots"); return; }

  // P0-3
  const cycle = detectSymlinkCycle(dir);
  if (cycle) { writeJsonError(res, 200, cycle); return; }
  if (!fs.existsSync(dir) || !fs.statSync(dir).isDirectory()) { writeJsonError(res, 400, "target path is not a directory"); return; }
  const clean = truncateName(path.basename(name));
  // P0-4/P0-5：超长名按字素簇截短（不是劈开 emoji），其余非法名直接 4xx。
  // 旧逻辑是把非法字符静默替换成 _，会让"存 A 文件"变成"存 A_ 文件"，用户回头找不到。
  const nameErr = validateName(clean);
  if (!clean || nameErr) { writeJsonError(res, 400, nameErr || "invalid file name"); return; }
  // unique=1 keeps earlier attachments intact (-2 / -3 suffix).
  const wantUnique = urlObj.searchParams.get("unique") === "1";
  const chunks = [];
  let size = 0;
  const LIMIT = 1024 * 1024 * 1024; // 1 GB cap
  let tooBig = false;
  req.on("data", chunk => {
    if (tooBig) return;
    size += chunk.length;
    if (size > LIMIT) { tooBig = true; chunks.length = 0; return; }
    chunks.push(chunk);
  });
  req.on("end", () => {
    if (tooBig) { writeJsonError(res, 413, "file too large (>1GB)"); return; }
    let finalName = clean;
    if (wantUnique) {
      const ext = path.extname(clean);
      const stem = clean.slice(0, clean.length - ext.length);
      for (let n = 2; n <= 999 && fs.existsSync(path.join(dir, finalName)); n += 1) {
        finalName = stem + "-" + n + ext;
      }
    }
    const target = path.join(dir, finalName);
    try { fs.writeFileSync(target, Buffer.concat(chunks)); }
    catch (e) { writeJsonError(res, 500, e.message); return; }
    res.writeHead(200, { "Content-Type": "application/json; charset=utf-8" });
    res.end(JSON.stringify({ ok: true, path: target.replace(/\\/g, "/"), size, name: finalName }));
  });
  req.on("error", () => {
    try { writeJsonError(res, 500, "upload aborted"); } catch (_) {}
  });
}

function handleCreateFolder(req, res) {
  let body = "";
  req.on("data", chunk => { body += chunk; });
  req.on("end", () => {
    let payload;
    try { payload = JSON.parse(body || "{}"); }
    catch (e) { writeJsonError(res, 400, "bad json"); return; }
    const parent = resolveFMRoot(payload.path || "");
    // 故意不 trim：静默裁掉尾随空格/点会让"建 A 目录"变成"建 A 目录"（用户找不到）
    let name = String(payload.name === undefined || payload.name === null ? "" : payload.name);
    // P0-5：超长名按字素簇截短 + 稳定后缀，而不是直接报错
    name = truncateName(name);
    if (parent === null) { writeJsonError(res, 403, "path is outside allowed roots"); return; }
    // P0-4：应用层统一拦非法名（这些在 POSIX 上完全合法）
    const nameErr = validateName(name);
    if (nameErr) { writeJsonError(res, 400, nameErr); return; }
    // P0-3
    const target = path.join(parent, name);
    const cycle = detectSymlinkCycle(target);
    if (cycle) { writeJsonError(res, 200, cycle); return; }
    // P0-7：等价名冲突同样不静默并存；payload.overwrite 可显式放行
    const forceOverwrite = payload.overwrite === true || payload.overwrite === "1";
    const conflict = forceOverwrite ? null : findNameConflict(parent, name);
    if (conflict && conflict.name !== name) {
      writeJsonError(res, 409, `名称冲突：已存在 "${conflict.name}"`, { conflict: conflict.name, options: ["rename","overwrite","keep-both"] });
      return;
    }
    // recursive + idempotent: the first upload creates its own dir.
    try { fs.mkdirSync(path.join(parent, name), { recursive: true }); }
    catch (e) { writeJsonError(res, 500, describeFsError(e)); return; }
    res.writeHead(200, { "Content-Type": "application/json; charset=utf-8" });
    res.end(JSON.stringify({ ok: true }));
  });
}

function serveIndexFM(req, res) {
  const path = require("path").join(__dirname, "index-fm.html");
  if (!fs.existsSync(path)) { writeJsonError(res, 500, "index-fm.html missing"); return; }
  const data = fs.readFileSync(path);
  res.writeHead(200, {
    "Content-Type": "text/html; charset=utf-8",
    "Content-Length": data.length,
    "Cache-Control": "no-store",
  });
  res.end(data);
}

const MIME_BY_EXT = {
  pdf: "application/pdf",
  docx: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
  xlsx: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
  xls: "application/vnd.ms-excel",
  ppt: "application/vnd.ms-powerpoint",
  pptx: "application/vnd.openxmlformats-officedocument.presentationml.presentation",
  txt: "text/plain; charset=utf-8",
  md: "text/plain; charset=utf-8",
  py: "text/plain; charset=utf-8",
  js: "text/javascript; charset=utf-8",
  ts: "text/plain; charset=utf-8",
  tsx: "text/plain; charset=utf-8",
  jsx: "text/plain; charset=utf-8",
  json: "application/json; charset=utf-8",
  yaml: "text/plain; charset=utf-8",
  yml: "text/plain; charset=utf-8",
  html: "text/html; charset=utf-8",
  htm: "text/html; charset=utf-8",
  css: "text/css; charset=utf-8",
  go: "text/plain; charset=utf-8",
  rs: "text/plain; charset=utf-8",
  java: "text/plain; charset=utf-8",
  rb: "text/plain; charset=utf-8",
  sh: "text/plain; charset=utf-8",
  bash: "text/plain; charset=utf-8",
  ps1: "text/plain; charset=utf-8",
  bat: "text/plain; charset=utf-8",
  csv: "text/csv; charset=utf-8",
  xml: "application/xml; charset=utf-8",
  // audio (browser <audio> tag supports these natively)
  mp3: "audio/mpeg",
  m4a: "audio/mp4",
  wav: "audio/wav",
  flac: "audio/flac",
  ogg: "audio/ogg",
  oga: "audio/ogg",
  aac: "audio/aac",
  opus: "audio/opus",
  // video (browser <video> tag supports these natively)
  mp4: "video/mp4",
  m4v: "video/mp4",
  webm: "video/webm",
  mkv: "video/x-matroska",
  mov: "video/quicktime",
  avi: "video/x-msvideo",
  mov1: "video/quicktime",
  // images
  png: "image/png",
  jpg: "image/jpeg",
  jpeg: "image/jpeg",
  gif: "image/gif",
  webp: "image/webp",
  svg: "image/svg+xml",
  bmp: "image/bmp",
  ico: "image/x-icon",
  avif: "image/avif",
  tif: "image/tiff",
  tiff: "image/tiff",
  // audio
  mp3: "audio/mpeg",
  wav: "audio/wav",
  m4a: "audio/mp4",
  aac: "audio/aac",
  ogg: "audio/ogg",
  oga: "audio/ogg",
  opus: "audio/ogg",
  flac: "audio/flac",
  weba: "audio/webm",
  wma: "audio/x-ms-wma",
  // video
  mp4: "video/mp4",
  m4v: "video/mp4",
  webm: "video/webm",
  mov: "video/quicktime",
  mkv: "video/x-matroska",
  avi: "video/x-msvideo",
  ogv: "video/ogg",
  // fonts
  otf: "font/otf",
  ttf: "font/ttf",
  woff: "font/woff",
  woff2: "font/woff2",
  // misc documents
  rtf: "application/rtf",
  epub: "application/epub+zip",
  odt: "application/vnd.oasis.opendocument.text",
  ods: "application/vnd.oasis.opendocument.spreadsheet",
  odp: "application/vnd.oasis.opendocument.presentation",
  zip: "application/zip",
};

// ---------- static assets ----------

function serveVendorFile(req, res, pathname) {
  const rel = decodeURIComponent(pathname.slice("/vendor/".length)).replace(/\\/g, "/");
  const vendorRoot = path.join(__dirname, "vendor");
  const fp = path.resolve(vendorRoot, rel);
  if (fp.toLowerCase() !== vendorRoot.toLowerCase() && !fp.toLowerCase().startsWith(vendorRoot.toLowerCase() + path.sep)) {
    writeJsonError(res, 403, "invalid vendor path"); return;
  }
  if (!fs.existsSync(fp) || !fs.statSync(fp).isFile()) {
    writeJsonError(res, 404, "vendor file not found"); return;
  }
  const ext = path.extname(fp).toLowerCase();
  const mime = ({ ".js": "text/javascript; charset=utf-8", ".mjs": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8", ".json": "application/json; charset=utf-8", ".map": "application/json; charset=utf-8", ".woff2": "font/woff2", ".woff": "font/woff", ".ttf": "font/ttf", ".svg": "image/svg+xml", ".png": "image/png", ".wasm": "application/wasm" })[ext] || "application/octet-stream";
  const data = fs.readFileSync(fp);
  res.writeHead(200, { "Content-Type": mime, "Content-Length": data.length, "Cache-Control": "public, max-age=604800" });
  res.end(data);
}

// P3 前端资源：/fm-assets/<文件名>。
// 刻意用白名单而不是把 __dirname 整个挂出去——那等于把 filemanager.cjs
// 的源码和 vendor 目录一起开放给浏览器。要加新资源就往这个数组里加一行。
const FM_ASSETS = ["tokens.css", "fm-p3.css", "fm-p3.js", "fm-lock.js", "fm-upload.js", "fm-tabs.css", "fm-tabs.js"];

function serveFmAsset(req, res, pathname) {
  const name = decodeURIComponent(pathname.slice("/fm-assets/".length));
  if (!FM_ASSETS.includes(name)) { writeJsonError(res, 404, "asset not found"); return; }
  const fp = path.join(__dirname, name);
  if (!fs.existsSync(fp) || !fs.statSync(fp).isFile()) {
    writeJsonError(res, 404, "asset not found"); return;
  }
  const mime = ({ ".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8" })[path.extname(fp).toLowerCase()] || "application/octet-stream";
  const data = fs.readFileSync(fp);
  res.writeHead(200, { "Content-Type": mime, "Content-Length": data.length, "Cache-Control": "no-cache" });
  res.end(data);
}

function serveViewerPage(req, res) {
  const viewerPath = path.join(__dirname, "viewer.html");
  if (!fs.existsSync(viewerPath)) { writeJsonError(res, 500, "viewer.html missing on disk"); return; }
  const data = fs.readFileSync(viewerPath);
  res.writeHead(200, {
    "Content-Type": "text/html; charset=utf-8",
    "Content-Length": data.length,
    "Cache-Control": "no-store",
  });
  res.end(data);
}

// ---------- route dispatch ----------

// tryHandleFileManager claims every file-manager route. Returns true when
// the request was handled (the response is already written); false means
// "not mine" and the caller should keep dispatching to other handlers.
function tryHandleFileManager(req, res, pathname, method, sendJson) {
  // P4-1 WebDAV：自定义方法（PROPFIND/MOVE/COPY/MKCOL…）必须在这里最先拦，
  // 放在上面那个 `if (method === "GET")` 里是接不到的。
  if (pathname === "/dav" || pathname.startsWith("/dav/")) {
    require('./fm-webdav.cjs').handleWebdav(req, res, pathname); return true;
  }
  if (method === "HEAD" && pathname === "/api/file-content") { serveFileContent(req, res); return true; }
  if (method === "GET") {
    if (pathname.startsWith("/vendor/")) { serveVendorFile(req, res, pathname); return true; }
    if (pathname.startsWith("/fm-assets/")) { serveFmAsset(req, res, pathname); return true; }
    if (pathname === "/viewer.html" || pathname === "/viewer") { serveViewerPage(req, res); return true; }
    if (pathname === "/api/file-content") { serveFileContent(req, res); return true; }
    if (pathname === "/api/download") { handleDownload(req, res); return true; }
    if (pathname === "/api/files") { handleListFiles(req, res); return true; }
    // P2 检索与用量：都是只读查询，走 GET
    if (pathname === "/api/search") { handleSearch(req, res); return true; }
    if (pathname === "/api/usage") { handleUsage(req, res); return true; }
    // P1-2 回收站列表（写操作在下方 POST 段）
    if (pathname === "/api/trash" && method === "GET") { handleTrashList(req, res); return true; }
    // P1-10 锁列表（写操作在下方 POST 段）
    if (pathname === "/api/lock" && method === "GET") { handleLockList(req, res); return true; }
    if ((pathname === "/api/upload/status" || pathname === "/api/upload/status/") && (method === "GET" || method === "HEAD")) { handleUploadStatus(req, res); return true; }
    if (pathname === "/api/star") { handleStar(req, res); return true; }
    if (pathname === "/" || pathname === "/index.html" || pathname === "/index-fm.html" || pathname === "/filemanager" || pathname === "/filemanager/") { serveIndexFM(req, res); return true; }
    if (pathname.startsWith("/fm-assets/")) { serveFmAsset(req, res, pathname); return true; }
    return false;
  }
  if (method === "DELETE") {
    if (pathname === "/api/files") { handleDeleteFile(req, res); return true; }
    return false;
  }
  if (method === "POST" || method === "PUT") {
    if (pathname === "/api/folder") { handleCreateFolder(req, res); return true; }
    if (pathname === "/api/upload") { handleUploadFile(req, res); return true; }
    if (pathname === "/api/file" && method === "PUT") { handleSaveFile(req, res); return true; }
    // P1-1 核心操作：重命名 / 移动 / 复制
    if (pathname === "/api/rename" && method === "POST") { handleRenameOrMove(req, res, false); return true; }
    if (pathname === "/api/move" && method === "POST") { handleRenameOrMove(req, res, true); return true; }
    if (pathname === "/api/copy" && method === "POST") { handleCopy(req, res); return true; }
    // P1-2 回收站：写操作（列表在 GET 段）
    if (pathname === "/api/trash/restore" && method === "POST") { handleTrashAction(req, res, "restore"); return true; }
    if (pathname === "/api/trash/purge" && method === "POST") { handleTrashAction(req, res, "purge"); return true; }
    if (pathname === "/api/trash/empty" && method === "POST") { handleTrashAction(req, res, "empty"); return true; }
    // P1-10 租约锁
    if (pathname === "/api/lock/acquire" && method === "POST") { handleLockAction(req, res, "acquire"); return true; }
    // P1-5 分片上传
    if (pathname === "/api/upload/init" && method === "POST") { handleUploadInit(req, res); return true; }
    if (pathname === "/api/upload/part" && method === "PUT") { handleUploadPart(req, res); return true; }
    if (pathname === "/api/upload/commit" && method === "POST") { handleUploadCommit(req, res); return true; }
    if (pathname === "/api/upload/abort" && method === "POST") { handleUploadAbort(req, res); return true; }
    if (pathname === "/api/lock/renew" && method === "POST") { handleLockAction(req, res, "renew"); return true; }
    if (pathname === "/api/lock/release" && method === "POST") { handleLockAction(req, res, "release"); return true; }
    // P2-6 星标：GET 列表在上面的 GET 段，这里只放写操作
    if (pathname === "/api/star" && method === "POST") { handleStar(req, res); return true; }
    return false;
  }
  return false;
}

// CleanupSession drops the deduped path memo for a finished chat so the map
// doesn't grow unbounded across many sessions in a long-running server.
function cleanupSession(sessionName) {
  viewerSeenThisSession.delete(sessionName);
}

module.exports = {
  tryHandleFileManager,
  // P4-1 fm-webdav.cjs 惰性 require 本模块取 MIME 表复用，这里导出一份
  MIME_BY_EXT,
  findViewerPaths,
  viewerAutoOpen,
  spawnViewerFor,
  spawnFileManagerFor,
  spawnPptPreviewFor,
  cleanupSession,
};
