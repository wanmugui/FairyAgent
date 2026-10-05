"use strict";
/**
 * P2 组织与检索的后端支撑：星标存储、模糊搜索、存储用量。
 *
 * 星标是"用户数据"不是"代码"，所以落在 XDG data dir 而不是仓库里——
 * 放进 workspace/ 会被 git 跟踪，然后某天不小心连同测试数据一起提交出去。
 */

const fs = require("fs");
const fsp = require("fs/promises");
const path = require("path");
const os = require("os");

// ---------------------------------------------------------------------------
// 星标存储（P2-6）
// ---------------------------------------------------------------------------

function dataDir() {
  const base = process.env.XDG_DATA_HOME || path.join(os.homedir(), ".local", "share");
  return path.join(base, "Fairy", "filemanager");
}
function starsFile() {
  return path.join(dataDir(), "stars.json");
}

let _starsCache = null;
function loadStars() {
  if (_starsCache) return _starsCache;
  try {
    const raw = fs.readFileSync(starsFile(), "utf8");
    const j = JSON.parse(raw);
    _starsCache = j && typeof j === "object" && !Array.isArray(j) ? j : {};
  } catch {
    _starsCache = {};
  }
  return _starsCache;
}

async function saveStars(obj) {
  await fsp.mkdir(dataDir(), { recursive: true });
  // 与 P0-2 同一个理由：星标写一半崩掉，等于用户的星标全丢
  const { atomicWriteStream } = require("./fm-path.cjs");
  const { Readable } = require("stream");
  await atomicWriteStream(starsFile(), Readable.from([JSON.stringify(obj, null, 2)]));
  _starsCache = obj;
}

/** 键用绝对路径，保证同一文件无论从哪个入口星标都指向同一条。 */
function starKey(abs) {
  return path.resolve(abs);
}

function isStarred(abs) {
  return Boolean(loadStars()[starKey(abs)]);
}

function listStarred() {
  return Object.keys(loadStars()).sort();
}

async function setStar(abs, on) {
  const s = loadStars();
  const k = starKey(abs);
  if (on) s[k] = { starredAt: Date.now() };
  else delete s[k];
  await saveStars(s);
  return on;
}

// ---------------------------------------------------------------------------
// 模糊匹配（P2-2）
// ---------------------------------------------------------------------------

/**
 * 子序列模糊匹配：查询字符按顺序出现在目标中即可（"rpt" 命中 "report"）。
 * 返回 null 表示不匹配，否则返回打分：越小越靠前。
 */
function fuzzyScore(query, target) {
  const q = query.toLowerCase();
  const t = target.toLowerCase();

  if (t === q) return 0;                       // 完全相等
  if (t.startsWith(q)) return 1;                // 前缀
  // 词首：foo-bar / foo_bar / foo bar
  const wordStart = new RegExp(`(^|[\\s._\\-/\\\\])${escapeRe(q)}`).test(t);
  if (wordStart) return 2;
  // 子序列
  let ti = 0;
  let gap = 0;
  for (const ch of q) {
    const found = t.indexOf(ch, ti);
    if (found === -1) return null;
    gap += found - ti;
    ti = found + 1;
  }
  return 3 + gap * 0.01;
}

function escapeRe(s) {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

// ---------------------------------------------------------------------------
// 搜索（P2-2）
// ---------------------------------------------------------------------------

const CONTENT_MAX_BYTES = 512 * 1024;   // 只在 512KB 以内的文本里找
const SEARCH_MAX_FILES = 20000;         // 兜底，别把整块盘读穿
const SEARCH_MAX_DEPTH = 12;

function looksBinary(buf) {
  const n = Math.min(buf.length, 4096);
  for (let i = 0; i < n; i++) if (buf[i] === 0) return true;
  return false;
}

/**
 * 在 root 下搜 query。
 * @returns {{results:Array, scanned:number, truncated:boolean, slow:boolean, tookMs:number}}
 */
function searchFiles(root, query, opts = {}) {
  const started = Date.now();
  const q = String(query || "").trim();
  if (!q) return { results: [], scanned: 0, truncated: false, slow: false, tookMs: 0 };
  const withContent = opts.content !== false;
  const results = [];
  let scanned = 0;
  let truncated = false;

  const queue = [{ dir: root, depth: 0 }];
  while (queue.length) {
    const { dir, depth } = queue.shift();
    if (depth > SEARCH_MAX_DEPTH) continue;
    let entries;
    try { entries = fs.readdirSync(dir, { withFileTypes: true }); }
    catch { continue; }
    for (const ent of entries) {
      if (scanned >= SEARCH_MAX_FILES) { truncated = true; break; }
      const full = path.join(dir, ent.name);
      if (ent.isDirectory()) {
        queue.push({ dir: full, depth: depth + 1 });
        continue;
      }
      scanned++;
      let nameScore = fuzzyScore(q, ent.name);
      let contentHit = false;
      if (nameScore === null && withContent) {
        // 名字没中，再看内容。只对文本文件动手，且先按大小筛掉。
        try {
          const st = fs.lstatSync(full);
          if (st.size <= CONTENT_MAX_BYTES && !ent.isSymbolicLink()) {
            const fd = fs.openSync(full, "r");
            try {
              const buf = Buffer.alloc(Math.min(st.size, CONTENT_MAX_BYTES));
              fs.readSync(fd, buf, 0, buf.length, 0);
              if (!looksBinary(buf) && buf.toString("utf8").toLowerCase().includes(q.toLowerCase())) {
                contentHit = true;
              }
            } finally { fs.closeSync(fd); }
          }
        } catch { /* 读不了就当没命中 */ }
      }
      if (nameScore === null && !contentHit) continue;
      let st = null;
      try { st = fs.lstatSync(full); } catch { /* 可能在遍历中消失 */ }
      results.push({
        name: ent.name,
        path: full,
        size: st ? st.size : 0,
        modified: st ? st.mtimeMs : 0,
        // 内容命中的排在所有名字命中之后
        rank: contentHit ? 9 : nameScore,
        matchedBy: contentHit ? "content" : "name",
      });
    }
    if (truncated) break;
  }

  results.sort((a, b) => (a.rank - b.rank) || a.name.localeCompare(b.name));
  const tookMs = Date.now() - started;
  return { results, scanned, truncated, slow: tookMs > 200, tookMs };
}

// ---------------------------------------------------------------------------
// 存储用量（P2-8）
// ---------------------------------------------------------------------------

const USAGE_MAX_DEPTH = 32;

/**
 * 递归统计字节数与文件数。
 * 软链一律不跟随：跟随会把同一个目录算两遍，遇到环还会直接死循环。
 */
function usageOf(root) {
  let bytes = 0;
  let files = 0;
  let dirs = 0;
  let skipped = 0;
  const stack = [{ dir: root, depth: 0 }];
  while (stack.length) {
    const { dir, depth } = stack.pop();
    if (depth > USAGE_MAX_DEPTH) { skipped++; continue; }
    let entries;
    try { entries = fs.readdirSync(dir, { withFileTypes: true }); }
    catch { skipped++; continue; }
    dirs++;
    for (const ent of entries) {
      const full = path.join(dir, ent.name);
      if (ent.isSymbolicLink()) { skipped++; continue; }
      if (ent.isDirectory()) { stack.push({ dir: full, depth: depth + 1 }); continue; }
      try {
        bytes += fs.lstatSync(full).size;
        files++;
      } catch { skipped++; }
    }
  }
  return { bytes, files, dirs, skipped };
}

module.exports = {
  starsFile,
  dataDir,
  isStarred,
  listStarred,
  setStar,
  fuzzyScore,
  searchFiles,
  usageOf,
  looksBinary,
};
