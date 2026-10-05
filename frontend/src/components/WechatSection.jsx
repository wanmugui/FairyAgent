import { useCallback, useEffect, useRef, useState } from 'react';
import { Link2, Loader2, QrCode, Unlink } from 'lucide-react';

const POLL_MS = 1800;
const LIVE_STAGES = new Set(['starting', 'waiting', 'scanned']);

const STAGE_TEXT = {
  starting: '正在获取二维码…',
  waiting: '用微信扫码，约 5 分钟有效',
  scanned: '已扫码，请在手机上确认',
  confirmed: '已接入',
  timeout: '二维码已过期，请重新生成',
};

async function callApi(path, method = 'GET') {
  const res = await fetch(path, {
    method,
    headers: { 'Content-Type': 'application/json; charset=utf-8' },
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

// linkedAt may arrive as ISO string or epoch seconds/millis; keep the raw value readable if it is neither.
function formatTime(value) {
  if (!value) return '—';
  const date = typeof value === 'number'
    ? new Date(value < 1e12 ? value * 1000 : value)
    : new Date(value);
  if (Number.isNaN(date.getTime())) return String(value);
  return date.toLocaleString('zh-CN', { hour12: false });
}

// A flow whose expiresAt already passed counts as terminal: the backend only
// marks it timeout when the login process reports back, and a dead QR with no
// way to regenerate is a dead end.
function isLiveFlow(flow) {
  if (!flow || !LIVE_STAGES.has(flow.stage)) return false;
  return !flow.expiresAt || flow.expiresAt > Date.now();
}

function isStaleFlow(flow) {
  return Boolean(flow) && LIVE_STAGES.has(flow.stage) && !isLiveFlow(flow);
}

export default function WechatSection({ onStatus }) {
  const [status, setStatus] = useState(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState('');
  const [confirmUnlink, setConfirmUnlink] = useState(false);
  const timerRef = useRef(null);
  const mountedRef = useRef(true);
  // Keep the parent callback out of the polling effect so summary updates never restart polling.
  const notifyRef = useRef(onStatus);
  notifyRef.current = onStatus;

  const applyStatus = useCallback((next) => {
    if (!mountedRef.current) return;
    setStatus(next);
    notifyRef.current?.(next);
  }, []);

  const refresh = useCallback(async () => {
    try {
      const data = await callApi('/api/wechat/status');
      if (!mountedRef.current) return { ok: true, data: null };
      setError('');
      applyStatus(data);
      return { ok: true, data };
    } catch (err) {
      if (mountedRef.current) setError(err.message || '读取微信接入状态失败');
      return { ok: false, data: null };
    }
  }, [applyStatus]);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      if (timerRef.current) clearTimeout(timerRef.current);
    };
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh]);

  const flow = status?.flow || null;
  const linked = Boolean(status?.linked);
  const polling = isLiveFlow(flow);
  const stale = isStaleFlow(flow);

  // Poll only while a scan can still make progress; every other stage is terminal until the user acts.
  useEffect(() => {
    if (!polling) {
      if (timerRef.current) clearTimeout(timerRef.current);
      return undefined;
    }
    timerRef.current = setTimeout(async () => {
      const result = await refresh();
      const stage = result.data?.flow?.stage;
      if (result.ok && !isLiveFlow({ stage })) {
        setBusy((current) => (current === 'start' ? '' : current));
      }
    }, POLL_MS);
    return () => {
      if (timerRef.current) clearTimeout(timerRef.current);
    };
  }, [polling, flow?.stage, flow?.id, refresh]);

  const handleStart = async () => {
    setBusy('start');
    setError('');
    try {
      await callApi('/api/wechat/link/start', 'POST');
      await refresh();
    } catch (err) {
      setBusy('');
      setError(err.message || '生成二维码失败');
    }
  };

  const handleCancel = async () => {
    setBusy('cancel');
    setError('');
    try {
      await callApi('/api/wechat/link/cancel', 'POST');
      applyStatus({ ...(status || {}), flow: null });
      await refresh();
    } catch (err) {
      setError(err.message || '取消失败');
    } finally {
      if (mountedRef.current) setBusy('');
    }
  };

  const handleBridge = async () => {
    setBusy('bridge');
    setError('');
    try {
      await callApi('/api/wechat/bridge', 'POST');
      await refresh();
    } catch (err) {
      setError(err.message || '启动桥接失败');
    } finally {
      if (mountedRef.current) setBusy('');
    }
  };

  const handleUnlink = async () => {
    setBusy('unlink');
    setError('');
    try {
      await callApi('/api/wechat/account', 'DELETE');
      setConfirmUnlink(false);
      applyStatus({ ...(status || {}), linked: false, account: null });
      await refresh();
    } catch (err) {
      setError(err.message || '断开接入失败');
    } finally {
      if (mountedRef.current) setBusy('');
    }
  };

  const account = status?.account || null;
  const bridge = status?.bridge || { running: false };
  const stage = flow?.stage || '';

  return (
    <div className="fairy-wechat" data-testid="wechat-section">
      {error ? (
        <div
          data-testid="wechat-error"
          style={{ marginBottom: 10, padding: '8px 10px', borderRadius: 10, background: 'rgba(248,113,113,.12)', color: '#fca5a5', fontSize: 12 }}
        >
          {error}
        </div>
      ) : null}

      {linked ? (
        <div data-testid="wechat-linked">
          <div className="fairy-row">
            <div className="fairy-row__label">
              <span className="fairy-row__title">接入状态</span>
              <span className="fairy-row__hint">官方 iLink 通道</span>
            </div>
            <div className="fairy-row__control">
              <span className="fairy-badge" data-testid="wechat-linked-badge">已接入</span>
            </div>
          </div>
          <div className="fairy-row">
            <div className="fairy-row__label">
              <span className="fairy-row__title">Bot ID</span>
            </div>
            <div className="fairy-row__control">
              <span className="fairy-code" data-testid="wechat-bot-id">{account?.botId || '—'}</span>
            </div>
          </div>
          <div className="fairy-row">
            <div className="fairy-row__label">
              <span className="fairy-row__title">接入的微信号</span>
            </div>
            <div className="fairy-row__control">
              <span className="fairy-code" data-testid="wechat-user-id">{account?.wechatUserId || '—'}</span>
            </div>
          </div>
          <div className="fairy-row">
            <div className="fairy-row__label">
              <span className="fairy-row__title">接入时间</span>
            </div>
            <div className="fairy-row__control">
              <span data-testid="wechat-linked-at">{formatTime(account?.linkedAt)}</span>
            </div>
          </div>
          <div className="fairy-row">
            <div className="fairy-row__label">
              <span className="fairy-row__title">消息桥接</span>
              <span className="fairy-row__hint">负责把微信消息转给 Fairy</span>
            </div>
            <div className="fairy-row__control">
              <span
                data-testid="wechat-bridge-state"
                style={{
                  display: 'inline-flex',
                  alignItems: 'center',
                  gap: 6,
                  color: bridge.running ? '#86efac' : '#9ca3af',
                }}
              >
                <span
                  style={{
                    width: 8,
                    height: 8,
                    borderRadius: '50%',
                    background: bridge.running ? '#4ade80' : '#6b7280',
                  }}
                />
                {bridge.running ? '运行中' : '未运行'}
                {bridge.pid ? `（PID ${bridge.pid}）` : ''}
              </span>
            </div>
          </div>
          <div className="fairy-form__actions">
            {!bridge.running ? (
              <button
                type="button"
                className="fairy-btn fairy-btn--primary"
                onClick={handleBridge}
                disabled={busy === 'bridge'}
                data-testid="wechat-bridge-start"
              >
                {busy === 'bridge' ? <Loader2 size={13} className="fairy-btn__spin" /> : <Link2 size={13} />}
                启动桥接
              </button>
            ) : null}
            {confirmUnlink ? (
              <>
                <button
                  type="button"
                  className="fairy-btn fairy-btn--ghost is-danger"
                  onClick={handleUnlink}
                  disabled={busy === 'unlink'}
                  data-testid="wechat-unlink-confirm"
                >
                  {busy === 'unlink' ? <Loader2 size={13} className="fairy-btn__spin" /> : <Unlink size={13} />}
                  确认断开接入
                </button>
                <button
                  type="button"
                  className="fairy-btn fairy-btn--ghost"
                  onClick={() => setConfirmUnlink(false)}
                  data-testid="wechat-unlink-cancel"
                >
                  再想想
                </button>
              </>
            ) : (
              <button
                type="button"
                className="fairy-btn fairy-btn--ghost is-danger"
                onClick={() => setConfirmUnlink(true)}
                data-testid="wechat-unlink"
              >
                <Unlink size={13} />
                断开接入
              </button>
            )}
          </div>
        </div>
      ) : (
        <div data-testid="wechat-unlinked">
          {flow && stage === 'error' ? (
            <div
              className=""
              data-testid="wechat-stage-error"
              style={{ marginBottom: 10, padding: '8px 10px', borderRadius: 10, background: 'rgba(248,113,113,.12)', color: '#fca5a5', fontSize: 12 }}
            >
              {flow.error || '扫码流程出错'}
            </div>
          ) : null}

          {flow && stage === 'waiting' && flow.qr && !stale ? (
            <div
              style={{ display: 'flex', gap: 16, alignItems: 'flex-start', margin: '4px 0 12px' }}
              data-testid="wechat-qr-wrap"
            >
              <img
                src={flow.qr}
                alt="微信登录二维码"
                width={200}
                height={200}
                data-testid="wechat-qr"
                style={{
                  width: 200,
                  height: 200,
                  borderRadius: 12,
                  border: '1px solid rgba(255,255,255,.14)',
                  background: '#fff',
                  padding: 8,
                }}
              />
              <div style={{ display: 'flex', flexDirection: 'column', gap: 8, paddingTop: 4 }}>
                <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, color: '#e5e7eb' }}>
                  <QrCode size={14} />
                  {STAGE_TEXT.waiting}
                </span>
                <span className="fairy-row__hint">扫码后在手机上确认，确认成功后这里会自动变成已接入。</span>
                <button
                  type="button"
                  className="fairy-btn fairy-btn--ghost"
                  onClick={handleCancel}
                  disabled={busy === 'cancel'}
                  data-testid="wechat-cancel"
                >
                  {busy === 'cancel' ? <Loader2 size={13} className="fairy-btn__spin" /> : null}
                  取消
                </button>
              </div>
            </div>
          ) : (
            <div
              style={{ display: 'flex', alignItems: 'center', gap: 8, color: '#e5e7eb', margin: '4px 0 12px' }}
              data-testid="wechat-stage"
            >
              {stage === 'starting' || busy === 'start' ? <Loader2 size={14} className="fairy-btn__spin" /> : null}
              <span data-testid="wechat-stage-text">
                {stage === 'starting' ? STAGE_TEXT.starting
                  : stage === 'scanned' ? STAGE_TEXT.scanned
                    : (stage === 'timeout' || stale) ? STAGE_TEXT.timeout
                      : stage === 'error' ? '扫码流程出错'
                        : busy === 'start' ? '正在生成二维码…'
                          : '当前账号（' + (status?.label || '—') + '）还没有接入微信。'}
              </span>
            </div>
          )}

          {flow && stage === 'scanned' ? (
            <div className="fairy-form__actions">
              <button
                type="button"
                className="fairy-btn fairy-btn--ghost"
                onClick={handleCancel}
                disabled={busy === 'cancel'}
                data-testid="wechat-cancel-scanned"
              >
                取消
              </button>
            </div>
          ) : null}

          <div className="fairy-form__actions">
            {polling || stage === 'scanned' ? null : (
              <button
                type="button"
                className="fairy-btn fairy-btn--primary"
                onClick={handleStart}
                disabled={busy === 'start'}
                data-testid="wechat-start"
              >
                {busy === 'start' ? <Loader2 size={13} className="fairy-btn__spin" /> : <QrCode size={13} />}
                {stage === 'timeout' || stale ? '重新生成二维码' : '生成二维码'}
              </button>
            )}
          </div>
        </div>
      )}

      <div style={{ marginTop: 14, paddingTop: 12, borderTop: '1px solid rgba(255,255,255,.07)' }}>
        <p className="fairy-row__hint" style={{ margin: 0 }}>
          官方 iLink 通道只支持私聊，拉不进群；扫码用的是你自己的微信，接入后只有这个微信号能跟 bot 对话。
        </p>
        {status?.logPath ? (
          <p className="fairy-row__hint" style={{ margin: '6px 0 0', opacity: .75 }} data-testid="wechat-log-path">
            排障日志：<span className="fairy-code">{status.logPath}</span>
          </p>
        ) : null}
      </div>
    </div>
  );
}
