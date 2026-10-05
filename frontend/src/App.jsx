// TTS_ENABLED: streaming TTS playback on the main page. Streamed reply text
// is split into sentences and synthesized via /voice/api/tts (local voice
// service on :8787). Set false to disable all TTS calls.
const TTS_ENABLED = true;
const MESSAGE_PAGE_LIMIT = 100;

import React, { useState, useEffect, useRef, useCallback, useMemo } from 'react';
import { createPortal } from 'react-dom';
import Sidebar from './components/Sidebar';
import FilePreviewPanel from './components/FilePreviewPanel';
import SettingsMenu from './components/SettingsMenu';
import TimelineEditor from './features/timeline/TimelineEditor';
import AmbientBackdrop from './components/AmbientBackdrop';
import ChatArea from './features/chat/ChatArea';
import { SubtaskStreamContext } from './components/ToolBlock';
import { splitSentences, cleanTTSText, buildTTSChunks } from './utils/ttsText';
import InputBar from './components/InputBar';
import { applyTheme, currentTheme } from './theme';
// import VersionNav from './components/VersionNav';  // temporarily disabled (results-only view)
import AskModal from './components/AskModal';
import PPTConfigModal from './components/PPTConfigModal';
import TracePanel from './components/TracePanel';
import PPTOutlineConfirmModal from './components/PPTOutlineConfirmModal';
import { downloadFile, buildTraceHtml } from './utils/exportHtml';
import {
  fetchModels, fetchSessions, fetchSessionMessages, fetchSessionTrace,
  createSession, createBranchSession, sendChat, normalizeServiceMessages, extractAssistantContent, stripReflectionTags, saveSessionAudio,
  fetchSettings, updateSettings,
  deleteSession as deleteSessionApi,
  deleteMemoryInteraction as deleteMemoryInteractionApi,
  toInjectedUserMessage
} from './api/chat';
import { dateKeyFromSessionName, sessionDateLabel, shanghaiDateKey } from './utils/sessionDate';
import {
  FALLBACK_ENABLED_STORAGE_KEY,
  FALLBACK_MODEL_STORAGE_KEY,
  MODEL_STORAGE_KEY,
  pickValidFallbackModel,
  pickValidModel,
  readAutoFallback,
  readFallbackModel,
  readStoredModel,
  writeAutoFallback,
  writeFallbackModel,
  writeStoredModel,
} from './utils/modelPreference';
import { readVoiceAutoRead, writeVoiceAutoRead } from './utils/voicePreference';
import { normalizeAskQuestions } from './utils/askUser';

async function resolveDailySessionName() {
  const today = shanghaiDateKey();
  try {
    const sessions = await fetchSessions();
    const exact = sessions.find(session => session && session.name === today);
    if (exact) return today;
    const legacy = sessions
      .filter(session => {
        const nameKey = dateKeyFromSessionName(session && session.name);
        if (nameKey === today) return true;
        // Legacy sessions such as "voice-design" do not carry a date in their
        // name; use the session's Shanghai-local modified date as a fallback.
        return sessionDateLabel(session) === today;
      })
      .sort((a, b) => String(b.modified || '').localeCompare(String(a.modified || '')))[0];
    if (legacy && legacy.name) return legacy.name;
  } catch {}
  return today;
}

function escapePptConfigValue(value) {
  return String(value || '').replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

function createPptDeckID() {
  if (globalThis.crypto && typeof globalThis.crypto.randomUUID === 'function') {
    return 'pptid_' + globalThis.crypto.randomUUID();
  }
  return 'pptid_' + Date.now().toString(36) + '-' + Math.random().toString(36).slice(2, 10);
}

function formatPptConfigMessage(config) {
  const deckDir = '/mnt/data/result/' + createPptDeckID();
  return `[用户已确认 PPT 制作参数]
<ppt_config>
  <role>${escapePptConfigValue(config.role)}</role>
  <scene>${escapePptConfigValue(config.scene)}</scene>
  <audience>${escapePptConfigValue(config.audience)}</audience>
  <page_count_desc>${escapePptConfigValue(config.page_count_desc)}</page_count_desc>
  <ppt_mode>${escapePptConfigValue(config.ppt_mode)}</ppt_mode>
  <deck_dir>${deckDir}</deck_dir>
</ppt_config>`;
}

function sessionRunCursorKey(sessionName, runId) {
  return String(sessionName || '') + '\u0000' + String(runId || '');
}

function interactionArtifactKey(interactionId) {
  const id = String(interactionId || '').trim();
  return id ? 'i:' + id : '';
}

function runArtifactKey(runId) {
  const id = String(runId || '').trim();
  return id ? 'r:' + id : '';
}

function isPPTConfigAsk(ask) {
  return ask?.askType === 'ppt_mode.confirm_params' ||
    (ask?.questions || []).some(question => question?.id === 'ppt_mode.confirm_params');
}

function isPPTOutlineAsk(ask) {
  return ask?.askType === 'ppt_mode.confirm_outline';
}

const ARTIFACT_STORAGE_PREFIX = 'fairy_artifacts_';

function readSessionArtifacts(sessionName) {
  if (!sessionName) return {};
  try {
    const raw = localStorage.getItem(ARTIFACT_STORAGE_PREFIX + sessionName);
    const parsed = raw ? JSON.parse(raw) : {};
    return parsed && typeof parsed === 'object' ? parsed : {};
  } catch {
    return {};
  }
}

function writeSessionArtifacts(sessionName, artifacts) {
  if (!sessionName) return;
  try {
    localStorage.setItem(ARTIFACT_STORAGE_PREFIX + sessionName, JSON.stringify(artifacts || {}));
  } catch {
    // Storage is a convenience cache; the live message still renders without it.
  }
}

function formatHeaderTokens(value) {
  return (Number(value) || 0).toLocaleString();
}

function formatHeaderDuration(ms) {
  const seconds = (Number(ms) || 0) / 1000;
  if (seconds >= 3600) return (seconds / 3600).toFixed(1) + 'H';
  if (seconds >= 60) return (seconds / 60).toFixed(1) + 'M';
  return seconds.toFixed(1) + 'S';
}

const EMBED_PREVIEW_PARAM = '__fairy_embed';

function isEmbeddedFairyPreview() {
  if (typeof window === 'undefined') return false;
  try {
    return new URLSearchParams(window.location.search).get(EMBED_PREVIEW_PARAM) === '1';
  } catch {
    return false;
  }
}

function appendUniqueMessages(base, extra) {
  const out = Array.isArray(base) ? [...base] : [];
  const seen = new Set(out.map(m => (m && m.id != null ? String(m.id) : '')).filter(Boolean));
  for (const message of Array.isArray(extra) ? extra : []) {
    if (!message) continue;
    if (message.id == null) { out.push(message); continue; }
    const key = String(message.id);
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(message);
  }
  return out;
}

export default function App() {
  const embeddedPreview = useMemo(isEmbeddedFairyPreview, []);
  const [mode, setMode] = useState('all-tags');
  const [models, setModels] = useState([]);
  const [selectedModel, setSelectedModel] = useState(() => readStoredModel());
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [timelineOpen, setTimelineOpen] = useState(false);
  const [autoFallback, setAutoFallback] = useState(() => readAutoFallback());
  const [fallbackModel, setFallbackModel] = useState(() => readFallbackModel());
  const [messages, setMessages] = useState([]);
  // Native-scroll paging cursor. The open request returns only the newest
  // window; older windows are prepended with an anchor-preserving scroll.
  const [paging, setPaging] = useState({ total: 0, displayTotal: 0, oldestOrdinal: null, newestOrdinal: null, hasMoreOlder: false });
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [subtaskStreams, setSubtaskStreams] = useState({});
  const [runningSubtasks, setRunningSubtasks] = useState({});
  // True while this session still owns detached subtask work (or a pending
  // resume). Keeping it separate from runningSubtasks lets a reloaded page
  // re-arm the status poller without inventing a fake sidebar branch.
  const [backgroundActive, setBackgroundActive] = useState(false);
  const [sessions, setSessions] = useState([]);
  const [sessionName, setSessionName] = useState('');
  const [loading, setLoading] = useState(false);
  const [llmProgress, setLlmProgress] = useState(null);
  const [eventResume, setEventResume] = useState(null);
  const [stats, setStats] = useState({ current: null, session: null });
  const [pendingAskUser, setPendingAskUser] = useState(null);
  const [showTrace, setShowTrace] = useState(false);
  const [previewFile, setPreviewFile] = useState(null);
  const [artifactsByMessage, setArtifactsByMessage] = useState({});
  const [pendingBranch, setPendingBranch] = useState(null);
  const pendingBranchRef = useRef(null);
  const abortRef = useRef(null);
  const eventStreamRef = useRef(null);
  const [muted, setMuted] = useState(() => !readVoiceAutoRead());
  useEffect(() => {
    writeVoiceAutoRead(!muted);
    mutedRef.current = muted;
  }, [muted]);
  const audioCtxRef = useRef(null);
  const audioSourceRef = useRef(null);
  const sendStartRef = useRef(0);
  const sessionNameRef = useRef('');
  const pollTokenRef = useRef(0);
  const loadTokenRef = useRef(0);
  const streamSessionRef = useRef('');
  const messagesRef = useRef([]);
  const sessionStateCacheRef = useRef(new Map());
  const sessionRealMsRef = useRef(0);
  const loadingOlderRef = useRef(false);
  // Mirror of paging state so async callbacks (poll, refresh) can read the
  // latest newestOrdinal without having to recreate the callback each time.
  const pagingRef = useRef({ total: 0, displayTotal: 0, oldestOrdinal: null, newestOrdinal: null, hasMoreOlder: false });
  const pendingArtifactsRef = useRef({});
  const activeRunRef = useRef({ session: '', runId: '' });
  const eventCursorRef = useRef(new Map());
  const healTimerRef = useRef(null);
  const streamGenRef = useRef(0); // bumped on every switch/newChat to kill in-flight SSE streams
  const inputBarRef = useRef(null);
  const [sessionRealMs, setSessionRealMs] = useState(0);
  const [clockNow, setClockNow] = useState(() => Date.now());
  const [theme, setTheme] = useState(currentTheme());
  const initialSessionLoadedRef = useRef(false);
  useEffect(() => { applyTheme(theme); }, [theme]);
  useEffect(() => {
    messagesRef.current = messages;
  }, [messages]);
  useEffect(() => {
    pagingRef.current = paging;
  }, [paging]);
  useEffect(() => {
    sessionRealMsRef.current = sessionRealMs;
  }, [sessionRealMs]);
  useEffect(() => {
    pendingArtifactsRef.current = {};
    setArtifactsByMessage(readSessionArtifacts(sessionName));
  }, [sessionName]);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      const [modelsResult, settingsResult] = await Promise.allSettled([fetchModels(), fetchSettings()]);
      if (cancelled) return;
      const list = modelsResult.status === 'fulfilled' && Array.isArray(modelsResult.value) ? modelsResult.value : [];
      const settings = settingsResult.status === 'fulfilled' ? settingsResult.value : null;
      if (list.length) setModels(list);
      if (settings) {
        setMuted(settings.voice_auto_read === false);
        setAutoFallback(settings.auto_fallback !== false);
        if (settings.theme) setTheme(settings.theme);
      }

      const preferredModel = (settings && settings.default_model) || readStoredModel();
      const nextModel = pickValidModel(list, preferredModel);
      if (nextModel) {
        setSelectedModel(nextModel);
        writeStoredModel(nextModel);
        if (settings && (settings.default_model || '') !== nextModel) {
          updateSettings({ default_model: nextModel }).catch(() => {});
        }
      }

      const preferredFallback = settings && typeof settings.fallback_model === 'string'
        ? settings.fallback_model
        : readFallbackModel();
      const nextFallback = pickValidFallbackModel(list, preferredFallback, nextModel);
      setFallbackModel(nextFallback);
      writeFallbackModel(nextFallback);
      if (settings && (settings.fallback_model || '') !== nextFallback) {
        updateSettings({ fallback_model: nextFallback }).catch(() => {});
      }
    })().catch(() => {});
    refreshSessions();
    return () => { cancelled = true; };
  }, []);
  const handleModelChange = useCallback((modelId) => {
    if (!modelId) return;
    setSelectedModel(modelId);
    writeStoredModel(modelId);
    const patch = { default_model: modelId };
    if (fallbackModel === modelId) {
      setFallbackModel('');
      writeFallbackModel('');
      patch.fallback_model = '';
    }
    updateSettings(patch).catch(() => {});
  }, [fallbackModel]);
  const handleModelsChanged = useCallback((list) => {
    const nextModels = Array.isArray(list) ? list : [];
    setModels(nextModels);
    const next = pickValidModel(nextModels, selectedModel);
    if (next && next !== selectedModel) {
      setSelectedModel(next);
      writeStoredModel(next);
    }
  }, [selectedModel]);
  const handleAutoFallbackChange = useCallback(() => {
    setAutoFallback(value => {
      const next = !value;
      writeAutoFallback(next);
      updateSettings({ auto_fallback: next }).catch(() => {});
      return next;
    });
  }, []);
  const handleFallbackModelChange = useCallback((modelId) => {
    const next = String(modelId || '').trim();
    setFallbackModel(next);
    writeFallbackModel(next);
    updateSettings({ fallback_model: next }).catch(() => {});
  }, []);
  const handleVoiceAutoReadChange = useCallback((enabled) => {
    setMuted(!enabled);
    writeVoiceAutoRead(enabled);
    updateSettings({ voice_auto_read: enabled }).catch(() => {});
  }, []);
  const handleThemeChange = useCallback((nextTheme) => {
    setTheme(nextTheme);
    updateSettings({ theme: nextTheme }).catch(() => {});
  }, []);
  useEffect(() => {
    if (!models.length) return;
    const next = pickValidFallbackModel(models, fallbackModel, selectedModel);
    if (next !== fallbackModel) {
      setFallbackModel(next);
      writeFallbackModel(next);
      updateSettings({ fallback_model: next }).catch(() => {});
    }
  }, [models, selectedModel, fallbackModel]);
  useEffect(() => {
    const onStorage = (event) => {
      if (event.key === MODEL_STORAGE_KEY && event.newValue) setSelectedModel(event.newValue);
      if (event.key === FALLBACK_ENABLED_STORAGE_KEY) setAutoFallback(event.newValue !== '0');
      if (event.key === FALLBACK_MODEL_STORAGE_KEY) setFallbackModel(event.newValue || '');
    };
    window.addEventListener('storage', onStorage);
    return () => window.removeEventListener('storage', onStorage);
  }, []);

  // Show each turn's real wait time on its user message: real is stored on
  // the turn's final assistant message; map it back to the preceding user msg.
  const applyTurnRealToUsers = useCallback((msgs) => {
    let lastReal = null;
    for (let i = msgs.length - 1; i >= 0; i--) {
      const m = msgs[i];
      if (m && m.role === 'assistant' && m.real_ms) {
        lastReal = m.real_ms;
      } else if (m && m.role === 'user' && lastReal) {
        m.real_ms = lastReal;
        lastReal = null;
      }
    }
    return msgs;
  }, []);

  // Reflection flow may emit a draft <report> then the final <report> in the
  // same turn. Collapse to the last report and mark it as the updated final.
  const collapseDraftReports = useCallback((msgs) => {
    const out = [];
    let pending = [];
    const flush = () => {
      if (pending.length > 1) {
        const last = pending[pending.length - 1];
        out.push({ ...last, replacedDraft: true });
      } else {
        out.push(...pending);
      }
      pending = [];
    };
    for (const m of msgs) {
      if (m && m.role === 'user') { flush(); out.push(m); }
      else if (m && m.role === 'assistant' && /<report\b/i.test(m.content || '')) { pending.push(m); }
      else { flush(); out.push(m); }
    }
    flush();
    return out;
  }, []);

  const refreshSessions = useCallback(async () => {
    try { setSessions(await fetchSessions()); } catch { /* ignore */ }
  }, []);

  useEffect(() => {
    if (!loading) return undefined;
    const timer = window.setInterval(refreshSessions, 1500);
    return () => window.clearInterval(timer);
  }, [loading, refreshSessions]);

  useEffect(() => {
    setClockNow(Date.now());
    if (!loading) return undefined;
    const timer = window.setInterval(() => setClockNow(Date.now()), 500);
    return () => window.clearInterval(timer);
  }, [loading]);

  const openPreview = useCallback((file) => {
    if (embeddedPreview && file && (file.kind === 'browser-live' || file.page_url || file.live_url)) return;
    if (!file || (file.kind !== 'workspace' && !file.path && !file.bodyText && !file.page_url && !file.live_url)) return;
    setPreviewFile(file);
  }, [embeddedPreview]);

  const attachArtifactsToMessage = useCallback((messageId, files) => {
    if (messageId == null || !Array.isArray(files) || files.length === 0) return;
    setArtifactsByMessage(prev => {
      const next = { ...prev, [String(messageId)]: files };
      writeSessionArtifacts(sessionNameRef.current, next);
      return next;
    });
  }, []);

  const cacheCurrentSession = useCallback(() => {
    const name = sessionNameRef.current;
    if (!name) return;
    const cache = sessionStateCacheRef.current;
    cache.delete(name);
    cache.set(name, {
      messages: messagesRef.current,
      paging: pagingRef.current,
      sessionRealMs: sessionRealMsRef.current,
    });
    while (cache.size > 12) {
      cache.delete(cache.keys().next().value);
    }
  }, []);

  const loadSession = useCallback(async (name) => {
    const token = ++loadTokenRef.current;
    // Hard session refresh on switch: kill any leftover status poller / SSE
    // stream from the previously displayed session so old content can never
    // write into this view after the switch.
    pollTokenRef.current++;
    streamGenRef.current++;
    // Switch immediately so any in-flight stream from another session is
    // stopped by the session guard, and the sidebar highlight follows.
    flushBatchNow(); // 上一会话的 pending updater 在切走前推到 UI，避免落到下一会话
    cacheCurrentSession();
    sessionNameRef.current = name;
    setSessionName(name);
    setSubtaskStreams({}); // stale subtask cards from the previous session must not linger
    setRunningSubtasks({});
    setBackgroundActive(false);
    setLlmProgress(null);
    setPreviewFile(null);
    setStats({ current: null, session: null });
    setSessionRealMs(0);
    try {
      if (abortRef.current) abortRef.current.abort();
      if (eventStreamRef.current) {
        eventStreamRef.current.abort();
        eventStreamRef.current = null;
      }
      setEventResume(null);
      activeRunRef.current = { session: name, runId: '' };
      setLoading(false);
      const loadStartSend = sendStartRef.current;
      const cached = sessionStateCacheRef.current.get(name);
      const restoredFromCache = !!(cached && Array.isArray(cached.messages));
      let msgs;
      let nextPaging;
      let savedModel = null;
      let savedUsage = null;
      let restoredRealMs = 0;

      if (restoredFromCache) {
        // Restore this tab's already-loaded window first. Only pull messages
        // newer than its newest cursor, so switching back never re-downloads
        // history the user already scrolled through.
        const cachedPaging = cached.paging || {};
        msgs = cached.messages;
        nextPaging = {
          total: Number(cachedPaging.total) || msgs.length,
          displayTotal: Number(cachedPaging.displayTotal) || Number(cachedPaging.total) || msgs.length,
          oldestOrdinal: cachedPaging.oldestOrdinal != null ? cachedPaging.oldestOrdinal : null,
          newestOrdinal: cachedPaging.newestOrdinal != null ? cachedPaging.newestOrdinal : null,
          hasMoreOlder: !!cachedPaging.hasMoreOlder,
        };
        restoredRealMs = Number(cached.sessionRealMs) || 0;
        messagesRef.current = msgs;
        pagingRef.current = nextPaging;
        setMessages(msgs);
        setPaging(nextPaging);
        setSessionRealMs(restoredRealMs);

        try {
          const tail = nextPaging.newestOrdinal;
          const res = await fetchSessionMessages(name, tail != null
            ? { limit: MESSAGE_PAGE_LIMIT, afterOrdinal: tail }
            : { limit: MESSAGE_PAGE_LIMIT });
          if (token !== loadTokenRef.current) return; // stale: a newer switch happened
          const tailMessages = applyTurnRealToUsers(collapseDraftReports(normalizeServiceMessages(res)));
          msgs = appendUniqueMessages(msgs, tailMessages);
          const p = res && res.data && res.data.paging;
          if (p) {
            nextPaging = {
              ...nextPaging,
              total: p.total != null ? p.total : nextPaging.total,
              displayTotal: p.display_total != null ? p.display_total : nextPaging.displayTotal,
              newestOrdinal: p.newest_ordinal != null ? p.newest_ordinal : nextPaging.newestOrdinal,
            };
          }
          messagesRef.current = msgs;
          pagingRef.current = nextPaging;
          setMessages(msgs);
          setPaging(nextPaging);
          savedModel = res && res.data && res.data.model;
          savedUsage = res && res.data && res.data.usage;
        } catch { /* keep the cached window when the tail refresh fails */ }
      } else {
        // Open on the newest window only. Older messages are fetched when the
        // native scrollport reaches the top and prepended with an anchor offset.
        const res = await fetchSessionMessages(name, { limit: MESSAGE_PAGE_LIMIT });
        if (token !== loadTokenRef.current) return; // stale: a newer switch happened
        const resPaging = res && res.data && res.data.paging;
        msgs = applyTurnRealToUsers(collapseDraftReports(normalizeServiceMessages(res)));
        nextPaging = resPaging
          ? { total: resPaging.total || 0, displayTotal: resPaging.display_total != null ? resPaging.display_total : msgs.length, oldestOrdinal: resPaging.oldest_ordinal, newestOrdinal: resPaging.newest_ordinal, hasMoreOlder: !!resPaging.has_more_older }
          : { total: msgs.length, displayTotal: msgs.length, oldestOrdinal: null, newestOrdinal: null, hasMoreOlder: false };
        savedModel = res && res.data && res.data.model;
        savedUsage = res && res.data && res.data.usage;
        messagesRef.current = msgs;
        pagingRef.current = nextPaging;
        setPaging(nextPaging);
      }
      if (!selectedModel && savedModel && models.length) {
        const stillValid = models.some(m => m.id === savedModel);
        if (stillValid) {
          setSelectedModel(savedModel);
        }
      }
      setStats({ current: null, session: savedUsage || null });
      flushBatchNow(); // loadSession 落定前 flush，避免残留 updater 污染新会话首屏
      setSessionName(name);
      sessionNameRef.current = name;
      setMessages(msgs);
      const savedUsageReal = (savedUsage && Number(savedUsage.real_ms)) || 0;
      let localReal = 0;
      try { localReal = Number(localStorage.getItem('real_ms_' + name)) || 0; } catch {}
      setSessionRealMs(savedUsageReal || localReal || restoredRealMs);
      // Post-switch refresh: re-fetch the conversation shortly after so any
      // contamination from a dying stream/poller is wiped from the view.
      setTimeout(async () => {
        if (token !== loadTokenRef.current) return; // switched again meanwhile
        if (sendStartRef.current !== loadStartSend) return; // a new send started
        if (restoredFromCache) return; // cached branch already refreshed the tail above
        try {
          // Pick up any messages that arrived in the brief window between
          // the first fetch and this refresh. afterOrdinal is the last
          // ordinal we already have; the server returns strictly newer ones.
          const tail = pagingRef.current.newestOrdinal;
          const res2 = await fetchSessionMessages(name, tail != null ? { limit: MESSAGE_PAGE_LIMIT, afterOrdinal: tail } : { limit: MESSAGE_PAGE_LIMIT });
          if (token !== loadTokenRef.current) return;
          const msgs2 = applyTurnRealToUsers(collapseDraftReports(normalizeServiceMessages(res2)));
          if (msgs2.length) setMessages(prev => appendUniqueMessages(prev, msgs2));
          const p2 = res2 && res2.data && res2.data.paging;
          if (p2) {
            const mergedPaging = {
              ...pagingRef.current,
              total: p2.total != null ? p2.total : pagingRef.current.total,
              displayTotal: p2.display_total != null ? p2.display_total : pagingRef.current.displayTotal,
              newestOrdinal: p2.newest_ordinal != null ? p2.newest_ordinal : pagingRef.current.newestOrdinal,
            };
            pagingRef.current = mergedPaging;
            setPaging(mergedPaging);
          }
        } catch { /* ignore */ }
      }, 500);
      // If this chat's agent is still running (switched away / refreshed),
      // reconnect to its replayable event stream instead of falling back to
      // message polling. Polling cannot deliver live tool/text deltas.
      try {
        const st = await fetch('/api/sessions/' + encodeURIComponent(name) + '/status').then(r => r.json());
        if (st && (st.running || Number(st.event_seq) > 0)) {
          activeRunRef.current = { session: name, runId: st.run_id || '' };
          setLoading(!!st.running);
          setEventResume({ name, runId: st.run_id || '', nonce: Date.now() });
        }
        // A detached subtask outlives the page. Re-arm the background poller so
        // a reloaded tab keeps following the job into its continuation run
        // instead of going quiet until the next manual switch.
        if (st) {
          const activeJobs = Array.isArray(st.background_job_list)
            ? st.background_job_list.filter(job => job && job.branch_session && (job.status === 'running' || job.status === 'waiting'))
            : [];
          if (activeJobs.length) {
            setRunningSubtasks(prev => {
              const next = { ...prev };
              for (const job of activeJobs) {
                next[job.branch_session] = {
                  name: job.branch_session,
                  parent: name,
                  domain: job.title || job.branch_session,
                };
              }
              return next;
            });
          }
          if (activeJobs.length || st.resume_pending || Number(st.background_running) > 0) {
            setBackgroundActive(true);
          }
        }
      } catch { /* ignore */ }
    } catch { /* ignore */ }
  }, [models, cacheCurrentSession, selectedModel]);

  // Fetch the previous raw-message window. ChatArea records the first visible
  // semantic row before calling this and restores that same row after React
  // commits the prepend, so the reader never gets thrown to the new top.
  const loadOlder = useCallback(async () => {
    if (loadingOlderRef.current) return;
    const current = pagingRef.current;
    if (!current.hasMoreOlder || current.oldestOrdinal == null) return;
    const session = sessionNameRef.current;
    if (!session) return;

    loadingOlderRef.current = true;
    setLoadingOlder(true);
    try {
      const res = await fetchSessionMessages(session, {
        limit: MESSAGE_PAGE_LIMIT,
        beforeOrdinal: current.oldestOrdinal,
      });
      if (sessionNameRef.current !== session) return;
      const older = applyTurnRealToUsers(collapseDraftReports(normalizeServiceMessages(res)));
      const page = res && res.data && res.data.paging;
      if (older.length) {
        setMessages(prev => {
          const seen = new Set(prev.map(m => (m && m.id != null ? String(m.id) : '')).filter(Boolean));
          const fresh = older.filter(m => m && (m.id == null || !seen.has(String(m.id))));
          return fresh.length ? [...fresh, ...prev] : prev;
        });
      }
      if (page) {
        setPaging(prev => ({
          ...prev,
          total: page.total != null ? page.total : prev.total,
          displayTotal: page.display_total != null ? page.display_total : prev.displayTotal,
          oldestOrdinal: page.oldest_ordinal != null ? page.oldest_ordinal : prev.oldestOrdinal,
          hasMoreOlder: !!page.has_more_older,
        }));
      } else if (!older.length) {
        setPaging(prev => ({ ...prev, hasMoreOlder: false }));
      }
    } catch {
      // Leave the current window untouched; the next top scroll may retry.
    } finally {
      loadingOlderRef.current = false;
      setLoadingOlder(false);
    }
  }, []);

  const openSubtaskSession = useCallback(async (tc, result) => {
    let payload = null;
    try { payload = result && result.content ? JSON.parse(result.content) : null; } catch {}

    let target = payload && payload.branch_session ? payload.branch_session : '';
    if (!target && payload && payload.session) {
      target = String(payload.session).split(/[\\/]/).pop() || '';
    }

    let title = '';
    try { title = JSON.parse(tc && tc.function && tc.function.arguments || '{}').title || ''; } catch {}

    const parent = sessionNameRef.current || sessionName;
    if (!target && title && parent) {
      const findBranch = list => (Array.isArray(list) ? list : [])
        .filter(item => item && item.kind === 'branch' && item.parent_session === parent && item.domain === title)
        .sort((a, b) => String(b.modified || '').localeCompare(String(a.modified || '')))[0];

      let candidates = sessions;
      let match = findBranch(candidates);
      if (!match) {
        try {
          candidates = await fetchSessions();
          setSessions(candidates);
        } catch { /* keep the current list */ }
        match = findBranch(candidates);
      }
      target = match && match.name || '';
    }

    if (!target) return false;
    await loadSession(target);
    return true;
  }, [loadSession, sessionName, sessions]);

  const pollSessionStatus = useCallback(async (name, token) => {
    const deadline = Date.now() + 30 * 60 * 1000; // max 30 min poll
    while (Date.now() < deadline) {
      await new Promise(r => setTimeout(r, 3000));
      if (token !== pollTokenRef.current) return; // switched to another chat
      try {
        const st = await fetch('/api/sessions/' + encodeURIComponent(name) + '/status').then(r => r.json());
        if (token !== pollTokenRef.current) return;
        if (st && st.running) {
          // Keep refreshing messages so new steps / subtask cards appear live.
          // Use paging.newestOrdinal so we fetch only what arrived since the
          // last poll - the previous full re-fetch was paying to download
          // the entire 1347-message tail every 3 seconds.
          const tail = pagingRef.current.newestOrdinal;
          const opts = tail != null ? { limit: 200, afterOrdinal: tail } : { limit: 100 };
          const res = await fetchSessionMessages(name, opts);
          if (token !== pollTokenRef.current) return;
          const newMsgs = normalizeServiceMessages(res);
          if (newMsgs.length) setMessages(prev => [...prev, ...applyTurnRealToUsers(collapseDraftReports(newMsgs))]);
          const p = res && res.data && res.data.paging;
          if (p) setPaging(prev => ({ ...prev, total: p.total, displayTotal: p.display_total != null ? p.display_total : prev.displayTotal, newestOrdinal: p.newest_ordinal != null ? p.newest_ordinal : prev.newestOrdinal }));
          // Stream the running turn's token usage into the header so the
          // TOKENS counter ticks up while a response is still streaming
          // instead of staying frozen at 0 until the turn finishes.
          const liveUsage = res && res.data && res.data.usage;
          if (liveUsage) setStats(prev => ({ ...prev, session: liveUsage }));
          continue;
        }
        // finished: do one final tail fetch, then clear loading.
        const tail2 = pagingRef.current.newestOrdinal;
        const res = await fetchSessionMessages(name, tail2 != null ? { limit: 200, afterOrdinal: tail2 } : { limit: 100 });
        if (token !== pollTokenRef.current) return;
        const newMsgs2 = normalizeServiceMessages(res);
        if (newMsgs2.length) setMessages(prev => [...prev, ...applyTurnRealToUsers(collapseDraftReports(newMsgs2))]);
        const p3 = res && res.data && res.data.paging;
        if (p3) setPaging(prev => ({ ...prev, total: p3.total, displayTotal: p3.display_total != null ? p3.display_total : prev.displayTotal, newestOrdinal: p3.newest_ordinal != null ? p3.newest_ordinal : prev.newestOrdinal }));
        const us = res && res.data && res.data.usage;
        if (us) setStats(prev => ({ ...prev, session: us }));
        const usReal = (us && Number(us.real_ms)) || 0;
        let pollLocalReal = 0;
        try { pollLocalReal = Number(localStorage.getItem('real_ms_' + name)) || 0; } catch {}
        setSessionRealMs(usReal || pollLocalReal);
        setLoading(false);
        refreshSessions();
        return;
      } catch { /* keep polling */ }
    }
    if (token === pollTokenRef.current) setLoading(false);
  }, [refreshSessions]);


  const newChat = useCallback(async () => {
    const sess = await resolveDailySessionName();
    try { await createSession(sess); } catch {}
    await loadSession(sess);
    refreshSessions();
  }, [loadSession, refreshSessions]);
  // 「新对话」按钮：在当前主会话下创建并立即进入一个空子分支
  const newChatBranch = useCallback(async () => {
    const parent = sessionNameRef.current && /^\d{4}-\d{2}-\d{2}$/.test(sessionNameRef.current)
      ? sessionNameRef.current
      : (await resolveDailySessionName()) || shanghaiDateKey();
    const stamp = new Date().toLocaleTimeString('zh-CN', { hour12: false, timeZone: 'Asia/Shanghai' }).replace(/[:]/g, '');
    const domain = `分支 ${stamp}`;
    const res = await createBranchSession(parent, domain, { created_by: 'user-sidebar' });
    if (res && res.ok && res.name) {
      await loadSession(res.name);
      return res.name;
    }
    return null;
  }, [loadSession]);

  const deleteInteractionMemory = useCallback(async (interactionId, scope, context = {}) => {
    if (!interactionId) return;
    try {
      const result = await deleteMemoryInteractionApi(interactionId, scope, context);
      if (scope === 'conversation+memory') {
        const targetTurn = String(context.turnId || '');
        setMessages(prev => prev.filter(message => {
          if (String(message.interaction_id || '') === String(interactionId)) return false;
          return !targetTurn || String(message.turn_id || '') !== targetTurn;
        }));
      }
      return result;
    } catch (error) {
      if (typeof window !== 'undefined') window.alert(error.message || String(error));
      throw error;
    }
  }, []);

  // 删除一个分支会话：服务端只接受分支会话，主会话会被拒绝。
  // 若删的就是当前打开的会话，落到今日主会话，避免停留在已删除的窗口。
  const handleDeleteSession = useCallback(async (name) => {
    if (!name) return false;
    const ok = (typeof window === 'undefined') ? true : window.confirm(`删除分支会话「${name}」？此操作不可恢复。`);
    if (!ok) return false;
    try {
      await deleteSessionApi(name);
    } catch (error) {
      if (typeof window !== 'undefined') window.alert(error.message || String(error));
      return false;
    }
    // 刷新侧边栏列表
    refreshSessions();
    // 删的是当前打开的会话就跳到今日主会话
    if (sessionNameRef.current === name) {
      const today = await resolveDailySessionName();
      if (today) await loadSession(today);
    }
    return true;
  }, [loadSession, refreshSessions]);

  // A first upload must name the session so files land in that
  // session's own upload folder.
  const ensureSession = useCallback(async () => {
    const current = sessionNameRef.current;
    if (current && dateKeyFromSessionName(current) === shanghaiDateKey()) return current;
    const sess = await resolveDailySessionName();
    flushBatchNow(); // 首次会话注入前的兜底
    sessionNameRef.current = sess;
    setSessionName(sess);
    return sess;
  }, []);

  const send = useCallback(async (text, files, forceSession) => {
    const attachmentFiles = Array.isArray(files) ? files : [];
    if ((!text || !text.trim()) && attachmentFiles.length === 0) return;
    if (!selectedModel) return;
    let sess = forceSession || sessionNameRef.current || (await resolveDailySessionName());
    if (!forceSession && dateKeyFromSessionName(sess) !== shanghaiDateKey()) {
      sess = await resolveDailySessionName();
      if (sess !== sessionNameRef.current) await loadSession(sess);
    }

    // Live inject: a previous turn is still streaming on the server, so we
    // do not spawn a fresh agent process — we push the new user message into
    // the running one via UDP. The server's SSE keeps flowing and will emit
    // an "injected_user" event the UI renders alongside the streaming reply.
    if (!forceSession && loading && sessionNameRef.current === sess) {
      // Attachments travel as a <file_context> XML block prepended to the text,
      // which is the same wire shape the normal send path produces. The inject
      // protocol is NDJSON {"op":"inject","text":"..."} end to end, so no server
      // or Go change is needed to carry files.
      const injectText = attachmentFiles.length
        ? `<file_context>${JSON.stringify(attachmentFiles)}</file_context>\n${text.trim()}`
        : text.trim();
      if (!injectText.trim()) {
        // Nothing to inject; let the normal path handle it.
      } else {
        try {
        const r = await fetch('/api/chat/inject', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json; charset=utf-8' },
          body: JSON.stringify({ session: sess, text: injectText }),
        });
          const d = await r.json().catch(() => ({}));
          if (!r.ok || !d.ok) throw new Error(d.error || `inject failed (HTTP ${r.status})`);
          // The agent will emit an injected_user SSE event once the message
          // reaches the loop boundary; handleSSEEvent renders it. We deliberately
          // do NOT echo locally here, otherwise the message would render twice.
          return;
        } catch (e) {
          // Fall through to a normal send if inject fails (e.g. agent already
          // exited between send calls).
          console.warn('inject failed, falling back to fresh send', e);
        }
      }
    }

    // Answering an ask_user from another session must first switch to that
    // session's history, otherwise the answer + stream would mix into the
    // currently displayed conversation.
    if (forceSession && forceSession !== sessionNameRef.current) {
      await loadSession(forceSession);
    }
    if (forceSession) {
      if (sessionName !== forceSession) setSessionName(forceSession);
      sessionNameRef.current = forceSession;
    } else if (!sessionName) {
      setSessionName(sess); sessionNameRef.current = sess;
    } else {
      sessionNameRef.current = sess;
    }

    flushBatchNow(); // 启动新一轮 send 前清掉 pending，避免与 userMsg 顺序错乱
    const userMsg = { role: 'user', content: text, files: attachmentFiles };
    setMessages(prev => [...prev, userMsg]);
    sendStartRef.current = Date.now();
    setLlmProgress(null);
    setLoading(true);
    resetTTS();

    const controller = new AbortController();
    abortRef.current = controller;
    const myGen = streamGenRef.current;
    // A new foreground run is starting. Clear the previous run id before the
    // first SSE event arrives; otherwise the stale run id makes handleSSEEvent
    // reject every event from this run as if it belonged to an older stream.
    activeRunRef.current = { session: sess, runId: '' };

    try {
      const response = await sendChat(text, selectedModel, sess, controller.signal, attachmentFiles, 'chat');

      if (!response.ok) {
        let detail = '';
        let retryable = false;
        try {
          const errBody = await response.json();
          detail = String((errBody && errBody.error) || '');
          retryable = !!(errBody && errBody.retry);
        } catch { /* non-JSON error body */ }
        if (sessionNameRef.current === sess) {
          setMessages(prev => {
            // The server rejected this request before running it, so drop the
            // optimistic user bubble rather than leaving a turn that never ran.
            const next = [...prev];
            for (let i = next.length - 1; i >= 0; i--) {
              if (next[i] && next[i].role === 'user' && next[i].content === text) {
                next.splice(i, 1);
                break;
              }
            }
            next.push({
              role: 'system',
              content: (detail || ('HTTP Error: ' + response.status)) + (retryable ? '（稍后可直接重发）' : ''),
            });
            return next;
          });
        }
        return;
      }

      const contentType = response.headers.get('Content-Type') || '';
      if (contentType.indexOf('text/event-stream') >= 0) {
        const reader = response.body.getReader();
        const decoder = new TextDecoder();
        let buffer = '';
        let streamDone = false;

        while (!streamDone) {
          const { done, value } = await reader.read();
          streamDone = done;
          if (value) {
            buffer += decoder.decode(value, { stream: !streamDone });
            const lines = buffer.split('\n');
            buffer = lines.pop() || '';
            for (const line of lines) {
              if (line.startsWith('data: ')) {
                const raw = line.slice(6);
                if (raw === '[DONE]') continue;
                try {
                  const event = JSON.parse(raw);
                  if (streamGenRef.current !== myGen || sessionNameRef.current !== sess) { controller.abort(); streamDone = true; break; }
                  streamSessionRef.current = sess;
                  handleSSEEvent(event);
                } catch { /* skip */ }
              }
            }
          }
        }
        if (buffer.trim()) {
          const dataLine = buffer.trim();
          if (dataLine.startsWith('data: ')) {
            const raw = dataLine.slice(6);
            if (raw !== '[DONE]') {
              if (streamGenRef.current !== myGen || sessionNameRef.current !== sess) { controller.abort(); }
              else {
                streamSessionRef.current = sess;
                try { handleSSEEvent(JSON.parse(raw)); } catch { /* ignore */ }
              }
            }
          }
        }
      } else {
        try {
          const data = await response.json();
          if (sessionNameRef.current === sess) {
            if (data.data && data.data.messages) {
              setMessages(applyTurnRealToUsers(collapseDraftReports(normalizeServiceMessages(data))));
            } else if (data.error) {
              setMessages(prev => [...prev, { role: 'system', content: 'Error: ' + data.error }]);
            }
          }
        } catch { /* ignore */ }
      }
    } catch (err) {
      if (err.name !== 'AbortError' && sessionNameRef.current === sess) {
        setMessages(prev => [...prev, { role: 'system', content: 'Network Error: ' + err.message }]);
      }
    } finally {
      flushBatchNow(); // 流结束前把残余 updater 推到 UI，确保 done 事件之后的最终状态立刻可见
      if (streamGenRef.current === myGen && sessionNameRef.current === sess) {
        activeRunRef.current = { session: sess, runId: '' };
        setLoading(false);
        setLlmProgress(null);
      }
      if (abortRef.current === controller) abortRef.current = null;
      refreshSessions();
    }
  }, [loading, selectedModel, sessionName, refreshSessions, loadSession]);

  const regenerateInteraction = useCallback(async ({ userMessage, interactionId, turnId, sessionName: targetSession }) => {
    if (!userMessage || !window.confirm("重新生成会替换这一轮回复，并重写对应的分段记忆。继续？")) return;
    const target = targetSession || sessionNameRef.current;
    if (interactionId) {
      await deleteInteractionMemory(interactionId, "conversation+memory", { turnId, session: target });
    }
    await send(userMessage.content || "", Array.isArray(userMessage.files) ? userMessage.files : [], target);
  }, [deleteInteractionMemory, send]);

  useEffect(() => {
    if (initialSessionLoadedRef.current) return;
    initialSessionLoadedRef.current = true;
    (async () => {
      const today = shanghaiDateKey();
      try { await createSession(today); } catch (e) { console.warn('createSession(today) failed:', e); }
      try { await refreshSessions(); } catch {}
      try { await loadSession(today); } catch {}
    })();
    const onRollover = () => {
      const today = shanghaiDateKey();
      if (sessionNameRef.current && sessionNameRef.current !== today) {
        (async () => {
          try { await createSession(today); } catch {}
          try { await refreshSessions(); } catch {}
          try { await loadSession(today); } catch {}
        })();
      }
    };
    const tick = setInterval(onRollover, 30 * 1000);
    document.addEventListener('visibilitychange', onRollover);
    return () => {
      clearInterval(tick);
      document.removeEventListener('visibilitychange', onRollover);
    };
  }, [loadSession]);

  const triggerSelfHeal = useCallback(() => {
    const name = sessionNameRef.current;
    if (!name) return;
    if (healTimerRef.current) clearTimeout(healTimerRef.current);
    healTimerRef.current = setTimeout(() => {
      healTimerRef.current = null;
      loadSession(name);
    }, 300);
  }, [loadSession]);

  // ── Per-turn TTS audio: accumulate the generated audio, save it into the
  //    local session so each spoken reply can be replayed from its icon. ──
  const ttsTurnChunksRef = useRef([]);
  const ttsTurnSrRef = useRef(48000);
  const ttsTurnMsgIdRef = useRef(null);
  const ttsTurnDoneRef = useRef(false);
  const ttsTurnFinalizingRef = useRef(false);

  const float32ToWav = useCallback((f32, sampleRate) => {
    const numSamples = f32.length;
    const dataSize = numSamples * 2;
    const buffer = new ArrayBuffer(44 + dataSize);
    const view = new DataView(buffer);
    const wstr = (off, s) => { for (let i = 0; i < s.length; i++) view.setUint8(off + i, s.charCodeAt(i)); };
    wstr(0, 'RIFF'); view.setUint32(4, 36 + dataSize, true); wstr(8, 'WAVE');
    wstr(12, 'fmt '); view.setUint32(16, 16, true); view.setUint16(20, 1, true);
    view.setUint16(22, 2, true); view.setUint32(24, sampleRate, true);
    view.setUint32(28, sampleRate * 2 * 2, true); view.setUint16(32, 4, true); view.setUint16(34, 16, true);
    wstr(36, 'data'); view.setUint32(40, dataSize, true);
    const pcm = new Int16Array(buffer, 44);
    for (let i = 0; i < numSamples; i++) {
      const s = Math.max(-1, Math.min(1, f32[i]));
      pcm[i] = s < 0 ? s * 0x8000 : s * 0x7FFF;
    }
    return new Uint8Array(buffer);
  }, []);

  // ── TTS text sanitizer: never speak reflection/report/thinking or tool noise ──
  const TTS_BLOCK_TAGS = ['reflection', 'report', 'think', 'summary'];
  const ttsXmlBlockRef = useRef(null);            // open XML block tag name, or null
  const ttsInCodeFenceRef = useRef(false);        // inside a ``` code fence
  const ttsMarkdownReflectionRef = useRef(false); // inside an unwrapped "### 反思" section

  const sanitizeForTTS = useCallback((chunk) => {
    const s = String(chunk || '');
    if (!s) return '';
    let out = '';
    let block = ttsXmlBlockRef.current;
    let inFence = ttsInCodeFenceRef.current;
    let inMdRefl = ttsMarkdownReflectionRef.current;
    const re = /<\/?[a-zA-Z][a-zA-Z0-9_-]*(?:\s[^>]*)?>|```|(^|\n)#{2,4}[ \t]*/g;
    let last = 0;
    let m;
    while ((m = re.exec(s))) {
      const tok = m[0];
      const idx = m.index;
      const before = s.slice(last, idx);
      if (!block && !inFence && !inMdRefl) out += before;
      if (inMdRefl) {
        // drop everything from here to the end of the turn
      } else if (inFence) {
        if (tok.indexOf('```') === 0) inFence = false;
      } else if (block) {
        if (new RegExp('^</' + block + '>', 'i').test(tok)) block = null;
      } else if (tok.indexOf('```') === 0) {
        inFence = true;
      } else if (tok.indexOf('<') === 0) {
        const name = (tok.match(/^<\/*([a-zA-Z][a-zA-Z0-9_-]*)/i) || [])[1];
        if (!name || tok.indexOf('</') === 0) {
          // stray close tag or unparsable: drop it
        } else if (TTS_BLOCK_TAGS.includes(name.toLowerCase()) || name.toLowerCase() === 'cite') {
          block = name.toLowerCase();
        } else {
          out += tok; // keep non-block tags; stripped later
        }
      } else if (/^#{2,4}[ \t]*/ .test(tok) && /反思/.test(tok)) {
        inMdRefl = true; // unwrapped markdown reflection: drop to end of turn
      }
      last = idx + tok.length;
    }
    if (!block && !inFence && !inMdRefl) out += s.slice(last);
    ttsXmlBlockRef.current = block;
    ttsInCodeFenceRef.current = inFence;
    ttsMarkdownReflectionRef.current = inMdRefl;
    return out;
  }, []);

  const maybeFinalizeTTSTurn = useCallback(() => {
    if (ttsTurnFinalizingRef.current) return;
    const msgId = ttsTurnMsgIdRef.current;
    const chunks = ttsTurnChunksRef.current;
    const session = sessionNameRef.current;
    // Turn not finished yet (done event not received / no target message):
    // keep the accumulated audio and wait; never clear it prematurely.
    if (msgId == null || !chunks.length || !session) return;
    const sr = ttsTurnSrRef.current;
    ttsTurnFinalizingRef.current = true;
    ttsTurnChunksRef.current = [];
    ttsTurnMsgIdRef.current = null;
    ttsTurnDoneRef.current = false;
    const total = chunks.reduce((n, c) => n + c.length, 0);
    const merged = new Float32Array(total);
    let off = 0;
    for (const c of chunks) { merged.set(c, off); off += c.length; }
    const wav = float32ToWav(merged, sr);
    const duration = Math.round((total / 2 / sr) * 100) / 100;
    const url = '/api/sessions/' + encodeURIComponent(session) + '/audio/' + encodeURIComponent(msgId);
    setMessages(prev => prev.map(m => (m && String(m.id) === String(msgId)) ? { ...m, _ttsAudio: { url, duration_sec: duration } } : m));
    try {
      let bin = '';
      for (let i = 0; i < wav.length; i += 0x8000) bin += String.fromCharCode.apply(null, wav.subarray(i, i + 0x8000));
      saveSessionAudio(session, msgId, btoa(bin)).catch(() => {});
    } catch {}
    ttsTurnFinalizingRef.current = false;
  }, [float32ToWav]);

  // Cut any in-flight TTS playback and discard accumulated audio. Used when a
  // draft/final answer is replaced (post-reflection final, or the backend
  // re-answering a turn) so the same reply is never read aloud twice.
  const cutTTSStream = useCallback(() => {
    ttsStoppedRef.current = true;
    ttsQueueRef.current = [];
    ttsPendingRef.current = '';
    ttsTurnChunksRef.current = [];
    for (const src of ttsActiveSourcesRef.current) { try { src.stop(); } catch { /* not started */ } }
    ttsActiveSourcesRef.current.clear();
    ttsNextTimeRef.current = 0;
    ttsPlayingRef.current = false;
    ttsXmlBlockRef.current = null;
    ttsInCodeFenceRef.current = false;
    ttsMarkdownReflectionRef.current = false;
  }, []);

  // ── Batch SSE state updates within one frame ────────────────────────
  // stripThinkingClient — defense-in-depth sanitizer for assistant content.
  // The backend already runs stripThinking on streaming chunks, but the
  // `assistant` final event still ships the raw resp.Content (un-stripped on
  // unclosed / nested edges). This mirrors the Go stripThinking regex so the
  // visible bubble never leaks protocol tags.
  function stripThinkingClient(text) {
    if (!text) return '';
    let out = String(text).replace(
      /<(?:think|thinking|mm:think|reflection)\b[^>]*>[\s\S]*?<\/(?:think|thinking|mm:think|reflection)>/gi,
      ''
    );
    out = out.replace(/<\/?(?:think|thinking|mm:think|reflection)\b[^>]*>/gi, '');
    return out;
  }

  // 高频 SSE 事件（tool_call 循环、assistant/chunk 文本流）每事件一次
  // setState 会让 React 同一帧 reconcile 整个 messages 列表 N 次，主线程
  // 被锁死导致 UI 不刷新（"思考中"挂着没新工具/回复）。下面的 batch infra
  // 把同帧内的 updater 聚合成一次 setState；updater 之间仍然用 reduce 串行，
  // 所以依赖前一条 state 的分支（如 assistant/chunk 追加到最后一条 draft）
  // 行为不变。
  //
  // 调度分三层，避免任一通道被掐断时整个批处理停摆：
  //   1) queueMicrotask：最快出手，脱离 RAF 在后台 tab / 不可见窗口被节流的束缚
  //   2) requestAnimationFrame：tab 可见时与浏览器渲染节奏对齐，合并同帧多次 flush
  //   3) 60ms setTimeout 兜底：极端调度阻塞（合成器卡顿 / 微任务队列饿死）下硬下限
  // 任意一层触发就把另两层的句柄清掉，保证只 flush 一次。
  const messageBatchRef = useRef([]);
  const llmProgressBatchRef = useRef(null);
  const subtaskBatchRef = useRef([]);
  const batchRafRef = useRef(0);
  const batchMicroRef = useRef(false);
  const batchTimerRef = useRef(0);
  const batchFlushingRef = useRef(false);
  const FLUSH_FALLBACK_MS = 60;
  const flushBatch = useCallback(() => {
    // 一次性清掉所有调度句柄，保证只 flush 一次
    batchRafRef.current = 0;
    batchMicroRef.current = false;
    if (batchTimerRef.current) {
      clearTimeout(batchTimerRef.current);
      batchTimerRef.current = 0;
    }
    if (batchFlushingRef.current) return; // 幂等，递归调度时直接返回
    batchFlushingRef.current = true;
    try {
      const msgUpdaters = messageBatchRef.current;
      const subUpdaters = subtaskBatchRef.current;
      const llmProgress = llmProgressBatchRef.current;
      if (msgUpdaters.length) {
        messageBatchRef.current = [];
        setMessages(prev => msgUpdaters.reduce((p, u) => u(p), prev));
      }
      if (subUpdaters.length) {
        subtaskBatchRef.current = [];
        setSubtaskStreams(prev => subUpdaters.reduce((p, u) => u(p), prev));
      }
      if (llmProgress !== null) {
        llmProgressBatchRef.current = null;
        setLlmProgress(llmProgress);
      }
    } finally {
      batchFlushingRef.current = false;
      // SSE 流式期间会在 flush 进行中再 push 新的 updater, 这时
      // scheduleBatch 会因 batchFlushingRef.current === true 早 return,
      // 没有任何调度重新触发 —— 这批新 updater 会卡在 batch ref 里.
      // 在 finally 解锁后再扫一眼 pending, 有就立即 re-schedule.
      if (
        messageBatchRef.current.length ||
        subtaskBatchRef.current.length ||
        llmProgressBatchRef.current !== null
      ) {
        scheduleBatch();
      }
    }
  }, []);
  const scheduleBatch = useCallback(() => {
    if (batchFlushingRef.current) return; // 正在 flush，等下一轮再排
    // microtask 优先：脱离 RAF 节流约束
    if (!batchMicroRef.current) {
      batchMicroRef.current = true;
      queueMicrotask(() => flushBatch());
    }
    // RAF 同帧合帧（tab 不可见时被节流也无妨，microtask + 兜底会救场）
    if (!batchRafRef.current) {
      batchRafRef.current = requestAnimationFrame(flushBatch);
    }
    // 60ms 兜底：防止 microtask 队列饿死 / 合成器卡顿
    if (!batchTimerRef.current) {
      batchTimerRef.current = setTimeout(() => flushBatch(), FLUSH_FALLBACK_MS);
    }
  }, [flushBatch]);
  // 强制立即 flush：用于切 session / send 启动 / finally / 错误分支，
  // 避免上一会话的 pending updater 落到下一会话的第一帧。
  const flushBatchNow = useCallback(() => {
    flushBatch();
  }, [flushBatch]);
  const batchedSetMessages = useCallback((updater) => {
    messageBatchRef.current.push(updater);
    scheduleBatch();
  }, [scheduleBatch]);
  const batchedSetSubtaskStreams = useCallback((updater) => {
    subtaskBatchRef.current.push(updater);
    scheduleBatch();
  }, [scheduleBatch]);
  const batchedSetLlmProgress = useCallback((event) => {
    llmProgressBatchRef.current = event;
    scheduleBatch();
  }, [scheduleBatch]);
  useEffect(() => {
    // 可见性探针：tab 切回前台时把堆积的 updater 立即推到 UI
    const onVisible = () => {
      if (document.visibilityState === 'visible') flushBatch();
    };
    const onPageShow = () => flushBatch();
    // Tauri 窗口可见性钩子：src-tauri/src/lib.rs 在 win.show() / win.hide() /
    // Focused(true|false) 时通过 window.eval() 注入这段 CustomEvent。
    // Tauri 2 的 hide()/show() 不触发 document.visibilityState 也不发
    // visibilitychange —— 单纯靠浏览器事件救不了 WebView2 失焦/被托盘冻结时
    // 堆积的 batched updater。这里兜底接管那条死路径。
    const onFairyFocus = () => {
      if (import.meta.env.DEV) {
        // dev mode 仅打印一行, prod 不污染 console
        console.debug('[fairy:focus] flush pending batch');
      }
      flushBatch();
    };
    document.addEventListener('visibilitychange', onVisible);
    window.addEventListener('pageshow', onPageShow);
    window.addEventListener('fairy:focus', onFairyFocus);
    // DevTools 调试入口: 主人在 console 里 dispatchEvent 即可验证 listener
    // 接通, 无需走 Tauri 桌面壳. 仅 dev mode 暴露.
    if (import.meta.env.DEV) {
      window.__fairyDebug = Object.freeze({
        flushBatch,
        dispatchFocus: () =>
          window.dispatchEvent(new CustomEvent('fairy:focus')),
        pendingUpdaters: () => messageBatchRef.current.length,
      });
    }
    return () => {
      document.removeEventListener('visibilitychange', onVisible);
      window.removeEventListener('pageshow', onPageShow);
      window.removeEventListener('fairy:focus', onFairyFocus);
      if (batchRafRef.current) {
        cancelAnimationFrame(batchRafRef.current);
        batchRafRef.current = 0;
      }
      if (batchTimerRef.current) {
        clearTimeout(batchTimerRef.current);
        batchTimerRef.current = 0;
      }
      // unmount 前最后 flush 一次，避免最后一次更新丢失
      flushBatch();
    };
  }, [flushBatch]);

  const handleSSEEvent = useCallback((event) => {
    if (!event || !event.session) return;
    const currentSession = sessionNameRef.current;
    if (event.session !== currentSession) return;

    const eventRunId = String(event.run_id || '');
    const activeRun = activeRunRef.current;
    if (eventRunId && activeRun.session === currentSession && activeRun.runId && activeRun.runId !== eventRunId) {
      return;
    }
    if (eventRunId) activeRunRef.current = { session: currentSession, runId: eventRunId };

    const eventId = Number(event.event_id) || 0;
    if (eventId) {
      const cursorKey = sessionRunCursorKey(currentSession, eventRunId);
      const previousId = eventCursorRef.current.get(cursorKey) || 0;
      if (eventId <= previousId) return;
      eventCursorRef.current.set(cursorKey, eventId);
    }
    if ((event.contents && Array.isArray(event.contents)) ||
        (event.data && event.data.messages && Array.isArray(event.data.messages))) {
      batchedSetMessages(prev => [...prev, ...normalizeServiceMessages(event.data ? event : [event])]);
      return;
    }
    if (event.type === 'subtask_event') {
      const t = event.title || 'subtask';
      batchedSetSubtaskStreams(prev => ({
        ...prev,
        [t]: [...(prev[t] || []), event.event]
      }));
      return;
    }
    if (event.type === 'session_usage') {
      // Backend pushes the accumulated session-level usage after each
      // LLM call so the header TOKENS counter ticks up live.
      setStats(prev => ({
        ...prev,
        session: {
          prompt_tokens:     Number(event.prompt_tokens) || 0,
          completion_tokens: Number(event.completion_tokens) || 0,
        },
      }));
      return;
    }
    if (event.type === 'llm_progress') {
      batchedSetLlmProgress(event);
      if (Array.isArray(event.tool_calls) && event.tool_calls.length > 0) {
        const streamedCalls = event.tool_calls;
        batchedSetMessages(prev => {
          const copy = [...prev];
          let index = -1;
          for (let i = copy.length - 1; i >= 0; i--) {
            const msg = copy[i];
            if (msg && msg.role === 'assistant' && msg._streaming) {
              index = i;
              break;
            }
          }
          if (index < 0) {
            copy.push({ role: 'assistant', content: '', tool_calls: streamedCalls, _streaming: true, draft: true, toolArgsStreaming: true });
          } else {
            copy[index] = { ...copy[index], tool_calls: streamedCalls, toolArgsStreaming: true };
          }
          return copy;
        });
      }
      return;
    }
    if (event.type === 'status' && event.message) {
      batchedSetLlmProgress({
        phase: 'status',
        status_text: String(event.message),
        elapsed_ms: 0,
        since_last_delta_ms: 0,
      });
      return;
    }
    if (event.type === 'assistant') {
      const draft = !!event.draft;
      const isReport = /<report\b/i.test(event.content || '');
      // A draft answer is about to be replaced by the post-reflection final;
      // cut its TTS (and discard accumulated audio) so the same reply is not
      // read aloud twice.
      if (draft && !isReport) {
        cutTTSStream();
      }
      batchedSetMessages(prev => {
        const copy = [...prev];
        let streamedResults = null;
        const last = copy[copy.length - 1];
        if (last && last.role === 'assistant' && last._streaming) {
          if (Array.isArray(last._streamResults) && last._streamResults.length) {
            streamedResults = last._streamResults;
          }
          copy.pop();
        }
        const keepStreamedResults = message => (
          streamedResults ? { ...message, _streamResults: streamedResults } : message
        );
        if (isReport && !draft && copy.length) {
          // Replace the previous draft report in this turn with the final report.
          for (let i = copy.length - 1; i >= 0; i--) {
            const m = copy[i];
            if (m && m.role === 'user') break;
            if (m && m.role === 'assistant' && m.draft && /<report\b/i.test(m.content || '')) {
              copy[i] = keepStreamedResults({ role: 'assistant', content: stripThinkingClient(event.content || ''), tool_calls: event.tool_calls || [], replacedDraft: true, thinking: event.thinking || undefined });
              return copy;
            }
          }
        }
        // Collapse repeated final answers within the same turn: when the
        // backend continuation nudge makes the model re-answer, replace the
        // previous final answer instead of stacking duplicates (and cut TTS so
        // the old text is not spoken twice).
        const newContent = event.content || '';
        const hasToolCalls = Array.isArray(event.tool_calls) && event.tool_calls.length > 0;
        if (!draft && !isReport && !hasToolCalls && copy.length) {
          for (let i = copy.length - 1; i >= 0; i--) {
            const m = copy[i];
            if (m && m.role === 'user') break;
            if (m && m.role === 'assistant' && Array.isArray(m.tool_calls) && m.tool_calls.length > 0) break;
            if (m && m.role === 'assistant' && !m._streaming && !m.draft) {
              // Keep the previous answer for <done>-only confirmations.
              if (!newContent.trim() && !event.thinking) return copy;
              cutTTSStream();
              copy[i] = keepStreamedResults({ role: 'assistant', content: stripThinkingClient(newContent), tool_calls: event.tool_calls || [], draft, thinking: event.thinking || undefined });
              return copy;
            }
          }
        }
        copy.push(keepStreamedResults({ role: 'assistant', content: event.content || '', tool_calls: event.tool_calls || [], draft, thinking: event.thinking || undefined }));
        return copy;
      });
    } else if (event.type === 'interrupted') {
      // The agent stopped mid-turn because the host cancelled it. Mark the
      // tail of the conversation as "truncated" so the user can see what was
      // cut off; the next user message will resume the same session.
      batchedSetMessages(prev => {
        const copy = [...prev];
        for (let i = copy.length - 1; i >= 0; i--) {
          const m = copy[i];
          if (!m) continue;
          if (m.role === 'user') break;
          if (m.role === 'assistant') {
            copy[i] = { ...m, truncated: true };
          }
        }
        return copy;
      });
    } else if (event.type === 'injected_user') {
      // A user message the host injected mid-turn. The agent emits this event
      // twice with the same id: once immediately on InjectMessage() and once
      // again at the next step boundary (with pending: false). We dedupe by
      // id so only the latest copy survives; updates in place rather than
      // appending twice.
      // 注入回显和普通历史消息共用同一套 <file_context> 承载附件的约定。
      // 这里必须走 toInjectedUserMessage：事件里只有 content，MessageBubble
      // 靠 msg.files 渲染 chips，不拆的话注入的图片会变成一坨原始 JSON
      // 直接显示在气泡正文里（附件等于白传）。
      const { content: text, files } = toInjectedUserMessage(event.content || '');
      const id = event.id;
      if (text || files.length) {
        batchedSetMessages(prev => {
          if (id != null) {
            const idx = prev.findIndex(m => m && m.role === 'user' && m.injectId === id);
            if (idx >= 0) {
              const copy = [...prev];
              copy[idx] = { ...copy[idx], content: text, files, pending: !!event.pending };
              return copy;
            }
          } else if (!event.pending) {
            // Older agents emitted the step-boundary event without the id.
            // Reconcile it with the pending bubble emitted on enqueue instead
            // of appending the same injected request a second time.
            for (let i = prev.length - 1; i >= 0; i--) {
              const m = prev[i];
              if (!m || m.role !== 'user' || !m.injected || !m.pending) continue;
              if (String(m.content || '') !== String(text)) continue;
              const copy = [...prev];
              copy[i] = { ...m, pending: false };
              return copy;
            }
          }
          return [...prev, { ...toInjectedUserMessage(event.content || '', { injected: true, injectId: id, pending: !!event.pending }) }];
        });
      }
    } else if (event.type === 'assistant/chunk') {
      // Live token streaming: append visible text to the current draft.
      const visChunk = stripThinkingClient(event.content || '');
      if (!visChunk) return;
      batchedSetMessages(prev => {
        const copy = [...prev];
        const last = copy[copy.length - 1];
        if (last && last.role === 'assistant' && last._streaming) {
          // Replace, do not mutate. `last` is a reference into a shallow copy of
          // the previous state, so assigning to last.content also rewrites the
          // object React still holds. StrictMode invokes updaters twice in
          // development, so the second pass appends the same chunk to the value
          // the first pass already changed and every streamed message renders
          // doubled until the final `assistant` event replaces the draft.
          copy[copy.length - 1] = { ...last, content: (last.content || '') + visChunk };
        } else {
          copy.push({ role: 'assistant', content: visChunk, _streaming: true, draft: true });
        }
        return copy;
      });
      feedTTSStream(visChunk);
    } else if (event.type === 'assistant/thinking') {
      // Live chain-of-thought: attach to the current streaming draft so it can
      // render as a collapsible thinking block (Codex-style).
      batchedSetMessages(prev => {
        const copy = [...prev];
        const last = copy[copy.length - 1];
        if (last && last.role === 'assistant' && last._streaming) {
          // Same shape as the content append above: build a new object rather
          // than mutating the one React is still holding.
          copy[copy.length - 1] = { ...last, thinking: (last.thinking || '') + (event.content || '') };
        } else {
          copy.push({ role: 'assistant', content: '', _streaming: true, draft: true, thinking: event.content || '' });
        }
        return copy;
      });
    } else if (event.type === 'tool_score_cleanup') {
      const cleaned = Array.isArray(event.cleaned) ? event.cleaned : [];
      if (!cleaned.length) return;
      const replacements = new Map();
      for (const item of cleaned) {
        const callId = String(item && item.call_id || '').trim();
        const content = item && item.content;
        if (callId && typeof content === 'string' && content) replacements.set(callId, item);
      }
      if (!replacements.size) return;
      batchedSetMessages(prev => {
        let changed = false;
        const updateResults = results => {
          if (!Array.isArray(results) || !results.length) return results;
          let listChanged = false;
          const next = results.map(result => {
            const callId = String(result && result.tool_call_id || result && result.id || '').trim();
            const replacement = replacements.get(callId);
            if (!replacement || result.content === replacement.content) return result;
            listChanged = true;
            return { ...result, content: replacement.content, cleaned: true, score: replacement.score };
          });
          if (listChanged) changed = true;
          return listChanged ? next : results;
        };
        const copy = prev.map(message => {
          if (!message) return message;
          let next = message;
          if (Array.isArray(next._streamResults)) {
            const streamResults = updateResults(next._streamResults);
            if (streamResults !== next._streamResults) next = { ...next, _streamResults: streamResults };
          }
          if (Array.isArray(next._pairedResults)) {
            const pairedResults = updateResults(next._pairedResults);
            if (pairedResults !== next._pairedResults) next = { ...next, _pairedResults: pairedResults };
          }
          if (next.role === 'tool') {
            const callId = String(next.tool_call_id || '').trim();
            const replacement = replacements.get(callId);
            if (replacement && next.content !== replacement.content) {
              changed = true;
              next = { ...next, content: replacement.content, cleaned: true, score: replacement.score };
            }
          }
          return next;
        });
        return changed ? copy : prev;
      });
    } else if (event.type === 'tool_result' || (event.type === 'tool_call' && event.status === 'end')) {
      const toolName = event.tool || event.name || '';
      const rawToolResult = event.result !== undefined ? event.result : (event.content || '');
      if (toolName === 'browser' && rawToolResult) {
        let parsedToolResult = rawToolResult;
        if (typeof parsedToolResult === 'string') {
          try { parsedToolResult = JSON.parse(parsedToolResult); } catch { parsedToolResult = null; }
        }
        const artifact = parsedToolResult && (parsedToolResult.artifact || (parsedToolResult.result && parsedToolResult.result.artifact));
        if (artifact && artifact.kind === 'browser-live' && (artifact.page_url || artifact.live_url)) {
          openPreview({ ...artifact, kind: 'browser-live' });
        }
      }
      batchedSetMessages(prev => {
        const copy = [...prev];
        const callId = event.call_id || event.tool_call_id || '';
        const name = toolName;
        const content = event.result !== undefined ? event.result : (event.content || '');
        const resultEntry = {
          tool_call_id: callId,
          name,
          content: content || (event.error ? JSON.stringify({ error: event.error }) : ''),
          timestamp: event.ts || event.timestamp || Date.now(),
        };
        for (let i = copy.length - 1; i >= 0; i--) {
          const m = copy[i];
          if (m.role === 'assistant' && m.tool_calls && m.tool_calls.length > 0) {
            const matchIdx = callId
              ? m.tool_calls.findIndex(tc => (tc.id || tc.tool_call_id || '') === callId)
              : -1;
            if (matchIdx >= 0 || callId === '') {
              const nextResults = Array.isArray(m._streamResults) ? [...m._streamResults] : [];
              const existingIdx = callId
                ? nextResults.findIndex(result => String(result && result.tool_call_id || '') === callId)
                : -1;
              if (existingIdx >= 0) nextResults[existingIdx] = resultEntry;
              else nextResults.push(resultEntry);
              copy[i] = { ...m, _streamResults: nextResults };
              break;
            }
          }
        }
        return copy;
      });
    } else if (event.type === 'branch_created') {
      const branch = {
        name: event.branch_session,
        parent: event.parent_session || event.session || sessionNameRef.current,
        domain: event.domain || event.branch_session,
      };
      if (branch.name) {
        if (event.status === 'running') {
          setRunningSubtasks(prev => ({ ...prev, [branch.name]: branch }));
        } else {
          setRunningSubtasks(prev => {
            const next = { ...prev };
            delete next[branch.name];
            return next;
          });
          pendingBranchRef.current = branch;
          setPendingBranch(branch);
        }
        refreshSessions();
      }
    } else if (event.type === 'done') {
      // Prefer the server-computed real_ms (derived from message ts timestamps)
      // on the final assistant message; fall back to client wall-clock for
      // legacy sessions / backends that do not stamp real_ms yet.
      let realMs = 0;
      const prodMsgs = event.messages && event.messages.data && event.messages.data.messages;
      if (prodMsgs) {
        for (let i = prodMsgs.length - 1; i >= 0; i--) {
          const m = prodMsgs[i];
          if (m && m.role === 'assistant' && m.content && Number(m.real_ms) > 0) { realMs = Number(m.real_ms); break; }
        }
      }
      if (!realMs) realMs = sendStartRef.current ? (Date.now() - sendStartRef.current) : 0;
      if (realMs > 0) {
        setSessionRealMs(prev => {
          const next = prev + realMs;
          if (sessionNameRef.current) {
            try { localStorage.setItem('real_ms_' + sessionNameRef.current, String(next)); } catch {}
            try {
              fetch('/api/sessions/' + encodeURIComponent(sessionNameRef.current) + '/real', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json; charset=utf-8' },
                body: JSON.stringify({ real_ms: realMs })
              }).catch(() => {});
            } catch {}
          }
          return next;
        });
      }
      if (event.usage || event.session_usage) {
        setStats(prev => ({
          ...prev,
          session: event.session_usage || event.usage || prev.session
        }));
      }
            if (event.messages) {
        const normalized = applyTurnRealToUsers(collapseDraftReports(normalizeServiceMessages(event.messages)));
        if (realMs > 0) {
          // live: stamp this turn's real onto its user message
          for (let i = normalized.length - 1; i >= 0; i--) {
            if (normalized[i].role === 'user') { normalized[i].real_ms = realMs; break; }
          }
        }
        const requestedInteractionId = String(event.interaction_id || '').trim();
        const resultMessage = [...normalized].reverse().find(m => {
          if (!m || m.role !== 'assistant' || m.id == null) return false;
          if (!requestedInteractionId) return true;
          return String(m.interaction_id || m.turn_id || '') === requestedInteractionId;
        });
        const pendingArtifactsByKey = pendingArtifactsRef.current;
        const interactionKey = interactionArtifactKey(requestedInteractionId || (resultMessage && (resultMessage.interaction_id || resultMessage.turn_id)));
        const runKey = runArtifactKey(event.run_id);
        const pendingArtifacts = (interactionKey && pendingArtifactsByKey[interactionKey])
          || (runKey && pendingArtifactsByKey[runKey])
          || [];
        if (resultMessage && pendingArtifacts.length) {
          attachArtifactsToMessage(interactionKey || String(resultMessage.id), pendingArtifacts);
        }
        if (interactionKey) delete pendingArtifactsByKey[interactionKey];
        if (runKey) delete pendingArtifactsByKey[runKey];
        setMessages(normalized);
        const lastAssistant = [...normalized].reverse().find(m => m.role === 'assistant' && m.content && m.content.trim());
        if (lastAssistant) {
          const content = lastAssistant.content || '';
          const report = content.match(/<report[^>]*>([\s\S]*?)<\/report>/i);
          const readable = stripReflectionTags(report ? report[1] : extractAssistantContent(content)).replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ').trim();
          flushTTS();
        }
        if (lastAssistant && lastAssistant.id != null) {
          ttsTurnMsgIdRef.current = lastAssistant.id;
          ttsTurnDoneRef.current = true;
          if (!ttsPlayingRef.current && !ttsQueueRef.current.length) maybeFinalizeTTSTurn();
        }
      }
      const branch = pendingBranchRef.current;
      if (!event.background_running) {
        setRunningSubtasks({});
        setBackgroundActive(false);
      } else {
        setBackgroundActive(true);
      }
      if (branch && branch.name) {
        pendingBranchRef.current = null;
        setPendingBranch(null);
        window.setTimeout(() => loadSession(branch.name), 180);
      }
    } else if (event.type === 'request_done') {
      const realMs = sendStartRef.current ? (Date.now() - sendStartRef.current) : 0;
      setStats(prev => ({
        current: {
          usage: event.usage || null,
          duration_ms: event.duration_ms || 0,
          real_ms: realMs || prev.current?.real_ms
        },
        session: event.session_usage || prev.session
      }));
    } else if (event.type === 'waiting_user_input') {
      setPendingAskUser({
        askType: event.ask_type,
        questions: normalizeAskQuestions(event),
        session: event.session || streamSessionRef.current || sessionNameRef.current || sessionName
      });
    } else if (event.type === 'file_result') {
      const files = Array.isArray(event.files)
        ? event.files.filter(file => file && file.path)
        : [];
      if (files.length) {
        const key = interactionArtifactKey(event.interaction_id) || runArtifactKey(event.run_id) || 'latest';
        pendingArtifactsRef.current[key] = files;
      }
    } else if (event.type === 'error') {
      setMessages(prev => [...prev, { role: 'system', content: 'ERROR: ' + (event.error || '') }]);
    }
  }, [maybeFinalizeTTSTurn, cutTTSStream, openPreview, attachArtifactsToMessage, loadSession, refreshSessions]);

  // A detached subtask can outlive the main SSE turn. Keep a lightweight
  // status poll while background work exists, then reconnect to the normal
  // event stream as soon as the server starts the continuation run.
  useEffect(() => {
    const hasRunningBranches = Object.keys(runningSubtasks || {}).length > 0;
    if (loading || !sessionName || (!hasRunningBranches && !backgroundActive)) return undefined;
    let cancelled = false;
    const poll = async () => {
      try {
        const st = await fetch('/api/sessions/' + encodeURIComponent(sessionName) + '/status').then(r => r.json());
        if (cancelled || !st) return;
        if (st.running) {
          activeRunRef.current = { session: sessionName, runId: st.run_id || '' };
          setLoading(true);
          setEventResume({ name: sessionName, runId: st.run_id || '', nonce: Date.now() });
          return;
        }
        if (Number(st.background_running) > 0 || st.resume_pending) return;
        const res = await fetchSessionMessages(sessionName);
        if (cancelled) return;
        setMessages(applyTurnRealToUsers(collapseDraftReports(normalizeServiceMessages(res))));
        setRunningSubtasks({});
        setBackgroundActive(false);
        refreshSessions();
      } catch { /* keep polling */ }
    };
    poll();
    const timer = window.setInterval(poll, 2000);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [loading, sessionName, runningSubtasks, backgroundActive, refreshSessions]);

  // Resume a running session's event stream after the user switches back to
  // it. The server replays buffered events after the cursor and then keeps the
  // connection open, so tool calls, thinking text and assistant deltas continue
  // without polling or spawning a second agent.
  useEffect(() => {
    if (!eventResume || !eventResume.name) return undefined;
    const name = eventResume.name;
    const expectedRunId = String(eventResume.runId || '');
    const cursorKey = sessionRunCursorKey(name, expectedRunId);
    const controller = new AbortController();
    if (eventStreamRef.current) eventStreamRef.current.abort();
    eventStreamRef.current = controller;
    let reconnectTimer = null;

    const consumeLine = line => {
      if (!line.startsWith('data: ')) return;
      const raw = line.slice(6);
      if (!raw || raw === '[DONE]') return;
      let event;
      try { event = JSON.parse(raw); } catch { return; }
      if (!event || !event.session || event.session !== name) return;
      if (expectedRunId && event.run_id && String(event.run_id) !== expectedRunId) return;
      if (event.type === 'stream_state') return;
      const eventId = Number(event.event_id) || 0;
      const previousId = eventCursorRef.current.get(cursorKey) || 0;
      if (eventId && eventId <= previousId) return;
      handleSSEEvent(event);
      if (eventId) eventCursorRef.current.set(cursorKey, eventId);
    };

    const connect = async () => {
      const since = eventCursorRef.current.get(cursorKey) || 0;
      const params = new URLSearchParams();
      if (expectedRunId) params.set('run_id', expectedRunId);
      if (since) params.set('since', String(since));
      try {
        const response = await fetch('/api/sessions/' + encodeURIComponent(name) + '/events?' + params.toString(), {
          signal: controller.signal,
          headers: { Accept: 'text/event-stream' },
        });
        if (!response.ok || !response.body) throw new Error('event stream HTTP ' + response.status);
        const reader = response.body.getReader();
        const decoder = new TextDecoder();
        let buffer = '';
        while (!controller.signal.aborted) {
          const { done, value } = await reader.read();
          if (done) break;
          buffer += decoder.decode(value, { stream: true });
          const lines = buffer.split('\n');
          buffer = lines.pop() || '';
          for (const line of lines) consumeLine(line);
        }
        if (buffer) consumeLine(buffer);
      } catch (error) {
        if (error && error.name === 'AbortError') return;
      }

      if (controller.signal.aborted || sessionNameRef.current !== name) return;
      try {
        const status = await fetch('/api/sessions/' + encodeURIComponent(name) + '/status').then(r => r.json());
        if (status && status.running) {
          setLoading(true);
          reconnectTimer = window.setTimeout(() => {
            setEventResume(prev => prev && prev.name === name
              ? { name, runId: status.run_id || expectedRunId, nonce: Date.now() }
              : prev);
          }, 400);
          return;
        }
        const res = await fetchSessionMessages(name);
        if (sessionNameRef.current !== name) return;
        setMessages(applyTurnRealToUsers(collapseDraftReports(normalizeServiceMessages(res))));
        setLoading(false);
        setEventResume(prev => prev && prev.name === name ? null : prev);
        refreshSessions();
      } catch {
        if (sessionNameRef.current === name) setLoading(false);
      }
    };

    connect();
    return () => {
      if (reconnectTimer) window.clearTimeout(reconnectTimer);
      controller.abort();
      if (eventStreamRef.current === controller) eventStreamRef.current = null;
    };
  }, [eventResume, handleSSEEvent, refreshSessions]);

  // Download current session folder as zip
  const downloadSession = useCallback(() => {
    if (!sessionName) return;
    const a = document.createElement('a');
    a.href = '/api/sessions/' + encodeURIComponent(sessionName) + '/download';
    a.download = sessionName + '.zip';
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
  }, [sessionName]);

  const exportTraceHtml = useCallback(() => {
    if (!sessionName) return;
    fetchSessionTrace(sessionName, { full: true })
      .then(data => downloadFile(sessionName + '.trace.html', buildTraceHtml(sessionName, data)))
      .catch(e => alert('导出 Trace 失败: ' + e));
  }, [sessionName]);
  const handleAskComplete = useCallback((answers) => {
    const targetSession = pendingAskUser?.session || sessionNameRef.current;
    setPendingAskUser(null);
    if (answers.pptConfig) {
      send(formatPptConfigMessage(answers.pptConfig), [], targetSession);
      return;
    }
    // Confirmation mode (empty questions, user clicked "确认")
    if (answers.confirmed) {
      const msg = '[用户确认了] ' + (answers.askType || '');
      send(msg, [], targetSession);
      return;
    }
    // Question mode: format answers as text to send back to AI
    const qs = normalizeAskQuestions(pendingAskUser);
    const lines = qs.map((q, qi) => {
      const qid = q.id || ('q' + (qi + 1));
      const selected = answers[qid] || [];
      const free = answers[qid + '_free_text'] || '';
      let txt = 'Q' + (qi + 1) + ': ' + (q.question || q.title || '');
      if (selected.length) txt += ' → 选择: ' + selected.join(', ');
      if (free) txt += ' | 补充: ' + free;
      return txt;
    });
    const msg = '[用户已回答了问题]\n' + lines.join('\n');
    send(msg, [], targetSession);
  }, [pendingAskUser, send]);

  const handleAskSkip = useCallback(() => {
    const targetSession = pendingAskUser?.session || sessionNameRef.current;
    setPendingAskUser(null);
    const msg = '[用户跳过了提问]';
    send(msg, [], targetSession);
  }, [send, pendingAskUser]);

  const abort = useCallback((opts) => {
    // opt.hard === true => immediate kill (abandon in-flight work).
    // opt.hard === false (or undefined) => soft stop: agent finishes the
    // current step, drains any queued user messages, then exits cleanly.
    const hard = !!(opts && opts.hard);
    if (hard && abortRef.current) {
      // Hard stop closes the SSE stream AND signals the server to kill the
      // process tree. Same UX as the legacy "Stop" button.
      abortRef.current.abort();
      abortRef.current = null;
      setLoading(false);
    }
    if (sessionNameRef.current) {
      const endpoint = hard
        ? '/api/chat/cancel'
        : '/api/chat/stop';
      try {
        fetch(endpoint, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json; charset=utf-8' },
          body: JSON.stringify({ session: sessionNameRef.current }),
        }).catch(() => {});
      } catch {}
    }
  }, []);


  // Sum agent (LLM) time & tokens across this session AND every finished
  // create_subtask nested in its tool results, so the header shows the whole
  // tree, not just the main thread.
  const subtaskAgentStats = useMemo(() => {
    const sum = { prompt_tokens: 0, completion_tokens: 0, duration_ms: 0 };
    const absorbSubtask = (contentStr) => {
      let inner = null;
      try { inner = JSON.parse(contentStr || '{}'); } catch {}
      if (!inner) return;
      if (inner.agent_stats) {
        sum.prompt_tokens += inner.agent_stats.prompt_tokens || 0;
        sum.completion_tokens += inner.agent_stats.completion_tokens || 0;
        sum.duration_ms += inner.agent_stats.duration_ms || 0;
      } else if (Array.isArray(inner.messages)) {
        visit(inner.messages);
      }
    };
    // Fully count a SUBTASK session: its assistant durations/usage plus any
    // nested create_subtask results (used only for nested sessions).
    const visit = (list) => {
      for (const m of list || []) {
        if (!m) continue;
        if (m.role === 'assistant') {
          if (m.usage) {
            sum.prompt_tokens += m.usage.prompt_tokens || 0;
            sum.completion_tokens += m.usage.completion_tokens || 0;
          }
          if (m.duration_ms) sum.duration_ms += m.duration_ms;
          // server-loaded history nests tool results inside contents[]
          if (Array.isArray(m.contents)) {
            for (const blk of m.contents) {
              if (blk && blk.type === 'tool_result' && blk.name === 'create_subtask') {
                absorbSubtask(typeof blk.content === 'string' ? blk.content : JSON.stringify(blk.content || '{}'));
              }
            }
          }
        } else if (m.role === 'tool' && (m.name === 'create_subtask')) {
          absorbSubtask(typeof m.content === 'string' ? m.content : JSON.stringify(m.content || '{}'));
        }
      }
    };
    // Main thread: ONLY absorb subtask stats from create_subtask results.
    // Main-thread durations/tokens already live in stats.session (usage.json),
    // so they must NOT be added here (previously double-counted).
    for (const m of messages || []) {
      if (!m) continue;
      if (m.role === 'assistant' && Array.isArray(m.contents)) {
        for (const blk of m.contents) {
          if (blk && blk.type === 'tool_result' && blk.name === 'create_subtask') {
            absorbSubtask(typeof blk.content === 'string' ? blk.content : JSON.stringify(blk.content || '{}'));
          }
        }
      } else if (m.role === 'tool' && (m.name === 'create_subtask')) {
        absorbSubtask(typeof m.content === 'string' ? m.content : JSON.stringify(m.content || '{}'));
      }
    }
    return sum;
  }, [messages]);

  // ── Streaming TTS: sentence-split the streamed reply and play in a queue ──
  const ttsQueueRef = useRef([]);
  const ttsPlayingRef = useRef(false);
  const ttsPendingRef = useRef('');
  const ttsStoppedRef = useRef(false);
  const mutedRef = useRef(muted);
  mutedRef.current = muted;
  const ttsNextTimeRef = useRef(0);
  const ttsActiveSourcesRef = useRef(new Set());

  const synthesizeTTSStream = useCallback((text, onChunk) => new Promise((resolve) => {
    let settled = false;
    const finish = () => { if (!settled) { settled = true; resolve(); } };
    fetch('/voice/api/tts/stream', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json; charset=utf-8' },
      body: JSON.stringify({ text: text.slice(0, 200) }),
    }).then(async (r) => {
      if (!r.ok || !r.body) { finish(); return; }
      const reader = r.body.getReader();
      const decoder = new TextDecoder();
      let buffer = '';
      try {
        while (true) {
          const { done, value } = await reader.read();
          if (done) break;
          buffer += decoder.decode(value, { stream: true });
          let idx;
          while ((idx = buffer.indexOf('\n\n')) >= 0) {
            const evtText = buffer.slice(0, idx);
            buffer = buffer.slice(idx + 2);
            for (const line of evtText.split('\n')) {
              if (!line.startsWith('data: ')) continue;
              try {
                const evt = JSON.parse(line.slice(6));
                if (evt.type === 'audio') onChunk(evt.audio_b64, evt.sample_rate || 48000);
                else if (evt.type === 'done' || evt.type === 'error') finish();
              } catch { /* skip malformed */ }
            }
          }
        }
      } catch { /* stream closed */ }
      finish();
    }).catch(finish);
  }), []);

  // Gapless scheduling: each SSE chunk is scheduled right after the previous one.
  const playTTSChunk = useCallback((audioB64, sampleRate) => {
    try {
      const raw = Uint8Array.from(atob(audioB64), ch => ch.charCodeAt(0));
      const f32 = new Float32Array(raw.buffer);
      ttsTurnChunksRef.current.push(f32.slice());
      ttsTurnSrRef.current = sampleRate || 48000;
      const ctx = audioCtxRef.current || (audioCtxRef.current = new (window.AudioContext || window.webkitAudioContext)());
      if (ctx.state === 'suspended') ctx.resume();
      const buf = ctx.createBuffer(2, Math.floor(f32.length / 2), sampleRate || 48000);
      for (let ch = 0; ch < 2; ch++) {
        const data = buf.getChannelData(ch);
        const half = Math.floor(f32.length / 2);
        for (let i = 0; i < half; i++) data[i] = f32[i * 2 + ch];
      }
      const src = ctx.createBufferSource();
      src.buffer = buf;
      src.connect(ctx.destination);
      const now = ctx.currentTime;
      // The first chunk gets an initial buffering delay so the AudioContext has
      // time to spin up and the first word is never clipped; later chunks are
      // scheduled gaplessly right after the previous one.
      let when;
      if (ttsNextTimeRef.current <= 0 || ttsNextTimeRef.current < now) {
        when = now + 0.3;
      } else {
        when = ttsNextTimeRef.current;
      }
      ttsNextTimeRef.current = when + buf.duration;
      ttsActiveSourcesRef.current.add(src);
      src.onended = () => { ttsActiveSourcesRef.current.delete(src); };
      src.start(when);
    } catch (e) { /* voice off is fine */ }
  }, []);

  const drainTTS = useCallback(async () => {
    if (ttsPlayingRef.current || ttsStoppedRef.current) return;
    const seg = ttsQueueRef.current.shift();
    if (!seg) { maybeFinalizeTTSTurn(); return; }
    ttsPlayingRef.current = true;
    try {
      await synthesizeTTSStream(seg, (b64, sr) => {
        if (!ttsStoppedRef.current) playTTSChunk(b64, sr);
      });
    } catch (e) { /* voice off is fine */ }
    finally {
      ttsPlayingRef.current = false;
      if (!ttsStoppedRef.current) drainTTS();
      else maybeFinalizeTTSTurn();
    }
  }, [synthesizeTTSStream, playTTSChunk, maybeFinalizeTTSTurn]);

  const enqueueTTS = useCallback((text) => {
    if (!TTS_ENABLED || mutedRef.current || !text || !text.trim()) return;
    ttsStoppedRef.current = false;
    const clean = cleanTTSText(text);
    if (!clean) return;
    if (/^[\s\-*•#\d.、)）]+$/.test(clean)) return;
    ttsQueueRef.current.push(clean);
    drainTTS();
  }, [drainTTS]);

  const feedTTSStream = useCallback((chunk) => {
    if (!TTS_ENABLED || mutedRef.current) return;
    const clean = sanitizeForTTS(chunk);
    if (!clean) return;
    const { sentences, remainder } = splitSentences(ttsPendingRef.current + clean);
    ttsPendingRef.current = remainder;
    // Merge heading fragments (e.g. "政府工作报告：") into the following
    // sentence: a bare "标题：" makes the small TTS model run away into a long
    // stretch of garbled audio (observed 30s of noise from a 7-char heading).
    // Keep accumulating while the merged text still ends with ：, so nested
    // headings and titles of any length are never sent alone.
    for (const c of buildTTSChunks(sentences)) enqueueTTS(c);
  }, [enqueueTTS, sanitizeForTTS]);

  const flushTTS = useCallback(() => {
    const tail = cleanTTSText(ttsPendingRef.current);
    ttsPendingRef.current = '';
    if (tail) enqueueTTS(tail);
  }, [enqueueTTS]);

  const resetTTS = useCallback(() => {
    maybeFinalizeTTSTurn();
    ttsStoppedRef.current = true;
    ttsQueueRef.current = [];
    ttsPendingRef.current = '';
    ttsTurnChunksRef.current = [];
    ttsTurnSrRef.current = 48000;
    ttsTurnMsgIdRef.current = null;
    ttsTurnDoneRef.current = false;
    ttsXmlBlockRef.current = null;
    ttsInCodeFenceRef.current = false;
    ttsMarkdownReflectionRef.current = false;
    for (const src of ttsActiveSourcesRef.current) { try { src.stop(); } catch { /* not started yet */ } }
    ttsActiveSourcesRef.current.clear();
    ttsNextTimeRef.current = 0;
    ttsPlayingRef.current = false;
  }, [maybeFinalizeTTSTurn]);

  useEffect(() => {
    if (muted) resetTTS();
  }, [muted, resetTTS]);

  const sessionStatsTotal = useMemo(() => {
    const base = (stats && stats.session) || {};
    const hasTraceTotals = Number(base.trace_llm_calls) > 0;
    return {
      prompt_tokens: (Number(base.prompt_tokens) || 0) + (hasTraceTotals ? 0 : subtaskAgentStats.prompt_tokens),
      completion_tokens: (Number(base.completion_tokens) || 0) + (hasTraceTotals ? 0 : subtaskAgentStats.completion_tokens),
      real_ms: Number(base.real_ms) || 0,
      trace_llm_calls: Number(base.trace_llm_calls) || 0,
    };
  }, [stats, subtaskAgentStats]);
  const liveSessionRealMs = sessionRealMs + (
    loading && sendStartRef.current
      ? Math.max(0, clockNow - sendStartRef.current)
      : 0
  );
  return (
    <div className={'app' + (embeddedPreview ? ' app-embedded-preview' : '')}>
      {/* <VersionNav mode={mode} onChange={setMode} /> temporarily disabled (results-only view) */}
      <Sidebar
        sessions={sessions}
        sessionName={sessionName}
        onSelect={loadSession}
        onNew={newChat}
        onNewBranch={newChatBranch}
        onDeleteSession={handleDeleteSession}
        autoCollapse={Boolean(previewFile)}
        runningSubtasks={runningSubtasks}
      />
      <div className="main">
        {theme === 'zzz' && <AmbientBackdrop />}
        <header className="app-header">
          <div className="header-identity">
            <span className="header-brand-mark" aria-hidden="true">
              <img src="/fairy.png" alt="" draggable={false} />
            </span>
            <span className="header-copy">
              <span className="header-kicker">FAIRY // AGENT WORKBENCH</span>
              <strong title={sessionName}>{sessionName || 'NEW SESSION'} · {Number(paging.displayTotal) || messages.length} MSG</strong>
            </span>
            <span className={'header-live' + (loading ? ' busy' : '')}>
              <i />
              {loading ? 'RUNNING' : 'READY'}
            </span>
          </div>

          <div className="header-telemetry" aria-label="运行摘要">
            <button
              type="button"
              className="header-telemetry-item header-telemetry-trigger"
              onClick={() => setShowTrace(true)}
              title="查看本会话 trace 瀑布"
              aria-label="查看本会话 trace"
            >
              <small>MODEL</small>
              <b title={selectedModel}>{selectedModel || 'NONE'}</b>
            </button>
            <button
              type="button"
              className="header-telemetry-item header-telemetry-trigger"
              onClick={() => setShowTrace(true)}
              title="查看本会话 token 消耗明细（trace 瀑布）"
              aria-label="查看本会话 token 明细"
            >
              <small>TOKENS</small>
              <b title={`全部主线程与子任务的 prompt + completion token；prompt ${formatHeaderTokens(sessionStatsTotal.prompt_tokens)} / completion ${formatHeaderTokens(sessionStatsTotal.completion_tokens)}${sessionStatsTotal.trace_llm_calls ? "；LLM 调用 " + sessionStatsTotal.trace_llm_calls + " 次" : ""}`}>{formatHeaderTokens(sessionStatsTotal.prompt_tokens + sessionStatsTotal.completion_tokens)}</b>
            </button>
            <button
              type="button"
              className="header-telemetry-item header-telemetry-trigger"
              onClick={() => setShowTrace(true)}
              title="查看本会话实际耗时明细（trace 瀑布）"
              aria-label="查看本会话耗时明细"
            >
              <small>AGENT</small>
              <b title="实际耗时（由消息 timestamp / real_ms 计算，运行中按当前时间实时累加，不再使用 usage duration）">{formatHeaderDuration(Math.max(sessionStatsTotal.real_ms, liveSessionRealMs))}</b>
            </button>
          </div>

          <div className="header-actions">
            <button
              type="button"
              className="settings-trigger tl-trigger"
              data-testid="open-timeline"
              title="视频时间轴"
              onClick={() => setTimelineOpen(true)}
            >
              <span aria-hidden="true">⏱</span>时间轴
            </button>
            <button
              type="button"
              className="settings-trigger header-voice-trigger"
              onClick={() => { window.location.hash = '#/voice'; }}
              title="进入语音对话界面"
              aria-label="进入语音对话界面"
            >
              <span className="settings-trigger-icon" aria-hidden="true">
                <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                  <rect x="9" y="2" width="6" height="12" rx="3" />
                  <path d="M5 10a7 7 0 0 0 14 0" />
                  <line x1="12" y1="19" x2="12" y2="22" />
                </svg>
              </span>
              <span className="settings-trigger-copy">
                <strong>VOICE</strong>
                <small>语音对话</small>
              </span>
            </button>
            <button
              type="button"
              className="settings-trigger header-files-trigger"
              onClick={() => openPreview({ kind: 'workspace', name: '工作区文件' })}
              title="打开工作区文件目录"
            >
              <span className="settings-trigger-icon" aria-hidden="true">▤</span>
              <span className="settings-trigger-copy">
                <strong>FILES</strong>
                <small>工作区与产物</small>
              </span>
            </button>
            <button
              type="button"
              className="settings-trigger header-settings-trigger"
              onClick={() => setSettingsOpen(true)}
              title="打开设置面板"
              aria-label="设置"
            >
              <span className="settings-trigger-icon" aria-hidden="true">⚙</span>
              <span className="settings-trigger-copy">
                <strong>设置</strong>
                <small>模型 / 语音 / 外观</small>
              </span>
            </button>
            <SettingsMenu
              open={settingsOpen}
              onClose={() => setSettingsOpen(false)}
              models={models}
              selectedModel={selectedModel}
              onSelectModel={handleModelChange}
              theme={theme}
              onThemeChange={handleThemeChange}
              voiceAutoRead={!muted}
              onVoiceAutoReadChange={handleVoiceAutoReadChange}
              autoFallback={autoFallback}
              onAutoFallbackChange={handleAutoFallbackChange}
              fallbackModel={fallbackModel}
              onFallbackModelChange={handleFallbackModelChange}
              onModelsChanged={handleModelsChanged}
            />
          </div>
        </header>
        {pendingBranch ? (
          <div className="branch-created-banner">
            <span>已创建子会话：{pendingBranch.domain}</span>
            <button
              type="button"
              onClick={() => {
                const branch = pendingBranch;
                setPendingBranch(null);
                pendingBranchRef.current = null;
                loadSession(branch.name);
              }}
            >
              进入
            </button>
          </div>
        ) : null}
        {!embeddedPreview && showTrace && <TracePanel name={sessionName} onClose={() => setShowTrace(false)} />}
        <div className={'content-row' + (previewFile ? ' preview-open' : '')}>
          <div className="chat-col">
            <SubtaskStreamContext.Provider value={subtaskStreams}>
              <ChatArea
                key={sessionName || 'new'}
                messages={messages}
                loading={loading}
                llmProgress={llmProgress}
                mode={mode}
                stats={stats}
                sessionName={sessionName}
                onOpenSubtask={openSubtaskSession}
                onOpenFile={openPreview}
                artifactsByMessage={artifactsByMessage}
                onFilesDropped={files => inputBarRef.current?.addFiles?.(files)}
                onDeleteInteraction={deleteInteractionMemory}
                onRegenerate={regenerateInteraction}
                paging={paging}
                loadOlder={loadOlder}
                loadingOlder={loadingOlder}
              />
            </SubtaskStreamContext.Provider>
            <InputBar
              ref={inputBarRef}
              onSend={send}
              loading={loading}
              onAbort={abort}
              sessionName={sessionName}
              onEnsureSession={ensureSession}
            />
          </div>
          {!embeddedPreview && previewFile && createPortal(
            <div className="file-viewer-layer">
              <FilePreviewPanel
                file={previewFile}
                onClose={() => setPreviewFile(null)}
                onOpenFile={openPreview}
                sessionName={sessionName}
              />
            </div>,
            document.body,
          )}
        </div>
        {timelineOpen && (
          <TimelineEditor onClose={() => setTimelineOpen(false)} />
        )}
        {pendingAskUser && (
          isPPTConfigAsk(pendingAskUser) ? (
            <PPTConfigModal onComplete={handleAskComplete} onSkipAll={handleAskSkip} />
          ) : isPPTOutlineAsk(pendingAskUser) ? (
            <PPTOutlineConfirmModal session={pendingAskUser.session} onComplete={handleAskComplete} onSkipAll={handleAskSkip} />
          ) : (
            <AskModal
              questions={pendingAskUser.questions}
              askType={pendingAskUser.askType}
              onComplete={handleAskComplete}
              onSkipAll={handleAskSkip}
            />
          )
        )}
      </div>
    </div>
  );
}
