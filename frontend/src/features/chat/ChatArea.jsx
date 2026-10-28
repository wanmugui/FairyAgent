import React, { useRef, useEffect, useLayoutEffect, useMemo, useState, useCallback } from 'react';
import MessageBubble, { SummaryCard, ThinkingBlock } from './MessageBubble';
import ToolActivity, { ToolProcessFold } from '../../components/ToolActivity';
import { planOrdinalMap } from '../../components/PlanCard';
import InteractionActionBar from './InteractionActionBar';
import { hasReportTag, hasSummaryTag, extractThinkingContent, fetchSessionAudio, stripThinkingTags } from '../../api/chat';
import { formatLLMProgress } from '../../utils/llmProgress';

const SESSION_SCROLL_POSITIONS = new Map();

function artifactsForMessage(msg, artifactsByMessage) {
  if (!msg || !artifactsByMessage) return null;
  const interactionId = String(msg.interaction_id || msg.turn_id || '').trim();
  if (interactionId && artifactsByMessage['i:' + interactionId]?.length) {
    return artifactsByMessage['i:' + interactionId];
  }
  return msg.id != null ? artifactsByMessage[String(msg.id)] : null;
}

// In one user interaction the agent may emit several <report> messages (e.g.
// an interim report then the final one). Keep only the last report per user
// turn; earlier report messages are suppressed so the UI shows one report.
function suppressDuplicateReports(messages) {
  const out = messages.map(m => ({ ...m, _suppressReport: false }));
  for (let i = 0; i < out.length; i++) {
    if (out[i].role === 'user') continue;
    if (out[i].role !== 'assistant' || !hasReportTag(out[i].content || '')) continue;
    // A report followed by more work is an interim result. Mark only that
    // report as suppressed; every later message still renders in its original
    // position so the turn keeps the tool/reply/tool/reply chronology.
    let laterReport = -1;
    let laterProcessWork = false;
    for (let j = i; j < out.length; j++) {
      if (out[j].role === 'user') break;
      if (j > i && out[j].role === 'assistant') {
        if (hasReportTag(out[j].content || '')) laterReport = j;
        if (Array.isArray(out[j].tool_calls) && out[j].tool_calls.length > 0) laterProcessWork = true;
      }
      if (j > i && out[j].role === 'tool') laterProcessWork = true;
    }
    if (laterReport > i || laterProcessWork) out[i]._suppressReport = true;
    i = laterReport > i ? laterReport - 1 : i;
  }
  return out;
}

function stripProcessText(content) {
  return String(content || '')
    .replace(/<summary[\s\S]*?<\/summary>\s*/gi, '')
    .replace(/<(?:think|thinking|mm:think)[\s\S]*?<\/(?:think|thinking|mm:think)>\s*/gi, '')
    .replace(/<report[\s\S]*?<\/report>\s*/gi, '')
    .replace(/<reflection[\s\S]*?<\/reflection>\s*/gi, '')
    .replace(/<[^>]+>/g, ' ')
    .replace(/\s+/g, ' ')
    .trim();
}

// A compressed-history handover is process state: it belongs in the tool fold,
// never in a chat bubble and never as a user turn.
function isSummaryMessage(msg) {
  if (!msg) return false;
  if (String(msg.internal_type || '') === 'context_summary') return true;
  return hasSummaryTag(String(msg.content || ''));
}

function hasRenderableAssistantContent(msg, artifactsByMessage) {
  if (!msg || msg.role !== 'assistant') return false;
  if (isSummaryMessage(msg)) return false;
  const raw = String(msg.content || '');
  // Tags mentioned inside <think> are analysis text, not protocol output.
  // Check protocol tags only after removing the thinking block so a literal
  // <report> in reasoning cannot create an empty assistant bubble.
  const withoutThinking = stripThinkingTags(raw);
  if (msg._suppressReport && hasReportTag(withoutThinking)) return false;
  if (hasReportTag(withoutThinking)) return true;
  return Boolean(stripProcessText(withoutThinking));
}

function shouldRenderProcessMessage(msg, artifactsByMessage) {
  if (!msg) return false;
  if (msg.role !== 'assistant') return true;
  return hasRenderableAssistantContent(msg, artifactsByMessage);
}

function turnGroupKey(group, index) {
  const first = group && group.messages && group.messages[0];
  const localIdentity = (group && group.user && group.user.id != null)
    ? 'user-' + group.user.id
    : (first && first.id != null)
      ? 'head-' + first.id
      : (first && first.ts != null ? 'ts-' + first.ts : '');
  return localIdentity ? 'turn-' + localIdentity : 'turn-index-' + index;
}

function pairProcessMessages(messages) {
  const out = [];
  let i = 0;
  while (i < messages.length) {
    const msg = messages[i];
    const calls = msg && msg.role === 'assistant' && Array.isArray(msg.tool_calls)
      ? msg.tool_calls
      : [];
    if (calls.length === 0) {
      out.push(msg);
      i += 1;
      continue;
    }

    const following = [];
    let j = i + 1;
    while (j < messages.length && messages[j] && messages[j].role === 'tool') {
      following.push({
        content: messages[j].content || '',
        name: messages[j].name || '',
        tool_call_id: messages[j].tool_call_id || '',
        error: messages[j].error || '',
        timestamp: messages[j].timestamp || messages[j].ts || 0,
      });
      j += 1;
    }
    const streamed = Array.isArray(msg._streamResults) ? msg._streamResults : [];
    const combined = [...following, ...streamed];
    const results = calls.map((call, index) => {
      const byId = call && call.id
        ? combined.find(result => result.tool_call_id === call.id)
        : null;
      return byId || following[index] || streamed[index] || undefined;
    });
    out.push({ ...msg, _pairedResults: results });
    i = j;
  }
  return out;
}

function TurnBlock({ group, action, mode, onOpenFile, artifactsByMessage, sessionName, onDeleteInteraction, onRegenerate, onOpenSubtask }) {
  const { user, messages } = group;
  const interactionKey = user && (user.interaction_id || user.turn_id || user.id || user.ts || 'turn');
  const processStateKey = `${sessionName || 'session'}:${interactionKey}`;
  const renderMessage = (msg, keyPrefix, extra = {}) => {
    const key = msg && msg.id != null
      ? keyPrefix + msg.id
      : keyPrefix + String(msg && (msg.ts || msg.content || '')).slice(0, 32);
    return (
      <MessageBubble
        key={key}
        msg={msg}
        mode={mode}
        onOpenFile={onOpenFile}
        onOpenSubtask={onOpenSubtask}
        artifacts={artifactsForMessage(msg, artifactsByMessage)}
        {...extra}
      />
    );
  };
  const timeline = pairProcessMessages(messages);
  const timelineParts = [];
  let toolChain = null;
  let toolChainIndex = 0;

  const ensureToolChain = () => {
    if (!toolChain) {
      toolChain = {
        index: toolChainIndex++,
        toolCalls: [],
        toolResults: [],
        summaryCount: 0,
        thinkingCount: 0,
        auxiliaryCount: 0,
        items: [],
        planRail: [],
        llmStats: { prompt_tokens: 0, completion_tokens: 0, duration_ms: 0 },
      };
    }
    return toolChain;
  };

  const flushToolChain = () => {
    if (!toolChain) return;
    const chain = toolChain;
    toolChain = null;
    timelineParts.push(
      <ToolProcessFold
        key={`tool-chain-${chain.index}`}
        toolCalls={chain.toolCalls}
        toolResults={chain.toolResults}
        llmStats={chain.llmStats}
        thinkingCount={chain.thinkingCount}
        summaryCount={chain.summaryCount}
        auxiliaryCount={chain.auxiliaryCount}
        orderedItems={chain.items.length ? chain.items : null}
        planRail={chain.planRail.length ? chain.planRail : null}
        onOpenSubtask={onOpenSubtask}
        onOpenFile={onOpenFile}
        stateKey={`${processStateKey}:chain:${chain.index}`}
      />
    );
  };

  const appendProcessMessage = msg => {
    const chain = ensureToolChain();
    const raw = String(msg.content || '');
    const thinkText = (msg.thinking && String(msg.thinking).trim())
      ? String(msg.thinking).trim()
      : extractThinkingContent(raw);
    if (thinkText && String(thinkText).trim()) {
      chain.thinkingCount += 1;
      chain.auxiliaryCount += 1;
      chain.items.push(
        <ThinkingBlock key={`thinking-${msg.id ?? chain.thinkingCount}`} text={thinkText} />
      );
    }
    if (isSummaryMessage(msg)) {
      chain.summaryCount += 1;
      chain.auxiliaryCount += 1;
      chain.items.push(
        <SummaryCard key={`summary-${msg.id ?? chain.summaryCount}`} content={raw} />
      );
    }
    const calls = Array.isArray(msg.tool_calls) ? msg.tool_calls : [];
    const results = Array.isArray(msg._pairedResults) ? msg._pairedResults : [];
    calls.forEach((call, index) => {
      const itemKey = (call && call.id) || `tool-${chain.index}-${index}`;
      chain.toolCalls.push({ ...call, _timestamp: msg.timestamp || msg.ts || 0 });
      chain.toolResults.push(results[index]);
      // 计划序号按整条链重算，同一 question 的多次 update 仍是同一张 PLAN 卡片。
      const planOrdinal = planOrdinalMap(chain.toolCalls, chain.toolResults).get(chain.toolCalls.length - 1) || null;
      const node = (
        <ToolActivity
          key={`plan-rail-${chain.planRail.length}-${itemKey}`}
          tc={call}
          result={results[index]}
          onOpenSubtask={onOpenSubtask}
          onOpenFile={onOpenFile}
          stateKey={`${processStateKey}:chain:${chain.index}:tool:${itemKey}`}
          planOrdinal={planOrdinal}
        />
      );
      if (call && call.function && call.function.name === 'plan') {
        chain.planRail.push(node);
        chain.items.push(
          React.cloneElement(node, {
            key: `${itemKey}:debug`,
            debugOnly: true,
            stateKey: `${processStateKey}:chain:${chain.index}:tool:${itemKey}:debug`,
          })
        );
      } else {
        chain.items.push(node);
      }
    });
    if (msg.usage) {
      chain.llmStats.prompt_tokens += Number(msg.usage.prompt_tokens) || 0;
      chain.llmStats.completion_tokens += Number(msg.usage.completion_tokens) || 0;
    }
    chain.llmStats.duration_ms += Number(msg.duration_ms) || 0;
  };

  timeline.forEach((msg, index) => {
    if (!msg) return;
    const raw = String(msg.content || '');
    const calls = msg.role === 'assistant' && Array.isArray(msg.tool_calls)
      ? msg.tool_calls
      : [];
    const thinkText = msg.role === 'assistant'
      ? ((msg.thinking && String(msg.thinking).trim()) ? String(msg.thinking).trim() : extractThinkingContent(raw))
      : '';
    // A message that only carries reasoning (no tool calls) still belongs to
    // the tool chain: the thinking has to be folded into the same disclosure
    // as the tool activity, otherwise it is rendered as a detached block
    // above the answer and never appears inside 思考×N.
    const thinkOnly = Boolean(thinkText && String(thinkText).trim());

    const hasProcessContent = msg.role === 'assistant'
      && (calls.length > 0 || thinkOnly || isSummaryMessage(msg));

    const isFinalAssistant = msg.role === 'assistant'
      && calls.length === 0
      && !isSummaryMessage(msg)
      && !thinkOnly
      && hasRenderableAssistantContent(msg, artifactsByMessage);

    if (isFinalAssistant) {
      flushToolChain();
      timelineParts.push(renderMessage(msg, `timeline-${index}-`, {
        suppressToolFold: true,
        stateKey: `${processStateKey}:msg:${msg.id ?? index}`,
      }));
      return;
    }

    if (hasProcessContent) {
      // Push the thinking / tool activity into the chain BEFORE flushing it.
      // The chain holds the reasoning, so it has to be rendered before the
      // answer bubble that follows it — reasoning comes first, then the reply.
      appendProcessMessage(msg);
      if (hasRenderableAssistantContent(msg, artifactsByMessage)) {
        flushToolChain();
        timelineParts.push(renderMessage(msg, `timeline-${index}-`, {
          suppressToolFold: true,
          suppressThinking: true,
          stateKey: `${processStateKey}:msg:${msg.id ?? index}`,
        }));
      }
      return;
    }

    if (!shouldRenderProcessMessage(msg, artifactsByMessage)) return;
    flushToolChain();
    timelineParts.push(renderMessage(msg, `timeline-${index}-`, {
      stateKey: `${processStateKey}:msg:${msg.id ?? index}`,
    }));
  });
  flushToolChain();

  return (
    <>
      {user ? renderMessage(user, 'user-') : null}
      {timelineParts}
      {action ? (
        <InteractionActionBar
          {...action}
          sessionName={sessionName}
          onDeleteInteraction={onDeleteInteraction}
          onRegenerate={onRegenerate}
        />
      ) : null}
    </>
  );
}

export default function ChatArea({
  messages,
  loading,
  llmProgress,
  mode,
  stats,
  sessionName,
  onOpenFile,
  artifactsByMessage,
  onFilesDropped,
  onDeleteInteraction,
  onRegenerate,
  onOpenSubtask,
  paging,
  loadOlder,
  loadingOlder,
  // locateTs: a timestamp (ms) to scroll to. It is set when the owner picks
  // an event in the trace panel, so the chat can show where that event
  // happened. The value is a pair so the same timestamp can be picked twice.
  locateTs,
}) {
  const scrollRef = useRef(null);
  const contentRef = useRef(null);
  const pinnedToBottomRef = useRef(true);
  const observedTopRef = useRef(0);
  const lastContentKeyRef = useRef('');
  const lastUserKeyRef = useRef('');
  const oldestOrdinalRef = useRef(null);
  const olderRequestPendingRef = useRef(false);
  const olderAnchorRef = useRef(null);
  const dragDepthRef = useRef(0);
  const [dragActive, setDragActive] = useState(false);
  const [audioMap, setAudioMap] = useState(null);
  const liveProgress = formatLLMProgress(llmProgress);

  const jumpToBottom = useCallback(() => {
    const container = scrollRef.current;
    if (!container) return;
    container.scrollTop = container.scrollHeight;
    observedTopRef.current = container.scrollTop;
    pinnedToBottomRef.current = true;
  }, []);

  useLayoutEffect(() => {
    const container = scrollRef.current;
    if (!container) return;
    const savedTop = SESSION_SCROLL_POSITIONS.get(sessionName);
    if (savedTop != null) {
      const floor = Math.max(0, container.scrollHeight - container.clientHeight);
      const nextTop = Math.max(0, Math.min(savedTop, floor));
      container.scrollTop = nextTop;
      observedTopRef.current = nextTop;
      pinnedToBottomRef.current = floor - nextTop <= 32;
      return;
    }
    pinnedToBottomRef.current = true;
    jumpToBottom();
  }, [sessionName, jumpToBottom]);

  useLayoutEffect(() => {
    const nextOldest = paging && paging.oldestOrdinal != null ? paging.oldestOrdinal : null;
    const prepended = olderAnchorRef.current
      && nextOldest != null
      && oldestOrdinalRef.current != null
      && nextOldest < oldestOrdinalRef.current;
    if (prepended) {
      const anchor = olderAnchorRef.current;
      olderAnchorRef.current = null;
      const container = scrollRef.current;
      const content = contentRef.current;
      if (container && content) {
        const row = Array.from(content.querySelectorAll('[data-chat-anchor-key]'))
          .find(node => node.dataset.chatAnchorKey === anchor.key);
        if (row) {
          const nextTop = container.scrollTop + (row.getBoundingClientRect().top - container.getBoundingClientRect().top) - anchor.top;
          container.scrollTop = nextTop;
          observedTopRef.current = nextTop;
          if (sessionName) SESSION_SCROLL_POSITIONS.set(sessionName, nextTop);
        }
      }
    }
    oldestOrdinalRef.current = nextOldest;

    const last = messages.length ? messages[messages.length - 1] : null;
    const lastUser = [...messages].reverse().find(m => m && m.role === 'user');
    const contentKey = last
      ? [
          last.id ?? last.turn_id ?? last.interaction_id ?? '',
          String(last.content || '').length,
          String(last.content || '').slice(-48),
          Array.isArray(last.tool_calls) ? last.tool_calls.length : 0,
          last.status || '',
        ].join('|')
      : '';
    const userKey = lastUser
      ? String(lastUser.id ?? lastUser.turn_id ?? lastUser.interaction_id ?? lastUser.ts ?? '')
      : '';
    const appendedUser = Boolean(userKey && userKey !== lastUserKeyRef.current);
    const contentMoved = Boolean(contentKey && contentKey !== lastContentKeyRef.current);
    lastContentKeyRef.current = contentKey;
    lastUserKeyRef.current = userKey;
    if (appendedUser) pinnedToBottomRef.current = true;
    if (pinnedToBottomRef.current && (appendedUser || contentMoved)) jumpToBottom();
  }, [messages, paging, jumpToBottom, sessionName]);

  useEffect(() => {
    if (!loadingOlder) {
      olderRequestPendingRef.current = false;
      olderAnchorRef.current = null;
    }
  }, [loadingOlder]);

  // 从 trace 面板跳回聊天：locateTs 是一次性的定位请求。
  //
  // 事件的时间落在两条消息之间是常态，所以取「时间不晚于该事件的最晚一条
  // 消息」，也就是事件发生时正在显示的那条回复。找不到时退而求其次取最早
  // 的一条，总比让主人对着一个没有反应的界面强。
  useEffect(() => {
    if (!locateTs) return;
    const target = Number(locateTs) || 0;
    if (!target) return;
    const container = scrollRef.current;
    const content = contentRef.current;
    if (!container || !content) return;

    const rows = Array.from(content.querySelectorAll('[data-chat-anchor-key]'));
    if (!rows.length) return;

    const pick = () => {
      let best = null;
      for (const row of rows) {
        const key = row.getAttribute('data-chat-anchor-key') || '';
        const ts = Number(String(key).replace(/^ts-/, '')) || 0;
        if (!ts) continue;
        if (ts <= target && (!best || ts > best.ts)) best = { row, ts };
      }
      if (!best) {
        // 事件早于所有已加载的消息：落到最早的一条。
        let earliest = null;
        for (const row of rows) {
          const ts = Number(String(row.getAttribute('data-chat-anchor-key') || '').replace(/^ts-/, '')) || 0;
          if (ts && (!earliest || ts < earliest.ts)) earliest = { row, ts };
        }
        best = earliest;
      }
      if (!best) return;
      pinnedToBottomRef.current = false;
      const top = best.row.getBoundingClientRect().top - container.getBoundingClientRect().top;
      container.scrollTo({ top: container.scrollTop + top - 12, behavior: 'smooth' });
      best.row.classList.add('chat-locate-flash');
      setTimeout(() => best.row.classList.remove('chat-locate-flash'), 1600);
    };

    // 消息还没渲染完时，等一帧再定位。
    const timer = setTimeout(pick, 60);
    return () => clearTimeout(timer);
  }, [locateTs, messages]);

  useEffect(() => {
    const content = contentRef.current;
    if (!content || typeof ResizeObserver === 'undefined') return undefined;
    const observer = new ResizeObserver(() => {
      if (pinnedToBottomRef.current) jumpToBottom();
    });
    observer.observe(content);
    return () => observer.disconnect();
  }, [sessionName, jumpToBottom]);

  const captureOlderAnchor = useCallback(() => {
    const container = scrollRef.current;
    const content = contentRef.current;
    if (!container || !content) return;
    const viewportTop = container.getBoundingClientRect().top;
    const row = Array.from(content.querySelectorAll('[data-chat-anchor-key]'))
      .find(node => node.getBoundingClientRect().bottom > viewportTop + 1);
    if (!row || !row.dataset.chatAnchorKey) return;
    olderAnchorRef.current = {
      key: row.dataset.chatAnchorKey,
      top: row.getBoundingClientRect().top - viewportTop,
    };
  }, []);

  const requestOlder = useCallback(() => {
    if (olderRequestPendingRef.current || loadingOlder) return;
    if (!paging || !paging.hasMoreOlder || !loadOlder) return;
    olderRequestPendingRef.current = true;
    captureOlderAnchor();
    loadOlder();
  }, [paging, loadingOlder, loadOlder, captureOlderAnchor]);

  // If one collapsed history page adds too little height, keep filling until
  // the reader has real scroll range or the server says there is no more.
  useEffect(() => {
    if (loadingOlder || !paging || !paging.hasMoreOlder || !loadOlder) return undefined;
    const frame = window.requestAnimationFrame(() => {
      const container = scrollRef.current;
      if (container && container.scrollTop <= 120) requestOlder();
    });
    return () => window.cancelAnimationFrame(frame);
  }, [loadingOlder, paging, loadOlder, requestOlder, messages.length]);

  const handleMessagesScroll = useCallback(event => {
    const container = event.currentTarget;
    const floor = Math.max(0, container.scrollHeight - container.clientHeight);
    const expectedTop = Math.min(observedTopRef.current, floor);
    const movedByReader = Math.abs(container.scrollTop - expectedTop) > 0.5;
    if (!movedByReader) return;
    if (sessionName) {
      const positions = SESSION_SCROLL_POSITIONS;
      positions.delete(sessionName);
      positions.set(sessionName, container.scrollTop);
      while (positions.size > 12) {
        positions.delete(positions.keys().next().value);
      }
    }
    pinnedToBottomRef.current = floor - container.scrollTop <= 32;
    observedTopRef.current = container.scrollTop;
    if (container.scrollTop <= 120) requestOlder();
  }, [requestOlder, sessionName]);

  // DSH binds the scrollport directly. React's synthetic onScroll is not
  // enough here because the transcript owns a native overflow container and
  // keyboard/wheel-driven scrolls must update paging ownership immediately.
  useEffect(() => {
    const container = scrollRef.current;
    if (!container) return undefined;
    const onScroll = () => handleMessagesScroll({ currentTarget: container });
    container.addEventListener('scroll', onScroll, { passive: true });
    return () => container.removeEventListener('scroll', onScroll);
  }, [handleMessagesScroll, sessionName]);

  // Load the per-message TTS audio map for this session so historical replies
  // show their replay icon too. Re-fetch when messages settle (a turn may have
  // just saved new audio while polling).
  useEffect(() => {
    let alive = true;
    if (!sessionName) { setAudioMap(null); return () => { alive = false; }; }
    fetchSessionAudio(sessionName).then(res => {
      if (alive) setAudioMap((res && res.audio) || {});
    }).catch(() => {});
    return () => { alive = false; };
  }, [sessionName]);

  useEffect(() => {
    if (!sessionName) return;
    const t = setTimeout(() => {
      fetchSessionAudio(sessionName).then(res => {
        setAudioMap((res && res.audio) || {});
      }).catch(() => {});
    }, 900);
    return () => clearTimeout(t);
  }, [messages, sessionName]);

  const displayMsgs = useMemo(() => {
    // Dedupe by message id: every session's production ids restart from 1, so
    // any foreign-session content that slips through would collide and render
    // as duplicate keys / mixed messages. Keep the first occurrence only.
    //
    // 但只按 id 去重不够：注入消息在实时态下**两条都还没有 id**（乐观气泡只有
    // injectId，injected_user 回显也只有 injectId），于是 `m.id != null` 全部落空，
    // 同一条消息会被渲染两次 —— 表现是「发送瞬间两条，轮次停止后恢复一条」。
    // 轮次一停，历史回填把真实 id 补上，按 id 的去重才重新生效，这正好解释了这个
    // 「实时两条 / 静止一条」的现象。
    //
    // 补救：id 缺失时退回 injectId，再退回「正文 + 附件」的内容签名。只认后出现的
    // 那条（它已被事件认领、内容更全），保留先出现那条的位置。
    const signatureOf = (m) => {
      if (!m || m.role !== 'user') return '';
      const text = String(m.content == null ? '' : m.content).trim();
      const paths = (Array.isArray(m.files) ? m.files : [])
        .map(f => String((f && f.path) || ''))
        .filter(Boolean)
        .sort()
        .join('|');
      if (!text && !paths) return '';
      return text + '' + paths;
    };
    const keysOf = (m) => {
      const keys = [];
      if (!m) return keys;
      if (m.id != null) keys.push('id:' + String(m.id));
      if (m.injectId != null) keys.push('inj:' + String(m.injectId));
      const sig = signatureOf(m);
      if (sig) keys.push('sig:' + sig);
      return keys;
    };
    const seen = new Set();
    const deduped = [];
    for (const m of messages) {
      const keys = keysOf(m);
      // 有 id 就走主键去重（跨会话 id 冲突的防线）；没有 id 才用 injectId + 签名，
      // 且签名只在「相邻的同类气泡」之间比对，避免用户隔很久连发相同内容被误合并。
      if (m && m.id != null) {
        const k = 'id:' + String(m.id);
        if (seen.has(k)) continue;
        seen.add(k);
        deduped.push(m);
        keys.forEach(key => seen.add(key));
        continue;
      }
      const softKeys = keys.filter(k => k.startsWith('inj:') || k.startsWith('sig:'));
      const last = deduped[deduped.length - 1];
      const lastKeys = last ? keysOf(last).filter(k => k.startsWith('inj:') || k.startsWith('sig:')) : [];
      const collides = softKeys.length > 0 && softKeys.some(k => lastKeys.includes(k));
      if (collides) {
        // 后一条是同一条消息的更完整副本，就地替换，位置保持不变。
        deduped[deduped.length - 1] = { ...last, ...m };
        continue;
      }
      softKeys.forEach(k => seen.add(k));
      deduped.push(m);
    }
    const cleaned = suppressDuplicateReports(deduped);
    if (audioMap) {
      return cleaned.map(m => {
        if (m && m.id != null && audioMap[String(m.id)]) {
          return { ...m, _ttsAudio: audioMap[String(m.id)] };
        }
        return m;
      });
    }
    return cleaned;
  }, [messages, audioMap]);

  // One interaction starts at a user message and ends at the last visible
  // message before the next user message. The action bar is rendered after
  // that boundary, matching the usual chat-product interaction controls.
  const turnGroups = useMemo(() => {
    const groups = [];
    let current = null;
    for (const msg of displayMsgs) {
      if (msg.role === 'user') {
        if (current) groups.push(current);
        current = { user: msg, messages: [] };
      } else if (current) {
        current.messages.push(msg);
      } else {
        current = { user: null, messages: [msg] };
      }
    }
    if (current) groups.push(current);
    return groups.map((group, index) => {
      const assistant = [...group.messages].reverse().find(m => m && m.role === 'assistant' && m.content && !m._suppressReport) || null;
      return {
        ...group,
        action: group.user ? {
          userMessage: group.user,
          assistantMessage: assistant,
          audio: assistant && assistant._ttsAudio ? assistant._ttsAudio : null,
          interactionId: group.user.interaction_id || group.user.turn_id || '',
          turnId: group.user.turn_id || '',
        } : null,
        isLast: index === groups.length - 1,
      };
    });
  }, [displayMsgs]);

  const isFileDrag = event => {
    const types = event.dataTransfer && event.dataTransfer.types;
    return !!types && Array.from(types).indexOf('Files') >= 0;
  };

  const handleDragEnter = event => {
    if (loading || !onFilesDropped || !isFileDrag(event)) return;
    event.preventDefault();
    dragDepthRef.current += 1;
    setDragActive(true);
  };

  const handleDragOver = event => {
    if (loading || !onFilesDropped || !isFileDrag(event)) return;
    event.preventDefault();
    if (event.dataTransfer) event.dataTransfer.dropEffect = 'copy';
  };

  const handleDragLeave = event => {
    if (!isFileDrag(event)) return;
    event.preventDefault();
    dragDepthRef.current = Math.max(0, dragDepthRef.current - 1);
    if (dragDepthRef.current === 0) setDragActive(false);
  };

  const handleDrop = event => {
    if (!onFilesDropped) return;
    const dropped = event.dataTransfer ? Array.from(event.dataTransfer.files || []) : [];
    if (!dropped.length) return;
    event.preventDefault();
    dragDepthRef.current = 0;
    setDragActive(false);
    if (loading) return;
    onFilesDropped(dropped);
  };

  return (
    <div
      className={'chat-area' + (dragActive ? ' drag-active' : '')}
      onDragEnter={handleDragEnter}
      onDragOver={handleDragOver}
      onDragLeave={handleDragLeave}
      onDrop={handleDrop}
    >
      <div
        ref={scrollRef}
        className="messages"
        id="messages"
      >
        <div ref={contentRef} className="messages-content">
          {loadingOlder ? (
            <div className="conversation-pad messages-history-loader">正在加载更早消息…</div>
          ) : null}
          {displayMsgs.length === 0 ? (
            <div className="conversation-pad"><div className="empty">输入消息开始对话</div></div>
          ) : turnGroups.map((group, index) => (
            <div
              className="turn-item"
              data-chat-anchor-key={turnGroupKey(group, index)}
              key={turnGroupKey(group, index)}
            >
              <TurnBlock
                group={group}
                action={!loading || index !== turnGroups.length - 1 ? group.action : null}
                mode={mode}
                onOpenFile={onOpenFile}
                artifactsByMessage={artifactsByMessage}
                sessionName={sessionName}
                onDeleteInteraction={onDeleteInteraction}
                onRegenerate={onRegenerate}
                onOpenSubtask={onOpenSubtask}
              />
            </div>
          ))}
          <div className="conversation-pad">
            {loading && (
              <div className="msg msg-assistant">
                <div className="bubble assistant-text"><em>思考中...</em></div>
                {liveProgress ? (
                  <div className="llm-progress-inline" title={liveProgress.title}>
                    <i className="llm-progress-pulse" aria-hidden="true" />
                    <span className="llm-progress-phase">{liveProgress.phase}</span>
                    <span className="llm-progress-meta">{liveProgress.detail}</span>
                  </div>
                ) : (
                  <div className="stats-thinking">计算中...</div>
                )}
              </div>
            )}
            {!loading && stats && stats.current && (
              <div className="stats-per-request">
                ↑ {stats.current.usage ? (stats.current.usage.prompt_tokens ?? '?') : '?'} in &nbsp;
                ↓ {stats.current.usage ? (stats.current.usage.completion_tokens ?? '?') : '?'} out &nbsp;
                ⏱ agent {stats.current.duration_ms ? (stats.current.duration_ms / 1000).toFixed(1) + 's' : '?'}
              </div>
            )}
          </div>
        </div>
      </div>
      {dragActive && (
        <div className="conversation-drop-overlay" aria-hidden="true">
          <span className="conversation-drop-icon">
            <svg viewBox="0 0 24 24">
              <path d="M12 4v11" />
              <path d="m8 8 4-4 4 4" />
              <path d="M5 14v4a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2v-4" />
            </svg>
          </span>
          <strong>释放文件</strong>
          <span>添加到下一条消息</span>
        </div>
      )}
    </div>
  );
}
