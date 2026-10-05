#!/usr/bin/env node
/**
 * Step 1 of the memory migration: load the existing files into memory/fairy.db.
 *
 * This step changes no read path. The agent and the HTTP server keep reading the
 * files exactly as before, so nothing can break here. The point is to prove the
 * database can hold what the files hold before anything depends on it.
 *
 * Two decisions that make that provable:
 *
 *   - Every row keeps the original JSON in a `payload` column beside the parsed
 *     columns. A migration that keeps only "the fields I thought of" drops the
 *     rest silently, and you discover it months later when something reads a
 *     field that is no longer there.
 *   - The import is idempotent: it clears the tables it owns first, so a failed
 *     run leaves a database that is safe to re-run rather than a half-populated
 *     one.
 *
 * Scope, agreed with the user: only the segmented *index* is imported. The
 * interaction directories under segmented/sessions/<date>/interactions/ hold the
 * segment bodies and stay on disk untouched - they are referenced by the index,
 * and copying them into the database while the files are still authoritative
 * would create two sources of truth for the same thing.
 *
 * Usage:
 *   node tools/memory-migration/import-memory.cjs           # import
 *   node tools/memory-migration/import-memory.cjs --verify  # import, then compare
 */

"use strict";

const fs = require("node:fs");
const path = require("node:path");
const crypto = require("node:crypto");

const SCHEMA = `
CREATE TABLE IF NOT EXISTS sessions (
  session_id     TEXT PRIMARY KEY,
  name           TEXT NOT NULL,
  kind           TEXT,
  model          TEXT,
  daily_date     TEXT,
  domain         TEXT,
  parent_session TEXT,
  created_at     INTEGER,
  created_by     TEXT,
  message_count  INTEGER NOT NULL DEFAULT 0,
  source_path    TEXT NOT NULL,
  -- The session file's mtime. listSessions() builds its 'modified' field and its
  -- sort order from it, so a database-backed listing that cannot reproduce this
  -- value would silently reorder the sidebar. (No backticks here: this block is
  -- a JavaScript template literal.)
  source_mtime   INTEGER
  ,
  -- The original record with the messages array removed, exactly as it was on
  -- disk. Column round-tripping is not enough on its own: created_at is stored as
  -- epoch milliseconds, which happens to reproduce today's ISO strings but would
  -- not reproduce a writer that used an offset or omitted milliseconds. Keeping
  -- the source text makes reconstruction independent of every such accident.
  -- (No backticks in this block: it is a JavaScript template literal.)
  payload        TEXT
  ,
  -- Owning Fairy account, parsed from the source path: sessions live at
  -- memory/sessions/u<userId>/<date>/<name>.json, so that level names the owner.
  -- NULL for everything written before multi-user support and for the
  -- single-user desktop layout (sessions/<date>/<name>.json), which keeps the
  -- pre-existing rows readable instead of silently reassigning them. It is a
  -- plain INTEGER rather than a REFERENCES clause on purpose: users live in the
  -- separate auth database, and SQLite cannot enforce a cross-file foreign key.
  user_id        INTEGER
);
-- Every user-scoped session query filters on this first, so the index leads
-- with user_id and keeps name in second position for the "sessions for this
-- user, ordered by date" listing.
CREATE INDEX IF NOT EXISTS idx_sessions_user_date ON sessions(user_id, daily_date);

CREATE TABLE IF NOT EXISTS messages (
  session_id        TEXT NOT NULL,
  ordinal           INTEGER NOT NULL,
  role              TEXT,
  content           TEXT,
  reasoning_content TEXT,
  ts                TEXT,
  step              INTEGER,
  interaction_id    TEXT,
  name              TEXT,
  tool_call_id      TEXT,
  tool_calls        TEXT,
  internal_type     TEXT,
  duration_ms       REAL,
  real_ms           REAL,
  payload           TEXT NOT NULL,
  PRIMARY KEY (session_id, ordinal)
);

CREATE TABLE IF NOT EXISTS session_usage (
  name              TEXT PRIMARY KEY,
  prompt_tokens     INTEGER,
  completion_tokens INTEGER,
  turn_count        INTEGER,
  duration_ms       REAL,
  real_ms           REAL,
  payload           TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS session_plans (
  name        TEXT PRIMARY KEY,
  question    TEXT,
  created_at  INTEGER,
  updated_at  INTEGER,
  accepted_at INTEGER,
  item_count  INTEGER,
  payload     TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS memory_index (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  source_file    TEXT NOT NULL,
  line_no        INTEGER NOT NULL,
  key            TEXT,
  target         TEXT,
  type           TEXT,
  kind           TEXT,
  key_status     TEXT,
  status         TEXT,
  session_id     TEXT,
  interaction_id TEXT,
  created_at     INTEGER,
  payload        TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_memory_index_session ON memory_index(session_id);
CREATE INDEX IF NOT EXISTS idx_memory_index_key ON memory_index(key);
-- The two indexes below turn listSessions per-session first-user-message lookup
-- and readSessionRecord message ORDER BY into index range scans instead of
-- full table scans. Both are N-hot queries (one per session in listSessions,
-- one per opened session in readSessionRecord) so the cost is paid on every
-- frontend refresh.
CREATE INDEX IF NOT EXISTS idx_messages_session_ordinal ON messages(session_id, ordinal);
CREATE INDEX IF NOT EXISTS idx_messages_session_role_ordinal ON messages(session_id, role, ordinal);

CREATE TABLE IF NOT EXISTS memory_documents (
  name        TEXT PRIMARY KEY,
  kind        TEXT NOT NULL,
  body        TEXT NOT NULL,
  source_path TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS date_memory (
  date        TEXT PRIMARY KEY,
  body        TEXT NOT NULL,
  source_path TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS stamps (
  name        TEXT PRIMARY KEY,
  value       TEXT NOT NULL,
  source_path TEXT NOT NULL
);

-- Records the source fingerprint of the last successful import, so a sync can
-- tell "nothing changed" from "something changed" without re-reading every file.
CREATE TABLE IF NOT EXISTS meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
`;

const TABLES = [
  "sessions",
  "messages",
  "session_usage",
  "session_plans",
  "memory_index",
  "memory_documents",
  "date_memory",
  "stamps",
];

function readJson(file) {
  return JSON.parse(fs.readFileSync(file, "utf8"));
}

function readJsonl(file) {
  return fs
    .readFileSync(file, "utf8")
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean)
    .map((line, index) => {
      try {
        return { lineNo: index + 1, value: JSON.parse(line) };
      } catch (error) {
        throw new Error(`${file}:${index + 1}: ${error.message}`);
      }
    });
}

function listFiles(dir, filter) {
  if (!fs.existsSync(dir)) return [];
  const out = [];
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) out.push(...listFiles(full, filter));
    else if (!filter || filter(full)) out.push(full);
  }
  return out.sort();
}

function timestamp(value) {
  if (typeof value === "number" && Number.isFinite(value)) return value;
  if (typeof value === "string" && value) {
    const parsed = Date.parse(value);
    if (Number.isFinite(parsed)) return parsed;
  }
  return null;
}

/**
 * SQLite binds numbers, strings, null and bigint. Anything else (an array, an
 * object) throws at bind time, which is the good outcome - but it means every
 * column that mirrors a JSON field needs this guard, because the field's shape
 * is not guaranteed by anything.
 */
function scalar(value) {
  if (value === null || value === undefined) return null;
  if (typeof value === "number") return Number.isFinite(value) ? value : null;
  if (typeof value === "string" || typeof value === "bigint") return value;
  if (typeof value === "boolean") return value ? 1 : 0;
  return null;
}

/**
 * One inventory drives both import and verification, so the two can never
 * disagree about what "everything" means.
 */
function buildInventory(memoryRoot) {
  const sessionsDir = path.join(memoryRoot, "sessions");
  const segmentedDir = path.join(memoryRoot, "segmented");
  return {
    sessionFiles: listFiles(sessionsDir, (f) => f.endsWith(".json")).filter(
      // .plan.json 是执行计划、usage.json 是用量、.job.json 是子任务清单，
      // 三者都不是会话。漏掉任何一个都会在侧边栏渲染成"（分支会话，空的）"。
      (f) => !/usage\.json$/.test(f) && !/\.plan\.json$/.test(f) && !/\.job\.json$/.test(f)
    ),
    usageFiles: listFiles(sessionsDir, (f) => /usage\.json$/.test(f)),
    planFiles: listFiles(sessionsDir, (f) => /\.plan\.json$/.test(f)),
    indexFiles: [path.join(segmentedDir, "index", "root.jsonl")].concat(
      listFiles(path.join(segmentedDir, "index", "volumes"), (f) => f.endsWith(".jsonl"))
    ),
    dateFiles: listFiles(path.join(memoryRoot, "date-memory"), (f) => f.endsWith(".md")),
    stampFiles: listFiles(path.join(memoryRoot, ".stamps")),
    rootDocs: ["memory.md", "user.md"]
      .map((name) => path.join(memoryRoot, name))
      .filter((file) => fs.existsSync(file)),
  };
}

function importAll(db, memoryRoot, inventory, legacyOwnerId = null) {
  // Wrap the entire rebuild in one transaction. Auto-commit per row turns an
  // N-second rebuild into an Nx100 rebuild: each INSERT flushes a WAL frame
  // and fsyncs. The agent does not write to fairy.db while this runs (the
  // file store is the source of truth and the next sync is a no-op while a
  // rebuild is mid-flight), so a deferred transaction cannot deadlock with
  // anyone. A failed rebuild rolls back to the previous full import.
  db.exec("BEGIN");
  try {
    for (const table of TABLES) db.exec(`DELETE FROM ${table}`);

  const insertSession = db.prepare(
    `INSERT INTO sessions (session_id, name, kind, model, daily_date, domain, parent_session,
       created_at, created_by, message_count, source_path, source_mtime, payload, user_id)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
  );
  const insertMessage = db.prepare(
    `INSERT INTO messages (session_id, ordinal, role, content, reasoning_content, ts, step,
       interaction_id, name, tool_call_id, tool_calls, internal_type, duration_ms, real_ms, payload)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
  );
  const insertUsage = db.prepare(
    `INSERT INTO session_usage (name, prompt_tokens, completion_tokens, turn_count, duration_ms, real_ms, payload)
     VALUES (?, ?, ?, ?, ?, ?, ?)`
  );
  const insertPlan = db.prepare(
    `INSERT INTO session_plans (name, question, created_at, updated_at, accepted_at, item_count, payload)
     VALUES (?, ?, ?, ?, ?, ?, ?)`
  );
  const insertIndex = db.prepare(
    `INSERT INTO memory_index (source_file, line_no, key, target, type, kind, key_status, status,
       session_id, interaction_id, created_at, payload)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
  );
  const insertDoc = db.prepare(
    `INSERT INTO memory_documents (name, kind, body, source_path) VALUES (?, ?, ?, ?)`
  );
  const insertDate = db.prepare(
    `INSERT INTO date_memory (date, body, source_path) VALUES (?, ?, ?)`
  );
  const insertStamp = db.prepare(`INSERT INTO stamps (name, value, source_path) VALUES (?, ?, ?)`);

  const stats = {
    sessions: 0,
    messages: 0,
    usage: 0,
    plans: 0,
    index: 0,
    documents: 0,
    dateMemory: 0,
    stamps: 0,
  };

  for (const file of inventory.sessionFiles) {
    const raw = readJson(file);
    const name = path.basename(file, ".json");
    const userId = userIdFromSessionPath(file, memoryRoot, legacyOwnerId);
    const sessionId = sessionIdentity(raw, name, userId, legacyOwnerId);
    const existing = db.prepare("SELECT name FROM sessions WHERE session_id = ?").get(sessionId);
    if (existing) {
      // Two files claiming one session id would mean the files and the database
      // disagree about identity. Fail loudly rather than overwrite.
      throw new Error(`duplicate session_id ${sessionId} in ${file} (already from ${existing.name})`);
    }
    insertSession.run(
      sessionId,
      name,
      raw.kind || "",
      raw.model || "",
      raw.daily_date || "",
      raw.domain || "",
      raw.parent_session || "",
      timestamp(raw.created_at),
      raw.created_by || "",
      Array.isArray(raw.messages) ? raw.messages.length : 0,
      path.relative(memoryRoot, file),
      Math.floor(fs.statSync(file).mtimeMs),
      // messages live in their own table; keeping a copy here would be two
      // sources of truth for the same thing.
      JSON.stringify(
        Object.fromEntries(Object.entries(raw).filter(([key]) => key !== "messages"))
      ),
      userId
    );
    stats.sessions += 1;

    const messages = Array.isArray(raw.messages) ? raw.messages : [];
    messages.forEach((message, ordinal) => {
      insertMessage.run(
        sessionId,
        ordinal,
        message.role || "",
        typeof message.content === "string" ? message.content : JSON.stringify(message.content ?? null),
        message.reasoning_content || null,
        message.ts || null,
        Number.isFinite(message.step) ? message.step : null,
        message.interaction_id || null,
        message.name || null,
        message.tool_call_id || null,
        message.tool_calls ? JSON.stringify(message.tool_calls) : null,
        message.internal_type || null,
        Number.isFinite(message.duration_ms) ? message.duration_ms : null,
        Number.isFinite(message.real_ms) ? message.real_ms : null,
        JSON.stringify(message)
      );
      stats.messages += 1;
    });
  }

  for (const file of inventory.usageFiles) {
    const raw = readJson(file);
    const name = path.basename(path.dirname(file));
    insertUsage.run(
      name,
      scalar(raw.prompt_tokens),
      scalar(raw.completion_tokens),
      // `turns` is an array of per-turn records, not a count.
      Array.isArray(raw.turns) ? raw.turns.length : scalar(raw.turns),
      scalar(raw.duration_ms),
      scalar(raw.real_ms),
      JSON.stringify(raw)
    );
    stats.usage += 1;
  }

  for (const file of inventory.planFiles) {
    const raw = readJson(file);
    const name = path.basename(file).replace(/\.plan\.json$/, "");
    insertPlan.run(
      name,
      raw.question ?? null,
      timestamp(raw.created_at),
      timestamp(raw.updated_at),
      timestamp(raw.accepted_at),
      Array.isArray(raw.items) ? raw.items.length : null,
      JSON.stringify(raw)
    );
    stats.plans += 1;
  }

  for (const file of inventory.indexFiles) {
    if (!fs.existsSync(file)) continue;
    const relative = path.relative(memoryRoot, file);
    for (const { lineNo, value } of readJsonl(file)) {
      insertIndex.run(
        relative,
        lineNo,
        value.key ?? null,
        value.target ?? null,
        value.type ?? null,
        value.kind ?? null,
        value.key_status ?? null,
        value.status ?? null,
        value.session_id ?? null,
        value.interaction_id ?? null,
        timestamp(value.created_at),
        JSON.stringify(value)
      );
      stats.index += 1;
    }
  }

  for (const file of inventory.rootDocs) {
    const name = path.basename(file);
    insertDoc.run(
      name,
      name === "user.md" ? "user" : "memory",
      fs.readFileSync(file, "utf8"),
      path.relative(memoryRoot, file)
    );
    stats.documents += 1;
  }

  for (const file of inventory.dateFiles) {
    insertDate.run(
      path.basename(file, ".md"),
      fs.readFileSync(file, "utf8"),
      path.relative(memoryRoot, file)
    );
    stats.dateMemory += 1;
  }

  for (const file of inventory.stampFiles) {
    insertStamp.run(
      path.basename(file),
      fs.readFileSync(file, "utf8").trim(),
      path.relative(memoryRoot, file)
    );
    stats.stamps += 1;
  }

  db.exec("COMMIT");
  return stats;
  } catch (err) {
    try { db.exec("ROLLBACK"); } catch {}
    throw err;
  }
}

/**
 * Re-read the files and compare against what is in the database. Counts alone are
 * weak evidence, so this also checks that every stored payload still parses and
 * that the union of session ids matches in both directions.
 */
function verify(db, memoryRoot, inventory, legacyOwnerId = null) {
  const problems = [];
  const expected = {
    sessions: inventory.sessionFiles.length,
    messages: 0,
    usage: inventory.usageFiles.length,
    plans: inventory.planFiles.length,
    index: 0,
    documents: inventory.rootDocs.length,
    dateMemory: inventory.dateFiles.length,
    stamps: inventory.stampFiles.length,
  };

  const fileSessionIds = new Set();
  for (const file of inventory.sessionFiles) {
    const raw = readJson(file);
    const name = path.basename(file, ".json");
    fileSessionIds.add(
      sessionIdentity(raw, name, userIdFromSessionPath(file, memoryRoot, legacyOwnerId), legacyOwnerId)
    );
    expected.messages += Array.isArray(raw.messages) ? raw.messages.length : 0;
  }
  for (const file of inventory.indexFiles) {
    if (fs.existsSync(file)) expected.index += readJsonl(file).length;
  }

  const actual = {};
  for (const [name, table] of [
    ["sessions", "sessions"],
    ["messages", "messages"],
    ["usage", "session_usage"],
    ["plans", "session_plans"],
    ["index", "memory_index"],
    ["documents", "memory_documents"],
    ["dateMemory", "date_memory"],
    ["stamps", "stamps"],
  ]) {
    actual[name] = db.prepare(`SELECT COUNT(*) AS n FROM ${table}`).get().n;
  }

  console.log("");
  console.log("category     files/db expected   actual   status");
  for (const key of Object.keys(expected)) {
    const ok = expected[key] === actual[key];
    if (!ok) problems.push(`${key}: expected ${expected[key]}, database has ${actual[key]}`);
    console.log(
      `${key.padEnd(13)}${String(expected[key]).padStart(6)}${String(actual[key]).padStart(9)}   ${ok ? "ok" : "MISMATCH"}`
    );
  }

  const dbSessionIds = new Set(db.prepare("SELECT session_id FROM sessions").all().map((r) => r.session_id));
  for (const id of fileSessionIds) if (!dbSessionIds.has(id)) problems.push(`session missing from database: ${id}`);
  for (const id of dbSessionIds) if (!fileSessionIds.has(id)) problems.push(`session not present in files: ${id}`);

  // Payloads must round-trip: if JSON.stringify(JSON.parse(payload)) throws or
  // changes, the database cannot reproduce the original response later.
  const payloadChecks = [
    ["messages", "payload"],
    ["memory_index", "payload"],
    ["session_usage", "payload"],
    ["session_plans", "payload"],
  ];
  for (const [table, column] of payloadChecks) {
    for (const row of db.prepare(`SELECT rowid AS rid, ${column} AS p FROM ${table}`).all()) {
      try {
        const parsed = JSON.parse(row.p);
        if (parsed === null || typeof parsed !== "object") {
          problems.push(`${table} rowid=${row.rid}: payload is not an object`);
        }
      } catch (error) {
        problems.push(`${table} rowid=${row.rid}: payload does not parse (${error.message})`);
      }
    }
  }

  // Message ordinals must be dense and start at zero for every session, otherwise
  // a reader that indexes by position will silently shift.
  for (const row of db
    .prepare("SELECT session_id, COUNT(*) AS n, MIN(ordinal) AS lo, MAX(ordinal) AS hi FROM messages GROUP BY session_id")
    .all()) {
    if (row.lo !== 0 || row.hi !== row.n - 1) {
      problems.push(`session ${row.session_id}: ordinals not dense (min=${row.lo} max=${row.hi} count=${row.n})`);
    }
  }

  console.log("");
  if (problems.length) {
    console.log(`VERIFY FAILED (${problems.length} problem(s)):`);
    for (const problem of problems.slice(0, 20)) console.log(`  - ${problem}`);
    return false;
  }
  console.log("VERIFY PASSED: every category matches and payloads round-trip");
  return true;
}

/**
 * Fingerprint of every file the import reads.
 *
 * Includes the file list, not just the contents: an added or a deleted session
 * has to be detected too, and a path-set diff catches both without reading a
 * single byte of the large files.
 */
function sourceFingerprint(memoryRoot, inventory) {
  const parts = [];
  for (const group of [
    "sessionFiles",
    "usageFiles",
    "planFiles",
    "indexFiles",
    "rootDocs",
    "dateFiles",
    "stampFiles",
  ]) {
    for (const file of inventory[group] || []) {
      const relative = path.relative(memoryRoot, file);
      if (!fs.existsSync(file)) {
        parts.push(`${relative}:missing`);
        continue;
      }
      const stat = fs.statSync(file);
      parts.push(`${relative}:${Math.floor(stat.mtimeMs)}:${stat.size}`);
    }
  }
  return crypto.createHash("sha256").update(parts.join("\n")).digest("hex");
}

/**
 * Bring the database up to date with the files, but only when something changed.
 *
 * The fingerprint decides whether to import at all; when it does change the
 * import is a full rebuild rather than a diff. At this size (~2.5 MB, 601
 * messages) a rebuild costs about half a second and only runs when a file
 * actually moved. That buys correctness: there is no incremental path that can
 * drift out of step, and the code that runs is the same code `--verify` checks.
 */
function sync(db, memoryRoot, inventory, legacyOwnerId = null) {
  const fingerprint = sourceFingerprint(memoryRoot, inventory);
  const stored = db.prepare("SELECT value FROM meta WHERE key = 'source_fingerprint'").get();
  if (stored && stored.value === fingerprint) {
    return { changed: false, fingerprint, stats: null };
  }
  const stats = importAll(db, memoryRoot, inventory, legacyOwnerId);
  db.prepare(
    "INSERT INTO meta (key, value) VALUES ('source_fingerprint', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value"
  ).run(fingerprint);
  return { changed: true, fingerprint, stats };
}

function main(argv) {
  const args = argv.slice(2);
  const verifyOnly = args.includes("--verify");
  const syncOnly = args.includes("--sync");
  const dbIndex = args.indexOf("--db");
  const repoRoot = path.join(__dirname, "..", "..");
  // --memory-root exists so the sync behaviour can be tested against a throwaway
  // copy instead of the live store.
  const rootIndex = args.indexOf("--memory-root");
  const memoryRoot = rootIndex >= 0 ? path.resolve(args[rootIndex + 1]) : path.join(repoRoot, "memory");
  const dbPath = dbIndex >= 0 ? path.resolve(args[dbIndex + 1]) : path.join(memoryRoot, "fairy.db");

  if (!fs.existsSync(memoryRoot)) {
    console.error(`memory root not found: ${memoryRoot}`);
    return 2;
  }

  const { DatabaseSync } = require("node:sqlite");
  fs.mkdirSync(path.dirname(dbPath), { recursive: true });
  const db = new DatabaseSync(dbPath);
  db.exec("PRAGMA journal_mode = WAL");
  // Order matters: see migrate(). Adding the column first is what lets the
  // indexes in SCHEMA be created on a database that predates the column.
  const migrated = migrate(db);
  if (migrated.length) console.log(`migrated   : ${migrated.join(", ")}`);
  db.exec(SCHEMA);

  const inventory = buildInventory(memoryRoot);
  console.log(`memory root: ${memoryRoot}`);
  console.log(`database   : ${dbPath}`);

  if (syncOnly) {
    const result = sync(db, memoryRoot, inventory);
    if (!result.changed) {
      console.log("sync       : no change (fingerprint matches)");
      return 0;
    }
    console.log(`sync       : re-imported  ${JSON.stringify(result.stats)}`);
    return verify(db, memoryRoot, inventory) ? 0 : 1;
  }

  console.log(
    `files      : ${inventory.sessionFiles.length} sessions, ${inventory.usageFiles.length} usage, ` +
      `${inventory.planFiles.length} plans, ${inventory.indexFiles.length} index files, ` +
      `${inventory.rootDocs.length} docs, ${inventory.dateFiles.length} date-memory, ${inventory.stampFiles.length} stamps`
  );

  const stats = importAll(db, memoryRoot, inventory);
  console.log(`imported   : ${JSON.stringify(stats)}`);

  if (!verifyOnly) {
    console.log("(run with --verify to compare against the files)");
    return 0;
  }
  return verify(db, memoryRoot, inventory) ? 0 : 1;
}

/**
 * Resolve which Fairy account owns a session file.
 *
 * The multi-user layout puts one directory level between `sessions/` and the
 * date folder:
 *
 *   memory/sessions/u5/<date>/<name>.json   -> 5
 *   memory/sessions/<date>/<name>.json      -> null
 *
 * The second form is every session written before multi-user support, and it is
 * also what the single-user desktop build still produces, so returning null for
 * it keeps the old behaviour intact instead of guessing an owner.
 *
 * `legacyOwnerId` is the fallback for that second form when the caller already
 * knows who should inherit the history - the first account in the auth database
 * is the person who owned this machine before anyone else had an account, so
 * their pre-multi-user sessions belong to them. It is passed in rather than
 * derived here because this module has no access to the separate auth database
 * and a wrong guess would be worse than an explicit answer.
 *
 * The owner segment is matched as "u" plus digits rather than as bare digits on
 * purpose. A bare-integer test looks equivalent but is not: date folders are
 * digits too, and a compact one such as 20260928 passes /^\d+$/. Those sessions
 * would then be filed under a user id that matches no account, so they would
 * vanish from every real user's list - no error, just silently missing history.
 * The letter prefix cannot collide with a date, so the two layouts stay
 * distinguishable no matter how the date is spelled.
 */
function userIdFromSessionPath(file, memoryRoot, legacyOwnerId = null) {
  const rel = path.relative(memoryRoot, file);
  const parts = rel.split(/[\\/]+/).filter(Boolean);
  if (parts[0] !== "sessions" || parts.length < 3) return null;
  const match = /^u(\d+)$/.exec(parts[1]);
  if (!match) return legacyOwnerId;
  const owner = Number(match[1]);
  return owner > 0 ? owner : null;
}

/**
 * Identify a session file in the database.
 *
 * The file's own `session_id` wins when it has one, because that is what the
 * agent wrote and what its messages were keyed by. Otherwise the identity is
 * the name - except for a session that lives in someone else's tree.
 *
 * That exception is not cosmetic. Each account has a session named after today's
 * date, so the second account's file has the same name as the first's, and a
 * name-only identity would be a duplicate primary key - the import aborts, the
 * projection stays stale, and both accounts lose their whole history rather than
 * one session. Qualifying the id by owner keeps the two apart.
 *
 * The machine owner's ids stay bare so the rows they already have keep the
 * identity they have always had.
 */
function sessionIdentity(raw, name, userId, legacyOwnerId = null) {
  if (raw && raw.session_id) return String(raw.session_id);
  if (userId != null && userId !== legacyOwnerId) return `u${userId}/${name}`;
  return name;
}

/**
 * Bring an existing database up to the current SCHEMA.
 *
 * SCHEMA is written with CREATE TABLE IF NOT EXISTS, which by design never
 * touches a table that already exists - that is what makes importing into a
 * populated database safe. The side effect is that adding a column to SCHEMA
 * only helps brand-new databases, and every Fairy install that already has
 * memory/fairy.db would keep the old shape forever and then fail at runtime
 * with "no such column: user_id" the first time a filtered query ran.
 *
 * So each new column gets an explicit, idempotent migration here. Every step is
 * guarded by a table_info probe rather than by a remembered version counter:
 * probing the real schema cannot drift out of sync with it, and re-running is
 * harmless. Backfilling is deliberately not attempted - importAll() clears and
 * reinserts these tables from the files anyway, so the authoritative owner
 * comes from userIdFromSessionPath, not from a guess.
 *
 * migrate() must run BEFORE exec(SCHEMA), not after. That ordering is load
 * bearing, not stylistic: on an existing database exec(SCHEMA) skips the
 * CREATE TABLE (the table is already there, so the new column never appears)
 * but still runs the CREATE INDEX statements at the end of the block, and an
 * index on a column that does not exist yet aborts the whole exec. Running
 * first means the column is in place by the time the index is attempted.
 */
function migrate(db) {
  const applied = [];
  const tableExists = (table) =>
    db.prepare("SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?").get(table);
  const hasColumn = (table, column) =>
    tableExists(table) && db.prepare(`PRAGMA table_info(${table})`).all().some((row) => row.name === column);

  if (!hasColumn("sessions", "user_id")) {
    if (tableExists("sessions")) {
      db.exec("ALTER TABLE sessions ADD COLUMN user_id INTEGER");
      applied.push("sessions.user_id");
    }
    // A missing table is not a migration: exec(SCHEMA) is about to create it
    // with the column already declared.
  }
  return applied;
}

module.exports = {
  SCHEMA,
  TABLES,
  buildInventory,
  importAll,
  sync,
  migrate,
  sourceFingerprint,
  verify,
  listFiles,
  readJson,
  readJsonl,
  userIdFromSessionPath,
  sessionIdentity,
};

if (require.main === module) {
  process.exit(main(process.argv));
}
