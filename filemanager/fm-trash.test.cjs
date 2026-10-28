'use strict';
/**
 * 回收站周期清理的回归测试。
 *
 * 重点不在「能不能删」，而在「不该删的绝对不能删」——
 * 回收站里的东西是主人以为还能拿回来的，真删错了不可逆。
 */
const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');
const os = require('node:os');

// 测试必须用独立数据目录，绝不碰主人的真实回收站。
//
// 注意变量名：模块用的是 XDG_DATA_HOME，不是 FAIRY_DATA_HOME。
// 写错会导致测试直接在主人真实回收站上 reset() ——
// 真实事故：本文件最初写成 FAIRY_DATA_HOME，隔离静默失效，
// reset() 删掉了主人 16 条 / 11GB 的回收站内容。
// 下面这行断言就是为了让这类错误当场暴露，而不是等到出事才发现。
const ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'fm-trash-test-'));
process.env.XDG_DATA_HOME = ROOT;
const trash = require('./fm-trash.cjs');

// 隔离断言：路径一旦指向真实数据目录就地地失败。
const REAL_TRASH = path.join(os.homedir(), '.local', 'share', 'Fairy', 'filemanager', 'trash');
assert.notStrictEqual(
  trash.trashRoot(), REAL_TRASH,
  '测试正在操作主人的真实回收站！XDG_DATA_HOME 隔离是否生效？'
);

const DAY = 86400000;

/** 造一条回收站条目，返回 id。 */
function seed(name, daysAgo, { bytes = 8, subdir = null } = {}) {
  const id = trash.trashPath ? null : null;
  const entryId = `${name}`;
  const payload = subdir
    ? path.join(trash.filesDir(), entryId, subdir)
    : path.join(trash.filesDir(), entryId);
  fs.mkdirSync(path.dirname(payload), { recursive: true });
  fs.writeFileSync(payload, Buffer.alloc(bytes, 1));
  const when = new Date(Date.now() - daysAgo * DAY);
  const stamp = `${when.getFullYear()}-${String(when.getMonth() + 1).padStart(2, '0')}-` +
    `${String(when.getDate()).padStart(2, '0')}T${String(when.getHours()).padStart(2, '0')}:` +
    `${String(when.getMinutes()).padStart(2, '0')}:${String(when.getSeconds()).padStart(2, '0')}`;
  fs.writeFileSync(
    path.join(trash.infoDir(), entryId + '.trashinfo'),
    `[Trash Info]\nPath=/tmp/${name}\nDeletionDate=${stamp}\n`
  );
  return entryId;
}

function reset() {
  fs.rmSync(trash.filesDir(), { recursive: true, force: true });
  fs.rmSync(trash.infoDir(), { recursive: true, force: true });
  fs.mkdirSync(trash.filesDir(), { recursive: true });
  fs.mkdirSync(trash.infoDir(), { recursive: true });
}

test.after(() => fs.rmSync(ROOT, { recursive: true, force: true }));

test('超过保留期的条目被删除', () => {
  reset();
  seed('old-file', 40);
  const r = trash.purgeExpired({ retentionDays: 30 });
  assert.strictEqual(r.removed, 1);
  assert.strictEqual(fs.existsSync(path.join(trash.filesDir(), 'old-file')), false);
  assert.strictEqual(fs.existsSync(path.join(trash.infoDir(), 'old-file.trashinfo')), false);
});

test('保留期内的条目原封不动', () => {
  reset();
  seed('fresh-file', 3);
  const r = trash.purgeExpired({ retentionDays: 30 });
  assert.strictEqual(r.removed, 0);
  assert.strictEqual(r.kept, 1);
  assert.strictEqual(fs.existsSync(path.join(trash.filesDir(), 'fresh-file')), true);
  assert.strictEqual(fs.existsSync(path.join(trash.infoDir(), 'fresh-file.trashinfo')), true);
});

test('边界：正好等于保留天数的不删（避免边界抖动误删）', () => {
  reset();
  seed('edge-file', 30);
  // .trashinfo 里的时间只精确到秒，直接拿 Date.now() 推边界永远差几毫秒。
  // 所以反推：读出实际落盘的时刻，再让 now 正好等于「它 + 30 天」，
  // 这样才真正落在边界上，而不是在边界附近抖动。
  const info = fs.readFileSync(path.join(trash.infoDir(), 'edge-file.trashinfo'), 'utf8');
  const stored = /DeletionDate=(.+)/.exec(info)[1].trim();
  const exact = new Date(stored).getTime();
  assert.ok(Number.isFinite(exact), '落盘时间应可解析');

  const r = trash.purgeExpired({ retentionDays: 30, now: new Date(exact + 30 * DAY) });
  assert.strictEqual(r.removed, 0, '恰好到期应保守保留');

  // 越过边界一秒就必须删 —— 证明这不是「永不删除」
  const r2 = trash.purgeExpired({ retentionDays: 30, now: new Date(exact + 30 * DAY + 1000) });
  assert.strictEqual(r2.removed, 1, '超过边界一秒应被删除');
});

test('日期缺失或损坏的条目必须保留 —— 防误删的最后一道闸', () => {
  reset();
  fs.mkdirSync(trash.filesDir(), { recursive: true });
  fs.mkdirSync(trash.infoDir(), { recursive: true });
  // 三种坏数据：完全没有 DeletionDate、空值、完全无法解析
  for (const [name, body] of [
    ['no-date', '[Trash Info]\nPath=/tmp/x\n'],
    ['empty-date', '[Trash Info]\nPath=/tmp/x\nDeletionDate=\n'],
    ['garbage-date', '[Trash Info]\nPath=/tmp/x\nDeletionDate=not-a-date\n'],
  ]) {
    fs.writeFileSync(path.join(trash.filesDir(), name), Buffer.alloc(4, 1));
    fs.writeFileSync(path.join(trash.infoDir(), name + '.trashinfo'), body);
  }
  const r = trash.purgeExpired({ retentionDays: 1 });
  assert.strictEqual(r.removed, 0, '坏日期条目绝不能被删');
  assert.strictEqual(r.undated, 3, '应如实报告跳过数量');
  for (const n of ['no-date', 'empty-date', 'garbage-date']) {
    assert.strictEqual(fs.existsSync(path.join(trash.filesDir(), n)), true, `${n} 被误删了`);
  }
});

test('目录条目按递归体积统计，不会低估释放量', () => {
  reset();
  seed('old-dir', 40, { subdir: 'deep/nested.bin', bytes: 4096 });
  const r = trash.purgeExpired({ retentionDays: 30, dryRun: true });
  assert.strictEqual(r.removed, 1);
  assert.ok(r.freedBytes >= 4096, `目录体积被低估: ${r.freedBytes}`);
});

test('dryRun 只报告不落刀', () => {
  reset();
  seed('victim', 40);
  const r = trash.purgeExpired({ retentionDays: 30, dryRun: true });
  assert.strictEqual(r.removed, 1, 'dryRun 应报告将删除的数量');
  assert.strictEqual(r.dryRun, true);
  assert.strictEqual(
    fs.existsSync(path.join(trash.filesDir(), 'victim')), true,
    'dryRun 竟然真的删了'
  );
});

test('retentionDays 为 0 或负数视为「别删」，而不是「全删」', () => {
  reset();
  seed('keep-zero', 999);
  seed('keep-neg', 999);
  assert.strictEqual(trash.purgeExpired({ retentionDays: 0 }).removed, 0);
  assert.strictEqual(trash.purgeExpired({ retentionDays: -5 }).removed, 0);
  assert.strictEqual(fs.existsSync(path.join(trash.filesDir(), 'keep-zero')), true);
});

test('环境变量可覆盖保留天数', () => {
  reset();
  seed('via-env', 10);
  process.env.FAIRY_TRASH_RETENTION_DAYS = '5';
  try {
    const r = trash.purgeExpired({});
    assert.strictEqual(r.removed, 1, 'FAIRY_TRASH_RETENTION_DAYS=5 时 10 天前的条目应被清');
  } finally {
    delete process.env.FAIRY_TRASH_RETENTION_DAYS;
  }
});

test('多条混合：只删过期的，其余保留', () => {
  reset();
  seed('a-old', 60);
  seed('b-new', 2);
  seed('c-mid', 31);
  const r = trash.purgeExpired({ retentionDays: 30 });
  assert.strictEqual(r.removed, 2);
  assert.strictEqual(r.kept, 1);
  assert.strictEqual(fs.existsSync(path.join(trash.filesDir(), 'b-new')), true);
});

test('startAutoPurge 返回的定时器不吊住进程', () => {
  reset();
  const h = trash.startAutoPurge({ intervalMs: 86400000 });
  assert.ok(h.firstRun, '启动时应立即跑一次');
  assert.ok(typeof h.stop === 'function');
  h.stop();
});