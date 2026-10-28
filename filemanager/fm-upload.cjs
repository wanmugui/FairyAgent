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

/**
 * 清掉 dataDir 暂存目录里的残留文件。
 * 【注意】暂存位置已重构为「目标目录内」，新产生的 .part 不再落在 dataDir，
 * 所以这个函数现在只负责清理重构之前的历史遗留；新产生的孤儿由
 * sweepStaleInDir() 就在目标目录就地回收。
 */
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

/**
 * 就地回收目标目录里过期的上传 .part 暂存。
 * 暂存建在目标目录后，进程崩溃/重启会让这些文件脱离会话表而无人认领；
 * sweepOrphans 扫不到（它们不在 dataDir 下），所以在每次 init 时顺手清一遍。
 * 只看 .<名字>.<uuid>.part 这种本模块生成的命名，不碰用户的其他隐藏文件。
 */
function sweepStaleInDir(dir) {
  const ttl = SESSION_TTL_MS;
  const now = Date.now();
  const keep = new Set(Array.from(sessions.values()).map((s) => s.staging));
  let n = 0;
  let names;
  try { names = fs.readdirSync(dir); } catch (_) { return 0; }
  for (const name of names) {
    if (!/^\..+\.[0-9a-f-]{8,}\.part$/.test(name)) continue;
    const p = path.join(dir, name);
    if (keep.has(p)) continue;
    try {
      if (now - fs.statSync(p).mtimeMs < ttl) continue;   // 可能还有活跃会话在写
      fs.unlinkSync(p); n++;
    } catch (_) { /* 已不在 */ }
  }
  return n;
}

function chunkRanges(session) {
  return session.ranges.map((r) => ({ start: r.start, end: r.end, crc32c: r.crc32c }));
}

function initSession({ uploadId, target, size, chunkSize, dataDir, parallel }) {
  ensureSweeper();
  const id = uploadId || crypto.randomUUID();
  if (sessions.has(id)) { const e = new Error("uploadId 已存在"); e.code = "EXISTS"; throw e; }
  // 【性能重构】暂存文件直接建在「目标目录内」，不再放 dataDir 下。
  // 原因：dataDir 与用户实际浏览的目标经常不在同一挂载点（如 dataDir 在 /
  // 而目标在 /dev/sda1），跨设备 rename 会抛 EXDEV；即使兜底成 copy，
  // 也等于同一份数据在磁盘上多走一趟。放进目标目录后 commit 的 rename
  // 是同设备操作，原子且零拷贝。
  // 命名用 .<原名>.<uploadId>.part：点开头，目录列表默认不显示，不会被误当正常文件。
  const targetDir = path.dirname(target);
  fs.mkdirSync(targetDir, { recursive: true });
  sweepStaleInDir(targetDir);   // 顺手回收本目录里上次崩溃留下的过期分片
  const staging = path.join(targetDir, "." + path.basename(target) + "." + id + ".part");
  // 预分配到最终大小：writeSync(position) 不会在中间挖洞，
  // commit 时也不用担心稀疏文件导致 size 与实际不符。
  const fd = fs.openSync(staging, "w");
  try { fs.ftruncateSync(fd, size); } finally { fs.closeSync(fd); }
  // 【增量哈希】字节在 putPart 到达时就喂进来，commit 不再把文件读回来重算。
  // 原实现里 commit 要把整个暂存文件重新读一遍算 sha256+crc32c，
  // 5MB 文件实测 514ms，与传输本身一样慢，纯属浪费。
  //
  // 但增量哈希的前提是**分片按 offset 顺序到达**。并发上传时第 3 片可能先于
  // 第 1 片落地，这时喂进去就等于把乱序字节当成顺序字节，哈希必错且无法察觉。
  // 所以并行会话不在这里建哈希，改为 commit 阶段从暂存文件顺序读一遍重算：
  // 代价是 commit 多一次顺序读（本地盘几百 MB/s，3.5GB 约十几秒），
  // 换来的是正确性。串行会话保持增量路径，零额外成本。
  const isParallel = !!parallel;
  const sha = isParallel ? null : crypto.createHash("sha256");
  const s = {
    id, target, staging, size: Number(size) || 0,
    chunkSize: Number(chunkSize) || 0, received: 0, ranges: [],
    parallel: isParallel,
    covered: 0,        // 去重后的已落盘字节数，并行模式用它判完成
    sha, crcSeed: 0,   // crcSeed: 增量 CRC 状态，跨分片延续
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
  const end = offset + buf.length;
  if (end > session.size) {
    const e = new Error(`分片超出声明的总大小（${end} > ${session.size}）`);
    e.code = "TOOBIG"; throw e;
  }
  // 串行模式：offset 必须紧接已收前缀，错了就在落盘前拒掉。
  // 并行模式：offset 是任意的已声明位置，完整性交给 CRC + 覆盖度校验。
  if (!session.parallel && offset !== session.received) {
    const e = new Error(`offset 不匹配：服务端已收到 ${session.received} 字节，客户端发的是 ${offset}`);
    e.code = "OFFSET"; e.expected = session.received;
    throw e;
  }
  const declared = declaredCrc ? String(declaredCrc).toLowerCase().replace(/^0x/, "") : null;
  const actualCrc = crc32cHex(buf);

  const fd = fs.openSync(session.staging, "r+");
  try {
    fs.writeSync(fd, buf, 0, buf.length, offset);
  } finally { fs.closeSync(fd); }

  // 校验放在落盘之后，因为整片回滚只要 truncate 一下；但 offset 校验必须在
  // 落盘之前——写错了没法悄悄收回。
  if (declared && actualCrc !== declared) {
    // 串行模式下 truncate 回 offset 是安全的：此刻高 offset 上没有任何有效数据。
    // 并行模式下绝对不能 truncate——那里 offset 之后可能已经躺着别的并发分片，
    // 一刀切下去会把别人的数据抹掉。所以并行模式只把这片判为“未收到”：
    // 已写入的坏字节留在原地，重传时同 offset 会被完整覆盖，且因为没记进
    // ranges，覆盖度不足会让 commit 被拒，不会静默产出坏文件。
    if (!session.parallel) {
      const fd2 = fs.openSync(session.staging, "r+");
      try { fs.ftruncateSync(fd2, offset); } finally { fs.closeSync(fd2); }
    }
    const e = new Error(`CRC32C 校验失败：本片已丢弃，需重传（声明 ${declared}，实际 ${actualCrc}）`);
    e.code = "CRC"; e.expected = actualCrc;
    throw e;
  }
  // 【增量哈希】必须放在 CRC 校验「之后」：上面坏片会判失败并抛错，
  // 走不到这里，所以哈希状态不会被污染。放在之前的话，哈希已经吃进坏数据且
  // 无法回退（crypto.Hash 没有 rewind），重传后结果就永远错了。
  // 并行会话没有 sha（乱序不能增量喂），哈希在 commit 阶段顺序重算。
  if (!session.parallel) {
    session.sha.update(buf);
    session.crcSeed = crc32c(buf, session.crcSeed);   // crc32c 本身支持 seed，天然可增量
  }
  addRange(session, offset, end, actualCrc);
  return { received: session.received, ranges: session.ranges.length, covered: session.covered };
}

/**
 * 记一片已确认的区间，并维护「已覆盖字节数」。
 * 重复重传同一片是常态（网络抖动后重试），直接 push 会让 covered 重复计数，
 * 于是出现 covered > size 的假完成。所以这里先按区间去重再累计。
 */
function addRange(session, start, end, crc) {
  const hit = session.ranges.find(r => r.start === start && r.end === end);
  if (hit) { hit.crc32c = crc; return; }   // 重传同一片，覆盖即可
  session.ranges.push({ start, end, crc32c: crc });
  session.ranges.sort((a, b) => a.start - b.start);
  // 合并相邻/重叠区间，重算 covered。
  // 注意：合并分支必须把 cur 扩展出来的那一段也算进去，只算「新区间的起点」会把
  // 乱序到达的高 offset 分片漏出统计——6 片全传完却报只收到 1 片就是这么来的。
  let covered = 0, curStart = -1, curEnd = -1;
  for (const r of session.ranges) {
    if (curStart < 0) { curStart = r.start; curEnd = r.end; }
    else if (r.start <= curEnd) { curEnd = Math.max(curEnd, r.end); }
    else { covered += curEnd - curStart; curStart = r.start; curEnd = r.end; }
  }
  if (curStart >= 0) covered += curEnd - curStart;
  session.covered = covered;
  // received 维持「连续前缀」语义：并行模式下取连续覆盖到的位置
  let prefix = 0;
  for (const r of session.ranges) {
    if (r.start <= prefix) prefix = Math.max(prefix, r.end); else break;
  }
  if (!session.parallel) session.received = Math.max(session.received, end);
  else session.received = prefix;
}

function remaining(session) { return Math.max(0, session.size - session.received); }

/** 提交：校验总长 + 算 SHA-256，然后把暂存文件搬到目标位置。 */
function commitSession(session) {
  // 【坑】不能拿 fs.statSync(staging).size 当「收全了」的判据：init 时为了避免
  // 稀疏空洞已经 ftruncate 到最终大小，所以暂存文件**永远**是 size 字节长，
  // 哪怕一段都没传。早期版本就踩了这个——没传完也 commit 成功，产出的是
  // 「前面真实数据 + 后面全 0」的文件，而且长度检查完全通过，静默损坏。
  // 唯一的真判据是「已通过 CRC 校验的字节数」。
  // 串行会话用 received（= 连续前缀）；并行会话用 covered（= 去重合并后的区间总长）。
  // 两者在正常传完时都等于 size，区别只在乱序到达的过程中。
  const got = session.parallel ? session.covered : session.received;
  if (got !== session.size) {
    const e = new Error(`未传完：已收 ${got} 字节，声明 ${session.size} 字节`);
    e.code = "SIZE"; e.received = got; e.size = session.size;
    throw e;
  }
  // 暂存文件尺寸是独立的第二道防线，挡住 ftruncate 预分配后写入异常等边界情况。
  // 只看尺寸会被预分配出来的空洞骗过去——少传一片，尺寸照样是满的。
  const staged = fs.statSync(session.staging);
  if (staged.size !== session.size) {
    sessions.delete(session.id);
    const e = new Error("暂存文件尺寸与声明不符");
    e.code = "SIZE"; e.received = staged.size; e.size = session.size;
    throw e;
  }
  // 串行会话直接用增量结果，零回读成本。
  // 并行会话的字节是乱序到达的，增量哈希在这里不可用，必须顺序重算一遍：
  // 分片大小有界，顺序读一遍本地盘是带宽级开销，换正确性很划算。
  let sha256, crc32cVal;
  if (!session.parallel) {
    sha256 = session.sha.digest("hex");
    crc32cVal = (session.crcSeed >>> 0).toString(16).padStart(8, "0");
  } else {
    const h = crypto.createHash("sha256");
    let seed = 0;
    const rfd = fs.openSync(session.staging, "r");
    try {
      const buf = Buffer.allocUnsafe(1 << 20);
      let pos = 0;
      for (;;) {
        const n = fs.readSync(rfd, buf, 0, buf.length, pos);
        if (n <= 0) break;
        h.update(buf.subarray(0, n));
        seed = crc32c(buf.subarray(0, n), seed);
        pos += n;
      }
    } finally { fs.closeSync(rfd); }
    sha256 = h.digest("hex");
    crc32cVal = (seed >>> 0).toString(16).padStart(8, "0");
  }

  fs.mkdirSync(path.dirname(session.target), { recursive: true });
  // staging 现在就建在目标目录内，因此 rename 必然同设备，不会再抛 EXDEV。
  // 仍保留兜底：万一目标目录本身是个挂载点边界（bind mount 等），不至于整批失败。
  try {
    fs.renameSync(session.staging, session.target);
  } catch (err) {
    if (!err || err.code !== "EXDEV") throw err;
    fs.copyFileSync(session.staging, session.target);
    fs.unlinkSync(session.staging);
  }
  sessions.delete(session.id);
  return {
    path: session.target, size: session.size,
    sha256, crc32c: crc32cVal,
  };
}

module.exports = {
  crc32c, crc32cHex, sha256Hex, stagingRoot, sweepOrphans,
  initSession, getSession, putPart, commitSession, abortSession,
  remaining, chunkRanges, sessions, SESSION_TTL_MS,
};
