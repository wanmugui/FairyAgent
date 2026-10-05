import test from 'node:test';
import assert from 'node:assert/strict';
import { normalizeArtifactPath } from './reportArtifacts.js';

test('normalizes URL pathnames for Windows drive paths', () => {
  assert.equal(
    normalizeArtifactPath('/D:/Fairy/workspace/result/新疆旅游指南.pdf'),
    'D:/Fairy/workspace/result/新疆旅游指南.pdf',
  );
});

