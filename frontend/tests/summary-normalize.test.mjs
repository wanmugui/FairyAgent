// Regression guard for the compressed-history handover ("summary") message.
//
// The agent stores the handover as a *user-role* message with
// internal_type=context_summary. The chat UI groups interactions by user
// message, so before this guard the summary opened a brand new user turn and
// never showed up inside the tool/thinking fold. normalizeServiceMessages must
// therefore move it to the assistant side as a process message.

import { register } from 'node:module';
import test from 'node:test';
import assert from 'node:assert/strict';

register('./esm-extensionless-loader.mjs', import.meta.url);

const { normalizeServiceMessages } = await import('../src/api/chat.js');

const SUMMARY = '<summary>\n[当前请求] 三件事\n</summary>';

test('flat context_summary becomes an assistant process message', () => {
  const out = normalizeServiceMessages([
    { id: 1, role: 'user', content: '原始提问' },
    { id: 2, role: 'user', content: SUMMARY, internal_type: 'context_summary', interaction_id: 't1' },
    { id: 3, role: 'assistant', content: '<report>done</report>', tool_calls: [] },
  ]);
  assert.deepEqual(out.map(m => m.role), ['user', 'assistant', 'assistant']);
  assert.equal(out[1].internal_type, 'context_summary');
  assert.ok(out[1].content.includes('[当前请求]'), 'summary body must survive intact');
  assert.deepEqual(out[1].tool_calls, [], 'summary must not pretend to call tools');
  assert.equal(out[1].is_final, false, 'summary is process state, never a final answer');
});

test('contents-style context_summary becomes an assistant process message', () => {
  const out = normalizeServiceMessages([
    {
      id: 4,
      role: 'user',
      interaction_id: 't2',
      contents: [{ type: 'text', content: '问题' }],
    },
    {
      id: 5,
      role: 'user',
      interaction_id: 't2',
      contents: [{ type: 'text', content: SUMMARY, internal_type: 'context_summary' }],
    },
  ]);
  assert.deepEqual(out.map(m => m.role), ['user', 'assistant']);
  assert.equal(out[1].internal_type, 'context_summary');
});

test('a summary never opens a user turn', () => {
  const out = normalizeServiceMessages([
    { id: 6, role: 'user', content: SUMMARY, internal_type: 'context_summary' },
  ]);
  assert.equal(out.filter(m => m.role === 'user').length, 0);
});

test('internal control messages are still dropped', () => {
  const out = normalizeServiceMessages([
    { id: 7, role: 'user', content: '普通问题' },
    { id: 8, role: 'user', content: '[会话状态] 内部', internal_type: 'auto_continue' },
    { id: 9, role: 'user', content: '补充说明', internal_type: 'delivered_status' },
  ]);
  assert.deepEqual(out.map(m => m.content), ['普通问题']);
});

test('a legacy <summary> tagged user message is treated as a summary too', () => {
  const out = normalizeServiceMessages([
    { id: 10, role: 'user', content: SUMMARY },
  ]);
  assert.equal(out[0].role, 'assistant');
  assert.equal(out[0].internal_type, 'context_summary');
});
