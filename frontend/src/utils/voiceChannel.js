function normalizedChannelPart(value) {
  return String(value || '').toLowerCase().replace(/[^a-z0-9]+/g, '');
}

export function voiceChannelForIdentity(identity) {
  if (identity && identity.authenticated === false) return null;
  const userId = Number(identity && identity.userId);
  if (!Number.isInteger(userId) || userId <= 0) return null;
  return {
    name: 'voice',
    kind: 'private',
    conversation_id: `user-${userId}`,
  };
}

export function voiceSessionName(channel) {
  const parts = [
    String(channel && channel.name || 'voice'),
    String(channel && channel.kind || ''),
    String(channel && channel.conversation_id || '').slice(0, 12),
  ]
    .map(normalizedChannelPart)
    .filter(Boolean);
  return parts.length ? parts.join('-') : 'voice-private-local';
}

export function voiceChannelDisplay(channel, sessionName = '') {
  return {
    label: String(channel && channel.name || '').toLowerCase() === 'voice'
      ? '语音 · 私聊'
      : '会话',
    internal: String(sessionName || ''),
  };
}
