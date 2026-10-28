// PlanCard — 把 plan 工具调用从「一维工具行」升级成可读的 PLAN 卡片。
//
// 数据全部来自工具真实报文：arguments（action/question/items）与 result
// （items/question/accepted/updated_at）。没有 mock、没有二次推断模型行为，
// 卡片里每一行都能在报文里找到对应字段。
import React, { useState } from 'react';

const STATUS_META = {
  pending: { label: '待办', tone: 'pending' },
  in_progress: { label: '进行中', tone: 'progress' },
  interrupted: { label: '已中断', tone: 'warn' },
  done: { label: '已完成', tone: 'done' },
  skipped: { label: '已跳过', tone: 'muted' },
};

const CHIP_META = {
  accepted: { label: '已验收', tone: 'done' },
  interrupted: { label: '被阻塞', tone: 'warn' },
  running: { label: '进行中', tone: 'progress' },
  done: { label: '全部完成', tone: 'done' },
  waiting: { label: '待你确认', tone: 'wait' },
};

function clean(value) {
  return String(value == null ? '' : value).replace(/\s+/g, ' ').trim();
}

function parseJSON(value) {
  if (typeof value !== 'string') return value;
  try { return JSON.parse(value); } catch { return null; }
}

function toItemList(value) {
  if (Array.isArray(value)) return value.filter(entry => entry && typeof entry === 'object');
  if (value && typeof value === 'object' && Array.isArray(value.items)) {
    return value.items.filter(entry => entry && typeof entry === 'object');
  }
  return [];
}

function itemStatus(item) {
  return clean(item.status).toLowerCase() || 'pending';
}

function evidenceLine(item) {
  const evidence = Array.isArray(item.evidence) ? item.evidence : [];
  const last = evidence[evidence.length - 1];
  if (last && clean(last.summary)) return clean(last.summary);
  if (clean(item.audit_error)) return clean(item.audit_error);
  return '';
}

function dependencyNotes(items) {
  const notes = [];
  for (const item of items) {
    const id = clean(item.id) || '未命名项';
    const deps = Array.isArray(item.depends_on) ? item.depends_on : (clean(item.depends_on) ? [item.depends_on] : []);
    const labels = deps.map(dep => clean(dep)).filter(Boolean);
    if (labels.length > 0) notes.push(`${id} → ${labels.join('、')}`);
    const parent = clean(item.parent_id);
    if (parent) notes.push(`${id} 隶属于 ${parent}`);
  }
  return notes;
}

function statusNotes(items) {
  const counts = new Map();
  for (const item of items) {
    const meta = STATUS_META[itemStatus(item)] || STATUS_META.pending;
    counts.set(meta.label, (counts.get(meta.label) || 0) + 1);
  }
  return Array.from(counts, ([label, count]) => `${label} ${count} 项`);
}

function evidenceNotes(items) {
  const notes = [];
  for (const item of items) {
    const id = clean(item.id) || '未命名项';
    const summaries = (Array.isArray(item.evidence) ? item.evidence : [])
      .map(entry => clean(entry && entry.summary))
      .filter(Boolean);
    if (summaries.length > 0) notes.push(`${id}：${summaries[summaries.length - 1]}`);
    else if (Array.isArray(item.evidence_refs) && item.evidence_refs.length > 0) {
      notes.push(`${id}：${item.evidence_refs.length} 条工具证据`);
    }
  }
  return notes;
}

function doneWhenNotes(items) {
  return items
    .filter(item => clean(item.done_when))
    .map(item => `${clean(item.id) || '项'}：${clean(item.done_when)}`);
}

function planChip(items, accepted, running) {
  if (running) return CHIP_META.running;
  if (accepted) return CHIP_META.accepted;
  const statuses = items.map(itemStatus);
  if (statuses.length > 0) {
    if (statuses.some(status => status === 'interrupted')) return CHIP_META.interrupted;
    if (statuses.every(status => status === 'done' || status === 'skipped')) return CHIP_META.done;
    if (statuses.some(status => status === 'in_progress')) return CHIP_META.running;
  }
  return CHIP_META.waiting;
}

function formatTime(value) {
  const text = clean(value);
  if (!text) return '';
  const parsed = new Date(text);
  if (Number.isNaN(parsed.getTime())) return text;
  const pad = n => String(n).padStart(2, '0');
  return `${pad(parsed.getMonth() + 1)}-${pad(parsed.getDate())} ${pad(parsed.getHours())}:${pad(parsed.getMinutes())}`;
}

export function readPlanPayload(args, result, running = false) {
  const call = args && typeof args === 'object' ? args : {};
  const payload = result && typeof result === 'object' ? result : {};
  const items = toItemList(payload.items).length > 0
    ? toItemList(payload.items)
    : toItemList(call.items);
  return {
    action: clean(call.action),
    question: clean(payload.question) || clean(call.question),
    items,
    accepted: payload.accepted === true,
    updatedAt: clean(payload.updated_at),
    running,
  };
}

// mark / accept / audit 只是状态推进，用一行摘要表达，不重复渲染整张卡片。
export function PlanMarkLine({ args, result, failed = false }) {
  const call = args && typeof args === 'object' ? args : {};
  const payload = result && typeof result === 'object' ? result : {};
  const id = clean(call.id);
  const status = clean(call.status).toLowerCase();
  const meta = STATUS_META[status];
  const evidence = Array.isArray(payload.evidence) ? payload.evidence : [];
  const last = evidence[evidence.length - 1];
  const summary = clean(payload.error) || clean(call.summary) || (last ? clean(last.summary) : '');
  const accepted = payload.accepted === true;
  return (
    <div className={'plan-mark-line' + (failed ? ' error' : '')}>
      <span className="plan-mark-id">{id || clean(call.action) || 'plan'}</span>
      {accepted ? <span className="plan-mark-state" data-tone="done">已验收</span>
        : meta ? <span className="plan-mark-state" data-tone={meta.tone}>{meta.label}</span>
          : <span className="plan-mark-state" data-tone="pending">{clean(call.action) || '更新'}</span>}
      {summary ? <span className="plan-mark-summary" title={summary}>{summary}</span> : null}
    </div>
  );
}

function PlanField({ name, items, empty, open, onToggle }) {
  const visible = items.slice(0, 8);
  const rest = items.length - visible.length;
  return (
    <div className={'plan-card-field' + (open ? ' open' : '')}>
      <button type="button" className="plan-card-field-chip" onClick={onToggle} aria-expanded={open} title={open ? '收起' : '展开'}>
        <span className="plan-card-field-name">{name}</span>
        <span className="plan-card-field-count">{items.length}</span>
        <span className="plan-card-field-caret">{open ? '▾' : '▸'}</span>
      </button>
      {open ? (
        <ul className="plan-card-foot-list">
          {visible.length === 0 ? <li className="plan-card-foot-empty">{empty}</li> : null}
          {visible.map((text, index) => <li key={index}>{text}</li>)}
          {rest > 0 ? <li className="plan-card-foot-empty">另有 {rest} 项</li> : null}
        </ul>
      ) : null}
    </div>
  );
}

export default function PlanCard({ plan, ordinal = null, stateKey = '' }) {
  const items = plan.items || [];
  const [openField, setOpenField] = useState('');
  const fields = [
    { name: 'status', items: statusNotes(items), empty: '无条目' },
    { name: 'depends_on', items: dependencyNotes(items), empty: '无依赖' },
    { name: 'done_when', items: doneWhenNotes(items), empty: '未登记验收标准' },
    { name: 'evidence', items: evidenceNotes(items), empty: '暂无证据' },
  ];
  const chip = planChip(items, plan.accepted, plan.running);
  const finished = items.filter(item => itemStatus(item) === 'done').length;
  const meta = [
    `${items.length} 项`,
    finished > 0 ? `已完成 ${finished}` : null,
    plan.updatedAt ? `更新于 ${formatTime(plan.updatedAt)}` : null,
  ].filter(Boolean).join(' · ');

  return (
    <section className="plan-card" data-chip={chip.tone} data-state-key={stateKey}>
      <header className="plan-card-head">
        <span className="plan-card-index">{ordinal ? `PLAN #${ordinal}` : 'PLAN'}</span>
        <span className="plan-card-chip" data-tone={chip.tone}>{chip.label}</span>
      </header>

      {plan.question ? <h4 className="plan-card-title">{plan.question}</h4> : null}
      {meta ? <p className="plan-card-meta">{meta}</p> : null}

      <ol className="plan-card-steps">
        {items.map((item, index) => {
          const status = itemStatus(item);
          const meta2 = STATUS_META[status] || STATUS_META.pending;
          const details = clean(item.details);
          const doneWhen = clean(item.done_when);
          const evidence = evidenceLine(item);
          return (
            <li className="plan-card-step" key={clean(item.id) || index} data-status={status}>
              <span className="plan-card-step-no">{index + 1}</span>
              <div className="plan-card-step-body">
                <div className="plan-card-step-head">
                  <span className="plan-card-step-action">{clean(item.action) || clean(item.id) || `第 ${index + 1} 项`}</span>
                  <span className="plan-card-step-state" data-tone={meta2.tone}>{meta2.label}</span>
                </div>
                {details ? <p className="plan-card-step-details">{details}</p> : null}
                {evidence ? <p className="plan-card-step-evidence">已核对 · {evidence}</p> : null}
              </div>
            </li>
          );
        })}
      </ol>

      <footer className="plan-card-foot">
        {fields.map(field => (
          <PlanField
            key={field.name}
            name={field.name}
            items={field.items}
            empty={field.empty}
            open={openField === field.name}
            onToggle={() => setOpenField(value => (value === field.name ? '' : field.name))}
          />
        ))}
      </footer>
    </section>
  );
}

// PLAN #n 的序号：同一目标下的多次 update 视为同一张计划，
// 换了 question 才是新计划，序号才 +1。mark/accept/audit 不参与编号。
export function planOrdinalMap(calls, results) {
  const map = new Map();
  const list = Array.isArray(calls) ? calls : [];
  const resultList = Array.isArray(results) ? results : [];
  let ordinal = 0;
  let key = '';
  list.forEach((call, index) => {
    if (!(call && call.function && call.function.name === 'plan')) return;
    const args = parseJSON(call.function.arguments) || {};
    const action = clean(args.action).toLowerCase();
    if (action === 'mark' || action === 'accept' || action === 'audit') return;
    const payload = parseJSON(resultList[index] && resultList[index].content) || {};
    const question = clean(payload.question || args.question).toLowerCase();
    if (!key || question !== key) {
      ordinal += 1;
      key = question || `plan-${index}`;
    }
    map.set(index, ordinal);
  });
  return map;
}