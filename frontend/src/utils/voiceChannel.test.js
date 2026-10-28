import test from 'node:test';
import assert from 'node:assert/strict';

import {
  voiceChannelDisplay,
  voiceChannelForIdentity,
  voiceSessionName,
} from './voiceChannel.js';


test('authenticated voice identity maps to its own channel session', () => {
  const channel = voiceChannelForIdentity({ userId: 11, username: 'alice' });
  assert.deepEqual(channel, {
    name: 'voice',
    kind: 'private',
    conversation_id: 'user-11',
  });
  assert.equal(voiceSessionName(channel), 'voice-private-user11');
});


test('voice channel requires a verified numeric user id', () => {
  assert.equal(voiceChannelForIdentity({ authenticated: true }), null);
  assert.equal(voiceChannelForIdentity({ authenticated: false, userId: 1 }), null);
});


test('settings display uses a readable route label with the internal id below it', () => {
  assert.deepEqual(
    voiceChannelDisplay(
      { name: 'voice', kind: 'private', conversation_id: 'user-1' },
      'voice-private-user1',
    ),
    {
      label: '语音 · 私聊',
      internal: 'voice-private-user1',
    },
  );
});
