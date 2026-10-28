import test from 'node:test';
import assert from 'node:assert/strict';
import { dateKeyFromSessionName, sessionDateLabel, shanghaiDateKey } from './sessionDate.js';

test('shanghaiDateKey uses Asia/Shanghai across midnight', () => {
  assert.equal(shanghaiDateKey(new Date('2026-09-17T15:59:59Z')), '2026-09-17');
  assert.equal(shanghaiDateKey(new Date('2026-09-17T16:00:00Z')), '2026-09-18');
});

test('dateKeyFromSessionName supports daily and legacy chat names', () => {
  assert.equal(dateKeyFromSessionName('2026-09-18'), '2026-09-18');
  assert.equal(dateKeyFromSessionName('2026-09-18-work'), '2026-09-18');
  assert.equal(dateKeyFromSessionName('chat-20260918-083000-1'), '2026-09-18');
});

test('sessionDateLabel falls back to Shanghai date for legacy names', () => {
  assert.equal(sessionDateLabel({ name: 'legacy-session', modified: '2026-09-17 16:30:00' }), '2026-09-18');
});
