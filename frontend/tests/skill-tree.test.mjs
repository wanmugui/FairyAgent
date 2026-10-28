import { test } from 'node:test';
import assert from 'node:assert/strict';
import { buildSkillTree, UNCATEGORIZED } from '../src/skillTree.js';

test('核心技能提到第一层，且不在分类里重复', () => {
  const tree = buildSkillTree([
    { name: 'beta', core: true, category: '3D' },
    { name: 'alpha', category: '3D' },
  ]);
  assert.deepEqual(tree.core.map((s) => s.name), ['beta']);
  assert.deepEqual(tree.groups.map((g) => g.category), ['3D']);
  assert.deepEqual(tree.groups[0].skills.map((s) => s.name), ['alpha']);
});

test('未分类沉底，且一条都不丢', () => {
  const tree = buildSkillTree([
    { name: 'zeta' },
    { name: 'yankee', category: 'Alpha' },
    { name: 'xray', category: UNCATEGORIZED },
    { name: 'whiskey', core: true },
  ]);
  assert.deepEqual(
    tree.groups.map((g) => g.category),
    ['Alpha', UNCATEGORIZED],
  );
  const total =
    tree.core.length + tree.groups.reduce((n, g) => n + g.skills.length, 0);
  assert.equal(total, 4);
});

test('空列表与非法条目不会炸', () => {
  assert.deepEqual(buildSkillTree([]), { core: [], groups: [] });
  assert.deepEqual(buildSkillTree(null), { core: [], groups: [] });
  const tree = buildSkillTree([null, { name: 'ok', category: 'B' }]);
  assert.deepEqual(tree.groups.map((g) => g.category), ['B']);
});
