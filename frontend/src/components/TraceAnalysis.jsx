import { useMemo } from 'react';
import { activeTraceSpanMs } from '../utils/traceTiming';

// 从 8800「运行分析」迁移过来的可视化，数据全部由已有 trace 事件现算。
const CH_COLORS = ['#6366f1', '#0ea5e9', '#10b981', '#f59e0b', '#ef4444', '#8b5cf6',
  '#ec4899', '#14b8a6', '#f97316', '#84cc16', '#a855f7', '#f43f5e'];
const C_LLM = '#0ea5e9', C_PEAK = '#f59e0b', C_BAD = '#ef6a6a';

function fmtTok(v) { return v != null ? Number(v).toLocaleString() : ''; }
function fmtMs(ms) {
  if (ms == null) return '';
  return ms >= 1000 ? (ms / 1000).toFixed(2) + 's' : ms + 'ms';
}
function fmtDur(ms) {
  const v = ms || 0;
  return v >= 60000 ? (v / 60000).toFixed(1) + 'min' : fmtMs(v);
}
function ChartCard({ title, children, style, stacked }) {
  return (
    <div className="chartcard" style={style}>
      <div className="chart-title">{title}</div>
      <div className="chart-body" style={stacked ? { display: 'block' } : undefined}>{children}</div>
    </div>
  );
}

function SubMatrix({ subs }) {
  if (!subs || !subs.length) return <span className="mut-inline">无子任务</span>;
  const maxOf = k => Math.max(1, ...subs.map(s => s[k] || 0));
  const mc = maxOf('calls'), mm = maxOf('ms'), mp = maxOf('pt');
  const bar = (v, m, color) => (
    <span className="sm-bar"><i style={{ width: (v / m * 100).toFixed(1) + '%', background: color }} /></span>
  );
  return (
    <div className="sm">
      <div className="sm-row sm-head">
        <span className="grow">子任务</span>
        <span className="sm-num">轮次</span>
        <span className="sm-num">耗时</span>
        <span className="sm-num">prompt</span>
        <span className="sm-num">失败</span>
      </div>
      {subs.map(s => (
        <div className="sm-row" key={s.name}>
          <span className="grow sm-name" title={s.name}>{s.name}</span>
          <span className="sm-num">{bar(s.calls, mc, C_LLM)}{s.calls}</span>
          <span className="sm-num">{bar(s.ms, mm, '#0ea5e9')}{fmtDur(s.ms)}</span>
          <span className="sm-num">{bar(s.pt, mp, C_PEAK)}{fmtTok(s.pt)}</span>
          <span className={'sm-num' + (s.fail ? ' bad' : '')}>{s.fail || '—'}</span>
        </div>
      ))}
    </div>
  );
}

function BarRows({ items, color, fmt }) {
  const max = Math.max(1, ...items.map(x => x.v));
  return items.map((x, i) => (
    <div className="bar-row" key={i}>
      <div className="lab" title={x.k}>{x.k}</div>
      <div className="bar"><i style={{ width: (x.v / max * 100).toFixed(1) + '%', background: color }} /></div>
      <div className="num">{fmt ? fmt(x.v) : fmtTok(x.v)}</div>
    </div>
  ));
}

// 每步 LLM tokens：蓝=prompt 绿=completion，标注峰值
function TokensLine({ llms }) {
  if (llms.length < 2) return <span className="mut-inline">数据不足</span>;
  const W = 320, H = 96, padL = 30, padB = 16, padT = 6;
  const mp = Math.max(1, ...llms.map(e => e.prompt_tokens || 0));
  const mc = Math.max(1, ...llms.map(e => e.completion_tokens || 0));
  const peak = Math.max(mp, mc);
  const peakKey = mp >= mc ? 'prompt_tokens' : 'completion_tokens';
  const peakIdx = llms.findIndex(e => (e[peakKey] || 0) === peak);
  const y = v => H - padB - (v / Math.max(mp, mc)) * (H - padB - padT);
  const x = i => padL + (llms.length > 1 ? i / (llms.length - 1) * (W - padL - 6) : W - padL - 6);
  const path = key => 'M' + llms.map((e, i) => x(i).toFixed(1) + ' ' + y(e[key] || 0).toFixed(1)).join(' L');
  const px = x(peakIdx), py = y(peak);
  const lx = Math.min(Math.max(px + 5, padL + 2), W - 62);
  const ly = py < padT + 12 ? py + 13 : py - 6;
  return (
    <svg viewBox={`0 0 ${W} ${H}`} width="100%" height="100">
      <line x1={padL} y1={H - padB} x2={W - 6} y2={H - padB} stroke="#cbd5e1" strokeWidth="1" />
      <path d={path('prompt_tokens')} fill="none" stroke={C_LLM} strokeWidth="1.6" />
      <path d={path('completion_tokens')} fill="none" stroke="#10b981" strokeWidth="1.6" />
      <circle cx={px.toFixed(1)} cy={py.toFixed(1)} r="2.8" fill={C_PEAK} stroke="#e2e8f0" strokeWidth="0.8" />
      <text x={lx.toFixed(1)} y={ly.toFixed(1)} fontSize="9" fontWeight="bold" fill={C_PEAK}>峰值 {fmtTok(peak)}</text>
      <rect x="4" y={H - padB - 34} width="7" height="7" rx="2" fill={C_LLM} />
      <text x="14" y={H - padB - 28} fontSize="9" fill={C_LLM}>prompt</text>
      <rect x="4" y={H - padB - 16} width="7" height="7" rx="2" fill="#10b981" />
      <text x="14" y={H - padB - 10} fontSize="9" fill="#10b981">completion</text>
    </svg>
  );
}

function Donut({ items }) {
  const total = items.reduce((a, b) => a + b.v, 0);
  if (!total) return <span className="mut-inline">无数据</span>;
  const cx = 80, cy = 80, R = 70, r0 = R * 0.6;
  const rad = x => x * Math.PI / 180;
  let a = -90;
  const paths = items.map((it, i) => {
    if (!it.v) return null;
    const a1 = a + it.v / total * 360;
    const col = CH_COLORS[i % CH_COLORS.length];
    const large = (a1 - a) > 180 ? 1 : 0;
    const x0 = cx + R * Math.cos(rad(a)), y0 = cy + R * Math.sin(rad(a));
    const x1 = cx + R * Math.cos(rad(a1)), y1 = cy + R * Math.sin(rad(a1));
    const i0x = cx + r0 * Math.cos(rad(a)), i0y = cy + r0 * Math.sin(rad(a));
    const i1x = cx + r0 * Math.cos(rad(a1)), i1y = cy + r0 * Math.sin(rad(a1));
    const d = `M${x0.toFixed(2)} ${y0.toFixed(2)} A${R} ${R} 0 ${large} 1 ${x1.toFixed(2)} ${y1.toFixed(2)}`
      + ` L${i1x.toFixed(2)} ${i1y.toFixed(2)} A${r0} ${r0} 0 ${large} 0 ${i0x.toFixed(2)} ${i0y.toFixed(2)} Z`;
    a = a1;
    return <path key={i} d={d} fill={col} />;
  });
  return (
    <>
      <svg viewBox="0 0 160 160" width="150" height="150">{paths}</svg>
      <div className="legend">
        {items.map((it, i) => (
          <div className="lg-row" key={i}>
            <span className="dot" style={{ background: CH_COLORS[i % CH_COLORS.length] }} />
            <span className="grow">{it.k}</span>
            <span className="num">{fmtTok(it.v)}</span>
          </div>
        ))}
      </div>
    </>
  );
}


export default function TraceAnalysis({ all, data }) {
  const A = useMemo(() => {
    const evs = all.filter(e => e.timestamp);
    if (!evs.length) return null;
    const ts = evs.map(e => e.timestamp);
    const t0 = Math.min(...ts), t1 = Math.max(...ts);
    const span = Math.max(0, t1 - t0);
    // 墙钟：多轮会话里两次请求之间的用户空档不算耗时（无 loop 标记时退回首末跨度）
    const spanActive = activeTraceSpanMs(evs.filter(e => !e._sub));
    let llmT = 0, toolT = 0;
    evs.forEach(e => {
      if (e.event === 'llm_call') llmT += e.duration_ms || 0;
      else if (e.event === 'tool_result') toolT += e.duration_ms || 0;
    });
    const isLlm = e => e.event === 'llm_call';
    const mainLlm = evs.filter(e => isLlm(e) && !e._sub);
    const subLlm = evs.filter(e => isLlm(e) && e._sub);
    const sum = (arr, k) => arr.reduce((a, e) => a + (e[k] || 0), 0);
    const toolFail = evs.filter(e => e.event === 'tool_failed').length;
    const gateFail = evs.filter(e => {
      const v = e.violations;
      return Array.isArray(v) ? v.length > 0 : (v != null && v !== '');
    }).length;
    const llms = evs.filter(isLlm).slice().sort((a, b) => a.timestamp - b.timestamp);
    const toolCnt = {};
    evs.forEach(e => { if (e.event === 'tool_invoked' && e.tool) toolCnt[e.tool] = (toolCnt[e.tool] || 0) + 1; });
    const toolsTop = Object.entries(toolCnt).sort((a, b) => b[1] - a[1]).slice(0, 7).map(([k, v]) => ({ k, v }));
    // 子任务失败：按来源统计工具失败与违规
    const subFailMap = {};
    evs.filter(e => e._sub).forEach(e => {
      const k = e._source;
      if (!subFailMap[k]) subFailMap[k] = { tool: 0, gate: 0 };
      if (e.event === 'tool_failed') subFailMap[k].tool += 1;
      const v = e.violations;
      if (Array.isArray(v) ? v.length : (v != null && v !== '')) subFailMap[k].gate += 1;
    });
    const subFails = Object.entries(subFailMap)
      .map(([k, v]) => ({ k, tool: v.tool, gate: v.gate }))
      .filter(x => x.tool || x.gate);
    // 各来源聚合：子任务维度（名称去掉尾部时间戳-序号）
    const bySrc = {};
    evs.forEach(e => { const k = e._source || 'main'; (bySrc[k] = bySrc[k] || []).push(e); });
    const subs = Object.keys(bySrc)
      .filter(k => k !== 'main')
      .map(k => {
        const arr = bySrc[k];
        const subLlms = arr.filter(e => e.event === 'llm_call');
        const subTools = arr.filter(e => e.event === 'tool_result');
        const fail = arr.filter(e => e.event === 'tool_failed').length
          + arr.filter(e => { const v = e.violations; return Array.isArray(v) ? v.length > 0 : (v != null && v !== ''); }).length;
        const nm = k.replace(/-\d{10,}(-\d+)?$/, '');
        return {
          name: nm,
          calls: subLlms.length,
          ms: subLlms.reduce((a, e) => a + (e.duration_ms || 0), 0),
          pt: subLlms.reduce((a, e) => a + (e.prompt_tokens || 0), 0),
          fail: fail,
        };
      })
      .sort((a, b) => b.ms - a.ms);
    const mainPrompt = sum(mainLlm, 'prompt_tokens');
    const mainCompletion = sum(mainLlm, 'completion_tokens');
    const subPrompt = sum(subLlm, 'prompt_tokens');
    const subCompletion = sum(subLlm, 'completion_tokens');
    // 成本口径：主线程 + 全部子任务的真实总量
    const totalPrompt = mainPrompt + subPrompt;
    const totalCompletion = mainCompletion + subCompletion;
    return {
      span, spanActive, llmT, toolT, mainLlm, subLlm, toolFail, gateFail, llms, toolsTop,
      subFails, subs,
      waitT: Math.max(0, spanActive - llmT - toolT),
      mainPrompt: mainPrompt,
      mainCompletion: mainCompletion,
      subPrompt: subPrompt,
      subCompletion: subCompletion,
      totalPrompt: totalPrompt,
      totalCompletion: totalCompletion,
    };
  }, [all]);

  if (!A) return null;

  const card = (v, k, title) => (
    <div className="ta-card" title={title} key={k}><div className="v">{v}</div><div className="k">{k}</div></div>
  );

  return (
    <div className="trace-analysis">
      <div className="ta-cards">
        {/* 按 8800 的规则决定这两张卡片（不是按 kind）：
            has_deck = htmls || pngs
            成品标记   ← has_deck || ppt_finished
            HTML/PNG  ← 仅 has_deck（数据分析用例两者都没有，自然不会显示）*/}
        {data && (() => {
          const hasDeck = !!((data.deck && (data.deck.htmls || data.deck.pngs)));
          if (!hasDeck && !data.ppt_finished) return null;
          return (
            <>
              <div className="ta-card"><div className="v">{data.finished ? '是' : '否'}</div><div className="k">成品标记</div></div>
              {hasDeck && (
                <div className="ta-card"><div className="v">{(data.deck.htmls || 0)} / {(data.deck.pngs || 0)}</div><div className="k">HTML / PNG 页</div></div>
              )}
            </>
          );
        })()}
        {card(fmtDur(A.span), '总耗时(墙钟)',
          '会话首个事件到最后一个事件的总时间；= 实际耗时 ' + fmtDur(A.spanActive) + ' + 用户挂机 ' + fmtDur(A.span - A.spanActive))}
        {card(fmtDur(A.spanActive), '实际耗时',
          '总耗时(墙钟) 扣除用户挂机后的实际耗时（用户挂机 ' + fmtDur(A.span - A.spanActive) + '）')}
        {card(A.mainLlm.length, '主线程 LLM 轮次')}
        {card(A.subLlm.length, '子任务 LLM 轮次')}
        {card(fmtTok(A.mainPrompt), '主线程 prompt')}
        {card(fmtTok(A.subPrompt), '子任务 prompt')}
        {card(fmtTok(A.totalPrompt), '全部 prompt', '主线程 + 全部子任务合计')}
        {card(fmtTok(A.subCompletion), '子任务 completion')}
        {card(fmtTok(A.totalCompletion), '全部 completion', '主线程 + 全部子任务合计')}
      </div>
      {/* 主线程统计 pills（8800: cards 之后、子任务之前，带 <h3> 标题）*/}
      {data && data.main_meta && (
        <>
          <h3 className="ta-h3">主线程</h3>
          <div className="tsi-pills">
            <span className={'tsi-pill ' + (data.main_meta.gate_failures ? 'bad' : 'ok')}>闸门失败 {data.main_meta.gate_failures}</span>
            <span className={'tsi-pill ' + (data.main_meta.tool_failures ? 'bad' : 'ok')}>工具失败 {data.main_meta.tool_failures}</span>
            <span className={'tsi-pill ' + (data.main_meta.soft_hint ? 'warn' : 'info')}>软限 {data.main_meta.soft_hint}</span>
            <span className={'tsi-pill ' + (data.main_meta.hard_hint ? 'bad' : 'info')}>硬限 {data.main_meta.hard_hint}</span>
            <span className={'tsi-pill ' + (data.main_meta.repeats ? 'warn' : 'info')}>重派 {data.main_meta.repeats || 0}</span>
            <span className="tsi-pill info">子任务 spawn {data.main_meta.subtask_spawns}</span>
          </div>
        </>
      )}
      {/* 子任务四条 bars（8800 renderOvs 里的区间）*/}
      <h3 className="ta-h3">子任务</h3>
        <div className="charts ta-subs">
          <ChartCard title="子任务对比（轮次 / 耗时 / prompt / 失败）" stacked>
            <SubMatrix subs={A.subs} />
          </ChartCard>
        </div>
      <h3 className="ta-h3">图表</h3>
      <div className="charts">
        <ChartCard title="每步 LLM tokens（蓝=prompt，绿=completion）" stacked>
          <TokensLine llms={A.llms} />
        </ChartCard>
        <ChartCard title="时间构成（实际耗时）">
          <Donut items={[{ k: 'LLM', v: A.llmT }, { k: '工具执行', v: A.toolT }, { k: '等待/其它', v: A.waitT }]} />
        </ChartCard>
        <ChartCard title="工具调用 Top" stacked>
          {A.toolsTop.length ? <BarRows items={A.toolsTop} color="#9a7bff" /> : <span className="mut-inline">无</span>}
        </ChartCard>
        <ChartCard title="子任务失败明细" stacked>
          {A.subFails.length ? A.subFails.map((f, i) => (
            <div className="lg-row" key={i}>
              <span className="dot" style={{ background: C_BAD }} />
              <span className="grow">{f.k}</span>
              <span className="num">工具 {f.tool} · 闸门 {f.gate}</span>
            </div>
          )) : <span className="mut-inline">无失败 ✅</span>}
        </ChartCard>
      </div>
      <div className="ta-main-meta mut-inline">
        工具失败 {A.toolFail} · 闸门失败 {A.gateFail}
      </div>
    </div>
  );
}