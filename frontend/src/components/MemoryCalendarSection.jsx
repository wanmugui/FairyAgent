import { useCallback, useEffect, useMemo, useState } from 'react';
import { Archive, CalendarDays, Loader2, RefreshCcw } from 'lucide-react';

/**
 * 记忆栏：日历 + 每日摘要 + 清理周期。
 *
 * 数据来自 memory/date-memory/YYYY-MM-DD.md —— 每一天一个文件，由会话总结器写入。
 * 点日历上的某一天，就看那天的本地记忆摘要（按会话分段）。
 *
 * 清理周期有两个约束：
 *   1. 只作用于每日摘要。分段记忆（memory/segments/）有独立的 KEY 索引和
 *      memory_search 检索链路，混在一起清会打断检索，所以不碰。
 *   2. 到期不删除，移进 memory/archive/。主人随时能把周期调回去，
 *      也不至于因为一次误设置把半年的记忆抹掉。
 *
 * 清理在打开设置面板时检查一次 —— 不起常驻定时线程，主人改完设置立即生效。
 */

const WEEKDAY_LABELS = ['一', '二', '三', '四', '五', '六', '日'];

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

function pad2(n) {
  return String(n).padStart(2, '0');
}

function formatKey(year, month, day) {
  return `${year}-${pad2(month)}-${pad2(day)}`;
}

/** 把 YYYY-MM-DD 拆成数字，避免 new Date(string) 的 UTC 偏移问题。 */
function parseKey(key) {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(String(key || ''));
  if (!m) return null;
  return { year: Number(m[1]), month: Number(m[2]), day: Number(m[3]) };
}

/** 生成日历网格：6 行 × 7 列，前后补齐相邻月份的日期。 */
function buildGrid(year, month, dayKeys) {
  const first = new Date(year, month - 1, 1);
  const startOffset = (first.getDay() + 6) % 7; // 周一为一周之首
  const gridStart = new Date(year, month - 1, 1 - startOffset);

  const cells = [];
  for (let i = 0; i < 42; i++) {
    const d = new Date(gridStart.getFullYear(), gridStart.getMonth(), gridStart.getDate() + i);
    const key = formatKey(d.getFullYear(), d.getMonth() + 1, d.getDate());
    cells.push({
      key,
      day: d.getDate(),
      inMonth: d.getMonth() + 1 === month && d.getFullYear() === year,
      hasMemory: dayKeys.has(key),
      isToday: key === formatKey(new Date().getFullYear(), new Date().getMonth() + 1, new Date().getDate()),
    });
  }
  return cells;
}

/** 毫秒时间戳转本地 HH:MM。0 或非法值返回空串。 */
function formatClock(ms) {
  const n = Number(ms);
  if (!Number.isFinite(n) || n <= 0) return '';
  const d = new Date(n);
  const hh = String(d.getHours()).padStart(2, '0');
  const mm = String(d.getMinutes()).padStart(2, '0');
  return `${hh}:${mm}`;
}

export default function MemoryCalendarSection({ onStatus }) {
  const [calendar, setCalendar] = useState(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  // 当前查看的月份（year/month），点箭头翻月
  const today = useMemo(() => new Date(), []);
  const [cursor, setCursor] = useState({ year: today.getFullYear(), month: today.getMonth() + 1 });

  const [selected, setSelected] = useState('');
  const [detail, setDetail] = useState(null);
  const [detailLoading, setDetailLoading] = useState(false);

  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await callApi('/api/memory/calendar');
      setCalendar(data);
      // Report the outcome, otherwise the section summary keeps showing
      // "加载中" forever — onStatus was only ever called when the
      // retention setting was saved.
      if (onStatus) {
        const days = data && Array.isArray(data.days) ? data.days.length : 0;
        onStatus(days > 0 ? `${days} 天有记录` : '暂无记忆');
      }
    } catch (err) {
      setError(err.message || '读取记忆日历失败');
      if (onStatus) onStatus('读取失败');
    } finally {
      setLoading(false);
    }
  }, [onStatus]);

  useEffect(() => {
    load();
  }, [load]);

  const openDay = useCallback(async (key) => {
    if (!key) return;
    setSelected(key);
    setDetailLoading(true);
    setDetail(null);
    try {
      const data = await callApi(`/api/memory/day?date=${encodeURIComponent(key)}`);
      setDetail(data.day);
    } catch (err) {
      setError(err.message || '读取当天交互失败');
    } finally {
      setDetailLoading(false);
    }
  }, []);

  const dayKeys = useMemo(() => {
    const set = new Set();
    for (const d of (calendar && calendar.days) || []) set.add(d.date);
    return set;
  }, [calendar]);

  const cells = useMemo(() => buildGrid(cursor.year, cursor.month, dayKeys), [cursor, dayKeys]);

  const monthStats = useMemo(() => {
    const prefix = `${cursor.year}-${pad2(cursor.month)}-`;
    const list = ((calendar && calendar.days) || []).filter(d => d.date.startsWith(prefix));
    return {
      count: list.length,
      interactions: list.reduce((n, d) => n + (d.interactions || 0), 0),
    };
  }, [calendar, cursor]);

  function shiftMonth(delta) {
    setCursor(prev => {
      const d = new Date(prev.year, prev.month - 1 + delta, 1);
      return { year: d.getFullYear(), month: d.getMonth() + 1 };
    });
  }

  async function saveRetention(months) {
    setSaving(true);
    setMessage(null);
    try {
      const data = await callApi('/api/memory/retention', 'POST', { retention_months: months });
      setMessage(
        data.archived && data.archived.length
          ? `已归档 ${data.archived.length} 天的旧记忆`
          : '保留期已更新',
      );
      await load();
      if (onStatus) onStatus(message);
    } catch (err) {
      setError(err.message || '保存保留期失败');
    } finally {
      setSaving(false);
    }
  }

  const retention = calendar ? calendar.retention : 0;
  const archive = (calendar && calendar.archive) || { count: 0, bytes: 0 };

  return (
    <div className="mem-panel">
      <div className="mem-panel__head">
        <div className="mem-panel__title">
          <CalendarDays size={16} />
          <span>记忆日历</span>
        </div>
        <button
          type="button"
          className="mem-btn mem-btn--ghost"
          onClick={load}
          disabled={loading}
          title="刷新"
        >
          {loading ? <Loader2 size={14} className="mem-spin" /> : <RefreshCcw size={14} />}
        </button>
      </div>

      {error ? <div className="mem-error">{error}</div> : null}

      <div className="mem-cal">
        <div className="mem-cal__bar">
          <button type="button" className="mem-btn mem-btn--ghost" onClick={() => shiftMonth(-1)} title="上个月">
            ‹
          </button>
          <span className="mem-cal__month">
            {cursor.year} 年 {cursor.month} 月
          </span>
          <button type="button" className="mem-btn mem-btn--ghost" onClick={() => shiftMonth(1)} title="下个月">
            ›
          </button>
        </div>

        <div className="mem-cal__weekdays">
          {WEEKDAY_LABELS.map(label => (
            <span key={label}>{label}</span>
          ))}
        </div>

        <div className="mem-cal__grid">
          {cells.map(cell => (
            <button
              key={cell.key}
              type="button"
              className={[
                'mem-cal__day',
                cell.inMonth ? '' : 'mem-cal__day--muted',
                cell.hasMemory ? 'mem-cal__day--has' : '',
                cell.isToday ? 'mem-cal__day--today' : '',
                selected === cell.key ? 'mem-cal__day--active' : '',
              ]
                .filter(Boolean)
                .join(' ')}
              onClick={() => openDay(cell.hasMemory ? cell.key : '')}
              title={cell.hasMemory ? `${cell.key} 有记忆记录` : cell.key}
              disabled={!cell.hasMemory}
            >
              <span className="mem-cal__num">{cell.day}</span>
            </button>
          ))}
        </div>

        <div className="mem-cal__foot">
          本月 {monthStats.count} 天有记忆 · 共 {monthStats.interactions} 次交互
        </div>
      </div>

      <div className="mem-section">
        <div className="mem-section__title">清理周期</div>
        <div className="mem-section__hint">
          到期的每日摘要会移入 <code>memory/archive/</code>，不会删除。分段记忆（可检索的那部分）不受影响。
        </div>
        <div className="mem-retention">
          {((calendar && calendar.retention_options) || [0, 1, 3, 6, 12, 24]).map(opt => (
            <button
              key={opt}
              type="button"
              className={`mem-chip${retention === opt ? ' is-on' : ''}`}
              onClick={() => saveRetention(opt)}
              disabled={saving}
            >
              {((calendar && calendar.retention_labels) || {})[opt] || `${opt} 个月`}
            </button>
          ))}
        </div>
        {message ? <div className="mem-note">{message}</div> : null}
        <div className="mem-archive">
          <Archive size={13} />
          <span>
            归档区 {archive.count} 份
            {archive.bytes ? ` · ${(archive.bytes / 1024).toFixed(1)} KB` : ''}
          </span>
        </div>
      </div>

      <div className="mem-section">
        <div className="mem-section__title">当天交互</div>
        {detailLoading ? (
          <div className="mem-note">
            <Loader2 size={13} className="mem-spin" /> 读取中…
          </div>
        ) : null}
        {!detailLoading && !selected ? (
          <div className="mem-note">点日历上有圆点的日期，看那天的交互记录。</div>
        ) : null}
        {!detailLoading && selected && detail && !(detail.interactions || []).length ? (
          <div className="mem-note">这一天没有记忆记录。</div>
        ) : null}
        {detail && (detail.interactions || []).length ? (
          <div className="mem-day">
            <div className="mem-day__date">
              {detail.date}
              <span className="mem-day__count">{detail.interactions.length} 次交互</span>
              <span className="mem-day__hint">列表可滚动</span>
            </div>
            {detail.interactions.map(it => (
              <div key={it.id} className="mem-day__session">
                <div className="mem-day__heading">
                  <span className="mem-day__time">{formatClock(it.started_at)}</span>
                  <span className="mem-day__id">{it.id}</span>
                  {it.tool_count ? (
                    <span className="mem-day__tools">{it.tool_count} 次工具调用</span>
                  ) : null}
                </div>
                {it.user_requests.map((q, qi) => (
                  <div key={`q${qi}`} className="mem-day__line">
                    <span className="mem-day__tag mem-day__tag--ask">提问</span>
                    <span className="mem-day__q">{q}</span>
                  </div>
                ))}
                {it.replies.map((r, ri) => (
                  <div key={`r${ri}`} className="mem-day__line">
                    <span className="mem-day__tag mem-day__tag--reply">回复</span>
                    <span className="mem-day__q">{r}</span>
                  </div>
                ))}
                {it.keys.length ? (
                  <div className="mem-day__keys">
                    {it.keys.slice(0, 8).map((k, ki) => (
                      <span key={ki} className="mem-key">{k}</span>
                    ))}
                  </div>
                ) : null}
              </div>
            ))}
          </div>
        ) : null}
      </div>
    </div>
  );
}
