"use strict";
/**
 * filemanager 共享的路径 / 文件名 / 落盘原语。
 *
 * 单独成模块是为了让 P0-2..P0-8 的规则只有一处定义：handler 只管调用，
 * 不各自复述一遍校验逻辑（复述 = 迟早漏改）。
 *
 * 依据：workspace/dev-backlog-filemanager-spec.md 的 P0-2 / P0-3 / P0-4 /
 * P0-5 / P0-7。这些名字在 POSIX 上完全合法，所以必须应用层拦截，不能等 OS 报错。
 */

const fs = require("fs");
const fsp = require("fs/promises");
const path = require("path");
const { randomBytes } = require("crypto");
// 注意用 stream/promises 版：这里按 Promise await 调用，传回调版会抛
// ERR_INVALID_ARG_TYPE（"streams[stream.length-1]" 必须是函数）。
const { pipeline } = require("stream/promises");

/** 单个文件名上限，单位是字节不是字符（中文 3B、emoji 4B）。 */
const MAX_NAME_BYTES = 255;

/** 临时名形如 `.<stem>.<32hex>.part`，要为它预留长度，否则快到上限的名字会因临时名超限而写失败。 */
const TEMP_NAME_RESERVE = 40;

/** Win32 设备保留名。`CON`、`con.txt`、`con .txt`、`con.txt.` 都会被 Win32 认成设备。 */
const WINDOWS_RESERVED = /^(con|prn|aux|nul|com[1-9]|lpt[1-9])$/i;

/** 全平台非法字符：Win32 禁用 + 控制字符。`/` `\` 单独在下面按路径分隔符处理。 */
const ILLEGAL_CHARS = /[<>:"/\\|?*\x00-\x1F]/;

// ---------------------------------------------------------------------------
// P0-5 字素簇
// ---------------------------------------------------------------------------

let _segmenter = null;
function segmenter() {
  if (_segmenter) return _segmenter;
  if (typeof Intl === "undefined" || typeof Intl.Segmenter !== "function") {
    // 没有 Segmenter 就不能安全截断。宁可报错也不要产出半个字符的文件名——
    // 那正是 P0-5 要消灭的 U+FFFD 乱码。
    return null;
  }
  _segmenter = new Intl.Segmenter(undefined, { granularity: "grapheme" });
  return _segmenter;
}

/**
 * 按扩展字素簇切分字符串。
 * 禁止用 Array.from()：它按 code point 切，ZWJ 家庭 emoji（👨‍👩‍👧‍👦）和
 * 区域指示符对（🇨🇳）仍会被劈开。
 */
function graphemes(str) {
  const seg = segmenter();
  if (!seg) return null;
  return Array.from(seg.segment(str), (s) => s.segment);
}

// ---------------------------------------------------------------------------
// P0-4 文件名校验
// ---------------------------------------------------------------------------

/**
 * 校验单个文件名（不含路径分隔符）。
 * @returns {string|null} 错误文案；合法返回 null
 */
function validateName(name) {
  if (typeof name !== "string" || name.length === 0) {
    return "文件名为空";
  }
  if (name === "." || name === "..") {
    return "文件名不能是 . 或 ..";
  }
  if (name.includes("/") || name.includes("\\")) {
    return "文件名不能包含路径分隔符";
  }
  // 控制字符（含 \0）
  if (ILLEGAL_CHARS.test(name)) {
    return "文件名含全平台非法字符 < > : \" / \\ | ? * 或控制字符";
  }
  if (Buffer.byteLength(name) > MAX_NAME_BYTES) {
    return `文件名超过 ${MAX_NAME_BYTES} 字节`;
  }

  // 尾随点/空格：Win32 会静默剥离，导致"上传成功但找不到文件"
  if (/[ .]$/.test(name)) {
    return "文件名不能以空格或点结尾（部分系统会静默剥离）";
  }

  // 保留设备名：去掉扩展名与尾随点空格后再判，拦住 CON.txt / CON .txt / con.txt.
  const base = name.split(".")[0].replace(/[ .]+$/, "");
  if (WINDOWS_RESERVED.test(base)) {
    return "文件名是系统保留设备名（CON / PRN / AUX / NUL / COM1-9 / LPT1-9）";
  }

  return null;
}

/**
 * 超长名按字素簇截断 + 稳定后缀，而不是直接报错。
 * 保留扩展名，方便用户认出文件类型。
 */
function truncateName(name, maxBytes = MAX_NAME_BYTES) {
  if (Buffer.byteLength(name) <= maxBytes) return name;
  const seg = segmenter();
  if (!seg) return name; // 无法安全截断，交由 validateName 报超限

  const dot = name.lastIndexOf(".");
  const hasExt = dot > 0 && name.length - dot <= 16;
  const ext = hasExt ? name.slice(dot) : "";
  const stem = hasExt ? name.slice(0, dot) : name;
  const suffix = "~1";

  const extBytes = Buffer.byteLength(ext);
  const stemBudget = maxBytes - extBytes - Buffer.byteLength(suffix);
  if (stemBudget <= 0) return name;

  let out = "";
  let used = 0;
  for (const g of seg.segment(stem)) {
    const b = Buffer.byteLength(g.segment);
    if (used + b > stemBudget) break;
    out += g.segment;
    used += b;
  }
  return out + suffix + ext;
}

// ---------------------------------------------------------------------------
// P0-7 大小写 / Unicode 冲突
// ---------------------------------------------------------------------------

/**
 * ICU casefold 的近似：NFC 归一 + 小写 + ß→ss。
 * Node 没有暴露完整 casefold（ß/SS、土耳其无点 i 的折叠），这里补最常踩的 ß。
 * 比较统一走 NFC 结果，不比原始字节，否则 macOS 的 é 与 Linux 的 e+U+0301 会看成两个文件。
 */
function casefoldKey(name) {
  return name.normalize("NFC").toLowerCase().replace(/ß/g, "ss");
}

/**
 * 在 dir 中找与 name 冲突的已有条目（大小写不敏感 + NFC 归一）。
 * @returns {{name:string, kind:"exact"|"casefold"}|null}
 */
function findNameConflict(dir, name) {
  let entries;
  try {
    entries = fs.readdirSync(dir, { withFileTypes: true });
  } catch {
    return null;
  }
  const target = casefoldKey(name);
  for (const ent of entries) {
    const other = casefoldKey(ent.name);
    if (other === target) {
      return { name: ent.name, kind: ent.name === name ? "exact" : "casefold" };
    }
  }
  return null;
}

// ---------------------------------------------------------------------------
// P0-3 符号链接
// ---------------------------------------------------------------------------

/**
 * 检测路径上的软链环。
 * lstat 本身不跟随软链，所以自指环不会抛错——必须手工逐跳解析并记录已访问节点。
 * @returns {string|null} "符号链接循环" 或 null
 */
function detectSymlinkCycle(target) {
  let current = path.resolve(target);
  const seen = new Set();
  for (let hops = 0; hops < 40; hops++) {
    let st;
    try {
      st = fs.lstatSync(current);
    } catch (e) {
      if (e.code === "ELOOP") return "符号链接循环";
      return null; // ENOENT 等交给调用方按正常缺失处理
    }
    if (!st.isSymbolicLink()) return null;
    if (seen.has(current)) return "符号链接循环";
    seen.add(current);
    let link;
    try {
      link = fs.readlinkSync(current);
    } catch {
      return null;
    }
    current = path.resolve(path.dirname(current), link);
  }
  return "符号链接层级过深";
}

/** 把 fs 异常翻译成人话。ELOOP 必须显式映射，不能混进"无权限"或"不存在"。 */
function describeFsError(e) {
  switch (e && e.code) {
    case "ELOOP":
      return "符号链接循环";
    case "ENOENT":
      return "路径不存在";
    case "EACCES":
    case "EPERM":
      return "无权限";
    case "EISDIR":
      return "目标是目录";
    case "ENOTDIR":
      return "路径中的某段不是目录";
    case "ENOSPC":
      return "磁盘空间不足";
    case "EMFILE":
    case "ENFILE":
      return "打开文件数超限";
    default:
      return (e && e.message) || String(e);
  }
}

// ---------------------------------------------------------------------------
// P0-2 原子落盘
// ---------------------------------------------------------------------------

/**
 * 流式原子写：同目录临时文件 → fsync → rename。
 * 同目录保证同一文件系统，rename 才是原子的；跨设备时才走 copy 回退。
 * 读者任何时刻看到的都是"改之前"或"改之后"的完整文件，不会读到半截。
 */
async function atomicWriteStream(dest, readable) {
  const dir = path.dirname(dest);
  const stem = truncateName(path.basename(dest), MAX_NAME_BYTES - TEMP_NAME_RESERVE);
  const temp = path.join(dir, `.${stem}.${randomBytes(16).toString("hex")}.part`);

  // 先用普通 createWriteStream 写：它结束时会自己关 fd，pipeline 一定会 settle。
  // 不用 filehandle.createWriteStream({autoClose:false}) —— 那样流不会发 'close'，
  // stream/promises 的 pipeline 会永远等下去（实测 promise 既不 resolve 也不 reject）。
  await pipeline(readable, fs.createWriteStream(temp, {
    highWaterMark: 1024 * 1024,
    flags: "wx",
    mode: 0o600,
  }));

  // 数据落到内核缓冲区后显式 fsync，确保 rename 之前数据真的在盘上。
  // 单独再打开一次只为了 sync：fsync 之后再 rename，崩溃后不会拿到空文件或旧内容。
  const fh = await fsp.open(temp, "r+");
  try { await fh.sync(); } finally { await fh.close(); }

  try {
    await fsp.rename(temp, dest);
  } catch (e) {
    if (e.code !== "EXDEV") {
      try { await fsp.unlink(temp); } catch { /* ignore */ }
      throw e;
    }
    // 跨设备：临时文件与目标不在同一文件系统，rename 不可用。退化为 copy + unlink，
    // 此时无法保证原子性，但至少不会静默丢数据。
    await fsp.copyFile(temp, dest);
    await fsp.unlink(temp);
  }
  return dest;
}

module.exports = {
  MAX_NAME_BYTES,
  TEMP_NAME_RESERVE,
  graphemes,
  validateName,
  truncateName,
  casefoldKey,
  findNameConflict,
  detectSymlinkCycle,
  describeFsError,
  atomicWriteStream,
};
