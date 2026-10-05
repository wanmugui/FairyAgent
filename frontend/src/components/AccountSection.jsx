import { useCallback, useEffect, useRef, useState } from 'react';
import { Copy, KeyRound, Loader2, LogOut, RefreshCcw, ShieldCheck, Trash2, UserPlus } from 'lucide-react';

// Self-contained: SettingsMenu only passes onIdentity so the nav summary can show
// the username without SettingsMenu having to fetch /auth/me a second time.
export default function AccountSection({ onIdentity }) {
  const [me, setMe] = useState(null);
  const [devices, setDevices] = useState([]);
  const [bindings, setBindings] = useState([]);
  const [bindCode, setBindCode] = useState('');
  const [bindExpiresAt, setBindExpiresAt] = useState(0);
  const [bindBusy, setBindBusy] = useState(false);
  const [unbindingId, setUnbindingId] = useState('');
  const [bindMsg, setBindMsg] = useState(null);
  const [currentIp, setCurrentIp] = useState('');
  const [group, setGroup] = useState(null);
  const [members, setMembers] = useState([]);
  const [inviteTtlMs, setInviteTtlMs] = useState(0);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState('');

  const [pw, setPw] = useState({ current: '', next: '', confirm: '' });
  const [pwBusy, setPwBusy] = useState(false);
  const [pwMsg, setPwMsg] = useState(null);

  const [busyDevice, setBusyDevice] = useState(0);
  const [invite, setInvite] = useState(null);
  const [inviteBusy, setInviteBusy] = useState(false);
  const [copied, setCopied] = useState(false);
  const [joinCode, setJoinCode] = useState('');
  const [joinBusy, setJoinBusy] = useState(false);

  // Relationship notes ("我爸", "姐姐"). noteEdits holds the in-progress value
  // per member so a half-typed note is never lost when the list re-renders,
  // and noteBusy tracks which member's save button is in flight.
  const [noteEdits, setNoteEdits] = useState({});
  const [noteBusy, setNoteBusy] = useState(0);
  const [noteMsg, setNoteMsg] = useState(null);

  // The household row expands to reveal the group prompt and the member list;
  // both only matter while editing relationships, so they stay collapsed by
  // default instead of occupying the whole settings page.
  const [groupOpen, setGroupOpen] = useState(false);
  const [promptEdit, setPromptEdit] = useState(null);
  const [promptBusy, setPromptBusy] = useState(false);
  const [promptMsg, setPromptMsg] = useState(null);
  const [joinMsg, setJoinMsg] = useState(null);
  const [logoutBusy, setLogoutBusy] = useState(false);

  // Held in a ref so an inline onIdentity prop from the parent cannot retrigger
  // the load effect on every render.
  const identityRef = useRef(onIdentity);
  useEffect(() => { identityRef.current = onIdentity; }, [onIdentity]);
  const publishIdentity = useCallback((identity) => {
    identityRef.current?.(identity);
  }, []);

  // Attach the parsed body to the thrown error so callers can branch on `code`
  // instead of matching on status codes that overlap between endpoints.
  const request = useCallback(async (path, options = {}) => {
    const response = await fetch(path, {
      credentials: 'same-origin',
      ...options,
      headers: options.body ? { 'Content-Type': 'application/json', ...(options.headers || {}) } : options.headers,
    });
    const payload = await response.json().catch(() => ({}));
    if (!response.ok) {
      const error = new Error(payload.error || `请求失败（${response.status}）`);
      error.code = payload.code;
      error.status = response.status;
      throw error;
    }
    return payload;
  }, []);

  const loadGroup = useCallback(async () => {
    try {
      const payload = await request('/auth/group');
      setGroup(payload.group || null);
      setMembers(Array.isArray(payload.members) ? payload.members : []);
      if (payload.inviteTtlMs) setInviteTtlMs(payload.inviteTtlMs);
    } catch {
      setGroup(null);
      setMembers([]);
    }
  }, [request]);

  const loadBindings = useCallback(async () => {
    try {
      const payload = await request('/api/bind/list');
      setBindings(Array.isArray(payload.bindings) ? payload.bindings : []);
    } catch {
      // A failure here must not hide the rest of the account panel; the list
      // simply stays empty and the user can retry by reopening the section.
      setBindings([]);
    }
  }, [request]);

  const loadDevices = useCallback(async () => {
    try {
      const payload = await request('/auth/devices');
      setDevices(Array.isArray(payload.devices) ? payload.devices : []);
      setCurrentIp(payload.currentIp || '');
    } catch {
      setDevices([]);
    }
  }, [request]);

  useEffect(() => {
    let alive = true;
    (async () => {
      setLoading(true);
      try {
        const identity = await request('/auth/me');
        if (!alive) return;
        setMe(identity);
        publishIdentity(identity);
        if (identity.authEnabled && identity.authenticated) {
          await Promise.all([loadDevices(), loadGroup(), loadBindings()]);
        }
      } catch (error) {
        if (alive) setLoadError(error.message || '无法读取账号信息');
      } finally {
        if (alive) setLoading(false);
      }
    })();
    return () => { alive = false; };
  }, [request, loadDevices, loadGroup, loadBindings, publishIdentity]);

  async function changePassword(event) {
    event.preventDefault();
    setPwMsg(null);
    if (pw.next !== pw.confirm) {
      setPwMsg({ kind: 'error', text: '两次输入的新密码不一致' });
      return;
    }
    setPwBusy(true);
    try {
      await request('/auth/password', {
        method: 'POST',
        body: JSON.stringify({ current: pw.current, next: pw.next }),
      });
      setPw({ current: '', next: '', confirm: '' });
      setPwMsg({ kind: 'ok', text: '密码已更新' });
    } catch (error) {
      if (error.code === 'bad_current_password') {
        setPwMsg({ kind: 'error', text: '当前密码不正确' });
      } else if (error.code === 'weak_password') {
        setPwMsg({ kind: 'error', text: error.message || '新密码太弱' });
      } else {
        setPwMsg({ kind: 'error', text: error.message || '修改失败' });
      }
    } finally {
      setPwBusy(false);
    }
  }

  async function revokeDevice(id) {
    setBusyDevice(id);
    try {
      await request(`/auth/devices/${id}`, { method: 'DELETE' });
      await loadDevices();
    } catch (error) {
      setLoadError(error.message || '撤销失败');
    } finally {
      setBusyDevice(0);
    }
  }

  async function createInvite() {
    setInviteBusy(true);
    try {
      const payload = await request('/auth/group/invites', { method: 'POST' });
      // The plaintext code exists only in this response, so it must be shown now.
      setInvite({ code: payload.code, expiresAt: payload.expiresAt, ttlMs: payload.ttlMs });
      setCopied(false);
      await loadGroup();
    } catch (error) {
      setLoadError(error.message || '生成邀请码失败');
    } finally {
      setInviteBusy(false);
    }
  }

  async function copyInvite() {
    if (!invite?.code) return;
    try {
      await navigator.clipboard.writeText(invite.code);
      setCopied(true);
    } catch {
      setCopied(false);
    }
  }

  const refreshMe = useCallback(async () => {
    try {
      const identity = await request('/auth/me');
      setMe(identity);
      publishIdentity(identity);
    } catch {
      /* keep the previous identity; the next explicit load will retry */
    }
  }, [request, publishIdentity]);

  async function joinGroup(event) {
    event.preventDefault();
    setJoinMsg(null);
    if (!joinCode.trim()) {
      setJoinMsg({ kind: 'error', text: '请输入邀请码' });
      return;
    }
    setJoinBusy(true);
    try {
      const payload = await request('/auth/group/join', {
        method: 'POST',
        body: JSON.stringify({ code: joinCode.trim() }),
      });
      setJoinCode('');
      setJoinMsg({ kind: 'ok', text: `已加入「${payload?.group?.name || '家庭组'}」` });
      await Promise.all([loadGroup(), refreshMe()]);
    } catch (error) {
      if (error.code === 'bad_invite') {
        setJoinMsg({ kind: 'error', text: '邀请码无效或已过期' });
      } else if (error.code === 'conflict') {
        setJoinMsg({ kind: 'error', text: '你已经在该家庭组中' });
      } else {
        setJoinMsg({ kind: 'error', text: error.message || '加入失败' });
      }
    } finally {
      setJoinBusy(false);
    }
  }

  async function createBindCode() {
    setBindBusy(true);
    setBindMsg(null);
    try {
      const payload = await request('/api/bind/code', { method: 'POST' });
      setBindCode(payload.code || '');
      setBindExpiresAt(payload.expiresAt || 0);
      setBindMsg({ kind: 'ok', text: '绑定码已生成，只显示这一次' });
    } catch (error) {
      setBindCode('');
      setBindMsg({ kind: 'error', text: error.message || '生成绑定码失败' });
    } finally {
      setBindBusy(false);
    }
  }

  async function copyBindCode() {
    if (!bindCode) return;
    try {
      await navigator.clipboard.writeText(bindCode);
      setBindMsg({ kind: 'ok', text: '绑定码已复制' });
    } catch {
      // Clipboard access is refused in some browsers over plain http, so fall
      // back to selecting the text rather than leaving the user stuck.
      setBindMsg({ kind: 'error', text: '复制失败，请手动选中上面的绑定码' });
    }
  }

  async function saveGroupPrompt() {
    const raw = promptEdit ?? group?.prompt ?? '';
    if (raw.trim() === (group?.prompt ?? '').trim()) return; // nothing to do
    setPromptBusy(true);
    setPromptMsg(null);
    try {
      const payload = await request('/auth/group/prompt', {
        method: 'PATCH',
        body: JSON.stringify({ prompt: raw }),
      });
      setGroup((prev) => (prev ? { ...prev, prompt: payload.prompt } : prev));
      setPromptEdit(null);
      setPromptMsg({ kind: 'ok', text: '已保存家庭组关系说明' });
    } catch (error) {
      setPromptMsg({ kind: 'err', text: error.message || '保存家庭组关系说明失败' });
    } finally {
      setPromptBusy(false);
    }
  }

  // Relationship notes steer how the agent addresses people, so only the
  // machine owner may set them (the server enforces the same rule).
  const canEditNote = members.some((m) => m.isSelf && m.owner);

  // Member rows live inside the household's expanded body, so they are built
  // here and rendered there rather than sitting as a sibling block.
  const membersBlock = members.length === 0 ? (
    <div className="fairy-empty">
      <UserPlus size={16} />
      <span>还没有家庭组，先生成一个邀请码吧</span>
    </div>
  ) : (
    <ul className="fairy-user-list">
      {members.map((member) => {
        const noteValue = noteEdits[member.userId] ?? member.note ?? '';
        const noteDirty = noteValue.trim() !== (member.note ?? '').trim();
        return (
          <li key={member.userId} className="fairy-user-list__item">
            <div className="fairy-user-list__main">
              <span className="fairy-user-list__name">
                {member.username}
                {member.isSelf ? '（我）' : ''}
              </span>
              <span className="fairy-user-list__meta">
                {member.role === 'admin' ? '管理员' : '成员'} · 加入于 {formatTime(member.joinedAt)}
              </span>
            </div>
            <div className="fairy-user-list__note">
              <input
                type="text"
                className="fairy-input"
                value={noteValue}
                placeholder="关系备注，例如：我爸、姐姐"
                maxLength={40}
                disabled={!canEditNote}
                onChange={(e) =>
                  setNoteEdits((prev) => ({ ...prev, [member.userId]: e.target.value }))
                }
                onKeyDown={(e) => {
                  if (e.key === 'Enter') saveMemberNote(member);
                }}
              />
              <button
                type="button"
                className="fairy-btn fairy-btn--ghost"
                disabled={!canEditNote || !noteDirty || noteBusy === member.userId}
                onClick={() => saveMemberNote(member)}
              >
                {noteBusy === member.userId ? '保存中…' : '保存备注'}
              </button>
            </div>
          </li>
        );
      })}
    </ul>
  );

  async function saveMemberNote(member) {
    const raw = noteEdits[member.userId] ?? member.note ?? '';
    if (raw.trim() === (member.note ?? '').trim()) return; // nothing to do
    setNoteBusy(member.userId);
    setNoteMsg(null);
    try {
      const payload = await request('/auth/group/member-note', {
        method: 'PATCH',
        body: JSON.stringify({ userId: member.userId, note: raw }),
      });
      // Re-read from the server response rather than trusting local state, so
      // what the page shows is exactly what was trimmed and stored.
      setMembers((prev) => prev.map((m) => (m.userId === member.userId ? { ...m, note: payload.note } : m)));
      setNoteEdits((prev) => {
        const next = { ...prev };
        delete next[member.userId];
        return next;
      });
      setNoteMsg({ kind: 'ok', text: `已保存「${member.username}」的关系备注` });
    } catch (error) {
      setNoteMsg({ kind: 'err', text: error.message || '保存备注失败' });
    } finally {
      setNoteBusy(0);
    }
  }

  async function unbindChannel(channel, conversationId) {
    if (!window.confirm('解绑后，这个通道将不能再指挥 Fairy。确定解绑？')) return;
    setUnbindingId(`${channel}:${conversationId}`);
    setBindMsg(null);
    try {
      await request(`/api/bind/${encodeURIComponent(channel)}/${encodeURIComponent(conversationId)}`, {
        method: 'DELETE',
      });
      await loadBindings();
      setBindMsg({ kind: 'ok', text: '已解绑' });
    } catch (error) {
      setBindMsg({ kind: 'error', text: error.message || '解绑失败' });
    } finally {
      setUnbindingId('');
    }
  }

  async function logout() {
    setLogoutBusy(true);
    try {
      await request('/auth/logout', { method: 'POST' });
    } catch {
      // Even if the server call fails, sending the user to the login page is
      // still the correct outcome: the cookie may already be gone.
    }
    window.location.replace('/login.html');
  }

  // Desktop single-user mode: no account exists to manage, so render only the note.
  if (me && !me.authEnabled) {
    return <p className="fairy-field__hint">本机未启用登录：这是单用户桌面模式</p>;
  }

  if (loading) {
    return (
      <div className="fairy-empty">
        <Loader2 size={16} className="fairy-btn__spin" />
        <span>正在读取账号信息…</span>
      </div>
    );
  }

  if (loadError && !me) {
    return (
      <div className="fairy-banner fairy-banner--error">
        <span>读取账号信息失败：{loadError}</span>
        <button type="button" className="fairy-btn fairy-btn--ghost" onClick={() => window.location.reload()}>
          <RefreshCcw size={14} />
          <span>重试</span>
        </button>
      </div>
    );
  }

  if (!me || !me.authenticated) {
    return (
      <div className="fairy-banner fairy-banner--error">
        <span>尚未登录，无法管理账号。</span>
        <button type="button" className="fairy-btn fairy-btn--ghost" onClick={() => window.location.replace('/login.html')}>
          <span>去登录</span>
        </button>
      </div>
    );
  }

  const roleLabel = me.role === 'admin' ? '管理员' : me.role === 'member' ? '成员' : me.role || '未知';

  // A QQ or WeChat conversation only speaks for the account bound to it, so the
  // code is the only way in; the list is the record of who has one.
  const CHANNEL_LABEL = { qq: 'QQ', wechat: '微信' };
  const shortConversation = (id) => (id.length > 18 ? `${id.slice(0, 10)}…${id.slice(-6)}` : id);
  const bindCodeCountdown = bindExpiresAt
    ? `10 分钟内有效，只显示一次（${new Date(bindExpiresAt).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })} 失效）`
    : '10 分钟内有效，只显示一次';

  const channelBindingPanel = (
    <>
      <div className="fairy-subsection-title">通道绑定</div>
      <div className="fairy-row">
        <div className="fairy-row__label">
          <span className="fairy-row__title">已绑定通道</span>
          <span className="fairy-row__hint">
            绑定后该通道才算「你」，并且和网页共用当天的同一个会话；微信在扫码成功时自动绑定。
          </span>
        </div>
        <div className="fairy-row__control">
          {bindings.length > 0 ? (
            <div className="fairy-form">
              {bindings.map((b) => {
                const id = `${b.channel}:${b.conversationId}`;
                const label = CHANNEL_LABEL[b.channel] || b.channel;
                return (
                  <div className="fairy-row" key={id}>
                    <span className="fairy-badge">{label}</span>
                    <code title={b.conversationId}>{shortConversation(String(b.conversationId))}</code>
                    <span className="fairy-row__hint">
                      {new Date(b.boundAt).toLocaleString('zh-CN')} 绑定
                    </span>
                    <button
                      type="button"
                      className="fairy-btn fairy-btn--ghost"
                      disabled={unbindingId === id}
                      onClick={() => unbindChannel(b.channel, b.conversationId)}
                    >
                      {unbindingId === id ? '解绑中…' : '解绑'}
                    </button>
                  </div>
                );
              })}
            </div>
          ) : (
            <span className="fairy-badge">暂未绑定</span>
          )}
        </div>
      </div>

      <div className="fairy-row">
        <div className="fairy-row__label">
          <span className="fairy-row__title">QQ 绑定码</span>
          <span className="fairy-row__hint">在 QQ 里给机器人发：/bind 加上这个码</span>
        </div>
        <div className="fairy-row__control">
          <button type="button" className="fairy-btn" onClick={createBindCode} disabled={bindBusy}>
            {bindBusy ? '生成中…' : '生成绑定码'}
          </button>
          {bindCode ? (
            <>
              <code className="fairy-code" style={{ fontSize: 26, letterSpacing: 3 }}>{bindCode}</code>
              <button type="button" className="fairy-btn fairy-btn--ghost" onClick={copyBindCode}>
                <Copy size={14} />
                <span>复制</span>
              </button>
              <span className="fairy-row__hint">{bindCodeCountdown}</span>
            </>
          ) : null}
        </div>
      </div>
      {bindMsg ? (
        <div className={`fairy-banner ${bindMsg.kind === 'ok' ? 'fairy-banner--ok' : 'fairy-banner--error'}`}>
          {bindMsg.text}
        </div>
      ) : null}
    </>
  );
  const groupName = group?.name || me.group?.name || '';
  const inviteTtl = invite?.ttlMs || inviteTtlMs || 0;

  return (
    <>
      {loadError ? (
        <div className="fairy-banner fairy-banner--error">
          <span>{loadError}</span>
        </div>
      ) : null}

      <div className="fairy-subsection-title">当前身份</div>
      <div className="fairy-row">
        <div className="fairy-row__label">
          <span className="fairy-row__title">账号</span>
          <span className="fairy-row__hint">
            {me.via === 'session' ? '通过登录会话' : me.via === 'trusted-ip' ? '通过已信任设备' : '本机访问'}
          </span>
        </div>
        <div className="fairy-row__control">
          <span className="fairy-badge">{me.username}</span>
          <span className="fairy-badge">{roleLabel}</span>
        </div>
      </div>
      <div className="fairy-group">
        <button
          type="button"
          className="fairy-group__toggle fairy-row"
          aria-expanded={groupOpen}
          onClick={() => setGroupOpen((v) => !v)}
        >
          <div className="fairy-row__label">
            <span className="fairy-row__title">家庭组</span>
            <span className="fairy-row__hint">{groupName ? `${members.length} 位成员` : '尚未加入任何家庭组'}</span>
          </div>
          <div className="fairy-row__control">
            <span className="fairy-code">{groupName || '未加入'}</span>
            <span className="fairy-group__caret" aria-hidden="true">{groupOpen ? '▾' : '▸'}</span>
          </div>
        </button>
        {groupOpen ? (
          <div className="fairy-group__body">
            <div className="fairy-field">
              <span className="fairy-row__title">家庭组关系说明</span>
              <textarea
                rows={3}
                value={promptEdit ?? group?.prompt ?? ''}
                placeholder="例如：我爸住外地，微信绑在他自己手机上；喊我小名。"
                disabled={!canEditNote}
                onChange={(e) => setPromptEdit(e.target.value)}
              />
            </div>
            <div className="fairy-row">
              <button
                type="button"
                disabled={
                  !canEditNote ||
                  promptBusy ||
                  (promptEdit ?? group?.prompt ?? '').trim() === (group?.prompt ?? '').trim()
                }
                onClick={saveGroupPrompt}
              >
                {promptBusy ? '保存中…' : '保存说明'}
              </button>
              {promptMsg ? (
                <span className={promptMsg.kind === 'ok' ? 'fairy-field__hint' : 'fairy-field__error'}>
                  {promptMsg.text}
                </span>
              ) : null}
            </div>
            <div className="fairy-subsection-title">家庭组成员</div>
            {membersBlock}
            {noteMsg ? (
              <span className={noteMsg.kind === 'ok' ? 'fairy-field__hint' : 'fairy-field__error'}>
                {noteMsg.text}
              </span>
            ) : null}
          </div>
        ) : null}
      </div>

      {channelBindingPanel}

      <div className="fairy-subsection-title">修改密码</div>
      <form className="fairy-form" onSubmit={changePassword}>
        <div className="fairy-form__row">
          <div className="fairy-field">
            <label className="fairy-field__label" htmlFor="acc-pw-current">当前密码</label>
            <input
              id="acc-pw-current"
              className="fairy-input"
              type="password"
              autoComplete="current-password"
              value={pw.current}
              onChange={(e) => setPw({ ...pw, current: e.target.value })}
            />
          </div>
        </div>
        <div className="fairy-form__row">
          <div className="fairy-field">
            <label className="fairy-field__label" htmlFor="acc-pw-next">新密码</label>
            <input
              id="acc-pw-next"
              className="fairy-input"
              type="password"
              autoComplete="new-password"
              value={pw.next}
              onChange={(e) => setPw({ ...pw, next: e.target.value })}
            />
          </div>
          <div className="fairy-field">
            <label className="fairy-field__label" htmlFor="acc-pw-confirm">确认新密码</label>
            <input
              id="acc-pw-confirm"
              className="fairy-input"
              type="password"
              autoComplete="new-password"
              value={pw.confirm}
              onChange={(e) => setPw({ ...pw, confirm: e.target.value })}
            />
          </div>
        </div>
        {pwMsg ? (
          <div className={`fairy-banner ${pwMsg.kind === 'ok' ? 'fairy-banner--ok' : 'fairy-banner--error'}`}>
            <span>{pwMsg.text}</span>
          </div>
        ) : null}
        <div className="fairy-form__actions">
          <button type="submit" className="fairy-btn fairy-btn--primary" disabled={pwBusy}>
            {pwBusy ? <Loader2 size={14} className="fairy-btn__spin" /> : <KeyRound size={14} />}
            <span>{pwBusy ? '提交中…' : '更新密码'}</span>
          </button>
        </div>
      </form>

      <div className="fairy-subsection-title">信任设备</div>
      {devices.length === 0 ? (
        <div className="fairy-empty">
          <ShieldCheck size={16} />
          <span>暂无已信任的设备</span>
        </div>
      ) : (
        <ul className="fairy-user-list">
          {devices.map((device) => (
            <li key={device.id} className="fairy-user-list__item">
              <div className="fairy-user-list__main">
                <span className="fairy-user-list__name">
                  {device.ip}
                  {device.ip === currentIp ? '（本机）' : ''}
                </span>
                <span className="fairy-user-list__meta">
                  最后使用 {formatTime(device.last_seen_at)} · 到期 {formatTime(device.expires_at)}
                </span>
              </div>
              <div className="fairy-user-list__actions">
                <button
                  type="button"
                  className="fairy-btn fairy-btn--ghost"
                  disabled={busyDevice === device.id}
                  onClick={() => revokeDevice(device.id)}
                >
                  {busyDevice === device.id ? <Loader2 size={13} className="fairy-btn__spin" /> : <Trash2 size={13} />}
                  <span>撤销</span>
                </button>
              </div>
            </li>
          ))}
        </ul>
      )}

      {/* 家庭组成员列表与关系说明已移入上方「家庭组」展开区 */}

      <div className="fairy-subsection-title">邀请码</div>
      <div className="fairy-form">
        <div className="fairy-field__hint">
          生成后请立刻发给对方：邀请码只显示这一次，关闭后无法再次查看。
        </div>
        <div className="fairy-form__actions">
          <button type="button" className="fairy-btn fairy-btn--primary" disabled={inviteBusy} onClick={createInvite}>
            {inviteBusy ? <Loader2 size={14} className="fairy-btn__spin" /> : <UserPlus size={14} />}
            <span>{inviteBusy ? '生成中…' : '生成邀请码'}</span>
          </button>
        </div>
        {invite?.code ? (
          <div className="fairy-row">
            <div className="fairy-row__label">
              <span className="fairy-row__title">当前邀请码</span>
              <span className="fairy-row__hint">
                {formatTtl(inviteTtl)}，只显示一次
                {invite.expiresAt ? `，${formatTime(invite.expiresAt)} 过期` : ''}
              </span>
            </div>
            <div className="fairy-row__control">
              <span className="fairy-code">{invite.code}</span>
              <button type="button" className="fairy-btn fairy-btn--ghost" onClick={copyInvite}>
                {copied ? <ShieldCheck size={14} /> : <Copy size={14} />}
                <span>{copied ? '已复制' : '复制'}</span>
              </button>
            </div>
          </div>
        ) : null}
      </div>

      <div className="fairy-subsection-title">加入家庭组</div>
      <form className="fairy-form" onSubmit={joinGroup}>
        <div className="fairy-form__row">
          <div className="fairy-field">
            <label className="fairy-field__label" htmlFor="acc-join-code">邀请码</label>
            <input
              id="acc-join-code"
              className="fairy-input"
              value={joinCode}
              placeholder="XXXX-XXXX"
              onChange={(e) => setJoinCode(e.target.value)}
            />
          </div>
        </div>
        {joinMsg ? (
          <div className={`fairy-banner ${joinMsg.kind === 'ok' ? 'fairy-banner--ok' : 'fairy-banner--error'}`}>
            <span>{joinMsg.text}</span>
          </div>
        ) : null}
        <div className="fairy-form__actions">
          <button type="submit" className="fairy-btn fairy-btn--primary" disabled={joinBusy}>
            {joinBusy ? <Loader2 size={14} className="fairy-btn__spin" /> : <UserPlus size={14} />}
            <span>{joinBusy ? '加入中…' : '用邀请码加入'}</span>
          </button>
        </div>
      </form>

      <div className="fairy-subsection-title">退出登录</div>
      <div className="fairy-form">
        <div className="fairy-field__hint">退出后需要重新输入用户名和密码才能继续使用。</div>
        <div className="fairy-form__actions">
          <button type="button" className="fairy-btn fairy-btn--ghost" disabled={logoutBusy} onClick={logout}>
            {logoutBusy ? <Loader2 size={14} className="fairy-btn__spin" /> : <LogOut size={14} />}
            <span>{logoutBusy ? '退出中…' : '退出登录'}</span>
          </button>
        </div>
      </div>
    </>
  );
}

function formatTime(value) {
  if (!value) return '—';
  const date = new Date(Number(value));
  if (Number.isNaN(date.getTime())) return '—';
  return date.toLocaleString('zh-CN', { hour12: false });
}

// Keep sub-day TTLs in hours so a 24h invite reads as "24 小时内有效".
function formatTtl(ms) {
  if (!ms) return '有效期内';
  const hours = ms / 3600000;
  if (hours < 48) return `${Math.round(hours)} 小时内有效`;
  return `${Math.round(ms / 86400000)} 天内有效`;
}
