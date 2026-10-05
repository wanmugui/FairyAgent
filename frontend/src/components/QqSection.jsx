import { useCallback, useEffect, useRef, useState } from 'react';
import { Copy, Loader2, MessageSquare, RefreshCcw, Unlink } from 'lucide-react';

/**
 * QQ 通道绑定。
 *
 * 和微信不一样：微信能靠"扫码"证明手机在谁手上，QQ 的 appId/appSecret 只证明
 * "这是哪只机器人"，证明不了发消息的人是谁。所以 QQ 按**会话**绑定——面板给一个
 * 一次性码，本人用它自己的 QQ 给机器人发 `/bind <码>`，那一条会话就绑到了生成码
 * 的那个账号上。
 *
 * 因此这里看到的是"哪些会话绑到了谁"，不是"连上了没有"：同一台机器上两个人可以
 * 各绑各的 QQ。没绑定的会话，机器人会提示并拒绝，不会把内容交给模型。
 */

const CODE_TTL_MINUTES = 10;
const CHANNEL = 'qq';

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

function formatTime(value) {
  if (!value) return '—';
  const date = typeof value === 'number'
    ? new Date(value < 1e12 ? value * 1000 : value)
    : new Date(value);
  if (Number.isNaN(date.getTime())) return String(value);
  return date.toLocaleString('zh-CN', { hour12: false });
}

// Openids are long and meaningless to a person; the tail is what disambiguates
// two of them, and the full value stays available on hover.
function shortId(value) {
  const text = String(value || '');
  return text.length <= 18 ? text : `${text.slice(0, 6)}…${text.slice(-6)}`;
}

export default function QqSection({ onStatus }) {
  const [bindings, setBindings] = useState([]);
  const [code, setCode] = useState('');
  const [expiresAt, setExpiresAt] = useState(0);
  const [busy, setBusy] = useState('');
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [unbinding, setUnbinding] = useState('');
  const [loading, setLoading] = useState(true);
  const mountedRef = useRef(true);
  const notifyRef = useRef(onStatus);
  notifyRef.current = onStatus;

  const publish = useCallback((list) => {
    const bound = (list || []).filter((item) => item.channel === CHANNEL);
    notifyRef.current?.(bound.length > 0 ? `${bound.length} 个已绑定` : '未绑定');
  }, []);

  const refresh = useCallback(async () => {
    try {
      const data = await callApi('/api/bind/list');
      if (!mountedRef.current) return;
      const list = Array.isArray(data.bindings) ? data.bindings : [];
      setBindings(list.filter((item) => item.channel === CHANNEL));
      publish(list);
      setError('');
    } catch (err) {
      if (mountedRef.current) {
        setError(err.message || '读取 QQ 绑定失败');
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

  async function createCode() {
    setBusy('code');
    setNotice('');
    try {
      const data = await callApi('/api/bind/code', 'POST');
      setCode(data.code || '');
      setExpiresAt(data.expiresAt || 0);
      setError('');
    } catch (err) {
      setError(err.message || '生成绑定码失败');
    } finally {
      setBusy('');
    }
  }

  async function copyCode() {
    try {
      await navigator.clipboard.writeText(code);
      setNotice('绑定码已复制');
    } catch {
      setNotice('复制失败，请手动选中上面的绑定码');
    }
  }

  async function unbind(conversationId) {
    const id = String(conversationId);
    setUnbinding(id);
    try {
      await callApi(`/api/bind/${CHANNEL}/${encodeURIComponent(id)}`, 'DELETE');
      await refresh();
      setNotice('已解绑');
    } catch (err) {
      setError(err.message || '解绑失败');
    } finally {
      setUnbinding('');
    }
  }

  const remainingMs = expiresAt ? expiresAt - Date.now() : 0;
  const countdown = code && remainingMs > 0
    ? `约 ${Math.ceil(remainingMs / 60000)} 分钟后失效`
    : '';

  if (loading) {
    return (
      <div className="fairy-empty">
        <Loader2 size={16} className="fairy-btn__spin" />
        <span>正在读取 QQ 绑定…</span>
      </div>
    );
  }

  return (
    <>
      {error ? (
        <div className="fairy-banner fairy-banner--error">
          <span>{error}</span>
          <button type="button" className="fairy-btn fairy-btn--ghost" onClick={refresh}>
            <RefreshCcw size={14} />
            <span>重试</span>
          </button>
        </div>
      ) : null}
      {notice ? <div className="fairy-banner fairy-banner--ok">{notice}</div> : null}

      <div className="fairy-row">
        <div className="fairy-row__label">
          <span className="fairy-row__title">QQ 机器人</span>
          <span className="fairy-row__hint">
            按会话绑定：谁绑定了，他的消息才算那个账号，并写进他当天的会话；
            没绑定的会话机器人只回一句提示，不处理内容。
          </span>
        </div>
        <div className="fairy-row__control">
          <span className="fairy-badge">
            {bindings.length > 0 ? `${bindings.length} 个会话已绑定` : '暂未绑定'}
          </span>
        </div>
      </div>

      <div className="fairy-row">
        <div className="fairy-row__label">
          <span className="fairy-row__title">绑定码</span>
          <span className="fairy-row__hint">
            在 QQ 里给机器人发：/bind 加上这个码（{CODE_TTL_MINUTES} 分钟内有效，只显示一次）
          </span>
        </div>
        <div className="fairy-row__control">
          <button type="button" className="fairy-btn" onClick={createCode} disabled={busy === 'code'}>
            {busy === 'code' ? '生成中…' : code ? '重新生成' : '生成绑定码'}
          </button>
          {code ? (
            <>
              <code className="fairy-code" style={{ fontSize: 26, letterSpacing: 3 }}>{code}</code>
              <button type="button" className="fairy-btn fairy-btn--ghost" onClick={copyCode}>
                <Copy size={14} />
                <span>复制</span>
              </button>
              {countdown ? <span className="fairy-row__hint">{countdown}</span> : null}
            </>
          ) : null}
        </div>
      </div>

      <div className="fairy-row">
        <div className="fairy-row__label">
          <span className="fairy-row__title">已绑定的 QQ</span>
          <span className="fairy-row__hint">私聊显示 openid，群聊显示 group_openid</span>
        </div>
        <div className="fairy-row__control">
          {bindings.length > 0 ? (
            <div className="fairy-form">
              {bindings.map((item) => {
                const id = String(item.conversationId);
                return (
                  <div className="fairy-row" key={id}>
                    <MessageSquare size={14} />
                    <code title={id}>{shortId(id)}</code>
                    <span className="fairy-row__hint">{formatTime(item.boundAt)} 绑定</span>
                    <button
                      type="button"
                      className="fairy-btn fairy-btn--ghost"
                      disabled={unbinding === id}
                      onClick={() => unbind(id)}
                    >
                      <Unlink size={14} />
                      <span>{unbinding === id ? '解绑中…' : '解绑'}</span>
                    </button>
                  </div>
                );
              })}
            </div>
          ) : (
            <span className="fairy-row__hint">还没有人绑定。生成绑定码后，在 QQ 里发给机器人即可。</span>
          )}
        </div>
      </div>
    </>
  );
}
