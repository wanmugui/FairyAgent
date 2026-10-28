// ── API helpers ──
import { readAutoFallback, readFallbackModel } from '../utils/modelPreference';

const BASE = '/api';

// 原始请求（不做任何缓存/去重），只被 dedupeRead 包裹着调用。
// ⚠️ 刻意与 fetchXxxFresh 分开：若让 Fresh 回调去调 fetchXxx()，而
// dedupeRead 是在 run() 返回后才登记 inflight，递归时看不到自己，会无限递归。
const rawFetchModels = async () => {
  const r = await fetch(BASE + '/models');
  return r.json();
};
const rawFetchSettings = async () => {
  const r = await fetch(BASE + '/settings');
  return r.json();
};

// Fresh 只绕过「结果缓存」，仍参与并发去重：绕开 in-flight 会让同一时刻
// 多个调用方各自发一份请求，那正是这次要去掉的重复。
export function fetchModelsFresh() {
  invalidateReadCache('models');
  return fetchModels();
}

export function fetchModels() {
  return dedupeRead('models', rawFetchModels);
}

export function fetchSettingsFresh() {
  invalidateReadCache('settings');
  return fetchSettings();
}

export function fetchSettings() {
  return dedupeRead('settings', rawFetchSettings);
}

export async function updateSettings(patch) {
  const r = await fetch(BASE + '/settings', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json; charset=utf-8' },
    body: JSON.stringify(patch || {}),
  });
  return r.json().catch(() => ({ ok: false, error: 'invalid json response' }));
}

// patch 可以是布尔（旧调用点直接传 enabled），也可以是
// { enabled?: boolean, params?: {key: value} } —— 后者用于修改工具自己的参数。
// 只发送调用方真正给到的键，避免把没打算改的字段写回后端。
export async function updateToolSetting(id, patch) {
  const body = typeof patch === 'boolean' || patch == null
    ? { id, enabled: patch }
    : {
      id,
      ...(Object.prototype.hasOwnProperty.call(patch, 'enabled') ? { enabled: patch.enabled } : {}),
      ...(Object.prototype.hasOwnProperty.call(patch, 'params') ? { params: patch.params } : {}),
    };
  const r = await fetch(BASE + '/settings/tool', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json; charset=utf-8' },
    body: JSON.stringify(body),
  });
  return r.json().catch(() => ({ ok: false, error: 'invalid json response' }));
}

export async function updateSkillSetting(name, enabled) {
  const r = await fetch(BASE + '/settings/skill', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json; charset=utf-8' },
    body: JSON.stringify({ name, enabled }),
  });
  return r.json().catch(() => ({ ok: false, error: 'invalid json response' }));
}

export async function addSkill(payload) {
  const r = await fetch(BASE + '/settings/skills', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json; charset=utf-8' },
    body: JSON.stringify(payload || {}),
  });
  return r.json().catch(() => ({ ok: false, error: 'invalid json response' }));
}

// Removes a skill registration and its directory on disk.
//
// The directory goes too, and that is the whole point: the registry re-scans
// the skill roots on every read, so a skill that is only unregistered comes
// straight back on the next render. Users read "delete" as "make it go away".
export async function deleteSkill(name) {
  const r = await fetch(BASE + '/settings/skill', {
    method: 'DELETE',
    headers: { 'Content-Type': 'application/json; charset=utf-8' },
    body: JSON.stringify({ name }),
  });
  return r.json().catch(() => ({ ok: false, error: 'invalid json response' }));
}

export function defaultModelPresets() {
  return [
    {
      id: 'deepseek',
      label: 'DeepSeek',
      hint: '深度求索',
      baseUrl: 'https://api.deepseek.com',
      modelPlaceholder: 'deepseek-chat',
      suggestDisplay: 'DeepSeek',
    },
    {
      id: 'openai',
      label: 'OpenAI',
      hint: 'GPT-4o / o1',
      baseUrl: 'https://api.openai.com/v1',
      modelPlaceholder: 'gpt-4o-mini',
      suggestDisplay: 'OpenAI',
    },
    {
      id: 'anthropic',
      label: 'Anthropic',
      hint: 'Claude 3.5 · 需兼容网关',
      baseUrl: '',
      modelPlaceholder: 'claude-3-5-sonnet-latest',
      suggestDisplay: 'Anthropic',
    },
    {
      id: 'minimax',
      label: 'MiniMax',
      hint: 'M3.1 Flash Preview · M3',
      baseUrl: 'https://api.minimaxi.com/v1',
      modelPlaceholder: 'MiniMax-M3.1-Flash-Preview',
      suggestDisplay: 'MiniMax M3.1 Flash',
    },
    {
      id: 'custom',
      label: '自定义',
      hint: '兼容 OpenAI 协议',
      baseUrl: '',
      modelPlaceholder: '',
      suggestDisplay: '',
    },
  ];
}

export async function testModel({ base_url, api_key, model }) {
  const r = await fetch(BASE + '/models/test', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json; charset=utf-8' },
    body: JSON.stringify({ base_url, api_key, model }),
  });
  return r.json().catch(() => ({ ok: false, error: 'invalid json response' }));
}

export async function listProviderModels({ base_url, api_key }) {
  const r = await fetch(BASE + '/models/list', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json; charset=utf-8' },
    body: JSON.stringify({ base_url, api_key }),
  });
  return r.json().catch(() => ({ ok: false, error: 'invalid json response' }));
}

export async function addModel(payload) {
  const r = await fetch(BASE + '/models', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json; charset=utf-8' },
    body: JSON.stringify(payload),
  });
  return r.json().catch(() => ({ ok: false, error: 'invalid json response' }));
}

// 改已存在模型的运行参数。推理强度按模型分别保存：空字符串表示不发送该字段。
export async function updateModel(id, patch) {
  const r = await fetch(BASE + '/models/' + encodeURIComponent(id), {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(patch || {}),
  });
  return r.json();
}

export async function removeModel(id) {
  const r = await fetch(BASE + '/models/' + encodeURIComponent(id), {
    method: 'DELETE',
  });
  return r.json().catch(() => ({ ok: false, error: 'invalid json response' }));
}

// 供应商注册表。供应商是「一组模型 + 一份凭据 + 一个上游协议」的单位。
// reveal=1 时后端才回传 API Key 明文，其余情况返回掩码值。

export async function fetchProviders({ reveal = false } = {}) {
  const r = await fetch(BASE + '/providers' + (reveal ? '?reveal=1' : ''));
  return r.json().catch(() => ({ providers: [], error: 'invalid json response' }));
}

export async function saveProvider(id, payload) {
  const r = await fetch(BASE + '/providers/' + encodeURIComponent(id), {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json; charset=utf-8' },
    body: JSON.stringify(payload || {}),
  });
  return r.json().catch(() => ({ ok: false, error: 'invalid json response' }));
}

export async function createProvider(payload) {
  const r = await fetch(BASE + '/providers', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json; charset=utf-8' },
    body: JSON.stringify(payload || {}),
  });
  return r.json().catch(() => ({ ok: false, error: 'invalid json response' }));
}

export async function removeProvider(id) {
  const r = await fetch(BASE + '/providers/' + encodeURIComponent(id), {
    method: 'DELETE',
  });
  return r.json().catch(() => ({ ok: false, error: 'invalid json response' }));
}

// 拉取该供应商 API 服务下的可用模型。key 由后端从注册表取，前端不传明文。
export async function listProviderAvailableModels(providerId) {
  const r = await fetch(BASE + '/providers/' + encodeURIComponent(providerId) + '/models', {
    method: 'POST',
  });
  return r.json().catch(() => ({ ok: false, error: 'invalid json response' }));
}

// 把拉取到的模型落到供应商上：后端把 models 合并进 provider.models（去重保留已有），
// 重新派生 config.models 并返回最终模型列表。
export async function commitProviderModels(providerId, models) {
  const r = await fetch(
    BASE + '/providers/' + encodeURIComponent(providerId) + '/models?commit=1',
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json; charset=utf-8' },
      body: JSON.stringify({ models: Array.isArray(models) ? models : [] }),
    }
  );
  return r.json().catch(() => ({ ok: false, error: 'invalid json response' }));
}

const rawFetchSessions = async () => {
  const r = await fetch(BASE + '/sessions');
  return r.json();
};

// Fresh 只绕过「结果缓存」，仍参与并发去重：绕开 in-flight 会让同一时刻
// 多个调用方各自发一份请求，那正是这次要去掉的重复。
export function fetchSessionsFresh() {
  invalidateReadCache('sessions');
  return fetchSessions();
}

export function fetchSessions() {
  return dedupeRead('sessions', rawFetchSessions);
}

export async function fetchSessionMessages(name, opts) {
  // `all` loads the complete transcript, matching the reference agent UI.
  // `afterOrdinal` remains for lightweight live-tail polling; beforeOrdinal is
  // retained for future explicitly requested history loading.
  const params = new URLSearchParams();
  if (opts && opts.limit != null) params.set('limit', String(opts.limit));
  if (opts && opts.all) params.set('all', '1');
  if (opts && opts.beforeOrdinal != null) params.set('before_ordinal', String(opts.beforeOrdinal));
  if (opts && opts.afterOrdinal != null) params.set('after_ordinal', String(opts.afterOrdinal));
  const qs = params.toString();
  const url = BASE + '/sessions/' + encodeURIComponent(name) + '/messages' + (qs ? '?' + qs : '');
  const r = await fetch(url);
  return r.json();
}

// ── 读接口的去重层 ──
//
// models / sessions / settings 是纯读接口，首屏却被 App、InputBar、SettingsMenu、
// VoiceDock 同时拉起，每个往返 400~1200ms。去重必须放在这里（api 层）而不是
// 某个组件的局部变量里 —— 放进 App.jsx 时 InputBar 和 SettingsMenu 的调用完全
// 绕过了它，实测首屏 /api/models 仍是 3 次。
//
// 规则：
//   - in-flight：同一 key 飞行中直接复用同一个 Promise；
//   - 2 秒 TTL：首屏那一串请求彼此相隔 1ms~700ms，只压并发压不掉，需要短缓存
//     把它们收敛成一次；
//   - 失败不写缓存：一次抖动不会被缓存住；
//   - 需要强制拿新数据时用 fetchModelsFresh / fetchSessionsFresh /
//     fetchSettingsFresh 显式绕过（用户点「刷新」走的就是这条）。
const REQUEST_TTL_MS = 2000;
const inflightReads = new Map();
const readCache = new Map();

function dedupeRead(key, run) {
  const hit = readCache.get(key);
  if (hit && Date.now() - hit.at < REQUEST_TTL_MS) return hit.promise;
  const existing = inflightReads.get(key);
  if (existing) return existing;
  const p = run()
    .then(value => {
      readCache.set(key, { at: Date.now(), promise: Promise.resolve(value) });
      return value;
    })
    .catch(err => {
      readCache.delete(key);
      throw err;
    })
    .finally(() => {
      if (inflightReads.get(key) === p) inflightReads.delete(key);
    });
  inflightReads.set(key, p);
  return p;
}

export function invalidateReadCache(key) {
  if (key) readCache.delete(key); else readCache.clear();
}

export function fetchSessionTrace(name, opts) {
  const q = opts && opts.full ? '?full=1' : '';
  const r = fetch(BASE + '/sessions/' + encodeURIComponent(name) + '/trace' + q);
  return r.then(res => res.json());
}

export async function createSession(name, options = {}) {
  const body = { name, ...options };
  const r = await fetch(BASE + '/sessions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json; charset=utf-8' },
    body: JSON.stringify(body),
  });
  return r.json();
}

// 便捷封装：在指定父会话下创建分支子会话。
//   - parent: 主会话名（必填，例如今日 YYYY-MM-DD）
//   - domain: 分支用途描述（可选，用于侧边栏显示）
// 服务端会在 parent 名下生成唯一的分支名；这里返回 { ok, name, parent_session, domain }
export async function createBranchSession(parent, domain = '', extra = {}) {
  return createSession('', {
    parent_session: parent,
    domain,
    kind: 'branch',
    created_by: extra.created_by || 'user',
    ...extra,
  });
}

// 删除分支会话：服务端只接受分支会话（daily 主会话 lifecycle 管，会拒绝）。
// 失败抛错，成功返回 { ok: true, name }。
export async function deleteSession(name) {
  const r = await fetch(BASE + '/sessions/' + encodeURIComponent(name), {
    method: 'DELETE',
  });
  const payload = await r.json().catch(() => ({}));
  if (!r.ok || payload.ok === false) {
    throw new Error(payload.error || ('delete session failed: ' + r.status));
  }
  return payload;
}

export async function deleteMemoryInteraction(interactionId, scope = 'memory', context = {}) {
  const r = await fetch(
    BASE + '/memory/interactions/' + encodeURIComponent(interactionId),
    {
      method: 'DELETE',
      headers: { 'Content-Type': 'application/json; charset=utf-8' },
      body: JSON.stringify({
        scope,
        session: context.session || '',
        turn_id: context.turnId || '',
      }),
    },
  );
  const payload = await r.json().catch(() => ({}));
  if (!r.ok || payload.ok === false) {
    throw new Error(payload.error || ('delete memory interaction failed: ' + r.status));
  }
  return payload;
}

export async function fetchSessionAudio(name) {
  const r = await fetch(BASE + '/sessions/' + encodeURIComponent(name) + '/audio');
  return r.json();
}

export async function saveSessionAudio(name, messageId, wavB64) {
  const r = await fetch(BASE + '/sessions/' + encodeURIComponent(name) + '/audio', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json; charset=utf-8' },
    body: JSON.stringify({ message_id: messageId, wav_b64: wavB64 }),
  });
  return r.json();
}

export async function sendChat(message, model, session, signal, files, surface, channel = null) {
  const autoFallback = readAutoFallback();
  const fallbackModel = autoFallback ? readFallbackModel() : '';
  const r = await fetch(BASE + '/chat', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json; charset=utf-8' },
    body: JSON.stringify({
      message, model, session, stream: true,
      files: Array.isArray(files) ? files : [],
      surface: surface || 'chat',
      ...(channel ? { channel } : {}),
      fallback_model: fallbackModel,
      auto_fallback: autoFallback,
    }),
    signal,
  });
  return r;
}

// Upload one file into <repo>/workspace/upload/<session>/ through the
// filemanager route and return its real absolute path.
// Returns { ok, name, size, path }.
export async function uploadFile(file, session) {
  // The upload route resolves repo-relative paths, so keep the session
  // segment free of separators and dot-only names.
  const sess = String(session || '').trim()
    .replace(/[^A-Za-z0-9._-]/g, '_')
    .replace(/^\.+$/, '') || 'default';
  const dirRel = 'workspace/upload/' + sess;
  // The upload route only writes into an existing directory; create it
  // first (the folder route is idempotent).
  const mk = await fetch(BASE + '/folder', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json; charset=utf-8' },
    body: JSON.stringify({ path: 'workspace/upload', name: sess }),
  });
  if (!mk.ok) {
    const payload = await mk.json().catch(() => ({}));
    throw new Error(payload.error || ('create upload folder failed: ' + mk.status));
  }
  const url = BASE + '/upload?path=' + encodeURIComponent(dirRel)
    + '&name=' + encodeURIComponent(file.name || 'upload.bin') + '&unique=1';
  const r = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/octet-stream' },
    body: file,
  });
  const payload = await r.json().catch(() => ({}));
  if (!r.ok || payload.ok === false) {
    throw new Error(payload.error || ('upload failed: ' + r.status));
  }
  const name = file.name || String(payload.path || '').split('/').pop() || 'upload.bin';
  return { ok: true, name, size: payload.size, path: payload.path };
}

// ── Normalize production format → flat messages ──
function parseToolCalls(value) {
  if (!value) return [];
  if (Array.isArray(value)) return value;
  try { return JSON.parse(value); } catch { return []; }
}

const INTERNAL_CONTROL_TYPES = new Set([
  'auto_continue',
  'delivered_status',
  'auto_answer',
]);

function isInternalControl(value) {
  const internalType = String((value && value.internal_type) || '');
  if (INTERNAL_CONTROL_TYPES.has(internalType)) return true;
  return String((value && value.content) || '').trim().startsWith('[会话状态]');
}

const CONTEXT_SUMMARY_TYPE = 'context_summary';

// The agent keeps its compressed-history handover as a user-role message with
// internal_type=context_summary. It is process state, not something the user
// typed, so it must join the tool/thinking fold instead of opening a new user
// turn. Mirrors the server-side production-format conversion.
export function isContextSummaryMessage(value) {
  if (!value) return false;
  if (String(value.internal_type || '') === CONTEXT_SUMMARY_TYPE) return true;
  return /^\s*<summary\b/i.test(String(value.content || ''));
}

function asContextSummaryMessage(message, block) {
  const text = String((block && block.content) || message.content || '');
  return {
    role: 'assistant',
    content: text,
    tool_calls: [],
    _pairedResults: [],
    is_final: false,
    id: (block && block.id) != null ? block.id : message.id,
    timestamp: (block && block.timestamp) || message.timestamp,
    internal_type: CONTEXT_SUMMARY_TYPE,
    active_agent: (block && block.active_agent) || message.active_agent,
    turn_id: message.turn_id,
    interaction_id: message.interaction_id || message.turn_id,
    session_id: message.session_id,
    message_uuid: message.message_uuid,
    display_tag: message.display_tag,
  };
}

// Split a user message into its plain text and the <file_context> attachments
// the server prepended for the model.
export function splitFileContext(content) {
  const raw = String(content || '');
  const match = raw.match(/<file_context>([\s\S]*?)<\/file_context>\n?/);
  if (!match) return { text: raw, files: [] };
  let files = [];
  try {
    const parsed = JSON.parse(match[1]);
    if (Array.isArray(parsed)) {
      files = parsed.filter(f => f && typeof f.path === 'string');
    }
  } catch { files = []; }
  const text = (raw.slice(0, match.index) + raw.slice(match.index + match[0].length)).trim();
  return { text, files };
}

// 注入回显专用。抽出来不是为了好看：这条链路原先只藏在 App.jsx 深处，
// 坏了没人知道（注入的图片会变成一坨原始 JSON 显示在气泡里）。
// 历史消息那侧 normalizeServiceMessages 早就在做同样的拆分，
// 唯独 injected_user 漏了——现在两边共用同一个约定，也共用这一个函数。
export function toInjectedUserMessage(content, extra) {
  const { text, files } = splitFileContext(content || '');
  return { role: 'user', content: text, files, ...(extra || {}) };
}

export function normalizeServiceMessages(payload) {
  const source = Array.isArray(payload) ? payload :
    (payload && payload.data && Array.isArray(payload.data.messages)) ? payload.data.messages :
    (payload && Array.isArray(payload.messages)) ? payload.messages : [];
  const output = [];
  const seenUserInteractions = new Set();

  for (const message of source) {
    const interactionId = String(message.interaction_id || message.turn_id || '');
    if (!Array.isArray(message.contents)) {
      if (message.role === 'user') {
        if (isInternalControl(message)) continue;
        if (String(message.content || '').trim() === '请继续。' && interactionId && seenUserInteractions.has(interactionId)) continue;
        if (interactionId) seenUserInteractions.add(interactionId);
        if (isContextSummaryMessage(message)) {
          output.push(asContextSummaryMessage(message, null));
          continue;
        }
      }
      const copy = { ...message };
      if (copy.tool_calls) copy.tool_calls = parseToolCalls(copy.tool_calls);
      output.push(copy);
      continue;
    }

    const blocks = message.contents;
    if (message.role !== 'assistant') {
      let markedUserInteraction = false;
      for (const block of blocks) {
        if (block.type === 'text') {
          if (message.role === 'user') {
            if (isInternalControl({ internal_type: block.internal_type, content: block.content })) continue;
            if (String(block.content || '').trim() === '请继续。' && interactionId && seenUserInteractions.has(interactionId)) continue;
            if (interactionId && !markedUserInteraction) {
              seenUserInteractions.add(interactionId);
              markedUserInteraction = true;
            }
            if (isContextSummaryMessage({ internal_type: block.internal_type, content: block.content })) {
              output.push(asContextSummaryMessage(message, block));
              continue;
            }
          }
          const split = message.role === 'user'
            ? splitFileContext(block.content || '')
            : { text: block.content || '', files: [] };
          output.push({
            role: message.role,
            content: split.text,
            files: split.files,
            id: block.id, timestamp: block.timestamp,
            internal_type: block.internal_type,
            active_agent: block.active_agent,
            turn_id: message.turn_id,
            interaction_id: message.interaction_id || message.turn_id,
            session_id: message.session_id,
            message_uuid: message.message_uuid,
            display_tag: message.display_tag
          });
        }
      }
      continue;
    }

    let i = 0;
    while (i < blocks.length) {
      const block = blocks[i];
      if (block.type === 'text') {
        const calls = [];
        const results = [];
        let j = i + 1;
        while (j < blocks.length && blocks[j].type !== 'text') {
          if (blocks[j].type === 'tool_calls') calls.push(...parseToolCalls(blocks[j].tool_calls));
          if (blocks[j].type === 'tool_result') results.push(blocks[j]);
          j++;
        }
        output.push({
          role: 'assistant',
          content: block.content || '',
          tool_calls: calls,
          _pairedResults: results.map(r => ({
            tool_call_id: r.tool_call_id || '',
            name: r.name || '',
            content: r.content || '',
            timestamp: r.timestamp || r.ts || 0,
          })),
          thinking: message.thinking || '',
          is_final: calls.length === 0,
          id: block.id, timestamp: block.timestamp,
          internal_type: block.internal_type,
          active_agent: block.active_agent,
          turn_id: message.turn_id,
          interaction_id: message.interaction_id || message.turn_id,
          session_id: message.session_id,
          message_uuid: message.message_uuid,
          display_tag: message.display_tag,
          usage: message.usage,
          duration_ms: message.duration_ms,
          real_ms: message.real_ms,
        });
        for (const result of results) {
          output.push({
            role: 'tool',
            content: result.content || '',
            tool_call_id: result.tool_call_id || '',
            name: result.name || '',
            _suppress: true, // already shown via assistant._pairedResults
            id: result.id, timestamp: result.timestamp,
            internal_type: result.internal_type,
            active_agent: result.active_agent,
            interaction_id: message.interaction_id || message.turn_id,
            session_id: message.session_id,
          });
        }
        i = j;
        continue;
      }

      if (block.type === 'tool_calls') {
        const calls = [];
        const results = [];
        let j = i;
        while (j < blocks.length && blocks[j].type !== 'text') {
          if (blocks[j].type === 'tool_calls') calls.push(...parseToolCalls(blocks[j].tool_calls));
          if (blocks[j].type === 'tool_result') results.push(blocks[j]);
          j++;
        }
        if (calls.length) {
          output.push({
            role: 'assistant',
            content: '',
            tool_calls: calls,
          _pairedResults: results.map(r => ({
            tool_call_id: r.tool_call_id || '',
            name: r.name || '',
            content: r.content || '',
            timestamp: r.timestamp || r.ts || 0,
          })),
            thinking: message.thinking || '',
            is_final: false,
            id: blocks[i] && blocks[i].id, timestamp: blocks[i] && blocks[i].timestamp,
            usage: message.usage,
            duration_ms: message.duration_ms,
            real_ms: message.real_ms,
            interaction_id: message.interaction_id || message.turn_id,
            session_id: message.session_id,
          });

        }
        for (const result of results) {
          output.push({
            role: 'tool',
            content: result.content || '',
            tool_call_id: result.tool_call_id || '',
            name: result.name || '',
            _suppress: true, // already shown via assistant._pairedResults
            interaction_id: message.interaction_id || message.turn_id,
            session_id: message.session_id,
          });
        }
        i = j;
        continue;
      }
      i++;
    }
  }
  return output;
}

// ── Content extraction helpers ──
export function extractThinkingContent(text) {
  if (!text) return '';
  const blocks = [];
  const re = /<(?:think|thinking|mm:think)(?:\s[^>]*)?>([\s\S]*?)<\/(?:think|thinking|mm:think)>/gi;
  let m;
  while ((m = re.exec(String(text)))) {
    const t = (m[1] || '').trim();
    if (t) blocks.push(t);
  }
  return blocks.join('\n\n');
}

export function stripThinkingTags(text) {
  return String(text || '').replace(/<(?:think|thinking|mm:think)(?:\s[^>]*)?>[\s\S]*?<\/(?:think|thinking|mm:think)>/gi, '');
}

// Reflection existed in older builds. Keep a private sanitizer so archived
// conversations never expose those internal blocks as ordinary reply text.
function stripLegacyReflectionTags(text) {
  return String(text || '').replace(/<reflection(?:\s[^>]*)?>[\s\S]*?<\/reflection>/gi, '');
}

function stripLegacyReflectionMarkdown(text) {
  return String(text || '').replace(/\n*#{2,4}[ \t]*反思[^\n]*\n[\s\S]*$/i, '');
}

export function extractAssistantContent(text) {
  if (!text) return text || '';
  text = stripThinkingTags(text);
  // Legacy reflection blocks are internal self-check content.
  text = stripLegacyReflectionTags(text);
  text = stripLegacyReflectionMarkdown(text);
  // Drop leftover markdown code fences that contain only //-comment lines and
  // whitespace (remnants of inline pseudo tool calls such as
  // ```typescript\n// 我正在读取...\n```) so they do not render as raw text.
  text = text.replace(/(```[a-zA-Z0-9_+-]*\r?\n(?:[ \t]*\/\/[^\r\n]*\r?\n|[ \t]*\r?\n)*[ \t]*```)/g, '');
  const report = text.match(/<report(?:\s[^>]*)?>([\s\S]*?)<\/report>/i);
  if (report) return report[1].trim();
  return text;
}

export function hasReportTag(text) {
  return /<report(?:\s[^>]*)?>/i.test(text);
}

export function parsePptTaskFinished(text) {
  const block = String(text || '').match(/<ppt_task_finished(?:\s[^>]*)?>([\s\S]*?)<\/ppt_task_finished>/i);
  if (!block) return null;
  const read = name => {
    const match = block[1].match(new RegExp('<' + name + '(?:\\s[^>]*)?>([\\s\\S]*?)<\\/' + name + '>', 'i'));
    return match ? match[1].trim() : '';
  };
  const rawDeckDir = read('deck_dir').replace(/\\/g, '/').trim();
  const logicalMatch = rawDeckDir.match(/^\/mnt\/data\/result\/(pptid_[A-Za-z0-9._-]+)\/?$/i);
  const localMatch = rawDeckDir.match(/^[A-Za-z]:\/.*\/workspace\/result\/(pptid_[A-Za-z0-9._-]+)\/?$/i);
  const deckId = (logicalMatch && logicalMatch[1]) || (localMatch && localMatch[1]);
  if (!deckId) return null;
  return {
    path: '/mnt/data/result/' + deckId,
    status: read('status'),
    failedPages: read('failed_pages'),
    reason: read('reason'),
  };
}

export function hasSummaryTag(text) {
  return /<summary\b/i.test(text || '');
}

// Extract readable text from a <summary> message. Handles nested
// <summary><summary> wrapping plus inner tags like <key_knowledge> /
// <recent_actions>: converts block tags into headings and strips the rest.
export function extractSummaryContent(text) {
  if (!text) return '';
  let out = stripThinkingTags(String(text));
  // Unwrap all summary layers.
  out = out.replace(/<\/?summary[^>]*>/gi, '');
  // Promote known inner sections to readable headings.
  out = out
    .replace(/<\/?key_knowledge\s*>/gi, (m) => (m.indexOf('/') >= 0 ? '\n' : '\n## 关键知识\n'))
    .replace(/<\/?recent_actions\s*>/gi, (m) => (m.indexOf('/') >= 0 ? '\n' : '\n## 最近操作\n'))
    .replace(/<\/?primary[^>]*>/gi, (m) => (m.indexOf('/') >= 0 ? '\n' : '\n### 主要需求\n'))
    .replace(/<\/?hard[^>]*>/gi, (m) => (m.indexOf('/') >= 0 ? '\n' : '\n### 硬性约束\n'))
    .replace(/<\/?pending[^>]*>/gi, (m) => (m.indexOf('/') >= 0 ? '\n' : '\n### 计划\n'))
    .replace(/<\/?current[^>]*>/gi, (m) => (m.indexOf('/') >= 0 ? '\n' : '\n### 当前进度\n'))
    .replace(/<\/?optional[^>]*>/gi, (m) => (m.indexOf('/') >= 0 ? '\n' : '\n### 可选下一步\n'))
    .replace(/<\/?user_directives[^>]*>/gi, (m) => (m.indexOf('/') >= 0 ? '\n' : '\n### 用户指示\n'));
  // Strip any remaining XML tags, collapse blank lines.
  out = out.replace(/<[^>]+>/g, ' ').replace(/[ \t]+/g, ' ').replace(/\n{3,}/g, '\n\n').trim();
  return out;
}
