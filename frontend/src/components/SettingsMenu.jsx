import { useEffect, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import {
  Bot,
  CalendarClock,
  Check,
  ChevronDown,
  ChevronRight,
  Eye,
  EyeOff,
  KeyRound,
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
} from '../api/chat';
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
  const [accountSummary, setAccountSummary] = useState('未启用');
  const [wechatSummary, setWechatSummary] = useState('未接入');
  const [qqSummary, setQqSummary] = useState('未绑定');
  const [scheduledSummary, setScheduledSummary] = useState('未设置');
  const [toolsState, setToolsState] = useState([]);
  const [skillsState, setSkillsState] = useState([]);
  const [skillDraft, setSkillDraft] = useState({ name: '', location: '', description: '' });
  const [skillSubmit, setSkillSubmit] = useState({ status: 'idle', message: '' });
  const [draft, setDraft] = useState(() => makeBlankDraft('deepseek'));
  const [testState, setTestState] = useState({ status: 'idle', message: '' });
  const [submitState, setSubmitState] = useState({ status: 'idle', message: '' });
  const [removingId, setRemovingId] = useState('');
  const [remoteModels, setRemoteModels] = useState([]);
  const [discovering, setDiscovering] = useState(false);
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

  const handleSelectModel = (id) => {
    writeStoredModel(id);
    onSelectModel?.(id);
  };

  const handleToolToggle = async (id, enabled) => {
    setToolsState(list => list.map(tool => tool.id === id ? { ...tool, enabled } : tool));
    const result = await updateToolSetting(id, enabled);
    if (!result.ok) {
      setTestState({ status: 'error', message: result.error || `更新工具 ${id} 失败` });
      await refreshSettings();
    }
  };

  const handleSkillToggle = async (name, enabled) => {
    setSkillsState(list => list.map(skill => skill.name === name ? { ...skill, enabled } : skill));
    const result = await updateSkillSetting(name, enabled);
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

  const navItems = [
    { id: 'general', label: '常规', icon: Settings2 },
    { id: 'models', label: '模型', icon: Bot },
    { id: 'tools', label: '工具', icon: Wrench },
    { id: 'skills', label: '技能', icon: Puzzle },
    { id: 'appearance', label: '外观', icon: Palette },
    { id: 'voice', label: '语音', icon: Volume2 },
    { id: 'scheduled', label: '定时', icon: CalendarClock },
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
          <Section icon={Palette} title="外观" panel="appearance" summary={THEMES[theme]?.label || theme || '基础'}>
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
          <Section icon={voiceAutoRead ? Volume2 : VolumeX} title="语音" panel="voice" summary={voiceAutoRead ? '默认朗读' : '已静音'}>
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
              toolsState.map(tool => (
                <Row
                  key={tool.id}
                  label={tool.name}
                  hint={tool.enabled ? '已启用，模型可调用此工具' : '已关闭，模型不会看到此工具'}
                  badge={tool.kind === 'mechanism' ? <span className="fairy-badge">机制</span> : null}
                  control={
                    <Toggle
                      checked={tool.enabled}
                      onChange={(value) => handleToolToggle(tool.id, value)}
                      label={tool.enabled ? '开启' : '关闭'}
                    />
                  }
                />
              ))
            )}
            <div className="fairy-form__actions">
              <GhostButton onClick={refreshSettings} icon={RefreshCcw}>刷新工具</GhostButton>
            </div>
          </Section>

          {/* 技能 */}
          <Section icon={Puzzle} title="Skill 技能" panel="skills" summary={`${skillsState.filter(s => s.enabled).length}/${skillsState.length} 已启用`}>
            {skillsState.map(skill => (
              <Row
                key={skill.name}
                label={skill.name}
                hint={skill.description || skill.location || 'Skill'}
                badge={skill.registered ? <span className="fairy-badge">已注册</span> : null}
                control={
                  <Toggle
                    checked={skill.enabled}
                    onChange={(value) => handleSkillToggle(skill.name, value)}
                    label={skill.enabled ? '开启' : '关闭'}
                  />
                }
              />
            ))}
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

          {/* 新增模型 */}
          <Section icon={Plus} title="新增 AI 模型" panel="models" summary="供应商 + API Key">
            <div className="fairy-grid">
              {PROVIDER_PRESETS.map((p) => {
                const active = draft.presetId === p.id;
                return (
                  <button
                    key={p.id}
                    type="button"
                    className={`fairy-preset ${active ? 'is-active' : ''}`}
                    onClick={() => onChangePreset(p.id)}
                  >
                    <span className="fairy-preset__badge">{p.label.slice(0, 2)}</span>
                    <span className="fairy-preset__copy">
                      <span className="fairy-preset__name">{p.label}</span>
                      <span className="fairy-preset__hint">{p.hint}</span>
                    </span>
                  </button>
                );
              })}
            </div>

            <div className="fairy-form">
              <div className="fairy-form__row">
                <Field label="显示名称">
                  <input
                    className="fairy-input"
                    value={draft.display}
                    onChange={(e) => setDraft({ ...draft, display: e.target.value })}
                    placeholder="如：DeepSeek-我的"
                  />
                </Field>
                <Field label="模型 ID" hint="用于内部引用，前缀 user_">
                  <input
                    className="fairy-input"
                    value={draft.id}
                    onChange={(e) => setDraft({ ...draft, id: e.target.value })}
                    placeholder="如：deepseek-personal"
                  />
                </Field>
              </div>

              <Field label="Base URL" hint="兼容 OpenAI 协议的接口地址">
                <input
                  className="fairy-input"
                  value={draft.baseUrl}
                  onChange={(e) => setDraft({ ...draft, baseUrl: e.target.value })}
                  placeholder="https://api.example.com/v1"
                />
              </Field>

              <Field label="API Key">
                <div className="fairy-input__with-action">
                  <input
                    className="fairy-input"
                    type={draft.showKey ? 'text' : 'password'}
                    value={draft.apiKey}
                    onChange={(e) => setDraft({ ...draft, apiKey: e.target.value })}
                    placeholder="sk-…"
                    autoComplete="off"
                  />
                  <button
                    type="button"
                    className="fairy-input__action"
                    onClick={() => setDraft({ ...draft, showKey: !draft.showKey })}
                    aria-label="切换显示"
                  >
                    {draft.showKey ? <EyeOff size={14} /> : <Eye size={14} />}
                  </button>
                </div>
              </Field>

              <Field label="模型名称" hint="该供应商下的真实模型名">
                <input
                  className="fairy-input"
                  list="fairy-provider-models"
                  value={draft.model}
                  onChange={(e) => setDraft({ ...draft, model: e.target.value })}
                  placeholder="如：deepseek-chat"
                />
                <datalist id="fairy-provider-models">
                  {remoteModels.map(model => <option key={model} value={model} />)}
                </datalist>
                {remoteModels.length > 0 ? (
                  <div className="fairy-model-options" aria-label="可用模型列表">
                    <div className="fairy-model-options__head">
                      <span>可用模型</span>
                      <span>{remoteModels.length}</span>
                    </div>
                    <div className="fairy-model-options__list" role="listbox">
                      {remoteModels.map((model) => {
                        const active = draft.model === model;
                        return (
                          <button
                            key={model}
                            type="button"
                            role="option"
                            aria-selected={active}
                            className={`fairy-model-option${active ? ' is-active' : ''}`}
                            onClick={() => setDraft((current) => ({ ...current, model }))}
                          >
                            <span className="fairy-model-option__name">{model}</span>
                            {active ? <Check size={13} /> : null}
                          </button>
                        );
                      })}
                    </div>
                  </div>
                ) : null}
              </Field>

              <div className="fairy-form__row">
                <Field label="Temperature">
                  <input
                    className="fairy-input"
                    type="number"
                    min="0"
                    max="2"
                    step="0.1"
                    value={draft.temperature}
                    onChange={(e) => setDraft({ ...draft, temperature: e.target.value })}
                  />
                </Field>
                <Field label="Max tokens">
                  <input
                    className="fairy-input"
                    type="number"
                    min="256"
                    step="256"
                    value={draft.maxTokens}
                    onChange={(e) => setDraft({ ...draft, maxTokens: e.target.value })}
                  />
                </Field>
              </div>

              <div className="fairy-form__actions">
                <GhostButton onClick={handleDiscover} disabled={discovering} icon={RefreshCcw}>
                  获取模型
                </GhostButton>
                <GhostButton onClick={handleTest} disabled={testState.status === 'loading'} icon={ShieldCheck}>
                  测试连接
                </GhostButton>
                <PrimaryButton onClick={handleAdd} loading={submitState.status === 'loading'} icon={Wand2}>
                  添加模型
                </PrimaryButton>
              </div>

              {testState.status !== 'idle' && testState.message ? (
                <div className={`fairy-banner fairy-banner--${testState.status}`}>{testState.message}</div>
              ) : null}
              {submitState.status !== 'idle' && submitState.message ? (
                <div className={`fairy-banner fairy-banner--${submitState.status}`}>{submitState.message}</div>
              ) : null}
            </div>
          </Section>

          {/* 已添加的模型 */}
          <Section
            icon={Sparkles}
            title="我的模型"
            panel="models"
            summary={`用户模型 ${userModels.length} / 共 ${modelsState.length}`}
          >
            {userModels.length === 0 ? (
              <div className="fairy-empty">
                <KeyRound size={16} />
                <span>暂未添加自定义模型，使用上方表单添加一个吧。</span>
              </div>
            ) : (
              <ul className="fairy-user-list">
                {userModels.map((m) => (
                  <li key={m.id} className="fairy-user-list__item">
                    <div className="fairy-user-list__main">
                      <span className="fairy-user-list__name">{m.display || m.id}</span>
                      <span className="fairy-user-list__meta">{m.id}</span>
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
            )}
            <div className="fairy-form__actions">
              <GhostButton onClick={refreshModels} icon={RefreshCcw}>
                刷新列表
              </GhostButton>
            </div>
          </Section>

          {/* 定时任务 */}
          <Section icon={CalendarClock} title="定时任务" panel="scheduled" summary={scheduledSummary}>
            <ScheduledTasksSection onStatus={(summary) => setScheduledSummary(summary || '未设置')} />
          </Section>

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
