// 从 ChatArea.jsx 抽出 displayMsgs 的去重逻辑原样测试（不重写实现）。
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';
import { test } from 'node:test';

const src = readFileSync('src/features/chat/ChatArea.jsx', 'utf8');
const start = src.indexOf('const displayMsgs = useMemo(() => {');
assert.ok(start > 0, 'displayMsgs block not found');
const end = src.indexOf('}, [messages, audioMap]);', start);
assert.ok(end > start, 'end of displayMsgs block not found');
const body = src.slice(start + 'const displayMsgs = useMemo(() => {'.length, src.indexOf('const cleaned = suppressDuplicateReports', start));

const run = new Function('messages', 'audioMap', 'suppressDuplicateReports', `
  ${body}
  return deduped;
`);

const clean = (ms) => ms.map(m => ({ ...m, _suppressReport: false }));
const bubble = (content, files, injectId) => ({ role: 'user', content, files, injectId, injected: true, pending: true });
const echo = (content, files, injectId) => ({ role: 'user', content, files, injectId, injected: true, pending: false });

test('实时态：乐观气泡 + injected_user 回显只渲染一条', () => {
  const f = [{ path: '/x.png' }];
  const out = run([bubble('看这个', f, 'local-inject-1'), echo('看这个', f, '55')], null, clean);
  assert.equal(out.length, 1, JSON.stringify(out));
  assert.equal(out[0].injectId, '55', '应保留被认领后的那条');
});

test('实时态：纯文本注入同样只渲染一条', () => {
  const out = run([bubble('纯文本', [], 'local-inject-1'), echo('纯文本', [], '55')], null, clean);
  assert.equal(out.length, 1);
});

test('两条不同内容都渲染', () => {
  const out = run([bubble('第一条', [], 'l1'), bubble('第二条', [], 'l2')], null, clean);
  assert.equal(out.length, 2);
});

test('用户隔开连发相同内容不被误合并', () => {
  const out = run([
    bubble('好的', [], 'l1'),
    { role: 'assistant', content: '回复一' },
    bubble('好的', [], 'l2'),
  ], null, clean);
  assert.equal(out.length, 3, JSON.stringify(out));
});

test('有 id 的历史消息按 id 去重不受影响', () => {
  const out = run([{ role: 'user', content: 'a', id: 1 }, { role: 'user', content: 'a', id: 1 }], null, clean);
  assert.equal(out.length, 1);
});

test('同正文不同附件不合并', () => {
  const out = run([bubble('看图', [{ path: '/x.png' }], 'l1'), bubble('看图', [{ path: '/y.png' }], 'l2')], null, clean);
  assert.equal(out.length, 2);
});

test('助手消息不会被内容签名误合并', () => {
  const out = run([
    { role: 'assistant', content: '好' },
    { role: 'assistant', content: '好' },
  ], null, clean);
  assert.equal(out.length, 2);
});
