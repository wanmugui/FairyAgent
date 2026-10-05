import { useCallback, useEffect, useRef, useState } from 'react';
import { CalendarClock, Loader2, Pencil, Play, RefreshCcw, Trash2, X } from 'lucide-react';

/**
 * 定时任务面板。
 *
 * 任务属于**账号**而不是会话："8 点提醒我吃药"是关于人的，和他当时在哪条会话里
 * 说的无关。到点会在该账号当天的会话里跑一轮，选了推送目标再把结果发到那条 QQ
 * 会话。时间一律按机器时区解释，"每天 08:00"就是墙上的 08:00。
 */

const WEEKDAYS = [
  { value: 1, label: '周一' },
  { value: 2, label: '周二' },
  { value: 3, label: '周三' },
  { value: 4, label: '周四' },
  { value: 5, label: '周五' },
  { value: 6, label: '周六' },
  { value: 7, label: '周日' },
];

async function callApi(path, method = 'GET', body) {
  const res = await fetch(path, {
    method,
    headers: body ? { 'Content-Type': 'application/json; charset=utf-8' } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  });
  let data = {};
  try {
    data = await res.json();
  } catch {
    data = {};
  }
  if (!res.ok || data.ok === false) {
    throw new Error(data.error || `请求失败（HTTP ${res.status}）`);
  }
  return data;
}

function formatTime(value) {
  if (!value) return '—';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return String(value);
  return date.toLocaleString('zh-CN', { hour12: false });
}

export default function ScheduledTasksSection({ onStatus }) {
  const [tasks, setTasks] = useState([]);
  const [meta, setMeta] = useState(null);
  const [loading, setLoading] = useState(true);
  const [busyId, setBusyId] = useState('');
  // 非空表示正在编辑这条任务：表单回填它的字段，保存时走 update 而不是新建。
  const [editingId, setEditingId] = useState('');
  const formRef = useRef(null);
  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState(null);

  const [title, setTitle] = useState('');
  const [type, setType] = useState('daily');
  const [time, setTime] = useState('08:00');
  const [weekday, setWeekday] = useState(1);
  const [minutes, setMinutes] = useState(30);
  const [onceAt, setOnceAt] = useState('');
  const [prompt, setPrompt] = useState('');
  // 后端 action.kind 支持 prompt / command，两种任务填的东西和下发的字段都不一样。
  const [kind, setKind] = useState('prompt');
  const [command, setCommand] = useState('');
  const [notifyChannel, setNotifyChannel] = useState('auto');
  const [notifyTo, setNotifyTo] = useState('');

  const mountedRef = useRef(true);
  const notifyRef = useRef(onStatus);
  notifyRef.current = onStatus;

  const publish = useCallback((list) => {
    const running = (list || []).filter((item) => item.enabled).length;
    notifyRef.current?.(running ? `${running} 个在跑` : (list || []).length ? '已停' : '未设置');
  }, []);

  const refresh = useCallback(async () => {
    try {
      const data = await callApi('/api/scheduled-tasks');
      if (!mountedRef.current) return;
      const list = Array.isArray(data.tasks) ? data.tasks : [];
      setTasks(list);
      setMeta(data.meta || null);
      publish(list);
    } catch (err) {
      if (mountedRef.current) {
        setMessage({ kind: 'error', text: err.message || '读取定时任务失败' });
        publish([]);
      }
    } finally {
      if (mountedRef.current) setLoading(false);
    }
  }, [publish]);

  useEffect(() => {
    mountedRef.current = true;
    refresh();
    return () => { mountedRef.current = false; };
  }, [refresh]);

  // schedule.at 存的是 ISO 字符串，而表单的 datetime-local 要的是本地
  // "YYYY-MM-DDTHH:mm"，直接塞进去会差一个时区。
  const toLocalInput = (iso) => {
    if (!iso) return '';
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return '';
    const pad = (n) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
      + `T${pad(d.getHours())}:${pad(d.getMinutes())}`;
  };

  const resetForm = () => {
    setEditingId('');
    setTitle('');
    setPrompt('');
    setCommand('');
    setType('daily');
    setTime('08:00');
    setWeekday(1);
    setMinutes(30);
    setOnceAt('');
    setKind('prompt');
    setNotifyChannel('auto');
    setNotifyTo('');
  };

  // 把已存在的任务反向映射回表单字段。之前没有这条路径，想改时间只能删了重建。
  const startEdit = (task) => {
    const sch = task.schedule || {};
    setEditingId(task.id);
    setTitle(task.title || '');
    setType(sch.type || 'daily');
    setTime(sch.type === 'once' ? '08:00' : (sch.at || '08:00'));
    setWeekday(typeof sch.weekday === 'number' ? sch.weekday : 1);
    setMinutes(typeof sch.minutes === 'number' ? sch.minutes : 30);
    setOnceAt(sch.type === 'once' ? toLocalInput(sch.at) : '');
    setKind(task.action?.kind || 'prompt');
    setPrompt(task.action?.text || '');
    setCommand(task.action?.command || '');
    setNotifyChannel(task.notify?.channel || 'auto');
    setNotifyTo(task.notify?.conversation_id || '');
    setMessage(null);
    // 表单在任务列表上方。不滚过去的话，用户点完「编辑」眼睛还盯着列表，
    // 会以为没有填入框、也点不到输入框。
    requestAnimationFrame(() => {
      formRef.current?.scrollIntoView({ behavior: 'smooth', block: 'start' });
      const first = formRef.current?.querySelector('input:not([type=hidden]), textarea, select');
      if (first) first.focus({ preventScroll: true });
    });
  };

  const submit = async (event) => {
    event.preventDefault();
    setSaving(true);
    setMessage(null);
    try {
      let schedule;
      if (type === 'every') {
        schedule = { type: 'every', minutes: Number(minutes) };
      } else if (type === 'weekly') {
        schedule = { type: 'weekly', weekday: Number(weekday), at: time };
      } else if (type === 'once') {
        const at = new Date(onceAt);
        if (Number.isNaN(at.getTime())) throw new Error('一次性任务需要一个具体时间');
        schedule = { type: 'once', at: at.toISOString() };
      } else {
        schedule = { type: 'daily', at: time };
      }
      const suggestion = meta?.suggested_notify || null;
      let notify = null;
      if (notifyChannel === 'auto') {
        notify = suggestion
          ? { channel: suggestion.channel, conversation_id: suggestion.conversation_id }
          : null;
      } else if (notifyChannel !== 'none') {
        const conversationId = notifyTo.trim()
          || (suggestion && suggestion.channel === notifyChannel ? suggestion.conversation_id : '');
        if (!conversationId) throw new Error('这条通道需要一个会话 id');
        notify = { channel: notifyChannel, conversation_id: conversationId };
      }
      if (kind === 'command' && !command.trim()) {
        throw new Error('命令任务需要一条要执行的命令');
      }
      if (kind === 'prompt' && !prompt.trim()) {
        throw new Error('提示词任务需要写清楚要做什么');
      }
      const action = kind === 'command'
        ? { kind: 'command', command: command.trim() }
        : { kind: 'prompt', text: prompt };
      // 保留调度器支持、但本表单不编辑的两个字段，否则每次在设置页保存都会被抹掉：
      //   session —— 支持 {today} 占位符；带 "__" 时走子会话（分支会话）而不是主会话
      //   guard   —— 闸门脚本；退出码非 0 则不唤醒 agent
      // 丢了 session 会让定时任务掉回主会话，丢了 guard 会让它无活也空转唤醒。
      const prevAction = (tasks || []).find((t) => t.id === editingId)?.action || {};
      if (prevAction.session) action.session = prevAction.session;
      if (prevAction.guard) action.guard = prevAction.guard;
      if (editingId) {
        const updated = await callApi('/api/scheduled-tasks/update', 'POST', {
          id: editingId,
          title,
          schedule,
          action,
          notify,
        });
        const next = updated?.task?.next_run_text;
        setMessage({ kind: 'ok', text: next ? `「${title}」已更新，下一次：${next}` : `「${title}」已更新` });
      } else {
        await callApi('/api/scheduled-tasks', 'POST', { title, schedule, action, notify });
      }
      // resetForm() 会清空 editingId，所以先记住这一轮是不是编辑。
      const wasEditing = Boolean(editingId);
      resetForm();
      // 编辑分支上面已经设过"已更新 + 下一次"，这里别再盖掉它。
      if (!wasEditing) {
        setMessage({ kind: 'ok', text: '已创建，到点会在当天会话里跑一轮' });
      }
      await refresh();
    } catch (err) {
      setMessage({ kind: 'error', text: err.message || (editingId ? '保存失败' : '创建失败') });
    } finally {
      if (mountedRef.current) setSaving(false);
    }
  };

  const toggle = async (task) => {
    setBusyId(task.id);
    try {
      await callApi('/api/scheduled-tasks/toggle', 'POST', { id: task.id, enabled: !task.enabled });
      await refresh();
    } catch (err) {
      setMessage({ kind: 'error', text: err.message || '操作失败' });
    } finally {
      if (mountedRef.current) setBusyId('');
    }
  };

  const runNow = async (task) => {
    setBusyId(task.id);
    try {
      await callApi('/api/scheduled-tasks/run', 'POST', { id: task.id });
      setMessage({ kind: 'ok', text: `「${task.title}」已开始跑，跑完自动显示结果` });
      // 立即运行是异步的（接口只回 started），这里隔一下再拉一次，让「上次结果」自己冒出来。
      for (let i = 0; i < 6; i += 1) {
        await new Promise((r) => setTimeout(r, 900));
        if (!mountedRef.current) return;
        await refresh();
      }
    } catch (err) {
      setMessage({ kind: 'error', text: err.message || '运行失败' });
    } finally {
      if (mountedRef.current) setBusyId('');
    }
  };

  const remove = async (task) => {
    setBusyId(task.id);
    try {
      await callApi('/api/scheduled-tasks/delete', 'POST', { id: task.id });
      await refresh();
    } catch (err) {
      setMessage({ kind: 'error', text: err.message || '删除失败' });
    } finally {
      if (mountedRef.current) setBusyId('');
    }
  };

  return (
    <>
      <div className="fairy-row">
        <div className="fairy-row__label">
          <span className="fairy-row__title">到点做什么</span>
          <span className="fairy-row__hint">
            时区 {meta?.timezone || 'Asia/Shanghai'}，任务跟着账号走（成员的列表互相看不到）。
            到点在该账号当天的会话里跑一轮；填了 QQ 会话就把结果也发过去。
          </span>
        </div>
        <div className="fairy-row__control">
          <button type="button" className="fairy-btn fairy-btn--ghost" onClick={refresh}>
            <RefreshCcw size={14} /> <span>刷新</span>
          </button>
        </div>
      </div>

      <form ref={formRef} className={"fairy-form" + (editingId ? " fairy-form--editing" : "")} onSubmit={submit}>
        <div className="fairy-form__row">
          <div className="fairy-field">
            <label className="fairy-field__label" htmlFor="task-title">标题</label>
            <input
              id="task-title"
              className="fairy-input"
              value={title}
              maxLength={120}
              placeholder="例如：提醒吃药"
              onChange={(e) => setTitle(e.target.value)}
            />
          </div>
          <div className="fairy-field">
            <label className="fairy-field__label" htmlFor="task-type">时间</label>
            <select id="task-type" className="fairy-select" value={type} onChange={(e) => setType(e.target.value)}>
              <option value="daily">每天</option>
              <option value="weekly">每周</option>
              <option value="every">每隔一段时间</option>
              <option value="once">只一次</option>
            </select>
          </div>
          {type === 'weekly' && (
            <div className="fairy-field">
              <label className="fairy-field__label" htmlFor="task-weekday">星期</label>
              <select id="task-weekday" className="fairy-select" value={weekday} onChange={(e) => setWeekday(e.target.value)}>
                {WEEKDAYS.map((day) => (
                  <option key={day.value} value={day.value}>{day.label}</option>
                ))}
              </select>
            </div>
          )}
          {(type === 'daily' || type === 'weekly') && (
            <div className="fairy-field">
              <label className="fairy-field__label" htmlFor="task-time">几点</label>
              <input id="task-time" className="fairy-input" type="time" value={time} onChange={(e) => setTime(e.target.value)} />
            </div>
          )}
          {type === 'every' && (
            <div className="fairy-field">
              <label className="fairy-field__label" htmlFor="task-minutes">间隔（分钟，至少 {meta?.minimum_every_minutes || 5}）</label>
              <input
                id="task-minutes"
                className="fairy-input"
                type="number"
                min={meta?.minimum_every_minutes || 5}
                value={minutes}
                onChange={(e) => setMinutes(e.target.value)}
              />
            </div>
          )}
          {type === 'once' && (
            <div className="fairy-field">
              <label className="fairy-field__label" htmlFor="task-once">什么时候</label>
              <input id="task-once" className="fairy-input" type="datetime-local" value={onceAt} onChange={(e) => setOnceAt(e.target.value)} />
            </div>
          )}
        </div>

        <div className="fairy-form__row">
          <div className="fairy-field">
            <label className="fairy-field__label" htmlFor="task-kind">任务类型</label>
            <select
              id="task-kind"
              className="fairy-select"
              value={kind}
              onChange={(e) => setKind(e.target.value)}
            >
              <option value="prompt">交给 Fairy 做事（提示词）</option>
              <option value="command">直接跑一条命令</option>
            </select>
          </div>
        </div>

        <div className="fairy-form__row">
          <div className="fairy-field">
            {kind === 'command' ? (
              <>
                <label className="fairy-field__label" htmlFor="task-command">要执行的命令</label>
                <input
                  id="task-command"
                  className="fairy-input"
                  value={command}
                  maxLength={2000}
                  placeholder="例如：date &gt;&gt; /tmp/x.log"
                  onChange={(e) => setCommand(e.target.value)}
                />
                <p className="fairy-field__hint">
                  直接在本机执行，命令的输出会作为「上次结果」显示。改小心里面的内容。
                </p>
              </>
            ) : (
              <>
                <label className="fairy-field__label" htmlFor="task-prompt">交给 Fairy 做的事</label>
                <textarea
                  id="task-prompt"
                  className="fairy-input"
                  rows={2}
                  value={prompt}
                  placeholder="例如：看看今天的天气，并提醒我吃药"
                  onChange={(e) => setPrompt(e.target.value)}
                />
              </>
            )}
          </div>
        </div>

        <div className="fairy-form__row">
          <div className="fairy-field">
            <label className="fairy-field__label" htmlFor="task-notify">推到哪</label>
            <select
              id="task-notify"
              className="fairy-select"
              value={notifyChannel}
              onChange={(e) => setNotifyChannel(e.target.value)}
            >
              <option value="auto">
                自动{meta?.suggested_notify
                  ? `（${meta.suggested_notify.channel === 'qq' ? 'QQ' : '微信'}，你最近用的那条）`
                  : '（还没有可用的通道）'}
              </option>
              <option value="qq">QQ</option>
              <option value="wechat">微信</option>
              <option value="none">不推送（只留在会话里）</option>
            </select>
          </div>
          {(notifyChannel === 'qq' || notifyChannel === 'wechat') && (
            <div className="fairy-field">
              <label className="fairy-field__label" htmlFor="task-notify-to">
                {notifyChannel === 'qq' ? 'QQ 私聊 openid' : '微信用户 id'}
              </label>
              <input
                id="task-notify-to"
                className="fairy-input"
                value={notifyTo}
                placeholder={meta?.suggested_notify?.channel === notifyChannel
                  ? `留空用 ${meta.suggested_notify.conversation_id}`
                  : '留空就用这个通道默认的那个人'}
                onChange={(e) => setNotifyTo(e.target.value)}
              />
            </div>
          )}
        </div>

        {message ? (
          <div className={`fairy-banner ${message.kind === 'ok' ? 'fairy-banner--ok' : 'fairy-banner--error'}`}>
            <span>{message.text}</span>
          </div>
        ) : null}

        <div className="fairy-form__actions">
          <button type="submit" className="fairy-btn fairy-btn--primary" disabled={saving || !title.trim() || (kind === 'command' ? !command.trim() : !prompt.trim())}>
            {saving ? <Loader2 size={14} className="fairy-btn__spin" /> : <CalendarClock size={14} />}
            <span>{saving ? '创建中…' : editingId ? '编辑定时任务' : '新建定时任务'}</span>
            {editingId ? (
              <button type="button" className="fairy-btn fairy-btn--ghost" onClick={resetForm} disabled={saving}>
                <X size={13} /> <span>取消编辑</span>
              </button>
            ) : null}
          </button>
        </div>
      </form>

      <div className="fairy-subsection-title">已设置的定时任务</div>
      {loading ? (
        <div className="fairy-banner fairy-banner--loading"><Loader2 size={14} className="fairy-btn__spin" /> <span>读取中…</span></div>
      ) : tasks.length === 0 ? (
        <div className="fairy-row">
          <div className="fairy-row__label">
            <span className="fairy-row__hint">还没有定时任务。</span>
          </div>
        </div>
      ) : (
        tasks.map((task) => (
          <div className="fairy-row" key={task.id}>
            <div className="fairy-row__label">
              <span className="fairy-row__title">
                {task.title}
                {task.enabled ? '' : '（已停用）'}
                {task.last_status === 'error' ? ' · 上次失败' : ''}
              </span>
              <span className="fairy-row__hint">
                {task.schedule_text} · 下次 {formatTime(task.next_run_text)} · 已跑 {task.run_count} 次
                {task.notify?.conversation_id ? ' · 推送到 QQ' : ''}
              </span>
              <span className="fairy-row__hint">{task.prompt}</span>
              {task.last_error
                ? <span className="fairy-row__hint">上次失败：{task.last_error}</span>
                : task.last_summary
                  ? <span className="fairy-row__hint">上次结果：{task.last_summary.slice(0, 120)}</span>
                  : null}
            </div>
            <div className="fairy-row__control">
              <button type="button" className="fairy-btn fairy-btn--ghost" disabled={busyId === task.id} onClick={() => startEdit(task)}>
                <Pencil size={13} /> <span>编辑</span>
              </button>
              <button type="button" className="fairy-btn fairy-btn--ghost" disabled={busyId === task.id} onClick={() => toggle(task)}>
                <span>{task.enabled ? '停用' : '启用'}</span>
              </button>
              <button type="button" className="fairy-btn fairy-btn--ghost" disabled={busyId === task.id} onClick={() => runNow(task)}>
                {busyId === task.id ? <Loader2 size={13} className="fairy-btn__spin" /> : <Play size={13} />}
                <span>立即运行</span>
              </button>
              <button type="button" className="fairy-btn fairy-btn--ghost" disabled={busyId === task.id} onClick={() => remove(task)}>
                <Trash2 size={13} /> <span>删除</span>
              </button>
            </div>
          </div>
        ))
      )}
    </>
  );
}
