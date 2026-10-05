/**
 * Fairy authentication: users, sessions, and IP-trusted devices.
 *
 * Deliberately dependency-free. Node 22 ships `node:sqlite` and `node:crypto`,
 * which cover a user table, a session table and a password hash, so exposing the
 * frontend to a LAN does not first require adopting a database server or a native
 * module that has to be compiled per platform.
 *
 * Threat model, stated plainly because the thing behind this gate can run shell
 * commands, drive the mouse and keyboard, and read every file the user can:
 *
 *   - Something scanning the network finds nothing but a login page.
 *   - Guessing is rate limited and costs a scrypt derivation per attempt.
 *   - Stealing the SQLite file does not hand over live sessions: tokens are
 *     stored as SHA-256 digests, so the file alone cannot be replayed.
 *   - "Remember this device" is an IP allow-list entry, not a longer cookie.
 *     It is convenience, not a second factor: anyone who can spoof the address
 *     inherits it. Keep the TTL short and revoke when unsure.
 *
 * What it is not: HTTPS, protection against a hostile network path, or any
 * defence against someone who already has the machine. Treat LAN exposure as
 * "the network is trusted enough", not as "this is safe on public Wi-Fi".
 */

"use strict";

const crypto = require("node:crypto");
const fs = require("node:fs");
const path = require("node:path");

const SESSION_COOKIE = "fairy_session";
const SESSION_TTL_MS = 7 * 24 * 60 * 60 * 1000; // browser session lifetime
const TRUSTED_IP_TTL_MS = 30 * 24 * 60 * 60 * 1000; // "remembered" device lifetime
const LOGIN_WINDOW_MS = 15 * 60 * 1000;
const LOGIN_MAX_ATTEMPTS = 8;
// Deliberately permissive: a short password is a legitimate choice for a
// convenience account on a trusted LAN, and the throttle is what makes it
// survivable. The warning at creation time is the honest part, not a hard floor.
const MIN_PASSWORD_LENGTH = 4;

// Self-service registration is off unless it is explicitly switched on. A server
// reachable from a LAN that lets anyone create an account is an open door, and
// every account here can drive the mouse, the keyboard and a shell. Turning this
// on is a decision about the network, so it is opt-in.
const DEFAULT_ALLOW_REGISTRATION = false;
// Registration is cheaper to abuse than login (no password to guess), so it gets
// its own, tighter budget. The key is per-IP only: an attacker who rotates
// usernames must still pay one scrypt plus a rate limit per attempt.
const REGISTER_WINDOW_MS = 60 * 60 * 1000;
const REGISTER_MAX_ATTEMPTS = 5;
const USERNAME_PATTERN = /^[A-Za-z0-9_.-]{3,32}$/;

// Family group. The name is only a label in the settings panel; the group is
// created by migration rather than chosen at setup, so this is not a decision
// the operator has to make.
const DEFAULT_GROUP_NAME = "我的家庭";
// An invite is a bearer credential handed out over a chat app, so it is short
// lived and single use. A day is long enough to walk to the other person's
// machine and type it in, and short enough that a screenshot left in a chat log
// stops mattering.
const INVITE_TTL_MS = 24 * 60 * 60 * 1000;
// Human-typed, so no 0/O/1/I/L and no lowercase to confuse: the alphabet is 32
// symbols, which is exactly 5 bits per character, so taking a byte modulo 32 is
// uniform and 8 characters carry 40 bits of entropy.
const INVITE_ALPHABET = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ";
const INVITE_CODE_LENGTH = 8;

function generateInviteCode() {
  const bytes = crypto.randomBytes(INVITE_CODE_LENGTH);
  let code = "";
  for (const byte of bytes) code += INVITE_ALPHABET[byte % INVITE_ALPHABET.length];
  // Dashes are presentation only; they make a code that gets read aloud or
  // retyped easier to hold onto, and normalizeInviteCode throws them away.
  return code.slice(0, 4) + "-" + code.slice(4);
}

// A bind code answers a different question from an invite: not "may you join
// this family" but "which account may this channel conversation speak as". It
// is still a credential, so it is stored the same way - digest only, burns on
// first use. Ten minutes, because it is read off one screen and typed into a
// chat app; an older code is clutter, not a risk.
const BIND_CODE_TTL_MS = 10 * 60 * 1000;
const BIND_CHANNELS = new Set(["qq", "wechat"]);

function normalizeConversationId(value) {
  return String(value == null ? "" : value).trim();
}

function isKnownChannel(value) {
  return BIND_CHANNELS.has(String(value == null ? "" : value).trim().toLowerCase());
}

function normalizeInviteCode(value) {
  return String(value || "")
    .toUpperCase()
    .replace(/[^0-9A-Z]/g, "");
}

// ---------------------------------------------------------------------------
// Password hashing
// ---------------------------------------------------------------------------

const SCRYPT_KEYLEN = 64;
const SCRYPT_PARAMS = { N: 16384, r: 8, p: 1 };

function hashPassword(password, salt = crypto.randomBytes(16)) {
  const derived = crypto.scryptSync(password, salt, SCRYPT_KEYLEN, SCRYPT_PARAMS);
  return { salt: salt.toString("hex"), hash: derived.toString("hex") };
}

function verifyPassword(password, saltHex, hashHex) {
  const expected = Buffer.from(hashHex, "hex");
  let actual;
  try {
    actual = crypto.scryptSync(password, Buffer.from(saltHex, "hex"), expected.length, SCRYPT_PARAMS);
  } catch {
    return false;
  }
  return actual.length === expected.length && crypto.timingSafeEqual(actual, expected);
}

function hashToken(token) {
  // Sessions are looked up by digest so the stored database cannot be replayed.
  return crypto.createHash("sha256").update(token).digest("hex");
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

class AuthStore {
  constructor(databasePath) {
    this.databasePath = databasePath;
    this.disabled = false;
    this.error = "";
    this.open();
  }

  open() {
    try {
      const { DatabaseSync } = require("node:sqlite");
      fs.mkdirSync(path.dirname(this.databasePath), { recursive: true });
      this.db = new DatabaseSync(this.databasePath);
      this.db.exec("PRAGMA journal_mode = WAL");
      this.db.exec("PRAGMA foreign_keys = ON");
      this.migrate();
    } catch (error) {
      // Never fail closed into "no auth required": if the store cannot open, the
      // caller must refuse to serve protected routes rather than serve them open.
      this.disabled = true;
      this.error = error && error.message ? error.message : String(error);
    }
  }

  migrate() {
    this.db.exec(`
      CREATE TABLE IF NOT EXISTS users (
        id            INTEGER PRIMARY KEY AUTOINCREMENT,
        username      TEXT NOT NULL UNIQUE,
        password_hash TEXT NOT NULL,
        password_salt TEXT NOT NULL,
        created_at    INTEGER NOT NULL,
        disabled      INTEGER NOT NULL DEFAULT 0
      );

      CREATE TABLE IF NOT EXISTS auth_sessions (
        token_hash   TEXT PRIMARY KEY,
        user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
        created_at   INTEGER NOT NULL,
        expires_at   INTEGER NOT NULL,
        last_seen_at INTEGER NOT NULL,
        ip           TEXT NOT NULL DEFAULT '',
        user_agent   TEXT NOT NULL DEFAULT ''
      );
      CREATE INDEX IF NOT EXISTS idx_auth_sessions_user ON auth_sessions(user_id);

      CREATE TABLE IF NOT EXISTS trusted_devices (
        id           INTEGER PRIMARY KEY AUTOINCREMENT,
        user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
        ip           TEXT NOT NULL,
        created_at   INTEGER NOT NULL,
        expires_at   INTEGER NOT NULL,
        last_seen_at INTEGER NOT NULL,
        label        TEXT NOT NULL DEFAULT '',
        UNIQUE(user_id, ip)
      );
      CREATE INDEX IF NOT EXISTS idx_trusted_devices_ip ON trusted_devices(ip);

      -- Throttle counters live in the database, not in a Map. A short password
      -- is only survivable because guessing is rate limited; keeping the counter
      -- in memory would mean restarting the server hands an attacker a fresh
      -- budget, which is exactly when a weak password stops being defensible.
      CREATE TABLE IF NOT EXISTS login_attempts (
        key      TEXT PRIMARY KEY,
        count    INTEGER NOT NULL,
        reset_at INTEGER NOT NULL
      );

      -- Per-user configuration. The single-user desktop build keeps everything in
      -- config/config.json, but once more than one person can log in, one shared
      -- file means one person's theme and model choice silently rewrites
      -- everyone else's. Each scope is one independently replaced document, so a
      -- user can override exactly the slice they care about.
      --
      -- ON DELETE CASCADE is deliberate: settings follow the account, not the
      -- filesystem, and a deleted user must not leave orphaned rows behind.
      CREATE TABLE IF NOT EXISTS user_settings (
        user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
        scope      TEXT NOT NULL,
        payload    TEXT NOT NULL,
        updated_at INTEGER NOT NULL,
        PRIMARY KEY (user_id, scope)
      );

      -- ---------------------------------------------------------------------
      -- Family group
      --
      -- A single-machine Fairy serves one household, so "which group" is not a
      -- question the operator answers at setup: migration creates the one group
      -- and the machine's owner becomes its admin. Accounts join it by
      -- redeeming an invite, never by simply existing - that is what keeps a
      -- reachable port from also being an open registration desk.
      --
      -- The tables are still keyed by group id rather than being a single row,
      -- because the workspace each group shares is a directory and a future
      -- release may serve more than one; making that a migration is a worse
      -- trade than carrying the column now.
      -- prompt describes the household as a whole ("我爸在这儿上班，我妈的
      -- 微信绑在她自己手机上"). Like group_members.note it is descriptive
      -- only: it is handed to the agent as context, never consulted for
      -- authorisation, so a wrong prompt can mislead but cannot grant access.
      CREATE TABLE IF NOT EXISTS groups (
        id         INTEGER PRIMARY KEY AUTOINCREMENT,
        name       TEXT NOT NULL,
        prompt     TEXT NOT NULL DEFAULT '',
        created_at INTEGER NOT NULL
      );

      -- role is 'admin' or 'member'. The machine owner is admin regardless of
      -- what this column says (see roleFor), so a hand-edited row cannot
      -- demote the person who owns the hardware out of their own account.
      -- note is a free-text relationship description ("我爸", "姐姐").
      -- It is never used for authorisation: it only tells the agent who a
      -- member is to the user, so "我爸跟你说给我发条消息" can be routed to
      -- the right member's bound channel.
      CREATE TABLE IF NOT EXISTS group_members (
        group_id  INTEGER NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
        user_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
        role      TEXT NOT NULL DEFAULT 'member',
        note      TEXT NOT NULL DEFAULT '',
        joined_at INTEGER NOT NULL,
        PRIMARY KEY (group_id, user_id)
      );

      -- Invites are stored as digests, exactly like session tokens, and for the
      -- same reason: the code is a bearer credential for joining the family, so
      -- a copy of this file must not be replayable as one. The plaintext exists
      -- only in the response that created it, which is why the settings panel
      -- shows a code once and cannot list old ones.
      CREATE TABLE IF NOT EXISTS group_invites (
        code_hash  TEXT PRIMARY KEY,
        group_id   INTEGER NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
        created_by INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
        created_at INTEGER NOT NULL,
        expires_at INTEGER NOT NULL,
        used_by    INTEGER REFERENCES users(id) ON DELETE SET NULL,
        used_at    INTEGER
      );
      CREATE INDEX IF NOT EXISTS idx_group_invites_group ON group_invites(group_id);

      -- Channel bindings are the authority for "who speaks through QQ or
      -- WeChat". A bridge only reports which conversation a message came from;
      -- the server joins that conversation to an account here. Primary key is
      -- (channel, conversation_id) because a conversation belongs to exactly one
      -- account - the second person to run /bind takes the conversation over.
      CREATE TABLE IF NOT EXISTS channel_bindings (
        channel         TEXT NOT NULL,
        conversation_id TEXT NOT NULL,
        user_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
        bound_at        INTEGER NOT NULL,
        PRIMARY KEY (channel, conversation_id)
      );
      CREATE INDEX IF NOT EXISTS idx_channel_bindings_user ON channel_bindings(user_id);

      -- Same reasoning as group_invites: the plaintext code is shown once and
      -- never stored, so a stolen database file cannot be replayed as a bind.
      CREATE TABLE IF NOT EXISTS channel_bind_codes (
        id         INTEGER PRIMARY KEY AUTOINCREMENT,
        user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
        code_hash  TEXT NOT NULL UNIQUE,
        created_at INTEGER NOT NULL,
        expires_at INTEGER NOT NULL,
        used_at    INTEGER
      );
    `);
    // Existing installs predate group_members.note and groups.prompt, and
    // CREATE TABLE IF NOT EXISTS above will not touch their tables. Add the
    // columns separately so both fresh and upgraded databases end up with the
    // same shape.
    this.#addColumnIfMissing("group_members", "note", "TEXT NOT NULL DEFAULT ''");
    this.#addColumnIfMissing("groups", "prompt", "TEXT NOT NULL DEFAULT ''");
    this.bootstrapGroup();
  }

  // -- users ---------------------------------------------------------------

  countUsers() {
    if (!this.db) return 0;
    return this.db.prepare("SELECT COUNT(*) AS n FROM users WHERE disabled = 0").get().n;
  }

  findUser(username) {
    if (!this.db) return null;
    return this.db.prepare("SELECT * FROM users WHERE username = ? AND disabled = 0").get(String(username || ""));
  }

  findUserById(id) {
    if (!this.db) return null;
    return this.db.prepare("SELECT * FROM users WHERE id = ? AND disabled = 0").get(Number(id));
  }

  listUsers() {
    if (!this.db) return [];
    return this.db.prepare("SELECT id, username, created_at, disabled FROM users ORDER BY id").all();
  }

  addUser(username, password, { replace = false } = {}) {
    if (!this.db) throw new Error(`auth store unavailable: ${this.error}`);
    const name = String(username || "").trim();
    if (!name) throw new Error("username is required");
    if (!password || String(password).length < MIN_PASSWORD_LENGTH) {
      throw new Error(`password must be at least ${MIN_PASSWORD_LENGTH} characters`);
    }
    const { salt, hash } = hashPassword(String(password));
    const existing = this.db.prepare("SELECT id FROM users WHERE username = ?").get(name);
    if (existing) {
      if (!replace) throw new Error(`user already exists: ${name}`);
      this.db
        .prepare("UPDATE users SET password_hash = ?, password_salt = ?, disabled = 0 WHERE id = ?")
        .run(hash, salt, existing.id);
      this.revokeAllForUser(existing.id);
      return existing.id;
    }
    const result = this.db
      .prepare("INSERT INTO users (username, password_hash, password_salt, created_at) VALUES (?, ?, ?, ?)")
      .run(name, hash, salt, Date.now());
    // The first account is the machine's owner, and the owner's membership row
    // is what makes the family group exist at all. bootstrapGroup() also runs on
    // every open, so a fresh install would get there on the next restart - but
    // "the settings panel is empty until you restart the server" is a bad first
    // impression, and it is one line to avoid.
    if (this.countUsers() === 1) this.bootstrapGroup();
    return Number(result.lastInsertRowid);
  }

  // -- login throttling ----------------------------------------------------

  isRateLimited(key, maxAttempts = LOGIN_MAX_ATTEMPTS, windowMs = LOGIN_WINDOW_MS) {
    if (!this.db) return true; // no store, no guessing
    const now = Date.now();
    const row = this.db.prepare("SELECT count, reset_at FROM login_attempts WHERE key = ?").get(String(key));
    if (!row) return false;
    if (now > row.reset_at) {
      this.db.prepare("DELETE FROM login_attempts WHERE key = ?").run(String(key));
      return false;
    }
    return row.count >= maxAttempts;
  }

  /**
   * Same throttle as `isRateLimited` but with an explicit budget.
   *
   * Registration is a different operation from login and needs a different
   * budget, so the window and ceiling are parameters rather than being baked into
   * one constant pair. Sharing the row type keeps the counters in one table.
   */
  isRateLimitedKey(key, maxAttempts, windowMs) {
    return this.isRateLimited(key, maxAttempts, windowMs);
  }

  recordFailureKey(key, windowMs) {
    this.recordFailure(key, windowMs);
  }

  recordFailure(key, windowMs = LOGIN_WINDOW_MS) {
    if (!this.db) return;
    const now = Date.now();
    // Compare the *stored* window against the current time. Comparing against the
    // incoming reset_at looks equivalent but never is: the new value is always
    // now + window, so the "expired" branch would win every time and the counter
    // would reset to 1 on each attempt - a throttle that never throttles.
    this.db
      .prepare(
        `INSERT INTO login_attempts (key, count, reset_at) VALUES (?, 1, ?)
         ON CONFLICT(key) DO UPDATE SET
           count = CASE WHEN login_attempts.reset_at <= ? THEN 1 ELSE login_attempts.count + 1 END,
           reset_at = CASE WHEN login_attempts.reset_at <= ? THEN ? ELSE login_attempts.reset_at END`
      )
      .run(String(key), now + windowMs, now, now, now + windowMs);
    // Opportunistic sweep so the table cannot grow without bound. The horizon is
    // the largest window that could be in the table, not this call's window, or a
    // long-window counter would delete itself before it ever expired.
    const longest = Math.max(windowMs, LOGIN_WINDOW_MS, REGISTER_WINDOW_MS);
    this.db.prepare("DELETE FROM login_attempts WHERE reset_at < ?").run(now - longest);
  }

  clearFailures(key) {
    if (!this.db) return;
    this.db.prepare("DELETE FROM login_attempts WHERE key = ?").run(String(key));
  }

  // -- sessions ------------------------------------------------------------

  /**
   * @param ttlMs 0 means "never expires". Browser logins leave it at the
   *   default; a channel bridge needs a token that survives a machine that is
   *   only rebooted once in a while, so the CLI passes an explicit lifetime.
   */
  createSession(userId, { ip = "", userAgent = "", ttlMs = SESSION_TTL_MS } = {}) {
    const token = crypto.randomBytes(32).toString("base64url");
    const now = Date.now();
    const expiresAt = ttlMs > 0 ? now + ttlMs : Number.MAX_SAFE_INTEGER;
    this.db
      .prepare(
        "INSERT INTO auth_sessions (token_hash, user_id, created_at, expires_at, last_seen_at, ip, user_agent) VALUES (?, ?, ?, ?, ?, ?, ?)"
      )
      .run(hashToken(token), userId, now, expiresAt, now, ip, userAgent.slice(0, 200));
    this.db.prepare("DELETE FROM auth_sessions WHERE expires_at < ?").run(now);
    return token;
  }

  lookupSession(token) {
    if (!this.db || !token) return null;
    const now = Date.now();
    const row = this.db.prepare("SELECT * FROM auth_sessions WHERE token_hash = ?").get(hashToken(token));
    if (!row) return null;
    if (row.expires_at < now) {
      this.db.prepare("DELETE FROM auth_sessions WHERE token_hash = ?").run(row.token_hash);
      return null;
    }
    this.db.prepare("UPDATE auth_sessions SET last_seen_at = ? WHERE token_hash = ?").run(now, row.token_hash);
    return row;
  }

  destroySession(token) {
    if (!this.db || !token) return;
    this.db.prepare("DELETE FROM auth_sessions WHERE token_hash = ?").run(hashToken(token));
  }

  revokeAllForUser(userId) {
    if (!this.db) return;
    this.db.prepare("DELETE FROM auth_sessions WHERE user_id = ?").run(userId);
    this.db.prepare("DELETE FROM trusted_devices WHERE user_id = ?").run(userId);
  }

  // -- trusted devices -----------------------------------------------------

  trustDevice(userId, ip, ttlMs = TRUSTED_IP_TTL_MS) {
    if (!this.db || !ip) return;
    const now = Date.now();
    this.db
      .prepare(
        `INSERT INTO trusted_devices (user_id, ip, created_at, expires_at, last_seen_at)
         VALUES (?, ?, ?, ?, ?)
         ON CONFLICT(user_id, ip) DO UPDATE SET expires_at = excluded.expires_at, last_seen_at = excluded.last_seen_at`
      )
      .run(userId, ip, now, now + ttlMs, now);
  }

  isTrustedIp(ip) {
    if (!this.db || !ip) return null;
    const now = Date.now();
    const row = this.db
      .prepare(
        `SELECT d.*, u.username FROM trusted_devices d
         JOIN users u ON u.id = d.user_id
         WHERE d.ip = ? AND d.expires_at > ? AND u.disabled = 0
         ORDER BY d.expires_at DESC LIMIT 1`
      )
      .get(ip, now);
    if (!row) return null;
    this.db.prepare("UPDATE trusted_devices SET last_seen_at = ? WHERE id = ?").run(now, row.id);
    return row;
  }

  listTrustedDevices() {
    if (!this.db) return [];
    return this.db
      .prepare(
        `SELECT d.id, d.ip, d.created_at, d.expires_at, d.last_seen_at, u.username
         FROM trusted_devices d JOIN users u ON u.id = d.user_id
         WHERE d.expires_at > ? ORDER BY d.last_seen_at DESC`
      )
      .all(Date.now());
  }

  forgetDevice(id) {
    if (!this.db) return;
    this.db.prepare("DELETE FROM trusted_devices WHERE id = ?").run(Number(id));
  }

  // -- per-user settings ----------------------------------------------------

  /**
   * Read one settings document for a user.
   *
   * Returns the parsed value, or `fallback` when the user has never written this
   * scope. A missing scope is a normal first-run state, not an error: the caller
   * passes in whatever the shared config.json currently says so a new account
   * starts from the machine's defaults instead of from nothing.
   */
  getUserSetting(userId, scope, fallback = null) {
    if (!this.db) return fallback;
    const row = this.db
      .prepare("SELECT payload FROM user_settings WHERE user_id = ? AND scope = ?")
      .get(Number(userId), String(scope));
    if (!row) return fallback;
    try {
      return JSON.parse(row.payload);
    } catch {
      // A hand-edited or truncated row must not take down every settings read.
      return fallback;
    }
  }

  setUserSetting(userId, scope, value) {
    if (!this.db) throw new Error(`auth store unavailable: ${this.error}`);
    this.db
      .prepare(
        `INSERT INTO user_settings (user_id, scope, payload, updated_at) VALUES (?, ?, ?, ?)
         ON CONFLICT(user_id, scope) DO UPDATE SET payload = excluded.payload, updated_at = excluded.updated_at`
      )
      .run(Number(userId), String(scope), JSON.stringify(value === undefined ? null : value), Date.now());
    return value === undefined ? null : value;
  }

  deleteUserSetting(userId, scope) {
    if (!this.db) return;
    this.db.prepare("DELETE FROM user_settings WHERE user_id = ? AND scope = ?").run(Number(userId), String(scope));
  }

  // -- passwords -----------------------------------------------------------

  /**
   * Replace an account's password.
   *
   * Every other session is dropped, so a browser left logged in elsewhere has to
   * authenticate again. The caller's own session (`keepToken`) and its remembered
   * device (`keepIp`) survive, because a password change should not log the
   * person out of the machine they are standing at - and if the change was made
   * from a browser that was *not* remembered, keeping the session is the only
   * thing standing between them and the login page they just came from.
   */
  setPassword(userId, password, { keepToken = "", keepIp = "" } = {}) {
    if (!this.db) throw new Error(`auth store unavailable: ${this.error}`);
    if (!password || String(password).length < MIN_PASSWORD_LENGTH) {
      throw new Error(`password must be at least ${MIN_PASSWORD_LENGTH} characters`);
    }
    const { salt, hash } = hashPassword(String(password));
    this.db
      .prepare("UPDATE users SET password_hash = ?, password_salt = ? WHERE id = ?")
      .run(hash, salt, Number(userId));
    // An empty keepToken leaves `<> ''` matching every row, which is the
    // intended "drop them all" when the caller has no session of their own.
    this.db
      .prepare("DELETE FROM auth_sessions WHERE user_id = ? AND token_hash <> ?")
      .run(Number(userId), keepToken ? hashToken(keepToken) : "");
    if (keepIp) {
      this.db.prepare("DELETE FROM trusted_devices WHERE user_id = ? AND ip <> ?").run(Number(userId), String(keepIp));
    } else {
      this.db.prepare("DELETE FROM trusted_devices WHERE user_id = ?").run(Number(userId));
    }
  }

  // -- the machine owner ---------------------------------------------------

  /**
   * The account that owns this machine: the first one created.
   *
   * Two callers need the same answer and must not disagree: the session
   * ownership layer, which adopts history written before multi-user support,
   * and the family group, whose admin is the person who set the machine up.
   * Both used to spell out "ORDER BY id LIMIT 1" separately.
   */
  ownerUserId() {
    if (!this.db) return null;
    const row = this.db.prepare("SELECT id FROM users WHERE disabled = 0 ORDER BY id LIMIT 1").get();
    return row ? row.id : null;
  }

  isOwner(userId) {
    const ownerId = this.ownerUserId();
    return ownerId != null && Number(userId) === ownerId;
  }

  // -- family group --------------------------------------------------------

  /**
   * Give this install exactly one group to live in.
   *
   * Called from migrate(), so it runs on every open, and every statement in it
   * is idempotent: an install that already has its group pays one SELECT. The
   * owner is re-asserted as a member each time because a database that predates
   * this table (or an account added later by the CLI) would otherwise leave the
   * person who runs the machine with no group at all, which reads in the
   * settings panel as "not in a family" on the machine that is the family.
   */
  bootstrapGroup() {
    const now = Date.now();
    const existing = this.db.prepare("SELECT id FROM groups ORDER BY id LIMIT 1").get();
    const groupId = existing
      ? existing.id
      : Number(this.db.prepare("INSERT INTO groups (name, created_at) VALUES (?, ?)").run(DEFAULT_GROUP_NAME, now).lastInsertRowid);
    const ownerId = this.ownerUserId();
    if (ownerId != null) {
      this.db
        .prepare(
          `INSERT INTO group_members (group_id, user_id, role, joined_at) VALUES (?, ?, 'admin', ?)
           ON CONFLICT(group_id, user_id) DO NOTHING`
        )
        .run(groupId, ownerId, now);
    }
    return groupId;
  }

  primaryGroupId() {
    if (!this.db) return null;
    const row = this.db.prepare("SELECT id FROM groups ORDER BY id LIMIT 1").get();
    return row ? row.id : null;
  }

  groupForUser(userId) {
    if (!this.db) return null;
    const row = this.db
      .prepare(
        `SELECT g.id AS id, g.name AS name, g.prompt AS prompt,
                g.created_at AS created_at, m.role AS role
         FROM group_members m JOIN groups g ON g.id = m.group_id
         WHERE m.user_id = ? ORDER BY g.id LIMIT 1`
      )
      .get(Number(userId));
    return row || null;
  }

  /**
   * Set or clear the household-wide relationship prompt.
   *
   * This is context for the agent, not configuration that gates anything: it
   * is what lets "我爸跟你说要给我发条消息" be understood once
   * group_members.note says who that person is. Descriptive text can mislead
   * the model but must never be what authorises a send, so the send path
   * keeps checking bound channels and group membership on its own.
   */
  setGroupPrompt(groupId, prompt) {
    const gid = Number(groupId);
    if (!Number.isInteger(gid)) {
      throw new Error("groupId must be an integer");
    }
    const clean = String(prompt ?? "").trim();
    const result = this.db.prepare("UPDATE groups SET prompt = ? WHERE id = ?").run(clean, gid);
    if (result.changes === 0 && !this.db.prepare("SELECT 1 FROM groups WHERE id = ?").get(gid)) {
      throw new Error("group not found");
    }
    return clean;
  }

  listMembers(groupId) {
    if (!this.db) return [];
    return this.db
      .prepare(
        `SELECT u.id AS user_id, u.username, m.role, m.note, m.joined_at
         FROM group_members m JOIN users u ON u.id = m.user_id
         WHERE m.group_id = ? AND u.disabled = 0
         ORDER BY m.joined_at, u.id`
      )
      .all(Number(groupId));
  }

  /**
   * Set or clear a member's relationship note ("我爸", "姐姐").
   *
   * The note is descriptive only and must stay that way: routing decisions
   * still have to be authorised against the group, so writing it can never
   * grant or revoke access. Empty/whitespace input clears the note rather
   * than storing a string that would render as an empty input.
   */
  setMemberNote(groupId, userId, note) {
    const gid = Number(groupId);
    const uid = Number(userId);
    if (!Number.isInteger(gid) || !Number.isInteger(uid)) {
      throw new Error("groupId and userId must be integers");
    }
    const clean = String(note ?? "").trim();
    const result = this.db
      .prepare("UPDATE group_members SET note = ? WHERE group_id = ? AND user_id = ?")
      .run(clean, gid, uid);
    if (result.changes === 0) {
      const exists = this.db
        .prepare("SELECT 1 FROM group_members WHERE group_id = ? AND user_id = ?")
        .get(gid, uid);
      if (!exists) throw new Error("member not in group");
    }
    return clean;
  }

  /**
   * Add a column to an existing table when it is missing.
   *
   * SQLite has no "ADD COLUMN IF NOT EXISTS", so every caller has to do the
   * PRAGMA check itself or the second boot of a machine would abort on the
   * duplicate-column error. Kept private and deliberately dumb: it only
   * reports whether the table exists at all.
   */
  #addColumnIfMissing(table, column, decl) {
    const tableExists = this.db
      .prepare("SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?")
      .get(table);
    if (!tableExists) return false;
    const columns = this.db.prepare(`PRAGMA table_info(${table})`).all();
    if (columns.some((c) => c.name === column)) return false;
    this.db.exec(`ALTER TABLE ${table} ADD COLUMN ${column} ${decl}`);
    return true;
  }

  /**
   * Put someone in a group. `role` is "member" unless the caller passes "admin",
   * and handing out admin is the one case that needs an owner on the other end.
   *
   * The check lives here rather than at each route for the same reason
   * removeMember refuses the owner: every caller that reaches for `admin` is a
   * caller deciding who else can drive this machine, so the guard belongs next
   * to the write. Invite acceptance passes no actor and gets "member", which is
   * not a privilege and therefore needs no owner.
   */
  addMember(groupId, userId, role = "member", actorUserId = null) {
    if (!this.db) throw new Error(`auth store unavailable: ${this.error}`);
    const next = role === "admin" ? "admin" : "member";
    if (next === "admin" && !this.isOwner(actorUserId)) {
      throw new Error("只有本机账号能授予管理员");
    }
    this.db
      .prepare(
        `INSERT INTO group_members (group_id, user_id, role, joined_at) VALUES (?, ?, ?, ?)
         ON CONFLICT(group_id, user_id) DO NOTHING`
      )
      .run(Number(groupId), Number(userId), next, Date.now());
  }

  /**
   * Grant or revoke admin for an existing member.
   *
   * Mirrors removeMember: the owner is refused here, not only at the route, and
   * nobody may point this at the owner. If an admin could demote the owner the
   * machine would end up with an owner no one can promote back.
   */
  setMemberRole(actorUserId, targetUserId, role) {
    if (!this.db) throw new Error(`auth store unavailable: ${this.error}`);
    if (!this.isOwner(actorUserId)) throw new Error("只有本机账号能改成员权限");
    if (this.isOwner(targetUserId)) throw new Error("不能改本机账号自己的权限");
    const next = role === "admin" ? "admin" : "member";
    const result = this.db
      .prepare("UPDATE group_members SET role = ? WHERE user_id = ? AND role != 'owner'")
      .run(next, Number(targetUserId));
    return result.changes > 0;
  }

  /**
   * Remove a member. The owner is refused here rather than at the route,
   * because "the admin removed themselves and the machine now has no admin" is
   * a state no route should be able to reach by accident.
   */
  removeMember(groupId, userId) {
    if (!this.db) return false;
    if (this.isOwner(userId)) return false;
    const result = this.db
      .prepare("DELETE FROM group_members WHERE group_id = ? AND user_id = ?")
      .run(Number(groupId), Number(userId));
    this.db.prepare("DELETE FROM auth_sessions WHERE user_id = ?").run(Number(userId));
    this.db.prepare("DELETE FROM trusted_devices WHERE user_id = ?").run(Number(userId));
    return result.changes > 0;
  }

  /**
   * Admin if the group says so, or if this is the machine's owner.
   *
   * The owner check is not redundant. It is what makes the person who owns the
   * hardware un-demotable: a membership row that is missing, hand-edited, or
   * lost to a future migration cannot lock them out of their own settings.
   */
  roleFor(userId) {
    if (this.isOwner(userId)) return "admin";
    const group = this.groupForUser(userId);
    return group && group.role === "admin" ? "admin" : "member";
  }

  isAdmin(userId) {
    return this.roleFor(userId) === "admin";
  }

  // -- invites -------------------------------------------------------------

  createInvite(groupId, createdBy, { ttlMs = INVITE_TTL_MS } = {}) {
    if (!this.db) throw new Error(`auth store unavailable: ${this.error}`);
    this.purgeExpiredInvites();
    const code = generateInviteCode();
    const now = Date.now();
    const expiresAt = now + ttlMs;
    this.db
      .prepare(
        `INSERT INTO group_invites (code_hash, group_id, created_by, created_at, expires_at)
         VALUES (?, ?, ?, ?, ?)`
      )
      .run(hashToken(normalizeInviteCode(code)), Number(groupId), Number(createdBy), now, expiresAt);
    return { code, expiresAt };
  }

  /** Unused and unexpired, or null. A used code is simply not found. */
  lookupInvite(code) {
    const normalized = normalizeInviteCode(code);
    if (!this.db || normalized.length !== INVITE_CODE_LENGTH) return null;
    const row = this.db.prepare("SELECT * FROM group_invites WHERE code_hash = ?").get(hashToken(normalized));
    if (!row) return null;
    if (row.used_by != null) return null;
    if (row.expires_at < Date.now()) return null;
    return row;
  }

  consumeInvite(code, userId) {
    if (!this.db) return;
    this.db
      .prepare("UPDATE group_invites SET used_by = ?, used_at = ? WHERE code_hash = ?")
      .run(Number(userId), Date.now(), hashToken(normalizeInviteCode(code)));
  }

  /** Counted rather than listed: the plaintext is not recoverable by design. */
  countOpenInvites(groupId) {
    if (!this.db) return 0;
    return this.db
      .prepare("SELECT COUNT(*) AS n FROM group_invites WHERE group_id = ? AND used_by IS NULL AND expires_at >= ?")
      .get(Number(groupId), Date.now()).n;
  }

  // -- channel bindings ----------------------------------------------------

  purgeBindCodes(now = Date.now()) {
    if (!this.db) return;
    this.db.prepare("DELETE FROM channel_bind_codes WHERE expires_at <= ?").run(now);
  }

  /**
   * Mint a one-shot bind code for the signed-in account. Superseding the
   * previous code keeps the settings panel honest: only the code currently on
   * screen can still work, so a screenshot taken an hour ago is dead.
   */
  issueBindCode(userId, now = Date.now()) {
    if (!this.db) throw new Error(`auth store unavailable: ${this.error}`);
    this.purgeBindCodes(now);
    const user = Number(userId);
    this.db.prepare("DELETE FROM channel_bind_codes WHERE user_id = ?").run(user);
    const code = generateInviteCode();
    const expiresAt = now + BIND_CODE_TTL_MS;
    this.db
      .prepare(
        `INSERT INTO channel_bind_codes (user_id, code_hash, created_at, expires_at)
         VALUES (?, ?, ?, ?)`
      )
      .run(user, hashToken(normalizeInviteCode(code)), now, expiresAt);
    return { code, expiresAt };
  }

  /** Unused, unexpired, or null. A burnt code is simply not found. */
  lookupBindCode(code, now = Date.now()) {
    const normalized = normalizeInviteCode(code);
    if (!this.db || normalized.length !== INVITE_CODE_LENGTH) return null;
    const row = this.db.prepare("SELECT * FROM channel_bind_codes WHERE code_hash = ?").get(hashToken(normalized));
    if (!row) return null;
    if (row.used_at != null) return null;
    // The deadline is exclusive so a code is dead exactly at ten minutes, the
    // same instant purgeBindCodes sweeps it and the UPDATE below refuses it.
    if (row.expires_at <= now) return null;
    return row;
  }

  /**
   * Burn the code and report its owner. Binding and burning happen together in
   * one transaction so a code can never be spent twice under concurrency.
   */
  consumeBindCode(code, now = Date.now()) {
    if (!this.db) return null;
    this.purgeBindCodes(now);
    const normalized = normalizeInviteCode(code);
    if (normalized.length !== INVITE_CODE_LENGTH) return null;
    const hash = hashToken(normalized);
    const burn = this.db
      .prepare("UPDATE channel_bind_codes SET used_at = ? WHERE code_hash = ? AND used_at IS NULL AND expires_at > ?")
      .run(now, hash, now);
    if (!burn.changes) return null;
    const row = this.db.prepare("SELECT user_id FROM channel_bind_codes WHERE code_hash = ?").get(hash);
    if (!row) return null;
    return this.findUserById(row.user_id);
  }

  lookupChannelBinding(channel, conversationId) {
    const name = String(channel == null ? "" : channel).trim().toLowerCase();
    const conv = normalizeConversationId(conversationId);
    if (!this.db || !name || !conv) return null;
    const row = this.db
      .prepare("SELECT channel, conversation_id, user_id, bound_at FROM channel_bindings WHERE channel = ? AND conversation_id = ?")
      .get(name, conv);
    return row || null;
  }

  /** Upsert: whoever runs /bind last owns the conversation. */
  bindChannel(channel, conversationId, userId, now = Date.now()) {
    if (!this.db) throw new Error(`auth store unavailable: ${this.error}`);
    const name = String(channel == null ? "" : channel).trim().toLowerCase();
    const conv = normalizeConversationId(conversationId);
    if (!isKnownChannel(name)) throw new Error("unknown channel");
    if (!conv) throw new Error("missing conversation id");
    this.db
      .prepare(
        `INSERT INTO channel_bindings (channel, conversation_id, user_id, bound_at)
         VALUES (?, ?, ?, ?)
         ON CONFLICT(channel, conversation_id)
         DO UPDATE SET user_id = excluded.user_id, bound_at = excluded.bound_at`
      )
      .run(name, conv, Number(userId), now);
    return this.lookupChannelBinding(name, conv);
  }

  listChannelBindings(userId) {
    if (!this.db) return [];
    return this.db
      .prepare("SELECT channel, conversation_id, bound_at FROM channel_bindings WHERE user_id = ? ORDER BY bound_at DESC")
      .all(Number(userId));
  }

  /** Scoped to userId on purpose: nobody may unbind someone else's conversation. */
  unbindChannel(channel, conversationId, userId) {
    if (!this.db) return false;
    const name = String(channel == null ? "" : channel).trim().toLowerCase();
    const conv = normalizeConversationId(conversationId);
    const info = this.db
      .prepare("DELETE FROM channel_bindings WHERE channel = ? AND conversation_id = ? AND user_id = ?")
      .run(name, conv, Number(userId));
    return info.changes > 0;
  }

  purgeExpiredInvites() {
    if (!this.db) return;
    // Keep used rows for a day so a redeemed code is distinguishable from an
    // invented one while someone is still typing it in.
    this.db
      .prepare("DELETE FROM group_invites WHERE expires_at < ? AND (used_at IS NULL OR used_at < ?)")
      .run(Date.now() - INVITE_TTL_MS, Date.now() - INVITE_TTL_MS);
  }

  // -- trusted devices, per account ----------------------------------------

  listTrustedDevicesFor(userId) {
    if (!this.db) return [];
    return this.db
      .prepare(
        `SELECT id, ip, created_at, expires_at, last_seen_at
         FROM trusted_devices WHERE user_id = ? AND expires_at > ?
         ORDER BY last_seen_at DESC`
      )
      .all(Number(userId), Date.now());
  }

  /** Scoped delete: a device id from another account is not the caller's to drop. */
  forgetDeviceFor(userId, id) {
    if (!this.db) return false;
    const result = this.db
      .prepare("DELETE FROM trusted_devices WHERE id = ? AND user_id = ?")
      .run(Number(id), Number(userId));
    return result.changes > 0;
  }
}

// ---------------------------------------------------------------------------
// HTTP glue
// ---------------------------------------------------------------------------

function parseCookies(header) {
  const jar = {};
  for (const part of String(header || "").split(";")) {
    const index = part.indexOf("=");
    if (index < 0) continue;
    const key = part.slice(0, index).trim();
    if (key) jar[key] = decodeURIComponent(part.slice(index + 1).trim());
  }
  return jar;
}

function clientIp(req) {
  // X-Forwarded-For is trusted only when the immediate peer is the local proxy.
  //
  // Both extremes are wrong here. Trusting the header unconditionally lets any
  // client claim to be an already-remembered device. Ignoring it unconditionally
  // means every request arriving through the Vite proxy looks like 127.0.0.1, so
  // the first LAN login would remember 127.0.0.1 and every other client would then
  // be auto-trusted - "remember this device" silently becoming "remember
  // everyone".
  //
  // The rule is the usual one: a proxy header is only meaningful when it actually
  // came from the proxy.
  const raw = (req.socket && req.socket.remoteAddress) || "";
  const peer = raw.startsWith("::ffff:") ? raw.slice(7) : raw;
  const loopbackPeer = peer === "127.0.0.1" || peer === "::1" || peer === "localhost";
  if (loopbackPeer) {
    // The chain is built by appending, so the leftmost entry is whatever the
    // original client chose to send and is entirely under their control. Every
    // proxy appends the address it actually saw, which makes the rightmost entry
    // the only one written by our own loopback proxy. Reading the leftmost entry
    // instead lets a visitor name any address they like - including a device this
    // store already trusts - and be logged in as that device's owner.
    const chain = String(req.headers["x-forwarded-for"] || "")
      .split(",")
      .map((hop) => hop.trim())
      .filter(Boolean);
    const forwarded = chain.length ? chain[chain.length - 1] : "";
    if (forwarded) {
      return forwarded.startsWith("::ffff:") ? forwarded.slice(7) : forwarded;
    }
  }
  return peer;
}

/**
 * True when the request reached the API directly from this machine.
 *
 * The API binds to 127.0.0.1, so there are exactly two ways in: a process
 * running here, or the Vite dev server proxying a browser - and the proxy always
 * sets X-Forwarded-For. Requiring that header to be *absent* is what separates
 * them, and it is the whole trick: a browser on this very machine still has to
 * log in (its request comes through the proxy, header present), while the
 * detached-subtask continuation and the chat bridges - which call the API
 * directly, with no cookie and no proxy in front of them - are not answered 401
 * by the gate they were never given a credential for.
 *
 * This is not the header-trust hole that clientIp() warns about. A caller that
 * can open a socket to 127.0.0.1 is already running on this machine, and the
 * threat model at the top of this file puts that caller out of scope: the gate
 * exists to keep the LAN out, and the LAN cannot reach this port.
 */
function isLocalDirectRequest(req) {
  const raw = (req.socket && req.socket.remoteAddress) || "";
  const peer = raw.startsWith("::ffff:") ? raw.slice(7) : raw;
  const loopbackPeer = peer === "127.0.0.1" || peer === "::1" || peer === "localhost";
  if (!loopbackPeer) return false;
  return !String(req.headers["x-forwarded-for"] || "").trim();
}

function sessionCookie(token, { secure = false, maxAge = SESSION_TTL_MS / 1000 } = {}) {
  // HttpOnly keeps the token away from scripts; SameSite=Lax still allows a top
  // level navigation to the app. Secure is opt-in because the dev server is http.
  const bits = [
    `${SESSION_COOKIE}=${encodeURIComponent(token)}`,
    "Path=/",
    "HttpOnly",
    "SameSite=Lax",
    `Max-Age=${Math.floor(maxAge)}`,
  ];
  if (secure) bits.push("Secure");
  return bits.join("; ");
}

function clearCookie() {
  return `${SESSION_COOKIE}=; Path=/; HttpOnly; SameSite=Lax; Max-Age=0`;
}

/** Paths that must remain reachable before a session exists. */
function isPublicPath(pathname) {
  return (
    pathname === "/login.html" ||
    pathname === "/login" ||
    pathname === "/auth/login" ||
    pathname === "/auth/register" ||
    pathname === "/auth/logout" ||
    pathname === "/auth/me" ||
    pathname === "/favicon.ico"
  );
}

function wantsHtml(req, pathname) {
  if (pathname.startsWith("/api/")) return false;
  const accept = String(req.headers["accept"] || "");
  return accept.includes("text/html") || (!path.extname(pathname) && accept !== "application/json");
}

function createAuth(options) {
  const config = Object.assign(
    {
      enabled: false,
      databasePath: "memory/auth.db",
      trustedIpDays: TRUSTED_IP_TTL_MS / (24 * 60 * 60 * 1000),
      allowRegistration: DEFAULT_ALLOW_REGISTRATION,
    },
    options || {}
  );
  const store = new AuthStore(config.databasePath);

  function status() {
    return {
      enabled: config.enabled,
      ready: !store.disabled,
      error: store.error,
      users: store.countUsers(),
      allowRegistration: !!config.allowRegistration,
      database: config.databasePath,
    };
  }

  /**
   * Identify the caller: a live session cookie first, then a remembered device.
   *
   * Exposed so request handling elsewhere can scope data to a user without
   * re-implementing cookie parsing or duplicating the trusted-IP fallback. Returns
   * null when auth is disabled or nothing matches, which callers must treat as
   * "single-user mode", never as an error.
   */
  function resolveSession(req) {
    if (!config.enabled) return null;
    const ip = clientIp(req);
    const cookies = parseCookies(req.headers.cookie);
    const bearer = String(req.headers.authorization || "").replace(/^Bearer\s+/i, "");
    const token = cookies[SESSION_COOKIE] || bearer;
    if (token) {
      const session = store.lookupSession(token);
      if (session) {
        const user = store.findUserById(session.user_id);
        if (user) return { userId: user.id, username: user.username, via: "session", ip, token };
      }
    }
    const trusted = store.isTrustedIp(ip);
    if (trusted) return { userId: trusted.user_id, username: trusted.username, via: "trusted-ip", ip, token: null };
    return null;
  }

  /**
   * The caller's family group, shaped for the settings panel.
   *
   * Null means the account is in no group, which a hand-edited database can
   * reach. The panel says that out loud rather than rendering it as an empty
   * member list, because "nobody is in your family" and "your row is missing"
   * need different fixes.
   */
  function describeGroup(userId) {
    const group = store.groupForUser(userId);
    if (!group) return null;
    return {
      id: group.id,
      name: group.name,
      role: store.roleFor(userId),
      owner: store.isOwner(userId),
      memberCount: store.listMembers(group.id).length,
      openInvites: store.countOpenInvites(group.id),
    };
  }

  /**
   * Returns true when the request has been answered. Call it before any protected
   * route so a new endpoint cannot accidentally ship unauthenticated.
   */
  function handle(req, res, pathname, method, url, sendJson) {
    if (!config.enabled) {
      // The settings panel asks who it is talking to in every mode, so answer
      // that one question here instead of letting it 404. Nothing leaks: with
      // the gate switched off, every other route already answers to anyone.
      if (pathname === "/auth/me") {
        sendJson({ authenticated: false, authEnabled: false });
        return true;
      }
      return false;
    }
    if (!store.disabled) {
      // Fail closed on an empty user table, because a gate with nothing to log in
      // against is worse than no gate: it looks protected while serving nothing.
      // The one exception is registration, which exists precisely to create the
      // first account. Without that carve-out the server would be permanently
      // bricked: no user can exist, and no user can make one.
      // The carve-out is the register path itself, not "register while open
      // registration is on": an invite is now a second way to be allowed in, and
      // the request body is what says which one applies. The register handler
      // enforces the same fail-closed rule for that path, so nothing is lost by
      // letting it read the body first - and with no users there are no invites
      // either, so the only way through is still the operator switching
      // registration on.
      if (store.countUsers() === 0 && pathname !== "/auth/register") {
        sendJson(
          { error: "authentication is enabled but no user exists", hint: "run: node frontend/auth.cjs adduser <name> or enable registration" },
          503
        );
        return true;
      }
    } else {
      sendJson({ error: "authentication store unavailable", detail: store.error }, 503);
      return true;
    }

    const ip = clientIp(req);
    const cookies = parseCookies(req.headers.cookie);
    const bearer = String(req.headers.authorization || "").replace(/^Bearer\s+/i, "");
    const token = cookies[SESSION_COOKIE] || bearer;

    // Resolve the caller once, and carry the answer through the rest of this
    // function. Three ways to be known, in order of how much they prove:
    //
    //   session     - a live cookie. Says who, unmistakably.
    //   trusted-ip  - a remembered device on that address. Says "someone who
    //                 logged in from here before", which is what the checkbox
    //                 promised and no more.
    //   local       - a direct call from this machine, which is how the
    //                 internal continuation and the chat bridges arrive. They
    //                 run as the machine's owner because there is no other
    //                 account they could sensibly be.
    //
    // Every lookup is done once. Re-deriving identity per endpoint is how the
    // next endpoint ends up deriving it slightly differently.
    const liveSession = store.lookupSession(token);
    const sessionUser = liveSession ? store.findUserById(liveSession.user_id) : null;
    const trusted = sessionUser ? null : store.isTrustedIp(ip);
    const localUser = !sessionUser && !trusted && isLocalDirectRequest(req)
      ? store.findUserById(store.ownerUserId())
      : null;
    const identity = sessionUser
      ? { userId: sessionUser.id, username: sessionUser.username, via: "session" }
      : trusted
        ? { userId: trusted.user_id, username: trusted.username, via: "trusted-ip" }
        : localUser
          ? { userId: localUser.id, username: localUser.username, via: "local" }
          : null;

    if (pathname === "/auth/me") {
      if (!identity) {
        sendJson({ authenticated: false, authEnabled: true, allowRegistration: !!config.allowRegistration });
        return true;
      }
      sendJson({
        authenticated: true,
        authEnabled: true,
        via: identity.via,
        userId: identity.userId,
        username: identity.username,
        role: store.roleFor(identity.userId),
        owner: store.isOwner(identity.userId),
        group: describeGroup(identity.userId),
      });
      return true;
    }

    if (pathname === "/auth/logout" && method === "POST") {
      const session = store.lookupSession(token);
      // Signing out has to undo "remember this device" as well, or the button
      // looks broken: the cookie goes away and the remembered address signs the
      // caller straight back in on the very next request.
      const remembered = store.isTrustedIp(ip);
      if (remembered) store.forgetDeviceFor(session ? session.user_id : remembered.user_id, remembered.id);
      store.destroySession(token);
      res.setHeader("Set-Cookie", clearCookie());
      sendJson({ ok: true });
      return true;
    }

    if (pathname === "/auth/register" && method === "POST") {
      readJsonBody(req, (body) => {
        const username = String((body && body.username) || "").trim();
        const password = String((body && body.password) || "");
        const inviteCode = String((body && body.invite) || "").trim();
        // An invite is the normal way in: it is what lets a household add each
        // other without also opening registration to whoever else can reach the
        // port. Open registration stays available for the operator who wants it.
        const invite = inviteCode ? store.lookupInvite(inviteCode) : null;
        if (!config.allowRegistration && !invite) {
          sendJson(
            {
              error: inviteCode ? "邀请码无效或已过期" : "需要邀请码才能注册",
              code: inviteCode ? "bad_invite" : "invite_required",
            },
            403
          );
          return;
        }
        if (!USERNAME_PATTERN.test(username)) {
          sendJson({ error: "username must be 3-32 characters of letters, numbers, _ . or -", code: "bad_username" }, 400);
          return;
        }
        if (!password || password.length < MIN_PASSWORD_LENGTH) {
          sendJson({ error: `password must be at least ${MIN_PASSWORD_LENGTH} characters`, code: "weak_password" }, 400);
          return;
        }
        if (password.toLowerCase() === username.toLowerCase()) {
          sendJson({ error: "password must differ from the username", code: "weak_password" }, 400);
          return;
        }
        // Throttle per IP, not per username: rotating usernames must not hand out
        // a fresh budget, otherwise the limit only stops one account's guessing.
        const throttleKey = `register|${ip}`;
        if (store.isRateLimitedKey(throttleKey, REGISTER_MAX_ATTEMPTS, REGISTER_WINDOW_MS)) {
          sendJson({ error: "too many registrations from this address, try again later", code: "rate_limited" }, 429);
          return;
        }
        try {
          const userId = store.addUser(username, password);
          store.clearFailures(throttleKey);
          if (invite) {
            store.addMember(invite.group_id, userId);
            store.consumeInvite(inviteCode, userId);
          } else {
            const groupId = store.primaryGroupId();
            if (groupId != null) store.addMember(groupId, userId);
          }
          sendJson({ ok: true, userId, username, group: describeGroup(userId) }, 201);
        } catch (error) {
          store.recordFailureKey(throttleKey, REGISTER_WINDOW_MS);
          const message = String((error && error.message) || error);
          sendJson({ error: /already exists/i.test(message) ? "username already taken" : message, code: "conflict" }, 409);
        }
      });
      return true;
    }

    if (pathname === "/auth/login" && method === "POST") {
      readJsonBody(req, (body) => {
        const username = String((body && body.username) || "").trim();
        const password = String((body && body.password) || "");
        const remember = !!(body && body.remember);
        const throttleKey = `${ip}|${username.toLowerCase()}`;
        if (store.isRateLimited(throttleKey)) {
          sendJson({ error: "too many attempts, try again later" }, 429);
          return;
        }
        const user = store.findUser(username);
        if (!user || !verifyPassword(password, user.password_salt, user.password_hash)) {
          store.recordFailure(throttleKey);
          sendJson({ error: "invalid username or password" }, 401);
          return;
        }
        store.clearFailures(throttleKey);
        const token = store.createSession(user.id, { ip, userAgent: String(req.headers["user-agent"] || "") });
        if (remember) store.trustDevice(user.id, ip, config.trustedIpDays * 24 * 60 * 60 * 1000);
        res.setHeader("Set-Cookie", sessionCookie(token, { secure: !!config.secureCookies }));
        sendJson({ ok: true, username: user.username });
      });
      return true;
    }

    // ---- channel binding --------------------------------------------------
    // The table is the single authority for "who may speak through QQ or
    // WeChat". Bridges only report which conversation a message came from; this
    // handler is the only place that turns a conversation into an account.
    if (pathname === "/api/bind/redeem" && method === "POST") {
      // Unauthenticated on purpose: the caller is the local bridge, not the web
      // UI. The one-shot code is the proof, so no session is required.
      if (!identity && !isLocalDirectRequest(req)) {
        sendJson({ ok: false, code: "unauthenticated" }, 401);
        return true;
      }
      readJsonBody(req, (body) => {
        const channel = body && body.channel;
        const conversationId = normalizeConversationId(body && body.conversation_id);
        if (!isKnownChannel(channel) || !conversationId) {
          sendJson({ ok: false, code: "bad_channel" }, 400);
          return;
        }
        const owner = store.consumeBindCode((body && body.code) || "");
        if (!owner) {
          sendJson({ ok: false, code: "bad_code" }, 400);
          return;
        }
        store.bindChannel(channel, conversationId, owner.id);
        sendJson({ ok: true, user: { id: owner.id, username: owner.username } });
      });
      return true;
    }

    if (pathname === "/api/bind/code" && method === "POST") {
      if (!identity) {
        sendJson({ ok: false, code: "unauthenticated" }, 401);
        return true;
      }
      const owner = store.findUserById(identity.userId);
      const issued = store.issueBindCode(owner.id);
      sendJson({ ok: true, code: issued.code, expiresAt: issued.expiresAt });
      return true;
    }

    if (pathname === "/api/bind/list" && method === "GET") {
      if (!identity) {
        sendJson({ ok: false, code: "unauthenticated" }, 401);
        return true;
      }
      const owner = store.findUserById(identity.userId);
      const bindings = store.listChannelBindings(owner.id).map((row) => ({
        channel: row.channel,
        conversationId: row.conversation_id,
        boundAt: row.bound_at,
      }));
      sendJson({ ok: true, bindings });
      return true;
    }

    const unbindMatch = /^\/api\/bind\/([A-Za-z]+)\/([^/]+)$/.exec(pathname);
    if (unbindMatch && method === "DELETE") {
      if (!identity) {
        sendJson({ ok: false, code: "unauthenticated" }, 401);
        return true;
      }
      const owner = store.findUserById(identity.userId);
      const ok = store.unbindChannel(
        unbindMatch[1],
        decodeURIComponent(unbindMatch[2]),
        owner.id
      );
      sendJson({ ok }, ok ? 200 : 404);
      return true;
    }

    if (isPublicPath(pathname)) return false;

    // The account panel's own routes. They answer "who am I and who is in my
    // family", which is meaningless without an identity, so they refuse
    // explicitly rather than falling through to the generic gate below.
    if (pathname.startsWith("/auth/")) {
      if (!identity) {
        sendJson({ error: "authentication required", code: "unauthenticated" }, 401);
        return true;
      }

      if (pathname === "/auth/password" && method === "POST") {
        readJsonBody(req, (body) => {
          const current = String((body && body.current) || "");
          const next = String((body && (body.next || body.new)) || "");
          const user = store.findUser(identity.username);
          if (!user || !verifyPassword(current, user.password_salt, user.password_hash)) {
            sendJson({ error: "当前密码不正确", code: "bad_current_password" }, 401);
            return;
          }
          if (next.length < MIN_PASSWORD_LENGTH) {
            sendJson({ error: `新密码至少 ${MIN_PASSWORD_LENGTH} 位`, code: "weak_password" }, 400);
            return;
          }
          if (next.toLowerCase() === identity.username.toLowerCase()) {
            sendJson({ error: "密码不能与用户名相同", code: "weak_password" }, 400);
            return;
          }
          try {
            // keepToken/keepIp: the browser making the change stays logged in,
            // every other one has to authenticate again.
            store.setPassword(identity.userId, next, { keepToken: token, keepIp: ip });
            sendJson({ ok: true });
          } catch (error) {
            sendJson({ error: String((error && error.message) || error), code: "rejected" }, 400);
          }
        });
        return true;
      }

      if (pathname === "/auth/devices" && method === "GET") {
        sendJson({
          devices: store.listTrustedDevicesFor(identity.userId),
          ttlDays: config.trustedIpDays,
          currentIp: ip,
        });
        return true;
      }

      if (method === "DELETE" && pathname.startsWith("/auth/devices/")) {
        const id = Number(pathname.slice("/auth/devices/".length));
        if (!Number.isInteger(id) || id <= 0) {
          sendJson({ error: "invalid device id", code: "bad_request" }, 400);
          return true;
        }
        const removed = store.forgetDeviceFor(identity.userId, id);
        if (!removed) {
          sendJson({ error: "device not found", code: "not_found" }, 404);
          return true;
        }
        sendJson({ ok: true });
        return true;
      }

      if (pathname === "/auth/group" && method === "GET") {
        const group = store.groupForUser(identity.userId);
        if (!group) {
          sendJson({ group: null, members: [], openInvites: 0 });
          return true;
        }
        sendJson({
          group: {
            id: group.id,
            name: group.name,
            prompt: group.prompt || "",
            role: store.roleFor(identity.userId),
            owner: store.isOwner(identity.userId),
          },
          members: store.listMembers(group.id).map((row) => ({
            userId: row.user_id,
            username: row.username,
            role: store.roleFor(row.user_id),
            owner: store.isOwner(row.user_id),
            note: row.note || "",
            joinedAt: row.joined_at,
            isSelf: row.user_id === identity.userId,
          })),
          openInvites: store.countOpenInvites(group.id),
          inviteTtlMs: INVITE_TTL_MS,
        });
        return true;
      }

      if (method === "PATCH" && pathname === "/auth/group/prompt") {
        readJsonBody(req, (body) => {
          const currentGroup = store.groupForUser(identity.userId);
          if (!currentGroup) {
            sendJson({ error: "当前账号不在家庭组里", code: "no_group" }, 409);
            return;
          }
          // Same rule as member notes: the prompt is read by the agent and can
          // steer it, so only the machine owner gets to write it.
          if (!store.isOwner(identity.userId)) {
            sendJson({ error: "只有本机账号能编辑家庭组备注", code: "rejected" }, 403);
            return;
          }
          try {
            const prompt = store.setGroupPrompt(currentGroup.id, body && body.prompt);
            sendJson({ ok: true, prompt });
          } catch (error) {
            sendJson({ error: String((error && error.message) || error), code: "rejected" }, 400);
          }
        });
        return true;
      }

      if (pathname === "/auth/group/invites" && method === "POST") {
        const group = store.groupForUser(identity.userId);
        if (!group) {
          sendJson({ error: "当前账号不在家庭组里", code: "no_group" }, 409);
          return true;
        }
        try {
          const invite = store.createInvite(group.id, identity.userId);
          // The plaintext code exists only in this response: the row holds a
          // digest, so it cannot be shown again later, only reissued.
          sendJson({ ok: true, code: invite.code, expiresAt: invite.expiresAt, ttlMs: INVITE_TTL_MS });
        } catch (error) {
          sendJson({ error: String((error && error.message) || error), code: "rejected" }, 500);
        }
        return true;
      }

      if (method === "PATCH" && pathname === "/auth/group/member-note") {
        readJsonBody(req, (body) => {
          const targetUserId = Number(body && body.userId);
          const currentGroup = store.groupForUser(identity.userId);
          if (!currentGroup) {
            sendJson({ error: "当前账号不在家庭组里", code: "no_group" }, 409);
            return;
          }
          // A note describes a person to the agent ("我爸"). Letting any member
          // overwrite someone else's relationship would let one member mislabel
          // another, so only the machine owner edits notes.
          if (!store.isOwner(identity.userId)) {
            sendJson({ error: "只有本机账号能编辑成员备注", code: "rejected" }, 403);
            return;
          }
          if (!Number.isInteger(targetUserId)) {
            sendJson({ error: "userId required", code: "rejected" }, 400);
            return;
          }
          try {
            const note = store.setMemberNote(currentGroup.id, targetUserId, body && body.note);
            sendJson({ ok: true, userId: targetUserId, note });
          } catch (error) {
            sendJson({ error: String((error && error.message) || error), code: "rejected" }, 400);
          }
        });
        return true;
      }

      if (method === "PATCH" && pathname === "/auth/group/member-role") {
        // Handing out admin decides who else can drive this machine, so the
        // owner check runs before the group lookup and before we look at the
        // body: a non-owner gets 403 regardless of what they send, which keeps
        // the response from confirming whether a group even exists.
        if (!store.isOwner(identity.userId)) {
          sendJson({ error: "只有本机账号能改成员权限", code: "rejected" }, 403);
          // Must claim the route even on refusal. A bare `return` here returns
          // undefined, auth.handle() looks unhandled, and server.cjs writes a
          // 404 on top of the 403 we just sent (ERR_HTTP_HEADERS_SENT).
          return true;
        }
        readJsonBody(req, (body) => {
          const targetUserId = Number(body && body.userId);
          if (!Number.isInteger(targetUserId)) {
            sendJson({ error: "userId required", code: "rejected" }, 400);
            return;
          }
          try {
            const changed = store.setMemberRole(identity.userId, targetUserId, body && body.role);
            const role = store.roleFor(targetUserId);
            sendJson({ ok: true, userId: targetUserId, role, changed });
          } catch (error) {
            sendJson({ error: String((error && error.message) || error), code: "rejected" }, 400);
          }
        });
        return true;
      }

      if (pathname === "/auth/group/join" && method === "POST") {
        readJsonBody(req, (body) => {
          const code = String((body && body.code) || "");
          const invite = store.lookupInvite(code);
          if (!invite) {
            sendJson({ error: "邀请码无效或已过期", code: "bad_invite" }, 400);
            return;
          }
          store.addMember(invite.group_id, identity.userId);
          store.consumeInvite(code, identity.userId);
          sendJson({ ok: true, group: describeGroup(identity.userId) });
        });
        return true;
      }

      sendJson({ error: "unknown auth endpoint", code: "not_found" }, 404);
      return true;
    }

    if (identity) return false;

    if (isLocalDirectRequest(req)) return false;

    if (wantsHtml(req, pathname)) {
      // 保留用户原本要去的路径，登录后原路返回。
      // Caddy 会把 /filemanage 改写成 / 再转过来，所以只看 req.url 永远拿不到
      // 真实目标，必须读代理透传的原始 URI。
      let next = "/";
      const rawNext = req.headers["x-original-uri"]
        || req.headers["x-forwarded-uri"]
        || req.url
        || "/";
      try {
        const u = new URL(rawNext, "http://placeholder");
        next = u.pathname + (u.search || "");
      } catch {
        next = "/";
      }
      // 只允许站内相对路径，挡掉 //evil.com 这类开放重定向
      if (!next.startsWith("/") || next.startsWith("//")) next = "/";
      res.writeHead(302, { Location: "/login.html?next=" + encodeURIComponent(next) });
      res.end();
      return true;
    }
    sendJson({ error: "authentication required" }, 401);
    return true;
  }

  // `enabled` is exposed as a getter rather than a snapshot, and it is exposed
  // at all because the ownership layer in server.cjs reads `auth.enabled` to
  // decide "multi-user mode or single-user desktop mode". When this object
  // lacked the property, `auth.enabled` was undefined, so every ownership check
  // fell through its "authentication off" branch and returned unfiltered
  // results - while auth.handle(), which reads config.enabled, still answered
  // 401 for anonymous callers. The visible symptom was the worst kind: logged
  // in users saw every session on the machine, and the 401s looked like
  // isolation was working. A getter keeps it correct even if config is patched
  // later at startup.
  return {
    status,
    handle,
    store,
    config,
    resolveSession,
    // Exported because the API server has to ask the same question - "is this
    // request from this machine, with no proxy in front of it?" - before it lets
    // a caller declare whose account a background turn belongs to. A second
    // copy of that predicate is exactly the kind of rule that is right on the
    // day it is written and wrong the day the next route copies it.
    isLocalDirectRequest,
    get enabled() {
      return !!config.enabled;
    },
  };
}

function readJsonBody(req, callback) {
  let raw = "";
  req.on("data", (chunk) => {
    raw += chunk;
    if (raw.length > 64 * 1024) req.destroy();
  });
  req.on("end", () => {
    try {
      callback(raw ? JSON.parse(raw) : {});
    } catch {
      callback({});
    }
  });
}

// ---------------------------------------------------------------------------
// CLI: node frontend/auth.cjs adduser <name> [--replace] [--password <pw>]
// ---------------------------------------------------------------------------

function runCli(argv) {
  const [, , command, name, ...rest] = argv;
  const databasePath = process.env.FAIRY_AUTH_DB || path.join(__dirname, "..", "memory", "auth.db");
  const store = new AuthStore(databasePath);
  if (store.disabled) {
    console.error(`cannot open auth database: ${store.error}`);
    return 1;
  }
  if (command === "list") {
    for (const row of store.listUsers()) {
      console.log(`id=${row.id} user=${row.username} created=${new Date(row.created_at).toISOString()} disabled=${row.disabled}`);
    }
    const groupId = store.primaryGroupId();
    if (groupId != null) {
      const group = store.groupForUser(store.ownerUserId());
      console.log(`group id=${groupId} name=${group ? group.name : "(unnamed)"} open-invites=${store.countOpenInvites(groupId)}`);
      for (const member of store.listMembers(groupId)) {
        console.log(`  member user=${member.username} role=${store.roleFor(member.user_id)} joined=${new Date(member.joined_at).toISOString()}`);
      }
    }
    for (const device of store.listTrustedDevices()) {
      console.log(
        `  trusted-device id=${device.id} user=${device.username} ip=${device.ip} expires=${new Date(device.expires_at).toISOString()}`
      );
    }
    return 0;
  }
  if (command === "invite") {
    // The escape hatch for when nobody can reach the settings panel yet - the
    // gate was just switched on, or the only browser in the house is the one
    // that is locked out. Prints the code once, the same as the panel does.
    const inviter = store.findUser(name);
    if (!inviter) {
      console.error(`unknown user: ${name}`);
      return 1;
    }
    const group = store.groupForUser(inviter.id);
    if (!group) {
      console.error(`'${name}' is not in a family group`);
      return 1;
    }
    const invite = store.createInvite(group.id, inviter.id);
    console.log(`invite for group '${group.name}' (created by ${name}):`);
    console.log(`  ${invite.code}`);
    console.log(`  expires ${new Date(invite.expiresAt).toISOString()}`);
    return 0;
  }
  if (command === "issue-token") {
    // A channel bridge (wechat / qq) runs on this machine but speaks to the
    // API as a person, not as the loopback owner. The only way to make its
    // messages land in that person's conversation tree is to hand it a real
    // session token, so this prints one exactly once, like the panel does.
    const user = store.findUser(name);
    if (!user) {
      console.error(`unknown user: ${name}`);
      return 1;
    }
    const daysIndex = rest.indexOf("--days");
    const days = daysIndex >= 0 ? Number(rest[daysIndex + 1]) : 3650;
    if (!Number.isFinite(days) || days < 0) {
      console.error("pass --days <n> (a positive number of days, or 0 for no expiry)");
      return 1;
    }
    const ttlMs = days === 0 ? 0 : days * 24 * 60 * 60 * 1000;
    const token = store.createSession(user.id, { ip: "127.0.0.1", userAgent: "fairy-cli/issue-token", ttlMs });
    console.log(`token for user '${user.username}' (id=${user.id}):`);
    console.log(`  ${token}`);
    console.log(`  expires ${days === 0 ? "never" : new Date(Date.now() + ttlMs).toISOString()}`);
    return 0;
  }
  if (command !== "adduser") {
    console.log("usage: node frontend/auth.cjs adduser <username> [--replace] [--password <password>]");
    console.log("       node frontend/auth.cjs invite <username>");
    console.log("       node frontend/auth.cjs issue-token <username> [--days <n>]");
    console.log("       node frontend/auth.cjs list");
    return 1;
  }
  const replace = rest.includes("--replace");
  const passwordIndex = rest.indexOf("--password");
  let password = passwordIndex >= 0 ? rest[passwordIndex + 1] : "";
  if (!password) {
    console.error("pass --password <value> (or set FAIRY_AUTH_PASSWORD) to avoid a shell history entry");
    password = process.env.FAIRY_AUTH_PASSWORD || "";
  }
  try {
    const id = store.addUser(name, password, { replace });
    console.log(`user '${name}' ready (id=${id}) in ${databasePath}`);
    if (password.length < 10 || password.toLowerCase() === String(name).toLowerCase()) {
      console.warn(
        `[!] '${name}' uses a short or guessable password.\n` +
          `    What protects it is the login throttle (${LOGIN_MAX_ATTEMPTS} attempts per ` +
          `${LOGIN_WINDOW_MS / 60000} min per IP+username), which is now stored in the database\n` +
          `    so a restart does not hand out a fresh budget. Anything that can reach this port\n` +
          `    also gets to run shell commands on this machine once it is in, so keep the LAN\n` +
          `    trusted, or change this password before exposing it more widely.`
      );
    }
    return 0;
  } catch (error) {
    console.error(String(error.message || error));
    return 1;
  }
}

module.exports = {
  createAuth,
  AuthStore,
  hashPassword,
  verifyPassword,
  hashToken,
  clientIp,
  isLocalDirectRequest,
  normalizeInviteCode,
  isPublicPath,
  sessionCookie,
  clearCookie,
  SESSION_COOKIE,
};

if (require.main === module) {
  process.exit(runCli(process.argv));
}
