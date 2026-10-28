import test from 'node:test';
import assert from 'node:assert/strict';

import { sessionRouteLabel } from './sessionDate.js';


test('voice channel sessions carry their own route label', () => {
  assert.equal(
    sessionRouteLabel({
      name: 'voice-private-user1',
      channel: { name: 'voice', kind: 'private', conversation_id: 'user-1' },
    }),
    '语音 · 私聊',
  );
});
