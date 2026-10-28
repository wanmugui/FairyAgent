// 回归测试：trace 时间线分页的页码序列。
//
// 时间线可能上千条，一次性铺开既慢又难读，因此按页展示。
// 页码需要在数量多时折叠成「1 2 3 4 ... N」，避免页码按钮铺满整行。
//
// 本测试从 TracePanel.jsx 抽出页码计算逻辑的原样实现来跑，
// 不重写一份模拟实现，保证测的是真实代码路径。

import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';
import { test } from 'node:test';

const src = readFileSync('src/components/TracePanel.jsx', 'utf8');

const start = src.indexOf('function TracePager(');
assert.ok(start > 0, 'TracePager not found');

const bodyStart = src.indexOf('{', src.indexOf('{', start) + 1);
assert.ok(bodyStart > start, 'TracePager body not found');

// 抽出 pages 的构造逻辑（在 return 之前）
// 只取纯逻辑部分：从 `const pages = []` 到 `pages.sort(...)` 结束，
// 避开后面的 JSX（里面含 pages.map，直接 eval 会语法报错）。
const logicStart = src.indexOf('const pages = [];', bodyStart);
const logicEnd = src.indexOf('pages.sort', bodyStart);
assert.ok(logicStart > 0 && logicEnd > logicStart, 'pages computation not found');
const pagesBlock = src.slice(logicStart, src.indexOf(';', src.indexOf('(', logicEnd)) + 1);

const computePages = new Function('page', 'totalPages', `
  ${pagesBlock}
  return pages;
`);

test('页数很少时列出全部页码', () => {
  assert.deepEqual(computePages(1, 5), [1, 2, 3, 4, 5]);
  assert.deepEqual(computePages(3, 5), [1, 2, 3, 4, 5]);
});

test('页数多时折叠成 1 2 3 4 ... N', () => {
  const pages = computePages(1, 224);
  assert.equal(pages[0], 1, '首页必须是 1');
  assert.equal(pages[pages.length - 1], 224, '末页必须是总页数');
  assert.ok(pages.length <= 9, `页码数量应被折叠，实际 ${pages.length}`);
  assert.ok(pages.includes(2), '应保留第 2 页');
});

test('位于中间时页码连续', () => {
  const pages = computePages(50, 224);
  assert.equal(pages[0], 1);
  assert.equal(pages[pages.length - 1], 224);
  // 49 / 50 / 51 应连续出现
  assert.ok(pages.includes(49) && pages.includes(50) && pages.includes(51));
});

test('页码始终升序且不重复', () => {
  for (const p of [1, 2, 4, 5, 50, 220, 223, 224]) {
    const pages = computePages(p, 224);
    assert.deepEqual(pages, [...pages].sort((a, b) => a - b), `page=${p} 未升序`);
    assert.equal(new Set(pages).size, pages.length, `page=${p} 有重复页码`);
  }
});

test('单页时只返回第 1 页', () => {
  assert.deepEqual(computePages(1, 1), [1]);
});
