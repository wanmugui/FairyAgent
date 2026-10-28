import { useEffect, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import {
  BarChart3,
  Bot,
  Brain,
  Building2,
  CalendarClock,
  Check,
  ChevronDown,
  ChevronRight,
  Eye,
  EyeOff,
  Cpu,
  Loader2,
  MessageCircle,
  MessageSquare,
  Palette,
  Puzzle,
  Plus,
  RefreshCcw,
  Settings2,
  ShieldCheck,
  Sparkles,
  Trash2,
  UserRound,
  Volume2,
  VolumeX,
  Wand2,
  Wrench,
  X,
} from 'lucide-react';
import AccountSection from './AccountSection';
import WechatSection from './WechatSection';
import QqSection from './QqSection';
import ScheduledTasksSection from './ScheduledTasksSection';
import MemoryCalendarSection from './MemoryCalendarSection';
import SystemSection from './SystemSection';
import UsageSection from './UsageSection';
import ProviderSection from './ProviderSection';
import {
  writeStoredModel,
} from '../utils/modelPreference';
import {
  fetchModels,
  fetchSettings,
  addModel,
  removeModel,
  testModel,
  listProviderModels,
  defaultModelPresets,
  updateToolSetting,
  updateSkillSetting,
  addSkill,
  deleteSkill,
} from '../api/chat';
import { UNCATEGORIZED, buildSkillTree } from '../skillTree';
import { THEMES } from '../theme';

const PROVIDER_PRESETS = defaultModelPresets();

function makeUserId(name) {
  const base = String(name || '')
    .toLowerCase()
    .replace(/[^a-z0-9_-]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 40);
  return ('user_' + (base || 'model')).slice(0, 48);
}

function makeBlankDraft(presetId) {
  const preset = PROVIDER_PRESETS.find((p) => p.id === presetId) || PROVIDER_PRESETS[0];
  return {
    presetId: preset.id,
    display: preset.suggestDisplay || '',
    id: '',
    baseUrl: preset.baseUrl || '',
    apiKey: '',
    model: preset.modelPlaceholder || '',
    temperature: '0.4',
    maxTokens: '16384',
    showKey: false,
  };
}

function Toggle({ checked, onChange, label, hint }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      onClick={() => onChange(!checked)}
      className={`fairy-switch ${checked ? 'is-on' : ''}`}
    >
      <span className="fairy-switch__track">
        <span className="fairy-switch__thumb" />
      </span>
      <span className="fairy-switch__copy">
        <span className="fairy-switch__label">{label}</span>
        {hint ? <span className="fairy-switch__hint">{hint}</span> : null}
      </span>
    </button>
  );
}

function Section({ icon: Icon, title, summary, children, defaultOpen = true, panel = '' }) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <section className={`fairy-section ${open ? 'is-open' : ''}`} data-settings-panel={panel}>
      <header className="fairy-section__head" onClick={() => setOpen((v) => !v)}>
        <span className="fairy-section__icon">
          <Icon size={14} />
        </span>
        <span className="fairy-section__title">{title}</span>
        {summary ? <span className="fairy-section__summary">{summary}</span> : null}
        <span className="fairy-section__chevron">
          {open ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
        </span>
      </header>
      {open ? <div className="fairy-section__body">{children}</div> : null}
    </section>
  );
}

function Row({ label, hint, control, badge }) {
  return (
    <div className="fairy-row">
      <div className="fairy-row__label">
        <span className="fairy-row__title">{label}</span>
        {hint ? <span className="fairy-row__hint">{hint}</span> : null}
        {badge}
      </div>
      <div className="fairy-row__control">{control}</div>
    </div>
  );
}

function SkillStar({ core, onToggle }) {
  return (
    <button
      type="button"
      className={`fairy-skill-star ${core ? 'is-core' : ''}`}
      title={core ? '核心技能（置顶）' : '设为核心技能'}
      onClick={(event) => {
        event.stopPropagation();
        onToggle();
      }}
    >
      {core ? '★' : '☆'}
    </button>
  );
}

function SkillRow({ skill, dragging, onDragStart, onPatch, onDelete }) {
  const [confirming, setConfirming] = useState(false);
  return (
    <div
      className={`fairy-skill-card ${dragging ? 'is-dragging' : ''}`}
      draggable
      onDragStart={(event) => {
        event.dataTransfer.setData('text/plain', skill.name);
        event.dataTransfer.effectAllowed = 'move';
        onDragStart(skill.name);
      }}
      onDragEnd={() => onDragStart(null)}
    >
      <div className="fairy-skill-card__head">
        <SkillStar core={!!skill.core} onToggle={() => onPatch(skill.name, { core: !skill.core })} />
        <span className="fairy-skill-card__name" title={skill.name}>{skill.name}</span>
        <Toggle
          checked={skill.enabled}
          onChange={(value) => onPatch(skill.name, { enabled: value })}
          label=""
        />
        <button
          type="button"
          className={`fairy-skill-card__delete ${confirming ? 'is-confirming' : ''}`}
          title={confirming ? '再点一次确认删除' : '删除这个 Skill'}
          aria-label={`删除 ${skill.name}`}
          onClick={(event) => {
            // The card is draggable, so a plain click would start a drag.
            // Without this the delete button is unusable.
            event.stopPropagation();
            event.preventDefault();
            if (!onDelete) return;
            if (!confirming) {
              setConfirming(true);
              // Revert to idle if the second click never comes, otherwise the
              // button stays armed forever and the next click deletes by surprise.
              window.setTimeout(() => setConfirming(false), 4000);
              return;
            }
            setConfirming(false);
            onDelete(skill.name);
          }}
        >
          <Trash2 size={13} />
        </button>
      </div>
      <div className="fairy-skill-card__hint">{skill.description || skill.location || 'Skill'}</div>
      {skill.registered ? <span className="fairy-badge">已注册</span> : null}
      {confirming ? (
        <div className="fairy-skill-card__confirm">连目录一起删除，无法撤销。再点一次确认。</div>
      ) : null}
    </div>
  );
}

function SkillColumn({ title, count, isCore, droppable, onDropSkill, empty, children }) {
  const [over, setOver] = useState(false);
  return (
    <div
      className={`fairy-skill-col ${isCore ? 'is-core' : ''} ${over ? 'is-drop-target' : ''}`}
      onDragOver={droppable ? (event) => { event.preventDefault(); event.dataTransfer.dropEffect = 'move'; setOver(true); } : undefined}
      onDragLeave={droppable ? () => setOver(false) : undefined}
      onDrop={droppable ? (event) => {
        event.preventDefault();
        setOver(false);
        const name = event.dataTransfer.getData('text/plain');
        if (name) onDropSkill(name);
      } : undefined}
    >
      <div className="fairy-skill-col__head">
        <span className="fairy-skill-col__title">{title}</span>
        <span className="fairy-skill-col__count">{count}</span>
      </div>
      <div className="fairy-skill-col__body">
        {count ? children : <div className="fairy-skill-col__empty">{empty}</div>}
      </div>
    </div>
  );
}

function SkillTree({ skills, onPatch, onDelete }) {
  const tree = buildSkillTree(skills);
  const [dragging, setDragging] = useState(null);
  const [newCatOver, setNewCatOver] = useState(false);
  // 横向拖拽浏览：分类列往往超出面板宽度，底部那条滚动条又细又远，
  // 主人想直接捏着空白处往右拖来看后面的分类。
  // 用 pointer 事件而不是 HTML5 drag —— 卡片本身是可拖拽的，
  // 两者会打架；所以只在「按下的目标不是可拖拽元素」时才启动平移。
  const boardRef = useRef(null);
  const panState = useRef(null);
  const [panning, setPanning] = useState(false);

  const onBoardPointerDown = (event) => {
    // 只处理主键；碰到卡片（draggable）交给卡片自己的拖拽逻辑
    if (event.button !== 0) return;
    if (event.target.closest('[draggable="true"]')) return;
    const board = boardRef.current;
    if (!board) return;
    // 没有可滚动距离就不必启动
    if (board.scrollWidth <= board.clientWidth + 1) return;
    panState.current = { startX: event.clientX, startScroll: board.scrollLeft, moved: false };
  };

  const onBoardPointerMove = (event) => {
    const state = panState.current;
    const board = boardRef.current;
    if (!state || !board) return;
    const delta = event.clientX - state.startX;
    if (!state.moved && Math.abs(delta) < 4) return;   // 抖动阈值，避免误判成点击
    state.moved = true;
    // 方向：鼠标往右拖 → 露出右边还没看到的分类 → scrollLeft 变大。
    // （原来写成减号，delta 为正时算出负值，被浏览器钳到 0，拖了个寂寞。）
    board.scrollLeft = state.startScroll + delta;
    if (!panning) setPanning(true);
  };

  const endPan = (event) => {
    const state = panState.current;
    panState.current = null;
    if (panning) setPanning(false);
    // 点击（没有实际拖动）不该吞掉事件
    if (state && !state.moved) return;
    if (event && event.cancelable) event.preventDefault();
  };

  // 纵向滚轮在横向溢出的容器里也应该横着走
  const onBoardWheel = (event) => {
    const board = boardRef.current;
    if (!board) return;
    const canScroll = board.scrollWidth > board.clientWidth + 1;
    if (!canScroll) return;
    const deltaX = Math.abs(event.deltaX) > Math.abs(event.deltaY) ? event.deltaX : 0;
    if (!deltaX) return;
    // 纵向滚轮想横着走时，按住 shift 或者直接用 deltaY 都滚横向
    board.scrollLeft += deltaX;
    event.preventDefault();
  };
  const renderCard = (skill) => (
    <SkillRow
      key={skill.name}
      skill={skill}
      dragging={dragging === skill.name}
      onDragStart={setDragging}
      onPatch={onPatch}
      onDelete={onDelete}
    />
  );
  const dropInto = (name, category) => onPatch(name, { category });
  return (
    <div
      className={'fairy-skill-board' + (panning ? ' is-panning' : '')}
      ref={boardRef}
      onPointerDown={onBoardPointerDown}
      onPointerMove={onBoardPointerMove}
      onPointerUp={endPan}
      onPointerCancel={endPan}
      onWheel={onBoardWheel}
    >
      <SkillColumn
        title="核心技能"
        count={tree.core.length}
        isCore
        droppable
        empty="把卡片拖到这里设为核心（会置顶）"
        onDropSkill={(name) => onPatch(name, { core: true })}
      >
        {tree.core.map(renderCard)}
      </SkillColumn>
      {tree.groups.map((group) => (
        <SkillColumn
          key={group.category}
          title={group.category}
          count={group.skills.length}
          droppable
          empty="把卡片拖到这里"
          onDropSkill={(name) => dropInto(name, group.category === UNCATEGORIZED ? '' : group.category)}
        >
          {group.skills.map(renderCard)}
        </SkillColumn>
      ))}
      <div
        className={`fairy-skill-col fairy-skill-col--new ${newCatOver ? 'is-drop-target' : ''}`}
        onDragOver={(event) => { event.preventDefault(); event.dataTransfer.dropEffect = 'move'; setNewCatOver(true); }}
        onDragLeave={() => setNewCatOver(false)}
        onDrop={(event) => {
          event.preventDefault();
          setNewCatOver(false);
          const name = event.dataTransfer.getData('text/plain');
          const category = window.prompt('新分类名称');
          if (name && category && category.trim()) dropInto(name, category.trim());
        }}
      >
        <div className="fairy-skill-col__head">
          <span className="fairy-skill-col__title">＋ 新建分类</span>
        </div>
        <div className="fairy-skill-col__empty">拖一张卡片到这里，输入分类名</div>
      </div>
    </div>
  );
}

function Field({ label, hint, error, children }) {
  return (
    <label className="fairy-field">
      <span className="fairy-field__label">{label}</span>
      {children}
      {hint && !error ? <span className="fairy-field__hint">{hint}</span> : null}
      {error ? <span className="fairy-field__error">{error}</span> : null}
    </label>
  );
}

function PrimaryButton({ children, onClick, loading, disabled, type = 'button', icon: Icon }) {
  return (
    <button
      type={type}
      className="fairy-btn fairy-btn--primary"
      onClick={onClick}
      disabled={disabled || loading}
    >
      {loading ? <Loader2 size={14} className="fairy-btn__spin" /> : Icon ? <Icon size={14} /> : null}
      <span>{children}</span>
    </button>
  );
}

function GhostButton({ children, onClick, disabled, icon: Icon, danger }) {
  return (
    <button
      type="button"
      className={`fairy-btn fairy-btn--ghost ${danger ? 'is-danger' : ''}`}
      onClick={onClick}
      disabled={disabled}
    >
      {Icon ? <Icon size={14} /> : null}
      <span>{children}</span>
    </button>
  );
}

export default function SettingsMenu({
  open,
  onClose,
  models,
  selectedModel,
  onSelectModel,
  theme,
  onThemeChange,
  voiceAutoRead,
  onVoiceAutoReadChange,
  autoFallback,
  onAutoFallbackChange,
  fallbackModel,
  onFallbackModelChange,
  onModelsChanged,
}) {
  const [modelsState, setModelsState] = useState(models || []);
  const [tab, setTab] = useState('general');
  // 「主机」页只对属主可见。这个组件自身拿不到登录身份（App.jsx 没有往下传），
  // 所以自己问一次 /auth/me。不要写死 true，也不必为它在 App.jsx 里开一条 prop 链。
  // 后端 /api/system/* 还有 auth.store.isOwner 兜底，这里只负责体验。
  const [isOwner, setIsOwner] = useState(false);

  useEffect(() => {
    let cancelled = false;
    fetch('/auth/me', { cache: 'no-store' })
      .then((r) => (r.ok ? r.json() : null))
      .then((me) => {
        if (!cancelled) setIsOwner(Boolean(me && me.owner));
      })
      .catch(() => {
        if (!cancelled) setIsOwner(false);
      });
    return () => {
      cancelled = true;
    };
  }, []);
  const [accountSummary, setAccountSummary] = useState('未启用');
  const [wechatSummary, setWechatSummary] = useState('未接入');
  const [qqSummary, setQqSummary] = useState('未绑定');
  const [scheduledSummary, setScheduledSummary] = useState('未设置');
  const [memorySummary, setMemorySummary] = useState('加载中');
  const [toolsState, setToolsState] = useState([]);
  // 带参数的工具（tool_score_cleanup / context_reasoning）可展开编辑。
  // expandedTool 记录当前展开的工具 id，paramDraft 存正在编辑的值，
  // paramState 存保存中的状态与错误。草稿独立于 toolsState，
  // 否则每敲一个字符就触发一次保存。
  const [expandedTool, setExpandedTool] = useState('');
  const [paramDraft, setParamDraft] = useState({});
  const [paramState, setParamState] = useState({ status: 'idle', message: '' });
  const [skillsState, setSkillsState] = useState([]);
  const [skillDraft, setSkillDraft] = useState({ name: '', location: '', description: '' });
  const [skillSubmit, setSkillSubmit] = useState({ status: 'idle', message: '' });
  const [draft, setDraft] = useState(() => makeBlankDraft('deepseek'));
  const [testState, setTestState] = useState({ status: 'idle', message: '' });
  const [submitState, setSubmitState] = useState({ status: 'idle', message: '' });
  const [removingId, setRemovingId] = useState('');
  const [remoteModels, setRemoteModels] = useState([]);
  const [discovering, setDiscovering] = useState(false);
  // 本地 / 自建模型：给 Ollama、vLLM 这类没有「拉取模型列表」能力的
  // 服务留一个手工入口。供应商体系管的是能列模型的云服务。
  const [localDraft, setLocalDraft] = useState({
    id: '',
    display: '',
    base_url: '',
    api_key: '',
    model: '',
    format: 'chat_completions',
    max_tokens: 16384,
    temperature: 0.4,
  });
  const [localState, setLocalState] = useState({ status: 'idle', message: '' });
  const panelRef = useRef(null);

  // 拉取远端模型列表（包含 user_*）
  const refreshModels = async () => {
    try {
      const list = await fetchModels();
      setModelsState(Array.isArray(list) ? list : []);
      onModelsChanged?.(Array.isArray(list) ? list : []);
    } catch (err) {
      setTestState({ status: 'error', message: '拉取模型失败：' + (err.message || err) });
    }
  };

  const refreshSettings = async () => {
    try {
      const result = await fetchSettings();
      setToolsState(Array.isArray(result?.tools) ? result.tools : []);
      setSkillsState(Array.isArray(result?.skills) ? result.skills : []);
    } catch (err) {
      setTestState({ status: 'error', message: '读取工具/技能设置失败：' + (err.message || err) });
    }
  };

  useEffect(() => {
    if (open) {
      refreshModels();
      refreshSettings();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  useEffect(() => {
    if (Array.isArray(models) && models.length) setModelsState(models);
  }, [models]);

  useEffect(() => {
    if (!open) return;
    const handler = (e) => {
      if (e.key === 'Escape') onClose?.();
    };
    window.addEventListener('keydown', handler);
    return () => window.removeEventListener('keydown', handler);
  }, [open, onClose]);

  const availableModels = useMemo(() => modelsState.filter((m) => m.available !== false), [modelsState]);
  const userModels = useMemo(() => modelsState.filter((m) => m.user), [modelsState]);

  // Hooks 必须无条件调用；early return 必须放在所有 hooks 之后。
  if (!open) return null;

  const onChangePreset = (presetId) => {
    const preset = PROVIDER_PRESETS.find((p) => p.id === presetId) || PROVIDER_PRESETS[0];
    setDraft((d) => ({
      ...d,
      presetId: preset.id,
      baseUrl: preset.baseUrl || '',
      model: preset.modelPlaceholder || '',
      display: preset.suggestDisplay || '',
      id: '',
    }));
    setRemoteModels([]);
  };

  const handleDiscover = async () => {
    if (!draft.apiKey || !draft.baseUrl) {
      setTestState({ status: 'error', message: '请先填写 API Key 和 Base URL' });
      return;
    }
    setDiscovering(true);
    setTestState({ status: 'loading', message: '正在获取模型列表…' });
    try {
      const result = await listProviderModels({
        base_url: draft.baseUrl,
        api_key: draft.apiKey,
      });
      if (!result.ok) {
        setTestState({ status: 'error', message: result.error || '获取模型列表失败' });
        return;
      }
      const list = Array.isArray(result.models) ? result.models : [];
      const currentModel = String(draft.model || '').trim();
      const preset = PROVIDER_PRESETS.find((item) => item.id === draft.presetId);
      const currentIsPlaceholder = !!currentModel && !!preset && currentModel === preset.modelPlaceholder;
      setRemoteModels(list);
      setDraft((d) => ({
        ...d,
        model: (!currentModel || currentIsPlaceholder) ? (list[0] || currentModel) : currentModel,
      }));
      setTestState({
        status: list.length ? 'ok' : 'error',
        message: list.length ? `已获取 ${list.length} 个模型` : '接口可用，但没有返回模型列表，请手动填写模型名称',
      });
    } catch (err) {
      setTestState({ status: 'error', message: '获取模型列表失败：' + (err.message || err) });
    } finally {
      setDiscovering(false);
    }
  };

  const handleTest = async () => {
    if (!draft.apiKey || !draft.baseUrl || !draft.model) {
      setTestState({ status: 'error', message: '请先填写 API Key、Base URL 和模型名' });
      return;
    }
    setTestState({ status: 'loading', message: '正在测试连接…' });
    try {
      const result = await testModel({
        base_url: draft.baseUrl,
        api_key: draft.apiKey,
        model: draft.model,
      });
      if (result.ok) {
        setTestState({ status: 'ok', message: result.message || '连接成功' });
      } else {
        setTestState({ status: 'error', message: result.error || '连接失败' });
      }
    } catch (err) {
      setTestState({ status: 'error', message: '请求失败：' + (err.message || err) });
    }
  };

  const handleAdd = async () => {
    const id = (draft.id || draft.display || draft.model || '').trim();
    if (!id) {
      setSubmitState({ status: 'error', message: '请填写模型 ID 或显示名称' });
      return;
    }
    if (!draft.apiKey || !draft.baseUrl || !draft.model) {
      setSubmitState({ status: 'error', message: 'API Key / Base URL / 模型名 必填' });
      return;
    }
    const finalId = makeUserId(id);
    setSubmitState({ status: 'loading', message: '正在保存…' });
    try {
      const result = await addModel({
        id: finalId,
        display: (draft.display || id).slice(0, 60),
        provider: draft.presetId || 'custom',
        base_url: draft.baseUrl,
        api_key: draft.apiKey,
        model: draft.model,
        temperature: Number(draft.temperature) || 0.4,
        max_tokens: Number(draft.maxTokens) || 16384,
      });
      if (result.ok) {
        setSubmitState({ status: 'ok', message: '已添加：' + (result.id || finalId) });
        setDraft(makeBlankDraft(draft.presetId));
        await refreshModels();
      } else {
        setSubmitState({ status: 'error', message: result.error || '保存失败' });
      }
    } catch (err) {
      setSubmitState({ status: 'error', message: '保存失败：' + (err.message || err) });
    }
  };

  const handleRemove = async (id) => {
    if (!id) return;
    setRemovingId(id);
    try {
      const result = await removeModel(id);
      if (result.ok) {
        // 若删的是当前选中，则让上层重选
        if (id === selectedModel && onSelectModel) {
          const remaining = modelsState.filter((m) => m.id !== id && m.available !== false);
          if (remaining.length) onSelectModel(remaining[0].id);
        }
        await refreshModels();
      } else {
        setSubmitState({ status: 'error', message: result.error || '删除失败' });
      }
    } catch (err) {
      setSubmitState({ status: 'error', message: '删除失败：' + (err.message || err) });
    } finally {
      setRemovingId('');
    }
  };

  // 保存一个本地 / 自建模型。
  const handleAddLocalModel = async () => {
    const d = localDraft;
    if (!d.id.trim() || !d.base_url.trim() || !d.api_key.trim() || !d.model.trim()) {
      setLocalState({ status: 'error', message: '模型 id、请求地址、API Key、模型名都要填' });
      return;
    }
    setLocalState({ status: 'busy', message: '保存中…' });
    try {
      const result = await addModel({
        id: d.id.trim(),
        display: d.display.trim() || d.id.trim(),
        base_url: d.base_url.trim(),
        api_key: d.api_key.trim(),
        model: d.model.trim(),
        format: d.format,
        max_tokens: Number(d.max_tokens) || 16384,
        temperature: Number(d.temperature),
      });
      if (result.ok) {
        setLocalState({ status: 'ok', message: `已添加 ${d.id.trim()}` });
        setLocalDraft({
          id: '', display: '', base_url: '', api_key: '', model: '',
          format: 'chat_completions', max_tokens: 16384, temperature: 0.4,
        });
        await refreshModels();
      } else {
        setLocalState({ status: 'error', message: result.error || '保存失败' });
      }
    } catch (err) {
      setLocalState({ status: 'error', message: '保存失败：' + (err.message || err) });
    }
  };

  const handleSelectModel = (id) => {
    writeStoredModel(id);
    onSelectModel?.(id);
  };

  const handleToolToggle = async (id, enabled) => {
    setToolsState(list => list.map(tool => tool.id === id ? { ...tool, enabled } : tool));
    const result = await updateToolSetting(id, { enabled });
    if (!result.ok) {
      setTestState({ status: 'error', message: result.error || `更新工具 ${id} 失败` });
      await refreshSettings();
    }
  };

  // 展开一个带参数的工具时，把当前值灌进草稿。收起时清掉草稿，
  // 避免下次展开看到的是上一次编辑到一半的值。
  const toggleToolParams = (tool) => {
    const next = expandedTool === tool.id ? '' : tool.id;
    setExpandedTool(next);
    setParamState({ status: 'idle', message: '' });
    if (!next) {
      setParamDraft({});
      return;
    }
    const seed = {};
    for (const p of tool.params || []) seed[p.key] = String(p.value);
    setParamDraft(seed);
  };

  const handleToolParamsSave = async (tool) => {
    // 只提交真正出现在 params 里的键，避免把别的工具的字段也写过去。
    const params = {};
    for (const p of tool.params || []) {
      const raw = paramDraft[p.key];
      if (raw == null || String(raw).trim() === '') continue;
      params[p.key] = Number(raw);
    }
    setParamState({ status: 'saving', message: '' });
    const result = await updateToolSetting(tool.id, { params });
    if (!result.ok) {
      setParamState({ status: 'error', message: result.error || `保存参数失败` });
      return;
    }
    setParamState({ status: 'ok', message: '已保存（重启 agent 后生效）' });
    await refreshSettings();
  };

  const handleSkillPatch = async (name, patch) => {
    setSkillsState(list => list.map(skill => skill.name === name ? { ...skill, ...patch } : skill));
    const result = await updateSkillSetting(name, patch);
    if (!result.ok) {
      setSkillSubmit({ status: 'error', message: result.error || `更新技能 ${name} 失败` });
      await refreshSettings();
    }
  };

  const handleAddSkill = async () => {
    if (!skillDraft.name || !skillDraft.location) {
      setSkillSubmit({ status: 'error', message: 'Skill 名称和路径必填' });
      return;
    }
    setSkillSubmit({ status: 'loading', message: '正在接入 Skill…' });
    const result = await addSkill(skillDraft);
    if (!result.ok) {
      setSkillSubmit({ status: 'error', message: result.error || '接入 Skill 失败' });
      return;
    }
    setSkillDraft({ name: '', location: '', description: '' });
    setSkillSubmit({ status: 'ok', message: 'Skill 已接入' });
    await refreshSettings();
  };

  const handleDeleteSkill = async (name) => {
    setSkillSubmit({ status: 'loading', message: `正在删除 ${name}…` });
    const result = await deleteSkill(name);
    if (!result.ok) {
      setSkillSubmit({ status: 'error', message: result.error || `删除 ${name} 失败` });
      await refreshSettings();
      return;
    }
    setSkillSubmit({ status: 'ok', message: `已删除 ${name}` });
    await refreshSettings();
  };

  // 「主机」只对属主出现：非属主连入口都看不到，避免点进去才发现是空的。
  // 后端 /api/system/* 还有一层 isOwner 兜底，前端这层只负责体验。
  const navItems = [
    { id: 'general', label: '常规', icon: Settings2 },
    { id: 'models', label: '模型', icon: Bot },
    { id: 'tools', label: '工具', icon: Wrench },
    { id: 'skills', label: '技能', icon: Puzzle },
    ...(isOwner
      ? [{ id: 'system', label: '主机', icon: Cpu }, { id: 'usage', label: '用量', icon: BarChart3 }]
      : []),
    { id: 'scheduled', label: '定时', icon: CalendarClock },
    { id: 'memory', label: '记忆', icon: Brain },
    { id: 'account', label: '账号', icon: UserRound },
    { id: 'wechat', label: '微信', icon: MessageCircle },
    { id: 'qq', label: 'QQ', icon: MessageSquare },
  ];

  const activeTheme = THEMES[theme] ? theme : 'base';
  return createPortal(
    <div className="fairy-settings__scrim" onMouseDown={(e) => { if (e.target === e.currentTarget) onClose?.(); }}>
      <div className={`fairy-settings fairy-settings--${activeTheme}`} ref={panelRef} role="dialog" aria-label="设置">
        <header className="fairy-settings__head">
          <div className="fairy-settings__title">
            <Settings2 size={16} />
            <span>设置</span>
          </div>
          <button type="button" className="fairy-settings__close" onClick={onClose} aria-label="关闭">
            <X size={16} />
          </button>
        </header>

        <div className="fairy-settings__body">
          <nav className="fairy-settings__nav" aria-label="设置分类">
            {navItems.map(item => {
              const Icon = item.icon;
              return (
                <button
                  key={item.id}
                  type="button"
                  className={`fairy-settings__nav-item${tab === item.id ? ' is-active' : ''}`}
                  onClick={() => setTab(item.id)}
                >
                  <Icon size={15} />
                  <span>{item.label}</span>
                </button>
              );
            })}
          </nav>
          <div className="fairy-settings__content" data-active={tab}>
          {/* 通用：默认模型选择 */}
          <Section icon={Bot} title="模型选择" panel="general" summary={selectedModel ? `当前：${selectedModel}` : '未选择'}>
            <Row
              label="默认模型"
              hint="用于新会话的首选模型"
              control={
                <select
                  className="fairy-select"
                  value={selectedModel || ''}
                  onChange={(e) => handleSelectModel(e.target.value)}
                >
                  <option value="" disabled>
                    选择一个模型…
                  </option>
                  {availableModels.map((m) => (
                    <option key={m.id} value={m.id}>
                      {m.display || m.id}
                    </option>
                  ))}
                </select>
              }
            />
            <Row
              label="自动回退"
              hint="首选模型失败时，自动切换到回退模型"
              control={
                <Toggle
                  checked={autoFallback}
                  onChange={onAutoFallbackChange}
                  label={autoFallback ? '已开启' : '已关闭'}
                />
              }
            />
            <Row
              label="回退模型"
              hint={autoFallback ? '当首选不可用时使用' : '需先开启自动回退'}
              control={
                <select
                  className="fairy-select"
                  value={fallbackModel || ''}
                  onChange={(e) => onFallbackModelChange?.(e.target.value)}
                  disabled={!autoFallback}
                >
                  <option value="">不指定</option>
                  {availableModels
                    .filter((m) => m.id !== selectedModel)
                    .map((m) => (
                      <option key={m.id} value={m.id}>
                        {m.display || m.id}
                      </option>
                    ))}
                </select>
              }
            />
          </Section>

          {/* 外观 */}
          <Section icon={Palette} title="外观" panel="general" summary={THEMES[theme]?.label || theme || '基础'}>
            <Row
              label="界面皮肤"
              hint="保存到 config/config.json 的 settings.theme"
              control={
                <select
                  className="fairy-select"
                  value={theme || 'base'}
                  onChange={(e) => onThemeChange?.(e.target.value)}
                >
                  {Object.entries(THEMES).map(([key, value]) => (
                    <option key={key} value={key}>{value.label}</option>
                  ))}
                </select>
              }
            />
          </Section>

          {/* 语音 */}
          <Section icon={voiceAutoRead ? Volume2 : VolumeX} title="语音" panel="general" summary={voiceAutoRead ? '默认朗读' : '已静音'}>
            <Row
              label="自动朗读"
              hint="助手回复后自动播放语音"
              control={
                <Toggle
                  checked={voiceAutoRead}
                  onChange={onVoiceAutoReadChange}
                  label={voiceAutoRead ? '开启' : '关闭'}
                />
              }
            />
            <Row
              label="持久化位置"
              hint="服务端写入 config/config.json"
              control={
                <code className="fairy-code">settings.voice_auto_read</code>
              }
            />
          </Section>

          {/* 工具开关 */}
          <Section icon={Wrench} title="工具机制" panel="tools" summary={`${toolsState.filter(t => t.enabled).length}/${toolsState.length} 已开启`}>
            {toolsState.length === 0 ? (
              <div className="fairy-empty"><Wrench size={16} /><span>暂未读取到工具配置</span></div>
            ) : (
              toolsState.map((tool) => {
                const hasParams = Array.isArray(tool.params) && tool.params.length > 0;
                const expanded = expandedTool === tool.id;
                return (
                  <div key={tool.id} className="settings-tool-group">
                    <Row
                      label={
                        hasParams ? (
                          <button
                            type="button"
                            className="settings-tool-expand"
                            aria-expanded={expanded}
                            onClick={() => toggleToolParams(tool)}
                          >
                            {expanded ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
                            {tool.name}
                          </button>
                        ) : tool.name
                      }
                      hint={
                        hasParams
                          ? '展开可调整运行参数'
                          : (tool.enabled ? '已启用，模型可调用此工具' : '已关闭，模型不会看到此工具')
                      }
                      badge={tool.kind === 'mechanism' ? <span className="fairy-badge">机制</span> : null}
                      control={
                        tool.fixed ? null : (
                          <Toggle
                            checked={tool.enabled}
                            onChange={(value) => handleToolToggle(tool.id, value)}
                            label={tool.enabled ? '开启' : '关闭'}
                          />
                        )
                      }
                    />
                    {hasParams && expanded ? (
                      <div className="settings-tool-params">
                        {tool.params.map((p) => (
                          <label key={p.key} className="settings-tool-param">
                            <span className="settings-tool-param-label">
                              {p.label}
                              {p.unit ? <span className="settings-tool-param-unit">{p.unit}</span> : null}
                            </span>
                            <input
                              type="number"
                              value={paramDraft[p.key] ?? ''}
                              min={p.min}
                              max={p.max}
                              step={p.step}
                              onChange={(e) => setParamDraft((d) => ({ ...d, [p.key]: e.target.value }))}
                            />
                            <span className="settings-tool-param-hint">
                              {p.hint}（默认 {p.default}）
                            </span>
                          </label>
                        ))}
                        <div className="settings-tool-param-actions">
                          <button
                            type="button"
                            onClick={() => handleToolParamsSave(tool)}
                            disabled={paramState.status === 'saving'}
                          >
                            {paramState.status === 'saving' ? '保存中…' : '保存参数'}
                          </button>
                          {paramState.message ? (
                            <span
                              className={
                                paramState.status === 'error'
                                  ? 'settings-tool-param-msg settings-tool-param-msg-error'
                                  : 'settings-tool-param-msg settings-tool-param-msg-ok'
                              }
                            >
                              {paramState.message}
                            </span>
                          ) : null}
                        </div>
                      </div>
                    ) : null}
                  </div>
                );
              })
            )}
            <div className="fairy-form__actions">
              <GhostButton onClick={refreshSettings} icon={RefreshCcw}>刷新工具</GhostButton>
            </div>
          </Section>

          {/* 技能 */}
          <Section icon={Puzzle} title="Skill 技能" panel="skills" summary={`${skillsState.filter(s => s.enabled).length}/${skillsState.length} 已启用`}>
            <SkillTree skills={skillsState} onPatch={handleSkillPatch} onDelete={handleDeleteSkill} />
            <div className="fairy-subsection-title">接入新 Skill</div>
            <div className="fairy-form">
              <div className="fairy-form__row">
                <Field label="Skill 名称">
                  <input className="fairy-input" value={skillDraft.name} onChange={(e) => setSkillDraft({ ...skillDraft, name: e.target.value })} placeholder="如：my-skill" />
                </Field>
                <Field label="SKILL.md 路径">
                  <input className="fairy-input" value={skillDraft.location} onChange={(e) => setSkillDraft({ ...skillDraft, location: e.target.value })} placeholder="E:\\skills\\my-skill" />
                </Field>
              </div>
              <Field label="描述">
                <input className="fairy-input" value={skillDraft.description} onChange={(e) => setSkillDraft({ ...skillDraft, description: e.target.value })} placeholder="可选；留空会读取 SKILL.md" />
              </Field>
              <div className="fairy-form__actions">
                <PrimaryButton onClick={handleAddSkill} loading={skillSubmit.status === 'loading'} icon={Plus}>接入 Skill</PrimaryButton>
              </div>
              {skillSubmit.message ? <div className={`fairy-banner fairy-banner--${skillSubmit.status}`}>{skillSubmit.message}</div> : null}
            </div>
          </Section>

          {/* 模型配置：只保留供应商体系，模型由供应商派生。 */}
          <Section icon={Building2} title="供应商与模型" panel="models" summary="凭据 · 请求地址 · 上游格式 · 模型">
            <ProviderSection models={modelsState} onModelsChanged={refreshModels} />
            {submitState.status !== 'idle' && submitState.message ? (
              <div className={`fairy-banner fairy-banner--${submitState.status}`}>{submitState.message}</div>
            ) : null}
            {testState.status !== 'idle' && testState.message ? (
              <div className={`fairy-banner fairy-banner--${testState.status}`}>{testState.message}</div>
            ) : null}
          </Section>

          {/* 本地 / 自建模型：给 Ollama、vLLM、llama.cpp 这类服务用的
              手工入口。它们没有「拉取模型列表」这回事，所以走独立表单，
              而不是塞进供应商体系。 */}
          <Section
            icon={Sparkles}
            title="本地 / 自建模型"
            panel="local-models"
            summary={`自建模型 ${userModels.length} 个`}
          >
            {userModels.length > 0 ? (
              <ul className="fairy-user-list">
                {userModels.map((m) => (
                  <li key={m.id} className="fairy-user-list__item">
                    <div className="fairy-user-list__main">
                      <span className="fairy-user-list__name">{m.display || m.id}</span>
                      <span className="fairy-user-list__meta">
                        {m.id} · {m.format || 'chat_completions'}
                      </span>
                    </div>
                    <div className="fairy-user-list__actions">
                      <GhostButton
                        onClick={() => handleRemove(m.id)}
                        disabled={removingId === m.id}
                        icon={Trash2}
                        danger
                      >
                        删除
                      </GhostButton>
                    </div>
                  </li>
                ))}
              </ul>
            ) : null}

            <div className="fairy-form">
              <div className="fairy-form__row">
                <Field label="模型 id" hint="以 user_ 开头，例如 user_ollama_qwen">
                  <input
                    className="fairy-input"
                    value={localDraft.id}
                    placeholder="user_ollama_qwen"
                    onChange={(e) => setLocalDraft({ ...localDraft, id: e.target.value })}
                  />
                </Field>
                <Field label="显示名">
                  <input
                    className="fairy-input"
                    value={localDraft.display}
                    placeholder="本地 Qwen"
                    onChange={(e) => setLocalDraft({ ...localDraft, display: e.target.value })}
                  />
                </Field>
              </div>
              <div className="fairy-form__row">
                <Field label="请求地址" hint="例如 http://127.0.0.1:11434/v1">
                  <input
                    className="fairy-input"
                    value={localDraft.base_url}
                    placeholder="http://127.0.0.1:11434/v1"
                    onChange={(e) => setLocalDraft({ ...localDraft, base_url: e.target.value })}
                  />
                </Field>
                <Field label="API Key" hint="本地服务没有鉴权时填任意字符串">
                  <input
                    className="fairy-input"
                    value={localDraft.api_key}
                    placeholder="ollama"
                    onChange={(e) => setLocalDraft({ ...localDraft, api_key: e.target.value })}
                  />
                </Field>
              </div>
              <div className="fairy-form__row">
                <Field label="模型名" hint="发给上游的 model 值">
                  <input
                    className="fairy-input"
                    value={localDraft.model}
                    placeholder="qwen2.5:14b"
                    onChange={(e) => setLocalDraft({ ...localDraft, model: e.target.value })}
                  />
                </Field>
                <Field label="协议">
                  <select
                    className="fairy-select"
                    value={localDraft.format}
                    onChange={(e) => setLocalDraft({ ...localDraft, format: e.target.value })}
                  >
                    <option value="chat_completions">OpenAI Chat Completions</option>
                    <option value="responses">OpenAI Responses</option>
                    <option value="anthropic_messages">Anthropic Messages（Claude）</option>
                  </select>
                </Field>
              </div>
              <div className="fairy-form__row">
                <Field label="输出上限">
                  <input
                    className="fairy-input"
                    type="number"
                    value={localDraft.max_tokens}
                    onChange={(e) => setLocalDraft({ ...localDraft, max_tokens: e.target.value })}
                  />
                </Field>
                <Field label="温度">
                  <input
                    className="fairy-input"
                    type="number"
                    step="0.1"
                    value={localDraft.temperature}
                    onChange={(e) => setLocalDraft({ ...localDraft, temperature: e.target.value })}
                  />
                </Field>
              </div>
              {localState.message ? (
                <div className={`fairy-banner fairy-banner--${localState.status}`}>
                  {localState.message}
                </div>
              ) : null}
              <div className="fairy-form__actions">
                <GhostButton
                  onClick={handleAddLocalModel}
                  disabled={localState.status === 'busy'}
                  icon={Plus}
                >
                  添加自建模型
                </GhostButton>
              </div>
            </div>
          </Section>

          {/* 定时任务 */}
          <Section icon={CalendarClock} title="定时任务" panel="scheduled" summary={scheduledSummary}>
            <ScheduledTasksSection onStatus={(summary) => setScheduledSummary(summary || '未设置')} />
          </Section>

          {/* 记忆：日历 + 每日摘要 + 清理周期。
              打开面板时后端顺带检查一次到期记忆，把超出保留期的每日摘要
              移进 memory/archive/（移动而非删除，随时可调回周期）。 */}
          <Section icon={Brain} title="记忆" panel="memory" summary={memorySummary}>
            <MemoryCalendarSection onStatus={setMemorySummary} />
          </Section>

          {/* 主机资源（仅属主）。后端 /api/system/* 还有 isOwner 兜底，
              这里再加一层：非属主连 Section 都不渲染。 */}
          {isOwner ? (
            <Section icon={Cpu} title="主机" panel="system" summary="实时资源 · 省电档 · 图形栈">
              <SystemSection />
            </Section>
          ) : null}

          {/* 用量统计（仅属主）。数据来自各 session 的 usage.json 逐条聚合，
              包含完整对话的 token 与成本，后端 /api/usage/stats 还有 isOwner 兜底。 */}
          {isOwner ? (
            <Section icon={BarChart3} title="用量" panel="usage" summary="成本 · 请求数 · Tokens · 缓存命中率">
              <UsageSection />
            </Section>
          ) : null}

          {/* 账号 */}
          <Section icon={UserRound} title="账号" panel="account" summary={accountSummary}>
            <AccountSection
              onIdentity={(identity) => setAccountSummary(
                identity?.authEnabled && identity?.username ? identity.username : '未启用',
              )}
            />
          </Section>

          {/* 微信 */}
          <Section icon={MessageCircle} title="微信" panel="wechat" summary={wechatSummary}>
            <WechatSection
              onStatus={(next) => setWechatSummary(next?.linked ? '已接入' : '未接入')}
            />
          </Section>

          {/* QQ */}
          <Section icon={MessageSquare} title="QQ" panel="qq" summary={qqSummary}>
            <QqSection onStatus={(summary) => setQqSummary(summary || '未绑定')} />
          </Section>
        </div>
        </div>
      </div>
    </div>,
    document.body,
  );
}
