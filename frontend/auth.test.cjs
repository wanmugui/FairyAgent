"use strict";

/**
 * Unit tests for the account store: the family group, invites, and the
 * password/device rules the settings panel leans on.
 *
 * These run against a throwaway database in the OS temp directory. They
 * deliberately do not touch memory/auth.db - the developer's own accounts and
 * trusted devices live there, and a test that rewrites them is a test nobody
 * runs twice.
 */

const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

const { AuthStore, clientIp, isLocalDirectRequest, normalizeInviteCode } = require("./auth.cjs");

function freshStore() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "fairy-auth-test-"));
  const store = new AuthStore(path.join(dir, "auth.db"));
  assert.equal(store.disabled, false, `store disabled: ${store.error}`);
  return { store, dir };
}

test("migration creates one group and makes the first account its admin", () => {
  const { store } = freshStore();
  const owner = store.addUser("harry", "pw1234");
  // Claimed at creation, not at the next restart: the group is created empty by
  // the migration, and the account that follows has to land in it immediately or
  // the settings panel is blank on a fresh install.
  assert.equal(store.isOwner(owner), true);
  assert.equal(store.roleFor(owner), "admin");
  assert.ok(store.groupForUser(owner), "owner should be in the family group");

  const reopened = new AuthStore(store.databasePath);
  assert.equal(reopened.roleFor(owner), "admin");
  assert.ok(reopened.groupForUser(owner), "membership survives a reopen");
});

test("signing out also forgets the device it was signed in from", () => {
  const { store } = freshStore();
  const userId = store.addUser("harry", "pw1234");
  store.trustDevice(userId, "10.0.0.5");
  const remembered = store.isTrustedIp("10.0.0.5");
  assert.ok(remembered);
  // What the logout route does.
  store.forgetDeviceFor(remembered.user_id, remembered.id);
  assert.equal(store.isTrustedIp("10.0.0.5"), null, "a signed-out device must not sign itself back in");
});

test("a member added by invite is not an admin", () => {
  const { store, dir } = freshStore();
  const owner = store.addUser("harry", "pw1234");
  const member = store.addUser("mei", "pw1234");
  const reopened = new AuthStore(path.join(dir, "auth.db"));
  const group = reopened.groupForUser(owner);
  reopened.addMember(group.id, member);
  assert.equal(reopened.roleFor(member), "member");
  assert.equal(reopened.isAdmin(member), false);
  assert.equal(reopened.listMembers(group.id).length, 2);
});

test("the owner stays admin even if the membership row is demoted by hand", () => {
  const { store } = freshStore();
  const owner = store.addUser("harry", "pw1234");
  const reopened = new AuthStore(store.databasePath);
  const group = reopened.groupForUser(owner);
  reopened.db
    .prepare("UPDATE group_members SET role = 'member' WHERE group_id = ? AND user_id = ?")
    .run(group.id, owner);
  assert.equal(reopened.isAdmin(owner), true);
});

test("invite codes are single use and expiring", () => {
  const { store } = freshStore();
  const owner = store.addUser("harry", "pw1234");
  const reopened = new AuthStore(store.databasePath);
  const group = reopened.groupForUser(owner);

  const invite = reopened.createInvite(group.id, owner);
  assert.match(invite.code, /^[0-9A-Z]{4}-[0-9A-Z]{4}$/);
  // The plaintext is not stored, so the row must not contain it.
  const rows = reopened.db.prepare("SELECT code_hash FROM group_invites").all();
  assert.equal(rows.length, 1);
  assert.notEqual(rows[0].code_hash, invite.code);

  // Typing it back is forgiving: case, dashes and spaces do not matter.
  assert.ok(reopened.lookupInvite(invite.code.toLowerCase().replace("-", " ")));
  assert.equal(reopened.lookupInvite("ZZZZ-ZZZZ"), null);

  const member = reopened.addUser("mei", "pw1234");
  reopened.consumeInvite(invite.code, member);
  assert.equal(reopened.lookupInvite(invite.code), null, "a used invite must not work twice");
  assert.equal(reopened.countOpenInvites(group.id), 0);

  const expired = reopened.createInvite(group.id, owner, { ttlMs: -1000 });
  assert.equal(reopened.lookupInvite(expired.code), null, "an expired invite must not work");
});

test("normalizeInviteCode strips the presentation, not the code", () => {
  assert.equal(normalizeInviteCode(" ab cd-efgh "), "ABCDEFGH");
  assert.equal(normalizeInviteCode(""), "");
});

test("changing a password keeps the caller's session and drops the rest", () => {
  const { store } = freshStore();
  const userId = store.addUser("harry", "pw1234");
  const mine = store.createSession(userId, { ip: "10.0.0.5" });
  const other = store.createSession(userId, { ip: "10.0.0.9" });
  store.trustDevice(userId, "10.0.0.5");
  store.trustDevice(userId, "10.0.0.9");

  store.setPassword(userId, "pw5678", { keepToken: mine, keepIp: "10.0.0.5" });

  assert.ok(store.lookupSession(mine), "the browser that made the change stays logged in");
  assert.equal(store.lookupSession(other), null, "other sessions are dropped");
  const ips = store.listTrustedDevicesFor(userId).map((row) => row.ip);
  assert.deepEqual(ips, ["10.0.0.5"]);
  assert.equal(store.findUser("harry") ? true : false, true);
});

test("a device id from another account is not the caller's to forget", () => {
  const { store } = freshStore();
  const a = store.addUser("harry", "pw1234");
  const b = store.addUser("mei", "pw1234");
  store.trustDevice(b, "10.0.0.9");
  const [device] = store.listTrustedDevicesFor(b);
  assert.equal(store.forgetDeviceFor(a, device.id), false);
  assert.equal(store.listTrustedDevicesFor(b).length, 1);
  assert.equal(store.forgetDeviceFor(b, device.id), true);
});

test("local direct requests are the ones with no proxy in front of them", () => {
  const direct = { socket: { remoteAddress: "127.0.0.1" }, headers: {} };
  const proxied = { socket: { remoteAddress: "127.0.0.1" }, headers: { "x-forwarded-for": "<lan-ip>" } };
  const lan = { socket: { remoteAddress: "<lan-ip>" }, headers: {} };
  const lanClaiming = { socket: { remoteAddress: "<lan-ip>" }, headers: { "x-forwarded-for": "127.0.0.1" } };
  assert.equal(isLocalDirectRequest(direct), true);
  assert.equal(isLocalDirectRequest(proxied), false, "a browser behind the proxy is not the local operator");
  assert.equal(isLocalDirectRequest(lan), false);
  assert.equal(isLocalDirectRequest(lanClaiming), false, "a spoofed header must not buy local trust");
});

// X-Forwarded-For grows by appending, so the leftmost entry is whatever the
// original client chose to send. A reverse proxy only ever appends the address
// it actually saw, which makes the rightmost entry the only trustworthy one.
test("clientIp reads the proxy-appended hop, not the client-supplied one", () => {
  const viaProxy = (xff) => ({ socket: { remoteAddress: "127.0.0.1" }, headers: { "x-forwarded-for": xff } });
  assert.equal(clientIp(viaProxy("203.0.113.7")), "203.0.113.7");
  assert.equal(
    clientIp(viaProxy("127.0.0.1, 203.0.113.7")),
    "203.0.113.7",
    "a forged leading entry must not impersonate a trusted device",
  );
  assert.equal(
    clientIp(viaProxy("127.0.0.1, 10.0.0.5, 203.0.113.7")),
    "203.0.113.7",
    "extra upstream hops must not let the client dictate the address",
  );
  assert.equal(clientIp({ socket: { remoteAddress: "198.51.100.4" }, headers: {} }), "198.51.100.4");
});

// ---------------------------------------------------------------------------
// Channel bindings
// ---------------------------------------------------------------------------

test("a bind code is one-shot: the second redeem of the same code fails", () => {
  const { store } = freshStore();
  const harry = store.addUser("harry", "pw1234");
  const issued = store.issueBindCode(harry);
  assert.match(issued.code, /^[23456789ABCDEFGHJKLMNPQRSTUVWXYZ]{4}-[23456789ABCDEFGHJKLMNPQRSTUVWXYZ]{4}$/);
  assert.ok(issued.expiresAt > Date.now(), "the code carries its own deadline");

  const first = store.consumeBindCode(issued.code);
  assert.equal(first.id, harry);
  // The whole point of storing a digest: a burnt code is not "rejected", it is
  // indistinguishable from one that never existed.
  assert.equal(store.consumeBindCode(issued.code), null);
  assert.equal(store.lookupBindCode(issued.code), null);
});

test("issuing a new code kills the previous one", () => {
  const { store } = freshStore();
  const harry = store.addUser("harry", "pw1234");
  const stale = store.issueBindCode(harry);
  store.issueBindCode(harry);
  assert.equal(store.consumeBindCode(stale.code), null);
});

test("a bind code expires ten minutes after it was minted", () => {
  const { store } = freshStore();
  const harry = store.addUser("harry", "pw1234");
  const t0 = Date.now();
  const issued = store.issueBindCode(harry, t0);
  assert.equal(issued.expiresAt, t0 + 10 * 60 * 1000);
  assert.ok(store.lookupBindCode(issued.code, t0 + 9 * 60 * 1000), "still good at nine minutes");
  assert.equal(store.lookupBindCode(issued.code, t0 + 10 * 60 * 1000), null, "dead at ten");
  assert.equal(store.consumeBindCode(issued.code, t0 + 11 * 60 * 1000), null);
});

test("the database never holds a usable bind code in plaintext", () => {
  const { store } = freshStore();
  const harry = store.addUser("harry", "pw1234");
  const issued = store.issueBindCode(harry);
  const raw = fs.readFileSync(store.databasePath);
  assert.equal(raw.includes(Buffer.from(issued.code, "utf8")), false);
  assert.equal(raw.includes(Buffer.from(issued.code.replace("-", ""), "utf8")), false);
});

test("a conversation is owned by one account, and a later /bind takes it over", () => {
  const { store } = freshStore();
  const harry = store.addUser("harry", "pw1234");
  const father = store.addUser("father", "pw1234");

  store.bindChannel("qq", "openid-A", harry);
  store.bindChannel("qq", "openid-B", father);
  // A group binds the group, not the person who happened to type /bind first.
  store.bindChannel("qq", "group-1", harry);

  assert.equal(store.lookupChannelBinding("qq", "openid-A").user_id, harry);
  assert.equal(store.lookupChannelBinding("qq", "openid-B").user_id, father);
  assert.equal(store.lookupChannelBinding("wechat", "openid-A"), null, "channels are separate namespaces");

  store.bindChannel("qq", "openid-A", father);
  assert.equal(store.lookupChannelBinding("qq", "openid-A").user_id, father);
  assert.equal(store.lookupChannelBinding("qq", "openid-A").bound_at >= 0, true);
});

test("listing bindings shows only your own conversations", () => {
  const { store } = freshStore();
  const harry = store.addUser("harry", "pw1234");
  const father = store.addUser("father", "pw1234");
  store.bindChannel("qq", "openid-A", harry);
  store.bindChannel("wechat", "ilink-B", harry);
  store.bindChannel("qq", "openid-C", father);

  const mine = store.listChannelBindings(harry).map((r) => r.conversation_id).sort();
  assert.deepEqual(mine, ["ilink-B", "openid-A"]);
  assert.equal(store.listChannelBindings(father).length, 1);
});

test("unbinding is scoped: you can only remove your own conversation", () => {
  const { store } = freshStore();
  const harry = store.addUser("harry", "pw1234");
  const father = store.addUser("father", "pw1234");
  store.bindChannel("qq", "openid-A", harry);
  store.bindChannel("qq", "openid-C", father);

  assert.equal(store.unbindChannel("qq", "openid-C", harry), false, "father's binding is not harry's to drop");
  assert.ok(store.lookupChannelBinding("qq", "openid-C"));
  assert.equal(store.unbindChannel("qq", "openid-A", harry), true);
  assert.equal(store.lookupChannelBinding("qq", "openid-A"), null);
});

test("an unknown channel cannot be bound", () => {
  const { store } = freshStore();
  const harry = store.addUser("harry", "pw1234");
  assert.throws(() => store.bindChannel("telegram", "x", harry), /unknown channel/);
  assert.throws(() => store.bindChannel("qq", "   ", harry), /conversation id/);
});

test("deleting an account takes its conversations and pending codes with it", () => {
  const { store } = freshStore();
  const harry = store.addUser("harry", "pw1234");
  store.bindChannel("qq", "openid-A", harry);
  const pending = store.issueBindCode(harry);
  // The cascade is the guarantee that a deleted account cannot leave a
  // conversation behind that still speaks as somebody.
  store.db.prepare("DELETE FROM users WHERE id = ?").run(harry);
  assert.equal(store.lookupChannelBinding("qq", "openid-A"), null);
  assert.equal(store.consumeBindCode(pending.code), null);
});

test("a member note round-trips, trims, and clears without touching anyone else", () => {
  const { store } = freshStore();
  const harry = store.addUser("harry", "pw1234");
  const sister = store.addUser("sister", "pw1234");
  const group = store.groupForUser(harry);
  store.addMember(group.id, harry, "admin", harry);
  store.addMember(group.id, sister, "member");

  assert.equal(store.listMembers(group.id)[0].note, "");
  // Whitespace around a typed relationship is a typing accident, not part of
  // the relationship, and it would defeat an exact-match lookup later.
  assert.equal(store.setMemberNote(group.id, harry, "  我爸  "), "我爸");
  const notes = new Map(store.listMembers(group.id).map((m) => [m.user_id, m.note]));
  assert.equal(notes.get(harry), "我爸");
  assert.equal(notes.get(sister), "", "editing one member must not touch the other");

  // Empty input clears instead of storing a blank that renders as an empty box.
  assert.equal(store.setMemberNote(group.id, harry, "   "), "");
  assert.equal(store.listMembers(group.id)[0].note, "");
});

test("a note can only be written for somebody who is actually in the group", () => {
  const { store } = freshStore();
  const harry = store.addUser("harry", "pw1234");
  const group = store.groupForUser(harry);
  store.addMember(group.id, harry, "admin", harry);
  // Silently upserting here would create a member row for a stranger.
  assert.throws(() => store.setMemberNote(group.id, 4242, "陌生人"), /member not in group/);
});

test("group_members gains its note column when migrate runs on an older database", () => {
  const { store, dir } = freshStore();
  // Simulate a database written before note existed: CREATE TABLE IF NOT
  // EXISTS will not add the column, so only the explicit ALTER can.
  store.db.exec("ALTER TABLE group_members RENAME TO group_members_old");
  store.db.exec(
    `CREATE TABLE group_members (
       group_id INTEGER NOT NULL, user_id INTEGER NOT NULL,
       role TEXT NOT NULL DEFAULT 'member', joined_at INTEGER NOT NULL,
       PRIMARY KEY (group_id, user_id))`,
  );
  store.db.exec("DROP TABLE group_members_old");

  store.migrate(); // must not throw on the duplicate-column path
  store.migrate(); // and must stay idempotent on a second boot

  const columns = store.db.prepare("PRAGMA table_info(group_members)").all().map((c) => c.name);
  assert.ok(columns.includes("note"), `note column missing after migrate: ${columns}`);
  fs.rmSync(dir, { recursive: true, force: true });
});

test("a household prompt round-trips, trims, and is readable through groupForUser", () => {
  const { store } = freshStore();
  const harry = store.addUser("harry", "pw1234");
  const group = store.groupForUser(harry);
  assert.equal(group.prompt, "");

  // The agent reads this when deciding who "我爸" is, so the raw value has to
  // survive storage exactly (modulo surrounding whitespace) and be visible
  // through the same lookup the API uses.
  assert.equal(store.setGroupPrompt(group.id, "  我爸住外地  "), "我爸住外地");
  assert.equal(store.groupForUser(harry).prompt, "我爸住外地");

  assert.equal(store.setGroupPrompt(group.id, "  "), "");
  assert.equal(store.groupForUser(harry).prompt, "");
});

test("a household prompt can only be written for a group that exists", () => {
  const { store } = freshStore();
  store.addUser("harry", "pw1234");
  // Otherwise this would silently create a phantom group row.
  assert.throws(() => store.setGroupPrompt(4242, "x"), /group not found/);
});

test("groups gains its prompt column when migrate runs on an older database", () => {
  const { store, dir } = freshStore();
  store.db.exec("ALTER TABLE groups RENAME TO groups_old");
  store.db.exec(
    `CREATE TABLE groups (
       id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL,
       created_at INTEGER NOT NULL)`,
  );
  store.db.exec("DROP TABLE groups_old");

  store.migrate(); // must not throw on the duplicate-column path
  store.migrate(); // and must stay idempotent on a second boot

  const columns = store.db.prepare("PRAGMA table_info(groups)").all().map((c) => c.name);
  assert.ok(columns.includes("prompt"), `prompt column missing after migrate: ${columns}`);
  fs.rmSync(dir, { recursive: true, force: true });
});

test("the owner can hand out admin and take it back", () => {
  const { store } = freshStore();
  const harry = store.addUser("harry", "pw1234");
  const sister = store.addUser("sister", "pw1234");
  const group = store.groupForUser(harry);
  store.addMember(group.id, sister, "member");

  assert.equal(store.isAdmin(sister), false);
  assert.equal(store.setMemberRole(harry, sister, "admin"), true);
  assert.equal(store.isAdmin(sister), true);
  assert.equal(store.roleFor(sister), "admin");

  assert.equal(store.setMemberRole(harry, sister, "member"), true);
  assert.equal(store.isAdmin(sister), false);
  assert.equal(store.roleFor(sister), "member");
});

test("a non-owner cannot hand out admin", () => {
  const { store } = freshStore();
  const harry = store.addUser("harry", "pw1234");
  const sister = store.addUser("sister", "pw1234");
  const mallory = store.addUser("mallory", "pw1234");
  const group = store.groupForUser(harry);
  store.addMember(group.id, sister, "member");
  store.addMember(group.id, mallory, "member");
  store.setMemberRole(harry, sister, "admin");

  // The route answers 403 for this; the store refuses regardless of who calls.
  assert.throws(() => store.setMemberRole(mallory, sister, "member"), /只有本机账号/);
  assert.equal(store.roleFor(sister), "admin", "a refused revoke must not change the role");
});

test("addMember cannot mint an admin without the owner on the other end", () => {
  const { store } = freshStore();
  const harry = store.addUser("harry", "pw1234");
  const mallory = store.addUser("mallory", "pw1234");
  const group = store.groupForUser(harry);

  // This is the shape the backlog called out: no actor, role just written through.
  assert.throws(() => store.addMember(group.id, mallory, "admin"), /只有本机账号/);
  assert.equal(store.isAdmin(mallory), false);

  // Same call with the owner on the other end still works.
  store.addMember(group.id, mallory, "admin", harry);
  assert.equal(store.isAdmin(mallory), true);
});

test("nobody can point setMemberRole at the owner", () => {
  const { store } = freshStore();
  const harry = store.addUser("harry", "pw1234");
  const sister = store.addUser("sister", "pw1234");
  const group = store.groupForUser(harry);
  store.addMember(group.id, sister, "member");
  store.setMemberRole(harry, sister, "admin");

  // An admin that could demote the owner would leave nobody able to promote back.
  assert.throws(() => store.setMemberRole(harry, harry, "member"), /不能改本机账号/);
  assert.equal(store.isOwner(harry), true, "the owner must not be demotable");
  // roleFor reports "admin" for the owner by design, so assert authority, not the string.
  assert.equal(store.isAdmin(harry), true);

  // The refusal is about the target being the owner, not about the role value.
  assert.throws(() => store.setMemberRole(harry, harry, "admin"), /不能改本机账号/);
  assert.equal(store.isOwner(harry), true);
});
