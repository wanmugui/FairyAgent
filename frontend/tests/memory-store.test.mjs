import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);
const {
  RETENTION_OPTIONS,
  parseDateKey,
  formatKey,
  todayParts,
  daysBetween,
  listDateMemory,
  readDateMemory,
  archiveExpiredDateMemory,
  archiveStats,
} = require('../memory_store.cjs');

/**
 * 这些测试跑在 OS 临时目录里，永远不碰真实的 memory/segmented/。
 */

function makeRepo() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'fairy-mem2-'));
  fs.mkdirSync(path.join(dir, 'segmented', 'sessions'), { recursive: true });
  return dir;
}

/** 造一天的交互数据。segments: [{kind, content, keys}] */
function writeDay(repo, date, interactions) {
  const dir = path.join(repo, 'segmented', 'sessions', date, 'interactions');
  fs.mkdirSync(dir, { recursive: true });
  interactions.forEach((interaction, ii) => {
    const idir = path.join(dir, interaction.id);
    fs.mkdirSync(idir, { recursive: true });
    const segments = interaction.segments || [];
    segments.forEach((seg, si) => {
      const n = String(si + 1).padStart(4, '0');
      fs.writeFileSync(
        path.join(idir, `s${n}.json`),
        JSON.stringify({
          id: `s${n}`,
          kind: seg.kind,
          content: seg.content,
          keys: seg.keys || [],
        }),
        'utf-8',
      );
    });
    fs.writeFileSync(
      path.join(idir, 'manifest.json'),
      JSON.stringify({
        schema_version: 1,
        session_id: date,
        interaction_id: interaction.id,
        status: 'active',
        started_at: interaction.started_at || 1000 + ii,
        ended_at: interaction.ended_at || 2000 + ii,
        segment_count: segments.length,
        segment_kinds: segments.reduce((acc, s) => {
          acc[s.kind] = (acc[s.kind] || 0) + 1;
          return acc;
        }, {}),
      }),
      'utf-8',
    );
  });
}

test('parseDateKey 接受合法日期并拒绝非法日期', () => {
  assert.deepEqual(parseDateKey('2026-10-26'), { year: 2026, month: 10, day: 26 });
  assert.equal(parseDateKey('2026-02-30'), null, '2 月 30 日必须被拒绝');
  assert.equal(parseDateKey('2026-13-01'), null);
  assert.equal(parseDateKey('not-a-date'), null);
  assert.equal(parseDateKey(''), null);
});

test('parseDateKey 不受时区影响（不按 UTC 偏移一天）', () => {
  assert.deepEqual(parseDateKey('2026-01-01'), { year: 2026, month: 1, day: 1 });
  assert.deepEqual(parseDateKey('2026-12-31'), { year: 2026, month: 12, day: 31 });
});

test('formatKey 与 todayParts 往返一致', () => {
  assert.equal(formatKey(todayParts(new Date(2026, 9, 26))), '2026-10-26');
});

test('daysBetween 计算自然日差', () => {
  assert.equal(daysBetween({ year: 2026, month: 10, day: 1 }, { year: 2026, month: 10, day: 26 }), 25);
  assert.equal(daysBetween({ year: 2026, month: 10, day: 26 }, { year: 2026, month: 10, day: 1 }), -25);
  assert.equal(daysBetween({ year: 2026, month: 10, day: 1 }, { year: 2026, month: 10, day: 1 }), 0);
});

test('listDateMemory 只列出有 interactions 目录的日期', () => {
  const repo = makeRepo();
  writeDay(repo, '2026-10-26', [{ id: 'req-1', segments: [{ kind: 'user_request', content: 'hi' }] }]);
  writeDay(repo, '2026-10-25', [{ id: 'req-1', segments: [{ kind: 'user_request', content: 'hi' }] }]);
  // 空目录：没有 interactions，不该出现
  fs.mkdirSync(path.join(repo, 'segmented', 'sessions', '2026-10-24'), { recursive: true });
  const list = listDateMemory(repo);
  assert.equal(list.length, 2);
  assert.equal(list[0].date, '2026-10-26', '按日期倒序');
  assert.equal(list[0].interactions, 1);
  assert.equal(list[0].segments, 1);
});

test('listDateMemory 统计交互数与段数', () => {
  const repo = makeRepo();
  writeDay(repo, '2026-10-26', [
    { id: 'req-1', segments: [{ kind: 'user_request', content: 'a' }, { kind: 'assistant_reply', content: 'b' }] },
    { id: 'req-2', segments: [{ kind: 'user_request', content: 'c' }] },
  ]);
  const list = listDateMemory(repo);
  assert.equal(list[0].interactions, 2);
  assert.equal(list[0].segments, 3);
});

test('readDateMemory 提取用户提问、回复与关键词', () => {
  const repo = makeRepo();
  writeDay(repo, '2026-10-26', [
    {
      id: 'req-1',
      started_at: 111,
      segments: [
        { kind: 'user_request', content: '帮我查一下配色' },
        { kind: 'assistant_reply', content: '好的，这是分析结果', keys: [{ key: '配色方案' }, { key: '主题' }] },
        { kind: 'tool_operation', content: 'read file' },
        { kind: 'tool_operation', content: 'read file 2' },
      ],
    },
  ]);
  const day = readDateMemory(repo, '2026-10-26');
  assert.ok(day);
  assert.equal(day.interactions.length, 1);
  const it = day.interactions[0];
  assert.equal(it.id, 'req-1');
  assert.deepEqual(it.user_requests, ['帮我查一下配色']);
  assert.equal(it.replies.length, 1);
  assert.equal(it.replies[0], '好的，这是分析结果');
  assert.equal(it.tool_count, 2, '工具调用只计数，不逐条展示');
  assert.deepEqual(it.keys, ['配色方案', '主题']);
});

test('readDateMemory 对不存在的日期返回 null', () => {
  const repo = makeRepo();
  assert.equal(readDateMemory(repo, '2026-01-01'), null);
  assert.equal(readDateMemory(repo, '../etc/passwd'), null);
  assert.equal(readDateMemory(repo, 'bad'), null);
});

test('readDateMemory 对没有有效交互的日期返回空列表而不是 null', () => {
  const repo = makeRepo();
  fs.mkdirSync(path.join(repo, 'segmented', 'sessions', '2026-10-26', 'interactions'), { recursive: true });
  const day = readDateMemory(repo, '2026-10-26');
  assert.ok(day);
  assert.deepEqual(day.interactions, []);
});

test('保留期为 0 时不做任何归档', () => {
  const repo = makeRepo();
  writeDay(repo, '2020-01-01', [{ id: 'req-1', segments: [{ kind: 'user_request', content: 'x' }] }]);
  const res = archiveExpiredDateMemory(repo, 0, new Date(2026, 9, 26));
  assert.deepEqual(res.archived, []);
  assert.equal(listDateMemory(repo).length, 1, '文件必须还在');
});

test('保留期到期时整天移入 archive 而不是删除', () => {
  const repo = makeRepo();
  writeDay(repo, '2020-01-01', [{ id: 'req-1', segments: [{ kind: 'user_request', content: 'x' }] }]);
  writeDay(repo, '2026-09-01', [{ id: 'req-1', segments: [{ kind: 'user_request', content: 'y' }] }]);
  const res = archiveExpiredDateMemory(repo, 6, new Date(2026, 9, 26));
  assert.deepEqual(res.archived, ['2020-01-01']);
  assert.equal(res.kept, 1);
  // 原目录被移走
  assert.equal(fs.existsSync(path.join(repo, 'segmented', 'sessions', '2020-01-01')), false);
  // 归档区里有它，内容一字不差
  const archivedDir = path.join(repo, 'archive', '2020-01-01');
  assert.equal(fs.existsSync(archivedDir), true);
  const seg = path.join(archivedDir, 'interactions', 'req-1', 's0001.json');
  assert.equal(JSON.parse(fs.readFileSync(seg, 'utf-8')).content, 'x');
  // 保留期内的仍在原位
  assert.equal(fs.existsSync(path.join(repo, 'segmented', 'sessions', '2026-09-01')), true);
});

test('归档不会覆盖已有的同名目录', () => {
  const repo = makeRepo();
  writeDay(repo, '2020-01-01', [{ id: 'req-1', segments: [{ kind: 'user_request', content: 'x' }] }]);
  fs.mkdirSync(path.join(repo, 'archive', '2020-01-01'), { recursive: true });
  fs.writeFileSync(path.join(repo, 'archive', '2020-01-01', 'marker.txt'), '旧的', 'utf-8');
  const res = archiveExpiredDateMemory(repo, 6, new Date(2026, 9, 26));
  assert.equal(res.archived.length, 1);
  assert.equal(fs.readFileSync(path.join(repo, 'archive', '2020-01-01', 'marker.txt'), 'utf-8'), '旧的', '既有归档必须原样保留');
  const names = fs.readdirSync(path.join(repo, 'archive'));
  assert.equal(names.length, 2, '新目录以时间戳后缀并存');
});

test('归档不碰 user.md / memory.md / date-memory', () => {
  const repo = makeRepo();
  fs.writeFileSync(path.join(repo, 'user.md'), 'profile');
  fs.writeFileSync(path.join(repo, 'memory.md'), 'durable');
  fs.mkdirSync(path.join(repo, 'date-memory'), { recursive: true });
  fs.writeFileSync(path.join(repo, 'date-memory', '2020-01-01.md'), 'old digest');
  writeDay(repo, '2020-01-01', [{ id: 'req-1', segments: [{ kind: 'user_request', content: 'x' }] }]);
  archiveExpiredDateMemory(repo, 6, new Date(2026, 9, 26));
  assert.equal(fs.existsSync(path.join(repo, 'user.md')), true);
  assert.equal(fs.existsSync(path.join(repo, 'memory.md')), true);
  assert.equal(fs.existsSync(path.join(repo, 'date-memory', '2020-01-01.md')), true);
});

test('archiveStats 统计归档区条目与体积', () => {
  const repo = makeRepo();
  writeDay(repo, '2020-01-01', [{ id: 'req-1', segments: [{ kind: 'user_request', content: 'x' }] }]);
  writeDay(repo, '2020-02-01', [{ id: 'req-1', segments: [{ kind: 'user_request', content: 'y' }] }]);
  archiveExpiredDateMemory(repo, 6, new Date(2026, 9, 26));
  const st = archiveStats(repo);
  assert.equal(st.count, 2);
  assert.ok(st.bytes > 0);
});

test('RETENTION_OPTIONS 覆盖主人要的半年一次', () => {
  assert.ok(RETENTION_OPTIONS.includes(6), '必须提供 6 个月选项');
  assert.ok(RETENTION_OPTIONS.includes(0), '必须提供永久保留选项');
});
