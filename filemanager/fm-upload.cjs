"use strict";
/**
 * P1-5 分片上传 + 断点续传 / P1-6 完整性校验。
 *
 * 【三段式（Nextcloud 模型）】建目标 → 逐片 PUT → MOVE 提交。
 * 关键在于**只有 commit 才产出目标文件**：分片只写进暂存区，中断时目标位置
 * 根本不存在，所以「未完成的分片」不需要任何事务表去对账，过期直接按垃圾回收。
 *
 * 【为什么暂存区就是一个普通文件 + 一个 received 游标】分片按 offset 直接写进
 * 暂存文件的对应位置（writeSync 带 position），于是「续传」退化成「从 received
 * 继续写」，天然没有重复字节的问题——这是本实现最重要的一个简化。
 *
 * 【CRC32C 不是 zlib.crc32】Node 的 zlib.crc32 是 CRC-32/ISO-HDLC（多项式
 * 0xEDB88320），和 spec 要的 CRC32C/Castagnoli（0x82F63B78）**是两种算法**，
 * 互不兼容。这里自己按 Castagnoli 建表，所以是软件实现——离线装不了
 * fast-crc32c 之类的硬件加速库。算法本身算得对（见 upload.test.cjs 的标准
 * 测试向量），性能只是比硬件慢，不影响正确性。
 */

const fs = require("fs");
const path = require("path");
const os = require("os");
const crypto = require("crypto");

// ---------------------------------------------------------------------------
// CRC32C (Castagnoli)
// ---------------------------------------------------------------------------
const CRC32C_POLY = 0x82f63b78; // 反射多项式
const CRC32C_TABLE = (() => {
  const t = new Int32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) c = (c & 1) ? (c >>> 1) ^ CRC32C_POLY : (c >>> 1);
    t[n] = c;
  }
  return t;
})();

// seed 语义对齐 zlib.crc32：传上一次的 CRC **结果值**（不是内部状态），
// 返回新的结果值。内部状态是 ~seed，所以链式分块计算天然正确。
function crc32c(buf, seed) {
  let c = ~(seed >>> 0);
  for (let i = 0; i < buf.length; i++) {
    c = CRC32C_TABLE[(c ^ buf[i]) & 0xff] ^ (c >>> 8);
  }
  return (~c) >>> 0;
}
function crc32cHex(buf) { return (crc32c(buf) >>> 0).toString(16).padStart(8, "0"); }

function sha256Hex(buf) { return crypto.createHash("sha256").update(buf).digest("hex"); }

// ---------------------------------------------------------------------------
// 暂存区
// ---------------------------------------------------------------------------
function stagingRoot(dataDir) {
  const r = path.join(dataDir, "uploads");
  fs.mkdirSync(r, { recursive: true });
  return r;
}

// 会话只活在内存里：目标文件在 commit 前根本不存在，进程重启等于所有未完成
// 上传作废，暂存区残留由 sweepOrphans() 扫掉。不需要事务表，也就不会有
// 「事务表和实际文件对不上」这类问题。
const sessions = new Map();
const SESSION_TTL_MS = Number(process.env.FM_UPLOAD_TTL_MS) || 24 * 3600_000;
let sweeper = null;

function ensureSweeper() {
  if (sweeper) return;
  sweeper = setInterval(() => {
    const now = Date.now();
    for (const [id, s] of sessions) {
      if (s.expiresAt <= now) { try { fs.unlinkSync(s.staging); } catch (_) { /* 已不在 */ } sessions.delete(id); }
    }
  }, Math.max(1000, Math.floor(SESSION_TTL_MS / 12)));
  if (sweeper.unref) sweeper.unref();
}

/** 清掉所有已不在会话表里的暂存文件（进程重启后的残留）。 */
function sweepOrphans(dataDir) {
  const root = stagingRoot(dataDir);
  const known = new Set(Array.from(sessions.values()).map((s) => s.staging));
  let n = 0;
  for (const f of fs.readdirSync(root)) {
    const p = path.join(root, f);
    if (!known.has(p)) { try { fs.unlinkSync(p); n++; } catch (_) { /* 忽略 */ } }
  }
  return n;
}

function chunkRanges(session) {
  return session.ranges.map((r) => ({ start: r.start, end: r.end, crc32c: r.crc32c }));
}

function initSession({ uploadId, target, size, chunkSize, dataDir }) {
  ensureSweeper();
  const id = uploadId || crypto.randomUUID();
  if (sessions.has(id)) { const e = new Error("uploadId 已存在"); e.code = "EXISTS"; throw e; }
  const staging = path.join(stagingRoot(dataDir), id + ".part");
  // 先把暂存文件预分配到最终大小：这样 writeSync(position) 不会在中间挖洞，
  // commit 时也不用担心稀疏文件导致 size 与实际不符。
  const fd = fs.openSync(staging, "w");
  try { fs.ftruncateSync(fd, size); } finally { fs.closeSync(fd); }
  const s = {
    id, target, staging, size: Number(size) || 0,
    chunkSize: Number(chunkSize) || 0, received: 0, ranges: [],
    createdAt: Date.now(), expiresAt: Date.now() + SESSION_TTL_MS,
  };
  sessions.set(id, s);
  return s;
}

function getSession(id) {
  ensureSweeper();
  const s = sessions.get(id);
  if (!s) { const e = new Error("上传会话不存在或已过期"); e.code = "GONE"; throw e; }
  if (s.expiresAt <= Date.now()) { sessions.delete(id); throw Object.assign(new Error("上传会话已过期"), { code: "GONE" }); }
  return s;
}

function abortSession(id) {
  const s = sessions.get(id);
  if (s) { try { fs.unlinkSync(s.staging); } catch (_) { /* 已不在 */ } sessions.delete(id); }
  return true;
}

/**
 * 写一个分片。
 * spec 有两条硬要求，这里逐条对应：
 *  1) offset 与服务端 received 不一致 → 409，且**不写任何字节**。客户端必须先
 *     HEAD 重新对齐；服务端绝不能「猜」着收，否则重复字节会静默写进文件。
 *  2) CRC32C 不符 → 整片丢弃并把暂存文件截回原 received，逼客户端重传，
 *     绝不能「警告后继续」。
 */
function putPart(session, offset, buf, declaredCrc) {
  if (offset !== session.received) {
    const e = new Error(`offset 不匹配：服务端已收到 ${session.received} 字节，客户端发的是 ${offset}`);
    e.code = "OFFSET"; e.expected = session.received;
    throw e;
  }
  const end = offset + buf.length;
  if (end > session.size) {
    const e = new Error(`分片超出声明的总大小（${end} > ${session.size}）`);
    e.code = "TOOBIG"; throw e;
  }
  const declared = declaredCrc ? String(declaredCrc).toLowerCase().replace(/^0x/, "") : null;

  const fd = fs.openSync(session.staging, "r+");
  try {
    fs.writeSync(fd, buf, 0, buf.length, offset);
  } finally { fs.closeSync(fd); }

  // 校验放在落盘之后，因为整片回滚只要 truncate 一下；但 offset 校验必须在
  // 落盘之前——写错了没法悄悄收回。
  if (declared && crc32cHex(buf) !== declared) {
    const fd2 = fs.openSync(session.staging, "r+");
    try { fs.ftruncateSync(fd2, offset); } finally { fs.closeSync(fd2); }
    const e = new Error(`CRC32C 校验失败：本片已丢弃，需重传（声明 ${declared}，实际 ${crc32cHex(buf)}）`);
    e.code = "CRC"; e.expected = crc32cHex(buf);
    throw e;
  }
  session.received = end;
  session.ranges.push({ start: offset, end, crc32c: crc32cHex(buf) });
  return { received: session.received, ranges: session.ranges.length };
}

function remaining(session) { return Math.max(0, session.size - session.received); }

/** 提交：校验总长 + 算 SHA-256，然后把暂存文件搬到目标位置。 */
function commitSession(session) {
  // 【坑】不能拿 fs.statSync(staging).size 当「收全了」的判据：init 时为了避免
  // 稀疏空洞已经 ftruncate 到最终大小，所以暂存文件**永远**是 size 字节长，
  // 哪怕一段都没传。早期版本就踩了这个——没传完也 commit 成功，产出的是
  // 「前面真实数据 + 后面全 0」的文件，而且长度检查完全通过，静默损坏。
  // 唯一的真判据是 received（客户端实际发过的字节数）。
  if (session.received !== session.size) {
    const e = new Error(`未传完：已收 ${session.received} 字节，声明 ${session.size} 字节`);
    e.code = "SIZE"; e.received = session.received; e.size = session.size;
    throw e;
  }
  // SHA-256 分块算，别为了算校验把 2GB 读进内存——那就等于把 P1-5 要治的
  // 毛病又犯一遍。
  const hash = crypto.createHash("sha256");
  const fd = fs.openSync(session.staging, "r");
  const buf = Buffer.allocUnsafe(1 << 20);
  let n, crcSeed = 0;
  try {
    while ((n = fs.readSync(fd, buf, 0, buf.length, null)) > 0) {
      hash.update(buf.subarray(0, n));
      crcSeed = crc32c(buf.subarray(0, n), crcSeed);
    }
  } finally { fs.closeSync(fd); }

  fs.mkdirSync(path.dirname(session.target), { recursive: true });
  fs.renameSync(session.staging, session.target);
  sessions.delete(session.id);
  return {
    path: session.target, size: session.size,
    sha256: hash.digest("hex"),
    crc32c: (crcSeed >>> 0).toString(16).padStart(8, "0"),
  };
}

module.exports = {
  crc32c, crc32cHex, sha256Hex, stagingRoot, sweepOrphans,
  initSession, getSession, putPart, commitSession, abortSession,
  remaining, chunkRanges, sessions, SESSION_TTL_MS,
};
