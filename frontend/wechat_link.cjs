"use strict";

/**
 * WeChat iLink link-up: what the "扫码接入" button in the settings panel talks to.
 *
 * The protocol itself lives in the skill's own Python client
 * (skills/wechat-bot/link.py drives wechatbot/ilink.py), because it is already
 * written, tested and speaking to a service that documents itself in Chinese and
 * changes without notice. Re-implementing those two calls here would mean two
 * copies of one protocol to keep in step.
 *
 * So this file owns what is *this server's* business:
 *
 *   who may link - only the signed-in account, and only under its own label, so
 *   a family member cannot attach someone else's WeChat to their account;
 *
 *   where a credential lands - one file per account, outside the repository,
 *   mode 0600, which is the file the bridge already reads;
 *
 *   what "接入" means when the scan succeeds - the credential is written, the
 *   scanning WeChat id is allowed to talk to this bot, and the bridge process
 *   that answers those messages is started;
 *
 *   how the panel sees all of that - one status endpoint answering "linked or
 *   not, whose, and is the bridge alive".
 *
 * Credentials never travel back through the browser. The QR image does, and
 * nothing else.
 */

const fs = require("fs");
const os = require("os");
const path = require("path");
const { spawn } = require("child_process");

const FLOW_TIMEOUT_SECONDS = 300;
const LINK_JOB_TTL_MS = 11 * 60 * 1000; // the flow script gives up at 5 minutes
const DEFAULT_BASE_URL = "https://ilinkai.weixin.qq.com";

function readJson(file) {
  try {
    return JSON.parse(fs.readFileSync(file, "utf-8"));
  } catch {
    return null;
  }
}

function writeJson(file, value) {
  fs.mkdirSync(path.dirname(file), { recursive: true });
  const tmp = `${file}.${process.pid}.tmp`;
  fs.writeFileSync(tmp, JSON.stringify(value, null, 2) + "\n", "utf-8");
  fs.renameSync(tmp, file);
}

/** A pid is only alive if a signal can be delivered; ESRCH means it is gone. */
function pidAlive(pid) {
  if (!Number.isInteger(pid) || pid <= 0) return false;
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    return error && error.code === "EPERM";
  }
}

function createWechatLink(options) {
  const config = Object.assign(
    {
      repoRoot: path.resolve(__dirname, ".."),
      pythonBin: process.env.AGENT_PYTHON_BIN || "python3",
      dataDir: process.env.FAIRY_WECHAT_DIR || path.join(os.homedir(), ".config", "fairy", "wechat"),
      logDir: os.homedir(),
      flowScript: "",
      bridgeScript: "",
      channelsPath: "",
      // Optional hook: (conversationId, userId) => void. Wired to the auth
      // store by the server. Injected rather than imported so the module keeps
      // no dependency on the database and stays testable on its own.
      bindChannel: null,
    },
    options || {}
  );
  const skillDir = path.join(config.repoRoot, "skills", "wechat-bot");
  const flowScript = config.flowScript || path.join(skillDir, "link.py");
  const bridgeScript = config.bridgeScript || path.join(skillDir, "bot.py");
  const channelsPath = config.channelsPath || path.join(config.repoRoot, "config", "channels.json");

  /** One scan attempt at a time per account. */
  const flows = new Map(); // label -> flow state

  function accountPath(label) {
    return path.join(config.dataDir, `${label}.json`);
  }

  function pidPath(label) {
    return path.join(config.dataDir, `${label}.pid`);
  }

  function logPath(label) {
    return path.join(config.logDir, `fairy-wechat-${label}.log`);
  }

  function bridgePid(label) {
    const raw = fs.existsSync(pidPath(label)) ? fs.readFileSync(pidPath(label), "utf-8").trim() : "";
    const pid = Number(raw);
    return pidAlive(pid) ? pid : 0;
  }

  function bridgeRunning(label) {
    return bridgePid(label) > 0;
  }

  /**
   * Start the bridge that answers this account's WeChat messages.
   *
   * Detached on purpose: it has to outlive the request that started it, and it
   * must not be killed when this server restarts underneath. The pid file is what
   * makes a second start idempotent instead of leaving two processes long-polling
   * the same bot.
   */
  function startBridge(label) {
    const running = bridgePid(label);
    if (running) return { running: true, pid: running, started: false };
    if (!fs.existsSync(bridgeScript)) {
      return { running: false, pid: 0, started: false, error: `bridge script missing: ${bridgeScript}` };
    }
    const fd = fs.openSync(logPath(label), "a");
    const child = spawn(config.pythonBin, [bridgeScript, "--account", label], {
      cwd: skillDir,
      detached: true,
      stdio: ["ignore", fd, fd],
      env: Object.assign({}, process.env, { PYTHONUNBUFFERED: "1" }),
    });
    child.unref();
    fs.writeFileSync(pidPath(label), String(child.pid), "utf-8");
    console.log(`[wechat] bridge for '${label}' started pid=${child.pid} log=${logPath(label)}`);
    return { running: true, pid: child.pid, started: true };
  }

  function stopBridge(label) {
    const pid = bridgePid(label);
    try { fs.rmSync(pidPath(label), { force: true }); } catch {}
    if (!pid) return false;
    try {
      process.kill(pid, "SIGTERM");
    } catch {}
    console.log(`[wechat] bridge for '${label}' stopped pid=${pid}`);
    return true;
  }

  function snapshot(label) {
    const account = readJson(accountPath(label));
    const flow = flows.get(label) || null;
    return {
      label,
      linked: !!(account && account.bot_token),
      account: account
        ? {
            botId: account.ilink_bot_id || "",
            wechatUserId: account.ilink_user_id || "",
            baseUrl: account.base_url || DEFAULT_BASE_URL,
            linkedAt: account.linked_at || 0,
          }
        : null,
      bridge: { running: bridgeRunning(label), pid: bridgePid(label) },
      logPath: logPath(label),
      flow: flow
        ? {
            id: flow.id,
            stage: flow.stage,
            error: flow.error || "",
            expiresAt: flow.expiresAt || 0,
            qr: flow.qr || "",
            wechatUserId: flow.wechatUserId || "",
            // A scan whose binding write failed still linked; the panel has to
            // be able to say so instead of showing a silently unbound account.
            bindingError: flow.bindingError || "",
          }
        : null,
    };
  }

  function finishFlow(label, patch) {
    const flow = flows.get(label);
    if (!flow) return;
    Object.assign(flow, patch);
    if (flow.child && !flow.child.killed && (patch.stage === "confirmed" || patch.stage === "error")) {
      try { flow.child.kill(); } catch {}
    }
    const timer = setTimeout(() => {
      const current = flows.get(label);
      if (current && current.id === flow.id && current.stage !== "waiting" && current.stage !== "scanned") {
        flows.delete(label);
      }
    }, LINK_JOB_TTL_MS);
    if (timer.unref) timer.unref();
  }

  /**
   * Run one scan attempt.
   *
   * The flow script emits JSON lines: a qr event (possibly several, as codes
   * expire), a confirmed event, or a failure. stderr is kept for the message,
   * because that is where Python leaves a traceback.
   */
  function startFlow(label, identity) {
    const existing = flows.get(label);
    if (existing && (existing.stage === "waiting" || existing.stage === "scanned")) return existing;
    if (!fs.existsSync(flowScript)) {
      const failed = { id: `${label}:${Date.now()}`, stage: "error", error: `flow script missing: ${flowScript}` };
      flows.set(label, failed);
      return failed;
    }
    const flow = {
      id: `${label}:${Date.now()}`,
      stage: "starting",
      qr: "",
      qrcode: "",
      error: "",
      stderr: "",
      startedAt: Date.now(),
      expiresAt: 0,
      child: null,
      // Remember who pressed "generate QR" so the confirmed event can bind the
      // scan to that account. The flow outlives the request that started it.
      scanUserId: identity && identity.userId != null ? Number(identity.userId) : 0,
    };
    flows.set(label, flow);

    const child = spawn(config.pythonBin, [flowScript, "--label", label, "--timeout", String(FLOW_TIMEOUT_SECONDS)], {
      cwd: skillDir,
      env: Object.assign({}, process.env, { PYTHONUNBUFFERED: "1" }),
      stdio: ["ignore", "pipe", "pipe"],
    });
    flow.child = child;

    let buffer = "";
    child.stdout.on("data", (chunk) => {
      buffer += chunk.toString("utf-8");
      const lines = buffer.split("\n");
      buffer = lines.pop() || "";
      for (const line of lines) {
        const text = line.trim();
        if (!text) continue;
        let event;
        try {
          event = JSON.parse(text);
        } catch {
          continue;
        }
        if (event.event === "qr") {
          flow.stage = "waiting";
          flow.qr = `data:image/png;base64,${event.png_base64 || ""}`;
          flow.qrcode = String(event.qrcode || "");
          flow.expiresAt = Date.now() + Number(event.expires_in || FLOW_TIMEOUT_SECONDS) * 1000;
        } else if (event.event === "expired") {
          flow.stage = "waiting";
          flow.qr = "";
        } else if (event.event === "scanned") {
          flow.stage = "scanned";
        } else if (event.event === "confirmed") {
          flow.wechatUserId = String(event.ilink_user_id || "");
          const account = {
            label,
            bot_token: String(event.bot_token || ""),
            ilink_bot_id: String(event.ilink_bot_id || ""),
            ilink_user_id: String(event.ilink_user_id || ""),
            base_url: String(event.base_url || DEFAULT_BASE_URL).replace(/\/+$/, ""),
            linked_at: Date.now(),
            // The bridge prefers the account's own list over the shared one, so
            // one person's bot does not answer another person's messages.
            allow_users: event.ilink_user_id ? [String(event.ilink_user_id)] : [],
          };
          fs.mkdirSync(config.dataDir, { recursive: true });
          fs.writeFileSync(accountPath(label), JSON.stringify(account, null, 2) + "\n", { encoding: "utf-8", mode: 0o600 });
          try { fs.chmodSync(accountPath(label), 0o600); } catch {}
          // Scanning the code is the proof of identity: only a signed-in
          // account can render the QR, and the phone that scans it is the
          // person who will speak through it. So the scan writes the channel
          // binding itself - there is nothing extra for the user to do, and no
          // second secret to leak out of band. The credential write above
          // already succeeded, so a failure here only means the account must
          // be bound again later; it must not fail the scan.
          const scanUserId = flow.scanUserId;
          if (typeof config.bindChannel === "function" && scanUserId > 0 && account.ilink_user_id) {
            try {
              config.bindChannel(account.ilink_user_id, scanUserId);
            } catch (error) {
              flow.bindingError = `微信已连接，但通道绑定写入失败：${error && (error.message || error)}`;
            }
          }
          let bridge = { running: false, pid: 0 };
          try {
            bridge = startBridge(label);
          } catch (error) {
            flow.error = `凭据已保存，但桥接启动失败：${error.message || error}`;
          }
          finishFlow(label, { stage: "confirmed", qr: "", bridge });
        } else if (event.event === "timeout") {
          finishFlow(label, { stage: "timeout", qr: "" });
        } else if (event.event === "error") {
          finishFlow(label, { stage: "error", qr: "", error: String(event.error || "未知错误") });
        }
      }
    });
    child.stderr.on("data", (chunk) => {
      flow.stderr += chunk.toString("utf-8");
      if (flow.stderr.length > 4000) flow.stderr = flow.stderr.slice(-4000);
    });
    child.on("error", (error) => {
      finishFlow(label, { stage: "error", qr: "", error: `无法启动扫码进程：${error.message || error}` });
    });
    child.on("close", (code) => {
      const current = flows.get(label);
      if (!current || current.id !== flow.id) return;
      if (current.stage === "waiting" || current.stage === "scanned" || current.stage === "starting") {
        const detail = current.stderr.trim().split("\n").slice(-2).join(" ").slice(0, 300);
        finishFlow(label, { stage: "error", qr: "", error: detail || `扫码进程退出（code ${code}）` });
      }
    });
    return flow;
  }

  function unlink(label) {
    stopBridge(label);
    flows.delete(label);
    try { fs.rmSync(accountPath(label), { force: true }); } catch {}
  }

  /**
   * Bring the bridges back after a restart.
   *
   * Every linked account keeps its own bot process and those processes are
   * detached, so a restart of this server does not drop a link. That also means
   * nothing else notices a machine that came back up with three credentials on
   * disk and no bridges running - which is the state this fixes.
   */
  function ensureBridges() {
    let labels = [];
    try {
      labels = fs.readdirSync(config.dataDir).filter((name) => name.endsWith(".json")).map((name) => name.slice(0, -5));
    } catch {
      return;
    }
    for (const label of labels) {
      const account = readJson(accountPath(label));
      if (!account || !account.bot_token) continue;
      if (bridgeRunning(label)) continue;
      try {
        startBridge(label);
      } catch (error) {
        console.error(`[wechat] could not restart bridge for '${label}':`, error.message || error);
      }
    }
  }

  function handle(req, res, pathname, method, url, sendJson, identity) {
    if (!pathname.startsWith("/api/wechat")) return false;
    // A signed-in account links its own WeChat. The desktop build has no
    // accounts, so it links the single owner - the name the existing credential
    // file already uses.
    const label = identity && identity.username ? String(identity.username) : "default";
    const readBody = () =>
      new Promise((resolve) => {
        let raw = "";
        req.on("data", (chunk) => { raw += chunk; });
        req.on("end", () => {
          try { resolve(raw ? JSON.parse(raw) : {}); } catch { resolve({}); }
        });
      });

    if (pathname === "/api/wechat/status" && method === "GET") {
      sendJson(snapshot(label));
      return true;
    }

    if (pathname === "/api/wechat/link/start" && method === "POST") {
      readBody().then(() => {
        const flow = startFlow(label, identity);
        if (flow.stage === "error") {
          sendJson({ ok: false, error: flow.error, code: "flow_unavailable" }, 503);
          return;
        }
        sendJson({ ok: true, link: { id: flow.id, stage: flow.stage, qr: flow.qr || "", expiresAt: flow.expiresAt || 0 } });
      });
      return true;
    }

    if (pathname === "/api/wechat/link/cancel" && method === "POST") {
      readBody().then(() => {
        const flow = flows.get(label);
        if (flow && flow.child && !flow.child.killed) {
          try { flow.child.kill(); } catch {}
        }
        flows.delete(label);
        sendJson({ ok: true });
      });
      return true;
    }

    if (pathname === "/api/wechat/account" && method === "DELETE") {
      unlink(label);
      sendJson({ ok: true });
      return true;
    }

    if (pathname === "/api/wechat/bridge" && method === "POST") {
      // The bridge starts by itself when a scan succeeds; this is here for the
      // case where it died (or the machine rebooted) and the panel wants it back
      // without asking anyone to scan again.
      try {
        const bridge = startBridge(label);
        sendJson({ ok: !bridge.error, bridge, error: bridge.error });
      } catch (error) {
        sendJson({ ok: false, error: String((error && error.message) || error) }, 500);
      }
      return true;
    }

    sendJson({ error: "unknown wechat endpoint", code: "not_found" }, 404);
    return true;
  }

  return {
    handle,
    snapshot,
    ensureBridges,
    startBridge,
    stopBridge,
    accountPath,
    paths: { flowScript, bridgeScript, channelsPath, dataDir: config.dataDir },
  };
}

module.exports = { createWechatLink };
