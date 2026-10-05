"use strict";

/**
 * Tests for the WeChat link-up backend.
 *
 * The protocol itself is Python and the real one needs a human with a phone, so
 * both ends are replaced with stubs: the flow script is a node script that emits
 * the same JSON lines link.py emits, and the bridge is a node script that does
 * nothing but stay alive. What is left under test is exactly what this file
 * wrote - the state machine, the credential file, the pid bookkeeping, and the
 * errors that must not be swallowed.
 */

const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

const { createWechatLink } = require("./wechat_link.cjs");

const FAKE_PNG = Buffer.from("fake-png-bytes").toString("base64");

function scratch() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "fairy-wechat-test-"));
  const dataDir = path.join(dir, "wechat");
  const repoRoot = path.join(dir, "repo");
  fs.mkdirSync(path.join(repoRoot, "config"), { recursive: true });
  fs.mkdirSync(path.join(repoRoot, "skills", "wechat-bot"), { recursive: true });
  fs.writeFileSync(path.join(repoRoot, "config", "channels.json"), JSON.stringify({ qq: { enabled: true } }, null, 2));
  return { dir, dataDir, repoRoot };
}

/**
 * A stand-in for link.py: it writes the events a real scan would produce, then
 * exits. Run under node (pythonBin = node) so the test needs no Python.
 */
function writeFlowStub(dir, name, lines, { exitCode = 0, stderr = "" } = {}) {
  const file = path.join(dir, name);
  const body = `
const lines = ${JSON.stringify(lines)};
${stderr ? `process.stderr.write(${JSON.stringify(stderr)});` : ""}
for (const line of lines) { process.stdout.write(JSON.stringify(line) + "\\n"); }
if (lines.length) { process.stdout.write("\\n"); }
process.exit(${exitCode});
`;
  fs.writeFileSync(file, body);
  return file;
}

/** A stand-in for bot.py: alive until something kills it. */
function writeBridgeStub(dir, name) {
  const file = path.join(dir, name);
  fs.writeFileSync(file, "setTimeout(() => {}, 600000);\n");
  return file;
}

function makeLink(scope, overrides = {}) {
  return createWechatLink(
    Object.assign(
      {
        repoRoot: scope.repoRoot,
        dataDir: scope.dataDir,
        logDir: scope.dir,
        // Both stubs are node scripts, so the "python" binary is node itself.
        pythonBin: process.execPath,
      },
      overrides
    )
  );
}

/** A request good enough for the body reader in wechat_link.cjs. */
function fakeRequest(body) {
  const listeners = {};
  return {
    on(event, handler) {
      listeners[event] = handler;
    },
    send() {
      setImmediate(() => {
        if (body !== undefined && listeners.data) listeners.data(JSON.stringify(body));
        if (listeners.end) listeners.end();
      });
    },
  };
}

function call(link, method, pathname, { body, identity } = {}) {
  return new Promise((resolve) => {
    const req = fakeRequest(body);
    const answered = link.handle(
      req,
      {},
      pathname,
      method,
      { searchParams: new URLSearchParams() },
      (payload, status) => resolve({ status: status || 200, body: payload }),
      identity
    );
    if (!answered) resolve({ status: 404, body: { error: "not handled" } });
    req.send();
  });
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function waitFor(predicate, timeoutMs = 5000) {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const value = predicate();
    if (value) return value;
    if (Date.now() > deadline) throw new Error("condition never became true");
    await sleep(50);
  }
}

test("a scan that succeeds writes one credential, allows the scanner, and starts the bridge", async () => {
  const scope = scratch();
  const flowScript = writeFlowStub(scope.dir, "flow-confirm.cjs", [
    { event: "qr", qrcode: "q-1", png_base64: FAKE_PNG, expires_in: 300 },
    { event: "confirmed", bot_token: "tok-1", ilink_bot_id: "bot@1", ilink_user_id: "wx-user-7", base_url: "https://ilink.example.com/" },
  ]);
  const bridgeScript = writeBridgeStub(scope.dir, "bridge.cjs");
  const link = makeLink(scope, { flowScript, bridgeScript });

  const started = await call(link, "POST", "/api/wechat/link/start", { identity: { username: "harry" } });
  assert.equal(started.status, 200);
  assert.equal(started.body.ok, true);

  const confirmed = await waitFor(() => {
    const snap = link.snapshot("harry");
    return snap.flow && snap.flow.stage === "confirmed" ? snap : null;
  });
  assert.equal(confirmed.linked, true);
  assert.equal(confirmed.account.botId, "bot@1");
  assert.equal(confirmed.account.baseUrl, "https://ilink.example.com", "trailing slash is trimmed");
  assert.equal(confirmed.bridge.running, true, "the bridge that answers messages is started");

  const credential = JSON.parse(fs.readFileSync(link.accountPath("harry"), "utf-8"));
  assert.equal(credential.bot_token, "tok-1");
  assert.deepEqual(credential.allow_users, ["wx-user-7"], "the bot only answers the WeChat that linked it");
  // The credential is a bearer token; it must not be world-readable.
  if (process.platform !== "win32") {
    assert.equal(fs.statSync(link.accountPath("harry")).mode & 0o777, 0o600);
  }

  // The link must not be readable through the status endpoint of another account.
  const other = link.snapshot("father");
  assert.equal(other.linked, false);

  await call(link, "DELETE", "/api/wechat/account", { identity: { username: "harry" } });
  assert.equal(fs.existsSync(link.accountPath("harry")), false);
  assert.equal(link.snapshot("harry").bridge.running, false, "unlinking stops the bridge it started");
});

test("the QR and its state are visible while waiting, and cancel takes it away", async () => {
  const scope = scratch();
  // A flow that publishes a QR and then waits forever, like a real one does.
  const flowScript = path.join(scope.dir, "flow-hold.cjs");
  fs.writeFileSync(
    flowScript,
    `process.stdout.write(JSON.stringify({ event: "qr", qrcode: "q-2", png_base64: ${JSON.stringify(FAKE_PNG)}, expires_in: 300 }) + "\\n");\n` +
      "setTimeout(() => {}, 600000);\n"
  );
  const link = makeLink(scope, { flowScript, bridgeScript: writeBridgeStub(scope.dir, "bridge2.cjs") });

  const started = await call(link, "POST", "/api/wechat/link/start", { identity: { username: "harry" } });
  assert.equal(started.status, 200);

  const waiting = await waitFor(() => {
    const snap = link.snapshot("harry");
    return snap.flow && snap.flow.stage === "waiting" && snap.flow.qr ? snap : null;
  });
  assert.match(waiting.flow.qr, /^data:image\/png;base64,/, "the panel gets an image it can show, not a token");
  assert.ok(waiting.flow.expiresAt > Date.now(), "and it knows when to stop showing it");
  assert.equal(waiting.linked, false, "nothing is linked until the scan is confirmed");

  const cancelled = await call(link, "POST", "/api/wechat/link/cancel", { identity: { username: "harry" } });
  assert.equal(cancelled.body.ok, true);
  assert.equal(link.snapshot("harry").flow, null);
});

test("a flow that dies reports why instead of looking like it is still waiting", async () => {
  const scope = scratch();
  const flowScript = writeFlowStub(scope.dir, "flow-boom.cjs", [], {
    exitCode: 1,
    stderr: "Traceback (most recent call last):\nrequests.exceptions.ConnectTimeout: ilink unreachable\n",
  });
  const link = makeLink(scope, { flowScript, bridgeScript: writeBridgeStub(scope.dir, "bridge3.cjs") });

  await call(link, "POST", "/api/wechat/link/start", { identity: { username: "harry" } });
  const failed = await waitFor(() => {
    const snap = link.snapshot("harry");
    return snap.flow && snap.flow.stage === "error" ? snap : null;
  });
  assert.match(failed.flow.error, /ConnectTimeout/, "the last line of stderr is what the user needs to see");
});

test("a missing flow script is an error, not a crash", async () => {
  const scope = scratch();
  const link = makeLink(scope, { flowScript: path.join(scope.dir, "nope.py"), bridgeScript: writeBridgeStub(scope.dir, "bridge4.cjs") });
  const started = await call(link, "POST", "/api/wechat/link/start", { identity: { username: "harry" } });
  assert.equal(started.status, 503);
  assert.equal(started.body.code, "flow_unavailable");
  assert.match(started.body.error, /flow script missing/);
});

test("bridges for linked accounts come back after a restart", async () => {
  const scope = scratch();
  fs.mkdirSync(scope.dataDir, { recursive: true });
  fs.writeFileSync(
    path.join(scope.dataDir, "mother.json"),
    JSON.stringify({ label: "mother", bot_token: "tok-9", ilink_bot_id: "bot@9" })
  );
  const link = makeLink(scope, {
    flowScript: writeFlowStub(scope.dir, "unused.cjs", []),
    bridgeScript: writeBridgeStub(scope.dir, "bridge5.cjs"),
  });

  assert.equal(link.snapshot("mother").bridge.running, false);
  link.ensureBridges();
  assert.equal(link.snapshot("mother").bridge.running, true);
  // Idempotent: a second pass must not leave two processes polling one bot.
  const pid = link.snapshot("mother").bridge.pid;
  link.ensureBridges();
  assert.equal(link.snapshot("mother").bridge.pid, pid);
  link.stopBridge("mother");
  assert.equal(link.snapshot("mother").bridge.running, false);
});

test("endpoints are namespaced so no other route can be swallowed", async () => {
  const scope = scratch();
  const link = makeLink(scope, {
    flowScript: writeFlowStub(scope.dir, "unused2.cjs", []),
    bridgeScript: writeBridgeStub(scope.dir, "bridge6.cjs"),
  });
  assert.equal(link.handle(fakeRequest(), {}, "/api/sessions", "GET", {}, () => {}, null), false);
  const unknown = await call(link, "GET", "/api/wechat/nope", { identity: { username: "harry" } });
  assert.equal(unknown.status, 404);
});

test("a successful scan writes the channel binding for the account that started it", async () => {
  const scope = scratch();
  const flowScript = writeFlowStub(scope.dir, "flow-bind.cjs", [
    { event: "qr", qrcode: "q-9", png_base64: FAKE_PNG, expires_in: 300 },
    { event: "confirmed", bot_token: "tok-9", ilink_bot_id: "bot@9", ilink_user_id: "wx-user-9", base_url: "https://ilink.example.com/" },
  ]);
  const bridgeScript = writeBridgeStub(scope.dir, "bridge.cjs");

  // Stands in for the auth store: records which conversation was handed to which
  // account, which is exactly the join the real store persists.
  const bound = [];
  const link = makeLink(scope, {
    flowScript,
    bridgeScript,
    bindChannel: (conversationId, userId) => bound.push({ conversationId, userId }),
  });

  await call(link, "POST", "/api/wechat/link/start", { identity: { username: "mother", userId: 3 } });
  await waitFor(() => {
    const snap = link.snapshot("mother");
    return snap.flow && snap.flow.stage === "confirmed" ? snap : null;
  });
  await waitFor(() => bound.length > 0);

  // Scanning is the proof: the person who scanned is the account whose QR it was.
  assert.deepEqual(bound, [{ conversationId: "wx-user-9", userId: 3 }]);
});

test("a binding hook that throws does not fail an otherwise successful scan", async () => {
  const scope = scratch();
  const flowScript = writeFlowStub(scope.dir, "flow-bindfail.cjs", [
    { event: "qr", qrcode: "q-10", png_base64: FAKE_PNG, expires_in: 300 },
    { event: "confirmed", bot_token: "tok-10", ilink_bot_id: "bot@10", ilink_user_id: "wx-user-10", base_url: "https://ilink.example.com/" },
  ]);
  const bridgeScript = writeBridgeStub(scope.dir, "bridge.cjs");
  const link = makeLink(scope, {
    flowScript,
    bridgeScript,
    bindChannel: () => {
      throw new Error("auth store unavailable");
    },
  });

  await call(link, "POST", "/api/wechat/link/start", { identity: { username: "harry", userId: 1 } });
  const confirmed = await waitFor(() => {
    const snap = link.snapshot("harry");
    return snap.flow && snap.flow.stage === "confirmed" ? snap : null;
  });
  // The credential is already on disk and the bridge is up; losing the binding is
  // recoverable, losing the scan is not.
  assert.equal(confirmed.linked, true);
  assert.equal(confirmed.bridge.running, true);
  assert.equal(fs.existsSync(link.accountPath("harry")), true);
  assert.match(confirmed.flow.bindingError || "", /通道绑定写入失败/);
});
