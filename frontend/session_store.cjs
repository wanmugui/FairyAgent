/**
 * Database-backed session reads.
 *
 * Step 2b of the memory migration: the file readers move to SQLite behind a flag
 * while writes still go to disk. Every function here has a file-based twin in
 * server.cjs that it must match exactly - not "roughly", because the failure
 * mode of a mismatch is a session list with the wrong order or a missing
 * message, which looks like a working page.
 *
 * Two deliberate non-goals:
 *
 *   - `patchMissingSubtaskResults` and `enrichSubtaskStats` keep reading the
 *     per-session `subtasks/` directory from disk. They are post-processors over
 *     the message array, subtask storage is not in scope for this step, and they
 *     are no-ops only because no subtask has ever run on this store - folding
 *     them into SQL would be correct today and silently wrong the first time one
 *     does run.
 *   - `subtaskSessionsForParent` stays on files too: every existing session has
 *     a null parent_session, so a SQL version would have no data to be verified
 *     against. Shipping an unverifiable rewrite of a code path is the thing this
 *     migration is trying not to do.
 */

"use strict";

/**
 * Session ownership scopes.
 *
 * These exist so "nobody is filtering" cannot be reached by accident. The two
 * cases look identical at the call site - the caller has no user id - but they
 * must not behave identically: one is the single-user desktop build, where
 * every session really is the caller's, and the other is an authenticated
 * request whose identity failed to resolve, where returning everything would
 * hand one account another account's history.
 *
 * So the scope is passed explicitly and a missing scope never means "allow all".
 * A numeric scope is an account id and matches only that account's rows.
 */
const SINGLE_USER = Symbol("single-user");
const ANONYMOUS = Symbol("anonymous");

/**
 * Build the ownership predicate for a scope.
 *
 * Returns SQL without a leading keyword so each caller can compose it with its
 * own existing WHERE clause. SINGLE_USER yields a tautology rather than an
 * empty string so the queries keep a uniform shape and cannot end up with a
 * dangling AND. ANONYMOUS yields a false predicate instead of an early return
 * so that a session which happens to be missing still reports "missing" in
 * exactly the same way it does for a real user, and callers keep one code path.
 */
function ownerClause(scope, alias) {
  const column = alias ? `${alias}.user_id` : "user_id";
  if (scope === SINGLE_USER) return "1 = 1";
  if (scope === ANONYMOUS) return "0 = 1";
  if (!Number.isInteger(scope) || scope <= 0) {
    throw new TypeError(
      "session scope must be SINGLE_USER, ANONYMOUS, or a positive user id"
    );
  }
  return `${column} = ?`;
}

function ownerParams(scope) {
  if (scope === SINGLE_USER || scope === ANONYMOUS) return [];
  return [scope];
}

/**
 * Only `messages` is fetched here; everything else about a session lives in the
 * sessions.payload column so reconstruction does not depend on how a timestamp
 * happened to be formatted when the file was written.
 */
function readSessionRecord(db, name, scope = SINGLE_USER) {
  const row = db
    .prepare(`SELECT session_id, payload FROM sessions WHERE name = ? AND ${ownerClause(scope)}`)
    .get(String(name), ...ownerParams(scope));
  if (!row) return null;
  if (!row.payload) return null;
  let meta;
  try {
    meta = JSON.parse(row.payload);
  } catch {
    return null;
  }
  const messages = readSessionMessages(db, row.session_id, scope);
  if (messages === null) return null;
  return { ...meta, messages };
}

/**
 * Messages-only reader, used by the chat endpoint. Returns the raw message
 * array (parsed from the `payload` column), or null if the session does not
 * exist. Skips malformed rows instead of failing the whole call, the same
 * way the file reader tolerates one unreadable message.
 *
 * Goes through the idx_messages_session_ordinal index added in step 2b so
 * even multi-thousand-message sessions open in single-digit milliseconds:
 * the alternative was JSON.parse() on a multi-MB session file, which could
 * be hundreds of ms on the same sessions.
 */
/**
 * Read the session metadata (everything except messages). Used by the
 * messages route to fill model / usage fields without paying for a second
 * whole-session JSON parse on disk. The payload column already holds the
 * original record, so reconstruction here mirrors what readSessionRecord
 * would have returned minus the messages array.
 */
function readSessionMeta(db, name, scope = SINGLE_USER) {
  const row = db
    .prepare(`SELECT session_id, payload FROM sessions WHERE name = ? OR session_id = ?`)
    .get(String(name), String(name));
  if (!row || !row.payload) return null;
  // The name lookup above is deliberately unfiltered because the same string
  // can be either a name or an id; the ownership check below is what actually
  // decides visibility, and it runs on the row we are about to return.
  if (!sessionBelongsTo(db, row, scope)) return null;
  try { return JSON.parse(row.payload); } catch { return null; }
}

/**
 * Ownership check for a row that has already been located by name or id.
 *
 * Kept separate from the query so readSessionMeta and the paged reader can share
 * it, and so a "WHERE name = ? OR session_id = ?" lookup never becomes a way to
 * read another account's session by guessing its id.
 */
function sessionBelongsTo(db, row, scope) {
  if (scope === SINGLE_USER) return true;
  if (scope === ANONYMOUS) return false;
  const owner = db
    .prepare("SELECT user_id FROM sessions WHERE session_id = ?")
    .get(row.session_id);
  return !!owner && owner.user_id === scope;
}

function readSessionMessages(db, sessionId, scope = SINGLE_USER) {
  const row = db
    .prepare(`SELECT session_id, user_id FROM sessions WHERE name = ? OR session_id = ?`)
    .get(String(sessionId), String(sessionId));
  if (!row) return null;
  if (scope === ANONYMOUS) return null;
  if (scope !== SINGLE_USER && row.user_id !== scope) return null;
  return db
    .prepare("SELECT payload FROM messages WHERE session_id = ? ORDER BY ordinal")
    .all(row.session_id)
    .map((message) => {
      try {
        return JSON.parse(message.payload);
      } catch {
        return null;
      }
    })
    .filter(Boolean);
}

// The sidebar and header count differs from raw row count: tool rows are
// attached to their assistant block, while internal continuation rows are not
// user-visible at all. Context-summary rows stay counted because the UI turns
// them into SUMMARY process blocks rather than user bubbles.
const DISPLAY_INTERNAL_TYPES = [
  "auto_continue",
  "delivered_status",
  "auto_answer",
  "execution_guard",
  "plan_continuation",
  "vision_context",
];

function countDisplayMessages(db, sessionId) {
  const placeholders = DISPLAY_INTERNAL_TYPES.map(() => "?").join(",");
  const row = db
    .prepare(
      `SELECT
         (SELECT COUNT(*) FROM messages
           WHERE session_id = ? AND role = 'assistant') +
         (SELECT COUNT(*) FROM messages
           WHERE session_id = ? AND role = 'user'
             AND COALESCE(json_extract(payload, '$.internal_type'), '') NOT IN (${placeholders})
             AND COALESCE(json_extract(payload, '$.content'), '') NOT LIKE '[会话状态]%') AS n`
    )
    .get(sessionId, sessionId, ...DISPLAY_INTERNAL_TYPES);
  return Number(row && row.n) || 0;
}


/**
 * Paginated messages reader. Returns the latest `limit` raw messages when
 * `beforeOrdinal` is null, otherwise the latest `limit` messages with
 * ordinal strictly less than `beforeOrdinal`. Messages are returned in
 * chronological order (oldest first), each tagged with the original ordinal
 * the file path also carries so the consumer can drive the next page.
 *
 * Four queries are needed for paging plus the stable UI display count:
 *   - sessions lookup (1) for the session_id (cheap, primary key)
 *   - COUNT(*) (1) for the total the frontend needs to render paging UI
 *   - the actual slice (1) using idx_messages_session_ordinal
 *
 * Two queries would not work: COUNT and the slice both run together with
 * a window function, but the planner picks a worse plan than two targeted
 * index scans and the slice is the only hot path.
 */
function readSessionMessagesPaged(db, name, opts, scope = SINGLE_USER) {
  const o = opts || {};
  // Bound the limit. The frontend asks for 50; we cap at 500 so a malformed
 // caller cannot ask the DB for a multi-thousand-row slice on every scroll.
  const limit = Math.max(1, Math.min(Number(o.limit) || 50, 500));
  const beforeOrdinal = o.beforeOrdinal == null ? null : Number(o.beforeOrdinal);
  const afterOrdinal = o.afterOrdinal == null ? null : Number(o.afterOrdinal);

  const row = db
    .prepare(`SELECT session_id, user_id FROM sessions WHERE name = ? OR session_id = ? LIMIT 1`)
    .get(String(name), String(name));
  if (!row) return null;
  // Same rule as readSessionMessages: locate first, then refuse if the located
  // row belongs to somebody else, so paging cannot be used to walk into another
  // account's session by id.
  if (scope === ANONYMOUS) return null;
  if (scope !== SINGLE_USER && row.user_id !== scope) return null;
  const sessionId = row.session_id;

  const total = db.prepare("SELECT COUNT(*) AS n FROM messages WHERE session_id = ?").get(sessionId).n;

  // Build the slice query. ORDER BY ordinal DESC LIMIT N then reverse in JS
  // so the response is always chronological; the index handles both
  // directions cheaply.
  let rows;
  if (afterOrdinal != null) {
    rows = db.prepare("SELECT ordinal, payload FROM messages WHERE session_id = ? AND ordinal > ? ORDER BY ordinal LIMIT ?").all(sessionId, afterOrdinal, limit);
  } else if (beforeOrdinal != null) {
    rows = db.prepare("SELECT ordinal, payload FROM messages WHERE session_id = ? AND ordinal < ? ORDER BY ordinal DESC LIMIT ?").all(sessionId, beforeOrdinal, limit);
    rows.reverse();
  } else {
    rows = db.prepare("SELECT ordinal, payload FROM messages WHERE session_id = ? ORDER BY ordinal DESC LIMIT ?").all(sessionId, limit);
    rows.reverse();
  }

  const messages = [];
  for (const r of rows) {
    let msg;
    try { msg = JSON.parse(r.payload); } catch { continue; }
    msg.ordinal = r.ordinal;
    messages.push(msg);
  }

  const oldestOrdinal = messages.length ? messages[0].ordinal : null;
  const newestOrdinal = messages.length ? messages[messages.length - 1].ordinal : null;
  // hasMore on the "older" side: there is at least one message older than
 // what we returned. On the "newer" side: there is at least one newer than
 // what we returned.
  const hasMoreOlder = oldestOrdinal != null && oldestOrdinal > 0;
  const hasMoreNewer = newestOrdinal != null && newestOrdinal < total - 1;
  const displayTotal = countDisplayMessages(db, sessionId);

  return {
    messages,
    paging: {
      total,
      display_total: displayTotal,
      limit,
      oldest_ordinal: oldestOrdinal,
      newest_ordinal: newestOrdinal,
      has_more_older: hasMoreOlder,
      has_more_newer: hasMoreNewer,
    },
  };
}

/**
 * Strips the decorations the agent's own prompt scaffolding leaves in a first
 * user message: file-context blocks, entities, empty markdown links and tags.
 *
 * Moved verbatim from server.cjs so both readers share one implementation; a
 * second copy would drift and only show up as an ugly preview.
 */
function cleanSessionPreviewText(value) {
  return String(value || '')
    .replace(/<file_context>[\s\S]*?<\/file_context>\s*/gi, ' ')
    .replace(/&lt;file_context&gt;[\s\S]*?&lt;\/file_context&gt;\s*/gi, ' ')
    .replace(/&#x?[0-9a-f]+;/gi, ' ')
    .replace(/(?:\*\*)?\[\s*\](?:\*\*)?/g, ' ')
    .replace(/<[^>]+>/g, ' ')
    .replace(/\s+/g, ' ')
    .trim();
}

/**
 * Same shape and ordering as the file-based listSessions():
 *   { name, modified, message_count, preview, model, kind,
 *     parent_session, domain, created_by, created_at, daily_date }
 * sorted by `modified` descending.
 *
 * `modified` is built from the stored session-file mtime, not from created_at,
 * because that is what the file version reports and what the sort depends on.
 */
function listSessions(db, scope = SINGLE_USER) {
  // Resolved before the try below on purpose. That catch exists to tolerate one
  // broken row or a missing table, and it must not also swallow a bad scope: a
  // caller that passes null by mistake would otherwise get an empty list and
  // read it as "this account has no sessions". A wrong scope is a programming
  // error and should fail loudly; only SQL-level trouble degrades to empty.
  const clause = ownerClause(scope, "s");
  const params = ownerParams(scope);
  // One round trip, not N+1. The previous version fired one prepared query
  // per session for the first user message, which made the sidebar list
  // proportional to (sessions x messages) instead of (sessions). The
  // correlated subquery below runs inside the same statement and uses the
  // idx_messages_session_role_ordinal index added in step 2b of the memory
  // migration to seek straight to (session_id, role=user, ordinal=0).
  const rows = (() => {
    try {
      return db
        .prepare(
          `SELECT s.session_id, s.name, s.kind, s.model, s.domain, s.parent_session,
                  s.created_by, s.created_at, s.daily_date, s.message_count, s.source_mtime,
                  s.payload,
                  (SELECT m.content FROM messages m
                   WHERE m.session_id = s.session_id AND m.role = 'user'
                   ORDER BY m.ordinal LIMIT 1) AS first_user_content
           FROM sessions s
           WHERE ${clause}`
        )
        .all(...params);
    } catch {
      return [];
    }
  })();

  const items = [];
  for (const row of rows) {
    try {
      // `payload` still has to be parsed: listSessions() reports the original
      // created_at value (whatever string or number the agent wrote) rather
      // than the epoch-ms the `created_at` column normalises to, and the file
      // reader does the same. The byte cost is the price of the existing
      // compare-session-readers.cjs guarantee; the N+1 query that used to
      // dominate the request was the actual cost.
      const meta = row.payload ? JSON.parse(row.payload) : {};
      const preview = cleanSessionPreviewText(row.first_user_content || '').slice(0, 60);
      items.push({
        name: row.name,
        modified: row.source_mtime
          ? new Date(row.source_mtime).toISOString().replace('T', ' ').slice(0, 19)
          : '',
        message_count: row.message_count,
        preview,
        model: row.model || null,
        kind: row.kind || 'main',
        parent_session: row.parent_session || null,
        domain: row.domain || null,
        created_by: row.created_by || null,
        created_at: meta.created_at !== undefined ? meta.created_at : null,
        daily_date: row.daily_date || null,
        // 渠道会话带着"我是哪条 QQ/微信会话"的元数据，侧栏据此标注来源
        // （原来是按名字前缀猜，改名成主会话级别之后就猜不出来了）。
        channel: meta.channel || null,
      });
    } catch {
      // Match the file version: one unreadable entry is skipped, not fatal.
    }
  }
  items.sort((a, b) => String(b.modified).localeCompare(String(a.modified)));
  return items;
}

/**
 * Does this account own the named session?
 *
 * Exists as a separate cheap probe so a request handler can ask the ownership
 * question once, up front, before any of the file-reading helpers go on to open
 * SESSIONS/<name>/... directly. Those helpers predate multi-user support and
 * filter nothing; checking ownership at the door means they do not each have to
 * learn about it, which is the difference between one gate that is easy to
 * audit and seven filters that are easy to forget. It also covers endpoints
 * that never call a filtered reader at all, such as the trace routes.
 */
function ownsSession(db, name, scope) {
  if (scope === ANONYMOUS) return false;
  if (scope === SINGLE_USER) return true;
  const row = db
    .prepare("SELECT 1 AS ok FROM sessions WHERE (name = ? OR session_id = ?) AND user_id = ?")
    .get(String(name), String(name), scope);
  return !!row;
}

function sessionNames(db, scope = SINGLE_USER) {
  // Same reasoning as listSessions: validate the scope outside the catch.
  const clause = ownerClause(scope);
  const params = ownerParams(scope);
  try {
    return db
      .prepare(`SELECT name FROM sessions WHERE ${clause} ORDER BY name`)
      .all(...params)
      .map((row) => row.name);
  } catch {
    return [];
  }
}

module.exports = { readSessionRecord, readSessionMessages, readSessionMessagesPaged, readSessionMeta, listSessions, sessionNames, ownsSession, cleanSessionPreviewText, SINGLE_USER, ANONYMOUS };
