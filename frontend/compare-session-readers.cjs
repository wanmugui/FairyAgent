#!/usr/bin/env node
/**
 * Proves the database readers agree with the file readers before anything is
 * switched over.
 *
 * Runs against the live memory store, read-only:
 *   1. cleanSessionPreviewText must be byte-identical to the server's copy, or the
 *      refactor changed behaviour rather than moving it
 *   2. readSessionRecord: file vs database, deeply equal, per session
 *   3. listSessions: file vs database, the *entire* array including `modified`
 *      strings and the resulting order, because an ordering difference renders as
 *      a plausible-looking sidebar rather than an error
 */

"use strict";

const fs = require("node:fs");
const path = require("node:path");
const { DatabaseSync } = require("node:sqlite");
const store = require("./session_store.cjs");

const REPO = path.join(__dirname, "..");
const SESSIONS = path.join(REPO, "memory", "sessions");
const DB = process.env.FAIRY_AUTH_DB_TEST_DB || path.join(REPO, "memory", "fairy.db");
const SERVER = path.join(REPO, "frontend", "server.cjs");

const problems = [];

function canon(value) {
  if (Array.isArray(value)) return value.map(canon);
  if (value && typeof value === "object") {
    return Object.fromEntries(Object.keys(value).sort().map((key) => [key, canon(value[key])]));
  }
  return value;
}

// --- 1. the moved helper must be identical, not merely similar ----------------

const serverSource = fs.readFileSync(SERVER, "utf8");
const helperMatch = serverSource.match(/function cleanSessionPreviewText\(value\) \{[\s\S]*?\n\}/);
const storeSource = fs.readFileSync(path.join(__dirname, "session_store.cjs"), "utf8");
const movedMatch = storeSource.match(/function cleanSessionPreviewText\(value\) \{[\s\S]*?\n\}/);
const normalize = (text) => text.replace(/\r\n/g, "\n").trim();
if (!helperMatch) {
  problems.push("could not locate cleanSessionPreviewText in server.cjs");
} else if (!movedMatch) {
  problems.push("could not locate cleanSessionPreviewText in session_store.cjs");
} else if (normalize(helperMatch[0]) !== normalize(movedMatch[0])) {
  problems.push("cleanSessionPreviewText differs between server.cjs and session_store.cjs");
} else {
  console.log("helper      : cleanSessionPreviewText is byte-identical to server.cjs");
}

// --- file-based reference implementations (copied from server.cjs) -----------

function fileReadSessionRecord(name) {
  const file = path.join(SESSIONS, name, name + ".json");
  try {
    return JSON.parse(fs.readFileSync(file, "utf-8").replace(/^\uFEFF/, ""));
  } catch {
    return null;
  }
}

function fileListSessions() {
  const items = [];
  try {
    const dirs = fs.readdirSync(SESSIONS).filter((f) => {
      try {
        return fs.statSync(path.join(SESSIONS, f)).isDirectory();
      } catch {
        return false;
      }
    });
    for (const d of dirs) {
      try {
        const fp = path.join(SESSIONS, d, d + ".json");
        if (!fs.existsSync(fp)) continue;
        const stat = fs.statSync(fp);
        const data = JSON.parse(fs.readFileSync(fp, "utf-8").replace(/^\uFEFF/, ""));
        const msgs = data.messages || [];
        const firstUser = msgs.find((m) => m.role === "user");
        let preview = (firstUser && firstUser.content) || "";
        preview = store.cleanSessionPreviewText(preview).slice(0, 60);
        items.push({
          name: d,
          modified: stat.mtime.toISOString().replace("T", " ").slice(0, 19),
          message_count: msgs.length,
          preview,
          model: data.model || null,
          kind: data.kind || "main",
          parent_session: data.parent_session || null,
          domain: data.domain || null,
          created_by: data.created_by || null,
          created_at: data.created_at || null,
          daily_date: data.daily_date || null,
        });
      } catch {}
    }
    items.sort((a, b) => String(b.modified).localeCompare(String(a.modified)));
  } catch {}
  return items;
}

// --- 2. readSessionRecord ----------------------------------------------------

const db = new DatabaseSync(DB, { readOnly: true });
const names = fs
  .readdirSync(SESSIONS)
  .filter((f) => fs.existsSync(path.join(SESSIONS, f, f + ".json")));

let recordOk = 0;
for (const name of names) {
  const fromFile = fileReadSessionRecord(name);
  const fromDb = store.readSessionRecord(db, name);
  const same = JSON.stringify(canon(fromFile)) === JSON.stringify(canon(fromDb));
  console.log(`record      : ${name.padEnd(14)} ${same ? "equal" : "DIFFERS"}`);
  if (same) recordOk++;
  else problems.push(`readSessionRecord mismatch for ${name}`);
}

// --- 3. listSessions ---------------------------------------------------------

const fileItems = fileListSessions();
const dbItems = store.listSessions(db);
const listSame = JSON.stringify(fileItems) === JSON.stringify(dbItems);
console.log(`listSessions: file=${fileItems.length} db=${dbItems.length} ${listSame ? "entire array equal" : "DIFFERS"}`);
if (!listSame) {
  problems.push("listSessions arrays differ");
  const limit = Math.max(fileItems.length, dbItems.length);
  for (let i = 0; i < limit; i++) {
    const a = JSON.stringify(fileItems[i]);
    const b = JSON.stringify(dbItems[i]);
    if (a !== b) {
      console.log(`  index ${i}`);
      console.log(`    file: ${a}`);
      console.log(`    db  : ${b}`);
    }
  }
}

console.log("");
if (problems.length) {
  console.log(`COMPARE FAILED (${problems.length})`);
  for (const problem of problems) console.log(`  - ${problem}`);
  process.exit(1);
}
console.log(`COMPARE PASSED: helper identical, ${recordOk}/${names.length} records equal, list equal`);
