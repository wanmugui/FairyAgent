import path from "node:path";
import fs from "node:fs";
import readline from "node:readline";
import { createRequire } from "node:module";

const repoRoot = path.resolve(process.env.FAIRY_REPO_ROOT || process.cwd());
const browserPath = String(process.env.FAIRY_BROWSER_PATH || "").trim();
const requireFromRepo = createRequire(path.join(repoRoot, "package.json"));
const { chromium } = requireFromRepo("playwright-core");

const state = {
  browser: null,
  context: null,
  page: null,
  pages: new Map(),
  closed: false,
};

const MAX_REQUESTS = 1000;
const MAX_CONSOLE = 500;
const MAX_SSE = 500;
const MAX_TEXT = 16000;
const LIVE_VIEW_ACTIONS = new Set([
  "open",
  "goto",
  "click",
  "fill",
  "type",
  "press",
  "hover",
  "check",
  "uncheck",
  "select_option",
  "reload",
]);

function limitNumber(value, fallback, min, max) {
  const n = Number(value);
  if (!Number.isFinite(n)) return fallback;
  return Math.max(min, Math.min(max, Math.floor(n)));
}

function preview(value, max = 600) {
  const text = typeof value === "string" ? value : JSON.stringify(value);
  if (!text) return "";
  return text.length <= max ? text : text.slice(0, max) + "...[truncated]";
}

function pageState(page) {
  let item = state.pages.get(page);
  if (!item) {
    item = {
      page,
      requests: [],
      requestMap: new Map(),
      console: [],
    };
    state.pages.set(page, item);
  }
  return item;
}

function trimArray(items, max) {
  if (items.length > max) items.splice(0, items.length - max);
}

// --- 远端实时画面 -------------------------------------------------------
// 背景:artifact 里的 page_url 让前端渲染 <iframe src=page_url>,那是"让
// 观者自己的浏览器重新打开同一个地址",不是本机 Playwright 的像素。
// 观者不在本机时 127.0.0.1 指向观者自己的设备,iframe 必然连不上。
//
// 这里改用 CDP Page.startScreencast 抓真实像素,写到磁盘目录,由
// frontend/server.cjs 读文件推 MJPEG 给观者。
//
// 为什么走文件而不是 stdout:stdout 是严格的一问一答 JSON 行协议,
// Go 侧用 bufio.NewReaderSize(stdout, 64*1024) 逐行配对请求与响应。
// 异步插一帧进去会错位配对,而且一帧 base64 就 ~20KB,逼近缓冲上限。
const cast = {
  dir: process.env.FAIRY_BROWSER_FRAME_DIR || "/tmp/fairy-browser-frames",
  session: null,      // CDP session
  page: null,         // 当前抓帧的 page
  latest: "",         // 最新一帧文件名
  seq: 0,
  started: false,
  lastError: "",
};

// 只保留最新两帧,供 server.cjs 在读旧帧时不被写覆盖打断。
function castPath(name) {
  return cast.dir + "/" + name;
}

function pruneFrames(keep) {
  for (const name of keep) {
    try { fs.unlinkSync(castPath(name)); } catch {}
  }
}

function nextFrameName() {
  cast.seq += 1;
  return "f" + String(cast.seq).padStart(6, "0") + ".jpg";
}

// 启动时清掉上一轮会话遗留的帧。
// cast.seq 每个会话都从 1 重新计数，而上一轮会话的帧从没人删过，
// 于是「编号大」等于「旧」：f007204 排在 f000171 之后，却早 9 小时抓的。
// 消费端若按编号取最新，拿到的永远是过期画面（2026-10-03 实测：远端观者
// 看到的是 9 小时半前的 Agent Workbench 画面，而不是当时的页面）。
// 只删本次启动之前就已存在的文件，不动其它会话正在写的帧。
const BOOT_TS = Date.now();
function pruneStaleFrames() {
  try {
    for (const f of fs.readdirSync(cast.dir)) {
      if (!/^f\d+\.jpg(\.tmp)?$/.test(f)) continue;
      const p = path.join(cast.dir, f);
      try {
        if (fs.statSync(p).mtimeMs < BOOT_TS) fs.unlinkSync(p);
      } catch {}
    }
  } catch {}
}
try { fs.mkdirSync(cast.dir, { recursive: true }); } catch {}
pruneStaleFrames();

// 原子写:先写 tmp 再 rename,避免 server.cjs 读到写了一半的半张图。
function writeFrame(jpegBase64) {
  try {
    fs.mkdirSync(cast.dir, { recursive: true });
    const name = nextFrameName();
    const tmp = castPath(name + ".tmp");
    fs.writeFileSync(tmp, Buffer.from(jpegBase64, "base64"));
    fs.renameSync(tmp, castPath(name));
    const previous = cast.latest;
    cast.latest = name;
    if (previous) {
      // 上一帧可能正被读取,延迟一点再删。
      const old = previous;
      setTimeout(() => { try { fs.unlinkSync(castPath(old)); } catch {} }, 1000);
    }
  } catch (err) {
    cast.lastError = String(err && err.message ? err.message : err).slice(0, 200);
  }
}

async function stopScreencast() {
  const session = cast.session;
  cast.session = null;
  cast.page = null;
  cast.started = false;
  cast.latest = "";
  if (session) {
    try { await session.send("Page.stopScreencast"); } catch {}
    try { await session.detach(); } catch {}
  }
}

async function startScreencast(page) {
  if (cast.page === page && cast.started) return;
  await stopScreencast();
  if (!page) return;

  let session;
  try {
    session = await page.context().newCDPSession(page);
    // everyNthFrame:2 是刻意压帧率。头一版 everyNthFrame:1 实测 5 秒只有
    // 1 帧,不是抓不到,是 CDP 只在页面"变化"时推帧;真实使用中画面一直在动,
    // 但仍留 2 避免观者端频繁解码拖慢 MJPEG 推送。
    await session.send("Page.startScreencast", {
      format: "jpeg",
      quality: 55,
      maxWidth: 1280,
      maxHeight: 800,
      everyNthFrame: 2,
    });
  } catch (err) {
    cast.lastError = String(err && err.message ? err.message : err).slice(0, 200);
    try { if (session) await session.detach(); } catch {}
    return;
  }

  cast.session = session;
  cast.page = page;
  cast.started = true;
  cast.lastError = "";

  session.on("Page.screencastFrame", (event) => {
    writeFrame(event.data);
    // 不 ack 的话 CDP 会在缓冲区堆积后停止推送,必须逐帧确认。
    session.send("Page.screencastFrameAck", { sessionId: event.sessionId }).catch(() => {});
  });
  session.on("Page.screencastFrameError", (event) => {
    cast.lastError = String(event.message || "screencastFrameError").slice(0, 200);
  });
  // 注意:这里绝不能 detach。CDP session 一断开,screencast 和它的监听会一起
  // 消失,抓帧会静默停摆。本轮的 session 留给 stopScreencast 释放。
}

function castStatus() {
  return {
    started: cast.started,
    latest: cast.latest,
    dir: cast.dir,
    seq: cast.seq,
    last_error: cast.lastError,
  };
}

function attachPage(page) {
  const item = pageState(page);
  page.on("request", request => {
    const id = request.url() + "#" + Date.now() + "#" + Math.random().toString(36).slice(2, 8);
    const record = {
      id,
      method: request.method(),
      url: request.url(),
      resource_type: request.resourceType(),
      started_at: Date.now(),
      status: null,
      ok: null,
      duration_ms: null,
      failure: "",
    };
    item.requestMap.set(request, record);
    item.requests.push(record);
    trimArray(item.requests, MAX_REQUESTS);
  });
  page.on("response", response => {
    const record = item.requestMap.get(response.request());
    if (!record) return;
    record.status = response.status();
    record.ok = response.ok();
    record.status_text = response.statusText();
    record.ended_at = Date.now();
    record.duration_ms = Math.max(0, record.ended_at - record.started_at);
  });
  page.on("requestfinished", request => {
    const record = item.requestMap.get(request);
    if (!record) return;
    record.ended_at = Date.now();
    record.duration_ms = Math.max(0, record.ended_at - record.started_at);
  });
  page.on("requestfailed", request => {
    const record = item.requestMap.get(request);
    if (!record) return;
    record.failure = request.failure()?.errorText || "request failed";
    record.ended_at = Date.now();
    record.duration_ms = Math.max(0, record.ended_at - record.started_at);
  });
  page.on("console", message => {
    const record = {
      type: message.type(),
      text: message.text(),
      timestamp: Date.now(),
      location: message.location(),
    };
    item.console.push(record);
    trimArray(item.console, MAX_CONSOLE);
  });
  page.on("pageerror", error => {
    item.console.push({
      type: "pageerror",
      text: error?.stack || error?.message || String(error),
      timestamp: Date.now(),
      location: {},
    });
    trimArray(item.console, MAX_CONSOLE);
  });
  page.on("close", () => {
    if (state.page === page) state.page = null;
  });
  // 每次挂上新页面就切到它上面抓帧。刻意不 await:screencast 是旁路能力,
  // 抓帧失败绝不能拖慢甚至搞挂正常的浏览器动作,所以让它在后台自己跑。
  startScreencast(page).catch(() => {});
}


async function instrumentPage(page) {
  await page.addInitScript(() => {
    if (window.__fairyBrowserInstrumented) return;
    window.__fairyBrowserInstrumented = true;
    window.__fairyBrowserSSE = [];

    const push = record => {
      try {
        window.__fairyBrowserSSE.push(record);
        if (window.__fairyBrowserSSE.length > 500) {
          window.__fairyBrowserSSE.splice(0, window.__fairyBrowserSSE.length - 500);
        }
      } catch {}
    };

    const parseBlock = (url, block) => {
      const lines = String(block || "").split(/\r?\n/);
      let event = "message";
      const data = [];
      for (const line of lines) {
        if (line.startsWith("event:")) event = line.slice(6).trim();
        else if (line.startsWith("data:")) data.push(line.slice(5).trimStart());
      }
      if (!data.length) return;
      const raw = data.join("\n");
      let parsed = null;
      try { parsed = JSON.parse(raw); } catch {}
      push({ url, event, data: raw, parsed, timestamp: Date.now() });
    };

    const tapStream = async (response, url) => {
      try {
        const reader = response.body && response.body.getReader();
        if (!reader) return;
        const decoder = new TextDecoder();
        let buffer = "";
        while (true) {
          const { value, done } = await reader.read();
          if (done) break;
          buffer += decoder.decode(value, { stream: true });
          let index;
          while ((index = buffer.search(/\r?\n\r?\n/)) >= 0) {
            const block = buffer.slice(0, index);
            const match = buffer.slice(index).match(/^\r?\n\r?\n/);
            buffer = buffer.slice(index + (match ? match[0].length : 2));
            parseBlock(url, block);
          }
        }
        if (buffer.trim()) parseBlock(url, buffer);
      } catch {}
    };

    const originalFetch = window.fetch && window.fetch.bind(window);
    if (originalFetch) {
      window.fetch = async function(input, init) {
        const response = await originalFetch(input, init);
        const url = typeof input === "string" ? input : (input && input.url) || response.url || "";
        const type = String(response.headers.get("content-type") || "").toLowerCase();
        if (type.includes("text/event-stream") || String(url).includes("/events")) {
          try { tapStream(response.clone(), String(url)); } catch {}
        }
        return response;
      };
    }

    const OriginalEventSource = window.EventSource;
    if (OriginalEventSource) {
      window.EventSource = class FairyObservedEventSource extends OriginalEventSource {
        constructor(url, options) {
          super(url, options);
          this.addEventListener("message", event => {
            let parsed = null;
            try { parsed = JSON.parse(event.data); } catch {}
            push({ url: String(url), event: "message", data: event.data, parsed, timestamp: Date.now() });
          });
        }
      };
    }
  });
}

async function ensurePage() {
  if (state.page && !state.page.isClosed()) return state.page;
  if (!state.context) throw new Error("browser is not open");
  const page = await state.context.newPage();
  await instrumentPage(page);
  attachPage(page);
  state.page = page;
  return page;
}

async function ensureBrowser() {
  if (state.browser && state.context) return;
  if (!browserPath) throw new Error("FAIRY_BROWSER_PATH is required");
  state.browser = await chromium.launch({
    executablePath: browserPath,
    headless: true,
    args: ["--disable-dev-shm-usage", "--no-first-run", "--no-default-browser-check"],
  });
  state.context = await state.browser.newContext({
    viewport: { width: 1440, height: 900 },
    ignoreHTTPSErrors: true,
  });
  state.context.on("page", page => {
    instrumentPage(page).catch(() => {});
    attachPage(page);
  });
}

async function reset() {
  for (const item of state.pages.values()) {
    try { if (!item.page.isClosed()) await item.page.close(); } catch {}
  }
  state.pages.clear();
  state.page = null;
  if (state.context) try { await state.context.close(); } catch {}
  if (state.browser) try { await state.browser.close(); } catch {}
  state.context = null;
  state.browser = null;
}

function requireUrl(request) {
  const url = String(request.url || "").trim();
  if (!url) throw new Error("url is required");
  return url;
}

function requireSelector(request) {
  const selector = String(request.selector || "").trim();
  if (!selector) throw new Error("selector is required");
  return selector;
}

function locator(page, request) {
  const selector = requireSelector(request);
  const loc = page.locator(selector);
  return request.nth !== undefined ? loc.nth(Number(request.nth) || 0) : loc.first();
}

async function snapshot(page) {
  const stateValue = await page.evaluate(() => ({
    title: document.title || "",
    url: location.href,
    text: document.body ? document.body.innerText : "",
    forms: Array.from(document.querySelectorAll("input,textarea,select,button")).slice(0, 80).map(el => ({
      tag: el.tagName.toLowerCase(),
      type: el.getAttribute("type") || "",
      name: el.getAttribute("name") || "",
      id: el.id || "",
      text: String(el.innerText || el.value || "").slice(0, 120),
      disabled: !!el.disabled,
    })),
  }));
  stateValue.text = preview(stateValue.text, MAX_TEXT);
  return stateValue;
}

async function attachLiveView(request, response) {
  if (!response || response.ok === false || !LIVE_VIEW_ACTIONS.has(String(request.action || ""))) {
    return response;
  }
  const page = state.page && !state.page.isClosed() ? state.page : null;
  if (!page) return response;
  const url = page.url();
  if (!url || url === "about:blank") return response;
  const title = await page.title().catch(() => "");
  return {
    ...response,
    artifact: {
      kind: "browser-live",
      name: title || "浏览器页面",
      live: true,
      page_url: url,
      // 远端观者真正要看的入口:本机 Playwright 的真实像素,由
      // frontend/server.cjs 读 FAIRY_BROWSER_FRAME_DIR 推 MJPEG。
      // page_url 保留,本机观者仍可用 iframe;两者并存由前端按需选择。
      live_url: "/api/browser-frame/stream.mjpg",
      cast: castStatus(),
      version: Date.now(),
    },
  };
}

async function collectSse() {
  const all = [];
  for (const item of state.pages.values()) {
    if (item.page.isClosed()) continue;
    try {
      const rows = await item.page.evaluate(() => window.__fairyBrowserSSE || []);
      for (const row of rows || []) all.push({ ...row, page_url: item.page.url() });
    } catch {}
  }
  all.sort((a, b) => Number(a.timestamp || 0) - Number(b.timestamp || 0));
  return all.slice(-MAX_SSE);
}

function sseMatches(row, request) {
  const urlContains = String(request.url_contains || request.url || "").trim();
  if (urlContains && !String(row.url || "").includes(urlContains)) return false;
  const eventType = String(request.event_type || "").trim();
  if (eventType) {
    const parsedType = row.parsed && (row.parsed.type || row.parsed.event || row.parsed.name);
    if (String(row.event || "") !== eventType && String(parsedType || "") !== eventType) return false;
  }
  return true;
}

function fieldValue(object, field) {
  const parts = String(field || "").split(".").filter(Boolean);
  let value = object;
  for (const part of parts) {
    if (value == null) return undefined;
    value = value[part];
  }
  return value;
}

async function waitForSse(request, requireMatch = true, minCount = 1) {
  const timeout = limitNumber(request.timeout_ms, 30000, 100, 300000);
  const deadline = Date.now() + timeout;
  let rows = [];
  while (Date.now() <= deadline) {
    rows = (await collectSse()).filter(row => sseMatches(row, request));
    if (rows.length >= minCount || !requireMatch) return rows;
    await new Promise(resolve => setTimeout(resolve, 150));
  }
  if (requireMatch) throw new Error("timed out waiting for matching SSE event");
  return rows;
}

async function assertSse(request) {
  const minCount = limitNumber(request.min_count, 1, 1, 10000);
  const rows = await waitForSse(request, true, minCount);
  if (rows.length < minCount) {
    throw new Error(`expected at least ${minCount} matching SSE events, got ${rows.length}`);
  }
  const field = String(request.field || "").trim();
  const values = field ? rows.map(row => fieldValue(row.parsed, field)) : [];
  if (request.min !== undefined) {
    const min = Number(request.min);
    for (const value of values) {
      const numeric = Number(value);
      if (!Number.isFinite(numeric) || numeric < min) {
        throw new Error(`SSE field ${field} expected >= ${min}, got ${JSON.stringify(value)}`);
      }
    }
  }
  if (request.monotonic) {
    for (let i = 1; i < values.length; i++) {
      const previous = Number(values[i - 1]);
      const current = Number(values[i]);
      if (!Number.isFinite(previous) || !Number.isFinite(current) || current < previous) {
        throw new Error(`SSE field ${field} is not monotonic: ${JSON.stringify(values)}`);
      }
    }
  }
  return { matched: rows.length, values };
}

async function handle(request) {
  const action = String(request.action || "").trim();
  if (action === "close") {
    await reset();
    state.closed = true;
    return { ok: true, action, closed: true };
  }
  if (action === "reset") {
    await reset();
    state.closed = false;
    return { ok: true, action, reset: true };
  }

  await ensureBrowser();
  const page = await ensurePage();
  const canWait = new Set(["click", "fill", "type", "press", "hover", "check", "uncheck", "select_option", "expect_text"]);
  const timeout = limitNumber(request.timeout_ms, 15000, 100, 300000);
  if (canWait.has(action)) page.setDefaultTimeout(timeout);

  switch (action) {
    case "open": {
      const url = requireUrl(request);
      const tabs = state.context.pages().filter(item => !item.isClosed());
      const target = tabs.length === 1 && tabs[0].url() === "about:blank" ? tabs[0] : await state.context.newPage();
      if (target !== state.page) {
        await instrumentPage(target);
        attachPage(target);
        state.page = target;
      }
      await target.goto(url, { waitUntil: request.wait_until || "domcontentloaded", timeout });
      return { ok: true, action, url: target.url(), title: await target.title(), page: await snapshot(target) };
    }
    case "goto": {
      const url = requireUrl(request);
      await page.goto(url, { waitUntil: request.wait_until || "domcontentloaded", timeout });
      return { ok: true, action, url: page.url(), title: await page.title() };
    }
    case "snapshot":
      return { ok: true, action, page: await snapshot(page) };
    case "click":
      await locator(page, request).click({ timeout });
      return { ok: true, action, url: page.url(), title: await page.title() };
    case "fill":
      await locator(page, request).fill(String(request.text ?? request.value ?? ""), { timeout });
      return { ok: true, action, selector: request.selector };
    case "type":
      await locator(page, request).pressSequentially(String(request.text ?? request.value ?? ""), { delay: limitNumber(request.delay_ms, 0, 0, 5000), timeout });
      return { ok: true, action, selector: request.selector };
    case "press":
      await locator(page, request).press(String(request.key || "Enter"), { timeout });
      return { ok: true, action, key: request.key || "Enter" };
    case "hover":
      await locator(page, request).hover({ timeout });
      return { ok: true, action, selector: request.selector };
    case "check":
      await locator(page, request).check({ timeout });
      return { ok: true, action, selector: request.selector };
    case "uncheck":
      await locator(page, request).uncheck({ timeout });
      return { ok: true, action, selector: request.selector };
    case "select_option": {
      const value = request.value ?? request.text ?? "";
      await locator(page, request).selectOption(value, { timeout });
      return { ok: true, action, selector: request.selector, value };
    }
    case "reload":
      await page.reload({ waitUntil: request.wait_until || "domcontentloaded", timeout });
      return { ok: true, action, url: page.url(), title: await page.title() };
    case "eval": {
      const expression = String(request.expression || request.value || "").trim();
      if (!expression) throw new Error("expression is required");
      const value = await page.evaluate(expression);
      return { ok: true, action, value: preview(value, MAX_TEXT) };
    }
    case "wait": {
      if (request.selector) await locator(page, request).waitFor({ state: request.state || "visible", timeout });
      else await page.waitForTimeout(limitNumber(request.timeout_ms, timeout, 0, 300000));
      return { ok: true, action };
    }
    case "expect_text": {
      const text = String(request.text ?? request.value ?? "");
      const loc = request.selector ? locator(page, request) : page.locator("body");
      await loc.waitFor({ state: "attached", timeout });
      const actual = await loc.innerText({ timeout });
      if (!actual.includes(text)) throw new Error(`expected text not found: ${preview(text, 200)}`);
      return { ok: true, action, matched: true };
    }
    case "screenshot": {
      const output = String(request.output_path || "").trim();
      if (!output) throw new Error("output_path is required");
      fs.mkdirSync(path.dirname(output), { recursive: true });
      await page.screenshot({ path: output, fullPage: request.full_page !== false, timeout });
      return {
        ok: true,
        action,
        path: output,
        artifact: { kind: "image", path: output, name: path.basename(output) },
      };
    }
    case "requests": {
      const limit = limitNumber(request.limit, 100, 1, 500);
      const urlContains = String(request.url || request.url_contains || "").trim();
      const rows = [];
      for (const item of state.pages.values()) {
        for (const record of item.requests) {
          if (urlContains && !record.url.includes(urlContains)) continue;
          rows.push(record);
        }
      }
      rows.sort((a, b) => a.started_at - b.started_at);
      return { ok: true, action, requests: rows.slice(-limit) };
    }
    case "console": {
      const limit = limitNumber(request.limit, 100, 1, 500);
      const rows = [];
      for (const item of state.pages.values()) rows.push(...item.console);
      rows.sort((a, b) => a.timestamp - b.timestamp);
      return { ok: true, action, console: rows.slice(-limit) };
    }
    case "sse": {
      const limit = limitNumber(request.limit, 100, 1, 500);
      const rows = (await collectSse()).filter(row => sseMatches(row, request));
      return { ok: true, action, sse: rows.slice(-limit) };
    }
    case "wait_sse": {
      const rows = await waitForSse(request, true);
      return { ok: true, action, matched: rows.length, sse: rows.slice(-limitNumber(request.limit, 10, 1, 100)) };
    }
    case "assert_sse": {
      const result = await assertSse(request);
      return { ok: true, action, ...result };
    }
    case "pages": {
      const rows = [];
      for (const item of state.pages.values()) {
        if (item.page.isClosed()) continue;
        rows.push({ url: item.page.url(), title: await item.page.title().catch(() => ""), active: item.page === state.page });
      }
      return { ok: true, action, pages: rows };
    }
    default:
      throw new Error(`unsupported browser action: ${action}`);
  }
}

async function main() {
  const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
  for await (const line of rl) {
    const text = String(line || "").trim();
    if (!text) continue;
    let response;
    try {
      const request = JSON.parse(text);
      response = await handle(request);
      response = await attachLiveView(request, response);
    } catch (error) {
      response = { ok: false, error: error?.stack || error?.message || String(error) };
    }
    process.stdout.write(JSON.stringify(response) + "\n");
  }
  await reset().catch(() => {});
}

let shutdownPromise = null;
function shutdown() {
  if (!shutdownPromise) shutdownPromise = reset().catch(() => {});
  return shutdownPromise;
}

process.once("SIGINT", () => {
  shutdown().finally(() => process.exit(0));
});
process.once("SIGTERM", () => {
  shutdown().finally(() => process.exit(0));
});

process.on("uncaughtException", error => {
  process.stderr.write((error?.stack || error?.message || String(error)) + "\n");
});
process.on("unhandledRejection", error => {
  process.stderr.write((error?.stack || error?.message || String(error)) + "\n");
});

main().catch(error => {
  process.stderr.write((error?.stack || error?.message || String(error)) + "\n");
  process.exitCode = 1;
});
