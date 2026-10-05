import { useEffect, useMemo, useRef, useState } from 'react';
import { fetchSessionTrace } from '../api/chat';
import TraceAnalysis from './TraceAnalysis';
import TraceSessionInfo from './TraceSessionInfo';

const EVENT_LABEL = {
  loop_start: '会话开始',
  llm_call: 'LLM 调用',
  model_response: '模型回复',
  tool_invoked: '工具调用',
  tool_result: '工具结果',
  tool_failed: '工具失败',
  compress: '上下文压缩',
  loop_end: '会话结束',
  auto_answered: '自动回答',
  waiting_user_input: '等待输入',
  loop_error: '循环错误',
  tool_capped: '工具限流',
  user_request: '用户请求',
  soft_step_limit: '软步数提醒',
  soft_limit_hint: '软步数提醒',
  hard_limit_hit: '硬步数上限',
  tool_forbidden_in_subtask: '子任务禁用工具',
  tool_approval_blocked: '工具审批拦截',
  tool_score_cleanup: 'JEV 工具结果清理',
  context_preflight: '上下文清理',
  context_recovery: '上下文恢复',
  compaction_degraded: '压缩降级',
  user_interrupt: '主人插话',
  plan_continuation: '计划续跑',
};

// 时间轴图例：配色语义与 trace-ov-* 一一对应
const OV_LEGEND = [
  { k: 'llm', label: 'LLM' },
  { k: 'res', label: '回复' },
  { k: 'tool', label: '工具' },
  { k: 'cmp', label: '压缩' },
  { k: 'err', label: '错误' },
];

// 刻度标签：按窗口跨度自动降级，避免刻度过长
function fmtTick(ts, range) {
  const d = new Date(ts);
  const p = (n, w = 2) => String(n).padStart(w, '0');
  if (range >= 60000) return p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds());
  if (range >= 1000) return p(d.getMinutes()) + ':' + p(d.getSeconds());
  return p(d.getSeconds()) + '.' + p(d.getMilliseconds(), 3);
}

function fmtMs(ms) {
  if (ms == null) return '';
  return ms >= 1000 ? (ms / 1000).toFixed(2) + 's' : ms + 'ms';
}

function fmtTok(v) {
  return v != null ? Number(v).toLocaleString() : '';
}

function fmtClock(ts) {
  if (!ts) return '';
  const d = new Date(ts);
  const p = n => String(n).padStart(2, '0');
  return p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds()) + '.' + String(d.getMilliseconds()).padStart(3, '0');
}

// 规范化标题：非字母数字中文统一成下划线，去掉尾部时间戳-序号
function normTitle(t) {
  if (!t) return '';
  return String(t)
    .replace(/[^\w\u4e00-\u9fa5]+/g, '_')
    .replace(/^_+|_+$/g, '')
    .replace(/[-_]\d+[-_]\d+$/, '');
}
function tryExtractTitle(argsRaw) {
  if (!argsRaw) return '';
  try { return String(JSON.parse(argsRaw).title || '').trim(); } catch { return ''; }
}

function kindOf(kind) {
  if (kind === 'llm_call') return 'llm';
  if (kind === 'model_response') return 'res';
  if (kind === 'compress' || kind.startsWith('context_')) return 'cmp';
  if (kind.startsWith('tool') || kind === 'tool_capped') return 'tool';
  return 'evt';
}

function isErrKind(kind) {
  return kind === 'tool_failed' || kind === 'loop_error';
}

function tryPretty(s) {
  if (s == null) return '';
  if (typeof s !== 'string') {
    try { return JSON.stringify(s, null, 2); } catch { return String(s); }
  }
  const t = s.trim();
  if (!t) return '';
  if ((t[0] === '{' && t[t.length - 1] === '}') || (t[0] === '[' && t[t.length - 1] === ']')) {
    try { return JSON.stringify(JSON.parse(t), null, 2); } catch { /* keep raw */ }
  }
  return s;
}

// 合并主线程 + 子任务事件，按时间全局排序（Overview 与列表共用）
function useGlobalEvents(data) {
  return useMemo(() => {
    if (!data) return [];
    const all = [];
    data.main.forEach(ev => all.push({ ...ev, _source: 'main', _sourceKey: 'main' }));
    data.subtasks.forEach((s, si) => {
      const sourceKey = s.session || s.file || (s.title ? s.title + '#' + si : 'sub-' + si);
      (s.events || []).forEach(ev => all.push({ ...ev, _source: s.title || sourceKey, _sourceKey: sourceKey, _sub: true }));
    });
    all.sort((a, b) => (a.timestamp || 0) - (b.timestamp || 0));
    return all;
  }, [data]);
}

// ---- 悬浮 Overview：多轨道时间轴（主线程一条，子任务各自一条）+ 缩放/拖选 ----
function TraceOverview({ events, onJump }) {
  const meta = useMemo(() => {
    const withTs = events.filter(e => e.timestamp);
    if (withTs.length === 0) return null;
    let min = Infinity, max = -Infinity;
    for (const e of withTs) {
      min = Math.min(min, e.timestamp);
      max = Math.max(max, e.timestamp);
    }
    const bySrc = new Map();
    for (const e of withTs) {
      const src = e._sourceKey || e._source || 'main';
      if (!bySrc.has(src)) bySrc.set(src, []);
      bySrc.get(src).push(e);
    }
    const keys = ['main', ...[...bySrc.keys()].filter(k => k !== 'main')];
    // 子任务轨道时间范围（title -> {min,max}），用于 create_subtask 标记对齐
    const subtaskRanges = new Map();
    for (const src of keys) {
      if (src === 'main') continue;
      const ts = bySrc.get(src).map(e => e.timestamp).filter(Boolean);
      if (ts.length) subtaskRanges.set(src, { min: Math.min(...ts), max: Math.max(...ts) });
    }
    return { subtaskRanges,
      min, max,
      lanes: keys.map(src => ({
        source: src,
        label: src === 'main' ? '主线程' : (bySrc.get(src)?.[0]?._source || src),
        events: bySrc.get(src),
      })),
    };
  }, [events]);

  // view = 当前可视时间窗口（毫秒时间戳），null 表示全览
  const [view, setView] = useState(null);
  const [sel, setSel] = useState(null); // 拖选中的区间 {a,b}
  const [panning, setPanning] = useState(false);
  const [dragActive, setDragActive] = useState(false);
  const trackRef = useRef(null);
  const dragRef = useRef(null);
  const panRef = useRef(null);

  const v = view || (meta ? { start: meta.min, end: meta.max } : null);
  const viewRef = useRef(v);
  viewRef.current = v;

  const toTime = clientX => {
    const el = trackRef.current;
    if (!el || !v) return 0;
    const rect = el.getBoundingClientRect();
    const ratio = Math.min(Math.max((clientX - rect.left) / rect.width, 0), 1);
    return v.start + ratio * (v.end - v.start);
  };

  // 滚轮缩放（原生监听，passive:false 才能阻止默认滚动）
  useEffect(() => {
    const el = trackRef.current;
    if (!el || !meta) return;
    const onWheel = e => {
      e.preventDefault();
      const cur = viewRef.current || { start: meta.min, end: meta.max };
      const rect = el.getBoundingClientRect();
      const ratio = Math.min(Math.max((e.clientX - rect.left) / rect.width, 0), 1);
      const anchor = cur.start + ratio * (cur.end - cur.start);
      const factor = e.deltaY > 0 ? 0.6 : 1 / 0.6; // 向下滚=放大，向上滚=缩小
      let span = (cur.end - cur.start) * factor;
      const total = meta.max - meta.min;
      span = Math.min(Math.max(span, Math.min(1000, total)), total);
      let s = anchor - ratio * span;
      let en = s + span;
      if (s < meta.min) { s = meta.min; en = s + span; }
      if (en > meta.max) { en = meta.max; s = en - span; }
      setView({ start: s, end: en });
    };
    el.addEventListener('wheel', onWheel, { passive: false });
    return () => el.removeEventListener('wheel', onWheel);
  }, [meta]);

  // 交互：全览=左键拖选放大；放大后=左键/右键拖动平移（右键单击恢复全览）
  const onMouseDown = e => {
    if (e.target.closest('.trace-ov-item')) return;
    if (e.button === 0) {
      if (!view) {
        // 全览：左键拖选放大
        const t = toTime(e.clientX);
        dragRef.current = { startT: t, curT: t };
        setSel({ a: t, b: t });
        setDragActive(true);
      } else {
        // 放大：左键拖动平移
        panRef.current = { startX: e.clientX, startView: { start: view.start, end: view.end } };
        setPanning(true);
      }
    } else if (e.button === 2 && view) {
      // 放大：右键拖动平移；单击恢复全览（由 mouseup 判定）
      panRef.current = { startX: e.clientX, startView: { start: view.start, end: view.end } };
      setPanning(true);
    }
  };

  // 拖动/平移期间用 window 级监听：鼠标移出时间轴也不丢事件
  useEffect(() => {
    if (!panning && !dragActive) return;
    const onMove = e => {
      if (panRef.current) {
        const el = trackRef.current;
        if (!el) return;
        const rect = el.getBoundingClientRect();
        const sv = panRef.current.startView;
        const span = sv.end - sv.start;
        const dt = ((e.clientX - panRef.current.startX) / rect.width) * span;
        let s = sv.start - dt;
        let en = s + span;
        if (s < meta.min) { s = meta.min; en = s + span; }
        if (en > meta.max) { en = meta.max; s = en - span; }
        setView({ start: s, end: en });
        return;
      }
      if (dragRef.current) {
        const t = toTime(e.clientX);
        dragRef.current.curT = t;
        setSel({ a: Math.min(dragRef.current.startT, t), b: Math.max(dragRef.current.startT, t) });
      }
    };
    const onUp = e => {
      if (panRef.current) {
        const moved = Math.abs(e.clientX - panRef.current.startX);
        const wasRight = e.button === 2;
        panRef.current = null;
        setPanning(false);
        if (wasRight && moved < 5) {
          setView(null);
          setSel(null);
        }
        return;
      }
      if (dragRef.current) {
        const { startT, curT } = dragRef.current;
        dragRef.current = null;
        setDragActive(false);
        setSel(null);
        const d = Math.abs(curT - startT);
        if (d > 500 && meta) {
          let s = Math.min(startT, curT), en = Math.max(startT, curT);
          const total = meta.max - meta.min;
          if (en - s < Math.min(1000, total)) en = s + Math.min(1000, total);
          if (s < meta.min) s = meta.min;
          if (en > meta.max) en = meta.max;
          setView({ start: s, end: en });
        }
      }
    };
    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup', onUp);
    return () => {
      window.removeEventListener('mousemove', onMove);
      window.removeEventListener('mouseup', onUp);
    };
  }, [panning, dragActive, meta]);
  const onContextMenu = e => {
    e.preventDefault();
    setView(null);
    setSel(null);
  };
  const resetView = () => { setView(null); setSel(null); };

  // 刻度：固定 6 段，随视图窗口重算（放在早退之前以满足 hooks 规则）
  const ticks = useMemo(() => {
    if (!v) return [];
    const r = Math.max(v.end - v.start, 1);
    const n = 6;
    return Array.from({ length: n + 1 }, (_, i) => ({
      left: (i / n) * 100,
      label: fmtTick(v.start + (r * i) / n, r),
    }));
  }, [v]);

  if (!meta || !v) return null;
  const range = Math.max(v.end - v.start, 1);

  const selBox = sel ? {
    left: Math.min(Math.max(((sel.a - v.start) / range) * 100, 0), 100),
    width: Math.max(Math.min((((sel.b - sel.a) / range) * 100), 100 - ((sel.a - v.start) / range) * 100), 0),
  } : null;

  return (
    <div className="trace-overview">
      <div className="trace-overview-label">
        <span className="trace-ov-kicker">TIMELINE</span>
        <span className="trace-ov-range">{fmtClock(v.start)} <b>→</b> {fmtClock(v.end)}</span>
        <span className="trace-ov-legend">
          {OV_LEGEND.map(l => (
            <span key={l.k} className={'trace-ov-key trace-ov-key-' + l.k}><i />{l.label}</span>
          ))}
        </span>
        <span className="trace-overview-hint">
          {view ? <span><button className="trace-ov-reset" onClick={resetView}>↺ 全览</button> 拖拽平移 · 滚轮缩放 · 右键恢复</span> : '滚轮缩放 · 拖选放大 · 放大后可拖拽平移'}
        </span>
      </div>
      <div className="trace-ov-ruler-row">
        <span className="trace-ov-ruler-cap">TIME</span>
        <div className="trace-ov-ruler">
          {ticks.map((tk, i) => (
            <span
              key={i}
              className={'trace-ov-tick' + (i === 0 ? ' edge-first' : '') + (i === ticks.length - 1 ? ' edge-last' : '')}
              style={{ left: tk.left + '%' }}
            >{tk.label}</span>
          ))}
        </div>
      </div>
      <div
        className={'trace-overview-tracks' + (view ? ' zoomed' : '') + (panning ? ' panning' : '')}
        ref={trackRef}
        onMouseDown={onMouseDown}
        onContextMenu={onContextMenu}
      >
        {selBox && selBox.width > 0 && (
          <div className="trace-ov-sel" style={{ left: selBox.left + '%', width: selBox.width + '%' }} />
        )}
        {meta.lanes.map(lane => (
          <div className="trace-overview-lane" key={lane.source}>
            <span className="trace-overview-lane-label" title={lane.label}>{lane.label}</span>
            <div className="trace-overview-track">
              {lane.events
                .map(e => {
                  let left = ((e.timestamp - v.start) / range) * 100;
                  let dur = e.duration_ms != null ? e.duration_ms : 0;
                  // create_subtask 不画长条：起点/终点标记对齐子任务轨道，避免与子任务范围错位
                  if (e.tool === 'create_subtask') {
                    dur = 0;
                    const title = e.event === 'tool_invoked' ? tryExtractTitle(e.args_raw) : '';
                    if (title) {
                      const nt = normTitle(title);
                      for (const [t, sr] of meta.subtaskRanges.entries()) {
                        const nt2 = normTitle(t);
                        if ((nt && nt2.startsWith(nt)) || (nt2 && nt.startsWith(nt2))) {
                          // 起点标记放子任务开始，终点标记放子任务结束
                          const anchor = e.event === 'tool_invoked' ? sr.min : sr.max;
                          left = ((anchor - v.start) / range) * 100;
                          break;
                        }
                      }
                    }
                  }
                  const width = Math.max((dur / range) * 100, 0.4);
                  return { ev: e, left, width, kind: kindOf(e.event), err: isErrKind(e.event), sub: lane.source !== 'main' };
                })
                .filter(it => it.left > -100 && it.left < 200)
                .map((it, i) => (
                  <div
                    key={eventKey(it.ev) || i}
                    className={'trace-ov-item trace-ov-' + it.kind + (it.err ? ' trace-ov-err' : '') + (it.sub ? ' trace-ov-sub' : '')}
                    style={{ left: it.left + '%', width: it.width + '%' }}
                    title={(EVENT_LABEL[it.ev.event] || it.ev.event) + (it.ev.tool ? ' · ' + it.ev.tool : '') + ' · ' + fmtClock(it.ev.timestamp) + (it.ev.duration_ms != null ? ' · ' + fmtMs(it.ev.duration_ms) : '')}
                    onClick={e => { e.stopPropagation(); onJump(it.ev); }}
                  />
                ))}
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}

// ---- 事件行：来源徽标 + 类型图标 + 摘要 + meta（样式对齐 8800），点击展开局部检查器 ----
function normName(t) { return String(t || '').replace(/-\d{10,}(-\d+)?$/, '').trim(); }
const eventKey = ev => ev && ev.span_id ? ((ev._sourceKey || ev._source || 'main') + '::' + ev.span_id) : '';
const eventDomId = ev => 'trace-row-' + encodeURIComponent(eventKey(ev));

const KIND_ICON = {
  llm: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round"><path d="M13 2 3 14h9l-1 8 10-12h-9l1-8z" /></svg>,
  res: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round"><path d="M21 11.5a8.38 8.38 0 0 1-.9 3.8 8.5 8.5 0 0 1-7.6 4.7 8.38 8.38 0 0 1-3.8-.9L3 21l1.9-5.7a8.38 8.38 0 0 1-.9-3.8 8.5 8.5 0 0 1 4.7-7.6 8.38 8.38 0 0 1 3.8-.9h.5a8.48 8.48 0 0 1 8 8v.5z" /></svg>,
  tool: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round"><path d="M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.77-3.77a6 6 0 0 1-7.94 7.94l-6.91 6.91a2.12 2.12 0 0 1-3-3l6.91-6.91a6 6 0 0 1 7.94-7.94l-3.76 3.76z" /></svg>,
  evt: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round"><path d="M22 12h-4l-3 9L9 3l-3 9H2" /></svg>,
};

// 来源徽标：主线程 / 子任务（后面带序号，按出现顺序轮转配色，同 8800）
function SrcBadge({ ev, idx }) {
  if (!ev._sub) return <span className="src-badge src-main" title="主线程">主线程</span>;
  const name = ev._source || '子任务';
  const seq = (String(name).match(/-(\d+)\s*$/) || [])[1] || '';
  return (
    <span className={'src-badge src-sub c' + (idx % 6)} title={name}>
      {normName(name)}{seq ? '·' + seq : ''}
    </span>
  );
}

function EventRow({ ev, sub, srcIdx, selected, flash, onToggle }) {
  const kind = ev.event || '?';
  const label = EVENT_LABEL[kind] || kind;
  const err = isErrKind(kind);
  const k = kindOf(kind);
  const rowCls = 'trace-row'
    + (err ? ' trace-row-err' : '')
    + (sub ? ' trace-row-sub' : '')
    + (selected ? ' trace-row-sel' : '') + (flash ? ' trace-row-flash' : '');
  return (
    <div
      className={rowCls}
      id={ev.span_id ? eventDomId(ev) : undefined}
      onClick={() => onToggle(ev)}
    >
      <div className="trace-row-main">
        <span className="trace-clock">{fmtClock(ev.timestamp)}</span>
        <span className={'trace-icon trace-icon-' + (err ? 'err' : k)}>{KIND_ICON[k] || KIND_ICON.evt}</span>
        <SrcBadge ev={ev} idx={srcIdx} />
        <span className={'trace-badge trace-badge-' + k}>{label}</span>
        <span className="trace-tool">{ev.tool || ''}</span>
        <span className="trace-preview">{ev.content_preview || ev.err || ev.args_raw || ''}</span>
        <span className="trace-meta">
          {ev.prompt_tokens != null && <span className="trace-tok">↑{fmtTok(ev.prompt_tokens)}</span>}
          {ev.completion_tokens != null && <span className="trace-tok">↓{fmtTok(ev.completion_tokens)}</span>}
          {ev.duration_ms != null && <span className="trace-dur">⏱ {fmtMs(ev.duration_ms)}</span>}
          <span className="trace-step">step {ev.step}</span>
          <span className="trace-arrow">{selected ? '▾' : '▸'}</span>
        </span>
      </div>
      {selected && <TraceDetail ev={ev} />}
    </div>
  );
}

// ---- 局部检查器：完整入参/出参/token/计时 ----
function TraceDetail({ ev }) {
  const rows = [];
  const push = (k, v) => { if (v != null && v !== '') rows.push([k, v]); };
  push('类型', EVENT_LABEL[ev.event] || ev.event);
  push('工具', ev.tool);
  push('时刻', fmtClock(ev.timestamp));
  push('耗时', ev.duration_ms != null ? fmtMs(ev.duration_ms) : '');
  if (ev.prompt_tokens != null || ev.completion_tokens != null) {
    push('Tokens', '↑' + fmtTok(ev.prompt_tokens) + ' ↓' + fmtTok(ev.completion_tokens));
  }
  if (ev.before_tokens != null || ev.after_tokens != null) {
    push('上下文 Tokens', '↑' + fmtTok(ev.before_tokens) + ' → ↓' + fmtTok(ev.after_tokens));
  }
  if (ev.cleared_tool_results) push('清理工具结果', ev.cleared_tool_results);
  if (ev.tool_result_chars_saved) push('节省字符', fmtTok(ev.tool_result_chars_saved));
  if (ev.cleared_results) push('清理结果', ev.cleared_results);
  if (ev.saved_runes) push('节省字符', fmtTok(ev.saved_runes));
  if (ev.scored) push('分类评分', ev.scored);
  if (ev.trigger) push('触发方式', ev.trigger === 'stage_boundary' ? '阶段边界' : ev.trigger === 'tokens' ? 'Token 阈值' : ev.trigger);
  if (ev.threshold != null) push('清理阈值', ev.threshold);
  push('step', ev.step);
  push('upstream_request_id', ev.upstream_request_id);
  push('首包耗时', ev.first_delta_ms != null ? fmtMs(ev.first_delta_ms) : '');
  push('chunk 数', ev.stream_chunk_count);
  push('收到字节', ev.received_bytes != null ? ev.received_bytes + ' B' : '');
  push('正文字节', ev.content_bytes != null ? ev.content_bytes + ' B' : '');
  push('工具参数字节', ev.tool_arguments_bytes != null ? ev.tool_arguments_bytes + ' B' : '');
  push('请求发送', ev.request_sent_at_ms ? fmtClock(ev.request_sent_at_ms) : '');
  push('响应完成', ev.response_complete_at_ms ? fmtClock(ev.response_complete_at_ms) : '');
  push('call_id', ev.call_id);
  push('span_id', ev.span_id);
  push('trace_id', ev.trace_id);
  push('route', ev.route);
  push('compliant', ev.compliant != null ? String(ev.compliant) : '');
  push('violations', ev.violations);
  push('tool_call_count', ev.tool_call_count);
  push('reason', ev.reason);
  push('phase', ev.phase);
  push('page_number', ev.page_number);
  push('point_number', ev.point_number);
  push('field', ev.field);
  const prettyArgs = tryPretty(ev.args_raw);
  const prettyOut = tryPretty(ev.content_full || ev.content_preview);
  const prettyErr = tryPretty(ev.err);
  const appliedEdits = Array.isArray(ev.applied_edits) ? ev.applied_edits : [];
  const prettyEdits = appliedEdits.length ? JSON.stringify(appliedEdits, null, 2) : '';
  const cleanedResults = Array.isArray(ev.cleaned) ? ev.cleaned : [];
  const prettyCleaned = cleanedResults.length ? JSON.stringify(cleanedResults, null, 2) : '';
  return (
    <div className="trace-detail" onClick={e => e.stopPropagation()}>
      {rows.length > 0 && (
        <table className="trace-detail-tbl">
          <tbody>
            {rows.map(([k, v], i) => (
              <tr key={i}><td className="trace-detail-k">{k}</td><td className="trace-detail-v">{v}</td></tr>
            ))}
          </tbody>
        </table>
      )}
      {prettyArgs && <div className="trace-detail-sec"><div className="trace-detail-sec-t">入参</div><pre className="trace-detail-pre">{prettyArgs}</pre></div>}
      {prettyOut && <div className="trace-detail-sec"><div className="trace-detail-sec-t">输出</div><pre className="trace-detail-pre">{prettyOut}</pre></div>}
      {prettyCleaned && <div className="trace-detail-sec"><div className="trace-detail-sec-t">JEV 已清理结果</div><pre className="trace-detail-pre">{prettyCleaned}</pre></div>}
      {prettyEdits && <div className="trace-detail-sec"><div className="trace-detail-sec-t">已应用清理</div><pre className="trace-detail-pre">{prettyEdits}</pre></div>}
      {prettyErr && <div className="trace-detail-sec trace-detail-err"><div className="trace-detail-sec-t">错误</div><pre className="trace-detail-pre">{prettyErr}</pre></div>}
    </div>
  );
}

// ---- 事件列表（滚动区）：分类过滤 + 主线程/子任务来源徽标 ----
const TRACE_FILTERS = [
  { k: 'all', label: '全部' },
  { k: 'main', label: '主线程' },
  { k: 'sub', label: '子任务' },
  { k: 'err', label: '仅错误' },
];

function EventList({ all, selected, flashId, onSelect, filter }) {
  // 子任务按首次出现顺序编号，用于徽标配色
  const srcIdx = useMemo(() => {
    const m = new Map();
    all.forEach(ev => {
      const k = ev._sourceKey || (ev._sub ? (ev._source || 'sub') : 'main');
      if (!m.has(k)) m.set(k, m.size);
    });
    return m;
  }, [all]);
  const list = useMemo(() => all.filter(ev => {
    if (filter === 'main') return !ev._sub;
    if (filter === 'sub') return !!ev._sub;
    if (filter === 'err') return isErrKind(ev.event);
    return true;
  }), [all, filter]);
  const nMain = all.filter(e => !e._sub).length;
  const nSub = all.length - nMain;
  const nErr = all.filter(e => isErrKind(e.event)).length;
  const counts = { all: all.length, main: nMain, sub: nSub, err: nErr };
  return (
    <div className="trace-group">
      <div className="trace-group-title">全局时间线 · {all.length} 条</div>
      {list.map((ev, i) => {
        const isSub = !!ev._sub;
        const rowKey = eventKey(ev);
        const sel = selected && rowKey && selected === rowKey;
        const key = ev._sourceKey || (ev._source || 'main');
        return (
          <EventRow
            key={rowKey || i}
            ev={ev}
            sub={isSub}
            srcIdx={srcIdx.get(key) || 0}
            selected={sel}
            flash={flashId === rowKey}
            onToggle={onSelect}
          />
        );
      })}
      {list.length === 0 && <div className="trace-empty">该分类下暂无事件</div>}
    </div>
  );
}

// 分类条数（chips 在时间轴上方，需要独立算）
function countsOf(all) {
  const main = all.filter(e => !e._sub).length;
  const err = all.filter(e => isErrKind(e.event)).length;
  return { all: all.length, main, sub: all.length - main, err };
}


// 极简 markdown → JSX（对齐 8800 mdHtml：标题、有序/无序列表、粗体、行内 code）
function inline(text, key) {
  const parts = String(text).split(/(\*\*[^*]+\*\*|`[^`]+`)/g).filter(Boolean);
  return parts.map((p, i) => {
    if (p.startsWith('**') && p.endsWith('**')) return <b key={i}>{p.slice(2, -2)}</b>;
    if (p.startsWith('`') && p.endsWith('`')) return <code key={i}>{p.slice(1, -1)}</code>;
    return <span key={i}>{p}</span>;
  });
}
function aiMd(text) {
  const lines = String(text || '').split(/\r?\n/);
  const out = [];
  let list = null;
  // must push React elements: pushing {type,items} makes React throw
  // "Objects are not valid as a React child" and blanks the whole page
  const closeList = () => {
    if (!list) return;
    const k = 'list-' + list.key;
    out.push(list.type === 'ul'
      ? <ul key={k}>{list.items}</ul>
      : <ol key={k}>{list.items}</ol>);
    list = null;
  };
  lines.forEach((raw, i) => {
    const ln = raw.trim();
    if (!ln) { closeList(); return; }
    const h = ln.match(/^(#{1,3})\s+(.*)$/);
    if (h) { closeList(); const lv = h[1].length; out.push(<div key={i} className={'ai-h ai-h' + lv}>{inline(h[2], i)}</div>); return; }
    const ul = ln.match(/^[-*]\s+(.*)$/);
    if (ul) {
      if (!list || list.type !== 'ul') { closeList(); list = { type: 'ul', items: [], key: i }; }
      list.items.push(<li key={i}>{inline(ul[1], i)}</li>); return;
    }
    const ol = ln.match(/^\d+[.)]\s+(.*)$/);
    if (ol) {
      if (!list || list.type !== 'ol') { closeList(); list = { type: 'ol', items: [], key: i }; }
      list.items.push(<li key={i}>{inline(ol[1], i)}</li>); return;
    }
    closeList();
    out.push(<div key={i} className="ai-p">{inline(ln, i)}</div>);
  });
  closeList();
  return out;
}

export default function TracePanel({ name, onClose, data: injectedData }) {
  const [data, setData] = useState(injectedData || null);
  const [err, setErr] = useState('');
  const [selected, setSelected] = useState(null);
  const [flashId, setFlashId] = useState(null);
  const flashTimer = useRef(null);
  const [overviewH, setOverviewH] = useState(132);
  const [filter, setFilter] = useState('all');
  const [aiReports, setAiReports] = useState([]);
  const [aiBusy, setAiBusy] = useState(false);
  const [aiOpen, setAiOpen] = useState({});
  const aiKey = (data && Array.isArray(data.ai_reports))
    ? data.ai_reports.map(r => String(r && r.at)).join('|') : '';
  const [resizing, setResizing] = useState(false);
  const resizeRef = useRef(null);
  const all = useGlobalEvents(data);
  useEffect(() => {
    try {
      const raw = localStorage.getItem('pptui:ai:' + name);
      const list = raw ? JSON.parse(raw) : [];
      setAiReports(Array.isArray(list) ? list : []);
      setAiOpen({});
    } catch { setAiReports([]); }
  }, [name]);

  // 服务端持久化的 AI 报告 + 本地缓存合并（同一份报告只留一条，服务端为准）
  useEffect(() => {
    if (!aiKey) return;
    const server = Array.isArray(data.ai_reports) ? data.ai_reports : [];
    setAiReports(prev => {
      const merged = [];
      const seen = new Set();
      for (const it of [...server, ...(prev || [])]) {
        if (!it) continue;
        const key = it.at != null ? String(it.at) : 't' + String(it.text || '').slice(0, 40);
        if (seen.has(key)) continue;
        seen.add(key);
        merged.push(it);
      }
      return merged.slice(0, 20);
    });
  }, [aiKey]);

  useEffect(() => {
    if (injectedData) return; // 导出视图：数据已注入，不轮询
    let alive = true;
    let timer = null;
    setData(null);
    setSelected(null);
    const load = () => {
      fetchSessionTrace(name)
        .then(d => { if (alive) setData(d); })
        .catch(e => { if (alive) setErr(String(e)); });
    };
    load();
    timer = setInterval(load, 2000); // 实时轮询：每 2 秒刷新
    return () => { alive = false; if (timer) clearInterval(timer); if (flashTimer.current) clearTimeout(flashTimer.current); };
  }, [name]);

  // AI 分析：只喂错误时间线，报告按 trace_id 留存
  const runAi = async () => {
    if (!data) return;
    setAiBusy(true);
    try {
      const errs = (data.main || [])
        .concat(...(data.subtasks || []).map(t => t.events || []))
        .filter(e => e.event === 'tool_failed' || e.event === 'loop_error'
          || (e.violations && (Array.isArray(e.violations) ? e.violations.length : true)))
        .slice(0, 40)
        .map(e => ({ event: e.event, tool: e.tool || '', err: String(e.err || '').slice(0, 300) }));
      const r = await fetch('/api/ai-analyze', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name, trace_id: data.trace_id || '', errors: errs }),
      });
      const d = await r.json();
      if (d.error) { setAiReports(v => [{ error: d.error, at: Date.now() }, ...v]); return; }
      const item = { text: d.text || '', at: Date.now() };
      setAiReports(v => {
        const next = [item, ...v].slice(0, 20);
        try { localStorage.setItem('pptui:ai:' + name, JSON.stringify(next)); } catch {}
        return next;
      });
    } catch (e) {
      setAiReports(v => [{ error: String(e), at: Date.now() }, ...v]);
    } finally { setAiBusy(false); }
  };

  const onSelect = ev => {
    const key = eventKey(ev);
    setSelected(prev => (prev && key && prev === key) ? null : (key || null));
  };
  const onJump = ev => {
    const id = eventKey(ev);
    if (!id) return;
    const el = document.getElementById(eventDomId(ev));
    if (el) {
      el.scrollIntoView({ behavior: 'smooth', block: 'center' });
      setSelected(id);
      setFlashId(id);
      if (flashTimer.current) clearTimeout(flashTimer.current);
      flashTimer.current = setTimeout(() => setFlashId(null), 2200);
    }
  };

  // 时间轴高度拖拽调整（window 级监听，鼠标移出也不丢）
  const startResize = e => {
    e.preventDefault();
    e.stopPropagation();
    resizeRef.current = { startY: e.clientY, startH: overviewH };
    setResizing(true);
  };
  useEffect(() => {
    if (!resizing) return;
    const onMove = e => {
      if (!resizeRef.current) return;
      const h = Math.min(480, Math.max(80, resizeRef.current.startH + (e.clientY - resizeRef.current.startY)));
      setOverviewH(h);
    };
    const onUp = () => { resizeRef.current = null; setResizing(false); };
    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup', onUp);
    return () => {
      window.removeEventListener('mousemove', onMove);
      window.removeEventListener('mouseup', onUp);
    };
  }, [resizing]);

  return (
    <div className="trace-panel">
      <div className="trace-panel-head">
        <span className="trace-panel-title">Trace · {name}</span>
        {data && data.trace_id && <span className="trace-tid">trace_id: {data.trace_id}</span>}
        <button className="trace-close" onClick={onClose}>✕ 关闭</button>
      </div>
      {/* 悬浮时间轴：固定不滚动，跳转后仍可见 */}
      <div className="trace-panel-body">
        {err && <div className="trace-err">加载失败：{err}</div>}
        {!data && !err && <div className="trace-empty">加载中…</div>}
        {data && data.main.length === 0 && data.subtasks.length === 0 && <div className="trace-empty">暂无 trace（需用最新版 agent 重新跑一次会话）</div>}
        {data && all.length > 0 && <TraceSessionInfo data={data} onAnalyze={runAi} aiBusy={aiBusy} sessionName={name} />}
        {/* AI 运行分析报告：多条堆叠，结构与样式照搬 8800 的 .ai-panel */}
        {aiReports.map((it, idx) => {
          const open = aiOpen[idx] === true; // 默认收起，点标题栏才展开
          return (
            <div className={'ai-panel saved' + (open ? ' open' : '')} key={it.at || idx}>
              <div className="ai-ph" onClick={() => setAiOpen(v => ({ ...v, [idx]: !open }))}
                title="点击展开/收起">
                <span className="ai-ico">
                  <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor"
                    strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                    <path d="M12 3v3M12 18v3M3 12h3M18 12h3M5.6 5.6l2.1 2.1M16.3 16.3l2.1 2.1M18.4 5.6l-2.1 2.1M7.7 16.3l-2.1 2.1" />
                  </svg>
                </span>
                <span className="ai-titles">
                  <span className="ai-title">AI 运行分析报告</span>
                  <span className="ai-time">{fmtClock(it.at)}</span>
                </span>
                <span className="ai-caret">▾</span>
                <span className="ai-x" title="删除此条报告"
                  onClick={e => {
                    e.stopPropagation();
                    try {
                      fetch('/api/ai-reports/delete', {
                        method: 'POST', headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({ session: name, at: it.at }),
                      }).catch(() => {});
                    } catch {}
                    setAiReports(v => {
                      const next = v.filter((_, i) => i !== idx);
                      try { localStorage.setItem('pptui:ai:' + name, JSON.stringify(next)); } catch {}
                      return next;
                    });
                  }}>×</span>
              </div>
              {it.error
                ? <div className="ai-body" style={{ display: 'block' }}>
                    <div className="ai-p bad">{it.error}</div>
                  </div>
                : <>
                    <div className="ai-teaser">{String(it.text || '').replace(/[#*`>-]/g, '').slice(0, 96)}</div>
                    <div className="ai-body"><div className="ai-md">{aiMd(it.text)}</div></div>
                  </>}
            </div>
          );
        })}
        {/* 分析区：卡片 / 主线程 pills / 子任务 bars / 图表 / 时间分析 */}
        {data && all.length > 0 && <TraceAnalysis all={all} data={data} />}
        {/* 时间轴：位置对齐 8800（分析区之后、事件列表之前），在滚动区里吸顶固定 */}
        {all.length > 0 && (
          <div className="trace-timeline-sticky">
              {/* 8800 的层级：Trace 时间线标题 → 分类 chips → 多轨时间轴 → 事件行 */}
              <div className="trace-tl-head">
                <span>Trace 时间线（{all.length} 事件）</span>
                <div className="trace-filters">
                  {TRACE_FILTERS.map(f => (
                    <span
                      key={f.k}
                      className={'trace-chip' + (filter === f.k ? ' on' : '') + (f.k === 'err' ? ' err' : '')}
                      onClick={() => setFilter(f.k)}
                    >
                      {f.label} {countsOf(all)[f.k]}
                    </span>
                  ))}
                </div>
              </div>
              <div
                className={'trace-resize-handle' + (resizing ? ' active' : '')}
                onMouseDown={startResize}
                title="拖拽调整时间轴高度"
              />
              <div className="trace-overview-wrap" style={{ maxHeight: overviewH + 'px' }}>
                <TraceOverview events={all} onJump={onJump} />
              </div>
              </div>
        )}
        {all.length > 0 && <EventList all={all} selected={selected} flashId={flashId} onSelect={onSelect} filter={filter} />}
      </div>
    </div>
  );
}
