"use strict";

/**
 * Scheduled tasks for Fairy.
 *
 * A task is a small record - when to run, what to run, who owns it - kept in one
 * JSON file next to the session store. The API server owns the timer, because it
 * is the only process that is always up: the agent processes are per-turn, and a
 * schedule that dies with its turn is not a schedule.
 *
 * Two things are worth knowing before reading on:
 *
 *   - Time is the machine's wall clock in Asia/Shanghai, the same zone the day
 *     sessions use. "08:00 daily" means 08:00 for the person reading the panel,
 *     not 08:00 UTC.
 *   - This module never decides who a task belongs to. The caller passes the
 *     owner, and the HTTP layer decides it from the logged-in account, the same
 *     way every other scoped resource in this repo does.
 */

const fs = require("node:fs");
const path = require("node:path");
const { randomUUID } = require("node:crypto");
const { spawn } = require("node:child_process");

const TZ = "Asia/Shanghai";
const MIN_EVERY_MINUTES = 5;
const DEFAULT_POLL_MS = 20_000;
const PROMPT_TIMEOUT_MS = 30 * 60 * 1000;

// ---------------------------------------------------------------------------
// Time
// ---------------------------------------------------------------------------

/** Wall-clock parts of an instant in the scheduling zone. */
function zonedParts(date, timeZone = TZ) {
  const fmt = new Intl.DateTimeFormat("en-US", {
    timeZone,
    hour12: false,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
  const parts = {};
  for (const item of fmt.formatToParts(date)) parts[item.type] = item.value;
  return {
    year: Number(parts.year),
    month: Number(parts.month),
    day: Number(parts.day),
    // Some ICU builds report midnight as hour 24.
    hour: Number(parts.hour === "24" ? "0" : parts.hour),
    minute: Number(parts.minute),
    second: Number(parts.second),
  };
}

/** Offset of the scheduling zone at that instant, in milliseconds. */
function zoneOffsetMs(date, timeZone = TZ) {
  const p = zonedParts(date, timeZone);
  const asUtc = Date.UTC(p.year, p.month - 1, p.day, p.hour, p.minute, p.second);
  return asUtc - Math.floor(date.getTime() / 1000) * 1000;
}

/** The instant at which the zone's clock reads the given local date and time. */
function zonedTimeToInstant(year, month, day, hour, minute, timeZone = TZ) {
  const guess = Date.UTC(year, month - 1, day, hour, minute, 0);
  // The offset can only be known once we have a candidate instant, and the
  // candidate depends on the offset. One refinement is enough for a
  // fixed-offset zone, which is what this app pins itself to.
  const first = guess - zoneOffsetMs(new Date(guess), timeZone);
  return guess - zoneOffsetMs(new Date(first), timeZone);
}

/** "08:30" -> {hour, minute}; null when it is not a time of day. */
function parseTimeOfDay(value) {
  const match = /^([01]?\d|2[0-3]):([0-5]\d)$/.exec(String(value || "").trim());
  if (!match) return null;
  return { hour: Number(match[1]), minute: Number(match[2]) };
}

// ---------------------------------------------------------------------------
// Schedules
// ---------------------------------------------------------------------------

/**
 * Validate and normalize a schedule.
 *
 * Four shapes cover what people actually ask for in a chat window, and each one
 * can be explained back to them in a sentence:
 *
 *   {type:"once",   at:"2026-10-02T08:00:00+08:00"}
 *   {type:"daily",  at:"08:00"}
 *   {type:"weekly", weekday:5, at:"09:30"}     // 1 = Monday ... 7 = Sunday
 *   {type:"every",  minutes:30}
 */
function normalizeSchedule(raw) {
  const input = raw && typeof raw === "object" ? raw : {};
  const type = String(input.type || "").trim().toLowerCase();
  const clock = value => `${String(value.hour).padStart(2, "0")}:${String(value.minute).padStart(2, "0")}`;
  if (type === "once") {
    const at = Date.parse(String(input.at || ""));
    if (!Number.isFinite(at)) return { error: "一次性任务需要一个可解析的时间" };
    return { schedule: { type, at: new Date(at).toISOString() } };
  }
  if (type === "daily" || type === "weekly") {
    const time = parseTimeOfDay(input.at);
    if (!time) return { error: "时间要写成 24 小时的 HH:MM，例如 08:30" };
    if (type === "daily") return { schedule: { type, at: clock(time) } };
    const weekday = Number(input.weekday);
    if (!Number.isInteger(weekday) || weekday < 1 || weekday > 7) {
      return { error: "每周任务需要 weekday 1-7（1 是周一）" };
    }
    return { schedule: { type, weekday, at: clock(time) } };
  }
  if (type === "every") {
    const minutes = Number(input.minutes);
    if (!Number.isFinite(minutes) || minutes < MIN_EVERY_MINUTES) {
      return { error: `间隔任务至少 ${MIN_EVERY_MINUTES} 分钟一次` };
    }
    return { schedule: { type, minutes: Math.round(minutes) } };
  }
  return { error: "调度类型只能是 once / daily / weekly / every" };
}

/** JS getUTCDay() counts 0=Sunday; schedules count 1=Monday..7=Sunday. */
function isoWeekday(year, month, day) {
  const jsDay = new Date(Date.UTC(year, month - 1, day)).getUTCDay();
  return jsDay === 0 ? 7 : jsDay;
}

/**
 * The next instant a schedule should fire, strictly after `from`.
 *
 * `previous` is when it last fired; interval schedules count from there so a
 * task that takes ten minutes still fires every thirty rather than every forty.
 * A null return means there is no future occurrence (a one-shot in the past),
 * which the caller turns into "disable this task".
 */
function nextRunAt(schedule, from, previous) {
  const now = Number.isFinite(from) ? from : Date.now();
  if (!schedule || typeof schedule !== "object") return null;
  if (schedule.type === "once") {
    const at = Date.parse(schedule.at);
    return Number.isFinite(at) && at > now ? at : null;
  }
  if (schedule.type === "every") {
    const step = Math.max(MIN_EVERY_MINUTES, Math.round(Number(schedule.minutes) || 0)) * 60_000;
    const anchor = Number.isFinite(previous) ? previous : now;
    let next = anchor + step;
    while (next <= now) next += step;
    return next;
  }
  const time = parseTimeOfDay(schedule.at);
  if (!time) return null;
  const local = zonedParts(new Date(now));
  for (let addDays = 0; addDays <= 8; addDays++) {
    // Noon is a safe probe: no zone in use shifts the date around it.
    const day = zonedParts(new Date(zonedTimeToInstant(local.year, local.month, local.day + addDays, 12, 0)));
    if (schedule.type === "weekly" && isoWeekday(day.year, day.month, day.day) !== Number(schedule.weekday)) continue;
    const instant = zonedTimeToInstant(day.year, day.month, day.day, time.hour, time.minute);
    if (instant > now) return instant;
  }
  return null;
}

/** A sentence for the panel, so the list needs no per-type rendering. */
function describeSchedule(schedule) {
  if (!schedule || typeof schedule !== "object") return "未设置";
  if (schedule.type === "once") return `一次：${schedule.at}`;
  if (schedule.type === "daily") return `每天 ${schedule.at}`;
  if (schedule.type === "weekly") {
    const names = ["周一", "周二", "周三", "周四", "周五", "周六", "周日"];
    return `每${names[Number(schedule.weekday) - 1] || "周?"} ${schedule.at}`;
  }
  if (schedule.type === "every") return `每 ${schedule.minutes} 分钟`;
  return "未设置";
}

// ---------------------------------------------------------------------------
// Tasks
// ---------------------------------------------------------------------------

function normalizeAction(raw) {
  const input = raw && typeof raw === "object" ? raw : {};
  const kind = String(input.kind || "prompt").trim().toLowerCase();
  if (kind === "command") {
    const command = String(input.command || "").trim();
    if (!command) return { error: "命令任务需要一个要执行的命令" };
    return { action: { kind, command } };
  }
  if (kind !== "prompt") return { error: "任务类型只能是 prompt / command" };
  const text = String(input.text || "").trim();
  if (!text) return { error: "要交给 Fairy 做的事不能为空" };
  return {
    action: {
      kind,
      text,
      model: String(input.model || "").trim(),
      session: String(input.session || "").trim(),
    },
  };
}

/**
 * Where the result should be sent, if anywhere.
 *
 * Both bridges have a push module that speaks their platform's rules: QQ buys a
 * token from appId/appSecret, WeChat has to reuse the context_token of the
 * person's last message to the bot (the bridge writes that down; see
 * skills/wechat-bot/wechatbot/state.py). Neither can message a stranger, so a
 * failure here is reported instead of swallowed.
 */
function normalizeNotify(raw) {
  if (raw === null || raw === undefined || raw === "") return { notify: null };
  const input = typeof raw === "object" ? raw : {};
  const channel = String(input.channel || "").trim().toLowerCase();
  if (!channel) return { notify: null };
  const conversationId = String(input.conversation_id || "").trim();
  if (!PUSH_TARGETS[channel]) return { error: "推送目标只支持 qq / wechat" };
  if (!conversationId) return { error: "推送目标缺少会话 id" };
  return { notify: { channel, conversation_id: conversationId } };
}

/**
 * How each channel is pushed: which python module, from which directory, and
 * how its target is spelled. QQ targets carry a `c2c:` prefix; WeChat user ids
 * are used as they are.
 */
const PUSH_TARGETS = {
  qq: { cwd: ["skills", "qq-bot"], module: "qqbot.push", prefix: "c2c:" },
  wechat: { cwd: ["skills", "wechat-bot"], module: "wechatbot.push", prefix: "" },
};

function validateTask(input) {
  const errors = [];
  const title = String((input && input.title) || "").trim();
  if (!title) errors.push("任务需要一个标题");
  const schedule = normalizeSchedule(input && input.schedule);
  if (schedule.error) errors.push(schedule.error);
  const action = normalizeAction(input && input.action);
  if (action.error) errors.push(action.error);
  const notify = normalizeNotify(input && input.notify);
  if (notify.error) errors.push(notify.error);
  if (errors.length) return { error: errors.join("；") };
  return {
    value: {
      title: title.slice(0, 120),
      schedule: schedule.schedule,
      action: action.action,
      notify: notify.notify,
    },
  };
}

function emptyStore() {
  return { version: 1, tasks: [] };
}

function loadStore(file) {
  try {
    const parsed = JSON.parse(fs.readFileSync(file, "utf-8"));
    if (parsed && Array.isArray(parsed.tasks)) {
      return { version: 1, tasks: parsed.tasks.filter(task => task && typeof task === "object") };
    }
  } catch {}
  return emptyStore();
}

function saveStore(file, store) {
  fs.mkdirSync(path.dirname(file), { recursive: true });
  const temporary = `${file}.${process.pid}.tmp`;
  fs.writeFileSync(temporary, JSON.stringify(store, null, 2), "utf-8");
  fs.renameSync(temporary, file);
}

function sameOwner(a, b) {
  return Number(a || 0) === Number(b || 0);
}

/** Every task this owner can see. A null owner is the single-user install. */
function listTasks(file, owner) {
  return loadStore(file).tasks.filter(task => sameOwner(task.owner, owner));
}

function createTask(file, { owner, title, schedule, action, notify }, now = Date.now()) {
  const store = loadStore(file);
  const task = {
    id: `st_${randomUUID().replace(/-/g, "").slice(0, 12)}`,
    owner: owner ?? null,
    title,
    enabled: true,
    schedule,
    action,
    notify: notify || null,
    created_at: new Date(now).toISOString(),
    updated_at: new Date(now).toISOString(),
    next_run_at: nextRunAt(schedule, now, null),
    last_run_at: null,
    last_status: null,
    last_error: "",
    last_summary: "",
    run_count: 0,
  };
  store.tasks.push(task);
  saveStore(file, store);
  return task;
}

function updateTask(file, owner, id, patch) {
  const store = loadStore(file);
  const task = store.tasks.find(item => item.id === id && sameOwner(item.owner, owner));
  if (!task) return null;
  Object.assign(task, patch, { updated_at: new Date().toISOString() });
  if (Object.prototype.hasOwnProperty.call(patch, "schedule") || patch.enabled === true) {
    task.next_run_at = nextRunAt(task.schedule, Date.now(), task.last_run_at ? Date.parse(task.last_run_at) : null);
  }
  saveStore(file, store);
  return task;
}

function deleteTask(file, owner, id) {
  const store = loadStore(file);
  const before = store.tasks.length;
  store.tasks = store.tasks.filter(item => !(item.id === id && sameOwner(item.owner, owner)));
  const removed = store.tasks.length !== before;
  if (removed) saveStore(file, store);
  return removed;
}

/** Write back what a run produced. Kept separate so tests can drive it. */
function recordRun(file, id, { status, error = "", summary = "", countsAsRun = true }, now = Date.now()) {
  const store = loadStore(file);
  const task = store.tasks.find(item => item.id === id);
  if (!task) return null;
  task.last_run_at = new Date(now).toISOString();
  task.last_status = status;
  task.last_error = String(error || "").slice(0, 500);
  task.last_summary = String(summary || "").slice(0, 500);
  // A tick the guard short-circuited is not work the task performed, so it must
  // not inflate the run count and make an idle schedule look busy.
  if (countsAsRun) task.run_count = Number(task.run_count || 0) + 1;
  if (task.schedule && task.schedule.type === "once") {
    // A one-shot that has fired has nothing left to do. Leaving it enabled with
    // no next run is how a list fills up with tasks that can never fire again.
    task.enabled = false;
    task.next_run_at = null;
  } else {
    task.next_run_at = task.enabled ? nextRunAt(task.schedule, now, now) : null;
  }
  saveStore(file, store);
  return task;
}

/** Tasks whose time has come, oldest first, skipping ones already running. */
function dueTasks(file, now = Date.now(), running = new Set()) {
  return loadStore(file).tasks
    .filter(task => task.enabled && Number(task.next_run_at || 0) > 0 && Number(task.next_run_at) <= now)
    .filter(task => !running.has(task.id))
    .sort((a, b) => Number(a.next_run_at) - Number(b.next_run_at));
}

// ---------------------------------------------------------------------------
// Running
// ---------------------------------------------------------------------------

const MAX_CAPTURED_OUTPUT = 8000;

/** Last assistant text out of a `done` event, mirroring the QQ bridge. */
function extractFinalText(payload) {
  const messages = (((payload || {}).messages || {}).data || {}).messages || [];
  for (let i = messages.length - 1; i >= 0; i--) {
    const message = messages[i];
    if (!message || message.role !== "assistant") continue;
    const text = (message.contents || [])
      .filter(part => part && part.type === "text")
      .map(part => String(part.content || ""))
      .join("")
      .trim();
    if (text) return text;
  }
  return "";
}

// Channels take images and files through different flags, and the split is by
// extension, not by MIME - the push CLIs sniff nothing.
const IMAGE_EXTENSIONS = new Set([".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp"]);
const MAX_NOTIFY_ARTIFACTS = 4;
const MAX_NOTIFY_ARTIFACT_BYTES = 20 * 1024 * 1024;

/**
 * Files a turn actually handed to the user, read out of `show_result`.
 *
 * The done payload keeps tool results as JSON strings, so the only honest
 * source of "this run produced a file" is that result - not the text, which
 * routinely mentions paths the agent never opened.
 */
function extractArtifacts(payload) {
  const messages = (((payload || {}).messages || {}).data || {}).messages || [];
  const seen = new Set();
  const out = [];
  for (const message of messages) {
    for (const part of (message && message.contents) || []) {
      if (!part || part.type !== "tool_result" || part.name !== "show_result") continue;
      let parsed;
      try { parsed = JSON.parse(String(part.content || "")); } catch { continue; }
      if (!parsed || parsed.ok === false) continue;
      for (const key of ["path", "file"]) {
        const p = typeof parsed[key] === "string" ? parsed[key].trim() : "";
        if (!p || seen.has(p)) continue;
        seen.add(p);
        out.push(p);
      }
    }
  }
  return out;
}

/**
 * Turn artifact paths into push flags, dropping anything we cannot send.
 *
 * A missing or oversized file must not take the text notification down with it,
 * so every rejection is collected instead of thrown.
 */
function artifactArgs(artifacts) {
  const args = [];
  const skipped = [];
  for (const p of artifacts || []) {
    if (args.length >= MAX_NOTIFY_ARTIFACTS * 2) { skipped.push(p); continue; }
    let stat;
    try { stat = fs.statSync(p); } catch { skipped.push(p); continue; }
    if (!stat.isFile() || stat.size === 0 || stat.size > MAX_NOTIFY_ARTIFACT_BYTES) {
      skipped.push(p);
      continue;
    }
    args.push(IMAGE_EXTENSIONS.has(path.extname(p).toLowerCase()) ? "--image" : "--file", p);
  }
  return { args, skipped };
}

/** Read a `/api/chat` SSE response to its `done` event. Returns text plus artifacts. */
async function readDoneText(response) {
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  let finalText = "";
  const artifacts = [];
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    const lines = buffer.split("\n");
    buffer = lines.pop() || "";
    let stop = false;
    for (const line of lines) {
      if (!line.startsWith("data:")) continue;
      const payload = line.slice(5).trim();
      if (payload === "[DONE]") { stop = true; break; }
      let event;
      try { event = JSON.parse(payload); } catch { continue; }
      if (event && event.type === "error") throw new Error(String(event.error || "运行失败"));
      if (event && event.type === "done") {
        finalText = extractFinalText(event) || finalText;
        for (const p of extractArtifacts(event)) {
          if (!artifacts.includes(p)) artifacts.push(p);
        }
      }
    }
    if (stop) break;
  }
  return { text: finalText, artifacts };
}

/**
 * Run a prompt task as a normal turn.
 *
 * The scheduler talks to the API over loopback rather than reaching into the
 * request handler: that keeps the turn on exactly the path every other caller
 * uses (session scoping, plan gate, transcript, usage), instead of a second
 * implementation that drifts. `session_owner` is how a local caller says "do
 * this in that account's tree" - the same declaration the background
 * continuations make.
 */
async function runPromptAction(task, deps) {
  // 会话名支持 {today} 占位符，运行时解析成当天日期(Asia/Shanghai)。
  // 定时任务若写死某个历史日期，每次运行都会记在那天的会话下，侧边栏里
  // 永远看不到新的运行记录。
  const targetSession = String(task.action.session || "")
    .replace(/\{today\}/g, deps.defaultSessionName());

  // 带 "__" 的是分支会话。/api/chat 只接受纯日期的顶层会话，
  // 直接把 "2026-10-02__dev-backlog" 当顶层发会被拒：
  //   400 top_level_only_today
  // 分支必须先经 POST /api/sessions 以 parent_session 建立（已存在则复用），
  // 之后 /api/chat 才能往里写。
  if (targetSession.includes("__")) {
    const [parentSession] = targetSession.split("__", 2);
    try {
      // name 必须是完整的 "<父>__<分支>"。服务端不会替我们拼前缀：
      // 传裸分支名会建出 "dev-backlog"，再次触发时又被去重成 "dev-backlog-2"，
      // 于是每次运行都多出一个空壳会话。
      const res = await fetch(`${deps.apiBase}/api/sessions`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: targetSession, parent_session: parentSession }),
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}: ${(await res.text()).slice(0, 200)}`);
      console.log(`[scheduler] branch session ready: ${targetSession}`);
    } catch (e) {
      // 这里不自行记录：外层 runTaskNow 会把异常写进 task.last_error。
      console.error(`[scheduler] failed to create branch ${targetSession}: ${e && e.message ? e.message : e}`);
      throw e;
    }
  }

  const body = {
    message: task.action.text,
    session: targetSession,
    model: task.action.model || deps.defaultModel() || "",
    stream: true,
  };
  if (task.owner) body.session_owner = task.owner;
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), PROMPT_TIMEOUT_MS);
  try {
    const response = await fetch(`${deps.apiBase}/api/chat`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
      signal: controller.signal,
    });
    if (!response.ok) {
      const detail = (await response.text().catch(() => "")).slice(0, 200);
      throw new Error(`HTTP ${response.status}: ${detail}`);
    }
    return await readDoneText(response);
  } finally {
    clearTimeout(timer);
  }
}

/** Run a shell task. Output is captured, tail-first, because that is the part
 *  that explains a failure. */
function runCommandAction(task, deps) {
  return new Promise((resolve, reject) => {
    const child = spawn("/bin/bash", ["-lc", task.action.command], { cwd: deps.repoRoot, env: process.env });
    let out = "";
    let err = "";
    child.stdout.on("data", chunk => { out = (out + chunk).slice(-MAX_CAPTURED_OUTPUT); });
    child.stderr.on("data", chunk => { err = (err + chunk).slice(-MAX_CAPTURED_OUTPUT); });
    child.on("error", reject);
    child.on("close", code => {
      const text = [out.trim(), err.trim()].filter(Boolean).join("\n");
      if (code === 0) { resolve({ text }); return; }
      reject(new Error(`退出码 ${code}${text ? "：" + text.slice(-300) : ""}`));
    });
  });
}

/**
 * Push a finished task's answer back to the conversation that asked for it.
 *
 * Both channels go through their bridge's push module rather than through this
 * process: the modules own the platform quirks (QQ's token exchange and de-dup
 * rules, WeChat's context_token), and the bridges are the ones that keep those
 * credentials fresh.
 */
function notifyChannel(task, text, artifacts, deps) {
  const target = PUSH_TARGETS[task.notify.channel];
  if (!target) return Promise.reject(new Error(`不支持的推送通道：${task.notify.channel}`));
  const { args: mediaArgs, skipped } = artifactArgs(artifacts);
  for (const p of skipped) deps.log?.(`[scheduled] 跳过无法发送的产物：${p}`);
  if (mediaArgs.length) deps.log?.(`[scheduled] 随通知附带 ${mediaArgs.length / 2} 个产物`);
  return new Promise((resolve, reject) => {
    const child = spawn(deps.pythonBin, [
      "-m", target.module,
      "--to", `${target.prefix}${task.notify.conversation_id}`,
      "--text", text.slice(0, 2000),
      ...mediaArgs,
    ], { cwd: path.join(deps.repoRoot, ...target.cwd), env: process.env });
    let err = "";
    let out = "";
    child.stdout.on("data", chunk => { out = (out + chunk).slice(-1000); });
    child.stderr.on("data", chunk => { err = (err + chunk).slice(-1000); });
    child.on("error", reject);
    child.on("close", code => {
      if (code === 0) { resolve(); return; }
      const detail = (err || out).trim().replace(/\s+/g, " ").slice(0, 240);
      reject(new Error(`${target.module} 退出码 ${code}${detail ? "：" + detail : ""}`));
    });
  });
}

function notificationText(task, body) {
  const text = String(body || "").trim();
  return `【${task.title}】\n${text || "（本次没有输出）"}`;
}

async function runTask(task, deps) {
  const startedAt = Date.now();
  deps.log?.(`[scheduled] 开始 ${task.id} ${task.title}`);
  try {
    const outcome = task.action.kind === "command"
      ? await runCommandAction(task, deps)
      : await runPromptAction(task, deps);
    if (task.notify && task.notify.channel) {
      await notifyChannel(task, notificationText(task, outcome.text), outcome.artifacts, deps);
    }
    const summary = String(outcome.text || "").trim().slice(0, 500);
    recordRun(deps.storeFile, task.id, { status: "ok", summary });
    deps.log?.(`[scheduled] 完成 ${task.id} ${task.title}（${Date.now() - startedAt}ms）`);
    return { ok: true, summary };
  } catch (error) {
    const message = String((error && error.message) || error);
    // 到点时那条会话还有回合在跑（比如上一轮开发还没结束）：这不是任务失败，
    // 是它来得太早。安静跳过、等下一次，别在列表里留一条假的红色失败。
    if (/HTTP 409|仍在处理当前会话|no active chat/i.test(message)) {
      recordRun(deps.storeFile, task.id, { status: "skipped", summary: "会话忙，本轮跳过" });
      deps.log?.(`[scheduled] 跳过 ${task.id} ${task.title}：会话忙`);
      return { ok: true, skipped: true, summary: "会话忙，本轮跳过" };
    }
    recordRun(deps.storeFile, task.id, { status: "error", error: message });
    deps.log?.(`[scheduled] 失败 ${task.id} ${task.title}: ${message}`);
    return { ok: false, error: message };
  }
}

async function runTaskNow(file, owner, id, deps) {
  const task = loadStore(file).tasks.find(item => item.id === id && sameOwner(item.owner, owner));
  if (!task) return { ok: false, error: "任务不存在" };
  return runTask(task, deps);
}

/**
 * A task may carry `action.guard`: a shell command that decides whether a due
 * tick is worth waking the agent for. Exit 0 runs the task, anything else skips
 * it. Without this, a schedule pointed at an empty queue still pays for a full
 * agent turn every interval - the agent reads the queue, finds nothing, and
 * reports that it found nothing, over and over.
 *
 * Only the schedule consults the guard. runTaskNow deliberately does not, so a
 * manual "run now" still forces the task through.
 */
function guardTask(task, deps) {
  const cmd = task && task.action ? task.action.guard : null;
  if (typeof cmd !== "string" || !cmd.trim()) return Promise.resolve({ ok: true });
  return new Promise(resolve => {
    const child = spawn("/bin/bash", ["-lc", cmd], {
      cwd: deps.repoRoot,
      env: process.env,
    });
    let out = "";
    let settled = false;
    const done = value => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      resolve(value);
    };
    const timer = setTimeout(() => {
      child.kill("SIGKILL");
      done({ ok: false, reason: "guard 超时未返回" });
    }, 15000);
    child.stdout?.on("data", chunk => { out += chunk; });
    child.stderr?.on("data", chunk => { out += chunk; });
    child.on("error", err => done({ ok: false, reason: `guard 无法执行: ${err.message}` }));
    child.on("close", code => {
      if (code === 0) return done({ ok: true });
      const detail = out.trim().split("\n").filter(Boolean).slice(-2).join(" / ");
      done({ ok: false, reason: `guard 未通过（退出码 ${code}）${detail ? `：${detail}` : ""}` });
    });
  });
}

/**
 * The timer. One tick may start several tasks; a task that is already running is
 * skipped rather than queued, because "run it again while it is still running"
 * is never what a schedule means.
 */
function startScheduler(deps) {
  const running = new Set();
  const tick = async () => {
    const due = dueTasks(deps.storeFile, Date.now(), running);
    for (const task of due) {
      running.add(task.id);
      guardTask(task, deps).then((verdict) => {
        if (!verdict.ok) {
          recordRun(deps.storeFile, task.id, {
            status: "skipped",
            error: "",
            summary: verdict.reason,
            countsAsRun: false,
          });
          return null;
        }
        return runTask(task, deps);
      }).catch(() => null).finally(() => running.delete(task.id));
    }
    return due.length;
  };
  const timer = setInterval(() => { tick().catch(() => {}); }, deps.pollMs || DEFAULT_POLL_MS);
  if (timer.unref) timer.unref();
  return { tick, stop: () => clearInterval(timer) };
}

/** The shape the settings panel consumes; internals stay server-side. */
function presentTask(task) {
  return {
    id: task.id,
    title: task.title,
    enabled: !!task.enabled,
    kind: task.action && task.action.kind,
    schedule: task.schedule,
    schedule_text: describeSchedule(task.schedule),
    prompt: task.action && task.action.kind === "prompt" ? task.action.text : "",
    command: task.action && task.action.kind === "command" ? task.action.command : "",
    notify: task.notify || null,
    next_run_at: task.next_run_at || null,
    next_run_text: task.next_run_at ? new Date(task.next_run_at).toISOString() : "",
    last_run_at: task.last_run_at || null,
    last_status: task.last_status || null,
    last_error: task.last_error || "",
    last_summary: task.last_summary || "",
    run_count: Number(task.run_count || 0),
  };
}

module.exports = {
  TZ,
  MIN_EVERY_MINUTES,
  DEFAULT_POLL_MS,
  PROMPT_TIMEOUT_MS,
  zonedParts,
  zoneOffsetMs,
  zonedTimeToInstant,
  parseTimeOfDay,
  normalizeSchedule,
  nextRunAt,
  describeSchedule,
  normalizeAction,
  normalizeNotify,
  validateTask,
  loadStore,
  saveStore,
  listTasks,
  createTask,
  updateTask,
  deleteTask,
  recordRun,
  dueTasks,
  guardTask,
  extractFinalText,
  extractArtifacts,
  artifactArgs,
  readDoneText,
  runTask,
  runTaskNow,
  startScheduler,
  presentTask,
};
