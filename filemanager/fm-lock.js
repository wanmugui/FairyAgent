// P1-10 编辑锁（前端层）。在 fm-p3.js 之后加载。
//
// 【为什么不直接改 index-fm.html 里的 openMd/savMd】它们各被复制了 5 份
// （第 13/123/232/341/1314 行），字节完全相同，只有最后一份生效。那是 P1-1
// 之前就有的冗余，不该在这轮顺手重写——动 5 行 minified 长代码的收益远小于风险。
// 所以这里在末尾覆盖全局函数：覆盖面完全一致，不碰一行旧代码。
//
// 【锁的节奏】60s 租约 + 20s 心跳（后端默认值），能容忍两次连续心跳丢失。

const LOCK_API = (typeof API !== 'undefined' ? API : '/api');
const HEARTBEAT_MS = 20000;

// 当前持有的锁：{ path, token, readOnly }
window.mdLock = null;
let _lockTimer = null;

// toast 定义在 fm-p3.js 的 IIFE 里，fm-lock.js 直接调会 ReferenceError。
// 优先用它导出的别名；万一 P3 那份没加载上，退回 console，别把整个打开流程搞崩。
function _lockToast(msg, opts) {
  try { if (typeof window.__fmToast === "function") return window.__fmToast(msg, opts); } catch (_) { /* 忽略 */ }
  console.warn("[lock] " + msg);
}

function _lockStopHeartbeat() {
  if (_lockTimer) { clearInterval(_lockTimer); _lockTimer = null; }
}

// owner 必须**按标签页**区分，不能用 userAgent。
// 同一个人开两个标签页改同一份文件，正是最常见的丢数据场景；而同 UA 会让两页
// 撞成同一个 owner，锁把第二页也放行——那这把锁在最需要的时候反而不管用。
// sessionStorage 天生按标签页隔离，刷新同一页也保持同一个 id（刷新不算换人）。
// 保存按钮没有 id（index-fm.html 里是 <button onclick="savMd()">💾</button>），按 onclick 定位。
// 用 querySelectorAll：万一以后再复制一份 UI 块，只改第一个等于没改。
function __fmSaveBtns() { return document.querySelectorAll("button[onclick=\"savMd()\"]"); }
function __fmLockReadonly(on) {
  __fmSaveBtns().forEach((b) => { b.disabled = !!on; });
}
function _lockOwnerId() {
  let id = null;
  try { id = sessionStorage.getItem('fm-owner-id'); } catch (_) { /* 隐私模式 */ }
  if (!id) {
    id = 'win-' + Math.random().toString(16).slice(2, 8);
    try { sessionStorage.setItem('fm-owner-id', id); } catch (_) { /* 存不下就用临时的 */ }
  }
  return id;
}

function _lockStartHeartbeat() {
  _lockStopHeartbeat();
  _lockTimer = setInterval(async () => {
    const lk = window.mdLock;
    if (!lk) return _lockStopHeartbeat();
    try {
      const r = await fetch(LOCK_API + '/lock/renew', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ path: lk.path, token: lk.token }),
      });
      if (r.ok) return;
      // 续期失败 = 锁没了（过期或被接管）。此时继续编辑下去会覆盖别人的内容，
      // 所以降级成只读，而不是默默续写。
      _lockStopHeartbeat();
      window.mdLock = { ...lk, readOnly: true };
      _lockToast('编辑锁已失效，已转为只读', true);
      __fmLockReadonly(true);
    } catch (_) { /* 网络抖一下下轮再试，不急着判死 */ }
  }, HEARTBEAT_MS);
}

async function _lockRelease() {
  const lk = window.mdLock;
  _lockStopHeartbeat();
  window.mdLock = null;
  if (!lk) return;
  try {
    await fetch(LOCK_API + '/lock/release', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ path: lk.path, token: lk.token }),
    });
  } catch (_) { /* 释放失败也不要紧：租约会自己过期 */ }
}

// 覆盖 openMd：先取锁，拿到才进编辑器；被占用则明确提示，而不是让两个人静默互相覆盖。
const _openMdOrig = window.openMd;
window.openMd = async function (p, n, t) {
  let r;
  try {
    r = await fetch(LOCK_API + '/lock/acquire', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ path: p, owner: _lockOwnerId() }),
    });
  } catch (e) {
    // 连不上后端就别卡住人：放行编辑，交给保存时的校验兜底
    return _openMdOrig(p, n, t);
  }
  if (r.status === 409) {
    let who = '另一个窗口';
    try { const j = await r.json(); if (j && j.holder) who = j.holder.owner; } catch (_) { /* 忽略 */ }
    const nm = n || p.split(/[\\/]/).pop();
    _lockToast(`「${nm}」正被 ${who} 编辑，已打开为只读`, { bad: true, action: { label: '仍然查看', onClick: () => { _openMdOrig(p, n, t); __fmLockReadonly(true); } } });
    window.mdLock = { path: p, token: null, readOnly: true };
    // 顺序要紧：openMd 会重建编辑区 DOM，先禁用的话按钮会被重新渲染冲掉。
    _openMdOrig(p, n, t);
    __fmLockReadonly(true);
    // 这里必须 return：少了它会一路掉进下面的成功分支，把 mdLock 覆盖回
    // readOnly:false 并起心跳——等于被占用的人照样拿到可编辑身份。
    return;
  }
  let token = null;
  try { const j = await r.json(); token = j && j.token; } catch (_) { /* 忽略 */ }
  if (window.mdLock && window.mdLock.path !== p) await _lockRelease();
  window.mdLock = { path: p, token, readOnly: false };
  _lockStartHeartbeat();
  return _openMdOrig(p, n, t);
};

// 覆盖 savMd：带上 token，并在被拒时说明原因（而不是笼统的「失败」）。
const _savMdOrig = window.savMd;
window.savMd = async function () {
  const lk = window.mdLock;
  if (lk && lk.readOnly) { _lockToast('当前是只读，不能保存', true); return; }
  if (!confirm('保存?')) return;
  const p = window.mdp;
  let url = LOCK_API + '/file?path=' + encodeURIComponent(p);
  if (lk && lk.token) url += '&lock=' + encodeURIComponent(lk.token);
  let r;
  try { r = await fetch(url, { method: 'PUT', body: document.getElementById('mdx').value }); }
  catch (e) { return alert('失败'); }
  if (r.ok) return alert('已保存');
  let msg = '失败';
  try { const j = await r.json(); if (j && j.error) msg = j.error; } catch (_) { /* 忽略 */ }
  alert(msg);
};

// 关页面/刷新时尽力释放。sendBeacon 能在页面卸载时把请求送出去，普通 fetch 不行。
window.addEventListener('pagehide', () => {
  const lk = window.mdLock;
  if (!lk || !lk.token) return;
  const blob = new Blob([JSON.stringify({ path: lk.path, token: lk.token })], { type: 'application/json' });
  try { navigator.sendBeacon(LOCK_API + '/lock/release', blob); } catch (_) { /* 算了，会过期 */ }
  _lockStopHeartbeat();
});

// 切走文件时释放（目录跳转走 loadDir，不经过 openMd，所以监听点击兜底）
document.addEventListener('click', (ev) => {
  const lk = window.mdLock;
  if (!lk || !lk.token) return;
  const t = ev.target;
  if (t && t.closest && t.closest('[data-path]')) _lockRelease();
}, true);
