const test = require('node:test');
const assert = require('node:assert/strict');

const {
  channelSessionName,
  normalizeVoiceChannelForUser,
} = require('./channel_scope.cjs');


test('voice session names are stable and per-user', () => {
  assert.equal(
    channelSessionName({ name: 'voice', kind: 'private', conversation_id: 'user-1' }),
    'voice-private-user1',
  );
  assert.equal(
    channelSessionName({ name: 'voice', kind: 'private', conversation_id: 'user-1' }),
    channelSessionName({ name: 'voice', kind: 'private', conversation_id: 'user-1' }),
  );
});


test('voice channel ignores a forged conversation owner', () => {
  assert.deepEqual(
    normalizeVoiceChannelForUser(
      { name: 'voice', kind: 'private', conversation_id: 'user-2' },
      1,
    ),
    { name: 'voice', kind: 'private', conversation_id: 'user-1' },
  );
});


test('non-voice channels keep their existing naming contract', () => {
  assert.equal(
    channelSessionName({ name: 'wechat', kind: 'private', conversation_id: 'openid-A' }),
    'wechat-private-openida',
  );
});
