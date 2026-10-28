import React, { useEffect, useMemo, useState } from "react";
import { BarChart3, RefreshCw, TrendingUp } from "lucide-react";

/**
 * Usage statistics panel.
 *
 * The data comes from /api/usage/stats, which aggregates the durable
 * per-turn usage ledger. Tokens are the ones the upstream provider reported:
 *
 *   real tokens   = prompt + completion - cached prompt
 *   cache hit rate = cached prompt / prompt
 *
 * A model without a known price is reported as unpriced rather than as a
 * zero cost, because a zero would read as "this request was free".
 */

const GRANULARITIES = [
  { id: "day", label: "按天" },
  { id: "hour", label: "按小时" },
];

const METRICS = [
  { id: "tokens", label: "Tokens" },
  { id: "requests", label: "请求" },
  { id: "cost", label: "成本" },
];

function formatNumber(value) {
  const n = Number(value) || 0;
  if (Math.abs(n) >= 1_000_000_000) return (n / 1_000_000_000).toFixed(2) + "B";
  if (Math.abs(n) >= 1_000_000) return (n / 1_000_000).toFixed(2) + "M";
  if (Math.abs(n) >= 1_000) return (n / 1_000).toFixed(1) + "K";
  return String(n);
}

function formatCost(value, priced) {
  if (!priced) return "未定价";
  const n = Number(value) || 0;
  if (n === 0) return "$0.00";
  if (n < 0.01) return "$" + n.toFixed(4);
  return "$" + n.toFixed(2);
}

function formatPercent(ratio) {
  return ((Number(ratio) || 0) * 100).toFixed(1) + "%";
}

function formatDuration(ms) {
  const n = Number(ms) || 0;
  if (n <= 0) return "—";
  if (n < 1000) return `${n}ms`;
  return `${(n / 1000).toFixed(1)}s`;
}

/** Tokens per second, derived from the completion tokens and the duration. */
function speedOf(record) {
  const ms = Number(record.duration_ms) || 0;
  const out = Number(record.completion_tokens) || 0;
  if (ms <= 0 || out <= 0) return null;
  return (out / (ms / 1000)).toFixed(0);
}

/**
 * Shorten a bucket key for the x axis. A day key is "2026-10-22" and an
 * hour key is "2026-10-22 14:00", both of which are too wide for a dozen
 * labels, so only the part that carries the information is kept.
 */
function shortBucketLabel(key) {
  const day = /^(\d{4})-(\d{2})-(\d{2})/.exec(key);
  if (!day) return key;
  const short = `${day[2]}-${day[3]}`;
  const hour = / (\d{2}):00$/.exec(key);
  return hour ? `${short} ${hour[1]}:00` : short;
}

/**
 * A bar chart drawn with plain SVG. The project ships no chart library, and
 * adding one for a single panel is not worth the bundle cost.
 */
function TrendChart({ points, metric }) {
  const width = 900;
  const height = 220;
  const padding = { top: 16, right: 16, bottom: 26, left: 48 };
  const innerW = width - padding.left - padding.right;
  const innerH = height - padding.top - padding.bottom;

  const values = points.map((point) => {
    if (metric === "requests") return Number(point.requests) || 0;
    if (metric === "cost") return Number(point.cost) || 0;
    return Number(point.real_tokens) || 0;
  });
  const max = Math.max(...values, 0);
  const usable = max > 0 ? max : 1;
  const slot = points.length > 0 ? innerW / points.length : innerW;
  const barW = Math.max(2, Math.min(56, slot * 0.6));

  const ticks = [0, 0.25, 0.5, 0.75, 1].map((ratio) => ({
    y: padding.top + innerH - innerH * ratio,
    label: formatNumber(usable * ratio),
  }));

  // Show at most a dozen x labels so they never overlap.
  const labelStride = Math.max(1, Math.ceil(points.length / 12));

  return (
    <svg className="usage-chart" viewBox={`0 0 ${width} ${height}`} role="img" aria-label="用量趋势">
      {ticks.map((tick) => (
        <g key={tick.y}>
          <line
            className="usage-chart-grid"
            x1={padding.left}
            x2={width - padding.right}
            y1={tick.y}
            y2={tick.y}
          />
          <text className="usage-chart-tick" x={padding.left - 8} y={tick.y + 4} textAnchor="end">
            {tick.label}
          </text>
        </g>
      ))}
      {points.map((point, index) => {
        const value = values[index];
        const barH = (value / usable) * innerH;
        const x = padding.left + slot * index + (slot - barW) / 2;
        const y = padding.top + innerH - barH;
        return (
          <g key={point.key}>
            <rect
              className="usage-chart-bar"
              x={x}
              y={y}
              width={barW}
              height={Math.max(1, barH)}
              rx={2}
            >
              <title>{`${point.key}: ${formatNumber(value)}`}</title>
            </rect>
            {index % labelStride === 0 ? (
              <text className="usage-chart-label" x={x + barW / 2} y={height - 8} textAnchor="middle">
                {shortBucketLabel(point.key)}
              </text>
            ) : null}
          </g>
        );
      })}
    </svg>
  );
}

export default function UsageSection() {
  const [stats, setStats] = useState(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [granularity, setGranularity] = useState("day");
  const [metric, setMetric] = useState("tokens");
  const [provider, setProvider] = useState("");
  const [model, setModel] = useState("");

  const load = useMemo(
    () => async () => {
      setLoading(true);
      setError("");
      try {
        const params = new URLSearchParams({ granularity, limit: "300" });
        if (provider) params.set("provider", provider);
        if (model) params.set("model", model);
        const res = await fetch(`/api/usage/stats?${params.toString()}`);
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        const data = await res.json();
        if (data && data.error) throw new Error(data.error);
        setStats(data);
      } catch (err) {
        setError(err.message || String(err));
        setStats(null);
      } finally {
        setLoading(false);
      }
    },
    [granularity, provider, model]
  );

  useEffect(() => {
    load();
  }, [load]);

  const summary = stats?.summary;
  const total = stats?.total_summary;

  return (
    <div className="usage-panel">
      <div className="usage-panel-head">
        <div className="usage-panel-title">
          <BarChart3 size={16} />
          <span>用量统计</span>
        </div>
        <div className="usage-panel-actions">
          <select value={provider} onChange={(e) => setProvider(e.target.value)} aria-label="供应商">
            <option value="">全部供应商</option>
            {(stats?.dimensions?.providers || []).map((name) => (
              <option key={name} value={name}>
                {name}
              </option>
            ))}
          </select>
          <select value={model} onChange={(e) => setModel(e.target.value)} aria-label="模型">
            <option value="">全部模型</option>
            {(stats?.dimensions?.models || []).map((name) => (
              <option key={name} value={name}>
                {name}
              </option>
            ))}
          </select>
          <button type="button" onClick={load} disabled={loading} className="usage-refresh">
            <RefreshCw size={13} className={loading ? "usage-spin" : ""} />
            刷新
          </button>
        </div>
      </div>

      {error ? (
        <div className="usage-empty">读取用量统计失败：{error}</div>
      ) : !summary ? (
        <div className="usage-empty">正在加载用量统计…</div>
      ) : (
        <>
          <div className="usage-cards">
            <div className="usage-card">
              <div className="usage-card-label">总成本</div>
              <div className="usage-card-value">
                {summary.unpriced_requests > 0
                  ? formatCost(summary.total_cost, true) + "*"
                  : formatCost(summary.total_cost, true)}
              </div>
              {summary.unpriced_requests > 0 ? (
                <div className="usage-card-hint">
                  {summary.unpriced_requests} 次请求未定价，未计入
                </div>
              ) : null}
            </div>
            <div className="usage-card">
              <div className="usage-card-label">总请求数</div>
              <div className="usage-card-value">{formatNumber(summary.total_requests)}</div>
            </div>
            <div className="usage-card">
              <div className="usage-card-label">真实消耗 Tokens</div>
              <div className="usage-card-value">{formatNumber(summary.real_tokens)}</div>
              <div className="usage-card-hint">
                输入 {formatNumber(summary.prompt_tokens)} · 输出 {formatNumber(summary.completion_tokens)}
              </div>
            </div>
            <div className="usage-card">
              <div className="usage-card-label">缓存命中率</div>
              <div className="usage-card-value">{formatPercent(summary.cache_hit_rate)}</div>
              <div className="usage-card-hint">缓存 {formatNumber(summary.cached_tokens)} tokens</div>
            </div>
          </div>

          <div className="usage-trend">
            <div className="usage-trend-head">
              <div className="usage-trend-title">
                <TrendingUp size={14} />
                <span>使用趋势</span>
              </div>
              <div className="usage-trend-controls">
                <div className="usage-segment">
                  {GRANULARITIES.map((item) => (
                    <button
                      key={item.id}
                      type="button"
                      className={granularity === item.id ? "usage-seg-active" : ""}
                      onClick={() => setGranularity(item.id)}
                    >
                      {item.label}
                    </button>
                  ))}
                </div>
                <div className="usage-segment">
                  {METRICS.map((item) => (
                    <button
                      key={item.id}
                      type="button"
                      className={metric === item.id ? "usage-seg-active" : ""}
                      onClick={() => setMetric(item.id)}
                    >
                      {item.label}
                    </button>
                  ))}
                </div>
              </div>
            </div>
            {stats.trend && stats.trend.length ? (
              <TrendChart points={stats.trend} metric={metric} />
            ) : (
              <div className="usage-empty">暂无趋势数据</div>
            )}
          </div>

          <div className="usage-log">
            <div className="usage-log-head">
              <span>请求日志</span>
              <span className="usage-log-count">
                共 {formatNumber(stats.pagination?.total || 0)} 条
              </span>
            </div>
            {stats.records && stats.records.length ? (
              <div className="usage-table-wrap">
                <table className="usage-table">
                  <thead>
                    <tr>
                      <th>会话</th>
                      <th>模型</th>
                      <th>新增输入</th>
                      <th>输出</th>
                      <th>缓存命中</th>
                      <th>成本</th>
                      <th>速度</th>
                    </tr>
                  </thead>
                  <tbody>
                    {stats.records.map((record) => (
                      <tr key={record.id}>
                        <td className="usage-cell-strong" title={record.session_path || record.session}>
                          {(() => {
                            const label = record.session_path || record.session;
                            return label.length > 26 ? label.slice(0, 26) + "…" : label;
                          })()}
                        </td>
                        <td>{record.model || "—"}</td>
                        <td>{formatNumber(record.prompt_tokens - record.cached_tokens)}</td>
                        <td>{formatNumber(record.completion_tokens)}</td>
                        <td>{record.cached_tokens ? formatNumber(record.cached_tokens) : "—"}</td>
                        <td>{formatCost(record.cost, record.priced)}</td>
                        <td>{speedOf(record) ? speedOf(record) + " tok/s" : "—"}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : (
              <div className="usage-empty">暂无请求记录</div>
            )}
          </div>

          {total && summary && total.total_requests !== summary.total_requests ? (
            <div className="usage-footnote">
              当前筛选：{formatNumber(summary.total_requests)} / {formatNumber(total.total_requests)} 次请求
            </div>
          ) : null}
        </>
      )}
    </div>
  );
}
