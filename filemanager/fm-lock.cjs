"use strict";
/**
 * P1-10 文件锁：租约（lease）锁。
 *
 * 存放位置选**进程内 Map**，不落盘。理由：filemanager 是单 Node 进程服务所有
 * 客户端，验收要的「两处同时打开」是两个浏览器打同一个进程，Map 天然共享。
 * 真要上多进程/多机才需要落盘或 Redis——那时再换不迟，现在引入只增加复杂度。
 *
 * 【主要复杂度：孤儿锁】客户端崩溃 / 关标签页时不会来 release，锁就会永远
 * 挡住别人。所以锁是**有期限的租约**：持锁方要定期续期（前端心跳），不续就
 * 到期自动失效。这里两条都做：
 *   1) 每次访问先惰性清扫——不依赖定时器也能保证正确性；
 *   2) 另加一个定时清扫——否则「扫到的时刻」仍取决于谁来访问。
 * 只做 1 不做 2 会漏：没人访问时就一直显示被占。
 *
 * 【不做的事】不判断「编辑内容有没有改动」。锁只防「两个人同时改」，
 * 不防「一个人开两个标签页编辑同一个文件」——那是同一个人，拦了只是添乱。
 */

const crypto = require("crypto");

// 60s 租约 + 20s 心跳是 spec 定的节奏：能容忍两次连续心跳丢失
const DEFAULT_TTL_MS = Number(process.env.FM_LOCK_TTL_MS) || 60_000;
const SWEEP_MS = Math.max(1000, Math.floor(DEFAULT_TTL_MS / 6));

// path -> { token, owner, acquiredAt, expiresAt }
const locks = new Map();
let sweeper = null;

function now() { return Date.now(); }

/** 惰性清扫：把所有已过期的租约踢掉。没被访问到的那部分交给定时器。 */
function sweep() {
  const t = now();
  for (const [p, l] of locks) {
    if (l.expiresAt <= t) locks.delete(p);
  }
}

/** 定时清扫：保证「没人访问时」孤儿锁也会消失。unref 以免吊住进程。 */
function ensureSweeper() {
  if (sweeper) return;
  sweeper = setInterval(sweep, SWEEP_MS);
  if (sweeper.unref) sweeper.unref();
}

function describe(p) {
  const l = locks.get(p);
  if (!l) return null;
  return { path: p, owner: l.owner, acquiredAt: l.acquiredAt, expiresAt: l.expiresAt, token: l.token };
}

/**
 * 取锁。同一 owner 重复取视为续期（同一标签页重开文件不该把自己挡在外面），
 * 这也是「幂等」该有的样子。
 */
function acquire(path, owner, ttlMs) {
  ensureSweeper();
  sweep();
  const t = now();
  const ttl = Number(ttlMs) > 0 ? Number(ttlMs) : DEFAULT_TTL_MS;
  const cur = locks.get(path);

  if (cur && cur.owner !== owner) {
    return { ok: false, holder: { owner: cur.owner, since: cur.acquiredAt }, expiresIn: cur.expiresAt - t };
  }
  if (cur) {
    cur.expiresAt = t + ttl;
    return { ok: true, token: cur.token, expiresAt: cur.expiresAt, reentrant: true };
  }
  const rec = { token: crypto.randomUUID(), owner, acquiredAt: t, expiresAt: t + ttl };
  locks.set(path, rec);
  return { ok: true, token: rec.token, expiresAt: rec.expiresAt, reentrant: false };
}

/** 续期。token 不匹配就拒绝——这挡住了「锁已易主，旧持有者还在续命」。 */
function renew(path, token, ttlMs) {
  ensureSweeper();
  sweep();
  const cur = locks.get(path);
  if (!cur) return { ok: false, reason: "gone" };
  if (cur.token !== token) return { ok: false, reason: "stolen", holder: { owner: cur.owner, since: cur.acquiredAt } };
  const ttl = Number(ttlMs) > 0 ? Number(ttlMs) : DEFAULT_TTL_MS;
  cur.expiresAt = now() + ttl;
  return { ok: true, expiresAt: cur.expiresAt };
}

function release(path, token) {
  const cur = locks.get(path);
  if (!cur) return { ok: true, alreadyGone: true };
  // 不给"拿别人的 token 去解锁"开口子，否则锁形同虚设
  if (token && cur.token !== token) return { ok: false, reason: "stolen" };
  locks.delete(path);
  return { ok: true };
}

/** 保存前校验：没持锁就不许写，否则这把锁只是个摆设。 */
function check(path, token) {
  sweep();
  const cur = locks.get(path);
  if (!cur) return { ok: false, reason: "gone" };
  if (cur.token !== token) return { ok: false, reason: "stolen", holder: { owner: cur.owner, since: cur.acquiredAt } };
  return { ok: true, expiresAt: cur.expiresAt };
}

function listLocks() { sweep(); return Array.from(locks.values()).map(describe); }
function reset() { locks.clear(); }
function size() { sweep(); return locks.size; }

module.exports = { acquire, renew, release, check, listLocks, reset, size, sweep, DEFAULT_TTL_MS };
