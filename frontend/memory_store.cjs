/**
 * 记忆日历的数据层。
 *
 * 记忆存在哪：不是数据库，全在文件系统。
 *
 *   memory/segmented/sessions/YYYY-MM-DD/interactions/req-XXX/
 *     manifest.json  一次交互的元信息（起止时间、段数、各 kind 计数）
 *     s0001.json     一个记忆段
 *
 * 段有三种 kind：
 *   user_request    用户的一次提问
 *   assistant_reply 助手的一段回复（带 LLM 抽取的 keys）
 *   tool_operation  一次工具调用
 *
 * 为什么用分段记忆而不是 date-memory/：
 * date-memory/YYYY-MM-DD.md 来自"会话结束后跑一次 LLM 摘要"的老机制，
 * 那个开关（config.json 的 memory_summarize）现在是关的，最近不再产生新文件，
 * 所以拿它当日历数据源只能看到四个历史日期。分段记忆是实时写入的，
 * 33 天真实数据都在，日历打开就有东西看。
 *
 * 保留期作用在分段记忆上：到期把整天的分段目录移进 archive/。
 * 只动 segmented/sessions/，不碰 date-memory/、user.md、memory.md 和会话原始记录。
 */

const fs = require("fs");
const path = require("path");

const DATE_DIR_RE = /^(\d{4})-(\d{2})-(\d{2})$/;

/** 保留期可选值（月）。0 表示永久保留。 */
const RETENTION_OPTIONS = [0, 1, 3, 6, 12, 24];

const RETENTION_LABELS = {
  0: "永久保留",
  1: "1 个月",
  3: "3 个月",
  6: "6 个月",
  12: "1 年",
  24: "2 年",
};

function segmentedRoot(memoryRoot) {
  return path.join(memoryRoot, "segmented", "sessions");
}

function archiveRoot(memoryRoot) {
  return path.join(memoryRoot, "archive");
}

/**
 * 解析 YYYY-MM-DD 为本地日期分量。
 * 不用 new Date(string)：那会按 UTC 解析，东八区会整体偏一天。
 */
function parseDateKey(key) {
  const m = DATE_DIR_RE.exec(String(key || ""));
  if (!m) return null;
  const year = Number(m[1]);
  const month = Number(m[2]);
  const day = Number(m[3]);
  if (month < 1 || month > 12 || day < 1 || day > 31) return null;
  const probe = new Date(year, month - 1, day);
  if (probe.getFullYear() !== year || probe.getMonth() !== month - 1 || probe.getDate() !== day) {
    return null;
  }
  return { year, month, day };
}

function daysBetween(older, newer) {
  const a = new Date(older.year, older.month - 1, older.day);
  const b = new Date(newer.year, newer.month - 1, newer.day);
  return Math.round((b - a) / 86400000);
}

function shiftMonths(base, months) {
  const d = new Date(base.year, base.month - 1 + months, base.day);
  return { year: d.getFullYear(), month: d.getMonth() + 1, day: d.getDate() };
}

function todayParts(now) {
  const d = now || new Date();
  return { year: d.getFullYear(), month: d.getMonth() + 1, day: d.getDate() };
}

function formatKey(parts) {
  const mm = String(parts.month).padStart(2, "0");
  const dd = String(parts.day).padStart(2, "0");
  return `${parts.year}-${mm}-${dd}`;
}

function safeReadJson(file) {
  try {
    return JSON.parse(fs.readFileSync(file, "utf-8"));
  } catch {
    return null;
  }
}

/**
 * 列出所有有记忆的日期。
 * @returns {{date:string, year:number, month:number, day:number, interactions:number, segments:number}[]}
 */
function listDateMemory(memoryRoot) {
  const root = segmentedRoot(memoryRoot);
  let names = [];
  try {
    names = fs.readdirSync(root);
  } catch {
    return [];
  }
  const out = [];
  for (const name of names) {
    const parts = parseDateKey(name);
    if (!parts) continue;
    const interactionsDir = path.join(root, name, "interactions");
    let interactionDirs = [];
    try {
      interactionDirs = fs.readdirSync(interactionsDir);
    } catch {
      continue; // 没有 interactions 目录的日期不算有记忆
    }
    let interactions = 0;
    let segments = 0;
    for (const id of interactionDirs) {
      const dir = path.join(interactionsDir, id);
      let stat;
      try {
        stat = fs.statSync(dir);
      } catch {
        continue;
      }
      if (!stat.isDirectory()) continue;
      interactions += 1;
      const manifest = safeReadJson(path.join(dir, "manifest.json"));
      if (manifest && Number.isInteger(manifest.segment_count)) {
        segments += manifest.segment_count;
      } else {
        // 没有 manifest 时自己数一遍
        let segNames = [];
        try {
          segNames = fs.readdirSync(dir);
        } catch {
          segNames = [];
        }
        segments += segNames.filter((n) => /^s\d+\.json$/.test(n)).length;
      }
    }
    if (!interactions) continue;
    out.push({ date: name, ...parts, interactions, segments });
  }
  out.sort((a, b) => (a.date < b.date ? 1 : a.date > b.date ? -1 : 0));
  return out;
}

function truncate(text, max) {
  const s = String(text == null ? "" : text).trim();
  if (s.length <= max) return s;
  return `${s.slice(0, max)}…`;
}

/**
 * 洗掉注入的上下文标签。
 *
 * 用户的提问在写进记忆前会带上 <file_context>…</file_context> 之类的注入块，
 * 那是给模型看的，不是主人说的话，展示时要拿掉。
 */
function stripInjectedBlocks(text) {
  return String(text == null ? "" : text)
    .replace(/<file_context>[\s\S]*?<\/file_context>/gi, "")
    .replace(/<[^>]{1,40}>[\s\S]*?<\/\1>/gi, "")
    .trim();
}

/**
 * 读取某一天的交互列表。
 *
 * 每个交互给出：用户提问、助手回复要点、工具调用数、起止时间、KEY 关键词。
 * tool_operation 段不逐条展示 —— 一天几百个工具调用，列表会被淹没；
 * 只统计数量，回复要点才是人想看的东西。
 */
function readDateMemory(memoryRoot, date) {
  const parts = parseDateKey(String(date || ""));
  if (!parts) return null;
  const interactionsDir = path.join(segmentedRoot(memoryRoot), date, "interactions");
  let ids = [];
  try {
    ids = fs.readdirSync(interactionsDir);
  } catch {
    return null;
  }

  const interactions = [];
  for (const id of ids) {
    const dir = path.join(interactionsDir, id);
    let manifest = null;
    let segNames = [];
    try {
      manifest = safeReadJson(path.join(dir, "manifest.json"));
      segNames = fs.readdirSync(dir);
    } catch {
      continue;
    }
    if (!statIsDir(dir)) continue;

    const userRequests = [];
    const replies = [];
    let toolCount = 0;
    const keys = [];

    for (const name of segNames) {
      if (!/^s\d+\.json$/.test(name)) continue;
      const seg = safeReadJson(path.join(dir, name));
      if (!seg) continue;
      const kind = String(seg.kind || "");
      if (kind === "user_request") {
        const clean = stripInjectedBlocks(seg.content);
        if (clean) userRequests.push(truncate(clean, 300));
      } else if (kind === "assistant_reply") {
        const clean = stripInjectedBlocks(seg.content);
        if (clean) replies.push(truncate(clean, 400));
        for (const k of seg.keys || []) {
          const text = String((k && k.key) || "").trim();
          if (!text) continue;
          // KEY 抽取器有时会把整句助手回复当成关键词，还会重复登记。
          // 只留下够短、够像"词"的那���，其余当噪声丢掉。
          if (text.length > 40) continue;
          keys.push(text);
        }
      } else if (kind === "tool_operation") {
        toolCount += 1;
      }
    }

    if (!userRequests.length && !replies.length) continue;

    interactions.push({
      id,
      started_at: manifest ? Number(manifest.started_at) || 0 : 0,
      ended_at: manifest ? Number(manifest.ended_at) || 0 : 0,
      status: manifest ? String(manifest.status || "") : "",
      user_requests: userRequests,
      replies: replies.slice(0, 5),
      reply_count: replies.length,
      tool_count: toolCount,
      keys: Array.from(new Set(keys)).slice(0, 20),
    });
  }

  interactions.sort((a, b) => a.started_at - b.started_at);
  if (!interactions.length) {
    return { date, ...parts, interactions: [], total_segments: 0, total_keys: 0 };
  }
  return {
    date,
    ...parts,
    interactions,
    total_segments: interactions.reduce((n, i) => n + i.reply_count + i.tool_count + i.user_requests.length, 0),
    total_keys: interactions.reduce((n, i) => n + i.keys.length, 0),
  };
}

function statIsDir(p) {
  try {
    return fs.statSync(p).isDirectory();
  } catch {
    return false;
  }
}

/**
 * 把超出保留期的整天分段记忆移入 archive/。
 *
 * 移的是 segmented/sessions/YYYY-MM-DD 整个目录；同名的 date-memory/YYYY-MM-DD.md
 * 一并移走，免得日历上出现"分段已归档、摘要还在"的半吊子状态。
 * date-memory 的归属由调用方保证，这里不越界去动别的目录。
 *
 * @returns {{archived: string[], kept: number, cutoff: string|null}}
 */
function archiveExpiredDateMemory(memoryRoot, retentionMonths, now) {
  const kept = [];
  const archived = [];
  if (!Number.isFinite(retentionMonths) || retentionMonths <= 0) {
    return { archived, kept: listDateMemory(memoryRoot).length, cutoff: null };
  }
  const today = todayParts(now);
  const cutoff = shiftMonths(today, -retentionMonths);
  const root = segmentedRoot(memoryRoot);
  const toArchive = archiveRoot(memoryRoot);

  let names = [];
  try {
    names = fs.readdirSync(root);
  } catch {
    return { archived, kept: 0, cutoff: formatKey(cutoff) };
  }
  for (const name of names) {
    const parts = parseDateKey(name);
    if (!parts) continue;
    // 保留期之内（含边界当天）不动
    if (daysBetween(parts, cutoff) <= 0) {
      kept.push(name);
      continue;
    }
    try {
      fs.mkdirSync(toArchive, { recursive: true });
      const target = path.join(toArchive, name);
      if (fs.existsSync(target)) {
        const stamp = new Date().toISOString().replace(/[:.]/g, "-");
        fs.renameSync(path.join(root, name), path.join(toArchive, `${name}.${stamp}`));
      } else {
        fs.renameSync(path.join(root, name), target);
      }
      archived.push(name);
    } catch {
      kept.push(name);
    }
  }
  return { archived, kept: kept.length, cutoff: formatKey(cutoff) };
}

/**
 * 统计归档区。
 */
function archiveStats(memoryRoot) {
  const dir = archiveRoot(memoryRoot);
  let names = [];
  try {
    names = fs.readdirSync(dir);
  } catch {
    return { count: 0, bytes: 0, files: [] };
  }
  const files = [];
  let bytes = 0;
  for (const name of names) {
    let size = 0;
    try {
      const st = fs.statSync(path.join(dir, name));
      size = st.isDirectory() ? dirSize(path.join(dir, name)) : st.size;
    } catch {
      /* ignore */
    }
    bytes += size;
    files.push({ name, bytes: size });
  }
  files.sort((a, b) => (a.name < b.name ? 1 : a.name > b.name ? -1 : 0));
  return { count: files.length, bytes, files };
}

function dirSize(dir) {
  let total = 0;
  let entries = [];
  try {
    entries = fs.readdirSync(dir, { withFileTypes: true });
  } catch {
    return 0;
  }
  for (const e of entries) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) total += dirSize(p);
    else {
      try {
        total += fs.statSync(p).size;
      } catch {
        /* ignore */
      }
    }
  }
  return total;
}

module.exports = {
  RETENTION_OPTIONS,
  RETENTION_LABELS,
  parseDateKey,
  formatKey,
  todayParts,
  daysBetween,
  listDateMemory,
  readDateMemory,
  archiveExpiredDateMemory,
  archiveStats,
};
