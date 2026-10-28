import { useCallback, useEffect, useRef, useState } from 'react';

// 主机资源监控 + 省电档位 / 图形栈开关。
//
// 只对属主可见：非属主调用 /api/system/stats 会拿到 403，这里直接不渲染任何东西，
// 而不是渲染一个空壳让人以为"我的机器没数据"。权限判定在后端 auth.store.isOwner，
// 前端这层只是避免无谓轮询。
const API = '/api/system';
// 15 秒而不是 4 秒：这个面板是"看一眼主机状态"，不是监控大屏。
// 更密的轮询会让每个请求都带着一次 auth_sessions 写入，
// 在 auth.db 上制造不必要的写锁争用。
// 5 秒。之前为了绕开 auth.db 写锁争用先放到 15 秒，但根因（缺 busy_timeout +
// 未捕获异常）已经修掉，锁争用不再是问题，可见时该给回实时性。
// 页面隐藏时仍然完全停掉轮询，那才是真正需要省请求的场景。
const POLL_MS = 5000;

const fmtUptime = (sec) => {
  if (!Number.isFinite(sec)) return '-';
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (d > 0) return `${d} 天 ${h} 小时`;
  if (h > 0) return `${h} 小时 ${m} 分`;
  return `${m} 分`;
};

// 温度分级：<50 绿，<65 黄，再高红。分档只是给个直观印象，不代替真实读数。
const tempTone = (c) => (c == null ? '' : c < 50 ? 'ok' : c < 65 ? 'warn' : 'bad');

function Meter({ label, percent, detail }) {
  const pct = Number.isFinite(percent) ? Math.max(0, Math.min(100, percent)) : 0;
  const tone = pct < 50 ? 'ok' : pct < 80 ? 'warn' : 'bad';
  return (
    <div className="sys-meter">
      <div className="sys-meter-head">
        <span>{label}</span>
        <span className={`sys-meter-value sys-tone-${tone}`}>
          {Number.isFinite(percent) ? `${percent}%` : '-'}
        </span>
      </div>
      <div className="sys-meter-track">
        <div className={`sys-meter-fill sys-fill-${tone}`} style={{ width: `${pct}%` }} />
      </div>
      {detail ? <div className="sys-meter-detail">{detail}</div> : null}
    </div>
  );
}

function Row({ label, value, tone }) {
  return (
    <div className="sys-row">
      <span className="sys-row-label">{label}</span>
      <span className={`sys-row-value${tone ? ` sys-tone-${tone}` : ''}`}>{value}</span>
    </div>
  );
}

// systemd 的 active/inactive 对主人没有信息量，翻成面板里已经用过的说法。
// 非 active/inactive 的值原样透出，避免把未知状态说成"已停止"。
const unitStateText = (v) => (v === 'active' ? '运行中' : v === 'inactive' ? '已停止' : v || '未知');
const unitStateClass = (v) => (v === 'active' ? 'sys-unit-ok' : v === 'inactive' ? 'sys-unit-off' : '');

// 状态灯 + 单位名 + 状态词。systemd 单位名自带技术味，但停/启图形栈时
// 得靠它定位到底是哪一个挂了，所以保留英文名，只把状态词换成人话。
function UnitPill({ name, value }) {
  return (
    <span className={`sys-unit ${unitStateClass(value)}`}>
      <span className="sys-unit-name">{name}</span>
      <span className="sys-unit-state">{unitStateText(value)}</span>
    </span>
  );
}

export default function SystemSection() {
  const [stats, setStats] = useState(null);
  const [display, setDisplay] = useState(null);
  const [denied, setDenied] = useState(false);
  const [busy, setBusy] = useState('');
  const [notice, setNotice] = useState(null);
  const [online, setOnline] = useState(true);
  const timer = useRef(null);

  const load = useCallback(async () => {
    try {
      const res = await fetch(`${API}/stats`, { cache: 'no-store' });
      if (res.status === 403) {
        setDenied(true);
        return;
      }
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const data = await res.json();
      setStats(data);
      setOnline(true);
      setDenied(false);
    } catch (error) {
      setOnline(false);
    }
  }, []);

  const loadDisplay = useCallback(async () => {
    try {
      const res = await fetch(`${API}/display`, { cache: 'no-store' });
      if (res.status === 403) {
        setDenied(true);
        return;
      }
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      setDisplay(await res.json());
    } catch {
      /* 图形栈状态拿不到不影响资源面板，静默即可 */
    }
  }, []);

  useEffect(() => {
    load();
    loadDisplay();

    // 页面不可见时完全停掉轮询。设置页开着当背景标签是最常见的常态，
    // 没人在看的时候不该还在打接口 —— 每个请求都会触发后端一次
    // UPDATE auth_sessions，是 auth.db 写锁争用的来源之一。
    const stop = () => {
      if (timer.current) {
        clearInterval(timer.current);
        timer.current = null;
      }
    };
    const start = () => {
      stop();
      if (typeof document !== 'undefined' && document.hidden) return;
      timer.current = setInterval(load, POLL_MS);
    };
    const onVisible = () => {
      if (document.hidden) {
        stop();
      } else {
        load(); // 回到前台先立刻刷一次，避免显示一屏陈旧数据
        start();
      }
    };

    start();
    document.addEventListener('visibilitychange', onVisible);
    return () => {
      stop();
      document.removeEventListener('visibilitychange', onVisible);
    };
  }, [load, loadDisplay]);

  // 属主之外的人完全看不到这一栏。
  if (denied) return null;

  // busy 存 `${kind}:${query}`，不只是 kind。原来两个按钮共用一个 busy，
  // 结果是点任意一个，两个都显示"执行中…"——看不出到底点了哪个，
  // 在"启动/停止"这种互斥动作上尤其误导。现在只有被点的那颗转圈。
  const act = async (kind, query, payloadKey) => {
    const token = `${kind}:${query}`;
    setBusy(token);
    setNotice(null);
    try {
      const res = await fetch(`${API}/${kind}?${query}`, { method: 'POST' });
      const data = await res.json().catch(() => ({}));
      if (!res.ok || data.ok === false) {
        setNotice({ tone: 'bad', text: data.error || data.detail || `操作失败（HTTP ${res.status}）` });
      } else {
        setNotice({ tone: 'ok', text: '已执行。状态稍后自动刷新。' });
        await load();
        await loadDisplay();
      }
    } catch (error) {
      setNotice({ tone: 'bad', text: `请求失败：${error.message}` });
    } finally {
      setBusy('');
    }
  };

  // 同一类操作进行中时，全部按钮禁用，避免连点两次 systemctl。
  const busyIn = (kind) => busy.startsWith(`${kind}:`);
  const isBusy = (kind, query) => busy === `${kind}:${query}`;

  const graphOn = !!(display && display.units && display.units.wm === 'active' && display.units.xorg === 'active');
  const temps = (stats && stats.temps) || {};
  const power = (stats && stats.power) || {};
  // CPU 占用率靠两次采样做差值，第一次调用必然是 null，如实显示"预热中"
  const cpuWarmup = stats && stats.cpu.percent == null;

  return (
    <div className="settings-section">
      <h3>主机资源</h3>
      <p className="settings-hint">
        仅属主可见，每 {POLL_MS / 1000} 秒刷新一次。数据直接读自内核 /proc，不落盘。
      </p>

      {!online ? (
        <div className="sys-note sys-tone-bad">后端暂时不可达，暂时拿不到资源数据。</div>
      ) : null}

      {stats ? (
        <>
          <Meter
            label={`CPU（${stats.cpu.cores} 核）`}
            percent={stats.cpu.percent}
            detail={cpuWarmup ? '首次采样，CPU 占用率需要两次采样间隔才能算出来' : stats.cpu.model}
          />
          <Meter
            label="内存"
            percent={stats.mem.percent}
            detail={`已用 ${stats.mem.usedGB} GB / 共 ${stats.mem.totalGB} GB`}
          />
          {stats.disks.map((d) => (
            <Meter
              key={d.mount}
              label={`磁盘 ${d.mount}`}
              percent={d.percent}
              detail={
                d.error
                  ? d.error
                  : `已用 ${d.usedGB} GB / 共 ${d.totalGB} GB（可用 ${d.availGB} GB）`
              }
            />
          ))}

          <div className="sys-grid">
            <Row
              label="CPU 温度"
              value={stats.tempC == null ? '读不到' : `${stats.tempC} °C`}
              tone={tempTone(stats.tempC)}
            />
            <Row
              label="GPU 温度"
              value={temps.gpuC == null ? '读不到' : `${temps.gpuC} °C`}
              tone={tempTone(temps.gpuC)}
            />
            <Row
              label="SSD 温度"
              value={temps.nvmeC == null ? '读不到' : `${temps.nvmeC} °C`}
              tone={tempTone(temps.nvmeC)}
            />
            <Row label="负载（1/5/15 分）" value={(stats.load || []).join(' / ')} />
            <Row
              label="CPU 频率"
              value={
                power.curMHz == null
                  ? '读不到'
                  : `${power.curMHz} MHz` +
                    (power.maxMHz ? ` / 上限 ${power.maxMHz}` : '')
              }
            />
            <Row
              label="功耗档位"
              value={
                power.mode === 'quiet'
                  ? '夜间省电'
                  : power.mode === 'balanced'
                    ? '日常档'
                    : power.epp || '未知'
              }
              tone={power.mode === 'quiet' ? 'ok' : undefined}
            />
            <Row label="连续运行" value={fmtUptime(stats.uptimeSec)} />
            <Row label="主机" value={`${stats.host.hostname}（${stats.host.arch}）`} />
            <Row label="系统" value={stats.host.platform} />
          </div>
        </>
      ) : (
        <div className="sys-note">正在读取…</div>
      )}

      <h3 style={{ marginTop: 22 }}>省电档位</h3>
      <p className="settings-hint">
        这台小主机 7x24 常驻。省电档会把 CPU 功耗偏好压低，闲置时温度下降，主板风扇曲线随之放缓。
        对话响应会略微变慢。
      </p>
      {/* 档位是互斥二选一，用分段控件而不是两个独立按钮：
          当前档位是"选中态"而非"可重复执行的动作"，两个同款按钮会让人
          以为"已是夜间省电"还能再点一次。aria-pressed 让读屏也能知道当前档。 */}
      <div className="sys-seg" role="group" aria-label="功耗档位">
        {[
          { mode: 'quiet', label: '夜间省电', hint: 'CPU 功耗偏好压低，闲置时温度下降' },
          { mode: 'balanced', label: '日常档', hint: '恢复默认功耗偏好' },
        ].map((opt) => {
          const current = power.mode === opt.mode;
          return (
            <button
              key={opt.mode}
              className="btn"
              aria-pressed={current}
              disabled={busyIn('power')}
              onClick={() => act('power', `mode=${opt.mode}`, 'mode')}
            >
              {isBusy('power', `mode=${opt.mode}`) ? '执行中…' : opt.label}
            </button>
          );
        })}
      </div>
      <div className="sys-note">
        {power.mode === 'quiet'
          ? '当前是夜间省电档：风扇曲线放缓、整机更安静，代价是对话响应略慢。'
          : '当前是日常档。切成夜间省电后 CPU 功耗偏好会压低，闲置时温度和噪音都会降下来。'}
      </div>

      <h3 style={{ marginTop: 22 }}>图形栈</h3>
      <p className="settings-hint">
        停止图形栈可以让 GPU 退出 4K 输出驱动，夜间省电效果最明显。不影响 QQ / 微信桥、SSH
        等后台服务。需要图形界面时再启动即可。
      </p>
      <div className="sys-actions">
        <button
          className={`sys-btn sys-btn-danger${graphOn ? '' : ' sys-btn-current'}`}
          disabled={busyIn('display') || !graphOn}
          onClick={() => act('display', 'action=stop', 'action')}
        >
          {isBusy('display', 'action=stop') ? (
            <>
              <span className="sys-btn-spin" aria-hidden="true" />
              停止中…
            </>
          ) : graphOn ? (
            '停止图形栈'
          ) : (
            '已是停止状态'
          )}
        </button>
        <button
          className={`sys-btn sys-btn-primary${graphOn ? ' sys-btn-current' : ''}`}
          disabled={busyIn('display') || graphOn}
          onClick={() => act('display', 'action=start', 'action')}
        >
          {isBusy('display', 'action=start') ? (
            <>
              <span className="sys-btn-spin" aria-hidden="true" />
              启动中…
            </>
          ) : graphOn ? (
            '已在运行'
          ) : (
            '启动图形栈'
          )}
        </button>
      </div>
      {display ? (
        <div className="sys-status">
          <div className="sys-status-head">
            <span className={`sys-status-dot ${graphOn ? 'on' : 'off'}`} aria-hidden="true" />
            <span>{graphOn ? '图形栈运行中' : '图形栈已停止'}</span>
            <span className="sys-status-hint">
              {graphOn ? '本机 HDMI 有输出' : '本机 HDMI 无输出'}
            </span>
          </div>
          <div className="sys-units">
            <UnitPill name="Xorg" value={display.units.xorg} />
            <UnitPill name="XFCE" value={display.units.wm} />
            <UnitPill name="Sunshine" value={display.units.sunshine} />
          </div>
        </div>
      ) : null}

      {notice ? (
        <div className={`sys-note sys-tone-${notice.tone}`}>{notice.text}</div>
      ) : null}
    </div>
  );
}