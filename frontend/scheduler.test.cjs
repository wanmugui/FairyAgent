"use strict";

/**
 * Scheduled tasks: the schedule arithmetic and the store rules the settings
 * panel and the timer both lean on.
 *
 * The arithmetic is the part worth pinning: "每天 08:00" has to mean 08:00 in
 * Asia/Shanghai regardless of where the server runs or what the process TZ is,
 * and an interval task must keep its cadence when a run takes longer than the
 * gap between runs.
 */

const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

const scheduler = require("./scheduler.cjs");

const SHANGHAI = 8 * 60 * 60 * 1000;

function shanghai(y, m, d, h, min) {
  return Date.UTC(y, m - 1, d, h, min, 0) - SHANGHAI;
}

function tempStore() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "fairy-schedule-"));
  return path.join(dir, "scheduled_tasks.json");
}

// --- schedules -------------------------------------------------------------

test("a daily schedule keeps the local clock, not the server's zone", () => {
  const { schedule } = scheduler.normalizeSchedule({ type: "daily", at: "8:05" });
  assert.equal(schedule.at, "08:05");

  // 2026-10-01 07:00 Shanghai -> next is 08:05 the same day.
  const from = shanghai(2026, 10, 1, 7, 0);
  assert.equal(scheduler.nextRunAt(schedule, from, null), shanghai(2026, 10, 1, 8, 5));

  // 09:00 Shanghai -> already past today, next is tomorrow.
  const later = shanghai(2026, 10, 1, 9, 0);
  assert.equal(scheduler.nextRunAt(schedule, later, null), shanghai(2026, 10, 2, 8, 5));
});

test("the same instant is read in the scheduling zone whatever TZ says", () => {
  const parts = scheduler.zonedParts(new Date(Date.UTC(2026, 9, 1, 1, 30)));
  assert.deepEqual(
    { y: parts.year, m: parts.month, d: parts.day, h: parts.hour, min: parts.minute },
    { y: 2026, m: 10, d: 1, h: 9, min: 30 },
  );
});

test("a weekly schedule walks forward to the right weekday", () => {
  const { schedule } = scheduler.normalizeSchedule({ type: "weekly", weekday: 5, at: "09:30" });
  // 2026-10-01 is a Thursday; Friday the 2nd is the next weekday 5.
  const from = shanghai(2026, 10, 1, 12, 0);
  assert.equal(scheduler.nextRunAt(schedule, from, null), shanghai(2026, 10, 2, 9, 30));
  assert.equal(scheduler.describeSchedule(schedule), "每周五 09:30");
});

test("an interval schedule counts from the previous run, not from the clock", () => {
  const { schedule } = scheduler.normalizeSchedule({ type: "every", minutes: 30 });
  const fired = shanghai(2026, 10, 1, 8, 0);
  // The run itself took 25 minutes; the next one is still 30 minutes after the
  // fire time, not 30 minutes after it finished.
  const now = fired + 25 * 60 * 1000;
  assert.equal(scheduler.nextRunAt(schedule, now, fired), fired + 30 * 60 * 1000);
  // If the clock has already blown past several intervals, roll to the future.
  assert.equal(scheduler.nextRunAt(schedule, fired + 95 * 60 * 1000, fired), fired + 120 * 60 * 1000);
});

test("a one-shot in the past has no next run", () => {
  const past = new Date(Date.now() - 60_000).toISOString();
  const { schedule } = scheduler.normalizeSchedule({ type: "once", at: past });
  assert.equal(scheduler.nextRunAt(schedule, Date.now(), null), null);
});

test("schedules that cannot be explained back to the user are refused", () => {
  assert.match(scheduler.normalizeSchedule({ type: "daily", at: "25:00" }).error, /HH:MM/);
  assert.match(scheduler.normalizeSchedule({ type: "every", minutes: 1 }).error, /至少 5 分钟/);
  assert.match(scheduler.normalizeSchedule({ type: "weekly", weekday: 9, at: "08:00" }).error, /weekday/);
  assert.match(scheduler.normalizeSchedule({ type: "cron", at: "* * * * *" }).error, /once \/ daily/);
});

// --- tasks -----------------------------------------------------------------

test("a task needs a title and something to do", () => {
  const bad = scheduler.validateTask({ title: "", schedule: { type: "daily", at: "08:00" }, action: { kind: "prompt", text: "" } });
  assert.match(bad.error, /标题/);
  assert.match(bad.error, /不能为空/);

  const ok = scheduler.validateTask({
    title: "吃药提醒",
    schedule: { type: "daily", at: "08:00" },
    action: { kind: "prompt", text: "提醒我吃药" },
  });
  assert.equal(ok.value.title, "吃药提醒");
  assert.equal(ok.value.action.kind, "prompt");
  assert.equal(ok.value.notify, null);
});

test("both channels can be pushed to, and each needs a conversation", () => {
  assert.deepEqual(
    scheduler.normalizeNotify({ channel: "wechat", conversation_id: "u1@im.wechat" }).notify,
    { channel: "wechat", conversation_id: "u1@im.wechat" },
  );
  assert.equal(scheduler.normalizeNotify({ channel: "telegram", conversation_id: "x" }).error !== undefined, true);
  assert.equal(scheduler.normalizeNotify({ channel: "qq" }).error !== undefined, true);
  assert.deepEqual(
    scheduler.normalizeNotify({ channel: "qq", conversation_id: "openid-A" }).notify,
    { channel: "qq", conversation_id: "openid-A" },
  );
});

test("tasks are created, listed per owner, and deleted by their owner only", () => {
  const file = tempStore();
  const created = scheduler.createTask(file, {
    owner: 1,
    title: "早报",
    schedule: { type: "daily", at: "08:00" },
    action: { kind: "prompt", text: "给我今天要看的新闻" },
    notify: null,
  });
  scheduler.createTask(file, {
    owner: 2,
    title: "别人的",
    schedule: { type: "daily", at: "09:00" },
    action: { kind: "prompt", text: "别人的任务" },
    notify: null,
  });

  assert.equal(scheduler.listTasks(file, 1).length, 1);
  assert.equal(scheduler.listTasks(file, 2).length, 1);
  assert.equal(created.next_run_at > Date.now(), true);

  assert.equal(scheduler.deleteTask(file, 2, created.id), false, "另一个账号不能删掉别人的任务");
  assert.equal(scheduler.deleteTask(file, 1, created.id), true);
  assert.equal(scheduler.listTasks(file, 1).length, 0);
});

test("a finished run advances the next occurrence and clears a one-shot", () => {
  const file = tempStore();
  const daily = scheduler.createTask(file, {
    owner: null,
    title: "每天",
    schedule: { type: "daily", at: "08:00" },
    action: { kind: "prompt", text: "每天一次" },
    notify: null,
  });
  const firedAt = shanghai(2026, 10, 1, 8, 0);
  const after = scheduler.recordRun(file, daily.id, { status: "ok", summary: "做完了" }, firedAt);
  assert.equal(after.last_status, "ok");
  assert.equal(after.run_count, 1);
  assert.equal(after.next_run_at, shanghai(2026, 10, 2, 8, 0));
  assert.equal(after.enabled, true);

  const once = scheduler.createTask(file, {
    owner: null,
    title: "一次性",
    schedule: { type: "once", at: new Date(Date.now() + 60_000).toISOString() },
    action: { kind: "prompt", text: "只做一次" },
    notify: null,
  });
  const done = scheduler.recordRun(file, once.id, { status: "ok" }, Date.now());
  assert.equal(done.enabled, false, "一次性任务跑完就该停，不该留在列表里等一个不会来的下一次");
  assert.equal(done.next_run_at, null);
});

test("only tasks that are due and not already running are picked up", () => {
  const file = tempStore();
  const due = scheduler.createTask(file, {
    owner: null,
    title: "到点了",
    schedule: { type: "daily", at: "08:00" },
    action: { kind: "command", command: "echo hi" },
    notify: null,
  });
  scheduler.createTask(file, {
    owner: null,
    title: "还没到",
    schedule: { type: "daily", at: "08:00" },
    action: { kind: "command", command: "echo later" },
    notify: null,
  });

  const store = scheduler.loadStore(file);
  store.tasks[0].next_run_at = Date.now() - 1000;
  store.tasks[1].next_run_at = Date.now() + 60_000;
  scheduler.saveStore(file, store);

  assert.deepEqual(scheduler.dueTasks(file, Date.now()).map(task => task.id), [due.id]);
  assert.deepEqual(scheduler.dueTasks(file, Date.now(), new Set([due.id])), [], "正在跑的不能被再发一次");

  scheduler.updateTask(file, null, due.id, { enabled: false });
  assert.deepEqual(scheduler.dueTasks(file, Date.now()), []);
});

// --- results ---------------------------------------------------------------

test("the answer read back from a turn is the last assistant text", () => {
  const payload = {
    type: "done",
    messages: {
      data: {
        messages: [
          { role: "user", contents: [{ type: "text", content: "提醒我吃药" }] },
          { role: "assistant", contents: [{ type: "thinking", content: "内部" }, { type: "text", content: "该吃药了" }] },
        ],
      },
    },
  };
  assert.equal(scheduler.extractFinalText(payload), "该吃药了");
  assert.equal(scheduler.extractFinalText({}), "");
});

// --- running ---------------------------------------------------------------

test("a command task runs for real and records its output", async () => {
  // The runner is the one place the module reaches outside itself - spawn for
  // commands and notifications, fetch for prompts. A missing import there is
  // invisible to every pure test above and only shows up as a task that ran and
  // then reported "spawn is not defined" to the person waiting for a reminder.
  const file = tempStore();
  const task = scheduler.createTask(file, {
    owner: null,
    title: "回声",
    schedule: { type: "daily", at: "08:00" },
    action: { kind: "command", command: "echo 到点了" },
    notify: null,
  });
  const result = await scheduler.runTask(task, {
    storeFile: file,
    repoRoot: os.tmpdir(),
    pythonBin: "/bin/echo",
    log: () => {},
  });
  assert.equal(result.ok, true);
  assert.match(result.summary, /到点了/);
  assert.equal(scheduler.listTasks(file, null)[0].last_status, "ok");
});

test("a failing command is recorded as a failure, with its output", async () => {
  const file = tempStore();
  const task = scheduler.createTask(file, {
    owner: null,
    title: "会失败",
    schedule: { type: "daily", at: "08:00" },
    action: { kind: "command", command: "echo 坏了 >&2; exit 3" },
    notify: null,
  });
  const result = await scheduler.runTask(task, {
    storeFile: file,
    repoRoot: os.tmpdir(),
    pythonBin: "/bin/echo",
    log: () => {},
  });
  assert.equal(result.ok, false);
  assert.match(String(result.error), /3/);
  assert.match(scheduler.listTasks(file, null)[0].last_error, /坏了/);
});

test("a task with a notification target reaches the push step", async () => {
  // `pythonBin` stands in for the interpreter that would push to QQ; what this
  // pins is that a reminder actually gets that far, and that the push runs where
  // the qq-bot skill lives.
  const file = tempStore();
  const repoRoot = path.resolve(__dirname, "..");
  const task = scheduler.createTask(file, {
    owner: null,
    title: "提醒",
    schedule: { type: "daily", at: "08:00" },
    action: { kind: "command", command: "echo 提醒主人" },
    notify: { channel: "qq", conversation_id: "openid-A" },
  });
  const result = await scheduler.runTask(task, {
    storeFile: file,
    repoRoot,
    pythonBin: "/bin/echo",
    log: () => {},
  });
  assert.equal(result.ok, true, `notify step failed: ${result.error || ""}`);
});

// A schedule that fires every N minutes against an empty queue still costs a
// full agent turn each time. A task may carry `action.guard`: exit 0 means
// "worth waking the agent", anything else means the tick is a no-op.
test("a task guard decides whether the schedule is worth an agent turn", async () => {
  const deps = { repoRoot: os.tmpdir() };
  assert.equal((await scheduler.guardTask({ action: { kind: "agent", prompt: "go" } }, deps)).ok, true);
  assert.equal((await scheduler.guardTask({ action: { guard: "   " } }, deps)).ok, true);
  assert.equal((await scheduler.guardTask({ action: { guard: "true" } }, deps)).ok, true);
  const blocked = await scheduler.guardTask({ action: { guard: "echo 'backlog is empty' >&2; exit 3" } }, deps);
  assert.equal(blocked.ok, false);
  assert.match(blocked.reason, /backlog is empty/);
  assert.match(blocked.reason, /3/);
});

test("a blocked guard skips the task instead of paying for a run", async () => {
  const file = tempStore();
  const marker = path.join(os.tmpdir(), `guard-marker-${process.pid}-blocked.txt`);
  fs.rmSync(marker, { force: true });
  scheduler.createTask(file, {
    owner: "harry",
    title: "guarded",
    schedule: { type: "every", minutes: 15 },
    action: { kind: "command", command: `printf ran > ${marker}`, guard: "exit 1" },
  });
  const store = scheduler.loadStore(file);
  store.tasks[0].next_run_at = 1; // due now (dueTasks ignores 0)
  scheduler.saveStore(file, store);

  const sched = scheduler.startScheduler({
    storeFile: file,
    repoRoot: os.tmpdir(),
    pollMs: 3600000,
    callAgent: async () => "unused",
    deliver: async () => ({ ok: true }),
  });
  await sched.tick();
  await new Promise((r) => setTimeout(r, 400));
  sched.stop();

  assert.equal(fs.existsSync(marker), false, "the command must not run when the guard blocks");
  const after = scheduler.loadStore(file).tasks[0];
  assert.equal(after.last_status, "skipped");
  assert.equal(after.run_count, 0, "a skipped tick is not a run");
  assert.ok(after.next_run_at > Date.now(), "the schedule must still move on");
});

test("a guard that passes leaves the task running as before", async () => {
  const file = tempStore();
  const marker = path.join(os.tmpdir(), `guard-marker-${process.pid}-ok.txt`);
  fs.rmSync(marker, { force: true });
  scheduler.createTask(file, {
    owner: "harry",
    title: "guarded-ok",
    schedule: { type: "every", minutes: 15 },
    action: { kind: "command", command: `printf ran > ${marker}`, guard: "true" },
  });
  const store = scheduler.loadStore(file);
  store.tasks[0].next_run_at = 1;
  scheduler.saveStore(file, store);

  const sched = scheduler.startScheduler({
    storeFile: file,
    repoRoot: os.tmpdir(),
    pollMs: 3600000,
    callAgent: async () => "unused",
    deliver: async () => ({ ok: true }),
  });
  await sched.tick();
  await new Promise((r) => setTimeout(r, 400));
  sched.stop();

  assert.equal(fs.existsSync(marker), true, "a passing guard must not change existing behaviour");
  assert.equal(scheduler.loadStore(file).tasks[0].last_status, "ok");
  assert.equal(scheduler.loadStore(file).tasks[0].run_count, 1);
});

// --- update: 编辑已存在的任务（设置页「编辑」按钮走的就是这条） ------------

// 把 next_run_at 换算成上海本地时刻，方便断言"下一次真的改到点了"。
function atShanghai(nextRunAt) {
  return new Date(nextRunAt + SHANGHAI);
}

test("updateTask rewrites the schedule and recomputes next_run_at", () => {
  const file = tempStore();
  const created = scheduler.createTask(file, {
    owner: 1,
    title: "早报",
    schedule: { type: "daily", at: "08:00" },
    action: { kind: "prompt", text: "给我今天要看的新闻" },
    notify: null,
  });
  const before = atShanghai(created.next_run_at);
  assert.equal(before.getUTCHours(), 8);

  const updated = scheduler.updateTask(file, 1, created.id, {
    schedule: { type: "daily", at: "09:30" },
  });
  assert.equal(updated.schedule.type, "daily");
  assert.equal(updated.schedule.at, "09:30");
  const after = atShanghai(updated.next_run_at);
  assert.equal(after.getUTCHours(), 9);
  assert.equal(after.getUTCMinutes(), 30);
  // 只改时间不应把标题和动作弄丢
  assert.equal(updated.title, "早报");
  assert.equal(updated.action.text, "给我今天要看的新闻");
});

test("updateTask recomputes next_run_at when the weekday changes", () => {
  const file = tempStore();
  const created = scheduler.createTask(file, {
    owner: 1,
    title: "周报",
    schedule: { type: "weekly", at: "17:00", weekday: 1 },
    action: { kind: "prompt", text: "整理这周进展" },
    notify: null,
  });
  const updated = scheduler.updateTask(file, 1, created.id, {
    schedule: { type: "weekly", at: "17:00", weekday: 5 },
  });
  assert.equal(updated.schedule.weekday, 5);
  // 下一个周五：day 0=周日, 5=周五
  const next = atShanghai(updated.next_run_at).getUTCDay();
  assert.equal(next, 5);
});

test("updateTask keeps a disabled task disabled", () => {
  const file = tempStore();
  const created = scheduler.createTask(file, {
    owner: 1,
    title: "停掉的任务",
    schedule: { type: "daily", at: "08:00" },
    action: { kind: "prompt", text: "x" },
    notify: null,
  });
  scheduler.updateTask(file, 1, created.id, { enabled: false });
  const edited = scheduler.updateTask(file, 1, created.id, {
    schedule: { type: "daily", at: "20:00" },
  });
  assert.equal(edited.enabled, false, "改时间不应该顺带把任务重新启用");
});

test("updateTask refuses ids that do not exist or belong to another owner", () => {
  const file = tempStore();
  const created = scheduler.createTask(file, {
    owner: 1,
    title: "我的任务",
    schedule: { type: "daily", at: "08:00" },
    action: { kind: "prompt", text: "x" },
    notify: null,
  });
  assert.equal(scheduler.updateTask(file, 1, "no-such-id", { title: "x" }), null);
  // 别人的任务不能改，否则知道 id 就能跨用户改别人任务
  assert.equal(scheduler.updateTask(file, 2, created.id, { title: "被劫持" }), null);
  assert.equal(scheduler.listTasks(file, 1)[0].title, "我的任务");
});

test("validateTask rejects a schedule the update endpoint cannot accept", () => {
  assert.match(
    scheduler.validateTask({
      title: "坏时间",
      schedule: { type: "daily", at: "99:99" },
      action: { kind: "prompt", text: "x" },
    }).error,
    /HH:MM/,
  );
  assert.match(
    scheduler.validateTask({
      title: "未知类型",
      schedule: { type: "fortnightly", at: "08:00" },
      action: { kind: "prompt", text: "x" },
    }).error,
    /类型|不支持|未知/,
  );
});
