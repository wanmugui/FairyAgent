function normalizedChannelPart(value) {
  return String(value || '').toLowerCase().replace(/[^a-z0-9]+/g, '');
}

function channelSessionName(channel) {
  const parts = [
    String(channel && channel.name || 'chat'),
    String(channel && channel.kind || ''),
    String(channel && channel.conversation_id || '').slice(0, 12),
  ]
    .map(normalizedChannelPart)
    .filter(Boolean);
  return parts.length ? parts.join('-') : 'channel';
}

function isVoiceChannel(channel) {
  return String(channel && channel.name || '').trim().toLowerCase() === 'voice';
}

function normalizeVoiceChannelForUser(channel, userId) {
  if (!isVoiceChannel(channel)) return channel;
  const numeric = Number(userId);
  const conversationId = Number.isInteger(numeric) && numeric > 0
    ? `user-${numeric}`
    : 'local';
  return {
    name: 'voice',
    kind: 'private',
    conversation_id: conversationId,
  };
}

module.exports = {
  channelSessionName,
  isVoiceChannel,
  normalizeVoiceChannelForUser,
};
