// 路径编码兜底：encodeURIComponent 会把 / 编成 %2F，nginx/Cloudflare 拒绝该形式，
// 公网表现为「打不开这个文件夹 HTTP 404」。这里按 / 分段编码，斜杠保持原样。
// index-fm.html 里已有同名实现，此处仅在它尚未加载时兜底，避免依赖脚本执行顺序。
if (typeof window.encPath !== 'function') {
  window.encPath = function (p) {
    if (p === undefined || p === null) return '';
    return String(p).split('/').map(function (seg) {
      return encodeURIComponent(seg);
    }).join('/');
  };
}
// P1-9 多标签页。与目录树正交的一层：标签页只管「当前看哪个目录 + 各自的
// 滚动位置 + 各自的排序」，不动导航语义。在 fm-upload.js 之后加载。
//
// 【能覆盖 loadFiles 的前提】index-fm.html 里 currentPath 是 script 顶层的
// let 绑定，在**全局词法环境**里，所以本文件能读到。loadFiles 是函数声明，
// 是 window 上的可写属性；内联 onclick="loadFiles(...)" 在**调用时**才解析全局，
// 所以覆盖 window.loadFiles 对 onclick 同样生效。
//
// 【为什么不自己渲染列表】第一版这里覆盖了 renderFileTree 自己拼 DOM，结果把
// P3 的渲染整个顶掉了——丢掉了 .file-item 上的 data-path 和 .fm-name，
// P3 的选择/框选/拖拽全部失效（verify.cjs 直接崩）。排序本来就 P3 已经有了
// （列头 + st.sort/st.order），所以这里**只存状态、只调 P3 的入口**，
// 一个 DOM 节点都不自己造。

const TABS_KEY = 'fm-tabs';

function _tabsLoad() {
  try {
    const s = JSON.parse(localStorage.getItem(TABS_KEY) || 'null');
    if (s && Array.isArray(s.tabs) && s.tabs.length) return s;
  } catch (_) { /* 坏了就重来 */ }
  return { tabs: [{ id: 't1', path: '', scroll: 0, sort: 'name', order: 'asc' }], activeId: 't1' };
}
let T = _tabsLoad();
function _tabsSave() { try { localStorage.setItem(TABS_KEY, JSON.stringify(T)); } catch (_) { /* 隐私模式 */ } }
function _active() { let t = T.tabs.find((x) => x.id === T.activeId); if (!t) { t = T.tabs[0]; T.activeId = t.id; } return t; }

function _esc(s) { return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;'); }

// P3 可能没加载成功（那就退化成默认排序，不崩）
function _applySort(tab) {
  try {
    if (window.__fmP3 && typeof window.__fmP3.setSort === 'function') window.__fmP3.setSort(tab.sort, tab.order);
  } catch (_) { /* 忽略 */ }
}
function _readSort(tab) {
  try {
    if (window.__fmP3 && typeof window.__fmP3.getSort === 'function') {
      const s = window.__fmP3.getSort();
      tab.sort = s.sort; tab.order = s.order;
    }
  } catch (_) { /* 忽略 */ }
}

// ---------------------------------------------------------------------------
// 覆盖 loadFiles：**只**把路径记进当前标签页
//
// 这里刻意什么都不多做——不重排、不重置排序、不动滚动。早先每次 loadFiles
// 都把标签页存的排序重新摆上去，结果用户在 P3 列头上点「大小」之后，下一次
// 导航又把它悄悄改回 name/asc，排序点不动（verify.cjs 直接抓到）。排序是
// P3 的事，标签页只在**切换时**接管一下，其余时间不跟它抢。
// ---------------------------------------------------------------------------
const _origLoadFiles = window.loadFiles;
window.loadFiles = async function (path = '') {
  const tab = _active();
  const changed = tab.path !== path;
  tab.path = path;
  _tabsSave();
  // 目录变了就把标签名一起刷掉。少了这一句，用户在一个标签页里从 A 走进 B，
  // 标签页还写着 A 的名字——真点击测试里就是这么抓到的。
  if (changed) _tabsPaint();
  return _origLoadFiles.apply(this, arguments);
};

function _tabsBar() { return document.getElementById('fm-tabs-bar'); }
function _tabsRestoreScroll() {
  const tree = document.getElementById('fileTree');
  if (tree) tree.scrollTop = _active().scroll || 0;
}

function _tabLabel(tab) {
  if (!tab.path) return '主页';
  const parts = tab.path.split(/[\\/]/).filter(Boolean);
  return parts.length ? parts[parts.length - 1] : '根目录';
}

function _tabsPaint() {
  const bar = _tabsBar();
  if (!bar) return;
  const tab = _active();
  bar.innerHTML = '<div class="fm-tabs-row">' +
    T.tabs.map((t) => '<div class="fm-tab' + (t.id === T.activeId ? ' active' : '') + '" data-tab="' + t.id + '" title="' + _esc(t.path || '/') + '">' +
      '<span class="fm-tab-name">' + _esc(_tabLabel(t)) + '</span>' +
      (T.tabs.length > 1 ? '<span class="fm-tab-close" data-close="' + t.id + '">×</span>' : '') +
      '</div>').join('') +
    '<div class="fm-tab fm-tab-add" data-add="1" title="新建标签页">+</div></div>';
}

async function __fmSwitchTab(id) {
  const cur = _active();
  const tree = document.getElementById('fileTree');
  if (tree) cur.scroll = tree.scrollTop;   // 离开前先存滚动位置
  _readSort(cur);                          // 走之前把 P3 里的当前排序记回本标签页
  T.activeId = id;
  _tabsSave();
  _tabsPaint();
  // 排序是**切页时**才接管：在加载目标目录之前把该标签页的排序摆上去，
  // 这样只渲染一次，顺序就对。切页之外的路径一律不碰 P3 的排序。
  _applySort(_active());
  await window.loadFiles(_active().path);
  _tabsRestoreScroll();
  return id;
}

async function __fmAddTab(path = '') {
  const id = 't' + Date.now().toString(36) + Math.random().toString(36).slice(2, 6);
  T.tabs.push({ id, path, scroll: 0, sort: 'name', order: 'asc' });
  _tabsSave();
  return __fmSwitchTab(id);
}

function __fmCloseTab(id) {
  if (T.tabs.length <= 1) return false;
  const i = T.tabs.findIndex((t) => t.id === id);
  if (i < 0) return false;
  _readSort(T.tabs[i]);
  T.tabs.splice(i, 1);
  if (T.activeId === id) T.activeId = T.tabs[Math.max(0, i - 1)].id;
  _tabsSave();
  _tabsPaint();
  window.loadFiles(_active().path);
  return true;
}

/** 滚动位置实时记进当前标签页，切走时不用靠「离开那一刻」去猜。 */
function __fmTrackScroll() {
  const tree = document.getElementById('fileTree');
  if (!tree) return;
  tree.addEventListener('scroll', () => { _active().scroll = tree.scrollTop; _tabsSaveThrottled(); }, { passive: true });
}
let _saveTimer = null;
function _tabsSaveThrottled() {
  if (_saveTimer) return;
  _saveTimer = setTimeout(() => { _saveTimer = null; _tabsSave(); }, 400);
}

function __fmInitTabs() {
  const anchor = document.querySelector('.sidebar-header');
  if (anchor && !_tabsBar()) {
    const bar = document.createElement('div');
    bar.id = 'fm-tabs-bar';
    anchor.insertAdjacentElement('afterend', bar);
    bar.addEventListener('click', async (ev) => {
      if (ev.target.closest('[data-add]')) { await __fmAddTab(_active().path); return; }
      const cl = ev.target.closest('[data-close]');
      if (cl) { ev.stopPropagation(); __fmCloseTab(cl.getAttribute('data-close')); return; }
      const t = ev.target.closest('[data-tab]');
      if (t) { await __fmSwitchTab(t.getAttribute('data-tab')); return; }
    });
    __fmTrackScroll();
  }
  _tabsPaint();
  // 恢复上次会话：标签页还在，各自的目录也要真的加载出来
  window.loadFiles(_active().path);
}

window.__fmTabs = {
  state: () => T,
  switchTab: __fmSwitchTab,
  addTab: __fmAddTab,
  closeTab: __fmCloseTab,
  active: _active,
};

if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', __fmInitTabs);
else __fmInitTabs();
