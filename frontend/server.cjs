const http = require("http");
const fs = require("fs");
const path = require("path");
const { AsyncLocalStorage } = require("node:async_hooks");
const { spawn } = require("child_process");
const { buildAgentProcessOptions, resolveAgentExecutable } = require("./agent_executable.cjs");
const { createSessionArchive } = require("./session_archive.cjs");
const { resolveDeckId, listDeckPages, resolveDeckFile, buildDeckPreviewHtml } = require("./ppt_preview.cjs");
const { resolveSessionPptOutline } = require("./ppt_outline.cjs");
const fm = require("../filemanager/filemanager.cjs");
const { activeTraceSpanMs } = require("./trace_timing.cjs");
const sessionStore = require("./session_store.cjs");
const { createWechatLink } = require("./wechat_link.cjs");
const scheduler = require("./scheduler.cjs");

// Step 2b of the memory migration: read sessions from memory/fairy.db instead of
// the file tree. Off by default - the file path is still the one this repo has
// always used, and it stays the fallback until step 3 makes the database
// authoritative for writes too.
//
// Opened read-only on purpose: while writes still go to disk, a bug here must not
// be able to corrupt the imported copy.
let memoryDb = null;
const { createAuth } = require("./auth.cjs");

// Authentication gate for everything this server exposes.
//
// Vite proxies /api, /viewer.html and /vendor here, so checking at this boundary
// covers the agent API, the file viewer and the file manager in one place. The SPA
// shell itself is served by Vite, which is fine: it loads, and then every request
// it makes comes back 401 until a session exists.
//
// Off by default so an existing single-user desktop setup is unchanged. Turn it on
// before binding the frontend to a LAN interface.
const auth = createAuth({
  enabled: String(process.env.FAIRY_AUTH || "").trim() === "1",
  databasePath: process.env.FAIRY_AUTH_DB || path.join(__dirname, "..", "memory", "auth.db"),
  trustedIpDays: Number(process.env.FAIRY_AUTH_TRUSTED_IP_DAYS || "30"),
  secureCookies: String(process.env.FAIRY_AUTH_SECURE_COOKIE || "").trim() === "1",
  // Opt-in: a LAN-reachable Fairy can run shell commands, so open registration has
  // to be a deliberate choice about the network, not a default.
  allowRegistration: String(process.env.FAIRY_AUTH_ALLOW_REGISTRATION || "").trim() === "1",
});

// Per-session inject channel.
// The host writes a single JSON line per inject/cancel to the child
// process's stdin. The agent keeps a reader goroutine that feeds lines into
// its AgentController. This replaces the earlier UDP scheme that broke with
// EBADF when the kernel-bound port was queried before bind() completed.
//
// Wire format (each line is one JSON object terminated by '\n'):
//   {"op":"inject","text":"..."}
//   {"op":"cancel"}
const injectRegistry = {
  // session key (see sessionRunKey) -> { child }. Keyed by tree and name rather
  // than by name alone: two accounts can both have a session named after today's
  // date, and a shared key would let one account's Stop reach the other's agent.
  sessions: new Map(),
  attach(sessionName, child) {
 if (! child) return;
 this.sessions.set(sessionRunKey(sessionName), { child });
  },
  detach(sessionName) {
 this.sessions.delete(sessionRunKey(sessionName));
  },
  inject(sessionName, text) {
 const entry = this.sessions.get(sessionRunKey(sessionName));
 if (!entry || !entry.child || !entry.child.stdin || entry.child.stdin.destroyed) {
 return false;
 }
 try {
 entry.child.stdin.write(JSON.stringify({ op: "inject", text }) + "\n");
 return true;
 } catch (err) {
 return false;
 }
  },
  cancel(sessionName) {
 const entry = this.sessions.get(sessionRunKey(sessionName));
 if (!entry || !entry.child || !entry.child.stdin || entry.child.stdin.destroyed) {
 return false;
 }
 try {
 entry.child.stdin.write(JSON.stringify({ op: "cancel" }) + "\n");
 return true;
 } catch (err) {
 return false;
 }
  },
  // stop signals the agent to finish its current step and then drain any
 // queued user messages into a fresh turn. Use this when the user wants to
 // interrupt and switch topic without losing the in-flight work.
 stop(sessionName) {
 const entry = this.sessions.get(sessionRunKey(sessionName));
 if (!entry || !entry.child || !entry.child.stdin || entry.child.stdin.destroyed) {
 return false;
 }
 try {
 entry.child.stdin.write(JSON.stringify({ op: "stop" }) + "\n");
 return true;
 } catch (err) {
 return false;
 }
  },
};

const REPO = path.resolve(__dirname, "..");
// agent 的 browser_bridge.mjs 把 CDP screencast 抓到的帧写到这里,
// 本服务轮询该目录推 MJPEG 给远端观者。两侧靠同一个环境变量对齐,
// 缺省值一致,保证不配也能工作。
const FRAME_DIR = process.env.FAIRY_BROWSER_FRAME_DIR || "/tmp/fairy-browser-frames";

// WeChat iLink link-up - the "扫码接入" button in the settings panel. In its own
// module so this file stays about the agent rather than about a messaging
// protocol; frontend/wechat_link.cjs documents what it owns.
// bindChannel is a closure, not a direct call: the scan that triggers it happens
// long after this module finished loading, and it must not throw into the scan
// flow if the auth store is unavailable.
const wechatLink = createWechatLink({
  repoRoot: REPO,
  bindChannel: (conversationId, userId) => {
    if (!auth || !auth.store) throw new Error("auth store unavailable");
    return auth.store.bindChannel("wechat", conversationId, userId);
  },
});

let memorySync = null;
// The memory tree holds every session file and the projection database built
// from it. It defaults to the repo's memory/ directory, but FAIRY_MEMORY_ROOT
// overrides it so a test server can be pointed at a throwaway copy.
//
// This override exists because there was no other way to exercise the server
// without touching real data: sync() clears and repopulates the session tables
// on every start, so a test that wanted a server at all would have been
// rewriting the owner's live history. Anything that changes this path must keep
// SESSIONS derived from it (see below) or ownership checks and file reads would
// end up looking at two different trees.
const MEMORY_ROOT = process.env.FAIRY_MEMORY_ROOT
  ? path.resolve(process.env.FAIRY_MEMORY_ROOT)
  : path.join(REPO, "memory");
// Declared after MEMORY_ROOT, not beside REPO, because const bindings are in
// their temporal dead zone until initialised: a SESSIONS line placed any
// earlier threw "Cannot access 'MEMORY_ROOT' before initialization" and the
// server died at require time, before the listener ever opened. The order of
// these two statements is load-bearing, not cosmetic.
const SESSIONS = path.join(MEMORY_ROOT, "sessions");
// On by default. The database readers were verified against the file readers -
// byte-identical /api/sessions output, deeply-equal records, sync proven to
// follow live writes - so this is the real path now rather than a dormant one.
//
// FAIRY_MEMORY_DB=0 forces the file path back, and the file path also takes over
// automatically when the database cannot be opened. That fallback is the reason
// the file branch below is kept rather than deleted: it is the safety net, not
// leftover double-write code.
if (String(process.env.FAIRY_MEMORY_DB ?? "1").trim() !== "0") {
  try {
    const { DatabaseSync } = require("node:sqlite");
    // Writable on purpose. The agent still writes session files, so this
    // connection exists so the importer can bring the database up to date before
    // anything reads from it. Files stay the source of truth; the database is a
    // queryable projection of them, which is why there is no double-write to get
    // out of step - the sync checks a fingerprint and rebuilds only when a file
    // actually moved.
    memoryDb = new DatabaseSync(path.join(MEMORY_ROOT, "fairy.db"));
    memorySync = require("../tools/memory-migration/import-memory.cjs");
    // Bootstrap the schema in the server process. import-memory.cjs only
    // creates tables when run as a CLI; without this, the first /api/sessions
    // request on a fresh install would query non-existent tables and return
    // an empty sidebar until someone ran the CLI by hand. Every statement is
    // CREATE TABLE/INDEX IF NOT EXISTS so existing databases are untouched.
    try {
      // migrate() first, exec(SCHEMA) second, and the order is required rather
      // than tidy. exec(SCHEMA) skips CREATE TABLE when the table already
      // exists, so on a database from before a column was added the column
      // never appears - but the CREATE INDEX statements later in the same block
      // still run, and an index over a missing column aborts the entire exec.
      // That would leave every existing install unable to start until someone
      // noticed, which is why the migration probe is not optional here.
      try {
        const migrated = memorySync.migrate(memoryDb);
        if (migrated.length) console.log("[memory-db] migrated:", migrated.join(", "));
      } catch (migrateError) {
        console.error("[memory-db] schema migration failed:",
          migrateError && migrateError.message ? migrateError.message : migrateError);
        memoryDb = null;
        memorySync = null;
      }
      if (memoryDb) {
        memoryDb.exec(memorySync.SCHEMA);
        // WAL is mandatory: every /api/sessions request opens the same db, and
        // without WAL each read would block on the sync write. WAL also cuts
        // the importer rebuild from N fsyncs to one, which is the biggest part
        // of first-refresh latency.
        memoryDb.exec("PRAGMA journal_mode = WAL");
        // FULL is the default and would re-fsync every commit. NORMAL trades a
        // small durability window (last few transactions on power cut) for a
        // several-x commit speedup, which is the right trade for a projection
        // database that can always be rebuilt from the files.
        memoryDb.exec("PRAGMA synchronous = NORMAL");
      }
    } catch (error) {
      console.error("[memory-db] schema bootstrap failed:", error && error.message ? error.message : error);
      memoryDb = null;
      memorySync = null;
    }
  } catch (error) {
    console.error("[memory-db] falling back to files:", error && error.message ? error.message : error);
    memoryDb = null;
    memorySync = null;
  }
}

/**
 * Ownership scope for a session read, derived from the request.
 *
 * This is the single place that decides "whose sessions is this", and it is
 * deliberately the only place. Every call site passes the result through rather
 * than re-deriving identity, so there is no path where a handler forgets to
 * check and silently falls back to the desktop behaviour.
 *
 * The two "no user id" cases are kept apart on purpose:
 *   - authentication off: this is the single-user desktop build, every session
 *     really does belong to the one person using the machine, so nothing is
 *     filtered and existing installs keep working untouched.
 *   - authentication on but the request has no resolvable identity: return
 *     ANONYMOUS, which reads as "nobody owns this", not as "no filter". The
 *     difference is the whole security property - one missing branch here would
 *     hand an anonymous request every session on the machine.
 */
function sessionScope(req) {
  if (!auth || !auth.enabled) return sessionStore.SINGLE_USER;
  const identity = auth.resolveSession(req);
  return identity == null ? sessionStore.ANONYMOUS : identity.userId;
}

/**
 * Which sessions tree the work in flight belongs to.
 *
 * Every session read and write in this file used to say `path.join(SESSIONS,
 * name)` - one flat tree, which is correct while one person uses the machine.
 * With accounts it stops being correct: "the session named after today's date"
 * is a different conversation for each person, and a second account's evening
 * chat would otherwise be appended to the first account's transcript.
 *
 * The tree is resolved per request and carried in async context rather than
 * threaded through the twenty-odd helpers that build a path from a session
 * name. Threading it means editing every one of them and every caller of every
 * one, and the failure mode of a single missed call site is one person reading
 * another person's history - the kind of mistake that is invisible in review
 * and silent at runtime. Carrying it means one place decides, and everything
 * downstream inherits that decision without being able to disagree with it.
 *
 * Two properties make the context safe to lean on:
 *
 *   - it is entered once, at the top of the request handler, before any route
 *     runs, and it propagates to everything that request starts - including the
 *     child-process 'close' handler that writes the finished transcript;
 *   - work that outlives its request (detached subtasks, background
 *     continuations) runs from timers created at module load, which inherit
 *     nothing. Those carry their context with them explicitly; see
 *     registerBackgroundSubtask and pendingContinuations.
 */
const sessionScopeContext = new AsyncLocalStorage();

/**
 * The sessions tree for one ownership scope.
 *
 * The machine's owner keeps the flat tree that predates accounts, because that
 * is where their history already is - the same rule the memory importer applies
 * when it adopts pre-multi-user session files. Later accounts get a directory
 * of their own, which is also the layout that importer reads an owner out of,
 * so ownership survives a rebuild from files alone.
 */
function sessionsRootForScope(scope) {
  if (!auth || !auth.enabled) return SESSIONS;
  if (scope === sessionStore.SINGLE_USER || scope === sessionStore.ANONYMOUS) return SESSIONS;
  try {
    if (auth.store && auth.store.isOwner(scope)) return SESSIONS;
  } catch {}
  return path.join(SESSIONS, "u" + scope);
}

/** The tree the current request must read and write. */
function activeSessionsRoot() {
  const context = sessionScopeContext.getStore();
  return context && context.root ? context.root : SESSIONS;
}

/**
 * Key for the per-session in-memory registries: the running-chat map, the event
 * replay log, and the inject pipes.
 *
 * Deliberately not the session name. Two accounts can both be working in a
 * session named after today's date, and a name-keyed registry would let one
 * account's Stop reach the other account's agent - the directory is unique per
 * account, the name is not.
 */
function sessionRunKey(name) {
  return path.join(activeSessionsRoot(), String(name || ""));
}

/** Enter one scope's sessions tree for everything the callback starts. */
function runInSessionScope(scope, fn) {
  return sessionScopeContext.run({ scope, root: sessionsRootForScope(scope) }, fn);
}

/**
 * Re-enter the tree a piece of background work was started in.
 *
 * Detached subtasks and their continuations outlive the request that created
 * them, and they are driven by timers that were created at module load - so
 * they inherit no context and would resolve to the machine owner's tree. Each
 * one carries the context it was registered under instead.
 */
function runInCapturedSessionScope(context, fn) {
  return context ? sessionScopeContext.run(context, fn) : fn();
}

/**
 * Keep every continuation of this request inside the tree it was entered with.
 *
 * Wrapping the handler covers the synchronous part and nothing else. Node's
 * EventEmitter does not carry async context, so a route that reads its body via
 * req.on('end') runs in the *socket's* context - no tree at all - and would
 * quietly create the session in the machine owner's directory instead of the
 * caller's. That is measured, not assumed: with only run() around the handler,
 * the 'end' listener sees an empty store while the 'data' listener happens to
 * see the right one, which is exactly the kind of difference that produces a
 * bug nobody can reproduce.
 *
 * So the listeners are bound, once, at the top of the request. It is a small
 * amount of magic in one place, and the alternative - every route remembering
 * to re-enter - is an invariant that holds until the next route is added.
 */
function bindRequestContext(req) {
  const context = sessionScopeContext.getStore();
  if (!context) return;
  for (const method of ["on", "once"]) {
    const original = req[method];
    req[method] = function (event, listener) {
      if (typeof listener !== "function") return original.call(this, event, listener);
      return original.call(this, event, function (...args) {
        return sessionScopeContext.run(context, () => listener.apply(this, args));
      });
    };
  }
}

/**
 * The account a bridge is speaking for, resolved from the conversation it names.
 *
 * undefined - no conversation was declared (an ordinary web call)
 * null      - one was declared and is not bound; that is a refusal, not a fallback
 * number    - the account that conversation is bound to
 *
 * The binding table is the only authority for "who may speak through QQ or
 * WeChat": a bridge is trusted to report *where* a message came from, and to say
 * nothing at all about who sent it.
 */
function boundChannelScope(name, conversationId) {
  if (!auth || !auth.enabled) return undefined;
  const channelName = String(name || "").trim();
  const conversation = String(conversationId || "").trim();
  if (!channelName || !conversation) return undefined;
  try {
    const bound = auth.store.lookupChannelBinding(channelName, conversation);
    return bound ? bound.user_id : null;
  } catch {
    return null;
  }
}

/**
 * Which account's tree a chat turn is written into.
 *
 * A logged-in caller writes into their own tree, always: a body that claims to
 * belong to someone else is ignored, because "the request names the owner" is
 * exactly the shape of a cross-account write.
 *
 * A declared conversation outranks that rule, and is therefore checked first.
 * The QQ and WeChat bridges run on this machine and call from loopback, which is
 * a trusted device of the owner, so an identity-first order wrote every bound
 * conversation into the owner's transcript and never consulted the binding
 * table at all - measured, not theorised: an unbound conversation_id reached the
 * model-check stage instead of being refused with 403 unbound.
 *
 * With no conversation declared, a local caller - the detached-subtask
 * continuation - may name an owner instead, and only on the local path, where
 * the trust boundary is the socket rather than the field. With no declaration it
 * is the machine owner, which is who those callers belong to.
 */
function chatWriteScope(req, data) {
  const scope = sessionScope(req);
  if (!auth || !auth.enabled) return scope;
  const channel = data && data.channel;
  const bridged = boundChannelScope(channel && channel.name, channel && channel.conversation_id);
  if (bridged !== undefined) return bridged;
  // A local caller may name the account it is acting for: the scheduler runs a
  // family member's task, and the background continuations resume one. This is
  // checked before the cookie/trusted-device identity because this machine's own
  // loopback IS a trusted device of the owner, and identity-first order would
  // quietly run every member's task in the owner's tree - the same trap the
  // channel branch above exists for.
  const declared = Number(data && data.session_owner);
  if (Number.isInteger(declared) && declared > 0 && auth.isLocalDirectRequest(req)) return declared;
  const identity = auth.resolveSession(req);
  if (identity && identity.via !== "local") return scope;
  if (Number.isInteger(declared) && declared > 0) return declared;
  return scope;
}

/** True when this turn was reported by a bridge rather than the web UI. */
function isChannelTurn(data) {
  const channel = data && data.channel;
  return !!(channel && channel.conversation_id);
}

/**
 * The session a bridge conversation talks in.
 *
 * A QQ or WeChat conversation is a conversation in its own right: it gets a
 * top-level session of its own, inside the account that the binding table says
 * owns it. It is deliberately NOT folded into the day's web conversation (a
 * chat message then queued behind a long web turn, and a busy main session
 * turned it into "稍后再发" and lost it) and not a branch of it either: unlike a
 * branch, which is a piece of work with an end, this is a place a person keeps
 * talking. The name therefore carries no date - the history runs on.
 */
function channelSessionName(channel) {
  const id = String((channel && channel.conversation_id) || "");
  const parts = [
    String((channel && channel.name) || "chat"),
    String((channel && channel.kind) || ""),
    id.slice(0, 12),
  ]
    .map(value => value.toLowerCase().replace(/[^a-z0-9]+/g, ""))
    .filter(Boolean);
  return parts.length ? parts.join("-") : "channel";
}

/**
 * Give a channel conversation its metadata the first time it is used.
 *
 * The sidebar groups by these fields, and the agent's own save only merges into
 * whatever is already there - so the seed has to be written once, before the
 * first turn, or the conversation shows up with no owner of its own.
 */
function seedChannelSession(sessionFile, channel) {
  if (fs.existsSync(sessionFile)) return;
  const seed = {
    // 主会话级别：顶层可见、跨天延续，和网页端当天会话平级。
    kind: "main",
    parent_session: null,
    domain: String((channel && channel.name) || "chat"),
    // The conversation this branch belongs to. Anything that later wants to
    // answer "where did this request come from?" - the assistant filing a
    // reminder, for one - reads it from here instead of guessing from the name.
    channel: {
      name: String((channel && channel.name) || "chat"),
      kind: String((channel && channel.kind) || ""),
      conversation_id: String((channel && channel.conversation_id) || ""),
    },
    daily_date: null,
    created_by: "model",
    created_at: new Date().toISOString(),
    model: null,
    messages: [],
  };
  try {
    fs.writeFileSync(sessionFile, JSON.stringify(seed, null, 2), "utf-8");
  } catch {}
}

/**
 * Account that should inherit sessions written before multi-user support.
 *
 * Those files sit at memory/sessions/<date>/<name>.json with no owner level, so
 * they belong to whoever used the machine before accounts existed. That is the
 * first account in the auth database, which on this machine is the person who
 * set Fairy up. Returns null when authentication is off, which keeps the
 * single-user path byte-identical to before.
 */
function legacySessionOwnerId() {
  if (!auth || !auth.enabled || !auth.store) return null;
  try {
    // One definition of "the machine's owner", shared with the family group's
    // admin rule and with the auth layer's own local-operator identity.
    return auth.store.ownerUserId();
  } catch {
    return null;
  }
}

/**
 * Gate a request that goes on to read SESSIONS/<name>/... directly.
 *
 * Most session read paths go through session_store, which filters by owner.
 * Several older ones do not: the trace routes read trace.jsonl, run files and
 * usage.json straight off disk, and they predate multi-user support entirely.
 * Filtering each of them would mean editing a dozen helpers, and the one that
 * gets forgotten is a cross-account read that returns real content.
 *
 * So ownership is decided once, here, before any of them run. A request whose
 * session does not belong to it is refused at the door; everything downstream
 * can then keep reading files by name, because by then the name is known to be
 * the caller's. Unfiltered legacy code is acceptable exactly when every session
 * belongs to one person, which is the case this function also checks.
 *
 * Returns false rather than throwing so the caller can answer 404 and reveal
 * nothing about whether the other session exists.
 */
function requireSessionAccess(req, name) {
  if (!auth || !auth.enabled) return true;
  if (!memoryDb) return false;
  if (!name) return false;
  // A bridge reads the same tree it writes into. The conversation is declared on
  // the query string for reads (a GET carries no body), and an unbound one is
  // refused here rather than falling back to the caller's own tree - otherwise a
  // follow-up read of a bound member's turn would 404, or worse, succeed against
  // the owner's same-named session.
  let query = null;
  try { query = new URL(String(req.url || ""), "http://localhost").searchParams; } catch {}
  const bridged = boundChannelScope(query && query.get("channel"), query && query.get("conversation_id"));
  if (bridged === null) return false;
  const scope = bridged === undefined ? sessionScope(req) : bridged;
  if (scope === sessionStore.ANONYMOUS) return false;
  try {
    refreshMemoryDatabase();
    return sessionStore.ownsSession(memoryDb, name, scope);
  } catch {
    return false;
  }
}

// Throttle + reentrancy guard. With BEGIN/COMMIT, WAL and synchronous=NORMAL
// the rebuild is now sub-second even on the largest store, so an inline
// synchronous call is acceptable - and avoiding a Promise here keeps every
// existing caller (the request handler, the trace endpoint, subtask lookups)
// synchronous without a 5-call-site async cascade. The reentrancy flag is the
// singleton: if request A is mid-rebuild, request B skips the work instead of
// starting a second rebuild that races A on the same database file.
let lastMemorySyncAt = 0;
let memorySyncInFlight = false;
function refreshMemoryDatabase() {
  if (!memoryDb || !memorySync) return;
  const now = Date.now();
  if (now - lastMemorySyncAt < 1000) return;
  if (memorySyncInFlight) return;
  memorySyncInFlight = true;
  try {
    const result = memorySync.sync(memoryDb, MEMORY_ROOT, memorySync.buildInventory(MEMORY_ROOT), legacySessionOwnerId());
    lastMemorySyncAt = Date.now();
    if (result.changed) console.log("[memory-db] synced:", JSON.stringify(result.stats));
  } catch (error) {
    console.error("[memory-db] sync failed:", error && error.message ? error.message : error);
  } finally {
    memorySyncInFlight = false;
  }
}

// Say which path is live at startup. Without this, "am I on the database or the
// files" can only be inferred from a sync log that stays silent when nothing
// changes.
if (memoryDb) {
  let count = "?";
  try {
    count = memoryDb.prepare("SELECT COUNT(*) AS n FROM sessions").get().n;
  } catch {}
  console.log(`[memory-db] mode=sqlite  (${count} sessions)  ${path.join(MEMORY_ROOT, "fairy.db")}`);
} else {
  console.log("[memory-db] mode=files");
}
const SEGMENTED_MEMORY_ROOT = path.join(REPO, "memory", "segmented");
// The root of the tree, not one account's slice of it: the per-account
// directories are created on demand by whoever creates a session in them.
if (!fs.existsSync(SESSIONS)) fs.mkdirSync(SESSIONS, { recursive: true });

function readStripped(fp) {
  try { return fs.readFileSync(fp, "utf-8").replace(/^\uFEFF/, ""); }
  catch { return null; }
}

function formatHarnessExitError(code, stderr) {
  const text = String(stderr || "").replace(/\r/g, "").trim();
  if (!text) return "harness exited " + code + ": no stderr";

  const lines = text.split("\n").map(line => line.trimEnd()).filter(Boolean);
  const fatal = lines.filter(line =>
    /^\[harness\] ERROR:/.test(line) ||
    /\bagent loop:/.test(line) ||
    /\bpanic:/.test(line) ||
    /\bfatal error:/.test(line)
  );
  const selected = fatal.length ? fatal.slice(-3) : lines.slice(-16);
  let detail = selected.join("\n");
  if (detail.length > 6000) {
    detail = detail.slice(0, 3000) + "\n...[middle truncated]...\n" + detail.slice(-3000);
  }
  return "harness exited " + code + ": " + detail;
}

function isValidMemoryID(value) {
  const id = String(value || "").trim();
  return !!id && id !== "." && id !== ".." && /^[A-Za-z0-9._-]+$/.test(id);
}

function interactionIndexEntryMatches(entry, interactionId) {
  if (!entry || typeof entry !== "object") return false;
  const entryId = String(entry.interaction_id || entry.turn_id || "");
  if (entryId && entryId === interactionId) return true;
  const target = String(entry.target || "").replace(/\\/g, "/");
  return target.includes("/interactions/" + interactionId + "/") ||
    target.includes("/segments/" + interactionId + "/");
}

function removeInteractionFromSegmentIndexes(interactionId) {
  const indexRoot = path.join(SEGMENTED_MEMORY_ROOT, "index");
  const volumeRoot = path.join(indexRoot, "volumes");
  const files = [path.join(indexRoot, "root.jsonl")];
  try {
    for (const entry of fs.readdirSync(volumeRoot, { withFileTypes: true })) {
      if (entry.isFile() && entry.name.endsWith(".jsonl")) files.push(path.join(volumeRoot, entry.name));
    }
  } catch {}
  let removed = 0;
  for (const file of files) {
    if (!fs.existsSync(file)) continue;
    const raw = readStripped(file);
    if (!raw) continue;
    const kept = [];
    let changed = false;
    for (const line of raw.split(/\r?\n/)) {
      if (!line.trim()) continue;
      try {
        const entry = JSON.parse(line);
        if (interactionIndexEntryMatches(entry, interactionId)) {
          removed++;
          changed = true;
          continue;
        }
      } catch {}
      kept.push(line);
    }
    if (changed) fs.writeFileSync(file, kept.join("\n") + (kept.length ? "\n" : ""), "utf-8");
  }
  return removed;
}

function deleteInteractionMemoryFiles(interactionId) {
  if (!isValidMemoryID(interactionId)) throw new Error("invalid interaction id");
  const sessionsRoot = path.join(SEGMENTED_MEMORY_ROOT, "sessions");
  let deletedSegments = 0;
  let deletedInteractions = 0;
  try {
    for (const dateEntry of fs.readdirSync(sessionsRoot, { withFileTypes: true })) {
      if (!dateEntry.isDirectory()) continue;
      const interactionRoot = path.join(sessionsRoot, dateEntry.name, "interactions");
      const target = path.join(interactionRoot, interactionId);
      if (!fs.existsSync(target)) continue;
      let stat;
      try { stat = fs.statSync(target); } catch { continue; }
      if (!stat.isDirectory()) continue;
      try {
        const files = fs.readdirSync(target, { withFileTypes: true });
        deletedSegments += files.filter(file => file.isFile() && file.name.endsWith(".json") && file.name !== "manifest.json").length;
      } catch {}
      fs.rmSync(target, { recursive: true, force: true });
      deletedInteractions++;
    }
  } catch {}
  // Legacy layout, kept for installations that have not migrated yet.
  const legacy = path.join(SEGMENTED_MEMORY_ROOT, "segments", interactionId);
  if (fs.existsSync(legacy)) {
    try {
      const files = fs.readdirSync(legacy, { withFileTypes: true });
      deletedSegments += files.filter(file => file.isFile() && file.name.endsWith(".json")).length;
    } catch {}
    fs.rmSync(legacy, { recursive: true, force: true });
    deletedInteractions++;
  }
  const removedIndexEntries = removeInteractionFromSegmentIndexes(interactionId);
  return { deletedSegments, deletedInteractions, removedIndexEntries };
}

function removeInteractionFromConversations(interactionId, sessionName = '', turnId = '') {
  let deletedMessages = 0;
  let deletedSessions = 0;
  const explicitTurn = Number.parseInt(String(turnId || ''), 10);
  const legacyTurn = /^\d+$/.test(String(interactionId || '')) ? Number.parseInt(String(interactionId), 10) : 0;
  const targetTurn = legacyTurn > 0 ? legacyTurn : explicitTurn;
  let sessionDirs = [];
  try { sessionDirs = fs.readdirSync(activeSessionsRoot(), { withFileTypes: true }); } catch { return { deletedMessages, deletedSessions }; }
  for (const entry of sessionDirs) {
    if (!entry.isDirectory()) continue;
    if (sessionName && entry.name !== sessionName) continue;
    const sessionFile = path.join(activeSessionsRoot(), entry.name, entry.name + ".json");
    if (!fs.existsSync(sessionFile)) continue;
    let session;
    try { session = JSON.parse(readStripped(sessionFile) || "{}"); } catch { continue; }
    if (!Array.isArray(session.messages)) continue;
    const kept = [];
    let removing = false;
    let changed = false;
    let turnIndex = 0;
    for (const message of session.messages) {
      if (message && message.role === "user") {
        turnIndex++;
        const rawInteractionId = String(message.interaction_id || message.turn_id || "");
        const directMatch = rawInteractionId && rawInteractionId === interactionId;
        const legacyMatch = !rawInteractionId && !!sessionName && targetTurn > 0 && turnIndex === targetTurn;
        removing = directMatch || legacyMatch;
      }
      if (removing) {
        deletedMessages++;
        changed = true;
        continue;
      }
      kept.push(message);
    }
    if (changed) {
      session.messages = kept;
      fs.writeFileSync(sessionFile, JSON.stringify(session, null, 2), "utf-8");
      deletedSessions++;
    }
  }
  return { deletedMessages, deletedSessions };
}

// Attachments travel to the model as a <file_context> prefix on the user
// message. Small text files also carry a bounded preview so the agent can
// decide whether it needs a full file-tool read.
const FILE_CONTEXT_PREVIEW_LIMIT = 12000;
const FILE_CONTEXT_PREVIEW_MAX_BYTES = 256 * 1024;
const FILE_CONTEXT_TEXT_EXT = new Set([
  "txt", "md", "markdown", "csv", "tsv", "json", "jsonl", "log",
  "xml", "yaml", "yml", "html", "htm", "css", "js", "jsx", "ts",
  "tsx", "py", "go", "rs", "java", "rb", "sh", "bash", "ps1", "bat",
]);

function fileContextItem(file) {
  const rawPath = String(file.path || "").trim();
  const item = { path: rawPath };
  try {
    const abs = path.isAbsolute(rawPath) ? rawPath : path.resolve(REPO, rawPath);
    const stat = fs.statSync(abs);
    const ext = path.extname(abs).toLowerCase().replace(/^\./, "");
    item.name = file.name || path.basename(abs);
    item.size = stat.size;
    item.type = ext || "file";
    item.is_full = false;
    if (stat.isFile() && FILE_CONTEXT_TEXT_EXT.has(ext) && stat.size <= FILE_CONTEXT_PREVIEW_MAX_BYTES) {
      const raw = fs.readFileSync(abs, "utf8");
      item.preview = raw.slice(0, FILE_CONTEXT_PREVIEW_LIMIT);
      item.is_full = raw.length <= FILE_CONTEXT_PREVIEW_LIMIT;
    }
  } catch {
    // Keep the path even if the file disappears between upload and send.
  }
  return item;
}

const buildFileContextPrefix = files => {
  const list = Array.isArray(files) ? files : [];
  const sanitized = list
    .filter(f => f && typeof f.path === "string" && f.path.trim())
    .map(fileContextItem);
  let json = "[]";
  try {
    json = JSON.stringify(sanitized)
      .replace(/</g, "\\u003c")
      .replace(/\u2028/g, "\\u2028")
      .replace(/\u2029/g, "\\u2029");
  } catch {
    json = "[]";
  }
  return "<file_context>" + json + "</file_context>\n";
};

function extractReportBody(content) {
  const match = String(content || "").match(/<report\b[^>]*>([\s\S]*?)<\/report>/i);
  return match ? match[1].trim() : "";
}

function saveModelReport(sessionName, modelId, content) {
  const reportBody = String(content || "").trim();
  if (!reportBody) return "";
  const resultDir = path.join(REPO, "workspace", "result");
  fs.mkdirSync(resultDir, { recursive: true });
  const date = new Date().toISOString().slice(0, 10);
  const safeSession = String(sessionName || "session")
    .replace(/[^\w\u4e00-\u9fff-]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 48) || "session";
  const base = `${date}-${safeSession}-report`;
  let target = path.join(resultDir, `${base}.md`);
  for (let index = 2; fs.existsSync(target) && index <= 999; index += 1) {
    target = path.join(resultDir, `${base}-${index}.md`);
  }
  const frontMatter = [
    "---",
    `session: ${sessionName}`,
    `model: ${modelId}`,
    `archived_at: ${new Date().toISOString()}`,
    "source: model_report_tag",
    "---",
    "",
  ].join("\n");
  fs.writeFileSync(target, frontMatter + reportBody + "\n", "utf-8");
  return target;
}


function readTraceLines(fp) {
  if (!fs.existsSync(fp)) return [];
  try {
    return fs.readFileSync(fp, 'utf8').split('\n').filter(Boolean).map(l => {
      try { return JSON.parse(l); } catch { return null; }
    }).filter(Boolean);
  } catch { return []; }
}

const LOCAL_PPT_DECK_ROOT = "/mnt/data/result/";
const LOCAL_PPT_WORKSPACE_ROOT = path.resolve(REPO, "workspace", "result");
const RUNS = path.join(REPO, "runs");

function readJsonSafe(fp, fallback = null) {
  try { return JSON.parse(readStripped(fp)); } catch { return fallback; }
}

function readSessionRecord(name, scope = sessionStore.SINGLE_USER) {
  if (memoryDb) {
    refreshMemoryDatabase();
    return sessionStore.readSessionRecord(memoryDb, name, scope);
  }
  // The file fallback has no ownership filter at all - it opens a path built
  // from the session name alone. When authentication is on, there is therefore
  // no safe version of this branch, and serving the owner's history to any
  // authenticated caller would be worse than serving nothing. Sessions stay
  // reachable through the database; if it is gone, they are unreachable rather
  // than shared.
  if (auth && auth.enabled) return null;
  return readJsonSafe(path.join(activeSessionsRoot(), name, name + '.json'), null);
}

function _tracePreview(value, max = 1200) {
  const text = typeof value === 'string' ? value : JSON.stringify(value || '');
  const clean = String(text || '').replace(/\r/g, '');
  return clean.length > max ? clean.slice(0, max) + '...' : clean;
}

function _runFilesForSession(name, kind) {
  const suffix = kind === 'subtask' ? 'subtask' : 'main';
  const prefix = 'run_' + name + '_' + suffix + '_';
  let names = [];
  try { names = fs.readdirSync(RUNS); } catch { return []; }
  return names
    .filter(file => file.startsWith(prefix) && file.endsWith('.json'))
    .map(file => path.join(RUNS, file))
    .sort((a, b) => {
      try { return fs.statSync(a).mtimeMs - fs.statSync(b).mtimeMs; } catch { return 0; }
    });
}

function _activeMessagesForRun(run) {
  const all = Array.isArray(run && run.messages) ? run.messages : [];
  let start = 0;
  for (let i = all.length - 1; i >= 0; i -= 1) {
    if (all[i] && all[i].role === 'user') { start = i; break; }
  }
  return all.slice(start);
}

function _messageTs(message) {
  const value = Number(message && message.ts);
  return Number.isFinite(value) && value > 0 ? value : 0;
}

function _fallbackTimestamp(active, step, minTs, maxTs, maxStep) {
  const exact = active.find(message => Number(message && message.step) === Number(step) && _messageTs(message));
  if (exact) return _messageTs(exact);
  if (!minTs || !maxTs || maxTs <= minTs) return minTs || maxTs || Date.now();
  const ratio = maxStep > 0 ? Math.max(0, Math.min(1, Number(step || 0) / maxStep)) : 0;
  return Math.round(minTs + ratio * (maxTs - minTs));
}

function _traceEventsFromRun(run, runIndex, full) {
  const active = _activeMessagesForRun(run);
  const rawTrace = Array.isArray(run && run.trace) ? run.trace : [];
  const user = [...active].reverse().find(message => message && message.role === 'user') || null;
  const interactionId = String((user && user.interaction_id) || '');
  const timed = active.map(_messageTs).filter(Boolean);
  const minTs = timed.length ? Math.min(...timed) : 0;
  const maxTs = timed.length ? Math.max(...timed) : 0;
  const maxStep = Math.max(0, ...active.map(message => Number(message && message.step) || 0), ...rawTrace.map(event => Number(event && event.step) || 0));
  const assistant = active.filter(message => message && message.role === 'assistant');
  const tools = active.filter(message => message && message.role === 'tool');
  const assistantsByStep = new Map();
  const toolById = new Map();
  const callTs = new Map();
  for (const message of assistant) {
    const step = Number(message.step) || 0;
    if (!assistantsByStep.has(step)) assistantsByStep.set(step, []);
    assistantsByStep.get(step).push(message);
    for (const call of (message.tool_calls || [])) {
      const id = String((call && call.id) || '');
      if (id) callTs.set(id, _messageTs(message));
    }
  }
  for (const message of tools) {
    const id = String(message.tool_call_id || '');
    if (id) toolById.set(id, message);
  }
  const runId = 'r' + runIndex;
  const out = [];
  let seq = 0;
  const push = event => {
    if (!event) return;
    if (!event.span_id) event.span_id = runId + '-e' + (seq += 1);
    if (!event.timestamp) event.timestamp = _fallbackTimestamp(active, event.step, minTs, maxTs, maxStep);
    if (interactionId && !event.interaction_id) event.interaction_id = interactionId;
    out.push(event);
  };

  if (user) {
    push({ event: 'user_request', step: user.step || 0, timestamp: _messageTs(user) || minTs, content_preview: _tracePreview(user.content), ...(full ? { content_full: String(user.content || '') } : {}) });
  }

  const hasLlmCalls = rawTrace.some(event => event && event.event === 'llm_call');
  if (!hasLlmCalls) {
    for (const message of assistant) {
      const ts = _messageTs(message) || _fallbackTimestamp(active, message.step, minTs, maxTs, maxStep);
      const duration = Number(message.duration_ms) || 0;
      push({
        event: 'llm_call',
        step: message.step || 0,
        timestamp: Math.max(minTs || ts, ts - duration),
        duration_ms: duration || undefined,
        prompt_tokens: message.usage && message.usage.prompt_tokens,
        completion_tokens: message.usage && message.usage.completion_tokens,
      });
    }
  }

  for (const raw of rawTrace) {
    const event = { ...raw };
    const step = Number(event.step) || 0;
    const byStep = assistantsByStep.get(step) || [];
    const message = byStep.shift() || null;
    if (event.event === 'model_response' && message) {
      event.timestamp = _messageTs(message) || event.timestamp;
      event.duration_ms = message.duration_ms || event.duration_ms;
      event.prompt_tokens = (message.usage && message.usage.prompt_tokens) || event.prompt_tokens;
      event.completion_tokens = (message.usage && message.usage.completion_tokens) || event.completion_tokens;
      if (!event.content_preview && message.content) event.content_preview = _tracePreview(message.content);
      if (full && message.content) event.content_full = String(message.content || '');
    } else if (event.event === 'tool_invoked') {
      const ts = callTs.get(String(event.call_id || '')) || _messageTs(message);
      if (ts) event.timestamp = ts;
    } else if (event.event === 'tool_result') {
      const result = toolById.get(String(event.call_id || '')) || null;
      const invokedTs = callTs.get(String(event.call_id || '')) || 0;
      if (result) {
        event.timestamp = _messageTs(result) || event.timestamp;
        if (invokedTs && event.timestamp > invokedTs) event.duration_ms = event.timestamp - invokedTs;
        if (!event.content_preview && result.content) event.content_preview = _tracePreview(result.content);
        if (full && result.content) event.content_full = String(result.content || '');
      }
    } else if (event.event === 'tool_failed') {
      const result = toolById.get(String(event.call_id || '')) || null;
      if (result) {
        event.timestamp = _messageTs(result) || event.timestamp;
        if (full && result.content) event.content_full = String(result.content || '');
      }
    }
    if (event.event === 'loop_start' && minTs) event.timestamp = minTs;
    if (event.event === 'loop_end' && maxTs) event.timestamp = maxTs;
    push(event);
  }

  if (!rawTrace.length) {
    for (const message of assistant) {
      const ts = _messageTs(message) || _fallbackTimestamp(active, message.step, minTs, maxTs, maxStep);
      const duration = Number(message.duration_ms) || 0;
      push({
        event: 'llm_call', step: message.step || 0, timestamp: Math.max(minTs || ts, ts - duration),
        duration_ms: duration || undefined,
        prompt_tokens: message.usage && message.usage.prompt_tokens,
        completion_tokens: message.usage && message.usage.completion_tokens,
      });
      push({
        event: 'model_response', step: message.step || 0, timestamp: ts,
        duration_ms: duration || undefined,
        prompt_tokens: message.usage && message.usage.prompt_tokens,
        completion_tokens: message.usage && message.usage.completion_tokens,
        content_preview: _tracePreview(message.content),
        ...(full ? { content_full: String(message.content || '') } : {}),
      });
      for (const call of (message.tool_calls || [])) {
        const fn = call.function || {};
        push({
          event: 'tool_invoked', step: message.step || 0, timestamp: ts,
          tool: fn.name || call.name || '', call_id: call.id || '', args_raw: fn.arguments || '',
        });
      }
    }
    for (const result of tools) {
      const invokedTs = callTs.get(String(result.tool_call_id || '')) || 0;
      push({
        event: 'tool_result', step: result.step || 0,
        timestamp: _messageTs(result) || _fallbackTimestamp(active, result.step, minTs, maxTs, maxStep),
        duration_ms: invokedTs && _messageTs(result) > invokedTs ? _messageTs(result) - invokedTs : undefined,
        tool: result.name || '', call_id: result.tool_call_id || '',
        content_preview: _tracePreview(result.content),
        ...(full ? { content_full: String(result.content || '') } : {}),
      });
    }
  }

  return out.sort((a, b) => (a.timestamp || 0) - (b.timestamp || 0));
}

function _legacyEventLogTrace(name, full) {
  const fp = path.join(activeSessionsRoot(), name, 'events.jsonl');
  if (!fs.existsSync(fp)) return [];
  const out = [];
  try {
    for (const [index, line] of fs.readFileSync(fp, 'utf8').split('\n').entries()) {
      if (!line.trim()) continue;
      let raw;
      try { raw = JSON.parse(line); } catch { continue; }
      const type = String(raw.type || '');
      let event = type;
      if (type === 'session_start') event = 'loop_start';
      else if (type === 'message') event = raw.role === 'user' ? 'user_request' : 'model_response';
      else if (type === 'summary') event = 'compress';
      else if (type === 'turn_end') event = 'loop_end';
      const item = {
        event,
        step: raw.step || 0,
        timestamp: Number(raw.ts) || 0,
        span_id: 'log-' + index,
        content_preview: raw.content ? _tracePreview(raw.content) : '',
        tool: raw.tool || '',
        call_id: raw.call_id || '',
      };
      if (full && raw.content) item.content_full = String(raw.content || '');
      out.push(item);
    }
  } catch { return []; }
  return out;
}

function _withUsageTraceFallback(name, events) {
  if (!name || !Array.isArray(events) || !events.length) return events;
  const usage = readJsonSafe(path.join(activeSessionsRoot(), name, 'usage.json'), null);
  const turns = usage && Array.isArray(usage.turns) ? usage.turns : [];
  if (!turns.length) return events;

  // Old trace files predate per-call usage fields. Pair usage turns with
  // llm_call events by step so historical traces show the same non-zero token
  // and timing data as the session header, without rewriting trace.jsonl.
  const callsByStep = new Map();
  for (const event of events) {
    if (!event || event.event !== 'llm_call') continue;
    const step = String(Number(event.step) || 0);
    if (!callsByStep.has(step)) callsByStep.set(step, []);
    callsByStep.get(step).push(event);
  }
  for (const turn of turns) {
    if (!turn) continue;
    const queue = callsByStep.get(String(Number(turn.step) || 0));
    const event = queue && queue.shift();
    if (!event) continue;
    if (!(Number(event.prompt_tokens) > 0)) event.prompt_tokens = Number(turn.prompt_tokens) || 0;
    if (!(Number(event.completion_tokens) > 0)) event.completion_tokens = Number(turn.completion_tokens) || 0;
    if (!(Number(event.duration_ms) > 0)) event.duration_ms = Number(turn.duration_ms) || 0;
  }
  return events;
}

function _traceEventsForSession(name, full) {
  const dir = path.join(activeSessionsRoot(), name);
  const legacy = readTraceLines(path.join(dir, name + '.trace.jsonl'));
  if (legacy.length) {
    return _withUsageTraceFallback(name, legacy.map((event, index) => ({
      ...event,
      span_id: event.span_id || ('legacy-' + index),
      timestamp: event.timestamp || Number(event.ts) || 0,
    })));
  }
  const session = readSessionRecord(name) || {};
  const kind = (session.kind === 'branch' || session.parent_session) ? 'subtask' : 'main';
  const runFiles = _runFilesForSession(name, kind);
  if (runFiles.length) {
    const events = [];
    runFiles.forEach((file, index) => {
      const run = readJsonSafe(file, null);
      if (run) events.push(..._traceEventsFromRun(run, index, full));
    });
    return _withUsageTraceFallback(name, events.sort((a, b) => (a.timestamp || 0) - (b.timestamp || 0)));
  }
  return _withUsageTraceFallback(name, _legacyEventLogTrace(name, full));
}

function _traceEventsForSessionTree(name, full = false) {
  const events = _traceEventsForSession(name, full).map(event => ({ ...event, _source: 'main' }));
  const seenSubtasks = new Set();
  const subDir = path.join(activeSessionsRoot(), name, 'subtasks');
  try {
    if (fs.existsSync(subDir)) {
      for (const file of fs.readdirSync(subDir)) {
        if (!file.endsWith('.trace.jsonl')) continue;
        const base = file.slice(0, -'.trace.jsonl'.length);
        const nested = readTraceLines(path.join(subDir, file)).map((event, index) => ({
          ...event,
          span_id: event.span_id || ('nested-' + index),
          timestamp: event.timestamp || Number(event.ts) || 0,
          _source: base,
          _sub: true,
        }));
        events.push(...nested);
        seenSubtasks.add(base);
      }
    }
  } catch {}
  for (const subtask of _subtaskSessionsForParent(name)) {
    if (seenSubtasks.has(subtask.name)) continue;
    events.push(..._traceEventsForSession(subtask.name, full).map(event => ({
      ...event,
      _source: subtask.name,
      _sub: true,
    })));
  }
  return events;
}

function _traceTotalsForSessionTree(name) {
  const totals = { prompt_tokens: 0, completion_tokens: 0, llm_calls: 0 };
  for (const event of _traceEventsForSessionTree(name, false)) {
    if (!event || event.event !== 'llm_call') continue;
    totals.prompt_tokens += Number(event.prompt_tokens) || 0;
    totals.completion_tokens += Number(event.completion_tokens) || 0;
    totals.llm_calls += 1;
  }
  return totals;
}

function usageWithMainTraceSpan(name, usage) {
  if (!usage) return usage;
  const turns = Array.isArray(usage.turns) ? usage.turns : [];
  const turnsPromptTokens = turns.reduce((sum, turn) => sum + (Number(turn && turn.prompt_tokens) || 0), 0);
  const turnsCompletionTokens = turns.reduce((sum, turn) => sum + (Number(turn && turn.completion_tokens) || 0), 0);
  const storedRealMs = Number(usage.real_ms) || 0;
  // usage.json is the durable per-turn ledger. When it already has turn data
  // (or a persisted real_ms), rebuilding the whole trace tree here is pure
  // duplicate work: it rereads the main session, every child session and the
  // event log on every pagination request. Keep the trace fallback only for
  // genuinely legacy usage files that have neither.
  if (turns.length > 0 || storedRealMs > 0) {
    const { duration_ms: _legacyDurationMs, ...tokenUsage } = usage;
    return {
      ...tokenUsage,
      prompt_tokens: turnsPromptTokens || (Number(usage.prompt_tokens) || 0),
      completion_tokens: turnsCompletionTokens || (Number(usage.completion_tokens) || 0),
      real_ms: storedRealMs,
      trace_llm_calls: turns.length || (Number(usage.trace_llm_calls) || 0),
    };
  }
  const mainEvents = _traceEventsForSession(name, false);
  const totals = _traceTotalsForSessionTree(name);
  const tracePromptTokens = totals.prompt_tokens || 0;
  const traceCompletionTokens = totals.completion_tokens || 0;
  const promptTokens = turnsPromptTokens || tracePromptTokens || (Number(usage.prompt_tokens) || 0);
  const completionTokens = turnsCompletionTokens || traceCompletionTokens || (Number(usage.completion_tokens) || 0);
  const { duration_ms: _legacyDurationMs, ...tokenUsage } = usage;
  return {
    ...tokenUsage,
    prompt_tokens: promptTokens,
    completion_tokens: completionTokens,
    real_ms: activeTraceSpanMs(mainEvents),
    trace_llm_calls: totals.llm_calls,
  };
}

function _subtaskSessionsForParent(parent) {
  const out = [];
  let dirs = [];
  try { dirs = fs.readdirSync(activeSessionsRoot()); } catch { return out; }
  for (const name of dirs) {
    if (name === parent) continue;
    const record = readSessionRecord(name);
    if (!record || record.parent_session !== parent) continue;
    let mtime = 0;
    try { mtime = fs.statSync(path.join(activeSessionsRoot(), name, name + '.json')).mtimeMs; } catch {}
    out.push({ name, title: record.domain || name, mtime });
  }
  return out.sort((a, b) => a.mtime - b.mtime);
}

function _chatDeckRel(sessionDir, fileName) {
  const data = readJsonSafe(path.join(sessionDir, fileName + '.json'), null);
  if (!data) return '';
  const text = (data.messages || []).map(message => String(message.content || '')).join('\n');
  const match = text.match(/<deck_dir>([^<]*)<\/deck_dir>/);
  if (!match) return '';
  const raw = match[1].trim().replace(/\\/g, '/');
  const rel = raw.match(/(?:\/mnt\/data\/result\/|workspace\/result\/)(.+)$/i);
  return rel ? rel[1].replace(/^\/+|\/+$/g, '') : '';
}

function _deckCountsForTrace(deckRel) {
  if (!deckRel) return [0, 0];
  const deckDir = path.join(LOCAL_PPT_WORKSPACE_ROOT, deckRel);
  const count = (dir, re) => {
    try { return fs.readdirSync(dir).filter(file => re.test(file)).length; } catch { return 0; }
  };
  const htmls = count(path.join(deckDir, 'htmls'), /\.html$/i);
  const pngs = count(path.join(deckDir, 'pages'), /\.(?:png|jpe?g|webp)$/i);
  return [htmls, pngs];
}

function _tracePreviewUrl(deckRel, htmls, pngs) {
  if (!deckRel || htmls + pngs === 0) return '';
  return '/api/ppt-preview?deck_dir=' + encodeURIComponent(LOCAL_PPT_DECK_ROOT + deckRel);
}

function _traceMainMeta(events, subtasks, messages) {
  const toolFailures = (events || []).filter(event => event && (event.event === 'tool_failed' || (event.event === 'tool_result' && event.ok === false))).length;
  const gateFailures = (events || []).filter(event => {
    if (!event || event.event !== 'tool_result') return false;
    const text = String(event.content_preview || '') + String(event.content_full || '');
    return /"status"\s*:\s*"(?:fail|error)"|"errors"\s*:\s*\[[^\]]/i.test(text);
  }).length;
  const soft = (events || []).filter(event => event && (event.event === 'soft_step_limit' || event.event === 'soft_limit_hint')).length;
  const hard = (events || []).filter(event => event && (event.event === 'hard_limit_hit' || event.event === 'tool_capped')).length;
  const spawns = (messages || []).reduce((total, message) => total + ((message && message.tool_calls) ? message.tool_calls.filter(call => call && call.function && call.function.name === 'create_subtask').length : 0), 0);
  return { tool_failures: toolFailures, gate_failures: gateFailures, soft_hint: soft, hard_hint: hard, repeats: 0, subtask_spawns: spawns || (subtasks || []).length };
}

const DECK_SCORE_JOBS = new Map();

function readDeckScore(deckRel) {
  if (!deckRel || !/^[A-Za-z0-9._-]+(?:\/[A-Za-z0-9._-]+)*$/.test(String(deckRel))) return null;
  return readJsonSafe(path.join(LOCAL_PPT_WORKSPACE_ROOT, deckRel, 'deck_score.json'), null);
}

function resolveDeckRelForSession(name) {
  const record = readSessionRecord(name);
  if (record && record.parent_session) return '';
  return _chatDeckRel(path.join(activeSessionsRoot(), name), name);
}

function aiReportsFile(name) {
  return path.join(activeSessionsRoot(), name, 'ai_reports.json');
}

function readAiReports(name) {
  const value = readJsonSafe(aiReportsFile(name), []);
  return Array.isArray(value) ? value : [];
}

function writeAiReports(name, list) {
  const fp = aiReportsFile(name);
  fs.mkdirSync(path.dirname(fp), { recursive: true });
  fs.writeFileSync(fp, JSON.stringify((list || []).slice(0, 20), null, 2), 'utf-8');
  return true;
}

function appendAiReport(name, report) {
  if (!name || !report) return false;
  const list = readAiReports(name).filter(item => String(item && item.at || '') !== String(report.at || ''));
  list.unshift(report);
  return writeAiReports(name, list);
}

function deleteAiReport(name, at) {
  return writeAiReports(name, readAiReports(name).filter(item => String(item && item.at || '') !== String(at || '')));
}

function resolveApiKeyValue(raw) {
  const value = String(raw || '').trim();
  if (!value) return '';
  if (!value.startsWith('READ_FROM_')) return value;
  for (const candidate of apiKeyCandidates(value)) {
    const text = readStripped(path.join(REPO, candidate));
    if (!text) continue;
    const first = text.split(/\r?\n/).map(line => line.trim()).find(line => line && !line.startsWith('#'));
    if (first && !first.startsWith('READ_FROM_')) return first;
  }
  return '';
}

function llmChatText(system, user, timeoutMs = 240000) {
  const cfg = JSON.parse(readStripped(APP_CONFIG) || '{}');
  let selected = null;
  for (const entry of Object.values(cfg.models || {})) {
    const api = (entry && entry.api) || {};
    const key = resolveApiKeyValue(api.api_key);
    if (api.base_url && key) { selected = { api, key }; break; }
  }
  if (!selected) throw new Error('LLM 未配置可用 API key');
  const base = String(selected.api.base_url || '').replace(/\/+$/, '');
  const url = base.endsWith('/chat/completions') ? base : base + '/chat/completions';
  const body = JSON.stringify({
    model: selected.api.model || '',
    messages: [{ role: 'system', content: system }, { role: 'user', content: user }],
    temperature: 0.3,
    max_tokens: Number(selected.api.max_tokens) || 2000,
    stream: false,
  });
  const lib = url.startsWith('https:') ? require('https') : require('http');
  const target = new URL(url);
  return new Promise((resolve, reject) => {
    const req = lib.request({
      hostname: target.hostname,
      port: target.port || (target.protocol === 'https:' ? 443 : 80),
      path: target.pathname + target.search,
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: 'Bearer ' + selected.key,
        'Content-Length': Buffer.byteLength(body),
      },
    }, res => {
      let buf = '';
      res.setEncoding('utf-8');
      res.on('data', chunk => { buf += chunk; });
      res.on('end', () => {
        if (res.statusCode < 200 || res.statusCode >= 300) {
          reject(new Error('LLM HTTP ' + res.statusCode + ': ' + buf.slice(0, 300)));
          return;
        }
        try {
          const data = JSON.parse(buf);
          const message = (((data.choices || [])[0] || {}).message) || {};
          resolve(String(message.content || ''));
        } catch (error) { reject(new Error('LLM 返回解析失败: ' + error.message)); }
      });
    });
    req.setTimeout(timeoutMs, () => req.destroy(new Error('LLM 调用超时')));
    req.on('error', error => reject(new Error('LLM 调用失败: ' + error.message)));
    req.write(body);
    req.end();
  });
}

function normalizePptDeckDir(rawDeckDir) {
  const value = String(rawDeckDir || "").replace(/\\/g, "/").trim();
  const logical = value.match(/^\/mnt\/data\/result\/(pptid_[A-Za-z0-9._-]+)\/?$/i);
  const local = value.match(/^[A-Za-z]:\/.*\/workspace\/result\/(pptid_[A-Za-z0-9._-]+)\/?$/i);
  const deckId = (logical && logical[1]) || (local && local[1]) || "";
  return deckId ? LOCAL_PPT_DECK_ROOT + deckId : "";
}

function buildPptArtifactFromPath(rawDeckDir) {
  const deckDir = normalizePptDeckDir(rawDeckDir);
  if (!deckDir) return null;
  const deckId = resolveDeckId(deckDir, {
    workspaceRoot: LOCAL_PPT_WORKSPACE_ROOT,
    virtualDeckRoot: LOCAL_PPT_DECK_ROOT,
  });
  if (!deckId || !listDeckPages(deckId, LOCAL_PPT_WORKSPACE_ROOT).length) return null;
  return { kind: 'ppt', path: deckDir, name: 'PPT 演示', ext: 'PPT' };
}

function extractPptArtifact(text) {
  const block = String(text || "").match(/<ppt_task_finished(?:\s[^>]*)?>([\s\S]*?)<\/ppt_task_finished>/i);
  if (!block) return null;
  const read = name => {
    const match = block[1].match(new RegExp('<' + name + '(?:\\s[^>]*)?>([\\s\\S]*?)<\\/' + name + '>', 'i'));
    return match ? match[1].trim() : "";
  };
  return buildPptArtifactFromPath(read('deck_dir'));
}

function discoverResultArtifacts(msgs) {
  const artifacts = [];
  let finalAssistantText = '';
  const addFile = absPath => {
    const normalized = String(absPath || '').replace(/\\/g, '/');
    if (!normalized) return;
    artifacts.push({ kind: 'file', path: normalized, name: path.basename(normalized) });
  };

  for (let mi = msgs.length - 1; mi >= 0; mi--) {
    const item = msgs[mi];
    if (!item) continue;
    if (item.role === 'user') break;
    if (item.role === 'tool' && item.name === 'show_result') {
      try {
        const value = typeof item.content === 'string' ? JSON.parse(item.content) : item.content;
        if (value && value.ok !== false && value.path) {
          const pptArtifact = value.kind === 'ppt'
            ? buildPptArtifactFromPath(value.path)
            : null;
          if (pptArtifact) {
            artifacts.push(pptArtifact);
          } else {
            for (const abs of fm.findViewerPaths(String(value.path))) addFile(abs);
          }
        }
      } catch {
        // Fall through to scanning the final assistant text.
      }
    }
    if (item.role === 'assistant' && !finalAssistantText) {
      finalAssistantText = String(item.content || '');
    }
  }

  const pptArtifact = extractPptArtifact(finalAssistantText);
  if (pptArtifact) artifacts.push(pptArtifact);
  for (const abs of fm.findViewerPaths(finalAssistantText)) addFile(abs);

  const seen = new Set();
  return {
    artifacts: artifacts.filter(item => {
      const key = item.path;
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    }),
    finalAssistantText,
  };
}

// Single-config multi-model layout: config/config.json holds a "models" map and
// the selected entry is passed to the agent via -ModelId.
const APP_CONFIG = path.join(REPO, "config", "config.json");
const LOCAL_SECRETS = path.join(REPO, "config", "local_secrets.json");
const DEFAULT_UI_SETTINGS = {
  voice_auto_read: true,
  auto_fallback: true,
  default_model: "",
  fallback_model: "",
  theme: "zzz",
};

function readUISettings() {
  try {
    const cfg = JSON.parse(readStripped(APP_CONFIG) || "{}");
    return { ...DEFAULT_UI_SETTINGS, ...(cfg.settings || {}) };
  } catch {
    return { ...DEFAULT_UI_SETTINGS };
  }
}

function writeUISettings(patch) {
  const cfg = JSON.parse(readStripped(APP_CONFIG) || "{}");
  const current = { ...DEFAULT_UI_SETTINGS, ...(cfg.settings || {}) };
  const next = { ...current };
  if (Object.prototype.hasOwnProperty.call(patch, "voice_auto_read")) {
    next.voice_auto_read = patch.voice_auto_read !== false;
  }
  if (Object.prototype.hasOwnProperty.call(patch, "auto_fallback")) {
    next.auto_fallback = patch.auto_fallback !== false;
  }
  if (Object.prototype.hasOwnProperty.call(patch, "fallback_model")) {
    const fallback = String(patch.fallback_model || "").trim();
    next.fallback_model = fallback && MODELS[fallback] ? fallback : "";
  }
  if (Object.prototype.hasOwnProperty.call(patch, "default_model")) {
    const defaultModel = String(patch.default_model || "").trim();
    next.default_model = defaultModel && MODELS[defaultModel] ? defaultModel : "";
  }
  if (Object.prototype.hasOwnProperty.call(patch, "theme")) {
    const theme = String(patch.theme || "").trim();
    next.theme = ["base", "zzz"].includes(theme) ? theme : "zzz";
  }
  cfg.settings = next;
  const tmp = APP_CONFIG + ".tmp";
  fs.writeFileSync(tmp, JSON.stringify(cfg, null, 2));
  fs.renameSync(tmp, APP_CONFIG);
  return next;
}

function readToolSettings() {
  const cfg = JSON.parse(readStripped(APP_CONFIG) || "{}");
  const mechanisms = [
    {
      id: "reflection",
      name: "Reflection 自检",
      enabled: !(cfg.reflection && cfg.reflection.enabled === false),
      kind: "mechanism",
    },
    {
      id: "memory_summarize",
      name: "长期记忆总结",
      enabled: cfg.memory_summarize !== false,
      kind: "mechanism",
    },
    {
      id: "generate_title",
      name: "生成对话标题",
      enabled: cfg.generate_title !== false,
      kind: "mechanism",
    },
    {
      id: "tool_score_cleanup",
      name: "JEV 工具结果清理",
      enabled: !(cfg.tool_score_cleanup && cfg.tool_score_cleanup.enabled === false),
      kind: "mechanism",
    },
  ];
  const tools = Object.entries(cfg.httpTools || {}).map(([name, value]) => ({
    id: "http:" + name,
    name,
    enabled: !(value && value.enabled === false),
    kind: "tool",
  }));
  return [...mechanisms, ...tools].sort((a, b) => {
    if (a.kind !== b.kind) return a.kind === "mechanism" ? -1 : 1;
    return a.name.localeCompare(b.name);
  });
}

function writeToolSetting(id, enabled) {
  const key = String(id || "").trim();
  if (!key) throw new Error("tool name is required");
  const cfg = JSON.parse(readStripped(APP_CONFIG) || "{}");
  if (key === "reflection") {
    cfg.reflection = { ...(cfg.reflection || {}), enabled: enabled !== false };
  } else if (key === "memory_summarize") {
    cfg.memory_summarize = enabled !== false;
  } else if (key === "generate_title") {
    cfg.generate_title = enabled !== false;
  } else if (key === "tool_score_cleanup") {
    cfg.tool_score_cleanup = { ...(cfg.tool_score_cleanup || {}), enabled: enabled !== false };
  } else if (key.startsWith("http:")) {
    const name = key.slice("http:".length);
    if (!cfg.httpTools || !cfg.httpTools[name]) throw new Error("unknown tool: " + name);
    const current = cfg.httpTools[name] && typeof cfg.httpTools[name] === "object"
      ? cfg.httpTools[name]
      : {};
    cfg.httpTools[name] = { ...current, enabled: enabled !== false };
  } else {
    throw new Error("unknown tool setting: " + key);
  }
  const tmp = APP_CONFIG + ".tmp";
  fs.writeFileSync(tmp, JSON.stringify(cfg, null, 2));
  fs.renameSync(tmp, APP_CONFIG);
  return { id: key, enabled: enabled !== false };
}

function parseSkillDescription(skillFile) {
  let text = "";
  try { text = fs.readFileSync(skillFile, "utf-8"); } catch { return ""; }
  const lines = text.split(/\r?\n/).map(line => line.trim()).filter(Boolean);
  const description = lines.find(line => /^description\s*:/i.test(line));
  if (description) return description.replace(/^description\s*:\s*/i, "").replace(/^["']|["']$/g, "").slice(0, 300);
  const paragraph = lines.find(line => !line.startsWith("#"));
  return paragraph ? paragraph.slice(0, 300) : "";
}

// 与 Go 侧 agent/skillroots.go 的 userLevelSkillRoots() 保持一致。
// 只扫仓库 skills/ 的话，装在用户级根里的技能在设置里就没有开关——
// 装完看着像没装，这是同一个根因在两个地方各表现一次。
function userLevelSkillRoots() {
  let home = "";
  try { home = require("os").homedir() || process.env.HOME || ""; } catch {}
  if (!home) return [];
  return [
    path.join(home, ".fairy", "skills"),
    path.join(home, ".agents", "skills"),
    path.join(home, ".claude", "skills"),
  ];
}

function skillRoots(cfg) {
  return [path.join(REPO, (cfg && cfg.skills_dir) || "skills"), ...userLevelSkillRoots()];
}

function findSkillFile(roots, name) {
  for (const root of roots) {
    const candidate = path.join(root, name, "SKILL.md");
    if (fs.existsSync(candidate)) return candidate;
  }
  return null;
}

// config.json 会被提交到 git，用户级技能位置必须写成 ~ 相对，
// 否则换台机器就指向一个不存在的绝对路径。
function homeRelative(p) {
  let home = "";
  try { home = require("os").homedir() || process.env.HOME || ""; } catch {}
  if (home && p.startsWith(home + path.sep)) return "~" + p.slice(home.length);
  return p;
}

function readSkillSettings() {
  const cfg = JSON.parse(readStripped(APP_CONFIG) || "{}");
  const configured = new Map();
  for (const skill of Array.isArray(cfg.skills) ? cfg.skills : []) {
    if (!skill || !skill.name) continue;
    configured.set(skill.name, {
      name: skill.name,
      description: skill.description || "",
      location: skill.location || ("/skills/" + skill.name),
      enabled: skill.enabled !== false,
      registered: true,
    });
  }
  for (const root of skillRoots(cfg)) {
    let entries = [];
    try { entries = fs.readdirSync(root, { withFileTypes: true }); } catch { continue; }
    for (const entry of entries) {
      if (!entry.isDirectory()) continue;
      const name = entry.name;
      const skillFile = path.join(root, name, "SKILL.md");
      if (!fs.existsSync(skillFile)) continue;
      const prev = configured.get(name) || {};
      // 已注册的沿用原 location，未注册的按实际所在根给出真实路径，
      // 否则前端拿到的 location 会指向不存在的 /skills/<name>。
      const location = prev.registered && prev.location
        ? prev.location
        : (root.startsWith(REPO) ? "/skills/" + name : homeRelative(root) + "/" + name);
      configured.set(name, {
        name,
        description: prev.description || parseSkillDescription(skillFile),
        location,
        enabled: prev.enabled !== false,
        registered: !!prev.registered,
      });
    }
  }
  return [...configured.values()].sort((a, b) => a.name.localeCompare(b.name));
}

function writeSkillSetting(name, enabled) {
  const key = String(name || "").trim();
  if (!key) throw new Error("skill name is required");
  const cfg = JSON.parse(readStripped(APP_CONFIG) || "{}");
  const skills = Array.isArray(cfg.skills) ? [...cfg.skills] : [];
  const index = skills.findIndex(skill => skill && skill.name === key);
  if (index >= 0) {
    skills[index] = { ...skills[index], enabled: enabled !== false };
  } else {
    // 技能可能装在用户级根（~/.fairy/skills 等），不能只认仓库路径，
    // 否则那些技能在设置里看得见、开关却报错。
    const skillFile = findSkillFile(skillRoots(cfg), key);
    if (!skillFile) throw new Error("unknown skill: " + key);
    const dir = path.dirname(skillFile);
    const location = dir.startsWith(REPO) ? "/skills/" + key : homeRelative(dir);
    skills.push({ name: key, description: parseSkillDescription(skillFile), location, enabled: enabled !== false });
  }
  cfg.skills = skills;
  const tmp = APP_CONFIG + ".tmp";
  fs.writeFileSync(tmp, JSON.stringify(cfg, null, 2));
  fs.renameSync(tmp, APP_CONFIG);
  return { name: key, enabled: enabled !== false };
}

function addSkillSetting(payload) {
  const name = String(payload.name || "").trim();
  const location = String(payload.location || "").trim();
  if (!name || !/^[A-Za-z0-9_.-]{1,64}$/.test(name)) throw new Error("skill name must use letters, numbers, _ . -");
  if (!location) throw new Error("skill location is required");
  const resolved = path.isAbsolute(location) ? location : path.join(REPO, location);
  const skillFile = resolved.endsWith("SKILL.md") ? resolved : path.join(resolved, "SKILL.md");
  if (!fs.existsSync(skillFile)) throw new Error("SKILL.md not found: " + skillFile);
  const sourceDir = path.dirname(skillFile);
  const skillsRoot = path.join(REPO, "skills");
  const destDir = path.join(skillsRoot, name);
  const sourceInsideRoot = path.resolve(sourceDir).toLowerCase().startsWith(path.resolve(skillsRoot).toLowerCase() + path.sep);
  if (!sourceInsideRoot) {
    if (fs.existsSync(destDir)) throw new Error("skill already exists in repository: " + name);
    fs.cpSync(sourceDir, destDir, { recursive: true });
  }
  const cfg = JSON.parse(readStripped(APP_CONFIG) || "{}");
  const skills = Array.isArray(cfg.skills) ? [...cfg.skills] : [];
  const entry = {
    name,
    description: String(payload.description || "").trim() || parseSkillDescription(skillFile),
    location: String(payload.logical_location || "").trim() || ("/skills/" + name),
    enabled: payload.enabled !== false,
  };
  const index = skills.findIndex(skill => skill && skill.name === name);
  if (index >= 0) skills[index] = entry;
  else skills.push(entry);
  cfg.skills = skills;
  const tmp = APP_CONFIG + ".tmp";
  fs.writeFileSync(tmp, JSON.stringify(cfg, null, 2));
  fs.renameSync(tmp, APP_CONFIG);
  return entry;
}

function readLocalSecrets() {
  try {
    const parsed = JSON.parse(readStripped(LOCAL_SECRETS) || "{}");
    return parsed && typeof parsed === "object" ? parsed : {};
  } catch {
    return {};
  }
}

function writeLocalSecrets(secrets) {
  const tmp = LOCAL_SECRETS + ".tmp";
  fs.writeFileSync(tmp, JSON.stringify(secrets || {}, null, 2), { mode: 0o600 });
  fs.renameSync(tmp, LOCAL_SECRETS);
}

function localSecretRef(id) {
  return "READ_FROM_LOCAL_SECRETS:" + String(id || "").trim();
}

function localSecretID(raw) {
  const value = String(raw || "").trim();
  return value.startsWith("READ_FROM_LOCAL_SECRETS:")
    ? value.slice("READ_FROM_LOCAL_SECRETS:".length)
    : "";
}

function apiKeyCandidates(key) {
  const raw = String(key || '').trim();
  if (!raw.startsWith('READ_FROM_')) return [];
  const suffix = raw.slice('READ_FROM_'.length);
  const candidates = [suffix + '.txt'];
  if (suffix.endsWith('_KEY_TXT')) {
    const base = suffix.slice(0, -'_KEY_TXT'.length);
    candidates.push(base + '_key.txt', base + '.txt');
  }
  return candidates;
}

function hasResolvedApiKey(key) {
  const raw = String(key || '').trim();
  if (!raw) return false;
  const secretID = localSecretID(raw);
  if (secretID) return !!String(readLocalSecrets()[secretID] || '').trim();
  if (!raw.startsWith('READ_FROM_')) return true;
  for (const name of apiKeyCandidates(raw)) {
    const text = readStripped(path.join(REPO, name));
    if (!text) continue;
    const first = text.split(/\r?\n/).map(line => line.trim()).find(line => line && !line.startsWith('#'));
    if (first && !first.startsWith('READ_FROM_')) return true;
  }
  return false;
}

// Some providers expose models through /chat/completions before they appear in
// their OpenAI-compatible /models response. Keep those known aliases merged so
// the settings picker can surface a model that is actually callable.
function knownProviderModels(baseUrl) {
  try {
    const host = new URL(String(baseUrl || "")).hostname.toLowerCase();
    if (host === "api.minimaxi.com" || host.endsWith(".minimaxi.com")) {
      return ["MiniMax-M3.1-Flash-Preview"];
    }
  } catch {}
  return [];
}

function loadModelRegistry(configPath) {
  const registry = {};
  const secrets = readLocalSecrets();
  try {
    const cfg = JSON.parse(readStripped(configPath) || "{}");
    for (const [id, m] of Object.entries(cfg.models || {})) {
      const api = (m && m.api) || {};
      const rawKey = String(api.api_key || "");
      const secretID = localSecretID(rawKey);
      const apiKey = secretID ? String(secrets[secretID] || "") : rawKey;
      const user = !!(m && m.user) || /^user_/.test(id);
      registry[id] = {
        display: (m && m.display) || id,
        provider: (m && m.provider) || "",
        kind: "real",
        config: configPath,
        modelId: id,
        apiModel: api.model || "",
        override: user ? {
          base_url: api.base_url || "",
          timeout_sec: api.timeout_sec || 120,
          temperature: api.temperature == null ? 0.4 : api.temperature,
          max_tokens: api.max_tokens || 16384,
        } : null,
        apiKey,
        user,
      };
    }
  } catch {}
  return registry;
}

const MODELS = loadModelRegistry(APP_CONFIG);

function saveModelRegistry() {
  let cfg = {};
  try { cfg = JSON.parse(readStripped(APP_CONFIG) || "{}"); } catch {}
  const existing = cfg.models && typeof cfg.models === "object" ? cfg.models : {};
  const out = { ...existing };
  const secrets = readLocalSecrets();
  for (const [id, v] of Object.entries(MODELS)) {
    if (!v.user) {
      if (existing[id]) out[id] = existing[id];
      continue;
    }
    const override = v.override || {};
    out[id] = {
      display: v.display || id,
      provider: v.provider || "",
      user: true,
      api: {
        base_url: override.base_url || '',
        api_key: localSecretRef(id),
        model: v.apiModel || id,
        timeout_sec: override.timeout_sec || 120,
        temperature: override.temperature == null ? 0.4 : override.temperature,
        max_tokens: override.max_tokens || 16384,
      },
    };
    if (v.apiKey) secrets[id] = v.apiKey;
  }
  for (const [id, model] of Object.entries(existing)) {
    if (model && model.user && !MODELS[id]) {
      delete out[id];
      delete secrets[id];
    }
  }
  cfg.models = out;
  const tmp = APP_CONFIG + ".tmp";
  fs.writeFileSync(tmp, JSON.stringify(cfg, null, 2));
  fs.renameSync(tmp, APP_CONFIG);
  writeLocalSecrets(secrets);
}

function reloadModelRegistry() {
  const fresh = loadModelRegistry(APP_CONFIG);
  for (const k of Object.keys(MODELS)) delete MODELS[k];
  Object.assign(MODELS, fresh);
  const nextMap = {};
  for (const [id, v] of Object.entries(MODELS)) {
    if (v.apiModel) nextMap[v.apiModel] = id;
  }
  Object.keys(apiModelToUi).forEach(key => delete apiModelToUi[key]);
  Object.assign(apiModelToUi, nextMap);
}

function prepareModelConfigPath(model) {
  if (!model || (!model.override && !model.user)) return model && model.config;
  const cfg = JSON.parse(readStripped(model.config) || "{}");
  if (!cfg.models || !cfg.models[model.modelId] || !cfg.models[model.modelId].api) {
    throw new Error("model config is unavailable: " + model.modelId);
  }
  const api = cfg.models[model.modelId].api;
  const override = model.override;
  if (typeof override === "string" && override) {
    api.model = override;
  } else if (override && typeof override === "object") {
    if (override.model) api.model = override.model;
    if (override.base_url) api.base_url = override.base_url;
    if (override.timeout_sec) api.timeout_sec = override.timeout_sec;
    if (override.temperature != null) api.temperature = override.temperature;
    if (override.max_tokens) api.max_tokens = override.max_tokens;
  }
  if (model.user && model.apiKey) api.api_key = model.apiKey;
  const tmp = path.join(require("os").tmpdir(), "cfg_" + Date.now() + "_" + process.pid + ".json");
  fs.writeFileSync(tmp, JSON.stringify(cfg, null, 2));
  return tmp;
}

// Reverse map: actual API model name -> frontend model id (sessions store the
// API model name written by the agent, e.g. "gateway_qn_claude_opus_47").
const apiModelToUi = {};
for (const [id, v] of Object.entries(MODELS)) {
  if (v.apiModel) apiModelToUi[v.apiModel] = id;
}


function sanitizeMsgId(id) {
  const s = String(id || '').trim();
  return /^[A-Za-z0-9_.-]+$/.test(s) ? s : '';
}
function listSessionAudio(name) {
  const out = {};
  try {
    const dir = path.join(activeSessionsRoot(), name, 'audio');
    if (!fs.existsSync(dir)) return out;
    for (const f of fs.readdirSync(dir)) {
      const m = /^msg-(.+)\.wav$/.exec(f);
      if (!m) continue;
      try {
        const buf = fs.readFileSync(path.join(dir, f));
        const sr = buf.readUInt32LE(24) || 48000;
        const ch = buf.readUInt16LE(22) || 2;
        const dataSize = buf.readUInt32LE(40) || 0;
        const duration = dataSize / (sr * ch * 2);
        out[m[1]] = {
          url: '/api/sessions/' + encodeURIComponent(name) + '/audio/' + encodeURIComponent(m[1]),
          duration_sec: Math.round(duration * 100) / 100,
        };
      } catch {}
    }
  } catch {}
  return out;
}

function defaultSessionName() {
  const d = new Date();
  const parts = new Intl.DateTimeFormat("en-US", {
    timeZone: "Asia/Shanghai",
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).formatToParts(d);
  const value = type => (parts.find(part => part.type === type) || {}).value || "";
  return value("year") + "-" + value("month") + "-" + value("day");
}

function cleanSessionPreviewText(value) {
  // One implementation, shared with the database reader so the two cannot drift.
  return sessionStore.cleanSessionPreviewText(value);
}

function listSessions(scope = sessionStore.SINGLE_USER) {
  if (memoryDb) {
    refreshMemoryDatabase();
    return sessionStore.listSessions(memoryDb, scope);
  }
  // Same reasoning as readSessionRecord: this branch walks every directory
  // under SESSIONS with no notion of who owns what, so it is only correct while
  // there is exactly one user who owns all of them.
  if (auth && auth.enabled) return [];
  const items = [];
  try {
    const dirs = fs.readdirSync(activeSessionsRoot()).filter(f => {
      try { return fs.statSync(path.join(activeSessionsRoot(), f)).isDirectory(); } catch { return false; }
    });
    for (const d of dirs) {
      try {
        const fp = path.join(activeSessionsRoot(), d, d + '.json');
        if (!fs.existsSync(fp)) continue;
        const stat = fs.statSync(fp);
        const data = JSON.parse(readStripped(fp));
        const msgs = data.messages || [];
        const firstUser = msgs.find(m => m.role === 'user');
        let preview = (firstUser && firstUser.content) || '';
        preview = cleanSessionPreviewText(preview).slice(0, 60);
        items.push({
          name: d,
          modified: stat.mtime.toISOString().replace('T', ' ').slice(0, 19),
          message_count: msgs.length,
          preview,
          model: data.model || null,
          kind: data.kind || 'main',
          parent_session: data.parent_session || null,
          domain: data.domain || null,
          created_by: data.created_by || null,
          created_at: data.created_at || null,
          daily_date: data.daily_date || null,
          channel: data.channel || null,
        });
      } catch {}
    }
    items.sort((a, b) => String(b.modified).localeCompare(String(a.modified)));
  } catch {}
  return items;
}
/**
 * Read the messages array for a session. Prefers the database when it is
 * available: the file path was reading a multi-MB JSON and JSON.parse()-ing
 * the whole blob on every chat open, which was the dominant cost of opening
 * any session past a few hundred messages. The DB reads only the messages
 * rows it needs and skips the whole-session parse entirely.
 *
 * The two post-processors (patchMissingSubtaskResults / enrichSubtaskStats)
 * are still applied in both branches because they read the subtask session
 * files from disk, which is independent of where the parent messages came
 * from. They run on the returned array in place, so call-site behaviour is
 * identical to the file-only path.
 */
function getSession(name, scope = sessionStore.SINGLE_USER) {
  if (memoryDb) {
    refreshMemoryDatabase();
    const dbMessages = sessionStore.readSessionMessages(memoryDb, name, scope);
    if (dbMessages === null) return [];
    const patched = patchMissingSubtaskResults(name, dbMessages);
    return enrichSubtaskStats(name, patched);
  }
  // No ownership filter exists on this branch, so it is only correct in the
  // single-user build. See the same note in readSessionRecord.
  if (auth && auth.enabled) return [];
  const fp = path.join(activeSessionsRoot(), name, name + '.json');
  if (!fs.existsSync(fp)) return [];
  try {
    const msgs = JSON.parse(readStripped(fp)).messages || [];
    const patched = patchMissingSubtaskResults(name, msgs);
    return enrichSubtaskStats(name, patched);
  }
  catch { return []; }
}

/**
 * Paginated variant of getSession. Returns the same shape as the file-based
 * path used to, plus a `paging` block: total message count, oldest / newest
 * ordinal returned in this batch, and hasMore flags on the older and newer
 * sides. Each returned message carries its ordinal so the frontend can drive
 * the next request without re-deriving it.
 *
 * DB path uses the indexed slice query in readSessionMessagesPaged. File
 * path reads the whole file once and slices in memory; the cost is still
 * bounded by the slice size, but the initial JSON.parse cannot be avoided
 * because the agent writes messages in array order and pagination would not
 * be possible without seeing the surrounding context.
 */
function isPageUserMessage(msg) {
  return Boolean(msg && msg.role === 'user' && !isInternalControlMessage(msg));
}

function displayMessageCount(messages) {
  let count = 0;
  for (const message of Array.isArray(messages) ? messages : []) {
    if (!message) continue;
    if (message.role === 'assistant') count += 1;
    else if (message.role === 'user' && !isInternalControlMessage(message)) count += 1;
  }
  return count;
}

function getSessionPaged(name, opts, scope = sessionStore.SINGLE_USER) {
  const o = opts || {};
  const all = o.all === true;
  const limit = Math.max(1, Math.min(Number(o.limit) || 50, 500));
  const beforeOrdinal = o.beforeOrdinal == null ? null : Number(o.beforeOrdinal);
  const afterOrdinal = o.afterOrdinal == null ? null : Number(o.afterOrdinal);

  // `all=1` is the normal transcript path for the chat UI. Pagination stays
  // available for the live tail and explicit history tooling, but it no longer
  // drives scroll position in the main conversation.
  if (all && beforeOrdinal == null && afterOrdinal == null && memoryDb) {
    refreshMemoryDatabase();
    const messages = sessionStore.readSessionMessages(memoryDb, name, scope);
    if (messages === null) {
      return { messages: [], paging: { total: 0, limit: 0, oldest_ordinal: null, newest_ordinal: null, has_more_older: false, has_more_newer: false } };
    }
    const tagged = messages.map((m, ordinal) => ({ ...m, ordinal }));
    patchMissingSubtaskResults(name, tagged);
    enrichSubtaskStats(name, tagged);
    const total = tagged.length;
    const displayTotal = displayMessageCount(tagged);
    const oldestOrdinal = total ? tagged[0].ordinal : null;
    const newestOrdinal = total ? tagged[total - 1].ordinal : null;
    return {
      messages: tagged,
      paging: {
        total,
        display_total: displayTotal,
        limit: total,
        oldest_ordinal: oldestOrdinal,
        newest_ordinal: newestOrdinal,
        has_more_older: false,
        has_more_newer: false,
      },
    };
  }

  if (memoryDb) {
    refreshMemoryDatabase();
    let result = sessionStore.readSessionMessagesPaged(memoryDb, name, { limit, beforeOrdinal, afterOrdinal }, scope);
    if (result === null) return { messages: [], paging: { total: 0, limit, oldest_ordinal: null, newest_ordinal: null, has_more_older: false, has_more_newer: false } };
    if (afterOrdinal == null && result.messages.length) {
      let combined = result.messages;
      let meta = result.paging;
      // A history page must begin at a real user request. Keep walking
      // backwards until the page contains one, then trim anything older than
      // that request so an interaction is never split across pages.
      for (let guard = 0; guard < 40 && !combined.some(isPageUserMessage) && meta.has_more_older; guard += 1) {
        const older = sessionStore.readSessionMessagesPaged(memoryDb, name, { limit, beforeOrdinal: combined[0].ordinal }, scope);
        if (!older || !older.messages.length) break;
        combined = older.messages.concat(combined);
        meta = { ...older.paging, newest_ordinal: meta.newest_ordinal, has_more_newer: meta.has_more_newer };
      }
      const firstUser = combined.findIndex(isPageUserMessage);
      if (firstUser > 0) combined = combined.slice(firstUser);
      if (combined.length) {
        const oldest = combined[0].ordinal;
        const newest = combined[combined.length - 1].ordinal;
        result = {
          messages: combined,
          paging: {
            ...meta,
            oldest_ordinal: oldest,
            newest_ordinal: newest,
            has_more_older: oldest > 0,
            has_more_newer: newest < meta.total - 1,
          },
        };
      }
    }
    patchMissingSubtaskResults(name, result.messages);
    enrichSubtaskStats(name, result.messages);
    return result;
  }

  // No ownership filter on this branch either; see readSessionRecord.
  if (auth && auth.enabled) {
    return { messages: [], paging: { total: 0, limit, oldest_ordinal: null, newest_ordinal: null, has_more_older: false, has_more_newer: false } };
  }

  const fp = path.join(activeSessionsRoot(), name, name + '.json');
  if (!fs.existsSync(fp)) return { messages: [], paging: { total: 0, limit, oldest_ordinal: null, newest_ordinal: null, has_more_older: false, has_more_newer: false } };
  let msgs = [];
  try { msgs = JSON.parse(readStripped(fp)).messages || []; } catch { msgs = []; }
  const total = msgs.length;
  const displayTotal = displayMessageCount(msgs);

  // Tag every message with its ordinal, then filter by before/after.
  // Tagging first means the slice we hand to the post-processors carries
  // the same shape the DB path returns, so convertToProductionFormat and
  // the rest of the chain cannot tell which backend answered.
  let view;
  let ordinalBase = 0;
  if (afterOrdinal != null) {
    ordinalBase = afterOrdinal + 1;
    view = msgs.slice(ordinalBase);
  } else if (beforeOrdinal != null) {
    view = msgs.slice(0, beforeOrdinal);
  } else {
    view = msgs;
  }
  let start = all && beforeOrdinal == null && afterOrdinal == null ? 0 : Math.max(0, view.length - limit);
  if (afterOrdinal == null && start > 0) {
    while (start > 0 && !view.slice(start).some(isPageUserMessage)) {
      start = Math.max(0, start - limit);
    }
    const firstUser = view.slice(start).findIndex(isPageUserMessage);
    if (firstUser > 0) start += firstUser;
  }
  const slice = view.slice(start);
  const tagged = slice.map((m, i) => ({ ...m, ordinal: ordinalBase + start + i }));
  patchMissingSubtaskResults(name, tagged);
  enrichSubtaskStats(name, tagged);

  const oldestOrdinal = tagged.length ? tagged[0].ordinal : null;
  const newestOrdinal = tagged.length ? tagged[tagged.length - 1].ordinal : null;
  return {
    messages: tagged,
    paging: {
      total,
      display_total: displayTotal,
      limit,
      oldest_ordinal: oldestOrdinal,
      newest_ordinal: newestOrdinal,
      has_more_older: oldestOrdinal != null && oldestOrdinal > 0,
      has_more_newer: newestOrdinal != null && newestOrdinal < total - 1,
    },
  };
}

// For historical sessions the create_subtask tool result carries a compact
// message snapshot (budget-trimmed), so summing it under-counts the subtask's
// real LLM time/tokens. The result also carries `session` = the full child
// session file; read it and inject complete agent_stats so the frontend shows
// true totals (new sessions get agent_stats straight from the tool already).
function enrichSubtaskStats(sessionName, msgs) {
  const subtasksDir = path.join(activeSessionsRoot(), sessionName, 'subtasks');
  const out = msgs.map(m => {
    if (!(m && m.role === 'tool' && m.name === 'create_subtask')) return m;
    let content = null;
    try { content = JSON.parse(m.content || ''); } catch {}
    if (!content) return m;
    const sessRef = content.session || '';
    const base = sessRef ? path.basename(sessRef) : '';
    const fp = base ? path.join(subtasksDir, base) : '';
    if (!fp || !fs.existsSync(fp)) return m;
    try {
      const sub = JSON.parse(readStripped(fp));
      const msgsArr = (sub && sub.messages) || [];
      let durMs = 0, pt = 0, ct = 0;
      for (const sm of msgsArr) {
        if (!sm || sm.role !== 'assistant') continue;
        if (typeof sm.duration_ms === 'number') durMs += sm.duration_ms;
        if (sm.usage) {
          pt += Number(sm.usage.prompt_tokens) || 0;
          ct += Number(sm.usage.completion_tokens) || 0;
        }
      }
      // Re-inject the FULL subtask process for the frontend card (the tool
      // result itself stays slim so the main-thread context is not flooded).
      const fullMsgs = msgsArr.filter(sm => sm && sm.role !== 'system');
      const clone = {
        ...content,
        agent_stats: { duration_ms: durMs, prompt_tokens: pt, completion_tokens: ct },
        messages: fullMsgs.length ? fullMsgs : content.messages,
      };
      const updated = { ...m, content: JSON.stringify(clone) };
      return updated;
    } catch { return m; }
  });
  return out;
}

// Live SSE path: when a create_subtask finishes, enrich its slim result with
// the full subtask process from the persisted session file, so the frontend
// card shows the whole run immediately (no page refresh needed). Mirrors
// enrichSubtaskStats used on history load.
function enrichCreateSubtaskResult(content, sessionName) {
  try {
    const parsed = JSON.parse(content);
    if (!parsed || typeof parsed !== 'object' || !parsed.session) return content;
    const base = path.basename(parsed.session);
    const fp = path.join(activeSessionsRoot(), sessionName, 'subtasks', base);
    if (!fp || !fs.existsSync(fp)) return content;
    const sub = JSON.parse(readStripped(fp));
    const msgsArr = (sub && sub.messages) || [];
    let durMs = 0, pt = 0, ct = 0;
    for (const sm of msgsArr) {
      if (!sm || sm.role !== 'assistant') continue;
      if (typeof sm.duration_ms === 'number') durMs += sm.duration_ms;
      if (sm.usage) {
        pt += Number(sm.usage.prompt_tokens) || 0;
        ct += Number(sm.usage.completion_tokens) || 0;
      }
    }
    const fullMsgs = msgsArr.filter(sm => sm && sm.role !== 'system');
    const clone = {
      ...parsed,
      agent_stats: { duration_ms: durMs, prompt_tokens: pt, completion_tokens: ct },
      messages: fullMsgs.length ? fullMsgs : parsed.messages,
    };
    return JSON.stringify(clone);
  } catch { return content; }
}

// Background subtask jobs are detached child agents. The parent turn returns
// immediately; these in-memory records only track control-plane state while
// the durable job/session data stays on disk.
function parseObject(value) {
  if (value && typeof value === 'object') return value;
  try { return JSON.parse(String(value || '')); } catch { return null; }
}

function latestSessionInteraction(sessionName) {
  try {
    const fp = path.join(activeSessionsRoot(), sessionName, sessionName + '.json');
    const session = parseObject(readStripped(fp)) || {};
    const msgs = Array.isArray(session.messages) ? session.messages : [];
    for (let i = msgs.length - 1; i >= 0; i--) {
      const msg = msgs[i] || {};
      if (msg.role !== 'user') continue;
      const id = String(msg.interaction_id || msg.turn_id || '').trim();
      if (id) return id;
    }
  } catch {}
  return '';
}

function extractSubtaskDelivery(messages) {
  let fallback = '';
  for (const msg of messages || []) {
    if (!msg || msg.role !== 'assistant') continue;
    const content = String(msg.content || '');
    if (content.includes('<subtask_result>')) fallback = content;
    if (!fallback && content.trim()) fallback = content;
  }
  return fallback;
}

function subtaskStatsFromMessages(messages) {
  const stats = { duration_ms: 0, prompt_tokens: 0, completion_tokens: 0 };
  for (const msg of messages || []) {
    if (!msg || msg.role !== 'assistant') continue;
    stats.duration_ms += Number(msg.duration_ms) || 0;
    if (msg.usage) {
      stats.prompt_tokens += Number(msg.usage.prompt_tokens) || 0;
      stats.completion_tokens += Number(msg.usage.completion_tokens) || 0;
    }
  }
  return stats;
}

function continuationPromptTemplate() {
  try {
    const cfg = parseObject(readStripped(APP_CONFIG)) || {};
    const prompts = (cfg && cfg.prompts) || {};
    // Prefer the explicitly configured module path; fall back to the
    // conventional location under the prompt modules directory.
    const configured = String(prompts.subtask_continue_path || '').trim();
    if (configured) {
      const fp = path.isAbsolute(configured) ? configured : path.join(REPO, configured);
      const text = readStripped(fp);
      if (text) return text;
    }
    const modulesDir = String(prompts.modules_dir || 'config/modules');
    const fp = path.isAbsolute(modulesDir) ? modulesDir : path.join(REPO, modulesDir);
    return readStripped(path.join(fp, 'subtask', 'continue', 'zh.md')) || '';
  } catch { return ''; }
}

function registerBackgroundSubtask(content, parentSession, modelId) {
  // Captured from the turn that started the subtask, because the timer that
  // collects this job's output later runs with no context of its own. Without
  // it, a subtask started by one account would be polled in the machine
  // owner's tree and its events published where that account cannot see them.
  const sessionContext = sessionScopeContext.getStore() || null;
  return runInCapturedSessionScope(sessionContext, () => registerBackgroundSubtaskIn(content, parentSession, modelId, sessionContext));
}

function registerBackgroundSubtaskIn(content, parentSession, modelId, sessionContext) {
  const parsed = parseObject(content);
  if (!parsed || parsed.background !== true || parsed.status !== 'running') return false;
  const jobId = String(parsed.job_id || parsed.branch_session || '').trim();
  if (!jobId || backgroundSubtaskJobs.has(jobId)) return !!jobId;
  const manifest = parseObject(readStripped(String(parsed.manifest_path || ''))) || parsed;
  const interactionId = latestSessionInteraction(parentSession);
  const continuationId = interactionId ? parentSession + '::' + interactionId : parentSession + '::' + jobId;
  const now = Date.now();
  const job = {
    ...manifest,
    ...parsed,
    job_id: jobId,
    parent_ui_session: parentSession,
    // The tree-qualified key, so the status route and the poller can tell two
    // accounts' same-named sessions apart.
    parent_run_key: sessionRunKey(parentSession),
    session_context: sessionContext,
    continuation_id: continuationId,
    model_id: modelId || '',
    status: 'running',
    stream_offset: 0,
    stream_buffer: '',
    registered_at: now,
  };
  backgroundSubtaskJobs.set(jobId, job);
  let continuation = pendingContinuations.get(continuationId);
  if (!continuation) {
    continuation = {
      id: continuationId,
      session: parentSession,
      session_context: sessionContext,
      interaction_id: interactionId,
      model_id: modelId || '',
      status: 'waiting',
      jobs: [],
      created_at: now,
    };
    pendingContinuations.set(continuationId, continuation);
  }
  if (!continuation.jobs.includes(jobId)) continuation.jobs.push(jobId);
  publishSessionEvent(parentSession, {
    type: 'subtask_job_started',
    job_id: jobId,
    title: job.title || '',
    branch_session: job.branch_session || '',
    interaction_id: interactionId || undefined,
  });
  return true;
}

function processBackgroundSubtaskStream(job) {
  const streamPath = String(job.stream_path || '');
  if (!streamPath || !fs.existsSync(streamPath)) return false;
  let stat;
  try { stat = fs.statSync(streamPath); } catch { return false; }
  if (stat.size < job.stream_offset) {
    job.stream_offset = 0;
    job.stream_buffer = '';
  }
  if (stat.size <= job.stream_offset) return false;
  let chunk = '';
  try {
    const fd = fs.openSync(streamPath, 'r');
    const buf = Buffer.alloc(stat.size - job.stream_offset);
    fs.readSync(fd, buf, 0, buf.length, job.stream_offset);
    fs.closeSync(fd);
    chunk = buf.toString('utf-8');
  } catch { return false; }
  job.stream_offset = stat.size;
  job.stream_buffer += chunk;
  const lines = job.stream_buffer.split('\n');
  job.stream_buffer = lines.pop() || '';
  let terminal = '';
  let failure = '';
  for (const rawLine of lines) {
    const line = rawLine.trim();
    if (!line) continue;
    const event = parseObject(line);
    if (!event || !event.type) continue;
    publishSessionEvent(job.parent_ui_session, {
      type: 'subtask_event',
      job_id: job.job_id,
      title: job.title || '',
      branch_session: job.branch_session || '',
      event,
    });
    if (event.type === 'done') terminal = 'done';
    if (event.type === 'error') {
      terminal = 'error';
      failure = String(event.error || 'subtask error');
    }
  }
  if (terminal) {
    finalizeBackgroundSubtask(job, terminal === 'error' ? failure : '');
    return true;
  }
  const age = Date.now() - (job.registered_at || Date.now());
  if (age > 5000 && job.pid && !processLooksAlive(job.pid)) {
    finalizeBackgroundSubtask(job, 'subtask process exited before emitting done');
    return true;
  }
  return false;
}

function processLooksAlive(pid) {
  const id = Number(pid) || 0;
  if (id <= 0) return false;
  try {
    process.kill(id, 0);
    return true;
  } catch (error) {
    return !!(error && error.code === 'EPERM');
  }
}

function finalizeBackgroundSubtask(job, errorText) {
  if (!job || (job.status !== 'running' && job.status !== 'waiting')) return;
  const failed = !!errorText;
  let messages = [];
  try {
    const session = parseObject(readStripped(String(job.session || ''))) || {};
    messages = Array.isArray(session.messages) ? session.messages : [];
  } catch {}
  const delivery = extractSubtaskDelivery(messages);
  job.status = failed ? 'failed' : 'completed';
  job.finished_at = Date.now();
  job.error = errorText || '';
  job.result = delivery || job.stream_buffer || '';
  job.agent_stats = subtaskStatsFromMessages(messages);
  publishSessionEvent(job.parent_ui_session, {
    type: 'subtask_completed',
    job_id: job.job_id,
    title: job.title || '',
    branch_session: job.branch_session || '',
    status: job.status,
    error: job.error || undefined,
    agent_stats: job.agent_stats,
  });
  const continuation = pendingContinuations.get(job.continuation_id);
  if (!continuation) return;
  const allDone = continuation.jobs.every(id => {
    const item = backgroundSubtaskJobs.get(id);
    return item && (item.status === 'completed' || item.status === 'failed');
  });
  if (allDone) {
    continuation.status = 'ready';
    maybeResumePendingContinuation(job.parent_ui_session);
  }
}

function maybeResumePendingContinuation(sessionName) {
  if (!sessionName) return;
  for (const continuation of pendingContinuations.values()) {
    if (continuation.session !== sessionName || continuation.status !== 'ready') continue;
    // Each continuation is resumed inside the tree it was registered in. This
    // runs from a timer - sometimes from the child-process close handler of an
    // unrelated turn - so the tree has to come from the continuation, not from
    // whatever context happens to be ambient.
    runInCapturedSessionScope(continuation.session_context, () => {
      // A foreground turn owns the session; the resume waits for it.
      if (runningChats.has(sessionRunKey(sessionName))) return;
      continuation.status = 'resuming';
      runBackgroundContinuation(continuation).catch(error => {
        continuation.status = 'ready';
        continuation.error = String((error && error.message) || error);
        console.error('[subtask continuation] resume failed:', continuation.error);
        if (error && error.retry) {
          // Foreground work owns the session; try again once it is free instead
          // of leaving the finished subtask stranded.
          setTimeout(() => maybeResumePendingContinuation(continuation.session), 2000);
        }
      });
    });
    return;
  }
}

function renderBackgroundContinuation(continuation) {
  const blocks = continuation.jobs.map(jobId => {
    const job = backgroundSubtaskJobs.get(jobId) || {};
    let result = String(job.result || job.error || '(empty result)');
    if (result.length > 50000) result = result.slice(0, 50000) + '\n...[truncated]';
    return [
      '### ' + String(job.title || job.branch_session || jobId),
      'status: ' + String(job.status || 'unknown'),
      'branch: ' + String(job.branch_session || ''),
      'agent_stats: ' + JSON.stringify(job.agent_stats || {}),
      result,
    ].join('\n');
  }).join('\n\n');
  const template = continuationPromptTemplate();
  const rendered = template.trim()
    ? template.replace(/\{\{\s*subtask_results\s*\}\}/g, blocks)
    : [
      '[会话状态] 后台子任务已完成。这不是用户的新请求，而是当前交互的异步工具结果。',
      '',
      '## 已完成的后台任务',
      '',
      blocks,
      '',
      '继续当前 skill / plan，不要重新开始或重复已完成工作。',
    ].join('\n');
  return ensureRuntimeControlMarker(rendered);
}

// The harness keys off this marker to keep runtime-injected turns out of the
// persisted transcript and to force active-plan injection. Guarantee it here so
// the prompt module under config/modules can be edited without silently
// breaking scheduling.
function ensureRuntimeControlMarker(text) {
  const body = String(text || '').trim();
  if (body.startsWith('[会话状态]')) return body;
  return '[会话状态] ' + body;
}

/**
 * Post a runtime turn back into the API as the account that owns the session.
 *
 * The declaration matters because this call arrives from this machine, which
 * the auth layer treats as the operator. Without it, a continuation for a family
 * member's session would be attributed to the machine owner and could land in a
 * session of the same name that the owner happens to have.
 */
function postInternalContinuation(sessionName, message, modelId, sessionContext) {
  const uiSettings = readUISettings();
  const preferredFallback = uiSettings.auto_fallback !== false
    && uiSettings.fallback_model
    && uiSettings.fallback_model !== modelId
    && MODELS[uiSettings.fallback_model]
    && hasResolvedApiKey(MODELS[uiSettings.fallback_model].apiKey)
    ? uiSettings.fallback_model
    : '';
  const fallbackModelId = preferredFallback || Object.keys(MODELS).find(id => (
    id !== modelId && hasResolvedApiKey(MODELS[id].apiKey)
  )) || '';
  return new Promise((resolve, reject) => {
    const body = JSON.stringify({
      message,
      model: modelId,
      fallback_model: fallbackModelId,
      session: sessionName,
      session_owner: sessionContext && sessionContext.scope && typeof sessionContext.scope === "number"
        ? sessionContext.scope
        : undefined,
      stream: true,
      surface: 'chat',
      internal_type: 'subtask_continuation',
    });
    const req = require('http').request({
      hostname: '127.0.0.1',
      port: PORT,
      path: '/api/chat',
      method: 'POST',
      headers: { 'Content-Type': 'application/json; charset=utf-8', 'Content-Length': Buffer.byteLength(body) },
    }, res => {
      let raw = '';
      res.setEncoding('utf-8');
      res.on('data', chunk => { raw += chunk; });
      res.on('end', () => {
        if (res.statusCode >= 200 && res.statusCode < 300) { resolve(); return; }
        const error = new Error('internal continuation HTTP ' + res.statusCode);
        // A user turn owns the session; the resume must wait and retry later.
        error.retry = res.statusCode === 409 || /session busy/.test(raw);
        reject(error);
      });
    });
    req.on('error', reject);
    req.write(body);
    req.end();
  });
}

async function runBackgroundContinuation(continuation) {
  let modelId = String(continuation.model_id || '').trim();
  if (!MODELS[modelId]) {
    try {
      const session = parseObject(readStripped(path.join(activeSessionsRoot(), continuation.session, continuation.session + '.json'))) || {};
      modelId = apiModelToUi[session.model] || String(session.model || '');
    } catch {}
  }
  if (!MODELS[modelId]) {
    const fallback = Object.keys(MODELS)[0] || '';
    if (!fallback) throw new Error('no model available for continuation');
    modelId = fallback;
  }
  const prompt = renderBackgroundContinuation(continuation);
  publishSessionEvent(continuation.session, {
    type: 'subtask_resume_started',
    interaction_id: continuation.interaction_id || undefined,
    jobs: continuation.jobs,
  });
  await postInternalContinuation(continuation.session, prompt, modelId, continuation.session_context);
  continuation.status = 'completed';
  continuation.completed_at = Date.now();
  publishSessionEvent(continuation.session, {
    type: 'subtask_resume_finished',
    interaction_id: continuation.interaction_id || undefined,
    jobs: continuation.jobs,
  });
}

function pollBackgroundSubtaskJobs() {
  for (const job of backgroundSubtaskJobs.values()) {
    if (job.status !== 'running' && job.status !== 'waiting') continue;
    runInCapturedSessionScope(job.session_context, () => processBackgroundSubtaskStream(job));
  }
}

setInterval(pollBackgroundSubtaskJobs, 1500).unref?.();

// When the main thread crashed while a create_subtask child was still running,
// the tool result was never written back to the main session. Recover it from
// the persisted child session (<chat>/subtasks/<title>-<nano>.json) so the
// frontend can render the finished subtask instead of a stuck "running" card.
function patchMissingSubtaskResults(sessionName, msgs) {
  const subtasksDir = path.join(activeSessionsRoot(), sessionName, 'subtasks');
  let files = [];
  try { files = fs.readdirSync(subtasksDir).filter(f => f.endsWith('.json')); } catch { return msgs; }
  if (!files.length) return msgs;

  const out = [...msgs];
  let inserted = 0;
  for (let i = 0; i < out.length; i++) {
    const m = out[i];
    if (m.role !== 'assistant' || !Array.isArray(m.tool_calls)) continue;
    const cs = m.tool_calls.filter(tc => tc.function && tc.function.name === 'create_subtask');
    if (!cs.length) continue;
    for (const tc of cs) {
      const callId = tc.id || tc.tool_call_id || '';
      const hasResp = out.slice(i + 1).some(t => t.role === 'tool' && (t.tool_call_id === callId || (t.name === 'create_subtask' && !callId)));
      if (hasResp) continue;
      let title = '';
      try { title = (JSON.parse(tc.function.arguments || '{}').title) || ''; } catch {}
      if (!title) continue;
      const safe = String(title).replace(/[\\/:*?"<>|]/g, '_').replace(/\s+/g, '_');
      let best = null;
      for (const f of files) {
        if (!f.startsWith(safe)) continue;
        const fp2 = path.join(subtasksDir, f);
        try {
          const st = fs.statSync(fp2);
          if (!best || st.mtimeMs > best.mtimeMs) best = { f, fp: fp2, mtime: st.mtimeMs };
        } catch {}
      }
      if (!best) continue;
      try {
        const child = JSON.parse(readStripped(best.fp));
        const childMsgs = child.messages || [];
        const BUDGET = 30000;
        const all = [];
        let total = 0;
        for (const cm of childMsgs) {
          if (cm.role === 'system') continue;
          const cc = typeof cm.content === 'string' ? cm.content : '';
          const isReport = /<report\b/i.test(cc);
          all.push({ ...cm, content: (!isReport && cc.length > 4000) ? cc.slice(0, 4000) + '...[truncated]' : cc });
          if (!isReport) total += cc.length;
        }
        let compact = all;
        if (total > BUDGET) {
          // Keep <report> deliverables and the newest tail; omit the middle.
          const kept = [];
          let keptLen = 0;
          let reportSeen = false;
          for (let i = all.length - 1; i >= 0; i--) {
            const cc = typeof all[i].content === 'string' ? all[i].content : '';
            const isReport = /<report\b/i.test(cc);
            if (isReport && !reportSeen) {
              reportSeen = true;
              kept.unshift(all[i]);
              keptLen += cc.length;
              continue;
            }
            if (keptLen + cc.length > BUDGET) continue;
            kept.unshift(all[i]);
            keptLen += cc.length;
          }
          const skipped = all.length - kept.length;
          if (skipped > 0) kept.unshift({ role: 'assistant', content: '...[中间省略 ' + skipped + ' 条消息，完整内容见子任务 session 文件]...' });
          compact = kept;
        }

        const resultContent = JSON.stringify({
          ok: true, title, session: best.fp,
          messages: compact,
          recovered: true,
        });
        out.splice(i + 1 + inserted, 0, { role: 'tool', content: resultContent, tool_call_id: callId, name: 'create_subtask' });
        inserted++;
      } catch {}
    }
  }
  return out;
}


let _counter = 0;
function nextId() { return ++_counter; }
function nowTs() { return Date.now(); }


// Extract user-visible text from assistant messages with XML protocol tags
function extractDisplayText(content) {
  if (!content) return content;
  // Strip internal protocol tags (reflection, etc) that should not reach frontend
  content = content.replace(/<reflection[\s\S]*?<\/reflection>/gi, '');
  // <process><message>text</message></process> -> only show message text (tool thought/preview)
  var m = content.match(/<message[^>]*>([\s\S]*?)<\/message>/);
  if (m) return m[1].trim();
  // <behavior><des>text</des></behavior> -> only show des text (backward compat)
  m = content.match(/<des[^>]*>([\s\S]*?)<\/des>/);
  if (m) return m[1].trim();
  // <report> is NOT stripped - frontend needs it for report card formatting
  return content;
}

const INTERNAL_CONTROL_TYPES = new Set([
  "auto_continue",
  "delivered_status",
  "auto_answer",
]);

function isInternalControlMessage(msg) {
  const internalType = String((msg && msg.internal_type) || "");
  if (INTERNAL_CONTROL_TYPES.has(internalType)) return true;
  const content = String((msg && msg.content) || "").trim();
  return content.startsWith("[会话状态]");
}

function isContextSummaryMessage(msg, content) {
  const internalType = String((msg && msg.internal_type) || "").trim();
  return internalType === "context_summary" || /^\s*<summary\b/i.test(String(content || ""));
}

// <summary> is an internal context checkpoint. If it ever reaches the user
// input path (copy/paste, an old client, or an interrupted stream), strip the
// protocol block before it can be persisted as a real user request.
function sanitizeUserProtocolText(value) {
  return String(value || "")
    .replace(/<summary\b[^>]*>[\s\S]*?<\/summary\s*>/gi, "")
    .replace(/<\/?summary\b[^>]*>/gi, "")
    .trim();
}

function convertToProductionFormat(rawMessages, model = null) {
  if (!rawMessages || !rawMessages.length) {
    return { code: 0, message: "success", data: { messages: [], paging: { offset: 0, limit: 20, total: 0 }, model } };
  }
  const production = [];
  let current = null;
  let turnIndex = 0;
  let legacyInteractionIndex = 0;
  let currentInteractionId = null;
  _counter = 0;
  const { randomUUID } = require("crypto");
  const seenUserInteractions = new Set();

  for (const msg of rawMessages) {
    // Paged requests are assembled independently. Seed the block id from the
    // stable raw ordinal so ids do not restart at 1 on every page and collide
    // in the client's prepend dedupe.
    if (msg && msg.ordinal != null && Number.isFinite(Number(msg.ordinal))) {
      _counter = Number(msg.ordinal) * 1000;
    }
    const role = msg.role || "";
    let content = msg.content || "";
    if (typeof content !== "string") content = String(content);

    if (role === "user") {
      const interactionId = String(msg.interaction_id || msg.turn_id || "");
      if (isInternalControlMessage(msg)) continue;
      if (isContextSummaryMessage(msg, content)) {
        if (current) { production.push(current); current = null; }
        turnIndex++;
        const rawInteractionId = msg.interaction_id || msg.turn_id;
        if (rawInteractionId) currentInteractionId = String(rawInteractionId);
        if (!currentInteractionId) currentInteractionId = String(++legacyInteractionIndex);
        current = {
          role: "assistant",
          contents: [{ id: nextId(), timestamp: msg.ts || nowTs(), type: "text", internal_type: "context_summary", content, active_agent: "main" }],
          turn_id: String(turnIndex),
          interaction_id: currentInteractionId,
          version_id: randomUUID(),
          message_uuid: randomUUID(),
          display_tag: "response",
        };
        production.push(current);
        current = null;
        continue;
      }
      content = sanitizeUserProtocolText(content);
      if (!content) continue;
      // Legacy sessions predate internal_type. A repeated "请继续。" inside
      // the same interaction is an agent continuation, while a new interaction
      // that literally starts with "请继续。" remains a real user request.
      if (content.trim() === "请继续。" && interactionId && seenUserInteractions.has(interactionId)) {
        continue;
      }
      if (interactionId) seenUserInteractions.add(interactionId);
    }

    if (role === "user") {
      if (current) { production.push(current); current = null; }
      turnIndex++;
      const rawInteractionId = msg.interaction_id || msg.turn_id;
      if (rawInteractionId) currentInteractionId = String(rawInteractionId);
      else currentInteractionId = String(++legacyInteractionIndex);
      const userMsg = {
        role: "user",
        contents: [{ id: nextId(), timestamp: msg.ts || nowTs(), type: "text", internal_type: "text", content: extractDisplayText(content), active_agent: "main" }],
        turn_id: String(turnIndex),
        interaction_id: currentInteractionId,
        version_id: randomUUID(),
        message_uuid: randomUUID(),
        display_tag: "response",
      };
      production.push(userMsg);
      if (msg.usage) userMsg.usage = msg.usage;
      if (msg.duration_ms) userMsg.duration_ms = msg.duration_ms;
      if (msg.step !== undefined) userMsg.step = msg.step;
      if (msg.ts) userMsg.ts = msg.ts;
      if (msg.session_id) userMsg.session_id = msg.session_id;
    } else if (role === "assistant") {
      if (current) production.push(current);
      turnIndex++;
      const rawInteractionId = msg.interaction_id || msg.turn_id;
      if (rawInteractionId) currentInteractionId = String(rawInteractionId);
      if (!currentInteractionId) currentInteractionId = String(++legacyInteractionIndex);
      current = {
        role: "assistant",
        contents: [],
        turn_id: String(turnIndex),
        interaction_id: currentInteractionId,
        version_id: randomUUID(),
        message_uuid: randomUUID(),
        display_tag: "response",
        usage: msg.usage,
        duration_ms: msg.duration_ms,
      };
      if (content) {
        current.contents.push({ id: nextId(), timestamp: msg.ts || nowTs(), type: "text", internal_type: msg.internal_type || "text", content: extractDisplayText(content), active_agent: "main" });
      }
      // Pass through usage and duration_ms for per-turn stats
      if (msg.usage) current.usage = msg.usage;
      if (msg.duration_ms !== undefined && msg.duration_ms !== null) current.duration_ms = msg.duration_ms;
      if (msg.real_ms !== undefined && msg.real_ms !== null) current.real_ms = msg.real_ms;
      if (msg.step !== undefined) current.step = msg.step;
      if (msg.ts) current.ts = msg.ts;
      if (msg.session_id) current.session_id = msg.session_id;
      if (msg.tools_used) current.tools_used = msg.tools_used;
      const tc = msg.tool_calls;
      if (tc && tc.length) {
        const tcStr = typeof tc === "string" ? tc : JSON.stringify(tc);
        const tcItem = { id: nextId(), timestamp: msg.ts || nowTs(), type: "tool_calls", internal_type: "tool_calls", tool_calls: tcStr, active_agent: "main" };
        if (msg.step !== undefined) tcItem.step = msg.step;
        if (msg.ts) tcItem.ts = msg.ts;
        current.contents.push(tcItem);
      }
    } else if (role === "tool") {
      if (!current) {
        turnIndex++;
        const rawInteractionId = msg.interaction_id || msg.turn_id;
        if (rawInteractionId) currentInteractionId = String(rawInteractionId);
        if (!currentInteractionId) currentInteractionId = String(++legacyInteractionIndex);
        current = { role: "assistant", contents: [], turn_id: String(turnIndex), interaction_id: currentInteractionId, version_id: randomUUID(), message_uuid: randomUUID(), display_tag: "response" };
      }
      if (msg.interaction_id && !current.interaction_id) current.interaction_id = String(msg.interaction_id);
      const trItem = { id: nextId(), timestamp: msg.ts || nowTs(), type: "tool_result", internal_type: "tool_result", content, tool_call_id: msg.tool_call_id || "", name: msg.name || "", active_agent: "main" };
      if (msg.step !== undefined) trItem.step = msg.step;
      if (msg.ts) trItem.ts = msg.ts;
      current.contents.push(trItem);
    }
  }
  if (current) production.push(current);

  return { code: 0, message: "success", data: { messages: production, paging: { offset: 0, limit: 20, total: production.length }, model } };
}

function appendErrorToSession(sessionFile, err) {
  try {
    let session = { messages: [] };
    if (fs.existsSync(sessionFile)) {
      const raw = readStripped(sessionFile);
      if (raw) session = JSON.parse(raw);
    }
    session.messages.push({
      role: "system",
      internal_type: "harness_error",
      content: "SYSTEM ERROR: " + err,
    });
    fs.writeFileSync(sessionFile, JSON.stringify(session, null, 2), "utf-8");
  } catch {}
}

// Track chats with an in-flight agent run so the frontend can show
// "thinking" state after switching sessions or refreshing.
const runningChats = new Map(); // session key (sessionRunKey) -> { startedAt, child }

// Keep a bounded replay log for each session run. A browser can leave the
// current session while the agent keeps working; reconnecting to this log
// resumes tool/text progress without starting another agent.
const sessionEventStreams = new Map(); // session key (sessionRunKey) -> stream state
const SESSION_EVENT_RETENTION_MS = 10 * 60 * 1000;
const SESSION_EVENT_MAX_EVENTS = 6000;
const backgroundSubtaskJobs = new Map(); // job_id -> detached subtask state
const pendingContinuations = new Map(); // continuation_id -> background fan-in state

function createSessionEventStream(sessionName, runId) {
  const key = sessionRunKey(sessionName);
  const previous = sessionEventStreams.get(key);
  if (previous && previous.cleanupTimer) clearTimeout(previous.cleanupTimer);
  if (previous) {
    for (const subscriber of previous.subscribers) {
      try { subscriber(null, 'done'); } catch {}
    }
  }
  const state = {
    session: sessionName,
    runId,
    nextEventId: 0,
    events: [],
    subscribers: new Set(),
    finished: false,
    finishedAt: 0,
    cleanupTimer: null,
  };
  sessionEventStreams.set(key, state);
  return state;
}

function publishSessionEvent(sessionName, payload) {
  const key = sessionRunKey(sessionName);
  const state = sessionEventStreams.get(key);
  if (!state) return { ...(payload || {}), session: sessionName };
  const event = {
    ...(payload || {}),
    session: sessionName,
    run_id: state.runId,
    event_id: ++state.nextEventId,
  };
  state.events.push(event);
  if (state.events.length > SESSION_EVENT_MAX_EVENTS) {
    state.events.splice(0, state.events.length - SESSION_EVENT_MAX_EVENTS);
  }
  for (const subscriber of [...state.subscribers]) {
    try { subscriber(event, null); } catch {}
  }
  return event;
}

function finishSessionEventStream(sessionName, runId) {
  const key = sessionRunKey(sessionName);
  const state = sessionEventStreams.get(key);
  if (!state || (runId && state.runId !== runId) || state.finished) return;
  state.finished = true;
  state.finishedAt = Date.now();
  for (const subscriber of [...state.subscribers]) {
    try { subscriber(null, 'done'); } catch {}
  }
  state.subscribers.clear();
  state.cleanupTimer = setTimeout(() => {
    const current = sessionEventStreams.get(key);
    if (current && current.runId === state.runId && current.finished) {
      sessionEventStreams.delete(key);
    }
  }, SESSION_EVENT_RETENTION_MS);
  if (state.cleanupTimer.unref) state.cleanupTimer.unref();
}

// Kill an agent run and its whole process tree. create_subtask/run.exe and
// nested subtask agent-loop.exe are children of the main agent process, so
// killing only the main process would leave orphaned subtasks still generating
// output (PPT would keep building after the user pressed Stop).
function killProcessTree(pid, signal) {
  if (!pid) return;
  const cp = require("child_process");
  if (process.platform === "win32") {
    // taskkill /T kills the whole tree rooted at pid (node is not a child, so
    // the server itself is never touched).
    try { cp.spawnSync("taskkill", ["/PID", String(pid), "/T", "/F"], { timeout: 15000 }); } catch {}
    return;
  }
  // POSIX: recursively collect descendants via pgrep -P, then signal them.
  try {
    const out = cp.spawnSync("pgrep", ["-P", String(pid)], { encoding: "utf8", timeout: 5000 });
    if (out.status === 0 && out.stdout) {
      for (const line of out.stdout.split("\n")) {
        const c = parseInt(line.trim(), 10);
        if (c && c !== pid) killProcessTree(c, signal);
      }
    }
  } catch {}
  try { process.kill(pid, signal); } catch {}
}

// Persist a turn's real wait time (measured client-side) onto the newest
// assistant message(s) of the session so history load still shows ⏱ real.
// Persist a turn's real wait time (measured client-side) onto the newest
// assistant message(s) of the session AND into usage.json (per-turn +
// cumulative real_ms) so it survives refresh/restart instead of living
// only in localStorage.
function computeRealMsFromTs(msgs) {
  let start = 0, end = 0;
  for (const m of msgs || []) {
    if (m.role === 'user' && Number(m.ts)) start = Number(m.ts);
    if (m.role === 'assistant' && Number(m.ts)) end = Number(m.ts);
  }
  return (start && end && end >= start) ? end - start : 0;
}
function attachRealMsToSession(name, realMs) {
  const fp = path.join(activeSessionsRoot(), name, name + '.json');
  let matchedIndex = -1;
  try {
    const raw = readStripped(fp);
    if (raw) {
      const session = JSON.parse(raw);
      const msgs = session.messages || [];
      // Prefer computing real wait from ts timestamps (epoch ms, direct diff);
      // fall back to the client-measured value for legacy sessions without ts.
      const tsReal = computeRealMsFromTs(msgs);
      if (tsReal > 0) realMs = tsReal;
      // Attach to the latest assistant message. Timing is no longer inferred
      // from usage duration; message timestamps own real_ms.
      for (let i = msgs.length - 1; i >= 0; i--) {
        const m = msgs[i];
        if (m.role === 'assistant' && (m.content || m.ts)) {
          m.real_ms = realMs;
          matchedIndex = i;
          break;
        }
      }
      fs.writeFileSync(fp, JSON.stringify(session, null, 2), 'utf-8');
    }
  } catch {}

  // Persist real time into usage.json too (per-turn + cumulative).
  const usageFp = path.join(activeSessionsRoot(), name, 'usage.json');
  try {
    const uRaw = readStripped(usageFp);
    if (!uRaw) return;
    const usage = JSON.parse(uRaw);
    const turns = usage.turns || [];
    if (turns.length) {
      let target = null;
      if (matchedIndex >= 0) target = turns.find(t => t && t.message_index === matchedIndex);
      if (!target) target = turns[turns.length - 1];
      if (target) target.real_ms = realMs;
    }
    usage.real_ms = turns.reduce((s, t) => s + (Number(t && t.real_ms) || 0), 0);
    usage.turns = turns;
    fs.writeFileSync(usageFp, JSON.stringify(usage, null, 2), 'utf-8');
  } catch {}
}

const PORT = parseInt(process.argv[2]) || 8081;

/**
 * One request, run inside the caller's sessions tree.
 *
 * The scope is resolved here - before the auth gate and before any route - so
 * everything the request touches agrees on which account's sessions it is
 * working with, including the child-process callbacks that finish the turn long
 * after this function has returned.
 */
// ---- 仅测试用（FAIRY_TEST_INJECT_HOOK=1 时才被用到）-------------------------
// 必须待在**模块作用域**：放 handleRequest 里会被每个请求重建一次，
// attach-run 存进去的 Map 和 received 读的就不是同一个东西，收到永远 0 行。
const testInbox = new Map(); // sessionKey -> 靶子回显回来的行
const testEcho = "let b='';process.stdin.on('data',d=>{b+=d;let i;while((i=b.indexOf('\\n'))>=0){const l=b.slice(0,i);b=b.slice(i+1);if(l)process.stdout.write(l+'\\n')}});";

function handleRequest(req, res) {
  const url = new URL(req.url, "http://localhost");
  const pathname = url.pathname;

  // HTTP 规定 HEAD 的状态码与响应头必须与 GET 一致，只是省略响应体。
  // 之前只认 GET：实测 HEAD / 与 HEAD /api/files 全部返回 404。任何用 HEAD
  // 做探针的中间层（Cloudflare tunnel、Caddy、监控、CDN 回源）都会拿到 404，
  // 这是公网上「有概率 404」的一个确定来源。这里把 HEAD 当 GET 路由，
  // 同时抑制响应体。
  if (req.method === "HEAD") {
    const innerEnd = res.end.bind(res);
    res.write = () => true;
    res.end = () => innerEnd();
    req.method = "GET";
  }
  const method = req.method;

  // While auth is off this mirrors the historical desktop behaviour. With auth on
  // a wildcard would defeat the point: any page the user visits could read this
  // API cross-origin, cookie or not.
  if (!auth.config.enabled) {
    res.setHeader("Access-Control-Allow-Origin", "*");
  } else if (req.headers.origin) {
    const origin = String(req.headers.origin);
    const host = String(req.headers.host || "");
    if (origin === `http://${host}` || origin === `https://${host}`) {
      res.setHeader("Access-Control-Allow-Origin", origin);
      res.setHeader("Vary", "Origin");
    }
  }
  res.setHeader("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS");
  res.setHeader("Access-Control-Allow-Headers", "Content-Type");
  // P4-1 WebDAV：OPTIONS 必须原样交给 /dav/ 处理，不能在这里统一 204 掉。
  // 客户端挂载前的第一个请求就是 OPTIONS /dav/，靠 DAV: 头判断这是不是
  // WebDAV 服务器；被 204 空回就等于告诉客户端"不是"，挂载直接失败。
  const isDavPath = pathname === "/dav" || pathname.startsWith("/dav/");
  if (method === "OPTIONS" && !isDavPath) { res.writeHead(204); res.end(); return; }

  const sendJson = (obj, status = 200) => {
    res.writeHead(status, { "Content-Type": "application/json; charset=utf-8" });
    res.end(JSON.stringify(obj));
  };

  // Authentication runs before any other route so a newly added endpoint cannot
  // accidentally ship without the check.
  if (auth.handle(req, res, pathname, method, url, sendJson)) return;

  // 入口导航页：由后端发，不走 Caddy 的 file_server。
  // 只有经过上面的 auth.handle 才会到这里，所以入口页和其它入口共用同一个
  // session——在入口登录一次，/agent 和 /filemanage 就都不用再登。
  // 刻意不占用 "/"：Caddy 把 /filemanage 改写成 "/" 交给这里，抢了会掀掉文件管理器。
  if (method === "GET" && pathname === "/entry.html") {
    try {
      const entry = fs.readFileSync(path.join(REPO, "landing", "index.html"));
      res.writeHead(200, {
        "Content-Type": "text/html; charset=utf-8",
        "Content-Length": entry.length,
        "X-Content-Type-Options": "nosniff",
        "Cache-Control": "no-store",
      });
      res.end(entry);
    } catch {
      sendJson({ error: "entry page unavailable" }, 500);
    }
    return;
  }

  // File manager (lives in the standalone filemanager/ folder): static UI,
  // vendor assets and the /api/files|download|upload|folder|file routes.
  // Claimed before any Fairy route — the two route sets are disjoint.
  if (fm.tryHandleFileManager(req, res, pathname, method, sendJson)) return;

  // WeChat link-up. Behind the same gate as every other route, so the account it
  // links under is the account that made the request - which is what keeps one
  // family member from attaching a WeChat to somebody else's account.
  if (wechatLink.handle(req, res, pathname, method, url, sendJson, auth.enabled ? auth.resolveSession(req) : null)) return;

  if (method === "GET") {
    if (pathname === "/api/ppt-preview") {
      const deckId = resolveDeckId(url.searchParams.get('deck_dir'), {
        workspaceRoot: LOCAL_PPT_WORKSPACE_ROOT,
        virtualDeckRoot: LOCAL_PPT_DECK_ROOT,
      });
      if (!deckId) { sendJson({ error: 'invalid or unavailable PPT deck' }, 404); return; }
      const pages = listDeckPages(deckId, LOCAL_PPT_WORKSPACE_ROOT);
      if (!pages.length) { sendJson({ error: 'PPT pages are unavailable' }, 404); return; }
      const html = buildDeckPreviewHtml(deckId, pages);
      res.writeHead(200, {
        'Content-Type': 'text/html; charset=utf-8',
        'Content-Length': Buffer.byteLength(html),
        'X-Content-Type-Options': 'nosniff',
        'Content-Security-Policy': "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; frame-src 'self'; img-src 'self'",
      });
      res.end(html);
      return;
    }
    const pptFileParts = pathname.split('/').filter(Boolean);
    if (pptFileParts.length >= 5 && pptFileParts[0] === 'api' && pptFileParts[1] === 'ppt-decks') {
      const deckId = decodeURIComponent(pptFileParts[2]);
      const relativePath = pptFileParts.slice(3).map(decodeURIComponent).join('/');
      const filePath = resolveDeckFile(deckId, relativePath, LOCAL_PPT_WORKSPACE_ROOT);
      if (!filePath) { sendJson({ error: 'PPT file not found' }, 404); return; }
      const ext = path.extname(filePath).toLowerCase();
      const mime = ({ '.html': 'text/html; charset=utf-8', '.css': 'text/css; charset=utf-8', '.js': 'text/javascript; charset=utf-8', '.json': 'application/json; charset=utf-8', '.png': 'image/png', '.jpg': 'image/jpeg', '.jpeg': 'image/jpeg', '.webp': 'image/webp', '.svg': 'image/svg+xml' })[ext] || 'application/octet-stream';
      const data = fs.readFileSync(filePath);
      res.writeHead(200, { 'Content-Type': mime, 'Content-Length': data.length, 'X-Content-Type-Options': 'nosniff' });
      res.end(data);
      return;
    }

    if (pathname === "/api/ai-reports") {
      const session = url.searchParams.get('session') || '';
      sendJson({ session, reports: readAiReports(session) });
      return;
    }
    if (pathname === "/api/browser-frame/stream.mjpg") {
      // 远端观者看的真实画面:agent 的 Playwright 用 CDP screencast 抓帧,
      // 写到 FRAME_DIR 下的 fNNNNNN.jpg,这里轮询最新帧推 MJPEG。
      // 与 artifact.page_url 的区别:后者是让观者自己再打开一次那个地址,
      // 在本机以外的设备上必然连不上,也不是本机浏览器的像素。
      const dir = FRAME_DIR;
      res.writeHead(200, {
        "Content-Type": "multipart/x-mixed-replace; boundary=frame",
        "Cache-Control": "no-store, no-cache, must-revalidate",
        "Pragma": "no-cache",
        "Connection": "close",
        "X-Accel-Buffering": "no",
      });
      let lastName = "";
      let stopped = false;
      const onClose = () => { stopped = true; };
      res.on("close", onClose);
      // 没有帧时先占位,避免观者那边一直转圈看不出是活着还是死了。
      let warmupSent = false;
      const timer = setInterval(() => {
        if (stopped) { clearInterval(timer); res.end(); return; }
        let name = "";
        try {
          // 按文件修改时间取最新帧，不能用 sort() 取最后一个。
          // browser_bridge.mjs 每次新会话都从 f000001 重新编号，而目录里还留着
          // 上一轮会话的旧帧，两者的编号大小与实际新旧完全无关
          // （实测 f007204 排在 f000171 之后，却是 9 小时前抓的）。
          // 字符串排序取「最大」会把最旧的帧永远推给远端观者。
          let newest = "";
          let newestM = -1;
          for (const f of fs.readdirSync(dir)) {
            if (!/^f\d+\.jpg$/.test(f)) continue;
            let m = 0;
            try { m = fs.statSync(path.join(dir, f)).mtimeMs; } catch { continue; }
            if (m > newestM) { newestM = m; newest = f; }
          }
          name = newest;
        } catch {}
        if (!name || name === lastName) {
          if (!warmupSent) {
            warmupSent = true;
            lastName = "";
          }
          return;
        }
        let buf;
        try { buf = fs.readFileSync(path.join(dir, name)); } catch { return; }
        lastName = name;
        res.write("--frame\r\nContent-Type: image/jpeg\r\nContent-Length: " + buf.length + "\r\n\r\n");
        res.write(buf);
        res.write("\r\n");
      }, 200);
      return;
    }
    if (pathname === "/api/deck-score") {
      const deckRel = String(url.searchParams.get('deck') || '')
        || resolveDeckRelForSession(url.searchParams.get('session') || '');
      if (!deckRel) { sendJson({ error: 'deck not found for this session' }, 404); return; }
      const job = DECK_SCORE_JOBS.get(deckRel);
      sendJson({ deck: deckRel, running: !!(job && job.running), score: readDeckScore(deckRel), log: job ? job.log : '' });
      return;
    }
    if (pathname === "/api/models") {
      sendJson(Object.entries(MODELS).map(([id, v]) => ({
        id,
        display: v.display || id,
        apiModel: v.apiModel || '',
        provider: v.provider || '',
        available: hasResolvedApiKey(v.apiKey),
        user: !!v.user,
      })));
      return;
    }
    if (pathname === "/api/settings") {
      sendJson({
        ...readUISettings(),
        tools: readToolSettings(),
        skills: readSkillSettings(),
      });
      return;
    }
    if (pathname === "/api/workspace-root") {
      sendJson({ root: String(REPO || '').replace(/\\/g, '/') });
      return;
    }
    if (pathname === "/api/sessions") {
      sendJson(listSessions(sessionScope(req)));
      return;
    }
    const parts = pathname.split("/").filter(Boolean);
    if (parts.length >= 3 && parts[0] === "api" && parts[1] === "sessions") {
      const name = decodeURIComponent(parts[2]);
      // One gate for the whole /api/sessions/<name>/... namespace, placed here
      // rather than inside each sub-route on purpose. The sub-routes below read
      // trace.jsonl, run files, deck files and ai reports straight from
      // SESSIONS/<name>/, and none of that code filters by owner; individual
      // per-route checks would be the kind of protection that is correct on the
      // day it is written and quietly missing from the route added next month.
      // Once the name is known to belong to this caller, the existing
      // unfiltered reads below are safe to keep. 404 rather than 403 so the
      // response does not confirm that someone else's session exists.
      if (!requireSessionAccess(req, name)) {
        sendJson({ error: "session not found" }, 404);
        return;
      }
      if (parts.length === 4 && parts[3] === 'trace') {
        const dir = path.join(activeSessionsRoot(), name);
        const session = readSessionRecord(name, sessionScope(req)) || {};
        const full = url.searchParams.get('full') === '1';
        const out = { trace_id: session.session_id || session.trace_id || null, main: [], subtasks: [] };
        out.main = _traceEventsForSession(name, full);
        const seenSubtasks = new Set();
        const subDir = path.join(dir, 'subtasks');
        try {
          if (fs.existsSync(subDir)) {
            for (const f of fs.readdirSync(subDir)) {
              if (!f.endsWith('.trace.jsonl')) continue;
              const fp = path.join(subDir, f);
              const base = f.slice(0, -'.trace.jsonl'.length);
              let title = base;
              try {
                const sj = JSON.parse(readStripped(path.join(subDir, base + '.json')) || '{}');
                if (sj.title) title = sj.title;
              } catch {}
              let events = readTraceLines(fp);
              if (full) {
                try {
                  const sj = JSON.parse(readStripped(path.join(subDir, base + '.json')) || 'null');
                  if (sj && Array.isArray(sj.messages)) _enrichEventsWithMessages(events, sj.messages);
                } catch {}
              }
              events = events.map((event, index) => ({
                ...event,
                span_id: event.span_id || ('nested-' + index),
                timestamp: event.timestamp || Number(event.ts) || 0,
              }));
              out.subtasks.push({ title, file: base, events });
              seenSubtasks.add(base);
            }
          }
        } catch {}
        for (const subtask of _subtaskSessionsForParent(name)) {
          if (seenSubtasks.has(subtask.name)) continue;
          out.subtasks.push({
            title: subtask.title,
            file: subtask.name,
            session: subtask.name,
            events: _traceEventsForSession(subtask.name, full),
          });
        }
        const deckRel = _chatDeckRel(dir, name);
        const [htmls, pngs] = _deckCountsForTrace(deckRel);
        out.kind = 'ppt';
        out.deck = { htmls, pngs };
        out.deck_rel = deckRel || '';
        out.deck_score = deckRel ? readDeckScore(deckRel) : null;
        out.ai_reports = readAiReports(name);
        out.preview_url = _tracePreviewUrl(deckRel, htmls, pngs);
        out.finished = htmls + pngs > 0;
        try {
          const sessionText = (session.messages || []).map(message => String(message.content || '')).join('\n');
          out.ppt_finished = /<ppt_task_finished>/.test(sessionText);
        } catch { out.ppt_finished = false; }
        out.main_meta = _traceMainMeta(out.main, out.subtasks, session.messages || []);
        sendJson(out);
        return;
      }
      if (parts.length === 4 && parts[3] === 'ppt-outline') {
        const outlinePath = resolveSessionPptOutline(name, {
          sessionsDir: activeSessionsRoot(),
          workspaceRoot: LOCAL_PPT_WORKSPACE_ROOT,
          virtualDeckRoot: LOCAL_PPT_DECK_ROOT,
        });
        if (!outlinePath) { sendJson({ error: 'PPT outline is unavailable for this session' }, 404); return; }
        const content = readStripped(outlinePath);
        if (content === null) { sendJson({ error: 'PPT outline could not be read' }, 404); return; }
        const limit = 200 * 1024;
        sendJson({ content: content.slice(0, limit), truncated: content.length > limit });
        return;
      }
      if (parts.length === 4 && parts[3] === "audio") {
        sendJson({ audio: listSessionAudio(name) });
        return;
      }
      if (parts.length === 5 && parts[3] === "audio") {
        const msgId = sanitizeMsgId(parts[4]);
        const fp = path.join(activeSessionsRoot(), name, "audio", "msg-" + msgId + ".wav");
        if (!msgId || !fs.existsSync(fp)) { sendJson({ error: "audio not found" }, 404); return; }
        const data = fs.readFileSync(fp);
        res.writeHead(200, { "Content-Type": "audio/wav", "Content-Length": data.length });
        res.end(data);
        return;
      }
      if (parts.length === 4 && parts[3] === "messages") {
        // Pagination params. `beforeOrdinal` paginates upward (used by the
        // Virtuoso scroll-up handler); `afterOrdinal` paginates downward and
        // is what the live-poll path uses to fetch only the messages that
        // arrived since the last poll, instead of re-downloading the whole
        // tail every 3 s. `limit` defaults to 50.
        const limitRaw = url.searchParams.get("limit");
        const limit = Math.max(1, Math.min(Number(limitRaw) || 50, 500));
        const beforeOrdinalRaw = url.searchParams.get("before_ordinal");
        const afterOrdinalRaw = url.searchParams.get("after_ordinal");
        const beforeOrdinal = beforeOrdinalRaw == null ? null : Number(beforeOrdinalRaw);
        const afterOrdinal = afterOrdinalRaw == null ? null : Number(afterOrdinalRaw);
        const all = url.searchParams.get("all") === "1";
        const paged = getSessionPaged(name, { limit, beforeOrdinal, afterOrdinal, all }, sessionScope(req));
        const raw = paged.messages;
        let savedModel = null;
        let savedUsage = null;
        // Prefer the DB for the meta side too: the previous block re-read
        // and re-parsed the same session JSON that getSession() just used
        // only to fish out `model`, which on a multi-MB session file was a
        // second full-file parse on every chat open. usage.json is a small
        // separate file and stays on disk; the DB does not mirror its
        // contents (separate import step would be busy-work for a few-KB
        // file).
        try {
          const meta = memoryDb ? sessionStore.readSessionMeta(memoryDb, name, sessionScope(req)) : null;
          if (meta) {
            savedModel = meta.model || null;
          } else {
            const sessRaw = readStripped(path.join(activeSessionsRoot(), name, name + '.json'));
            if (sessRaw) {
              const sessData = JSON.parse(sessRaw);
              savedModel = sessData.model || null;
            }
          }
          if (savedModel && apiModelToUi[savedModel]) savedModel = apiModelToUi[savedModel];
          const uf = readStripped(path.join(activeSessionsRoot(), name, 'usage.json'));
          if (uf) savedUsage = JSON.parse(uf);
        } catch {}
        savedUsage = usageWithMainTraceSpan(name, savedUsage);
        const prodData = convertToProductionFormat(raw, savedModel);
        if (savedUsage) prodData.data.usage = savedUsage;
        // Merge our paging info into the existing convertToProductionFormat
        // paging block so the client gets the new fields alongside the old
        // offset/limit/total it already knew about. Overwrite limit/total
        // so the values match what we actually returned, not the legacy
        // placeholder.
        prodData.data.paging = Object.assign({}, prodData.data.paging, paged.paging);
        sendJson(prodData);
        return;
      }
      if (parts.length === 4 && parts[3] === 'events') {
        // Read the same tree requireSessionAccess just authorised: a bridge
        // declares its conversation on the query string, and the binding decides
        // which account's run it is allowed to follow.
        const bridged = boundChannelScope(url.searchParams.get('channel'), url.searchParams.get('conversation_id'));
        if (bridged === null) { sendJson({ error: 'session not found' }, 404); return; }
        const stream = () => {
          const streamState = sessionEventStreams.get(sessionRunKey(name));
          const requestedRun = String(url.searchParams.get('run_id') || '');
          const since = Math.max(0, Number(url.searchParams.get('since')) || 0);
          res.writeHead(200, {
            'Content-Type': 'text/event-stream; charset=utf-8',
            'Cache-Control': 'no-cache, no-transform',
            Connection: 'keep-alive',
            'X-Accel-Buffering': 'no',
          });
          res.write(': fairy session event stream\n\n');

          let closed = false;
          const writeEvent = event => {
            if (closed || res.writableEnded) return;
            if (event && event.event_id) res.write('id: ' + event.event_id + '\n');
            res.write('data: ' + JSON.stringify(event) + '\n\n');
          };
          const endStream = () => {
            if (closed || res.writableEnded) return;
            closed = true;
            res.write('data: [DONE]\n\n');
            res.end();
          };

          if (!streamState || (requestedRun && streamState.runId !== requestedRun)) {
            writeEvent({
              type: 'stream_state',
              session: name,
              run_id: requestedRun || null,
              finished: true,
              event_id: since,
            });
            endStream();
            return;
          }

          for (const event of streamState.events) {
            if (event.event_id > since) writeEvent(event);
          }
          if (streamState.finished) {
            endStream();
            return;
          }

          const subscriber = (event, phase) => {
            if (phase === 'done') {
              endStream();
              return;
            }
            if (event && event.event_id > since) writeEvent(event);
          };
          streamState.subscribers.add(subscriber);
          req.on('close', () => {
            closed = true;
            streamState.subscribers.delete(subscriber);
          });
        };
        if (bridged === undefined) stream();
        else runInSessionScope(bridged, stream);
        return;
      }
      if (parts.length === 4 && parts[3] === 'status') {
        const run = runningChats.get(sessionRunKey(name));
        const streamState = sessionEventStreams.get(sessionRunKey(name));
        // Matched on the tree-qualified key, not the name: two accounts can both
        // be running a session called after today's date, and this answer is
        // about one of them.
        const sessionJobs = [...backgroundSubtaskJobs.values()].filter(job => job.parent_run_key === sessionRunKey(name));
        const backgroundRunning = sessionJobs.filter(job => job.status === 'running' || job.status === 'waiting').length;
        const resumePending = [...pendingContinuations.values()].some(cont => cont.session === name && (cont.status === 'ready' || cont.status === 'resuming'));
        sendJson({
          ok: true,
          running: !!run,
          started_at: run ? run.startedAt : null,
          elapsed_ms: run ? (Date.now() - run.startedAt) : 0,
          run_id: run ? run.runId : (streamState ? streamState.runId : null),
          event_seq: streamState ? streamState.nextEventId : 0,
          stream_finished: streamState ? streamState.finished : true,
          background_running: backgroundRunning,
          background_jobs: sessionJobs.length,
          background_job_list: sessionJobs.map(job => ({
            job_id: job.job_id,
            title: job.title || '',
            branch_session: job.branch_session || '',
            status: job.status || '',
          })),
          resume_pending: resumePending,
        });
        return;
      }
      if (parts.length === 4 && parts[3] === 'download') {
        // Download the whole session folder (chat-xxx/) as a zip archive.
        const dir = path.join(activeSessionsRoot(), name);
        if (!fs.existsSync(dir)) { sendJson({ error: 'session not found: ' + name }, 404); return; }
        let data;
        try { data = createSessionArchive(dir); }
        catch (error) { sendJson({ error: 'zip failed: ' + error.message }, 500); return; }
        res.writeHead(200, {
          "Content-Type": "application/zip",
          "Content-Disposition": 'attachment; filename="' + name + '.zip"',
          "Content-Length": data.length,
        });
        res.end(data);
        return;
      }

    }
  }

  if (method === "POST" || method === "PUT" || method === "DELETE") {
    const parts = pathname.split("/").filter(Boolean);
    if (parts.length === 4 && parts[0] === "api" && parts[1] === "sessions" && parts[3] === "real") {
      const name = decodeURIComponent(parts[2]);
      let body = '';
      req.on('data', chunk => { body += chunk; });
      req.on('end', () => {
        try {
          const payload = JSON.parse(body || '{}');
          const realMs = Number(payload.real_ms) || 0;
          if (realMs > 0) attachRealMsToSession(name, realMs);
          sendJson({ ok: true });
        } catch (e) {
          sendJson({ ok: false, error: e.message }, 400);
        }
      });
      return;
    }
    if (parts.length === 4 && parts[0] === "api" && parts[1] === "sessions" && parts[3] === "stop") {
      // Dedicated stop event: kill the running agent for this session AND its
      // whole process tree (create_subtask/run.exe + subtask agent-loop.exe)
      // so stopping actually halts PPT generation instead of leaving orphans.
      const name = decodeURIComponent(parts[2]);
        const run = runningChats.get(sessionRunKey(name));
      let stopped = false;
      if (run && run.child) {
        run.stoppedByUser = true;
        try {
          killProcessTree(run.child.pid, "SIGTERM");
          stopped = true;
        } catch {
          try { run.child.kill(); stopped = true; } catch {}
        }
      }
      sendJson({ ok: true, stopped });
      return;
    }
    if (parts.length === 4 && parts[0] === "api" && parts[1] === "sessions" && parts[3] === "audio") {
      const name = decodeURIComponent(parts[2]);
      let body = '';
      req.on('data', chunk => { body += chunk; });
      req.on('end', () => {
        try {
          const payload = JSON.parse(body || '{}');
          const msgId = sanitizeMsgId(String(payload.message_id || ''));
          const wavB64 = String(payload.wav_b64 || '');
          if (!msgId || !wavB64) { sendJson({ ok: false, error: 'message_id and wav_b64 required' }, 400); return; }
          const audioDir = path.join(activeSessionsRoot(), name, "audio");
          fs.mkdirSync(audioDir, { recursive: true });
          fs.writeFileSync(path.join(audioDir, "msg-" + msgId + ".wav"), Buffer.from(wavB64, "base64"));
          sendJson({ ok: true });
        } catch (e) { sendJson({ ok: false, error: e.message }, 400); }
      });
      return;
    }
    if ((method === "POST" || method === "PUT") && pathname === "/api/settings/tool") {
      let body = '';
      req.on('data', chunk => { body += chunk; });
      req.on('end', () => {
        try {
          const payload = JSON.parse(body || '{}');
          sendJson({ ok: true, tool: writeToolSetting(payload.id || payload.name, payload.enabled) });
        } catch (e) {
          sendJson({ ok: false, error: e.message || String(e) }, 400);
        }
      });
      return;
    }

    if ((method === "POST" || method === "PUT") && pathname === "/api/settings/skill") {
      let body = '';
      req.on('data', chunk => { body += chunk; });
      req.on('end', () => {
        try {
          const payload = JSON.parse(body || '{}');
          sendJson({ ok: true, skill: writeSkillSetting(payload.name, payload.enabled) });
        } catch (e) {
          sendJson({ ok: false, error: e.message || String(e) }, 400);
        }
      });
      return;
    }

    if (method === "POST" && pathname === "/api/settings/skills") {
      let body = '';
      req.on('data', chunk => { body += chunk; });
      req.on('end', () => {
        try {
          const payload = JSON.parse(body || '{}');
          sendJson({ ok: true, skill: addSkillSetting(payload) });
        } catch (e) {
          sendJson({ ok: false, error: e.message || String(e) }, 400);
        }
      });
      return;
    }

    if ((method === "POST" || method === "PUT") && pathname === "/api/settings") {
      let body = '';
      req.on('data', chunk => { body += chunk; });
      req.on('end', () => {
        try {
          const patch = JSON.parse(body || '{}');
          sendJson({ ok: true, settings: writeUISettings(patch) });
        } catch (e) {
          sendJson({ ok: false, error: e.message || String(e) }, 400);
        }
      });
      return;
    }

    // ── /api/models 管理（添加 / 测试 / 删除） ──
    if ((method === "POST" || method === "DELETE") && parts.length >= 2 && parts[0] === "api" && parts[1] === "models") {
      let body = '';
      req.on('data', chunk => { body += chunk; });
      req.on('end', async () => {
        try {
          // 0) POST /api/models/list — 拉取兼容 OpenAI 协议的模型清单
          if (method === "POST" && (parts.length === 3 && parts[2] === "list")) {
            let payload = {};
            try { payload = JSON.parse(body || '{}'); } catch {}
            const baseUrl = String(payload.base_url || '').trim().replace(/\/+$/, '');
            const apiKey = String(payload.api_key || '').trim();
            if (!baseUrl || !apiKey) {
              sendJson({ ok: false, error: 'base_url / api_key 必填' }, 400); return;
            }
            const url = baseUrl.endsWith('/models') ? baseUrl : baseUrl + '/models';
            const ac = new AbortController();
            const timer = setTimeout(() => ac.abort(), 12000);
            try {
              const r = await fetch(url, {
                headers: { 'Authorization': 'Bearer ' + apiKey },
                signal: ac.signal,
              });
              clearTimeout(timer);
              const text = await r.text().catch(() => '');
              if (!r.ok) {
                sendJson({ ok: false, error: 'HTTP ' + r.status + (text ? ' — ' + text.slice(0, 280) : '') }, 200);
                return;
              }
              let parsed = {};
              try { parsed = JSON.parse(text); } catch {}
              const rows = Array.isArray(parsed?.data) ? parsed.data
                : Array.isArray(parsed?.models) ? parsed.models
                : Array.isArray(parsed) ? parsed : [];
              const discovered = rows.map(item => (
                typeof item === 'string' ? item : (item?.id || item?.name || item?.model || '')
              )).map(x => String(x || '').trim()).filter(Boolean);
              const models = [...new Set([...knownProviderModels(baseUrl), ...discovered])];
              sendJson({ ok: true, models: models.slice(0, 500) });
            } catch (e) {
              clearTimeout(timer);
              sendJson({ ok: false, error: '请求失败：' + (e.message || e) }, 200);
            }
            return;
          }

          // 1) POST /api/models/test — 探测连通性
          if (method === "POST" && (parts.length === 3 && parts[2] === "test")) {
            let payload = {};
            try { payload = JSON.parse(body || '{}'); } catch {}
            const baseUrl = String(payload.base_url || '').trim();
            const apiKey = String(payload.api_key || '').trim();
            const model = String(payload.model || '').trim();
            if (!baseUrl || !apiKey || !model) {
              sendJson({ ok: false, error: 'base_url / api_key / model 必填' }, 400); return;
            }
            // 兼容 OpenAI Chat Completions：尝试用最小的非空消息探测
            const url = baseUrl.replace(/\/+$/, '') + '/chat/completions';
            const ac = new AbortController();
            const timer = setTimeout(() => ac.abort(), 12000);
            try {
              const r = await fetch(url, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json', 'Authorization': 'Bearer ' + apiKey },
                body: JSON.stringify({
                  model,
                  messages: [{ role: 'user', content: 'ping' }],
                  max_tokens: 1,
                  temperature: 0,
                  stream: false,
                }),
                signal: ac.signal,
              });
              clearTimeout(timer);
              if (r.ok) { sendJson({ ok: true, message: '连接成功（' + r.status + '）' }); return; }
              let detail = '';
              try { detail = (await r.text()).slice(0, 280); } catch {}
              sendJson({ ok: false, error: 'HTTP ' + r.status + (detail ? ' — ' + detail : '') }, 200);
            } catch (e) {
              clearTimeout(timer);
              sendJson({ ok: false, error: '请求失败：' + (e.message || e) }, 200);
            }
            return;
          }

          // 2) POST /api/models — 新增用户模型
          if (method === "POST" && parts.length === 2) {
            let payload = {};
            try { payload = JSON.parse(body || '{}'); } catch {}
            const id = String(payload.id || '').trim();
            const display = String(payload.display || id).trim();
            const provider = String(payload.provider || 'custom').trim();
            const baseUrl = String(payload.base_url || '').trim();
            const apiKey = String(payload.api_key || '').trim();
            const model = String(payload.model || '').trim();
            const temperature = Number(payload.temperature);
            const maxTokens = Number(payload.max_tokens);
            if (!/^[A-Za-z0-9_.\-:]{1,48}$/.test(id) || !/^user_/.test(id)) {
              sendJson({ ok: false, error: 'id 必须以 user_ 开头且仅含字母数字 _ . - :' }, 400); return;
            }
            if (!baseUrl || !apiKey || !model) {
              sendJson({ ok: false, error: 'base_url / api_key / model 必填' }, 400); return;
            }
            if (MODELS[id]) {
              sendJson({ ok: false, error: '模型 id 已存在' }, 409); return;
            }
            MODELS[id] = {
              display: display || id,
              provider,
              kind: 'real',
              config: APP_CONFIG,
              modelId: id,
              apiModel: model,
              override: {
                base_url: baseUrl,
                timeout_sec: 120,
                temperature: Number.isFinite(temperature) ? temperature : 0.4,
                max_tokens: Number.isFinite(maxTokens) ? maxTokens : 16384,
              },
              apiKey: apiKey,
              user: true,
            };
            try { saveModelRegistry(); }
            catch (e) { sendJson({ ok: false, error: '写入失败：' + (e.message || e) }, 500); return; }
            reloadModelRegistry();
            sendJson({ ok: true, id, display: display || id });
            return;
          }

          // 3) DELETE /api/models/:id — 删除用户模型
          if (method === "DELETE" && parts.length === 3) {
            const id = decodeURIComponent(parts[2]);
            const m = MODELS[id];
            if (!m) { sendJson({ ok: false, error: '模型不存在' }, 404); return; }
            if (!m.user) { sendJson({ ok: false, error: '内置模型不允许从前端删除' }, 403); return; }
            delete MODELS[id];
            try { saveModelRegistry(); }
            catch (e) { sendJson({ ok: false, error: '写入失败：' + (e.message || e) }, 500); return; }
            reloadModelRegistry();
            sendJson({ ok: true, id });
            return;
          }

          sendJson({ ok: false, error: 'unknown /api/models sub-route' }, 404);
        } catch (e) {
          sendJson({ ok: false, error: e.message || String(e) }, 500);
        }
      });
      return;
    }
    // not the real-ms route; fall through to /api/chat below
  }

  if (method === "DELETE") {
    const parts = pathname.split("/").filter(Boolean);
    if (parts.length === 4 && parts[0] === "api" && parts[1] === "memory" && parts[2] === "interactions") {
      const interactionId = decodeURIComponent(parts[3]);
      if (!isValidMemoryID(interactionId)) {
        sendJson({ ok: false, error: "invalid interaction id" }, 400);
        return;
      }
      let body = "";
      req.on("data", chunk => body += chunk);
      req.on("end", () => {
        let scope = "memory";
        let sessionName = "";
        let turnId = "";
        try {
          const payload = JSON.parse(body || "{}");
          if (payload && payload.scope) scope = String(payload.scope);
          if (payload && payload.session) sessionName = String(payload.session);
          if (payload && payload.turn_id) turnId = String(payload.turn_id);
        } catch {}
        if (scope !== "memory" && scope !== "conversation+memory") {
          sendJson({ ok: false, error: "scope must be memory or conversation+memory" }, 400);
          return;
        }
        const conversationScope = scope === "conversation+memory";
        try {
          const memory = deleteInteractionMemoryFiles(interactionId);
          const conversation = conversationScope
            ? removeInteractionFromConversations(interactionId, sessionName, turnId)
            : { deletedMessages: 0, deletedSessions: 0 };
          sendJson({
            ok: true,
            interaction_id: interactionId,
            scope,
            memory,
            conversation,
            attachments_deleted: false,
            artifacts_deleted: false,
          });
        } catch (error) {
          sendJson({ ok: false, error: error.message || String(error) }, 500);
        }
      });
      return;
    }
    if (parts.length === 3 && parts[0] === "api" && parts[1] === "sessions") {
      // 主/分支会话都允许手动删除（按主人要求拆掉 "lifecycle-managed" 闸口）；
      // 真正的清理路径仍然是 DELETE /api/memory/interactions/:id。
      const target = decodeURIComponent(parts[2]);
      const fp = path.join(activeSessionsRoot(), target, target + ".json");
      if (!fs.existsSync(fp)) {
        sendJson({ ok: false, error: "session not found" }, 404);
        return;
      }
      try {
        if (typeof runningChats !== "undefined" && runningChats && runningChats.has && runningChats.has(sessionRunKey(target))) runningChats.delete(sessionRunKey(target));
        if (typeof sessionEventStreams !== "undefined" && sessionEventStreams && sessionEventStreams.delete) sessionEventStreams.delete(sessionRunKey(target));
        fs.rmSync(path.join(activeSessionsRoot(), target), { recursive: true, force: true });
        sendJson({ ok: true, name: target });
      } catch (error) {
        sendJson({ ok: false, error: error.message || String(error) }, 500);
      }
      return;
    }
    sendJson({ error: "not found" }, 404);
    return;
  }

  // 每日主会话名由系统按当日日期生成。跨零点后，当天的根 session 可能
  // 要等会话开始落盘后才出现；此时若直接 404，定时任务会在每天 00:00~00:0X
  // 空转若干轮。这里返回不超过 upperBound 的、已存在根 json 的最新每日主会话，
  // 找不到时返回 null，由调用方决定怎么处理。
  function findLatestExistingDailySession(upperBound) {
    const root = activeSessionsRoot();
    if (!fs.existsSync(root)) return null;
    let entries = [];
    try { entries = fs.readdirSync(root); } catch (e) { return null; }
    let best = null;
    for (const entry of entries) {
      if (!/^\d{4}-\d{2}-\d{2}$/.test(entry)) continue;
      if (typeof upperBound === 'string' && upperBound && entry > upperBound) continue;
      if (best !== null && entry <= best) continue;
      if (!fs.existsSync(path.join(root, entry, entry + '.json'))) continue;
      best = entry;
    }
    return best;
  }

  // 删除「昨天及更早的每日空主会话」：用于实现
  // "如果当天创建的新会话没有被使用那就删掉，实际使用的才保留"。
  // 命中规则：messages 数组为空；usage 全零；目录内没有 subtasks/；
  //          同时确保没有其它分支把它的会话名当 parent_session。
  function sweepUnusedDailySessions(currentDate) {
    const removed = [];
    const kept = [];
    if (!fs.existsSync(activeSessionsRoot())) return { removed, kept };
    const isDateName = v => /^\d{4}-\d{2}-\d{2}$/.test(v);
    let entries = [];
    try { entries = fs.readdirSync(activeSessionsRoot()); } catch { return { removed, kept }; }
    for (const entry of entries) {
      if (!isDateName(entry) || entry >= currentDate) continue;
      const dir = path.join(activeSessionsRoot(), entry);
      const fp = path.join(dir, entry + '.json');
      if (!fs.existsSync(fp)) { kept.push({ name: entry, reason: 'no-json' }); continue; }
      let data = {};
      try { data = JSON.parse(readStripped(fp) || '{}'); } catch { kept.push({ name: entry, reason: 'bad-json' }); continue; }
      const messages = Array.isArray(data.messages) ? data.messages : [];
      if (messages.length > 0) { kept.push({ name: entry, reason: 'has-messages' }); continue; }
      const usage = data.usage || {};
      const u = (Number(usage.prompt_tokens) || 0) + (Number(usage.completion_tokens) || 0) + (Number(usage.duration_ms) || 0);
      if (u > 0) { kept.push({ name: entry, reason: 'has-usage' }); continue; }
      const subtasksDir = path.join(dir, 'subtasks');
      if (fs.existsSync(subtasksDir)) { kept.push({ name: entry, reason: 'has-subtasks-dir' }); continue; }
      let hasBranchChild = false;
      for (const other of entries) {
        if (other === entry) continue;
        const ofp = path.join(activeSessionsRoot(), other, other + '.json');
        if (!fs.existsSync(ofp)) continue;
        try {
          const od = JSON.parse(readStripped(ofp) || '{}');
          if (od.parent_session === entry) { hasBranchChild = true; break; }
        } catch {}
      }
      if (hasBranchChild) { kept.push({ name: entry, reason: 'has-branch-child' }); continue; }
      try {
        fs.rmSync(dir, { recursive: true, force: true });
        removed.push(entry);
      } catch (e) {
        kept.push({ name: entry, reason: 'rm-failed:' + e.message });
      }
    }
    return { removed, kept };
  }

  if (method === "POST" && pathname === "/api/sessions") {
    let body = "";
    req.on("data", chunk => body += chunk);
    req.on("end", () => {
      try {
        const data = JSON.parse(body || "{}");
        const requestedName = String(data.name || "").trim();
        // let：父会话缺失时下面会把它回退到最近一个已存在的每日主会话
        let parentSession = String(data.parent_session || data.parent || "").trim();
        const domain = String(data.domain || data.title || "").trim();
        const requestedKind = String(data.kind || "").trim();
        const today = new Date().toLocaleDateString('en-CA', { timeZone: 'Asia/Shanghai' });
        // 创建「今日主会话」之前先清扫昨天及更早的每日空主会话
        sweepUnusedDailySessions(today);
        const isDateName = value => /^\d{4}-\d{2}-\d{2}$/.test(value);
        const slugify = value => {
          const parts = String(value || '').trim().match(/[\p{L}\p{N}]+/gu) || [];
          return parts.join('-').slice(0, 48) || 'branch';
        };
        let name = requestedName;
        let kind = requestedKind || 'main';
        let parent = null;
        let dailyDate = null;
        // 声明在 if (parentSession) 块外：写入段在块尾还要读它
        let reuseExisting = false;
        if (parentSession) {
          let parentFile = path.join(activeSessionsRoot(), parentSession, parentSession + '.json');
          if (!fs.existsSync(parentFile) && /^\d{4}-\d{2}-\d{2}$/.test(parentSession)) {
            // 每日主会话名由系统按当日日期生成，不是用户手输的分支名。跨零点后
            // 当天根 session 可能要等主会话落盘后才出现，此刻直接 404 会让定时任务
            // 每天 00:00~00:0X 空转若干轮（实测已连续 96 次）。这里回退到最近一个
            // 已存在根 json、且不晚于请求日期的每日主会话。
            const fallback = findLatestExistingDailySession(parentSession);
            if (fallback) parentSession = fallback;
          }
          parentFile = path.join(activeSessionsRoot(), parentSession, parentSession + '.json');
          if (!fs.existsSync(parentFile)) {
            sendJson({ ok: false, error: 'parent session not found: ' + parentSession }, 404);
            return;
          }
          parent = parentSession;
          kind = 'branch';
          if (!name) name = parentSession + '__' + slugify(domain || 'branch');
          // 显式传了名字，且该名字已经是一个挂在同一个 parent 下的分支：
          // 这是调度器每轮都用同一个 targetSession 重复调用造成的幂等请求，
          // 必须复用原会话。之前这里无条件进入去重循环，于是每轮都新建
          // "<完整名>-2/-3/-4" 空壳，在侧边栏堆成一串"（分支会话，空的）"。
          // 注意只有"显式传名"才复用：UI 手动连点两次新建分支时 name 是空的，
          // 走 slugify 派生，那种情况仍然应当去重出 -2、-3，保持原有语义。
          if (requestedName) {
            const candidateFp = path.join(activeSessionsRoot(), name, name + '.json');
            if (fs.existsSync(candidateFp)) {
              try {
                const existing = JSON.parse(readStripped(candidateFp) || '{}');
                const sameParent = !existing.parent_session || existing.parent_session === parentSession;
                if (existing.kind === 'branch' && sameParent) reuseExisting = true;
              } catch { /* 坏 JSON 走正常创建路径 */ }
            }
          }
          const baseName = name;
          for (let attempt = 2; !reuseExisting && fs.existsSync(path.join(activeSessionsRoot(), name)); attempt++) {
            name = baseName + '-' + attempt;
          }
        } else if (!name) {
          name = today;
          kind = 'main';
          dailyDate = today;
        } else if (!isDateName(name) && !fs.existsSync(path.join(activeSessionsRoot(), name))) {
          sendJson({ ok: false, error: 'top-level sessions must be daily YYYY-MM-DD; use parent_session for branches' }, 400);
          return;
        } else if (isDateName(name) && name !== today && !fs.existsSync(path.join(activeSessionsRoot(), name))) {
          // A daily conversation belongs to its own day. Creating one for another
          // date would be a second top-level conversation that the sidebar shows
          // next to today's, which is exactly the shape this rule removes.
          sendJson(
            {
              ok: false,
              code: 'top_level_only_today',
              error: `顶层会话只能是今天的「${today}」；其它对话请作为它的子会话建立`,
            },
            400
          );
          return;
        }
        if (kind === 'main' && !dailyDate) dailyDate = isDateName(name) ? name : today;
        const dir = path.join(activeSessionsRoot(), name);
        fs.mkdirSync(dir, { recursive: true });
        const fp = path.join(dir, name + '.json');
        let session = {};
        if (fs.existsSync(fp)) {
          try { session = JSON.parse(readStripped(fp) || '{}'); } catch {}
        }
        session.messages = Array.isArray(session.messages) ? session.messages : [];
        session.model = session.model || null;
        session.kind = session.kind || kind;
        session.parent_session = session.parent_session || parent;
        session.domain = session.domain || domain || null;
        session.created_by = session.created_by || data.created_by || (parent ? 'model' : 'user');
        session.created_at = session.created_at || new Date().toISOString();
        if (dailyDate) session.daily_date = session.daily_date || dailyDate;
        if (reuseExisting) session.reused_at = new Date().toISOString();
        fs.writeFileSync(fp, JSON.stringify(session, null, 2), 'utf-8');
        sendJson({ ok: true, name, kind: session.kind, parent_session: session.parent_session, domain: session.domain });
      } catch (e) { sendJson({ ok: false, error: e.message }, 400); }
    });
    return;
  }

  // ---- 仅测试用的"活跃 run"钩子：默认整段不执行 ----------------------------
  // 存在的唯一理由：/api/chat/inject 的 409 闸门查 runningChats，要拿到真实
  // ok:true 就必须先有一个活跃 run，而仓库里没有假模型/桩 provider，起真 run
  // 要真调模型。这个钩子让自动化测试能在**完全不碰模型**的前提下端到端验证
  // 注入链路——而且是真子进程当靶子，能断言报文真的落到了 agent 的 stdin，
  // 不只是 ok 标志位为真。
  // 不设 FAIRY_TEST_INJECT_HOOK=1 时下面整块跳过，路由不存在。
  if (process.env.FAIRY_TEST_INJECT_HOOK === "1") {
    const tkey = (s) => sessionRunKey("test:" + s);
    const readBody = (cb) => {
      let raw = ""; req.on("data", (c) => raw += c);
      req.on("end", () => { try { cb(JSON.parse(raw || "{}")); } catch { sendJson({ ok: false, error: "bad json" }, 400); } });
    };
    if (method === "POST" && pathname === "/api/chat/_test/attach-run") {
      readBody((d) => {
        const s = String(d.session || "").trim();
        if (!s) return sendJson({ ok: false, error: "session is required" }, 400);
        const child = spawn(process.execPath, ["-e", testEcho], { stdio: ["pipe", "pipe", "ignore"] });
        testInbox.set(tkey(s), []);
        child.stdout.on("data", (c) => {
          const arr = testInbox.get(tkey(s));
          if (arr) String(c).split("\n").forEach((l) => { if (l) arr.push(l); });
        });
        runningChats.set(sessionRunKey(s), { startedAt: Date.now(), child, runId: "test-" + Date.now() });
        injectRegistry.attach(s, child);
        sendJson({ ok: true, session: s, run_id: runningChats.get(sessionRunKey(s)).runId });
      });
      return;
    }
    if (method === "GET" && pathname === "/api/chat/_test/received") {
      const s = String(new URL(req.url, "http://t").searchParams.get("session") || "").trim();
      return sendJson({ ok: true, session: s, received: testInbox.get(tkey(s)) || [] });
    }
    if (method === "POST" && pathname === "/api/chat/_test/detach-run") {
      readBody((d) => {
        const s = String(d.session || "").trim();
        const child = runningChats.get(sessionRunKey(s))?.child;
        runningChats.delete(sessionRunKey(s));
        injectRegistry.detach(s);
        if (child) { try { child.kill("SIGKILL"); } catch {} }
        sendJson({ ok: true, session: s, detached: true });
      });
      return;
    }
  }


  if (method === "POST" && (pathname === "/api/chat/inject" || pathname === "/api/chat/cancel" || pathname === "/api/chat/stop")) {
    let body = "";
    req.on("data", chunk => body += chunk);
    req.on("end", () => {
      let data;
      try { data = JSON.parse(body || "{}"); } catch (e) { sendJson({ ok: false, error: "bad json" }, 400); return; }
      const channel = data.channel;
      const bridged = boundChannelScope(channel && channel.name, channel && channel.conversation_id);
      if (bridged === null) { sendJson({ ok: false, code: "unbound" }, 403); return; }
      const run = () => {
        // A bridge never names the session it wants to reach. Its own lock key -
        // "qq:c2c:<openid>" - is not a session name at all, so honouring it made
        // every insertion answer "no active chat" and quietly degrade to
        // "稍后再发". The conversation's branch is derived exactly the way the
        // turn derived it, from the same channel and the same scope.
        const sessionName = String(
          bridged === undefined
            ? (data.session || data.sessionName || "")
            : channelSessionName(channel)
        ).trim();
        if (!sessionName) { sendJson({ ok: false, error: "session is required" }, 400); return; }
        const active = runningChats.get(sessionRunKey(sessionName));
        if (!active) {
          sendJson({ ok: false, error: "no active chat for session", session: sessionName }, 409);
          return;
        }
        if (pathname === "/api/chat/cancel") {
          const ok = injectRegistry.cancel(sessionName);
          sendJson({ ok, session: sessionName, run_id: active.runId || null, action: "cancel" });
          return;
        }
        if (pathname === "/api/chat/stop") {
          const ok = injectRegistry.stop(sessionName);
          sendJson({ ok, session: sessionName, run_id: active.runId || null, action: "stop" });
          return;
        }
        const text = sanitizeUserProtocolText(data.text || data.message || "").trim();
        if (!text) { sendJson({ ok: false, error: "text is required" }, 400); return; }
        const ok = injectRegistry.inject(sessionName, text);
        sendJson({ ok, session: sessionName, run_id: active.runId || null, action: "inject" });
      };
      if (bridged === undefined) run();
      else runInSessionScope(bridged, run);
    });
    return;
  }

  // ---- scheduled tasks ---------------------------------------------------
  //
  // A schedule belongs to an account, not to a conversation: "remind me at 8"
  // is about a person. Every handler scopes by sessionScope(req), the same way
  // sessions and bindings do, and the timer declares the owner it runs for.
  const scheduledTasksPath = () => path.join(REPO, "memory", "scheduled_tasks.json");
  function scheduledTaskOwner(req, data) {
    // The CLI an agent runs declares whose task it is - the same declaration the
    // scheduler makes when it runs that task - and only from loopback, so a
    // member's assistant can file a reminder in that member's own list.
    const declared = Number(data && data.session_owner);
    if (Number.isInteger(declared) && declared > 0 && auth.isLocalDirectRequest(req)) return declared;
    const scope = sessionScope(req);
    if (scope === sessionStore.SINGLE_USER) return null;
    if (scope === sessionStore.ANONYMOUS) return undefined;
    const id = Number(scope);
    return Number.isInteger(id) && id > 0 ? id : undefined;
  }
  const schedulerDeps = () => ({
    storeFile: scheduledTasksPath(),
    apiBase: `http://127.0.0.1:${PORT}`,
    repoRoot: REPO,
    pythonBin: process.env.AGENT_PYTHON_BIN || "python3",
    defaultSessionName,
    defaultModel: () => String(readUISettings().default_model || "").trim(),
    log: message => console.log(message),
  });

  /** 微信桥落盘的时间戳：这个人上次跟 bot 说话是什么时候。 */
  function wechatLastSeenAt(conversationId) {
    try {
      const dir = process.env.FAIRY_WECHAT_DIR
        || path.join(require("os").homedir(), ".config", "fairy", "wechat");
      for (const file of fs.readdirSync(dir)) {
        if (!file.endsWith(".state.json")) continue;
        const raw = JSON.parse(fs.readFileSync(path.join(dir, file), "utf-8"));
        const entry = ((raw || {}).contexts || {})[conversationId];
        if (entry && Number(entry.at) > 0) return Number(entry.at);
      }
    } catch {}
    return 0;
  }

  /**
   * 默认把提醒推到哪里。
   *
   * 设置页和助手都要这个答案，而且都不该猜：提醒发到人不在的那条通道，就等于
   * 没提醒。规则是"这个账号最近接上的那条通道"——绑定时间是账号侧的证据，微信
   * 那边还有更准的一条（桥记下的"上次说话时间"），取两者里更新的。
   */
  function suggestedNotifyTarget(owner) {
    const userId = Number(owner || 0);
    if (!userId) return null;
    let best = null;
    try {
      for (const binding of auth.store.listChannelBindings(userId)) {
        const conversationId = String(binding.conversationId || binding.conversation_id || "");
        if (!conversationId) continue;
        const attachedAt = Number(binding.bound_at) || 0;
        const seenAt = binding.channel === "wechat" ? wechatLastSeenAt(conversationId) : 0;
        const score = Math.max(attachedAt, seenAt);
        if (!best || score > best.score) {
          best = { channel: binding.channel, conversation_id: conversationId, score };
        }
      }
    } catch {}
    return best ? { channel: best.channel, conversation_id: best.conversation_id } : null;
  }

  if (pathname === "/api/scheduled-tasks" && method === "GET") {
    const owner = scheduledTaskOwner(req);
    if (owner === undefined) { sendJson({ ok: false, code: "unauthenticated" }, 401); return; }
    sendJson({
      ok: true,
      tasks: scheduler.listTasks(scheduledTasksPath(), owner).map(scheduler.presentTask),
      meta: {
        timezone: scheduler.TZ,
        poll_ms: scheduler.DEFAULT_POLL_MS,
        minimum_every_minutes: scheduler.MIN_EVERY_MINUTES,
        // Filled in so neither the panel nor the assistant has to guess where a
        // reminder should land; see suggestedNotifyTarget.
        suggested_notify: suggestedNotifyTarget(owner),
      },
    });
    return;
  }

  if (method === "POST" && (pathname === "/api/scheduled-tasks" || pathname === "/api/scheduled-tasks/toggle"
    || pathname === "/api/scheduled-tasks/update"
    || pathname === "/api/scheduled-tasks/delete" || pathname === "/api/scheduled-tasks/run")) {
    let body = "";
    req.on("data", chunk => body += chunk);
    req.on("end", () => {
      let data;
      try { data = JSON.parse(body || "{}"); } catch { sendJson({ ok: false, error: "bad json" }, 400); return; }
      const owner = scheduledTaskOwner(req, data);
      if (owner === undefined) { sendJson({ ok: false, code: "unauthenticated" }, 401); return; }
      const file = scheduledTasksPath();

      if (pathname === "/api/scheduled-tasks") {
        const checked = scheduler.validateTask(data);
        if (checked.error) { sendJson({ ok: false, error: checked.error }, 400); return; }
        const task = scheduler.createTask(file, { owner, ...checked.value });
        sendJson({ ok: true, task: scheduler.presentTask(task) });
        return;
      }

      const id = String(data.id || "").trim();
      if (!id) { sendJson({ ok: false, error: "id is required" }, 400); return; }

      if (pathname === "/api/scheduled-tasks/delete") {
        const removed = scheduler.deleteTask(file, owner, id);
        sendJson(removed ? { ok: true } : { ok: false, error: "任务不存在" }, removed ? 200 : 404);
        return;
      }

      if (pathname === "/api/scheduled-tasks/toggle") {
        const task = scheduler.updateTask(file, owner, id, { enabled: !!data.enabled });
        if (!task) { sendJson({ ok: false, error: "任务不存在" }, 404); return; }
        sendJson({ ok: true, task: scheduler.presentTask(task) });
        return;
      }

      // Full update: the panel edits a task in place (time, prompt, notify...)
      // instead of forcing delete + recreate. updateTask reassigns the patch and,
      // when schedule changes, recomputes next_run_at.
      if (pathname === "/api/scheduled-tasks/update") {
        const checked = scheduler.validateTask(data);
        if (checked.error) { sendJson({ ok: false, error: checked.error }, 400); return; }
        const task = scheduler.updateTask(file, owner, id, checked.value);
        if (!task) { sendJson({ ok: false, error: "\u4efb\u52a1\u4e0d\u5b58\u5728" }, 404); return; }
        sendJson({ ok: true, task: scheduler.presentTask(task) });
        return;
      }

      // Run now: start it and answer immediately. A turn can take minutes, and
      // an HTTP request that waits for one is a timeout waiting to happen; the
      // panel refreshes and reads last_status instead.
      scheduler.runTaskNow(file, owner, id, schedulerDeps())
        .catch(error => console.error("[scheduled] run now failed:", error && error.message));
      sendJson({ ok: true, started: true });
    });
    return;
  }

  if (method === "POST" && pathname === "/api/ai-reports/delete") {
    let body = "";
    req.on("data", chunk => body += chunk);
    req.on("end", () => {
      let payload = {};
      try { payload = JSON.parse(body || "{}"); } catch {}
      sendJson({ ok: deleteAiReport(String(payload.session || ''), payload.at) });
    });
    return;
  }
  if (method === "POST" && pathname === "/api/deck-score") {
    let body = "";
    req.on("data", chunk => body += chunk);
    req.on("end", () => {
      let payload = {};
      try { payload = JSON.parse(body || "{}"); } catch {}
      const deckRel = String(payload.deck || '') || resolveDeckRelForSession(String(payload.session || ''));
      if (!deckRel) { sendJson({ error: 'deck not found for this session' }, 404); return; }
      const script = path.join(REPO, 'batch_eval', 'scripts', 'score_deck.py');
      const runner = path.join(REPO, 'scripts', 'python.mjs');
      if (!fs.existsSync(script)) { sendJson({ error: 'PPT 成品评分脚本尚未安装到当前项目' }, 501); return; }
      const args = [runner, script, '--deck', deckRel, '--workers', '3', '--retries', '1'];
      if (payload.force) args.push('--force');
      if (payload.pages) args.push('--pages', Array.isArray(payload.pages) ? payload.pages.join(',') : String(payload.pages));
      const child = spawn(process.execPath, args, { cwd: REPO });
      const job = { deck: deckRel, running: true, startedAt: Date.now(), log: '' };
      DECK_SCORE_JOBS.set(deckRel, job);
      const append = chunk => { job.log = (job.log + chunk.toString()).slice(-3000); };
      child.stdout.on('data', append);
      child.stderr.on('data', append);
      child.on('error', error => { job.running = false; job.error = String((error && error.message) || error); });
      child.on('exit', code => { job.running = false; job.code = code; });
      sendJson({ ok: true, deck: deckRel, running: true });
    });
    return;
  }
  if (method === "POST" && pathname === "/api/ai-analyze") {
    let body = "";
    req.on("data", chunk => body += chunk);
    req.on("end", () => {
      let payload = {};
      try { payload = JSON.parse(body || "{}"); } catch {}
      const errors = Array.isArray(payload.errors) ? payload.errors : [];
      if (!errors.length) { sendJson({ text: '该次运行没有错误操作，无需分析。' }); return; }
      const system = '你是 PPT 生成流程的性能与稳定性分析师。用户会提供一次运行中的报错操作清单（工具失败 / 闸门 / 行为违规）。请用简洁中文输出：① 总览：从错误分布看主要问题出在哪个阶段；② 逐条解释每个错误：发生在哪个工具、常见原因、对最终结果的影响（致命/可恢复/噪音）；③ 给 2-3 条最值得做的优化建议。只基于给到的数据推断，不要编造。';
      const user = '以下是该次运行中的报错操作（JSON）：\n' + JSON.stringify(errors, null, 1).slice(0, 12000) + '\n\n请按要求输出分析报告（分点，总长不超过 600 字）。';
      llmChatText(system, user)
        .then(text => {
          const report = { text, at: Date.now() };
          try { appendAiReport(String(payload.name || ''), report); } catch {}
          sendJson(report);
        })
        .catch(error => sendJson({ error: String((error && error.message) || error) }, 500));
    });
    return;
  }
  if (method === "POST" && pathname === "/api/chat") {
    let body = "";
    req.on("data", chunk => body += chunk);
    req.on("end", () => {
      let data;
      try { data = JSON.parse(body || "{}"); } catch (e) {
        sendJson({ ok: false, error: "bad json: " + e.message }, 400);
        return;
      }
      // Refuse an unbound bridge turn before the sessions tree is entered, so a
      // stranger's message leaves no transcript and costs no model run.
      const writeScope = chatWriteScope(req, data);
      if (writeScope === null) {
        sendJson({ ok: false, code: "unbound" }, 403);
        return;
      }
      // Everything below - the agent process, the child-process callbacks that
      // finish the turn, the background registries - runs inside the sessions
      // tree this write belongs to. Entering it here rather than at each use is
      // what keeps a resumed turn for one account from landing in another
      // account's transcript.
      runInSessionScope(writeScope, () => {

        const msg = sanitizeUserProtocolText(data.message || "").trim();
        const reqFiles = Array.isArray(data.files) ? data.files : [];
        const surface = data.surface === "voice" ? "voice" : "chat";
        // Runtime-injected turns (e.g. resuming a plan after a detached subtask
        // finished) are not user requests. They carry a control marker so the
        // harness keeps them out of the persisted transcript and the chat UI,
        // while still showing the active plan to the model.
        const internalType = typeof data.internal_type === "string" ? data.internal_type.trim() : "";
        // <file_context> only reaches the model; the UI keeps the raw text.
        let llmMsg = (msg || reqFiles.length) ? buildFileContextPrefix(reqFiles) + msg : "";
        if (internalType) llmMsg = ensureRuntimeControlMarker(llmMsg);
        const modelId = data.model || "";
        const uiSettings = readUISettings();
        const autoFallback = data.auto_fallback !== false && uiSettings.auto_fallback !== false;
        const requestedFallbackModel = autoFallback
          ? String(data.fallback_model || uiSettings.fallback_model || "").trim()
          : "";
        const fallbackModelId = requestedFallbackModel
          && requestedFallbackModel !== modelId
          && MODELS[requestedFallbackModel]
          && hasResolvedApiKey(MODELS[requestedFallbackModel].apiKey)
          ? requestedFallbackModel
          : "";
        // A bridge turn gets no say in which transcript it lands in: binding
        // exists so the conversation decides the owner, not the caller. What it
        // does land in is derived from the conversation itself (its own branch
        // of today's conversation), never from the name the bridge sent - a
        // stale or hostile name must not be able to pick a transcript.
        const sessionName = isChannelTurn(data)
          ? channelSessionName(data.channel)
          : data.session || defaultSessionName();
        if (!llmMsg) { sendJson({ ok: false, error: "empty message" }, 400); return; }
        if (!MODELS[modelId]) { sendJson({ ok: false, error: "unknown model: " + modelId }, 400); return; }
        // One account has exactly one top-level conversation per day, and
        // everything else hangs off it as a branch. Appending to a session that
        // already exists stays allowed - history has to remain readable - but a
        // *new* top-level name is refused, because that is how "why do I have
        // six conversations for today" happens. Branches are unaffected: they
        // are created through POST /api/sessions, so by the time a turn runs
        // their file already exists.
        if (!isChannelTurn(data) && sessionName !== defaultSessionName()) {
          const sessionFileForGuard = path.join(activeSessionsRoot(), sessionName, sessionName + ".json");
          if (!fs.existsSync(sessionFileForGuard)) {
            sendJson(
              {
                ok: false,
                code: "top_level_only_today",
                error: `顶层会话只能是今天的「${defaultSessionName()}」；其它对话请作为它的子会话建立（POST /api/sessions 带 parent_session）`,
              },
              400
            );
            return;
          }
        }
        // A resumed interaction must never race a foreground turn: two agents
        // writing the same session file would corrupt the transcript.
        if (runningChats.has(sessionRunKey(sessionName))) {
          sendJson({
            ok: false,
            error: internalType ? "agent 正在处理当前会话，后台任务结果稍后接续" : "agent 仍在处理当前会话，请稍候",
            retry: true,
          }, 409);
          return;
        }
        if (!hasResolvedApiKey(MODELS[modelId].apiKey)) {
          const unavailable = "model " + modelId + " is unavailable: missing API key file";
          if (data.stream) {
            res.writeHead(200, { "Content-Type": "text/event-stream; charset=utf-8", "Cache-Control": "no-cache" });
            res.write("event: error\ndata: " + JSON.stringify({ type: "error", session: sessionName, error: unavailable }) + "\n\n");
            res.write("data: [DONE]\n\n");
            res.end();
          } else {
            sendJson({ ok: false, error: unavailable }, 400);
          }
          return;
        }

        const sessionFile = path.join(activeSessionsRoot(), sessionName, sessionName + '.json');

        const sessionDir = path.join(activeSessionsRoot(), sessionName);
        if (!fs.existsSync(sessionDir)) fs.mkdirSync(sessionDir, { recursive: true });
        // A channel conversation is a branch of today's conversation. Write the
        // grouping metadata before the first turn so the sidebar files it there
        // instead of showing an orphan next to the day's conversation.
        if (isChannelTurn(data)) seedChannelSession(sessionFile, data.channel);
        if (data.stream) {
          res.writeHead(200, { "Content-Type": "text/event-stream; charset=utf-8", "Cache-Control": "no-cache" });
          const runId = require("crypto").randomUUID();
          createSessionEventStream(sessionName, runId);
          const sse = (event, data) => {
            const payload = publishSessionEvent(sessionName, data);
            try {
              res.write((event ? "event: " + event + "\n" : "") + "data: " + JSON.stringify(payload) + "\n\n");
            } catch {}
            return payload;
          };

          const m = MODELS[modelId];
          let configPath = m.config;
          let tmpCfg = null, userFile = null;
          try {
            configPath = prepareModelConfigPath(m);
            tmpCfg = configPath !== m.config ? configPath : null;
            userFile = path.join(require("os").tmpdir(), "user_msg_" + Date.now() + ".txt");
            fs.writeFileSync(userFile, llmMsg, "utf-8");

            const agentExe = resolveAgentExecutable(REPO);
            // create_subtask registers its branch directory before the child
            // process starts. Surface that branch as running immediately, then
            // tail its stream file so the sidebar can show live progress before
            // the blocking tool call returns.
            const subtaskStartedAt = Date.now();
            const runningBranches = new Set();
            const subtaskTails = new Map(); // stream path -> { title, offset }
            const subtaskTimer = setInterval(() => {
              let entries = [];
              try { entries = fs.readdirSync(activeSessionsRoot(), { withFileTypes: true }); } catch { return; }
              for (const entry of entries) {
                if (!entry.isDirectory()) continue;
                const branchName = entry.name;
                const branchFile = path.join(activeSessionsRoot(), branchName, branchName + ".json");
                let st, data;
                try {
                  st = fs.statSync(branchFile);
                  data = JSON.parse(readStripped(branchFile));
                } catch { continue; }
                if (data.parent_session !== sessionName) continue;
                if (st.mtimeMs < subtaskStartedAt - 2000) continue;

                if (!runningBranches.has(branchName)) {
                  runningBranches.add(branchName);
                  sse(null, {
                    type: "branch_created",
                    status: "running",
                    session: sessionName,
                    branch_session: branchName,
                    parent_session: sessionName,
                    domain: data.domain || branchName,
                  });
                }

                const streamPath = branchFile + ".stream";
                try {
                  const streamStat = fs.statSync(streamPath);
                  let t = subtaskTails.get(streamPath) || { title: data.domain || branchName, offset: 0 };
                  if (streamStat.size < t.offset) t = { title: t.title, offset: 0 };
                  if (streamStat.size <= t.offset) continue;
                  const fd = fs.openSync(streamPath, "r");
                  const buf = Buffer.alloc(streamStat.size - t.offset);
                  fs.readSync(fd, buf, 0, buf.length, t.offset);
                  fs.closeSync(fd);
                  t.offset = streamStat.size;
                  for (const line of buf.toString("utf-8").split("\n")) {
                    const text = line.trim();
                    if (!text) continue;
                    try {
                      const ev = JSON.parse(text);
                      if (ev.type === "subtask_start") t.title = ev.title || t.title;
                      if (ev.type && t.title) sse(null, { type: "subtask_event", title: t.title, event: ev });
                    } catch {}
                  }
                  subtaskTails.set(streamPath, t);
                } catch {}
              }
            }, 500);

            // Stdin pipe lets the host push inject/cancel messages into the agent
  // without spawning a new one.
            const child = spawn(agentExe, [
              "-ConfigPath", configPath,
              "-ModelId", m.modelId,
              ...(fallbackModelId ? ["-FallbackModel", fallbackModelId] : []),
              "-UseMock", String(m.kind === "mock"),
              "-UserOverrideFile", userFile,
              "-SessionFile", sessionFile,
              "-InjectStdin"
            ], buildAgentProcessOptions(REPO, { stdio: ["pipe", "pipe", "pipe"] }));
            injectRegistry.attach(sessionName, child);
          runningChats.set(sessionRunKey(sessionName), { startedAt: Date.now(), child, runId });

            let buf = "";
            let errBuf = "";
            child.stdout.on("data", chunk => {
              buf += chunk.toString("utf-8");
              const lines = buf.split("\n");
              buf = lines.pop() || "";
              for (const line of lines) {
                const t = line.trim();
                if (!t) continue;
                try {
                  const ev = JSON.parse(t);
                  if (ev.type) {
                    // Real-time subtask card enrichment: replace the slim
                    // create_subtask result with the full process from the
                    // session file so the card shows everything immediately.
                    if (ev.type === 'tool_call' && ev.status === 'end' && ev.tool === 'create_subtask' && ev.result) {
                      const background = registerBackgroundSubtask(ev.result, sessionName, modelId);
                      if (!background) ev.result = enrichCreateSubtaskResult(ev.result, sessionName);
                      const parsed = parseObject(ev.result);
                      if (parsed && parsed.branch_session) {
                        sse(null, {
                          type: 'branch_created',
                          status: parsed.status === 'running' ? 'running' : 'finished',
                          session: sessionName,
                          branch_session: parsed.branch_session,
                          parent_session: parsed.parent_session || sessionName,
                          domain: parsed.domain || parsed.title || '',
                        });
                      }
                    } else if (ev.type === 'tool_result' && ev.name === 'create_subtask' && ev.result) {
                      const background = registerBackgroundSubtask(ev.result, sessionName, modelId);
                      if (!background) ev.result = enrichCreateSubtaskResult(ev.result, sessionName);
                      const parsed = parseObject(ev.result);
                      if (parsed && parsed.branch_session) {
                        sse(null, {
                          type: 'branch_created',
                          status: parsed.status === 'running' ? 'running' : 'finished',
                          session: sessionName,
                          branch_session: parsed.branch_session,
                          parent_session: parsed.parent_session || sessionName,
                          domain: parsed.domain || parsed.title || '',
                        });
                      }
                    }
                    ev.session = sessionName;
                    sse(null, ev);
                  }
                } catch {}
              }
            });

            child.stderr.on("data", chunk => {
              errBuf += chunk.toString("utf-8");
            });

            child.on("close", code => {
            const runInfo = runningChats.get(sessionRunKey(sessionName));
            const stoppedByUser = !!(runInfo && runInfo.stoppedByUser);
            runningChats.delete(sessionRunKey(sessionName));
              injectRegistry.detach(sessionName);
              // Foreground work has priority. If a detached subtask completed
              // while this turn was running, resume the waiting interaction only
              // after the session file is free again.
              setTimeout(() => maybeResumePendingContinuation(sessionName), 0);
              // Free the viewer's seen-paths memo so a future turn in the same
              // session can re-prompt for the same file.
              fm.cleanupSession(sessionName);
              if (buf.trim()) {
                try { const ev = JSON.parse(buf.trim()); if (ev.type) { ev.session = sessionName; sse(null, ev); } } catch {}
              }
              if (code !== 0) {
                if (stoppedByUser) {
                  // user pressed Stop: not an error, end the stream cleanly.
                  sse("done", { type: "done", session: sessionName });
                } else {
                  const errMsg = formatHarnessExitError(code, errBuf);
                  appendErrorToSession(sessionFile, errMsg);
                  sse("error", { type: "error", session: sessionName, error: errMsg });
                }
              } else if (fs.existsSync(sessionFile)) {
                try {
                  const raw = readStripped(sessionFile);
                  const session = JSON.parse(raw);
                  if (!session.model) {
                    session.model = modelId;
                    try { fs.writeFileSync(sessionFile, JSON.stringify(session, null, 2), "utf-8"); } catch {}
                  }
                  // Discover result artifacts from the final turn itself: explicit
                  // show_result calls, PPT completion markers, and viewable paths in
                  // the final assistant text. Text chat renders the shared artifact
                  // card; voice opens files in filemanager or PPT in the preview page.
                  let latestInteractionId = '';
                  try {
                    const msgs = Array.isArray(session.messages) ? session.messages : [];
                    for (let i = msgs.length - 1; i >= 0; i--) {
                      const item = msgs[i];
                      if (item && item.role === 'user' && (item.interaction_id || item.turn_id)) {
                        latestInteractionId = String(item.interaction_id || item.turn_id);
                        break;
                      }
                    }
                    const discovery = discoverResultArtifacts(msgs);
                    let resultArtifacts = discovery.artifacts;
                    const reportBody = extractReportBody(discovery.finalAssistantText);
                    if (!resultArtifacts.length && reportBody) {
                      const reportPath = saveModelReport(sessionName, modelId, reportBody);
                      if (reportPath) {
                        resultArtifacts = [{
                          kind: 'report',
                          path: reportPath.replace(/\\/g, '/'),
                          name: path.basename(reportPath),
                          archived: true,
                        }];
                      }
                    }
                    if (surface === 'voice' && resultArtifacts.length) {
                      resultArtifacts.forEach(item => {
                        if (item.kind === 'ppt' && item.path) fm.spawnPptPreviewFor(item.path);
                        else if (item.path) fm.spawnFileManagerFor(item.path);
                      });
                    } else if (surface !== 'voice' && resultArtifacts.length) {
                      sse('file_result', {
                        type: 'file_result',
                        session: sessionName,
                        interaction_id: latestInteractionId || undefined,
                        files: resultArtifacts,
                      });
                    }
                  } catch {}
                  const usageFile = path.join(activeSessionsRoot(), sessionName, 'usage.json');
                  let sessUsage = null;
                  try { if (fs.existsSync(usageFile)) { sessUsage = usageWithMainTraceSpan(sessionName, JSON.parse(readStripped(usageFile))); } } catch {}
          const hasBackgroundRunning = [...backgroundSubtaskJobs.values()].some(job => job.parent_run_key === sessionRunKey(sessionName) && (job.status === 'running' || job.status === 'waiting'));
                  sse('done', { type: 'done', session: sessionName, interaction_id: latestInteractionId || undefined, messages: convertToProductionFormat(session.messages || [], session.model || modelId), usage: sessUsage, session_usage: sessUsage, total_usage: sessUsage, background_running: hasBackgroundRunning });
                } catch { sse('done', { type: 'done', session: sessionName }); }
              } else {
                sse("done", { type: "done", session: sessionName });
              }
              clearInterval(subtaskTimer);
              res.write("data: [DONE]\n\n");
              res.end();
              finishSessionEventStream(sessionName, runId);
              if (tmpCfg) try { fs.unlinkSync(tmpCfg); } catch {}
              if (userFile) try { fs.unlinkSync(userFile); } catch {}
            });

            child.on("error", err => {
              sse("error", { type: "error", session: sessionName, error: err.message });
              res.write("data: [DONE]\n\n");
              res.end();
              finishSessionEventStream(sessionName, runId);
              if (tmpCfg) try { fs.unlinkSync(tmpCfg); } catch {}
              if (userFile) try { fs.unlinkSync(userFile); } catch {}
            });
          } catch (e) {
            sse("error", { type: "error", session: sessionName, error: e.message });
            res.write("data: [DONE]\n\n");
            res.end();
            finishSessionEventStream(sessionName, runId);
            if (tmpCfg) try { fs.unlinkSync(tmpCfg); } catch {}
            if (userFile) try { fs.unlinkSync(userFile); } catch {}
          }
        } else {
          const m = MODELS[modelId];
          let configPath = m.config;
          let tmpCfg = null, userFile = null;
          try {
            configPath = prepareModelConfigPath(m);
            tmpCfg = configPath !== m.config ? configPath : null;
            userFile = path.join(require("os").tmpdir(), "user_msg_" + Date.now() + ".txt");
            fs.writeFileSync(userFile, llmMsg, "utf-8");

            const agentExe = resolveAgentExecutable(REPO);
            const proc = require("child_process").spawnSync(agentExe, [
              "-ConfigPath", configPath,
              "-ModelId", m.modelId,
              ...(fallbackModelId ? ["-FallbackModel", fallbackModelId] : []),
              "-UseMock", String(m.kind === "mock"),
              "-UserOverrideFile", userFile,
              "-SessionFile", sessionFile
            ], buildAgentProcessOptions(REPO, { encoding: "utf-8", maxBuffer: 50 * 1024 * 1024 }));

            if (proc.error) { sendJson({ ok: false, error: "spawn error: " + proc.error.message }); return; }
            if (proc.status !== 0) {
              const err = formatHarnessExitError(proc.status, proc.stderr);
              appendErrorToSession(sessionFile, err);
              sendJson({ ok: false, error: err }); return;
            }
            if (!fs.existsSync(sessionFile)) { sendJson({ ok: false, error: "no session file" }); return; }
            const raw = readStripped(sessionFile);
            const session = JSON.parse(raw);
            if (!session.model) {
              session.model = modelId;
              try { fs.writeFileSync(sessionFile, JSON.stringify(session, null, 2), "utf-8"); } catch {}
            }
            sendJson(convertToProductionFormat(session.messages || [], session.model || modelId));
          } finally {
            if (tmpCfg) try { fs.unlinkSync(tmpCfg); } catch {}
            if (userFile) try { fs.unlinkSync(userFile); } catch {}
          }
        }
      });
    });
    return;
  }

  sendJson({ error: "not found" }, 404);
}

const server = http.createServer((req, res) => {
  const scope = sessionScope(req);
  runInSessionScope(scope, () => {
    bindRequestContext(req);
    handleRequest(req, res);
  });
});

server.listen(PORT, '127.0.0.1', () => {
  console.log('[server] Node.js API server on http://127.0.0.1:' + PORT + '/');
  console.log('[server] Ctrl+C to stop.');
});

// The scheduler lives in this process because this process is the one that is
// always up. It polls a JSON file instead of holding one timer per task: a list
// that survives a crash and a restart is worth more than millisecond precision,
// and these are reminders, not trades.
const scheduledTasks = scheduler.startScheduler({
  storeFile: path.join(REPO, "memory", "scheduled_tasks.json"),
  apiBase: `http://127.0.0.1:${PORT}`,
  repoRoot: REPO,
  pythonBin: process.env.AGENT_PYTHON_BIN || "python3",
  defaultSessionName,
  defaultModel: () => String(readUISettings().default_model || "").trim(),
  log: message => console.log(message),
});
console.log(`[scheduled] 定时任务已启动（每 ${scheduler.DEFAULT_POLL_MS / 1000}s 检查一次，时区 ${scheduler.TZ}）`);

// A linked WeChat account keeps a detached bridge process, so a machine that
// rebooted has credentials on disk and nothing answering messages. Give the
// network a moment, then bring those back.
setTimeout(() => wechatLink.ensureBridges(), 8000).unref?.();

let shuttingDown = false;
function shutdownServer(signal) {
  if (shuttingDown) return;
  shuttingDown = true;
  console.log(`[server] ${signal} received; stopping active agent trees`);

  for (const run of runningChats.values()) {
    if (run && run.child && run.child.pid) killProcessTree(run.child.pid, "SIGTERM");
  }
  runningChats.clear();
  injectRegistry.sessions.clear();

  for (const state of sessionEventStreams.values()) {
    for (const subscriber of [...state.subscribers]) {
      try { subscriber(null, 'done'); } catch {}
    }
    state.subscribers.clear();
    if (state.cleanupTimer) clearTimeout(state.cleanupTimer);
  }
  sessionEventStreams.clear();

  // SSE clients can keep a connection open after the agent exits. Give them
  // a short drain window, then force the listener down so Ctrl+C cannot hang.
  const forceExit = setTimeout(() => process.exit(0), 5000);
  if (forceExit.unref) forceExit.unref();
  server.close(() => {
    clearTimeout(forceExit);
    process.exit(0);
  });
}

process.once('SIGINT', () => shutdownServer('SIGINT'));
process.once('SIGTERM', () => shutdownServer('SIGTERM'));
