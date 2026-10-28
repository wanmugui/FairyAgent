// 回归测试：会话被停止（interrupted）时，思考过程不能消失。
//
// 背景：流式期间 thinking 累积在草稿消息上，草稿被 pop 掉后，thinking
// 只剩服务端事件里带的那一份。服务端在「用户停止」这条路径上不返回
// thinking，于是整段推理从 transcript 里消失。
//
// 本测试从 App.jsx 抽出 interrupted 分支的原样逻辑来跑，不重写实现。
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';
import { test } from 'node:test';

const src = readFileSync('src/App.jsx', 'utf8').replace(/\r\n/g, '\n');

const start = src.indexOf("} else if (event.type === 'interrupted') {");
assert.ok(start > 0, 'interrupted branch not found');

const bodyStart = src.indexOf('batchedSetMessages(prev => {', start);
assert.ok(bodyStart > start, 'interrupted setMessages not found');

const bodyEnd = src.indexOf('return copy;\n      });', bodyStart);
assert.ok(bodyEnd > bodyStart, 'end of interrupted setMessages not found');

const body = src.slice(bodyStart + 'batchedSetMessages(prev => {'.length, bodyEnd + 'return copy;'.length);

const run = new Function('prev', 'streamedThinkingRef', `
  ${body}
  return copy;
`);

test('停止会话：正式消息已存在时，保留其 thinking 并标记 truncated', () => {
  const ref = { current: '流式累积的推理内容' };
  const out = run(
    [
      { role: 'user', content: 'hi' },
      { role: 'assistant', content: '部分回答', thinking: '已有推理' },
    ],
    ref,
  );
  const msg = out[out.length - 1];
  assert.equal(msg.truncated, true, '应标记为被截断');
  assert.equal(msg.thinking, '已有推理', '应保留原有 thinking');
});

test('停止会话：正式消息没有 thinking 时，用流式累积内容兜底', () => {
  const ref = { current: '流式累积的推理内容' };
  const out = run(
    [
      { role: 'user', content: 'hi' },
      { role: 'assistant', content: '部分回答' },
    ],
    ref,
  );
  const msg = out[out.length - 1];
  assert.equal(msg.truncated, true, '应标记为被截断');
  assert.equal(msg.thinking, '流式累积的推理内容', '应回填流式 thinking');
});

test('停止会话：尚未生成正式消息时，新建带 thinking 的消息', () => {
  const ref = { current: '流式累积的推理内容' };
  const out = run([{ role: 'user', content: 'hi' }], ref);
  const msg = out[out.length - 1];
  assert.equal(msg.role, 'assistant', '应补一条 assistant 消息');
  assert.equal(msg.truncated, true, '应标记为被截断');
  assert.equal(msg.thinking, '流式累积的推理内容', '应保留流式 thinking');
});

test('停止会话：完全没有思考内容时不应凭空造消息', () => {
  const ref = { current: '' };
  const out = run([{ role: 'user', content: 'hi' }], ref);
  assert.equal(out.length, 1, '不应新增消息');
});
