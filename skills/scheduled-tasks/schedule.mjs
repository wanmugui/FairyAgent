#!/usr/bin/env node
"use strict";

/**
 * 定时任务的命令行入口——给 agent 用的那只手。
 *
 * 设置页里能建的东西，这里都能建，因为两边走的是同一个接口（本机 API 的
 * /api/scheduled-tasks）。差别的只有一件事：agent 在自己那一轮里跑，得知道
 * "我不是机主，我是替某个账号办事"——所以这里从 AGENT_SESSION_FILE 的路径里
 * 认出账号（多账号的会话树是 memory/sessions/u<id>/...），并在请求里声明。
 *
 * 用法：
 *   node skills/scheduled-tasks/schedule.mjs add --title "点外卖" --in 2m \
 *        --prompt "提醒主人点外卖" [--notify-qq [openid]]
 *   node skills/scheduled-tasks/schedule.mjs list
 *   node skills/scheduled-tasks/schedule.mjs remove --id st_xxx
 *   node skills/scheduled-tasks/schedule.mjs run --id st_xxx
 *
 * 时间一律按机器的时区（Asia/Shanghai）解释。
 */

import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));

const API = String(process.env.FAIRY_API_BASE || "http://127.0.0.1:8081").replace(/\/$/, "");
const REPO = String(process.env.AGENT_REPO_ROOT || path.resolve(HERE, "..", ".."));

/** 本机直连时，服务端允许调用方声明替哪个账号办事。 */
function ownerFromEnvironment() {
  const explicit = Number(process.env.FAIRY_SESSION_OWNER || 0);
  if (Number.isInteger(explicit) && explicit > 0) return explicit;
  const match = /[\\/]sessions[\\/]u(\d+)[\\/]/.exec(String(process.env.AGENT_SESSION_FILE || ""));
  return match ? Number(match[1]) : null;
}

/** "2m" / "90s" / "1h" / "2分钟" -> 毫秒。 */
function parseDuration(value) {
  const match = /^(\d+(?:\.\d+)?)\s*(s|sec|秒|m|min|分|分钟|h|小时)?$/i.exec(String(value || "").trim());
  if (!match) return null;
  const amount = Number(match[1]);
  const unit = (match[2] || "m").toLowerCase();
  const scale = ["s", "sec", "秒"].includes(unit) ? 1000
    : ["h", "小时"].includes(unit) ? 3600_000
      : 60_000;
  return Math.round(amount * scale);
}

/** QQ 会话 id：没给就取配置里已放行的第一个私聊，那通常就是主人自己。 */
function defaultQqConversation() {
  try {
    const raw = JSON.parse(fs.readFileSync(path.join(REPO, "config", "channels.json"), "utf-8"));
    const allowed = (((raw || {}).qq || {}).allowC2C) || [];
    return String(allowed[0] || "").trim();
  } catch {
    return "";
  }
}

/** 微信用户 id：取已接入的 bot 账号里放行的那个人（扫码的就是他自己）。 */
function defaultWechatConversation() {
  try {
    const base = process.env.FAIRY_WECHAT_DIR
      || path.join(process.env.HOME || "", ".config", "fairy", "wechat");
    for (const file of fs.readdirSync(base).sort()) {
      if (!file.endsWith(".json") || file.endsWith(".state.json") || file.endsWith(".discarded")) continue;
      const account = JSON.parse(fs.readFileSync(path.join(base, file), "utf-8"));
      const user = String((account.allow_users || [])[0] || account.ilink_user_id || "").trim();
      if (user) return user;
    }
  } catch {}
  return "";
}

/** 这条会话自己属于哪条通道：渠道分支的会话文件里记着。 */
function sessionChannel() {
  try {
    const file = String(process.env.AGENT_SESSION_FILE || "");
    if (!file) return null;
    const data = JSON.parse(fs.readFileSync(file, "utf-8"));
    const channel = data && data.channel;
    if (channel && channel.name && channel.conversation_id) {
      return { channel: String(channel.name), conversation_id: String(channel.conversation_id) };
    }
  } catch {}
  return null;
}

/**
 * 不写推送目标时推去哪儿。
 *
 * 在微信里说的提醒就推回微信，在 QQ 里说的就推回 QQ——这条会话自己知道它是谁；
 * 从网页端说的没有通道可循，就用服务端给的"最近接上的那条通道"，而不是习惯性
 * 地推 QQ。
 */
async function defaultNotify() {
  const fromSession = sessionChannel();
  if (fromSession) return fromSession;
  try {
    const meta = (await call("/api/scheduled-tasks")).meta || {};
    const suggested = meta.suggested_notify;
    if (suggested && suggested.channel && suggested.conversation_id) {
      return { channel: String(suggested.channel), conversation_id: String(suggested.conversation_id) };
    }
  } catch {}
  return null;
}

async function resolveNotify(argv) {
  if (argv.includes("--no-notify")) return null;
  if (argv.includes("--notify-qq")) {
    const conversationId = String(flagValue(argv, "--notify-qq") || "").trim() || defaultQqConversation();
    if (!conversationId) throw new Error("--notify-qq 需要 openid，配置里也没有可用的私聊");
    return { channel: "qq", conversation_id: conversationId };
  }
  if (argv.includes("--notify-wechat")) {
    const conversationId = String(flagValue(argv, "--notify-wechat") || "").trim() || defaultWechatConversation();
    if (!conversationId) throw new Error("--notify-wechat 需要微信用户 id，账号文件里也没有");
    return { channel: "wechat", conversation_id: conversationId };
  }
  const automatic = await defaultNotify();
  if (!automatic) throw new Error("找不到默认推送目标，请显式给 --notify-qq / --notify-wechat，或加 --no-notify");
  return automatic;
}

function flagValue(argv, name) {
  const index = argv.indexOf(name);
  if (index < 0) return undefined;
  const next = argv[index + 1];
  return next && !next.startsWith("--") ? next : "";
}

async function call(pathname, method = "GET", body) {
  const response = await fetch(`${API}${pathname}`, {
    method,
    headers: body ? { "Content-Type": "application/json; charset=utf-8" } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok || payload.ok === false) {
    throw new Error(payload.error || `HTTP ${response.status}`);
  }
  return payload;
}

function ownerField() {
  const owner = ownerFromEnvironment();
  return owner ? { session_owner: owner } : {};
}

function usage() {
  return [
    "用法:",
    "  schedule.mjs add --title <标题> --prompt <到点做什么> [时间] [推送目标] [--session <会话名>]",
    "      时间（默认 --in 10m）: --in 2m | --daily 08:00 | --weekly 5 09:30 | --every 30 | --at <ISO时间>",
    "      推送（默认推到你最近用的那条通道）: --notify-qq [openid] | --notify-wechat [wxid] | --no-notify",
    "  schedule.mjs list",
    "  schedule.mjs remove --id <id>",
    "  schedule.mjs run --id <id>",
  ].join("\n");
}

async function add(argv) {
  const title = String(flagValue(argv, "--title") || "").trim();
  const prompt = String(flagValue(argv, "--prompt") || "").trim();
  if (!title || !prompt) throw new Error("add 需要 --title 和 --prompt\n" + usage());

  let schedule = null;
  if (argv.includes("--daily")) {
    schedule = { type: "daily", at: String(flagValue(argv, "--daily") || "").trim() };
  } else if (argv.includes("--weekly")) {
    const index = argv.indexOf("--weekly");
    schedule = { type: "weekly", weekday: Number(argv[index + 1]), at: String(argv[index + 2] || "").trim() };
  } else if (argv.includes("--every")) {
    schedule = { type: "every", minutes: Number(flagValue(argv, "--every") || 0) };
  } else if (argv.includes("--at")) {
    schedule = { type: "once", at: String(flagValue(argv, "--at") || "").trim() };
  } else {
    const duration = parseDuration(flagValue(argv, "--in") ?? "10m");
    if (duration === null) throw new Error("--in 要写成 2m / 90s / 1h 这样\n" + usage());
    schedule = { type: "once", at: new Date(Date.now() + duration).toISOString() };
  }

  const notify = await resolveNotify(argv);
  // 到点在**哪条会话**里跑：不写就是该账号当天的主会话。开发类任务适合指到
  // 自己那条会话（例如 dev-backlog），免得每隔一阵子往当天主对话里灌一轮。
  const session = String(flagValue(argv, "--session") || "").trim();

  const result = await call("/api/scheduled-tasks", "POST", {
    title,
    schedule,
    action: { kind: "prompt", text: prompt, session },
    notify,
    ...ownerField(),
  });
  const task = result.task || {};
  console.log(JSON.stringify(task, null, 2));
  const where = task.notify ? `（结果推送到 ${task.notify.channel === "qq" ? "QQ" : "微信"}）` : "（结果留在会话里）";
  console.log(`已建定时任务 ${task.id}：${task.schedule_text}${where}`);
}

async function list() {
  const result = await call("/api/scheduled-tasks");
  const tasks = result.tasks || [];
  console.log(JSON.stringify(tasks, null, 2));
  console.log(`共 ${tasks.length} 条定时任务（时区 ${(result.meta || {}).timezone || "Asia/Shanghai"}）`);
}

async function act(argv, verb, endpoint) {
  const id = String(flagValue(argv, "--id") || "").trim();
  if (!id) throw new Error(`${verb} 需要 --id\n` + usage());
  const result = await call(`/api/scheduled-tasks/${endpoint}`, "POST", { id, ...ownerField() });
  console.log(JSON.stringify(result, null, 2));
}

async function main(argv) {
  const verb = String(argv[0] || "").trim();
  if (verb === "add") return add(argv.slice(1));
  if (verb === "list") return list();
  if (verb === "remove" || verb === "delete") return act(argv.slice(1), "remove", "delete");
  if (verb === "run") return act(argv.slice(1), "run", "run");
  throw new Error(usage());
}

main(process.argv.slice(2)).catch(error => {
  console.error(String((error && error.message) || error));
  process.exitCode = 1;
});
