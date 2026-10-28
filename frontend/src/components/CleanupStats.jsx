import { useMemo } from 'react';

// 本会话的上下文清理统计。
//
// 数据来自 trace 事件，两类：
//   tool_score_cleanup —— 工具结果按评分被替换成摘要，带 saved_runes / cleaned[]
//   reasoning_cleanup  —— 思考窗口按「距末尾多少条」清掉历史 reasoning
//
// 清理的字符数（runes）是后端给的原始值，这里同时给出按 token 的粗估
// （中文 1 字约 1 token，ASCII 4 字符约 1 token），方便和上下文预算对照。

const C_TOOL = '#22c55e';
const C_REASON = '#a855f7';

function fmtNum(v) {
  const n = Number(v) || 0;
  return n.toLocaleString();
}

function estimateTokens(runes) {
  // 只有量级意义：按 ASCII 4 字符/token、中文 1 字符/token 粗估。
  return Math.round(runes / 2.2);
}

function BarRow({ label, value, max, color, right }) {
  const pct = max > 0 ? Math.max(1, Math.round((value / max) * 100)) : 0;
  return (
    <div className="bar-row">
      <div className="bar-label" title={label}>{label}</div>
      <div className="bar-track">
        <div className="bar-fill" style={{ width: pct + '%', background: color }} />
      </div>
      <div className="bar-val">{right}</div>
    </div>
  );
}

export default function CleanupStats({ events }) {
  const stats = useMemo(() => {
    const toolEvents = [];
    const reasonEvents = [];
    for (const e of events || []) {
      const name = String(e.event || '');
      if (name === 'tool_score_cleanup') toolEvents.push(e);
      else if (name === 'reasoning_cleanup') reasonEvents.push(e);
    }

    let savedRunes = 0;
    let clearedResults = 0;
    const byTool = new Map();
    for (const e of toolEvents) {
      savedRunes += Number(e.saved_runes) || 0;
      clearedResults += Number(e.cleared_results) || 0;
      for (const c of e.cleaned || []) {
        const key = c.name || 'unknown';
        const prev = byTool.get(key) || { runes: 0, count: 0, score: 0 };
        prev.runes += Number(c.runes) || 0;
        prev.count += 1;
        prev.score = Math.max(prev.score, Number(c.score) || 0);
        byTool.set(key, prev);
      }
    }

    // 思考清理没有 saved_runes 字段，只有被清掉时的 token 量。
    let reasonTokens = 0;
    for (const e of reasonEvents) {
      reasonTokens += Number(e.tokens) || 0;
    }

    const tools = [...byTool.entries()]
      .map(([name, v]) => ({ name, ...v }))
      .sort((a, b) => b.runes - a.runes);

    return {
      toolCount: toolEvents.length,
      reasonCount: reasonEvents.length,
      savedRunes,
      clearedResults,
      reasonTokens,
      tools,
    };
  }, [events]);

  if (!stats.toolCount && !stats.reasonCount) return null;

  const maxTool = stats.tools.length ? stats.tools[0].runes : 0;
  const savedTokens = estimateTokens(stats.savedRunes);
  const totalTokens = savedTokens + stats.reasonTokens;

  return (
    <div className="ta-card" style={{ marginTop: 12 }}>
      <div className="chart-title">本会话上下文清理</div>
      <div className="chart-body">
        <div style={{ display: 'flex', gap: 18, flexWrap: 'wrap', marginBottom: 10, fontSize: 12 }}>
          <div>
            <div style={{ color: 'var(--ui-text-3)', fontSize: 11 }}>累计省下</div>
            <div style={{ fontSize: 18, fontWeight: 700, color: C_TOOL }}>
              {fmtNum(totalTokens)} <span style={{ fontSize: 11, fontWeight: 400 }}>tokens 估算</span>
            </div>
          </div>
          <div>
            <div style={{ color: 'var(--ui-text-3)', fontSize: 11 }}>工具结果压缩</div>
            <div style={{ fontSize: 18, fontWeight: 700 }}>
              {fmtNum(savedTokens)}
              <span style={{ fontSize: 11, fontWeight: 400 }}> tokens</span>
            </div>
          </div>
          <div>
            <div style={{ color: 'var(--ui-text-3)', fontSize: 11 }}>思考窗口清理</div>
            <div style={{ fontSize: 18, fontWeight: 700, color: C_REASON }}>
              {fmtNum(stats.reasonTokens)}
              <span style={{ fontSize: 11, fontWeight: 400 }}> tokens</span>
            </div>
          </div>
          <div>
            <div style={{ color: 'var(--ui-text-3)', fontSize: 11 }}>被替换的工具结果</div>
            <div style={{ fontSize: 18, fontWeight: 700 }}>
              {fmtNum(stats.clearedResults)}
              <span style={{ fontSize: 11, fontWeight: 400 }}> 条 / {fmtNum(stats.toolCount)} 次</span>
            </div>
          </div>
        </div>

        {stats.tools.length > 0 && (
          <div>
            <div style={{ fontSize: 11, color: 'var(--ui-text-3)', margin: '6px 0 4px' }}>
              按工具（被压缩掉的字符数）
            </div>
            {stats.tools.slice(0, 8).map(t => (
              <BarRow
                key={t.name}
                label={t.name}
                value={t.runes}
                max={maxTool}
                color={C_TOOL}
                right={`${fmtNum(t.runes)} 字符 · ${t.count} 条`}
              />
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
