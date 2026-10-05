const SHANGHAI_TIME_ZONE = 'Asia/Shanghai';

function partValue(parts, type) {
  const item = parts.find(part => part.type === type);
  return item ? item.value : '';
}

export function shanghaiDateKey(date = new Date()) {
  const parts = new Intl.DateTimeFormat('en-US', {
    timeZone: SHANGHAI_TIME_ZONE,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  }).formatToParts(date);
  return `${partValue(parts, 'year')}-${partValue(parts, 'month')}-${partValue(parts, 'day')}`;
}

export function dateKeyFromSessionName(name) {
  const value = String(name || '').trim();
  let match = value.match(/^(\d{4}-\d{2}-\d{2})(?:$|[-_])/);
  if (match) return match[1];
  match = value.match(/^chat-(\d{4})(\d{2})(\d{2})(?:-|$)/);
  if (match) return `${match[1]}-${match[2]}-${match[3]}`;
  return '';
}

export function sessionDateLabel(session) {
  const fromName = dateKeyFromSessionName(session && session.name);
  if (fromName) return fromName;
  const modified = String(session && session.modified || '').trim();
  if (modified) {
    // listSessions emits UTC mtime without an offset; parse it explicitly as
    // UTC before converting to the fixed Shanghai session date.
    const normalized = /^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/.test(modified)
      ? modified.replace(' ', 'T') + 'Z'
      : modified;
    const parsed = new Date(normalized);
    if (!Number.isNaN(parsed.getTime())) return shanghaiDateKey(parsed);
  }
  return modified.slice(0, 10) || String(session && session.name || '');
}

/**
 * 标注会话所属途径。本机路径（网页 chat 与语音 voice）视为同一条途径，不额外
 * 标注；外部 IM 通道各用自己的来源标签，避免与当日主会话混淆。
 *
 * 优先读会话自己的 `channel` 元数据（这是渠道会话携带的事实），名字前缀只作
 * 兜底：渠道会话升级成主会话级别之后叫 `qq-c2c-<id>` / `wechat-<id>`，旧的
 * `qq:c2c:` 前缀已经不出现了，靠名字猜会漏掉微信。
 */
export function sessionRouteLabel(session) {
  const channel = (session && session.channel) || null;
  const channelName = String(channel && channel.name || '').toLowerCase();
  const kind = String(channel && channel.kind || '').toLowerCase();
  if (channelName === 'qq') return kind === 'group' ? 'QQ · 群聊' : 'QQ · 私聊';
  if (channelName === 'wechat') return kind === 'group' ? '微信 · 群聊' : '微信 · 私聊';

  const name = String(session && session.name || '').trim();
  if (name.startsWith('qq:c2c:') || name.startsWith('qq-c2c-')) return 'QQ · 私聊';
  if (name.startsWith('qq:group:') || name.startsWith('qq-group-')) return 'QQ · 群聊';
  if (name.startsWith('wechat:') || name.startsWith('wechat-')) return '微信 · 私聊';
  return '';
}
