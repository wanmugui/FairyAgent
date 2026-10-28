// 把 App.jsx 里的 appendUniqueMessages 抽出来单测：真实逻辑，不是复刻。
import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';
import { test } from 'node:test';

const src = readFileSync('src/App.jsx', 'utf8');
function grab(name) {
  const start = src.indexOf('function ' + name);
  if (start < 0) throw new Error('not found: ' + name);
  let depth = 0, i = src.indexOf('{', start);
  for (let j = i; j < src.length; j++) {
    if (src[j] === '{') depth++;
    else if (src[j] === '}') { depth--; if (depth === 0) return src.slice(start, j + 1); }
  }
  throw new Error('unbalanced: ' + name);
}
const mod = new Function(grab('injectedMessageSignature') + '\n' + grab('isUnclaimedLocalBubble') + '\n' + grab('appendUniqueMessages') + '\nreturn { appendUniqueMessages, injectedMessageSignature };')();

const { appendUniqueMessages } = mod;
const bubble = (content, files) => ({ role: 'user', content, files, injected: true, injectId: 'local-inject-1', pending: true, _localInject: true });
const hist = (id, content, files) => ({ role: 'user', content, files, id });

test('optimistic bubble + history copy collapses into ONE', () => {
  const f = [{ path: '/a.png', name: 'a.png', size: 1 }];
  const out = appendUniqueMessages([bubble('看看这个', f)], [hist(310001, '看看这个', f)]);
  assert.equal(out.length, 1, JSON.stringify(out));
  assert.equal(out[0].id, 310001, 'history copy should fill the id back');
});

test('plain-text inject also collapses', () => {
  const out = appendUniqueMessages([bubble('纯文本注入')], [hist(42, '纯文本注入')]);
  assert.equal(out.length, 1);
});

test('two identical sends by the user are NOT swallowed', () => {
  const b1 = bubble('好的'); const b2 = { ...bubble('好的'), injectId: 'local-inject-2' };
  const out = appendUniqueMessages([b1, b2], [hist(1, '好的'), hist(2, '好的')]);
  assert.equal(out.length, 2, '签名只能认领，绝不能丢弃: ' + JSON.stringify(out.map(m => m.id)));
});

test('claimed bubble does not absorb a later distinct history message', () => {
  const claimed = { ...bubble('已认领'), _localInject: false, id: 7 };
  const out = appendUniqueMessages([claimed], [hist(8, '已认领')]);
  assert.equal(out.length, 2, 'different id = different message, must both show');
});

test('same-text different-attachments do not collapse', () => {
  const out = appendUniqueMessages(
    [bubble('看图', [{ path: '/x.png' }])],
    [hist(9, '看图', [{ path: '/y.png' }])],
  );
  assert.equal(out.length, 2, JSON.stringify(out));
});

test('no local bubble -> history appends normally', () => {
  const out = appendUniqueMessages([{ role: 'user', content: '老消息', id: 1 }], [hist(2, '新消息')]);
  assert.equal(out.length, 2);
});

test('repeated history delivery is idempotent by id', () => {
  const out = appendUniqueMessages([hist(1, 'a')], [hist(1, 'a'), hist(1, 'a')]);
  assert.equal(out.length, 1);
});

test('messages without id are never deduped away', () => {
  const out = appendUniqueMessages([{ role: 'assistant', content: 'x' }], [{ role: 'assistant', content: 'x' }]);
  assert.equal(out.length, 2);
});
