import React, { useState, useRef, useEffect, useCallback, useMemo } from 'react';
import { sendChat, normalizeServiceMessages, extractAssistantContent, stripReflectionTags, fetchSessions, fetchSessionMessages, fetchModels, splitFileContext } from '../../api/chat';
import { sessionDateLabel } from '../../utils/sessionDate';
import {
  splitSentences,
  buildTTSChunks,
  selectDialogueSpeechText,
  selectDialogueSpeechSegments,
  clampDialogueText,
} from '../../utils/ttsText';
import { normalizeAskQuestions } from '../../utils/askUser';
import {
  pickValidFallbackModel,
  readAutoFallback,
  readFallbackModel,
  writeAutoFallback,
  writeFallbackModel,
} from '../../utils/modelPreference';

// TTS_ENABLED: mirrors App.jsx. false stops /voice/api/tts fetches when the
// local voice service (:8787) is not running.
const TTS_ENABLED = true;

function readableText(content) {
  if (!content) return '';
  const base = stripReflectionTags(extractAssistantContent(content));
  return selectDialogueSpeechText(base);
}

function cleanSessionPreview(value) {
  const decoded = String(value || '')
    .replace(/&lt;/gi, '<')
    .replace(/&gt;/gi, '>')
    .replace(/&amp;/gi, '&');
  const { text } = splitFileContext(decoded);
  return text
    .replace(/&#x?[0-9a-f]+;/gi, ' ')
    .replace(/(?:\*\*)?\[\s*\](?:\*\*)?/g, ' ')
    .replace(/<[^>]+>/g, ' ')
    .replace(/\s+/g, ' ')
    .trim();
}

const STT_CONTEXT_STOPWORDS = new Set([
  'the', 'and', 'for', 'with', 'this', 'that', 'from', 'have', 'will', 'you',
  'are', 'not', 'can', 'but', 'was', 'were', 'has', 'had', 'our', 'its',
  'into', 'when', 'then', 'than', 'they', 'them', 'been', 'also', 'use',
  'using', 'file', 'path', 'code', 'true', 'false',
]);

function buildSttContext(messages) {
  const source = (Array.isArray(messages) ? messages : [])
    .slice(-8)
    .map(message => String(message?.text || message?.content || ''))
    .join(' ');
  const terms = [];
  const add = (value) => {
    const term = String(value || '').replace(/\s+/g, ' ').trim();
    if (term && term.length >= 2 && !STT_CONTEXT_STOPWORDS.has(term.toLowerCase())) {
      terms.push(term);
    }
  };
  for (const match of source.matchAll(/[“"']([^”"']{2,30})[”"']/g)) add(match[1]);
  for (const match of source.matchAll(/[A-Za-z][A-Za-z0-9_.-]{2,}/g)) add(match[0]);
  for (const match of source.matchAll(/[A-Za-z]:\\[^\s，。；;]+/g)) add(match[0]);
  return Array.from(new Set(terms)).slice(-24).join(', ').slice(0, 600);
}

function historyMessagesFromPayload(payload) {
  return normalizeServiceMessages(payload)
    .filter(message => message.role === 'user' || message.role === 'assistant')
    .map(message => {
      const text = message.role === 'user'
        ? cleanSessionPreview(message.content)
        : readableText(message.content);
      return { role: message.role === 'user' ? 'user' : 'fairy', text };
    })
    .filter(message => message.text);
}

function normalizeVoiceAnswer(text) {
  return String(text || '')
    .toLowerCase()
    .replace(/[\s,，。.!！?？:：;；'"“”‘’、\-]/g, '')
    .trim();
}

function voiceNumber(value) {
  const token = normalizeVoiceAnswer(value);
  const digits = token.match(/\d+/);
  if (digits) return Number(digits[0]);
  const chinese = { 一: 1, 二: 2, 两: 2, 三: 3, 四: 4, 五: 5, 六: 6, 七: 7, 八: 8, 九: 9, 十: 10 };
  const match = token.match(/[一二两三四五六七八九十]+/);
  return match ? (chinese[match[0]] || 0) : 0;
}

function findVoiceOptionIndex(text, options) {
  const normalized = normalizeVoiceAnswer(text);
  if (!normalized || !Array.isArray(options)) return -1;
  const numbered = normalized.match(/(?:选|选项|第)?([0-9]+|[一二两三四五六七八九十]+)(?:个|项|号)?/);
  if (numbered) {
    const index = voiceNumber(numbered[1]) - 1;
    if (index >= 0 && index < options.length) return index;
  }
  for (let index = 0; index < options.length; index += 1) {
    const label = normalizeVoiceAnswer(options[index]?.label ?? options[index]);
    if (label && (normalized.includes(label) || (normalized.length >= 2 && label.includes(normalized)))) {
      return index;
    }
  }
  return -1;
}


const STATUS_TECH = {
  idle: 'STANDBY',
  listening: 'LISTENING',
  thinking: 'PROCESSING',
  speaking: 'SPEAKING',
};

function MicIcon() {
  return (
    <svg viewBox="0 0 24 24" width="28" height="28" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <rect x="9" y="3" width="6" height="11" rx="3" />
      <path d="M5 11a7 7 0 0 0 14 0" />
      <line x1="12" y1="18" x2="12" y2="21" />
    </svg>
  );
}

function SoundIcon() {
  return (
    <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5" />
      <path d="M15.5 8.5a5 5 0 0 1 0 7" />
      <path d="M18.5 5.5a9 9 0 0 1 0 13" />
    </svg>
  );
}

function MutedIcon() {
  return (
    <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5" />
      <line x1="22" y1="9" x2="16" y2="15" />
      <line x1="16" y1="9" x2="22" y2="15" />
    </svg>
  );
}

const AGENT_TOKENS_A = ['ANALYZING','▸','CONTEXT','⧉','PROCESSING','⊥','REASONING','▱','SEARCHING','◁','RETRIEVING','▸','COMPUTING','⧉','GENERATING','⊥'];
const AGENT_TOKENS_B = ['OBSERVING','⧉','INTERPRETING','▸','VALIDATING','⊥','OPTIMIZING','◁','CALCULATING','▱','RECONSTRUCTING','⧉','EXECUTING'];
const WAKE_WORDS = [
  'fairy', '绯蕊', '菲蕊', '翡蕊', '斐蕊', '非蕊', '绯睿', '菲睿',
  '绯瑞', '菲瑞', '飞蕊', '飞瑞', '费蕊', '菲雅', '菲亚',
  'ferry', 'fairie', 'fary', 'faery', 'fayrie', 'ferri', 'ferrie',
];
const WAKE_SILENCE_RESET_SEC = 30;

function buildSessionTree(sessions) {
  const list = Array.isArray(sessions) ? sessions : [];
  const byParent = new Map();
  const roots = [];
  for (const session of list) {
    const parent = String(session && session.parent_session || '').trim();
    if (parent) {
      if (!byParent.has(parent)) byParent.set(parent, []);
      byParent.get(parent).push(session);
    } else {
      roots.push(session);
    }
  }
  return { roots, byParent };
}

function AgentTokens({ list, clone }) {
  return (
    <div className={'voice-agent-stream-content' + (clone ? ' clone' : '')}>
      {list.map((t, i) => <span key={(clone ? 'c' : '') + i}>{t}</span>)}
    </div>
  );
}

export default function VoiceDock({
  model,
  models: modelsProp,
  onModelsChanged,
  onModelChange,
  sessionName,
  muted: mutedProp,
  onMutedChange,
  autoFallback: autoFallbackProp,
  onAutoFallbackChange,
  fallbackModel: fallbackModelProp,
  onFallbackModelChange,
  onOpenSettings,
  onTurnComplete,
  onSelectSession,
}) {
  const [transcript, setTranscript] = useState([]);
  const [status, setStatus] = useState('idle');
  const [audioLevel, setAudioLevel] = useState(0);
  const [micLevel, setMicLevel] = useState(0);
  const [interim, setInterim] = useState('');
  const [recording, setRecording] = useState(false);
  const [sttPhase, setSttPhase] = useState('off');
  const [listenPreview, setListenPreview] = useState('');
  const [micBlocked, setMicBlocked] = useState(false);
  const [muted, setMuted] = useState(Boolean(mutedProp));
  const [menuOpen, setMenuOpen] = useState(false);
  const [menuPanel, setMenuPanel] = useState('home');
  const [sessions, setSessions] = useState([]);
  const [sessionsLoading, setSessionsLoading] = useState(false);
  const [historyMessages, setHistoryMessages] = useState([]);
  const [historyLoading, setHistoryLoading] = useState(false);
  const [pendingAskUser, setPendingAskUser] = useState(null);
  const [askIndex, setAskIndex] = useState(0);
  const [askAnswers, setAskAnswers] = useState({});
  const [askFreeText, setAskFreeText] = useState('');
  const [askVoiceEcho, setAskVoiceEcho] = useState('');
  const [toolTrace, setToolTrace] = useState([]);
  const [railOpen, setRailOpen] = useState(false);
  const [draft, setDraft] = useState('');
  const [systemPrompt, setSystemPrompt] = useState('');
  const [promptOpen, setPromptOpen] = useState(false);
  const [agentOnline, setAgentOnline] = useState(null); // null = checking
  const [models, setModels] = useState(() => Array.isArray(modelsProp) ? modelsProp : []);
  const [autoFallback, setAutoFallback] = useState(() => (
    typeof autoFallbackProp === 'boolean' ? autoFallbackProp : readAutoFallback()
  ));
  const [fallbackModel, setFallbackModel] = useState(() => (
    typeof fallbackModelProp === 'string' ? fallbackModelProp : readFallbackModel()
  ));
  const [collapsedVoiceParents, setCollapsedVoiceParents] = useState({});
  const sessionTree = useMemo(() => buildSessionTree(sessions), [sessions]);
  const modelDisplay = (models.find(item => item.id === model) || {}).display || model;
  const [fairySpeaking, setFairySpeaking] = useState(false);
  const [spokenText, setSpokenText] = useState('');
  const [eyeGlitch, setEyeGlitch] = useState(0);
  const [eyeTest, setEyeTest] = useState(null); // null = follow status
  const [glitching, setGlitching] = useState(false);
  useEffect(() => { if (Array.isArray(modelsProp)) setModels(modelsProp); }, [modelsProp]);
  useEffect(() => { setMuted(Boolean(mutedProp)); }, [mutedProp]);
  useEffect(() => { if (typeof autoFallbackProp === 'boolean') setAutoFallback(autoFallbackProp); }, [autoFallbackProp]);
  useEffect(() => { if (typeof fallbackModelProp === 'string') setFallbackModel(fallbackModelProp); }, [fallbackModelProp]);

  // design overlay: clock + current tool + streaming text
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const id = setInterval(() => setNow(new Date()), 1000);
    return () => clearInterval(id);
  }, []);
  // 回复生成进度：thinking 时递增到 ~92%，收到回复后 100% 再归零
  const [progress, setProgress] = useState(0);
  const prevStatusRef = useRef(status);
  useEffect(() => {
    const was = prevStatusRef.current;
    prevStatusRef.current = status;
    if (status === 'thinking') {
      const id = setInterval(() => {
        setProgress(p => (p >= 92 ? 92 : Math.min(92, p + Math.random() * 2.5)));
      }, 180);
      return () => clearInterval(id);
    }
    if (was === 'thinking') {
      setProgress(100);
      const id = setTimeout(() => setProgress(0), 700);
      return () => clearTimeout(id);
    }
    setProgress(0);
  }, [status]);

  const currentTool = toolTrace.length > 0 ? toolTrace[toolTrace.length - 1] : null;

  const thinkLines = useMemo(() => [
    '正在解析绳网返回的搜索结果,准备整合数据源...',
    '匹配到 3 条相关深圳今日天气数据,开始拼装响应...',
    '检测到用户使用简体中文,切换到中文回复模板...',
    '评估是否需要补充分钟级降水概率,触发 web_search 调用...',
    '正在合并历史会话上下文,生成最终答案骨架...',
  ], []);
  const [thinkIdx, setThinkIdx] = useState(0);
  const [thinkText, setThinkText] = useState('');
  useEffect(() => {
    const target = thinkLines[thinkIdx % thinkLines.length];
    let i = 0;
    const tick = setInterval(() => {
      i += 1;
      if (i <= target.length) {
        setThinkText(target.slice(0, i));
      } else {
        clearInterval(tick);
        setTimeout(() => setThinkIdx((n) => n + 1), 1800);
      }
    }, 55);
    return () => clearInterval(tick);
  }, [thinkIdx, thinkLines]);

  const micRef = useRef(null);
  const interimRef = useRef('');
  const audioCtxRef = useRef(null);
  const audioSourceRef = useRef(null);
  const ttsSourcesRef = useRef(new Set());
  const ttsAbortRef = useRef(null);
  const ttsNextTimeRef = useRef(0);
  const sttContextRef = useRef('');
  const turnAbortRef = useRef(null);
  const turnGenRef = useRef(0);
  const submitVoiceCommandRef = useRef(null);
  const historyLoadRef = useRef(0);
  const pendingAskUserRef = useRef(null);
  const askStartedAtRef = useRef(0);
  const askIgnoreUntilRef = useRef(0);
  const askTimerRef = useRef(null);
  const micLevelAtRef = useRef(0);
  const levelTimerRef = useRef(null);
  const scrollRef = useRef(null);
  const promptBodyRef = useRef(null);
  const rootRef = useRef(null);
  const menuStackRef = useRef(null);
  const menuPanelRef = useRef(null);
  const triggerEyeGlitch = useCallback(() => setEyeGlitch(n => n + 1), []);
  // Test cycle: AUTO -> NORMAL -> THINKING -> COMFORTING -> glitch burst -> AUTO.
  const cycleEyeTest = () => {
    if (!eyeTest) setEyeTest('normal');
    else if (eyeTest === 'normal') setEyeTest('thinking');
    else if (eyeTest === 'thinking') setEyeTest('comforting');
    else { triggerEyeGlitch(); setEyeTest(null); }
  };

  const randomFairyGlitch = useCallback(() => {
    const root = rootRef.current;
    if (!root) return;
    const rand = (min, max) => min + Math.random() * (max - min);
    root.style.setProperty('--fairy-g-x', `${rand(-0.9, 0.9).toFixed(2)}px`);
    root.style.setProperty('--fairy-g-skew', `${rand(-0.24, 0.24).toFixed(2)}deg`);
    root.style.setProperty('--fairy-g-bright', rand(1.02, 1.14).toFixed(2));
    root.style.setProperty('--fairy-g-contrast', rand(1.02, 1.16).toFixed(2));
    const noise = document.getElementById('fairy-glitch-noise');
    const displacement = document.getElementById('fairy-glitch-displace');
    if (noise) {
      noise.setAttribute('seed', String(Math.floor(rand(1, 999))));
      noise.setAttribute('baseFrequency', `${rand(.004, .011).toFixed(3)} ${rand(.65, 1).toFixed(2)}`);
    }
    if (displacement) displacement.setAttribute('scale', rand(17, 29).toFixed(1));
  }, []);

  useEffect(() => {
    if (!eyeGlitch) return;
    setGlitching(true);
    const timers = [];
    randomFairyGlitch();
    for (let i = 1; i < 4; i += 1) {
      timers.push(setTimeout(randomFairyGlitch, 30 * i + Math.random() * 12));
    }
    timers.push(setTimeout(() => setGlitching(false), 140 + Math.random() * 42));
    return () => timers.forEach(clearTimeout);
  }, [eyeGlitch, randomFairyGlitch]);

  const loadSessions = () => {
    setSessionsLoading(true);
    fetchSessions().then(list => { setSessions(list || []); setSessionsLoading(false); }).catch(() => setSessionsLoading(false));
  };

  const loadSessionContext = useCallback(async (name) => {
    const token = ++historyLoadRef.current;
    if (!name) {
      setHistoryMessages([]);
      setHistoryLoading(false);
      return;
    }
    setHistoryLoading(true);
    try {
      const payload = await fetchSessionMessages(name);
      if (token !== historyLoadRef.current) return;
      setHistoryMessages(historyMessagesFromPayload(payload));
    } catch {
      if (token === historyLoadRef.current) setHistoryMessages([]);
    } finally {
      if (token === historyLoadRef.current) setHistoryLoading(false);
    }
  }, []);

  const handleSelectSession = (name) => {
    setMenuOpen(false);
    setTranscript([]);
    setHistoryMessages([]);
    pendingAskUserRef.current = null;
    setPendingAskUser(null);
    if (onSelectSession) onSelectSession(name);
  };
  useEffect(() => {
    fetchModels()
      .then(list => {
        const nextModels = list || [];
        setModels(nextModels);
        onModelsChanged?.(nextModels);
        setAgentOnline(true);
        const firstAvailable = nextModels.find(item => item.available !== false);
        if (onModelChange && firstAvailable && !nextModels.some(item => item.id === model && item.available !== false)) {
          onModelChange(firstAvailable.id);
        }
      })
      .catch(() => { setAgentOnline(false); triggerEyeGlitch(); });
    // Sidebar shares the same session store as the main chat page.
    loadSessions();
  }, []);

  useEffect(() => {
    if (!models.length) return;
    const next = pickValidFallbackModel(models, fallbackModel, model);
    if (next !== fallbackModel) {
      setFallbackModel(next);
      writeFallbackModel(next);
      onFallbackModelChange?.(next);
    }
  }, [models, model, fallbackModel]);

  useEffect(() => {
    if (railOpen) loadSessionContext(sessionName);
  }, [railOpen, sessionName, loadSessionContext]);

  // 展开菜单：点空白处 / Esc 收起
  useEffect(() => {
    if (!menuOpen) return;
    const onDown = (e) => {
      const inStack = menuStackRef.current && menuStackRef.current.contains(e.target);
      const inPanel = menuPanelRef.current && menuPanelRef.current.contains(e.target);
      if (!inStack && !inPanel) { setMenuOpen(false); setMenuPanel("home"); }
    };
    const onKey = (e) => { if (e.key === "Escape") { setMenuOpen(false); setMenuPanel("home"); } };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [menuOpen]);

  useEffect(() => {
    if (!promptOpen) return;
    const onWheel = e => {
      const el = promptBodyRef.current;
      if (!el) return;
      e.preventDefault();
      el.scrollTop += e.deltaY;
    };
    window.addEventListener('wheel', onWheel, { passive: false });
    return () => window.removeEventListener('wheel', onWheel);
  }, [promptOpen]);

  useEffect(() => {
    if (scrollRef.current) scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
  }, [transcript, interim]);

  const sttContext = useMemo(() => buildSttContext(transcript), [transcript]);
  useEffect(() => {
    sttContextRef.current = sttContext;
    const ws = micRef.current?.ws;
    if (ws && ws.readyState === 1) {
      ws.send(JSON.stringify({ type: 'context', text: sttContext }));
    }
  }, [sttContext]);

  const speak = useCallback(async (text) => {
    if (!TTS_ENABLED || muted || !text) {
      setSpokenText('');
      return;
    }
    const controller = new AbortController();
    ttsAbortRef.current?.abort();
    ttsAbortRef.current = controller;
    for (const src of ttsSourcesRef.current) {
      try { src.stop(); } catch {}
    }
    ttsSourcesRef.current.clear();
    audioSourceRef.current = null;
    ttsNextTimeRef.current = 0;
    try {
      const ctx = audioCtxRef.current || (audioCtxRef.current = new (window.AudioContext || window.webkitAudioContext)());
      if (ctx.state === 'suspended') ctx.resume();

      const speechSegments = selectDialogueSpeechSegments(text);
      if (!speechSegments.length) {
        setSpokenText('');
        return;
      }
      const queue = speechSegments.flatMap(segment => {
        const split = splitSentences(segment);
        const chunks = buildTTSChunks(split.sentences);
        return chunks.length ? chunks : [String(segment).slice(0, 200)];
      });
      const scheduled = [];

      const scheduleAudio = (audioB64, sampleRate) => {
        const raw = Uint8Array.from(atob(audioB64), ch => ch.charCodeAt(0));
        const f32 = new Float32Array(raw.buffer);
        const half = Math.floor(f32.length / 2);
        if (!half) return;
        const buf = ctx.createBuffer(2, half, sampleRate || 48000);
        for (let ch = 0; ch < 2; ch++) {
          const data = buf.getChannelData(ch);
          for (let i = 0; i < half; i++) data[i] = f32[i * 2 + ch];
        }
        const now = ctx.currentTime;
        const when = ttsNextTimeRef.current > now
          ? ttsNextTimeRef.current
          : now + 0.12;
        ttsNextTimeRef.current = when + buf.duration;
        const src = ctx.createBufferSource();
        src.buffer = buf;
        src.connect(ctx.destination);
        audioSourceRef.current = src;
        ttsSourcesRef.current.add(src);
        const ended = new Promise(resolve => {
          src.onended = () => {
            ttsSourcesRef.current.delete(src);
            if (audioSourceRef.current === src) audioSourceRef.current = null;
            resolve();
          };
        });
        scheduled.push(ended);
        src.start(when);
      };

      for (const chunk of queue) {
        setSpokenText(clampDialogueText(chunk));
        setStatus('speaking');
        const r = await fetch('/voice/api/tts/stream', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json; charset=utf-8' },
          body: JSON.stringify({ text: chunk.slice(0, 200) }),
          signal: controller.signal,
        });
        if (!r.ok || !r.body) continue;
        const reader = r.body.getReader();
        const decoder = new TextDecoder();
        let buffer = '';
        while (true) {
          const { done, value } = await reader.read();
          if (done) break;
          buffer += decoder.decode(value, { stream: true });
          let idx;
          while ((idx = buffer.indexOf('\n\n')) >= 0) {
            const eventText = buffer.slice(0, idx);
            buffer = buffer.slice(idx + 2);
            for (const line of eventText.split('\n')) {
              if (!line.startsWith('data: ')) continue;
              try {
                const evt = JSON.parse(line.slice(6));
                if (evt.type === 'audio') {
                  scheduleAudio(evt.audio_b64, evt.sample_rate || 48000);
                }
              } catch {}
            }
          }
        }
      }
      if (scheduled.length) await Promise.all(scheduled);
      setAudioLevel(0);
      setSpokenText('');
    } catch (e) {
      if (e?.name !== 'AbortError') setStatus('idle');
    } finally {
      if (ttsAbortRef.current === controller) ttsAbortRef.current = null;
    }
  }, [muted]);

  const appendUserTranscript = useCallback((text) => {
    setTranscript(t => {
      const last = t[t.length - 1];
      if (last && last.role === 'user' && last.text === text) return t;
      return [...t, { role: 'user', text }];
    });
  }, []);

  const runTurn = useCallback(async (userText) => {
    if (turnAbortRef.current) turnAbortRef.current.abort();
    const myGen = ++turnGenRef.current;
    const controller = new AbortController();
    turnAbortRef.current = controller;
    appendUserTranscript(userText);
    setStatus('thinking');
    setFairySpeaking(false);
    setToolTrace([]);
    let gotStreamed = false;
    const applyEvent = (ev) => {
      if (ev.type === 'tool') {
        if (ev.isError) triggerEyeGlitch();
        setToolTrace(t => [...t, ev]);
      } else if (ev.type === 'user') {
        appendUserTranscript(ev.text);
      } else if (ev.type === 'injected_user') {
        appendUserTranscript(ev.content || ev.text || '');
      } else if (ev.type === 'context') {
        setTranscript(t => [...t, { role: 'context', text: ev.label }]);
      } else if (ev.type === 'message') {
        const text = readableText(ev.text || '');
        if (!text) return '';
        gotStreamed = true;
        setFairySpeaking(true);
        setTranscript(t => [...t, { role: 'fairy', text }]);
      } else if (ev.type === 'error') {
        gotStreamed = true;
        const rawError = String(ev.error || '未知错误').replace(/\s+/g, ' ').trim();
        const shortError = rawError.length > 400 ? rawError.slice(0, 400) + '…' : rawError;
        setTranscript(t => [...t, { role: 'fairy', text: '模型调用失败：' + shortError }]);
      } else if (ev.type === 'waiting_user_input') {
        const ask = {
          askType: ev.ask_type || '',
          questions: normalizeAskQuestions(ev),
          session: ev.session || sessionName,
        };
        askStartedAtRef.current = performance.now();
        askIgnoreUntilRef.current = performance.now() + 2500;
        if (askTimerRef.current) {
          clearTimeout(askTimerRef.current);
          askTimerRef.current = null;
        }
        pendingAskUserRef.current = ask;
        setPendingAskUser(ask);
        setAskIndex(0);
        setAskAnswers({});
        setAskFreeText('');
        setAskVoiceEcho('');
        interimRef.current = '';
        setInterim('');
        if (micRef.current) {
          micRef.current.finalText = '';
          micRef.current.queue = [];
        }
      } else if (ev.type === 'done' && ev.messages) {
        const norm = normalizeServiceMessages(ev.messages);
        const last = [...norm].reverse().find(m => m.role === 'assistant' && m.content && m.content.trim());
        if (last) return readableText(last.content);
      }
      return '';
    };
    try {
      const res = await sendChat(userText, model, sessionName, controller.signal, [], 'voice');
      if (myGen !== turnGenRef.current) return;
      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      let buffer = '';
      let finalText = '';
      let done = false;
      while (!done) {
        const { done: rd, value } = await reader.read();
        done = rd;
        if (value) {
          buffer += decoder.decode(value, { stream: !done });
          const lines = buffer.split('\n');
          buffer = lines.pop() || '';
          for (const line of lines) {
            const t = line.trim();
            if (!t.startsWith('data: ')) continue;
            const raw = t.slice(6);
            if (raw === '[DONE]') continue;
            try {
              const ev = JSON.parse(raw);
              const maybeFinal = applyEvent(ev);
              if (maybeFinal) finalText = maybeFinal;
            } catch {}
          }
        }
        if (myGen !== turnGenRef.current) return;
      }
      const dataLine = buffer.trim();
      if (dataLine.startsWith('data: ')) {
        const raw = dataLine.slice(6);
        if (raw !== '[DONE]') {
          try {
            const ev = JSON.parse(raw);
            const maybeFinal = applyEvent(ev);
            if (maybeFinal) finalText = maybeFinal;
          } catch {}
        }
      }
      if (myGen !== turnGenRef.current) return;
      if (!finalText) finalText = '抱歉，主人，我这边没有拿到可用回复。';
      if (!gotStreamed) setTranscript(t => [...t, { role: 'fairy', text: finalText }]);
      if (onTurnComplete) onTurnComplete();
      await speak(finalText);
      if (myGen !== turnGenRef.current) return;
      setFairySpeaking(false);
      setStatus(micRef.current ? 'listening' : 'idle');
    } catch (e) {
      if (e?.name === 'AbortError' || myGen !== turnGenRef.current) return;
      triggerEyeGlitch();
      setFairySpeaking(false);
      setTranscript(t => [...t, { role: 'fairy', text: '网络或服务出错了：' + e }]);
      setStatus(micRef.current ? 'listening' : 'idle');
    } finally {
      if (myGen === turnGenRef.current) turnAbortRef.current = null;
    }
  }, [model, sessionName, onTurnComplete, speak, triggerEyeGlitch, appendUserTranscript]);

  const stopSpeaking = useCallback(() => {
    ttsAbortRef.current?.abort();
    ttsAbortRef.current = null;
    for (const src of ttsSourcesRef.current) {
      try { src.stop(); } catch {}
    }
    ttsSourcesRef.current.clear();
    audioSourceRef.current = null;
    ttsNextTimeRef.current = 0;
    if (levelTimerRef.current) {
      clearInterval(levelTimerRef.current);
      levelTimerRef.current = null;
    }
    setAudioLevel(0);
    setSpokenText('');
    setFairySpeaking(false);
  }, []);

  const currentAskQuestion = pendingAskUser?.questions?.[askIndex] || null;
  const askQuestions = pendingAskUser?.questions || [];
  const askIsLast = askIndex >= askQuestions.length - 1;
  const askSelected = currentAskQuestion ? (askAnswers[currentAskQuestion.id] || []) : [];
  const askOptionList = (currentAskQuestion?.options || []).slice(0, 3);
  const hasExplicitRecommendation = askOptionList.some(option => option?.recommended === true);

  const resetAskState = () => {
    pendingAskUserRef.current = null;
    askStartedAtRef.current = 0;
    askIgnoreUntilRef.current = 0;
    if (askTimerRef.current) {
      clearTimeout(askTimerRef.current);
      askTimerRef.current = null;
    }
    setPendingAskUser(null);
    setAskIndex(0);
    setAskAnswers({});
    setAskFreeText('');
    setAskVoiceEcho('');
  };

  const sendAskReply = (message) => {
    if (!pendingAskUserRef.current) return;
    resetAskState();
    runTurn(message);
  };

  const finishAsk = (answers = askAnswers) => {
    const questions = pendingAskUser?.questions || [];
    const lines = questions.map((question, index) => {
      const qid = question.id || `q${index + 1}`;
      const selected = answers[qid] || [];
      const free = answers[`${qid}_free_text`] || '';
      let line = `Q${index + 1}: ${question.question || question.title || ''}`;
      if (selected.length) line += ` → 选择: ${selected.join(', ')}`;
      if (free) line += ` | 补充: ${free}`;
      return line;
    });
    sendAskReply(`[用户已回答了问题]\n${lines.join('\n')}`);
  };

  const confirmAsk = () => {
    sendAskReply(`[用户确认了] ${pendingAskUser?.askType || ''}`);
  };

  const skipAsk = () => {
    sendAskReply('[用户跳过了提问]');
  };

  const selectAskOption = (option) => {
    if (!currentAskQuestion) return null;
    const label = option?.label ?? option;
    const isMulti = currentAskQuestion.multi_select === true;
    const previous = askAnswers[currentAskQuestion.id] || [];
    const selected = isMulti
      ? (previous.includes(label) ? previous.filter(item => item !== label) : [...previous, label])
      : [label];
    const nextAnswers = { ...askAnswers, [currentAskQuestion.id]: selected };
    if (!isMulti && option?.description) {
      nextAnswers[`${currentAskQuestion.id}_desc`] = option.description;
    }
    setAskAnswers(nextAnswers);
    setAskVoiceEcho(String(label));
    return { label, answers: nextAnswers };
  };

  const proceedAsk = () => {
    if (!currentAskQuestion) {
      confirmAsk();
      return;
    }
    const nextAnswers = { ...askAnswers };
    if (askFreeText.trim()) {
      nextAnswers[`${currentAskQuestion.id}_free_text`] = askFreeText.trim();
    }
    if (askIsLast) {
      finishAsk(nextAnswers);
      return;
    }
    setAskAnswers(nextAnswers);
    setAskIndex(index => index + 1);
    setAskFreeText('');
    setAskVoiceEcho('');
  };

  const advanceAskWithAnswers = (answers) => {
    if (askIsLast) {
      finishAsk(answers);
      return;
    }
    setAskAnswers(answers);
    setAskIndex(index => index + 1);
    setAskFreeText('');
    setAskVoiceEcho('');
  };

  const handleAskVoiceAnswer = (text) => {
    const ask = pendingAskUserRef.current;
    if (!ask) return false;
    // Ignore speech that was already queued before the question appeared.
    if (performance.now() < askIgnoreUntilRef.current) return true;
    const message = String(text || '').trim();
    if (!message) return true;
    setAskVoiceEcho(message);

    const questions = ask.questions || [];
    if (!questions.length) {
      if (/跳过|取消|不要|不执行|否|不是/.test(message)) skipAsk();
      else if (/确认|继续|可以|好的|确定|执行|是/.test(message)) confirmAsk();
      return true;
    }

    if (/跳过全部|跳过|取消|不选了/.test(message)) {
      skipAsk();
      return true;
    }
    if (/完成|确认|下一步|继续|确定|可以|好的/.test(message)) {
      proceedAsk();
      return true;
    }

    const question = questions[Math.min(askIndex, questions.length - 1)];
    const options = Array.isArray(question?.options) ? question.options : [];
    const optionIndex = findVoiceOptionIndex(message, options);
    if (optionIndex >= 0) {
      const picked = selectAskOption(options[optionIndex]);
      if (picked && question.multi_select !== true) {
        advanceAskWithAnswers(picked.answers);
      }
      return true;
    }

    if (question?.allow_free_text !== false) {
      const freeText = message.replace(/^(补充|其他|自定义)[:：,，\s]*/i, '').trim();
      if (freeText && /^(补充|其他|自定义)/i.test(message)) {
        setAskFreeText(freeText);
        advanceAskWithAnswers(
          { ...askAnswers, [`${question.id}_free_text`]: freeText },
        );
      }
    }
    return true;
  };

  useEffect(() => {
    if (!pendingAskUser) return undefined;
    const timer = setTimeout(() => {
      if (!pendingAskUserRef.current) return;
      const questions = pendingAskUser.questions || [];
      if (!questions.length) {
        confirmAsk();
        return;
      }
      const question = questions[askIndex];
      const options = Array.isArray(question?.options) ? question.options.slice(0, 3) : [];
      if (!options.length) return;
      const selected = options.find(option => option?.recommended === true) || options[0];
      const picked = selectAskOption(selected);
      if (picked) advanceAskWithAnswers(picked.answers);
    }, 30_000);
    return () => clearTimeout(timer);
  }, [pendingAskUser, askIndex]);

  const submitVoiceCommand = useCallback(async (text) => {
    const message = String(text || '').trim();
    if (!message) return;
    if (pendingAskUserRef.current && handleAskVoiceAnswer(message)) return;

    // A running agent has the live-inject channel: queue the new utterance
    // into the current turn instead of killing it and losing its progress.
    if (status === 'thinking') {
      appendUserTranscript(message);
      try {
        const response = await fetch('/api/chat/inject', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json; charset=utf-8' },
          body: JSON.stringify({ session: sessionName, text: message }),
        });
        const data = await response.json().catch(() => ({}));
        if (!response.ok || !data.ok) {
          throw new Error(data.error || `inject failed (HTTP ${response.status})`);
        }
        return;
      } catch (error) {
        console.warn('voice inject failed, falling back to a fresh turn', error);
        if (turnAbortRef.current) turnAbortRef.current.abort();
        turnGenRef.current += 1;
        try {
          await fetch('/api/chat/cancel', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json; charset=utf-8' },
            body: JSON.stringify({ session: sessionName }),
          });
        } catch {}
      }
    }

    if (status === 'speaking') {
      turnGenRef.current += 1;
      if (turnAbortRef.current) {
        turnAbortRef.current.abort();
        turnAbortRef.current = null;
      }
      stopSpeaking();
    }
    runTurn(message);
  }, [status, sessionName, runTurn, appendUserTranscript, stopSpeaking]);

  useEffect(() => {
    submitVoiceCommandRef.current = submitVoiceCommand;
  }, [submitVoiceCommand]);

  const releaseMicCapture = (m) => {
    if (!m) return;
    try { if (m.processor) m.processor.disconnect(); } catch {}
    try { if (m.source) m.source.disconnect(); } catch {}
    try { if (m.stream) m.stream.getTracks().forEach(t => t.stop()); } catch {}
    try { if (m.ctx && m.ctx.state !== 'closed') m.ctx.close(); } catch {}
    setMicLevel(0);
    setSttPhase('off');
    setListenPreview('');
  };

  const stopMic = () => {
    const m = micRef.current;
    if (!m || m.stopping) return;
    m.stopping = true;
    setRecording(false);
    releaseMicCapture(m);
    setStatus('thinking');

    const finish = () => {
      if (m.finished) return;
      m.finished = true;
      clearTimeout(m.finalTimer);
      try { if (m.ws) m.ws.close(); } catch {}
      micRef.current = null;
      const t = (m.finalText || interimRef.current || '').trim();
      interimRef.current = '';
      setInterim('');
      if (t) runTurn(t);
      else setStatus('idle');
    };
    m.finish = finish;

    if (m.ws && m.ws.readyState === 1) {
      if (m.ready) m.ws.send(JSON.stringify({ type: 'end' }));
      else m.pendingEnd = true;
      // Final transcription normally arrives in well under a second. Keep a
      // hard fallback so a dead service can never trap the microphone UI.
      m.finalTimer = setTimeout(finish, 10000);
    } else {
      finish();
    }
  };

  const startMic = () => {
    if (micRef.current || status === 'thinking') return;
    if (!navigator.mediaDevices || !window.AudioContext) return;
    setMicBlocked(false);
    const wsProtocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const ws = new WebSocket(`${wsProtocol}//${location.host}/voice-ws`);
    const ctx = new (window.AudioContext || window.webkitAudioContext)();
    if (ctx.state === 'suspended') ctx.resume().catch(() => {});
    let stream = null, source = null, processor = null;
    setSttPhase('loading');
    setListenPreview('');
    ws.onmessage = e => {
      try {
        const d = JSON.parse(e.data);
        if (d && d.type === 'error') {
          setTranscript(t => [...t, { role: 'fairy', text: `语音识别失败：${d.error || '未知错误'}` }]);
          if (micRef.current && micRef.current.stopping) micRef.current.finish?.();
          return;
        }
        if (d && d.type === 'wake_state' && d.state === 'waiting' && micRef.current) {
          const m = micRef.current;
          m.ready = true;
          m.awake = false;
          for (const chunk of m.queue) {
            if (ws.readyState === 1) ws.send(chunk);
          }
          m.queue = [];
          if (m.pendingEnd && ws.readyState === 1) ws.send(JSON.stringify({ type: 'end' }));
          setSttPhase('waiting');
          setListenPreview('');
          return;
        }
        if (d && d.type === 'wake_state' && d.state === 'conversation' && micRef.current) {
          const m = micRef.current;
          m.ready = true;
          m.awake = true;
          setSttPhase('awake');
          setListenPreview('');
          return;
        }
        if (d && d.type === 'wake' && micRef.current) {
          micRef.current.awake = true;
          setSttPhase('awake');
          setListenPreview('');
          setStatus('listening');
          return;
        }
        if (d && d.type === 'reset' && micRef.current) {
          micRef.current.awake = false;
          interimRef.current = '';
          setInterim('');
          setSttPhase('waiting');
          setListenPreview('');
          return;
        }
        if (d && d.type === 'listen_partial') {
          const heard = String(d.text || '');
          setListenPreview(heard);
          interimRef.current = heard;
          setInterim(heard);
          setSttPhase(current => (current === 'awake' ? current : 'hearing'));
          return;
        }
        if (d && d.text) {
          if (d.type === 'partial') {
            interimRef.current = d.text;
            setInterim(d.text);
            setSttPhase('awake');
            setListenPreview('');
            return;
          }
          if (d.type === 'final' && micRef.current) {
            const m = micRef.current;
            if (m.wakeMode && !m.stopping) {
              const text = String(d.text || '').trim();
              m.awake = false;
              m.finalText = '';
              interimRef.current = '';
              setInterim('');
              setSttPhase('waiting');
              setListenPreview('');
              if (text) submitVoiceCommandRef.current?.(text);
              else setStatus('listening');
              return;
            }
            m.finalText = d.text;
            if (m.stopping) m.finish?.();
            return;
          }
          interimRef.current = d.text;
          setInterim(d.text);
        }
      } catch {}
    };
    micRef.current = {
      ws,
      ctx,
      finalText: '',
      queue: [],
      ready: false,
      wakeMode: true,
      awake: false,
      pendingEnd: false,
    };
    ws.onopen = () => {
      ws.send(JSON.stringify({
        type: 'config',
        mode: 'wake',
        wakeWords: WAKE_WORDS,
        silenceResetSec: WAKE_SILENCE_RESET_SEC,
        context: sttContextRef.current,
      }));
    };
    navigator.mediaDevices.getUserMedia({
      audio: {
        echoCancellation: true,
        noiseSuppression: true,
        autoGainControl: true,
      },
    })
      .then(ms => {
        stream = ms;
        source = ctx.createMediaStreamSource(ms);
        processor = ctx.createScriptProcessor(4096, 1, 1);
        const gain = ctx.createGain();
        gain.gain.value = 0;
        processor.onaudioprocess = ev => {
          const input = ev.inputBuffer.getChannelData(0);
          const now = performance.now();
          if (now - micLevelAtRef.current > 80) {
            let sum = 0;
            for (let i = 0; i < input.length; i += 1) sum += input[i] * input[i];
            const rms = Math.sqrt(sum / input.length);
            setMicLevel(Math.min(1, rms * 6));
            micLevelAtRef.current = now;
          }
          if (micRef.current && ws.readyState === 1) {
            const out = new Float32Array(Math.ceil(input.length / 3));
            for (let i = 0, j = 0; i < input.length; i += 3) out[j++] = input[i];
            if (micRef.current.ready) {
              ws.send(out.buffer);
            } else {
              micRef.current.queue.push(out.buffer);
              // A slow model load must not grow an unbounded in-memory queue.
              if (micRef.current.queue.length > 240) micRef.current.queue.shift();
            }
          }
        };
        source.connect(processor);
        processor.connect(gain);
        gain.connect(ctx.destination);
        micRef.current.stream = ms;
        micRef.current.source = source;
        micRef.current.processor = processor;
      })
      .catch(error => {
        releaseMicCapture(micRef.current);
        try { ws.close(); } catch {}
        micRef.current = null;
        setRecording(false);
        setStatus('idle');
        setSttPhase('off');
        setListenPreview('');
        setMicLevel(0);
        setMicBlocked(true);
        setInterim('');
        setTranscript(t => [...t, { role: 'fairy', text: `麦克风不可用：${error?.name || error || 'unknown error'}` }]);
      });
    setRecording(true);
    setStatus('listening');
    setInterim('');
  };

  const toggleMic = useCallback(() => {
    if (status === 'thinking') return;
    if (recording || micRef.current) stopMic();
    else startMic();
  }, [status, recording]);

  useEffect(() => {
    const timer = setTimeout(() => {
      if (!micRef.current && !recording) startMic();
    }, 500);
    return () => clearTimeout(timer);
  }, []);

  const sendDraft = () => {
    const msg = draft.trim();
    if (!msg) return;
    setDraft('');
    submitVoiceCommand(msg);
  };

  const openMenu = () => {
    const next = !menuOpen;
    setMenuOpen(next);
    if (next) setMenuPanel('home');
  };

  const lastDialogue = useMemo(() => {
    if (interim.trim()) {
      return { role: 'user', speaker: '用户', text: interim.trim() };
    }
    return [...transcript]
      .reverse()
      .find(m => (m.role === 'user' || m.role === 'fairy') && m.text && m.text.trim()) || null;
  }, [transcript, interim]);
  const dialogueDisplay = (status === 'speaking' || fairySpeaking)
    ? {
        role: 'fairy',
        speaker: 'FAIRY',
        text: spokenText || '…',
      }
    : pendingAskUser
      ? {
          role: 'fairy',
          speaker: 'FAIRY',
          text: currentAskQuestion?.question || currentAskQuestion?.title || '是否继续执行？',
        }
      : lastDialogue;
  const sttHudStatus = micBlocked
    ? 'MIC BLOCKED'
    : !recording
      ? 'MIC OFF'
    : sttPhase === 'loading'
      ? 'STT LOADING'
      : sttPhase === 'awake'
        ? 'LISTENING'
        : listenPreview
          ? `HEARD: ${listenPreview}`
          : 'WAITING';
  const hudStatusText = status === 'thinking'
    ? 'PROCESSING'
    : status === 'speaking'
      ? 'SPEAKING'
      : sttHudStatus;

  return (
    <div ref={rootRef} className={`voice-dock voice-${status}${fairySpeaking ? ' eye-speaking-live' : ''}${eyeTest ? ' eye-test-' + eyeTest : ''}${glitching ? ' eye-glitching' : ''}`}>
      <svg className="fairy-glitch-defs" aria-hidden="true" focusable="false">
        <defs>
          <filter id="fairy-glitch-interference" x="-30%" y="0%" width="160%" height="100%" colorInterpolationFilters="sRGB">
            <feTurbulence id="fairy-glitch-noise" type="fractalNoise" baseFrequency=".012 .72" numOctaves="1" seed="1" result="noise" />
            <feColorMatrix in="noise" type="matrix" values="1 0 0 0 0 0 1 0 0 0 0 0 0 0 .5 0 0 0 1 0" result="horizontal-noise" />
            <feDisplacementMap id="fairy-glitch-displace" in="SourceGraphic" in2="horizontal-noise" scale="5" xChannelSelector="R" yChannelSelector="B" />
          </filter>
        </defs>
      </svg>

      {/* ambient layers */}
      <div className="voice-dock-bg" aria-hidden="true" />
      <div className="voice-dock-texture" aria-hidden="true" />

      {/* 旧的时间浮层已移除：日期/时钟改由 voice-agent-backdrop 提供 */}


      {/* ==== Agent 界面层（替换原 Web 1920–2.png）==== */}
      <div className="voice-agent-backdrop" aria-hidden="true">
        <div className="voice-agent-core-glow" />
        <div className="voice-agent-core" />
        <div className="voice-agent-datetime">
          <span className="y">{now.getFullYear()}</span>
          <span className="d">{String(now.getMonth() + 1).padStart(2, '0')}·{String(now.getDate()).padStart(2, '0')}</span>
        </div>
        <div className="voice-agent-clock">{now.toLocaleTimeString('zh-CN', { hour12: false })}</div>

        <div className="voice-agent-stream-area">
          <div className="voice-agent-stream-line">
            <AgentTokens list={AGENT_TOKENS_A} />
            <AgentTokens list={AGENT_TOKENS_A} clone />
          </div>
          <div className="voice-agent-stream-line second">
            <AgentTokens list={AGENT_TOKENS_B} />
            <AgentTokens list={AGENT_TOKENS_B} clone />
          </div>
        </div>

        <div className="voice-agent-progress-area">
          <div className="voice-agent-progress-label">GENERATING</div>
          <div className="voice-agent-progress">
            <div className="voice-agent-progress-value" style={{ width: progress + '%' }} />
          </div>
        </div>
      </div>
      <div className="voice-dock-grid" aria-hidden="true" />
      <div className="voice-dock-icon voice-dock-icon-01" aria-hidden="true" />
      <div className="voice-dock-strip voice-dock-strip-top" aria-hidden="true" />
      <div className="voice-dock-strip voice-dock-strip-bottom" aria-hidden="true" />
      <div className="voice-dock-film voice-dock-film-top" aria-hidden="true" />
      <div className="voice-dock-film voice-dock-film-bottom" aria-hidden="true" />
      <div className="voice-dock-vignette" aria-hidden="true" />

      {/* top HUD bar */}
      <header className="voice-hudbar">
        <div className="voice-hudbar-bg" aria-hidden="true" />
        <div className="voice-hudbar-brand">
          <span className="voice-brand">FAIRY</span>
          <span className="voice-brand-sub">Voice Link // Agent System</span>
        </div>
        <div className="voice-hudbar-status">
          <span className="voice-tech">Sys.Status</span>
          <span
            className="voice-tech-value voice-tech-status"
            title={listenPreview ? `HEARD: ${listenPreview}` : hudStatusText}
          >
            <i className={`voice-hud-stt-dot stt-${sttPhase}${recording ? ' live' : ''}`} />
            <span className="voice-tech-status-text">{hudStatusText}</span>
            <span className="voice-hud-stt-meter" aria-hidden="true">
              <i style={{ width: `${Math.max(4, Math.round(micLevel * 100))}%` }} />
            </span>
          </span>
        </div>
        <div className="voice-hudbar-meta">
          <span className="voice-tech">Model</span>
          <span className="voice-tech-value">{modelDisplay}</span>
          <span className="voice-tech-sep" />
          <span className="voice-tech">Agent</span>
          <span className={`voice-tech-value agent-link ${agentOnline === null ? '' : agentOnline ? 'on' : 'off'}`}>
            {agentOnline === null ? 'LINK…' : agentOnline ? 'ONLINE' : 'OFFLINE'}
          </span>
        </div>
        {micBlocked ? (
          <button
            type="button"
            className={`voice-mic voice-mic-top${recording ? ' recording' : ''}`}
            onClick={toggleMic}
            disabled={status === 'thinking'}
            aria-label="允许使用麦克风"
            title="浏览器尚未授权麦克风，点击授权"
          >
            <MicIcon />
          </button>
        ) : null}
        <button type="button" className={`voice-mute-btn${muted ? ' muted' : ''}`} onClick={() => { const next = !muted; setMuted(next); onMutedChange?.(next); }} title="语音播报开关" aria-label="语音播报开关">
          {muted ? <MutedIcon /> : <SoundIcon />}
        </button>
        <button type="button" className={`voice-eye-test${eyeTest ? ' active' : ''}`} onClick={cycleEyeTest} title="眼睛状态测试：AUTO/NORMAL/THINKING/COMFORTING/GLITCH" aria-label="眼睛状态测试">
          {eyeTest ? eyeTest.toUpperCase() : 'AUTO'}
        </button>
        <div className={"voice-menu-stack" + (menuOpen ? " open" : "")} ref={menuStackRef}>
          <button
            type="button"
            className="voice-menu-pill voice-menu-pill--menu"
            onClick={openMenu}
            aria-expanded={menuOpen}
            aria-label="Fairy 功能菜单"
          >
            <span className="voice-menu-icon voice-menu-icon--green" aria-hidden="true">
              <svg viewBox="0 0 24 24" width="15" height="15">
                <rect x="3" y="5" width="18" height="3.2" rx="1.6" fill="#111" />
                <rect x="3" y="10.4" width="18" height="3.2" rx="1.6" fill="#111" />
                <rect x="3" y="15.8" width="18" height="3.2" rx="1.6" fill="#111" />
              </svg>
            </span>
            <span className="voice-menu-label">菜单</span>
          </button>

          <button
            type="button"
            className="voice-menu-pill voice-menu-pill--session"
            tabIndex={menuOpen ? 0 : -1}
            onClick={() => { setMenuOpen(false); setMenuPanel("home"); setRailOpen(true); if (sessions.length === 0) loadSessions(); }}
          >
            <span className="voice-menu-icon voice-menu-icon--orange" aria-hidden="true">
              <svg viewBox="0 0 24 24" width="15" height="15">
                <path d="M4 5.5h16A1.5 1.5 0 0 1 21.5 7v8a1.5 1.5 0 0 1-1.5 1.5h-8l-4.5 3.2V16.5H4A1.5 1.5 0 0 1 2.5 15V7A1.5 1.5 0 0 1 4 5.5Z" fill="#111" />
              </svg>
            </span>
            <span className="voice-menu-label">会话</span>
          </button>

          <button
            type="button"
            className="voice-menu-pill voice-menu-pill--config"
            tabIndex={menuOpen ? 0 : -1}
            onClick={() => { if (onOpenSettings) { setMenuOpen(false); setMenuPanel("home"); onOpenSettings(); } else setMenuPanel("config"); }}
          >
            <span className="voice-menu-icon voice-menu-icon--blue" aria-hidden="true">
              <svg viewBox="0 0 24 24" width="15" height="15">
                <rect x="3" y="6" width="18" height="2.6" rx="1.3" fill="#111" />
                <circle cx="9" cy="7.3" r="3" fill="#111" />
                <rect x="3" y="15.4" width="18" height="2.6" rx="1.3" fill="#111" />
                <circle cx="15" cy="16.7" r="3" fill="#111" />
              </svg>
            </span>
            <span className="voice-menu-label">设置</span>
          </button>
        </div>
      </header>

      {/* menu panel (Config) */}
      {menuOpen && menuPanel === "config" ? (
        <div className="voice-dock-menu" ref={menuPanelRef}>
          <button type="button" className="voice-dock-menu-back" onClick={() => setMenuPanel("home")}>← Back</button>
          <div className="voice-dock-menu-title">Config</div>
          <div className="voice-dock-menu-config">
            <div className="voice-dock-config-row">
              <span className="voice-tech">Model</span>
              <select
                className="voice-config-model-select"
                value={model}
                onChange={(event) => onModelChange && onModelChange(event.target.value)}
                aria-label="选择模型"
              >
                {models.length > 0 ? models.map((item) => (
                  <option key={item.id} value={item.id} disabled={item.available === false}>
                    {item.display || item.id}{item.available === false ? '（未配置 Key）' : ''}
                  </option>
                )) : (
                  <option value={model}>{model}</option>
                )}
              </select>
            </div>
            <div className="voice-dock-config-row">
              <span className="voice-tech">Fallback</span>
              <select
                className="voice-config-model-select"
                value={fallbackModel}
                onChange={(event) => {
                  const next = event.target.value;
                  setFallbackModel(next);
                  writeFallbackModel(next);
                  onFallbackModelChange?.(next);
                }}
                disabled={!autoFallback}
                aria-label="选择限流备用模型"
              >
                {models.length > 0 ? models.map((item) => (
                  <option
                    key={item.id}
                    value={item.id}
                    disabled={item.id === model || item.available === false}
                  >
                    {item.display || item.id}{item.id === model ? '（当前）' : item.available === false ? '（未配置 Key）' : ''}
                  </option>
                )) : (
                  <option value={fallbackModel}>{fallbackModel || '未选择'}</option>
                )}
              </select>
            </div>
            <div className="voice-dock-config-row">
              <span className="voice-tech">Rate Limit</span>
              <button
                type="button"
                className={"voice-config-toggle" + (autoFallback ? "" : " off")}
                onClick={() => {
                  setAutoFallback(value => {
                    const next = !value;
                    writeAutoFallback(next);
                    onAutoFallbackChange?.(next);
                    return next;
                  });
                }}
              >
                {autoFallback ? "AUTO" : "OFF"}
              </button>
            </div>
            <div className="voice-dock-config-row"><span className="voice-tech">Session</span><span className="voice-tech-value">{sessionName}</span></div>
            <div className="voice-dock-config-row"><span className="voice-tech">TTS Output</span>
              <button type="button" className={"voice-config-toggle" + (muted ? " off" : "")} onClick={() => { const next = !muted; setMuted(next); onMutedChange?.(next); }}>{muted ? "OFF" : "ON"}</button>
            </div>
            <div className="voice-dock-config-row"><span className="voice-tech">Agent Link</span>
              <span className={"voice-tech-value agent-link " + (agentOnline === null ? "" : agentOnline ? "on" : "off")}>
                {agentOnline === null ? "LINK…" : agentOnline ? "ONLINE" : "OFFLINE"}
              </span>
            </div>
            <div className="voice-dock-config-row voice-dock-config-prompt">
              <span className="voice-tech">System Prompt</span>
              <button type="button" className="voice-dock-prompt-open" onClick={() => setPromptOpen(true)}>
                {systemPrompt ? "查看" : "暂无"}
              </button>
            </div>
          </div>
        </div>
      ) : null}

      {/* stage: rolltext + avatar + waveform */}
      <div className="voice-stage">
        <div className="voice-dock-rolltext" aria-hidden="true"><span>THE THIRD GENERATION SEQUENTIAL INTEGRATED UNIVERSAL ARTIFICIAL INTELLIGENCE · FAIRY</span></div>

        <div className="fairy-avatar" aria-label="Fairy 动态形象">
          <span className="fairy-layer fairy-wave" />
          <span className="fairy-layer fairy-bg" />
          <span className="fairy-layer fairy-wheel" />
          <span className="fairy-layer fairy-shine" />
          <span className="fairy-layer fairy-ring-lb" />
          <span className="fairy-layer fairy-ring-b" />
          <span className="fairy-layer fairy-pupil" />
          <span className="fairy-layer fairy-dot" />
          <svg className="fairy-lid" viewBox="0 0 100 100" aria-hidden="true">
            <g className="fairy-lid-thinking">
              <path className="fairy-lid-upper" d="M-20,-20 H120 V38 Q50,58 -20,38 Z" />
            </g>
            <g className="fairy-lid-comforting">
              <path className="fairy-lid-upper" d="M-20,-20 H120 V38 Q50,18 -20,38 Z" />
            </g>
          </svg>
        </div>

      </div>

      {/* session history sidebar (Menu > Session opens this) */}
      <div
        className={`voice-rail-backdrop${railOpen ? ' open' : ''}`}
        onClick={() => setRailOpen(false)}
        aria-hidden="true"
      />
      <aside className={`voice-rail${railOpen ? ' open' : ''}`} data-open={railOpen ? 'true' : 'false'}>
        <div className="voice-rail-head">
          <div className="voice-rail-brand">
            <span className="voice-rail-logo" aria-hidden="true" />
            <span className="voice-rail-title">KNOCK KNOCK</span>
            <span className="voice-rail-subtitle">CONVERSATION LOG</span>
          </div>
          <button type="button" className="voice-rail-toggle" onClick={() => setRailOpen(v => !v)} aria-label={railOpen ? '收起会话栏' : '展开会话栏'} title={railOpen ? '收起' : '展开'}>
            ×
          </button>
        </div>
        <div className="voice-rail-actions">
          <button type="button" className="voice-rail-btn voice-rail-btn--primary" onClick={() => { if (onSelectSession) onSelectSession(''); loadSessions(); }}>＋ 新会话</button>
          <button type="button" className="voice-rail-btn" onClick={loadSessions}>⟳ 刷新</button>
        </div>
        <div className="voice-rail-content">
          <div className="voice-rail-list">
            {sessionsLoading ? <div className="voice-rail-empty">加载中...</div> : null}
            {!sessionsLoading && sessions.length === 0 ? <div className="voice-rail-empty">暂无历史会话</div> : null}
            {sessionTree.roots.map(parent => {
              const branches = sessionTree.byParent.get(parent.name) || [];
              const hasBranches = branches.length > 0;
              const collapsed = !!collapsedVoiceParents[parent.name];
              const label = parent.daily_date || sessionDateLabel(parent);
              const preview = cleanSessionPreview(parent.preview);
              return (
                <div className="voice-rail-group" key={parent.name}>
                  <button
                    type="button"
                    className={`voice-rail-item${parent.name === sessionName ? ' active' : ''}`}
                    onClick={() => handleSelectSession(parent.name)}
                    title={preview || parent.name}
                  >
                    <span className="voice-rail-avatar">{label.slice(5).replace('-', '.') || 'F'}</span>
                    <span className="voice-rail-item-copy">
                      <span className="voice-rail-item-name">{label || parent.name}</span>
                      {preview ? <span className="voice-rail-item-preview">{preview}</span> : null}
                    </span>
                    {hasBranches ? (
                      <span
                        className={'voice-rail-branch-toggle' + (collapsed ? ' collapsed' : '')}
                        onClick={event => {
                          event.stopPropagation();
                          setCollapsedVoiceParents(prev => ({ ...prev, [parent.name]: !collapsed }));
                        }}
                        role="button"
                        tabIndex={0}
                        title={collapsed ? '展开分支会话' : '收起分支会话'}
                      >{collapsed ? '▸' : '▾'}</span>
                    ) : <span className="voice-rail-more">···</span>}
                  </button>
                  {hasBranches && !collapsed ? (
                    <div className="voice-rail-branches">
                      {branches.map(branch => {
                        const branchLabel = branch.domain || branch.name;
                        const branchPreview = cleanSessionPreview(branch.preview);
                        return (
                          <button
                            key={branch.name}
                            type="button"
                            className={`voice-rail-item voice-rail-branch${branch.name === sessionName ? ' active' : ''}`}
                            onClick={() => handleSelectSession(branch.name)}
                            title={branchPreview || branch.name}
                          >
                            <span className="voice-rail-avatar">B</span>
                            <span className="voice-rail-item-copy">
                              <span className="voice-rail-item-name">{branchLabel}</span>
                              {branchPreview ? <span className="voice-rail-item-preview">{branchPreview}</span> : null}
                            </span>
                            <span className="voice-rail-more">···</span>
                          </button>
                        );
                      })}
                    </div>
                  ) : null}
                </div>
              );
            })}
          </div>
          <section className="voice-context">
            <header className="voice-context-head">
              <span className="voice-context-title">{sessionName || '未选择会话'}</span>
              <span className="voice-context-meta">{historyMessages.length} MSGS</span>
            </header>
            <div className="voice-context-messages">
              {historyLoading ? <div className="voice-rail-empty">读取上下文...</div> : null}
              {!historyLoading && historyMessages.length === 0 ? (
                <div className="voice-rail-empty">暂无对话上下文</div>
              ) : null}
              {!historyLoading && historyMessages.map((message, index) => (
                <div key={`${message.role}-${index}`} className={`voice-context-row ${message.role}`}>
                  {message.role === 'fairy' ? <span className="voice-context-avatar">F</span> : null}
                  <div className="voice-context-bubble">{message.text}</div>
                  {message.role === 'user' ? <span className="voice-context-avatar user">U</span> : null}
                </div>
              ))}
            </div>
          </section>
        </div>
      </aside>
      {!railOpen ? (
        <button type="button" className="voice-rail-collapsed-tab" onClick={() => setRailOpen(true)} aria-label="展开会话栏" title="会话历史">❯</button>
      ) : null}
      <div className="voice-dialog-stack">
      {pendingAskUser ? (
        <section className="voice-ask-panel" role="dialog" aria-live="polite" aria-label="Fairy 提问">
          <div className="voice-ask-options">
            {askQuestions.length
              ? askOptionList.map((option, optionIndex) => {
                  const rawLabel = option?.label ?? option;
                  const label = String(rawLabel || '').replace(/\s+/g, ' ').slice(0, 20);
                  const selected = askSelected.includes(label);
                  const recommended = option?.recommended === true
                    || (!hasExplicitRecommendation && optionIndex === 0);
                  return (
                    <div className="voice-ask-option-row" key={`${label}-${optionIndex}`}>
                      <span className="voice-ask-num">{optionIndex + 1}</span>
                      <button
                        type="button"
                        className={`voice-ask-option${recommended ? ' recommended' : ''}${selected ? ' selected' : ''}`}
                        onClick={() => {
                          const picked = selectAskOption(option);
                          if (!picked) return;
                          advanceAskWithAnswers(picked.answers);
                        }}
                        title={String(rawLabel || '')}
                      >
                        <span className="voice-ask-bullet" aria-hidden="true" />
                        <span className="voice-ask-option-copy">
                          <b>{label}</b>
                        </span>
                      </button>
                    </div>
                  );
                })
              : (
                <>
                  <div className="voice-ask-option-row">
                    <span className="voice-ask-num">1</span>
                    <button type="button" className="voice-ask-option recommended" onClick={confirmAsk}>
                      <span className="voice-ask-bullet" aria-hidden="true" />
                      <span className="voice-ask-option-copy"><b>确认</b></span>
                    </button>
                  </div>
                  <div className="voice-ask-option-row">
                    <span className="voice-ask-num">2</span>
                    <button type="button" className="voice-ask-option" onClick={skipAsk}>
                      <span className="voice-ask-bullet" aria-hidden="true" />
                      <span className="voice-ask-option-copy"><b>跳过</b></span>
                    </button>
                  </div>
                </>
              )}
          </div>
        </section>
      ) : null}
      {/* transcript dialog */}
      <div className="voice-dock-dialog" ref={scrollRef}>
        <div className={`voice-dialog-label${dialogueDisplay?.role === 'user' ? ' is-user' : ''}`}>
          {dialogueDisplay?.role === 'user' ? '用户' : 'FAIRY'}
        </div>
        {dialogueDisplay ? (
          <div className={`voice-line voice-line-${dialogueDisplay.role} voice-line-current`}>
            <span className="voice-line-text">{clampDialogueText(dialogueDisplay.text)}</span>
          </div>
        ) : (
          <div className="voice-empty">等待唤醒词...</div>
        )}
        <div className="voice-next">»</div>
      </div>
      </div>
      {/* TEMP: transparent test input, kept outside the dialog so it cannot affect its layout */}
      <div className="voice-dialog-input">
        <input
          value={draft}
          onChange={e => setDraft(e.target.value)}
          onKeyDown={e => { if (e.key === 'Enter') { e.preventDefault(); sendDraft(); } }}
          placeholder="临时文本测试..."
        />
        <button type="button" onClick={sendDraft} disabled={!draft.trim()} aria-label="发送">↓</button>
      </div>

      {/* system prompt modal */}
      {promptOpen ? (
        <div className="voice-dock-modal" onClick={() => setPromptOpen(false)}>
          <div className="voice-dock-modal-box" onClick={e => e.stopPropagation()}>
            <div className="voice-dock-modal-header">
              <span>System Prompt</span>
              <button type="button" onClick={() => setPromptOpen(false)} aria-label="关闭">×</button>
            </div>
            <div className="voice-dock-modal-body" ref={promptBodyRef}>
              {systemPrompt || '暂无'}
            </div>
          </div>
        </div>
      ) : null}

      {/* footer */}
      <footer className="voice-footer">
        <span className="voice-footer-item">Session // <b>{sessionName}</b></span>
        <span className="voice-footer-item voice-footer-right">Fairy · 3rd Gen Sequential AI · Ready</span>
      </footer>
    </div>
  );
}
