"use strict";
/**
 * P1-2 回收站。布局照 freedesktop.org Trash 规范（files/ + info/*.trashinfo）。
 *
 * 【一处刻意的偏离】规范给的是 $XDG_DATA_HOME/Trash，但那是**整个桌面共用**的
 * 真实回收站。本模块要是落在那里，「清空回收站」就会把主人从文件管理器之外
 * 删掉的东西一并抹掉——而且不可撤销。所以这里只借它的**布局**，根目录放在
 * Fairy 私有位置（与 fm-meta 的星标同源），永远不碰桌面那份。
 * 代价：它不是一个真正的「系统回收站」，不参与桌面 trash:// 协议。
 *
 * 另一个易错点：还原时**绝不能覆盖**同名文件。覆盖掉的话，
 * 「用旧版覆盖后就再也回不到中间状态」，正是 P4-2 版本历史反复强调的坑。
 */

const fs = require("fs");
const path = require("path");
const os = require("os");

function dataDir() {
  const base = process.env.XDG_DATA_HOME || path.join(os.homedir(), ".local", "share");
  return path.join(base, "Fairy", "filemanager");
}
function trashRoot() { return path.join(dataDir(), "trash"); }
function filesDir() { return path.join(trashRoot(), "files"); }
function infoDir() { return path.join(trashRoot(), "info"); }

function ensureDirs() {
  fs.mkdirSync(filesDir(), { recursive: true });
  fs.mkdirSync(infoDir(), { recursive: true });
}

// freedesktop: DeletionDate 是本地时间且不带时区
function stamp(d) {
  const p = (n) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

function infoFileFor(id) { return path.join(infoDir(), id + ".trashinfo"); }

// 跨文件系统 rename 会 EXDEV（回收站在数据盘、被删文件在别的挂载点时很常见）。
// 直接 renameSync 会抛，写在这里免得每个调用点各写一遍。
function moveAcross(src, dest) {
  try {
    fs.renameSync(src, dest);
    return;
  } catch (e) {
    if (e.code !== "EXDEV") throw e;
  }
  fs.cpSync(src, dest, { recursive: true, preserveTimestamps: true });
  fs.rmSync(src, { recursive: true, force: true });
}

// 回收站内部也要防重名：同名文件删两次必须能各占一格，否则第二次会覆盖第一次，
// 于是「删 A → 删 A → 还原」拿到的是后一份，文件内容被悄悄换掉。
function uniqueId(base) {
  const ext = path.extname(base);
  const stem = base.slice(0, base.length - ext.length);
  let id = base;
  for (let i = 2; i < 10000; i++) {
    if (!fs.existsSync(path.join(filesDir(), id)) && !fs.existsSync(infoFileFor(id))) return id;
    id = `${stem}.${i}${ext}`;
  }
  return `${stem}.${Date.now()}${ext}`;
}

function readInfo(id) {
  try {
    const raw = fs.readFileSync(infoFileFor(id), "utf8");
    let p = null, d = null;
    for (const line of raw.split(/\r?\n/)) {
      const i = line.indexOf("=");
      if (i < 0) continue;
      const k = line.slice(0, i).trim().toLowerCase();
      const v = line.slice(i + 1);
      if (k === "path") p = v;
      else if (k === "deletiondate") d = v;
    }
    // 规范要求 Path 是 URL 编码的
    return { originalPath: p ? decodeURIComponent(p) : null, deletionDate: d || null };
  } catch { return { originalPath: null, deletionDate: null }; }
}

function writeInfo(id, originalPath, date) {
  fs.writeFileSync(infoFileFor(id),
    "[Trash Info]\nPath=" + encodeURIComponent(originalPath) + "\nDeletionDate=" + date + "\n",
    "utf8");
}

/** 把 abs 移入回收站，返回该条目。调用方负责把错误往上抛。 */
function trashPath(abs) {
  ensureDirs();
  const id = uniqueId(path.basename(abs));
  // 先写 info 再搬：反过来的话，搬成功但写 info 失败会留下一个查无出处的孤儿文件。
  writeInfo(id, abs, stamp(new Date()));
  try {
    moveAcross(abs, path.join(filesDir(), id));
  } catch (e) {
    try { fs.rmSync(infoFileFor(id), { force: true }); } catch { /* 尽力清理 */ }
    throw e;
  }
  return { id, name: path.basename(abs), originalPath: abs, deletionDate: stamp(new Date()) };
}

function listTrash() {
  let ids = [];
  try { ids = fs.readdirSync(filesDir()); } catch { return []; }
  const out = [];
  for (const id of ids) {
    const p = path.join(filesDir(), id);
    let st;
    try { st = fs.lstatSync(p); } catch { continue; }
    const info = readInfo(id);
    out.push({
      id,
      name: info.originalPath ? path.basename(info.originalPath) : id,
      originalPath: info.originalPath,
      deletionDate: info.deletionDate,
      size: st.isDirectory() ? null : st.size,
      isDirectory: st.isDirectory(),
    });
  }
  out.sort((a, b) => String(b.deletionDate || "").localeCompare(String(a.deletionDate || "")));
  return out;
}

function isKnownId(id) {
  if (typeof id !== "string" || !id || id.includes("/") || id.includes("\\") || id.includes("..")) return false;
  return fs.existsSync(path.join(filesDir(), id));
}

/**
 * 还原。目标已存在时**明确失败**，不覆盖、不静默改名——
 * 静默改名会让用户以为还原了原文件，实际拿到的是「报告 (1).txt」。
 */
function restoreEntry(id) {
  if (!isKnownId(id)) { const e = new Error("回收站里没有这一项"); e.fmCode = 404; throw e; }
  const info = readInfo(id);
  if (!info.originalPath) { const e = new Error("该条目的原始路径已丢失，无法还原"); e.fmCode = 409; throw e; }
  const dest = info.originalPath;
  if (fs.existsSync(dest)) { const e = new Error("原路径已被占用，已取消还原以免覆盖"); e.fmCode = 409; throw e; }
  fs.mkdirSync(path.dirname(dest), { recursive: true });
  moveAcross(path.join(filesDir(), id), dest);
  try { fs.rmSync(infoFileFor(id), { force: true }); } catch { /* 已移走，清理失败不影响还原 */ }
  return { id, restoredTo: dest };
}

function purgeEntry(id) {
  if (!isKnownId(id)) { const e = new Error("回收站里没有这一项"); e.fmCode = 404; throw e; }
  fs.rmSync(path.join(filesDir(), id), { recursive: true, force: true });
  try { fs.rmSync(infoFileFor(id), { force: true }); } catch { /* 同上 */ }
  return { id };
}

function emptyTrash() {
  const n = listTrash().length;
  fs.rmSync(filesDir(), { recursive: true, force: true });
  fs.rmSync(infoDir(), { recursive: true, force: true });
  ensureDirs();
  return { removed: n };
}

module.exports = {
  dataDir, trashRoot, filesDir, infoDir,
  trashPath, listTrash, restoreEntry, purgeEntry, emptyTrash, isKnownId,
};
