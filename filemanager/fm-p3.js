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
/* P3-1/2/3/5/6/7 文件列表交互层
   单独成文件而不是塞进 index-fm.html 的 1297 行里：这块逻辑要能被单测和 review。
   挂载方式是"接管"已有的 renderFileTree / loadFiles，不改原文件的其它功能。 */

(function () {
  'use strict';

  // 视图与选择状态。view/sort 持久化：换目录回来还是大图标，很烦。
  const LS = 'fm_p3';
  let st = {
    view: 'list',
    sort: 'name',
    order: 'asc',
    selected: [],   // 存 path，保持稳定（名字会重复，索引不唯一）
    anchor: null,   // shift 连选的锚点
    cursor: 0,      // 键盘光标在当前可见列表里的下标
    theme: 'light',
  };
  try { Object.assign(st, JSON.parse(localStorage.getItem(LS) || '{}')); } catch (_) {}
  const save = () => { try { localStorage.setItem(LS, JSON.stringify(st)); } catch (_) {} };

  let tree, toolbar, head, bulk, marquee, stateBox, skeleton;
  // 请求序号：只有最后一次请求有权写状态。
  // 之前是"谁先返回谁说了算"，于是页面自身的 cwd 自动加载（读 sessionStorage）
  // 如果比显式跳转晚返回，就会把错误态覆盖成空态——同一个 URL 每次结果还不一样。
  let reqSeq = 0;
  // 当前错误信息。非空时 render() 不得退回空态——
  // 老代码有多处直接调 renderFileTree()，绕过了上面的请求序号护栏，
  // 于是打不开的目录会显示成"这个文件夹是空的"。
  let lastError = null;
  // 页面初始化时会自己发一次 loadFiles('')（空路径，走默认目录）。
  // 那次请求若晚于用户的显式导航返回，就会把"打不开"覆盖成"空目录"。
  // 判据是有没有带路径的显式导航，而不是拿 sessionStorage 里的 cwd 去比——
  // 实测它发的是空路径，比对根本命不中。
  let explicitNav = false;
  let dragPaths = [];

  const esc = (s) => String(s == null ? '' : s)
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;').replace(/'/g, '&#39;');

  function fmtSize(n) {
    if (typeof n !== 'number' || !isFinite(n)) return '—';
    if (n < 1024) return n + ' B';
    const u = ['KB', 'MB', 'GB', 'TB'];
    let v = n / 1024, i = 0;
    while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
    return (v >= 100 ? v.toFixed(0) : v.toFixed(1)) + ' ' + u[i];
  }
  function fmtTime(ms) {
    if (typeof ms !== 'number' || !isFinite(ms)) return '—';
    const d = new Date(ms);
    const p = (x) => String(x).padStart(2, '0');
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
  }

  // 目录恒在前（与后端 sortItems 一致），其次星标、最后按当前排序列。
  function visible() {
    const list = [...(typeof files !== 'undefined' ? files : [])];
    const cmp = {
      name: (a, b) => String(a.name).localeCompare(String(b.name), 'zh'),
      size: (a, b) => (a.size || 0) - (b.size || 0),
      mtime: (a, b) => (a.modified || 0) - (b.modified || 0),
    }[st.sort] || ((a, b) => String(a.name).localeCompare(String(b.name), 'zh'));
    const dir = cmp;
    return list.sort((a, b) => {
      if (!!a.isDirectory !== !!b.isDirectory) return a.isDirectory ? -1 : 1;
      if (!!a.starred !== !!b.starred) return a.starred ? -1 : 1;
      return dir(a, b) * (st.order === 'desc' ? -1 : 1);
    });
  }

  // ————————————————— P3-6 三态 —————————————————
  // 骨架行数跟随列表可视高度。之前固定 8 行，加载时下方留一大片纯白，
  // 看起来像"只加载了一半"；行宽也做点变化，更接近真实条目。
  function fillSkeleton() {
    // 先放一行量出真实步距：--ui-row-h 在不同视图下并不等于 .sk-row 的
    // 实际渲染高度，按变量算会算少一半行数。
    skeleton.innerHTML = '<div class="sk-row" style="width:80%"></div>';
    const probe = skeleton.firstElementChild;
    const pcs = getComputedStyle(probe);
    const stride = probe.offsetHeight +
      (parseFloat(pcs.marginTop) || 0) + (parseFloat(pcs.marginBottom) || 0);
    const cs = getComputedStyle(skeleton);
    const avail = skeleton.clientHeight -
      (parseFloat(cs.paddingTop) || 0) - (parseFloat(cs.paddingBottom) || 0);
    const n = Math.max(3, Math.min(24, Math.floor(avail / (stride || 31))));
    const widths = [88, 72, 81, 64, 86, 70, 78, 61, 84, 68, 90, 66];
    // 用取模循环而不是 slice：n 由可视高度算出，可能超过 widths 长度，
    // slice 会被数组长度截断（列表高时只画 12 行，下面留一大片空白）。
    skeleton.innerHTML = Array.from({ length: n }, (_, i) =>
      `<div class="sk-row" style="width:${widths[i % widths.length]}%"></div>`).join('');
  }

  function setState(kind, msg) {
    if (!stateBox) return;
    const icons = { loading: '', empty: '📂', error: '⚠️' };
    // 先无条件清掉 loading：之前把清理放在 `!kind` 的 return 之后，
    // 于是成功渲染完 toolbar 仍带 .loading，CSS 里 pointer-events:none
    // 把整个工具条变成点不动的死区。
    skeleton.classList.remove('on');
    toolbar.classList.remove('loading');
    if (!kind) { stateBox.className = ''; stateBox.innerHTML = ''; return; }
    if (kind === 'loading') { skeleton.classList.add('on'); toolbar.classList.add('loading'); fillSkeleton(); return; }
    stateBox.className = 'on ' + kind;
    stateBox.innerHTML = kind === 'error'
      ? `<div class="fm-state-icon">${icons.error}</div>
         <div class="fm-state-msg">${esc(msg || '加载失败')}</div>
         <button class="fm-retry" style="margin-top:8px">重试</button>`
      : `<div class="fm-state-icon">${icons.empty}</div><div>${esc(msg || '这个文件夹是空的')}</div>`;
    const rb = stateBox.querySelector('.fm-retry');
    if (rb) rb.onclick = () => loadFiles(currentPath);
  }

  // ————————————————— P3-2 选择 —————————————————
  const sel = new Set();
  function syncSel() { st.selected = [...sel]; save(); }

  function toggle(path, on) {
    if (on === undefined) on = !sel.has(path);
    if (on) sel.add(path); else sel.delete(path);
    syncSel(); paintSelection(); renderBulk();
  }
  function clearSel() { sel.clear(); syncSel(); paintSelection(); renderBulk(); }

  function paintSelection() {
    if (!tree) return;
    tree.querySelectorAll('.file-item').forEach((el) => {
      const on = sel.has(el.dataset.path);
      el.classList.toggle('selected', on);
      const cb = el.querySelector('.fm-check');
      if (cb) cb.checked = on;
    });
  }

  function renderBulk() {
    if (!bulk) return;
    const n = sel.size;
    bulk.classList.toggle('on', n > 0);
    bulk.querySelector('.count').textContent = `已选 ${n} 项`;
    const all = visible();
    const sa = toolbar.querySelector('#fm-p3-selectall');
    if (sa) {
      sa.checked = n > 0 && n === all.length;
      sa.indeterminate = n > 0 && n < all.length;
    }
  }

  // ————————————————— 渲染 —————————————————
  function rangeSelect(path) {
    const list = visible();
    const a = list.findIndex((f) => f.path === st.anchor);
    const b = list.findIndex((f) => f.path === path);
    if (a < 0 || b < 0) { toggle(path, true); return; }
    const [lo, hi] = a < b ? [a, b] : [b, a];
    sel.clear();                       // 区间是替换语义，不是叠加
    for (let k = lo; k <= hi; k++) sel.add(list[k].path);
    syncSel(); paintSelection(); renderBulk();
  }

  function render() {
    if (!tree) return;
    const list = visible();
    tree.dataset.view = st.view;
    const wrap = document.getElementById('fm-p3-wrap');
    if (wrap) wrap.dataset.view = st.view;
    toolbar.querySelectorAll('.fm-viewbtn').forEach((b) => {
      b.setAttribute('aria-pressed', String(b.dataset.view === st.view));
    });

    if (lastError && !list.length) { tree.innerHTML = ''; tree.hidden = true; setState('error', lastError); return; }
    if (!list.length) {
      tree.innerHTML = '';
      setState('empty');
      tree.hidden = true;
      return;
    }
    setState(null);
    tree.hidden = false;

    const wantDetail = st.view === 'detail';
    tree.innerHTML = list.map((f, i) => {
      const cls = ['file-item', f.isDirectory ? 'folder' : 'file'];
      if (sel.has(f.path)) cls.push('selected');
      if (f.active || (typeof activeFile !== 'undefined' && activeFile === f.path)) cls.push('active');
      if (i === st.cursor) cls.push('cursor');
      const icon = f.isDirectory ? 'mdi-folder' : getFileIconClass(f.name);
      return `<div class="${cls.join(' ')}" data-path="${esc(f.path)}" data-dir="${f.isDirectory ? 1 : 0}"
        data-kind="${typeof kindOf === 'function' ? kindOf(f) : 'other'}" style="--i:${Math.min(i, 11)}"
        data-i="${i}" draggable="true" tabindex="0" role="option"
        aria-selected="${sel.has(f.path)}" title="${esc(f.name)}">
        <input class="fm-check" type="checkbox" ${sel.has(f.path) ? 'checked' : ''}
          aria-label="选择 ${esc(f.name)}" tabindex="-1" />
        <i class="mdi ${icon} icon"></i>
        <span class="fm-name">${esc(f.name)}</span>
        ${wantDetail ? `<span class="fm-size">${esc(fmtSize(f.size))}</span>
          <span class="fm-time">${esc(fmtTime(f.modified))}</span>` : ''}
      </div>`;
    }).join('');

    if (head) {
      head.querySelectorAll('.col').forEach((c) => {
        if (c.dataset.sort === st.sort) c.setAttribute('aria-sort', st.order === 'asc' ? 'ascending' : 'descending');
        else c.removeAttribute('aria-sort');
      });
    }
    renderBulk();
  }

  // ————————————————— P3-3 移动/重命名 —————————————————
  // dest 是完整目标路径（含新文件名）。收目录再拼 basename 的写法
  // 对移动成立，对重命名就变成"改成自己"。
  function joinDest(dir, name) { return dir.replace(/\/+$/, '') + '/' + name; }

  async function moveTo(from, dest, isMove) {
    const to = dest;
    // API 常量本身已经是 '/api'，这里只补动作名
    const endpoint = isMove ? '/move' : '/rename';
    const r = await fetch(API + endpoint, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ from, to }),
    });
    return { ok: r.ok, status: r.status, body: await r.json().catch(() => ({})) };
  }

  // P1-3 / P1-7：toast 从「只能显示一句话」升级为可带操作按钮与可展开的失败明细。
  //   原来只有 textContent + pointer-events:none + 2.2 秒，装不下撤销按钮。
  // 兼容旧调用：toast(msg) / toast(msg, true) 仍然照常工作。
  function toast(msg, opts) {
    const o = (opts === true || opts === false) ? { bad: opts } : (opts || {});
    const hasDetail = Array.isArray(o.detail) && o.detail.length > 0;
    // 有可点的东西就别急着收走：2.2 秒根本来不及瞄准「撤销」
    const ms = o.ms || (o.action || hasDetail ? 10000 : 2200);

    let t = document.getElementById('fm-p3-toast');
    if (!t) {
      t = document.createElement('div');
      t.id = 'fm-p3-toast';
      document.body.appendChild(t);
    }
    t.innerHTML = '';

    const m = document.createElement('span');
    m.className = 'fm-toast-msg';
    m.textContent = msg;
    t.appendChild(m);

    if (hasDetail) {
      // 原生 <details>：展开/收起不用自己维护状态，键盘和读屏也免费拿到
      const d = document.createElement('details');
      d.className = 'fm-toast-detail';
      const s = document.createElement('summary');
      s.textContent = '展开失败明细';
      d.appendChild(s);
      const ul = document.createElement('ul');
      for (const f of o.detail) {
        const li = document.createElement('li');
        li.textContent = f.name + ' — ' + f.why;
        ul.appendChild(li);
      }
      d.appendChild(ul);
      t.appendChild(d);
    }

    if (o.action) {
      const b = document.createElement('button');
      b.type = 'button';
      b.className = 'fm-toast-action';
      b.id = 'fm-toast-undo';
      b.textContent = o.action.label || '撤销';
      b.addEventListener('click', () => {
        clearTimeout(t._h);
        t.style.opacity = '0';
        o.action.onClick();
      });
      t.appendChild(b);
    }

    const bg = o.bad ? 'var(--ui-danger)' : (hasDetail ? 'var(--ui-text)' : 'var(--ui-accent)');
    const fg = o.bad ? 'var(--ui-accent-fg)' : 'var(--ui-accent-fg)';
    t.style.cssText = `position:fixed;left:50%;bottom:26px;transform:translateX(-50%);
      display:flex;align-items:center;gap:12px;max-width:min(560px,92vw);
      padding:8px 14px;border-radius:var(--ui-radius);font-size:var(--ui-fs);z-index:999;
      background:${bg};color:${fg};box-shadow:var(--ui-shadow-md);
      pointer-events:auto;transition:opacity var(--ui-dur)`;
    t.style.opacity = '1';
    clearTimeout(t._h);
    t._h = setTimeout(() => { t.style.opacity = '0'; }, ms);
  }

  // P1-10：fm-lock.js 要复用这个 toast，IIFE 内的函数默认不外露。
  // 只加一个别名，不改 toast 本身的行为。
  window.__fmToast = toast;

  // P1-9 多标签页要按标签页各自记住排序状态，所以把排序的读写开出来。
  // 刻意**不**把整个 st 暴露出去：那等于让外面直接改 selected/cursor 之类
  // 与排序无关的状态，多标签页只需要「设一个排序」和「读当前排序」而已。
  window.__fmP3 = {
    // list 区的加载/空/错误态由内联脚本的 loadFiles 驱动，这里把状态机开出去。
    setState,
    getSort: () => ({ sort: st.sort, order: st.order }),
    setSort: (s, o) => {
      if (s) st.sort = s;
      if (o) st.order = o;
      save();
      render();
    },
  };

  // 自绘确认框：window.confirm 会被浏览器压住，且没法跟 P3 的视觉令牌统一
  function confirmBox(msg) {
    return new Promise((resolve) => {
      const back = document.createElement('div');
      back.id = 'fm-p3-modal';
      back.style.cssText = `position:fixed;inset:0;background:var(--ui-overlay);
        display:flex;align-items:center;justify-content:center;z-index:998`;
      back.innerHTML = `<div style="background:var(--ui-surface);border-radius:8px;
          box-shadow:var(--ui-shadow-md);padding:18px 20px;min-width:260px;
          border:1px solid var(--ui-border);font-family:var(--ui-font)">
        <div style="margin-bottom:14px;font-size:13px;color:var(--ui-text)">${esc(msg)}</div>
        <div style="display:flex;gap:8px;justify-content:flex-end">
          <button data-v="0" style="height:28px;padding:0 14px;border:1px solid var(--ui-border-strong);
            background:var(--ui-surface);border-radius:4px;cursor:pointer">取消</button>
          <button data-v="1" style="height:28px;padding:0 14px;border:1px solid var(--ui-danger);
            background:var(--ui-danger);color:#fff;border-radius:4px;cursor:pointer">确定</button>
        </div></div>`;
      document.body.appendChild(back);
      const done = (v) => { back.remove(); resolve(v); };
      back.addEventListener('click', (e) => {
        if (e.target === back) return done(false);
        const b = e.target.closest('button');
        if (b) done(b.dataset.v === '1');
      });
    });
  }

  async function doDelete(paths) {
    const names = paths.map((p) => p.split('/').pop());
    const label = paths.length === 1 ? `「${names[0]}」` : `这 ${paths.length} 项`;
    // P1-2 之后删除是「移到回收站」，「删除后无法恢复」已经是假话，
    // 会让人在可撤销时白白放弃确认。
    if (!(await confirmBox(`把 ${label} 移到回收站？之后可以还原。`))) return;

    // P1-7：整体事务不中断，逐项收成败，明细要能展开看
    const okIds = [], failed = [];
    for (const p of paths) {
      const nm = p.split('/').pop();
      try {
        const r = await fetch(API + '/files?path=' + encPath(p), { method: 'DELETE' });
        if (r.ok) {
          const j = await r.json().catch(() => ({}));
          if (j && j.id) okIds.push(j.id);
        } else {
          failed.push({ name: nm, why: 'HTTP ' + r.status });
        }
      } catch (e) {
        failed.push({ name: nm, why: e.message });
      }
    }
    clearSel();

    const okN = paths.length - failed.length;
    const undo = okIds.length
      ? { label: '撤销', onClick: () => undoTrash(okIds) }
      : null;

    if (okN === 0) {
      toast(`失败 ${failed.length} 项`, { bad: true, detail: failed });
    } else if (failed.length) {
      toast(`成功 ${okN} / 失败 ${failed.length}`, { detail: failed, action: undo });
    } else {
      toast(`已移到回收站 ${okN} 项`, { action: undo });
    }
    await loadFiles(currentPath);
  }

  // P1-3 撤销：把刚移进回收站的原样放回去。
  // 用 P1-2 的 restore 端点，所以撤销是真的还原，而不是再写一份副本。
  async function undoTrash(ids) {
    let okN = 0; const failed = [];
    for (const id of ids) {
      try {
        const r = await fetch(API + '/trash/restore', {
          method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ id })
        });
        if (r.ok) okN++;
        else {
          const j = await r.json().catch(() => ({}));
          failed.push({ name: id, why: (j && j.error) || ('HTTP ' + r.status) });
        }
      } catch (e) { failed.push({ name: id, why: e.message }); }
    }
    toast(failed.length ? `已撤销 ${okN} / 失败 ${failed.length}` : `已撤销 ${okN} 项`,
      { bad: failed.length > 0, detail: failed });
    await loadFiles(currentPath);
  }

  async function doMove(paths, toDir) {
    // P1-7 同样适用：原来失败项是每项弹一次 toast，彼此覆盖，
    // 用户最后只看得到最后一个，中间失败的几项就这么丢了。
    const failed = [];
    for (const p of paths) {
      // 拖进自己或自己的子目录是死循环，源和目标同一棵树，拦在前面
      if (p === toDir || p.startsWith(toDir.replace(/\/+$/, '') + '/')) {
        toast('不能移动到自身或其子目录', true);
        return;
      }
      const r = await moveTo(p, joinDest(toDir, p.split('/').pop()), true);
      if (!r.ok) {
        failed.push({
          name: p.split('/').pop(),
          why: r.status === 409 ? '目标已存在，未覆盖' : 'HTTP ' + r.status,
        });
      }
    }
    clearSel();
    const okN = paths.length - failed.length;
    if (failed.length) toast(`成功 ${okN} / 失败 ${failed.length}`, { detail: failed });
    else toast(`已移动 ${okN} 项`);
    await loadFiles(currentPath);
  }

  // ————————————————— P3-5 重命名 —————————————————
  function startRename(path) {
    const el = tree.querySelector(`.file-item[data-path="${CSS.escape(path)}"]`);
    const f = (typeof files !== 'undefined' ? files : []).find((x) => x.path === path);
    if (!el || !f) return;
    const nameSpan = el.querySelector('.fm-name');
    const old = f.name;
    nameSpan.innerHTML = '';
    const inp = document.createElement('input');
    inp.value = old;
    inp.className = 'fm-rename-input';
    inp.setAttribute('aria-label', '重命名');
    Object.assign(inp.style, {
      width: '100%', height: '22px', fontSize: '13px', fontFamily: 'var(--ui-font)',
      border: '1px solid var(--ui-accent)', borderRadius: '3px', padding: '0 4px',
      background: 'var(--ui-surface)', color: 'var(--ui-text)',
    });
    nameSpan.appendChild(inp);
    inp.focus();
    inp.select();
    let settled = false;
    const commit = async (saveIt) => {
      if (settled) return; settled = true;
      const next = inp.value.trim();
      if (!saveIt || !next || next === old) { render(); return; }
      const r = await moveTo(path, joinDest(path.replace(/\/[^/]*$/, ''), next), false);
      if (!r.ok) toast(r.status === 409 ? '重名了，改一个吧' : '重命名失败', true);
      await loadFiles(currentPath);
    };
    inp.addEventListener('keydown', (e) => {
      e.stopPropagation();               // 关键：别让 P3 的快捷键劫持输入框
      if (e.key === 'Enter') { e.preventDefault(); commit(true); }
      else if (e.key === 'Escape') { e.preventDefault(); commit(false); }
    });
    inp.addEventListener('blur', () => commit(true));
  }

  // ————————————————— P3-5 快捷键 —————————————————
  function isTyping(t) {
    if (!t) return false;
    const tag = (t.tagName || '').toLowerCase();
    if (tag === 'textarea' || tag === 'select' || t.isContentEditable) return true;
    if (tag === 'input') {
      // 勾选框/单选/按钮不是"正在输入文本"。
      // 一律当输入处理的话，点一下行内勾选框之后 Ctrl+A 和 Esc 就全被吞了——
      // 而这恰恰是用户点完勾选框最想按的两个键。
      const ty = (t.type || 'text').toLowerCase();
      return !['checkbox', 'radio', 'button', 'submit', 'range', 'color'].includes(ty);
    }
    return false;
  }
  function moveCursor(delta) {
    const items = tree.querySelectorAll('.file-item');
    if (!items.length) return;
    st.cursor = Math.max(0, Math.min(items.length - 1, st.cursor + delta));
    render();
    const el = items[st.cursor];
    if (el) el.focus();
  }
  function cursorItem() {
    const el = tree.querySelector('.file-item.cursor');
    return el || null;
  }

  function onKey(e) {
    if (isTyping(e.target) || e.target.closest('.monaco-editor, .cm-editor')) return;
    const mod = e.ctrlKey || e.metaKey;

    if (mod && (e.key === 'a' || e.key === 'A')) {
      e.preventDefault();
      visible().forEach((f) => sel.add(f.path));
      syncSel(); paintSelection(); renderBulk();
      return;
    }
    if (e.key === 'Escape') {
      if (sel.size) { clearSel(); return; }
      if (marquee && marquee.style.display === 'block') endMarquee();
      return;
    }
    if (e.key === 'ArrowDown') { e.preventDefault(); return moveCursor(1); }
    if (e.key === 'ArrowUp')   { e.preventDefault(); return moveCursor(-1); }
    if (e.key === 'ArrowRight' && st.view === 'list') {
      e.preventDefault();
      const c = cursorItem();
      if (c && c.dataset.dir === '1') loadFiles(c.dataset.path);
      return;
    }
    if (e.key === 'ArrowLeft') {
      e.preventDefault();
      if (currentPath) loadFiles(currentPath.replace(/\/[^/]*$/, ''));
      return;
    }
    if (e.key === 'Enter') {
      const c = cursorItem();
      if (c) { e.preventDefault(); c.click(); }
      return;
    }
    if (e.key === 'F2') {
      const c = cursorItem();
      if (c) { e.preventDefault(); startRename(c.dataset.path); }
      return;
    }
    if (e.key === 'Delete') {
      if (!sel.size) return;
      e.preventDefault();
      doDelete([...sel]);
    }
  }

  // ————————————————— P3-2 框选 —————————————————
  let marqueeStart = null;
  let justDragged = false;   // 框选拖完浏览器还会补一个 click，得挡住
  let suppressClick = false;
  function startMarquee(e) {
    if (e.button !== 0) return;
    // 列表铺满时下面没有空白可按，起点落在行上就是"拖文件"——
    // 这是对的。所以框选改用 Shift 按住从任意行起拖（Explorer/Nautilus 的做法）。
    if (e.target.closest('.file-item, .fm-check, button') && !e.shiftKey) return;
    marqueeStart = { x: e.clientX, y: e.clientY, base: new Set(sel) };
    document.addEventListener('mousemove', onMarqueeMove);
    document.addEventListener('mouseup', endMarquee, { once: true });
    window.addEventListener('mouseup', endMarquee, { once: true });   // 兜底
  }
  function onMarqueeMove(e) {
    if (!marqueeStart) return;
    const x = Math.min(marqueeStart.x, e.clientX), y = Math.min(marqueeStart.y, e.clientY);
    const w = Math.abs(marqueeStart.x - e.clientX), h = Math.abs(marqueeStart.y - e.clientY);
    marquee.style.display = 'block';
    Object.assign(marquee.style, { left: x + 'px', top: y + 'px', width: w + 'px', height: h + 'px' });
    if (w < 3 && h < 3) return;              // 抖动不算框选
    justDragged = true;
    suppressClick = true;                     // 真框选了：随后那发 click 必须吃掉
    window.__g=(window.__g||[]).concat([{rect:[x,y,w,h],rows:[...tree.querySelectorAll(".file-item")].slice(0,4).map(el=>{const r=el.getBoundingClientRect();return [Math.round(r.left),Math.round(r.top),Math.round(r.right),Math.round(r.bottom),el.dataset.path];})}]);
    sel.clear();
    marqueeStart.base.forEach((p) => sel.add(p));
    tree.querySelectorAll('.file-item').forEach((el) => {
      const r = el.getBoundingClientRect();
      const hit = r.right > x && r.left < x + w && r.bottom > y && r.top < y + h;
      if (hit) sel.add(el.dataset.path);
    });
    syncSel(); paintSelection(); renderBulk();
  }
  function endMarquee() {
    marqueeStart = null;
    document.removeEventListener('mousemove', onMarqueeMove);
    window.removeEventListener('mouseup', endMarquee);
    if (marquee) marquee.style.display = 'none';
  }

  // ————————————————— P3-3 拖拽 —————————————————
  function onDragStart(e) {
    if (e.shiftKey) { e.preventDefault(); return; }   // Shift 是框选，别同时搬文件
    const el = e.target.closest('.file-item');
    if (!el) return;
    const p = el.dataset.path;
    if (!sel.has(p)) { sel.clear(); sel.add(p); syncSel(); paintSelection(); renderBulk(); }
    dragPaths = [...sel];
    el.classList.add('dragging');
    e.dataTransfer.effectAllowed = 'move';
    try { e.dataTransfer.setData('text/plain', p); } catch (_) {}
  }
  function onDragOver(e) {
    const el = e.target.closest('.file-item');
    tree.querySelectorAll('.drop-target').forEach((x) => x.classList.remove('drop-target'));
    if (el && el.dataset.dir === '1') {
      e.preventDefault();
      e.dataTransfer.dropEffect = 'move';
      el.classList.add('drop-target');
    }
  }
  async function onDrop(e) {
    const el = e.target.closest('.file-item');
    tree.querySelectorAll('.drop-target').forEach((x) => x.classList.remove('drop-target'));
    if (!el || el.dataset.dir !== '1') { endDrag(); return; }
    e.preventDefault();
    const target = el.dataset.path;
    const paths = dragPaths.length ? dragPaths : (() => { try { return [e.dataTransfer.getData('text/plain')]; } catch (_) { return []; } })();
    endDrag();
    if (paths.length) await doMove(paths, target);
  }
  function endDrag() {
    dragPaths = [];
    tree.querySelectorAll('.dragging,.drop-target').forEach((x) => x.classList.remove('dragging', 'drop-target'));
  }

  // ————————————————— 挂载 —————————————————
  function mount() {
    tree = document.getElementById('fileTree');
    if (!tree || document.getElementById('fm-p3-toolbar')) return;
    const host = tree.parentElement;

    toolbar = document.createElement('div');
    toolbar.id = 'fm-p3-toolbar';
    toolbar.innerHTML = `
      <div id="fm-p3-viewbtns" role="group" aria-label="视图模式">
        <button class="fm-viewbtn" data-view="list"   title="列表"   aria-pressed="false">☰</button>
        <button class="fm-viewbtn" data-view="grid"   title="大图标" aria-pressed="false">▦</button>
        <button class="fm-viewbtn" data-view="detail" title="详情"   aria-pressed="false">▤</button>
      </div>
      <input type="checkbox" id="fm-p3-selectall" title="全选" aria-label="全选" />
      <span class="fm-spin">加载中…</span>
      <button id="fm-p3-themebtn" title="切换深浅色" aria-label="切换深浅色">◐</button>`;

    head = document.createElement('div');
    head.id = 'fm-p3-head';
    head.innerHTML = `
      <span class="col" data-sort="name"  style="flex:1 1 auto;min-width:0">名称 <span class="arrow">▾</span></span>
      <span class="col fm-size" data-sort="size">大小 <span class="arrow">▾</span></span>
      <span class="col fm-time" data-sort="mtime">修改时间 <span class="arrow">▾</span></span>`;

    bulk = document.createElement('div');
    bulk.id = 'fm-bulkbar';
    bulk.setAttribute('role', 'toolbar');
    bulk.setAttribute('aria-label', '批量操作');
    bulk.innerHTML = `<span class="count">已选 0 项</span>
      <button data-act="open" title="打开" aria-label="打开"><svg class="bb-ico" viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M2 13.5V3.6A1.4 1.4 0 0 1 3.4 2.2h3.1l1.5 1.8h4.6A1.4 1.4 0 0 1 14 5.4v1"/><path d="M1.2 13.6 2.9 8.5a1.4 1.4 0 0 1 1.3-1h9.4a1.4 1.4 0 0 1 1.3 1.9l-1.5 4.2a1.4 1.4 0 0 1-1.3 1H2.6a1.4 1.4 0 0 1-1.4-1z"/></svg></button>
      <button data-act="down" title="下载" aria-label="下载"><svg class="bb-ico" viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M8 2.2v7.6"/><path d="m4.6 6.6 3.4 3.4 3.4-3.4"/><path d="M2.6 12.4v.4a1 1 0 0 0 1 1h8.8a1 1 0 0 0 1-1v-.4"/></svg></button>
      <button data-act="star" title="星标" aria-label="星标"><svg class="bb-ico" viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m8 2.2 1.82 3.69 4.07.59-2.95 2.87.7 4.05L8 11.38l-3.64 1.91.7-4.05-2.95-2.87 4.07-.59z"/></svg></button>
      <button data-act="rename" title="重命名" aria-label="重命名"><svg class="bb-ico" viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M11.2 2.7a1.52 1.52 0 0 1 2.15 2.15l-7.9 7.9L2 14.2l1.45-3.4z"/></svg></button>
      <button data-act="del" class="danger" title="删除" aria-label="删除"><svg class="bb-ico" viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M2.6 4.4h10.8"/><path d="M6.1 4.4V3.1a1 1 0 0 1 1-1h1.8a1 1 0 0 1 1 1v1.3"/><path d="M4 4.4l.68 8.5a1 1 0 0 0 1 .93h4.64a1 1 0 0 0 1-.93L12 4.4"/><path d="M6.7 7.1v3.8M9.3 7.1v3.8"/></svg></button>
      <button data-act="clear" title="取消" aria-label="取消"><svg class="bb-ico" viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m4.2 4.2 7.6 7.6M11.8 4.2l-7.6 7.6"/></svg></button>`;

    stateBox = document.createElement('div');
    stateBox.id = 'fm-state';
    stateBox.setAttribute('role', 'status');
    stateBox.setAttribute('aria-live', 'polite');

    skeleton = document.createElement('div');
    skeleton.id = 'fm-p3-skeleton';
    skeleton.setAttribute('aria-hidden', 'true');
    skeleton.innerHTML = new Array(8).fill('<div class="sk-row"></div>').join('');

    marquee = document.createElement('div');
    marquee.id = 'fm-marquee';

    const wrap = document.createElement('div');
    wrap.id = 'fm-p3-wrap';
    wrap.dataset.view = st.view;
    host.insertBefore(wrap, tree);
    wrap.append(toolbar, head, skeleton, stateBox, tree, bulk);
    document.body.appendChild(marquee);

    tree.tabIndex = 0;   // listbox 必须可聚焦，否则键盘用户进不来
    tree.setAttribute('role', 'listbox');
    tree.setAttribute('aria-multiselectable', 'true');
    tree.setAttribute('aria-label', '文件列表');

    // 主题
    document.documentElement.dataset.theme = st.theme;
    toolbar.querySelector('#fm-p3-themebtn').onclick = () => {
      st.theme = st.theme === 'light' ? 'dark' : 'light';
      document.documentElement.dataset.theme = st.theme;
      save();
    };

    // 视图切换
    toolbar.querySelector('#fm-p3-viewbtns').addEventListener('click', (e) => {
      const b = e.target.closest('.fm-viewbtn');
      if (!b) return;
      st.view = b.dataset.view; st.cursor = 0; save(); render();
    });

    // 列头排序：交给后端 sort/order，保持与服务端一致
    head.addEventListener('click', (e) => {
      const c = e.target.closest('.col');
      if (!c) return;
      const s = c.dataset.sort;
      if (st.sort === s) st.order = st.order === 'asc' ? 'desc' : 'asc';
      else { st.sort = s; st.order = s === 'name' ? 'asc' : 'desc'; }
      save();
      loadFiles(currentPath);
    });

    toolbar.querySelector('#fm-p3-selectall').addEventListener('change', (e) => {
      const all = visible();
      if (e.target.checked) all.forEach((f) => sel.add(f.path));
      else sel.clear();
      syncSel(); paintSelection(); renderBulk();
    });

    // 列表：事件委托。原来的 onclick="loadFiles('...')" 拼接，
    // 遇到名字里带单引号的文件（a'b.png）会直接破掉，委托顺手绕开这个雷。
    tree.addEventListener('click', (e) => {
      if (suppressClick) { suppressClick = false; return; }
      if (justDragged) return;               // 框选尾巴上的 click，不当普通选择处理
      const el = e.target.closest('.file-item');
      if (!el) return;
      const path = el.dataset.path, dir = el.dataset.dir === '1';
      st.cursor = +el.dataset.i; save();
      // 修饰键要排在勾选框分支前面：点在勾选框还是点在行上，
      // ctrl/shift 的含义应该一致。原来先判勾选框，shift+勾选框退化成普通切换。
      if (e.shiftKey && st.anchor != null) { rangeSelect(path); return; }
      if (e.ctrlKey || e.metaKey) { toggle(path, !sel.has(path)); st.anchor = path; save(); return; }
      if (e.target.classList.contains('fm-check')) {
        // 勾选框是"加选"入口：这里绝不能先清空。
        // 之前多了一句 sel.clear()，于是勾第二个框时前一个被吃掉，
        // 多选退化成"永远只能选一个"，而且没有任何提示。
        st.anchor = path; save();
        toggle(path);
        return;
      }
      st.anchor = path; save();
      if (!sel.has(path) && sel.size) sel.clear();
      syncSel(); paintSelection();
      if (dir) loadFiles(path); else openFile(path, path.split('/').pop());
    });
    tree.addEventListener('dblclick', (e) => {
      const el = e.target.closest('.file-item');
      if (el) startRename(el.dataset.path);
    });

    tree.addEventListener('mousedown', startMarquee);
    // 挂在侧栏上而不是只挂树：树是贴合内容高度的，列表铺满时树内没有空白可起手
    if (host !== tree) host.addEventListener('mousedown', startMarquee);
    tree.addEventListener('dragstart', onDragStart);
    tree.addEventListener('dragover', onDragOver);
    tree.addEventListener('drop', onDrop);
    tree.addEventListener('dragend', endDrag);

    // 批量操作条
    bulk.addEventListener('click', async (e) => {
      const b = e.target.closest('button');
      if (!b) return;
      const paths = [...sel];
      const act = b.dataset.act;
      if (act === 'clear') return clearSel();
      if (!paths.length) return;
      if (act === 'del') return doDelete(paths);
      if (act === 'open') { paths[0].endsWith('/') ? null : null; return openFile(paths[0], paths[0].split('/').pop()); }
      if (act === 'down') { paths.forEach((p) => { window.location.href = API + '/download?path=' + encPath(p); }); return; }
      if (act === 'star') {
        for (const p of paths) {
          await fetch(API + '/star', { method: 'POST', headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ path: p, starred: true }) });
        }
        toast(`已星标 ${paths.length} 项`); return loadFiles(currentPath);
      }
      if (act === 'rename') { toast('批量重命名请逐个进行'); return startRename(paths[0]); }
    });

    document.addEventListener('keydown', onKey);
  }

  // 接管原实现
  const origLoad = window.loadFiles;
  const origRender = window.renderFileTree;

  window.renderFileTree = function () {
    if (!tree) { mount(); }
    if (!tree) return origRender && origRender.apply(this, arguments);
    render();
  };
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', refreshTrashCount);
  } else {
    refreshTrashCount();
  }


  // 侧栏回收站入口显示待清理数量，0 时隐藏徽标。
  function refreshTrashCount() {
    try {
      fetch(API + '/trash').then(r => r.ok ? r.json() : []).then(list => {
        var el = document.getElementById('trashCount');
        if (!el) return;
        var n = Array.isArray(list) ? list.length : 0;
        el.textContent = String(n);
        el.hidden = n === 0;
      }).catch(function () {});
    } catch (e) { /* 回收站不可用不该影响主流程 */ }
  }

  window.loadFiles = async function (path = '', opts) {
    // 原来只靠 path !== '' 判断「显式导航」，但用户点根目录面包屑时传的也是
    // 空字符串，于是被当成迟到的默认加载直接丢弃 —— 点根目录完全无反应。
    // 现在允许调用方用 { explicit: true } 明确表示这是用户主动导航。
    const forced = !!(opts && opts.explicit);
    if (path !== '' || forced) explicitNav = true;
    else if (explicitNav) return;
    const my = ++reqSeq;
    lastError = null;
    if (!tree) mount();
    if (!tree) return origLoad && origLoad.apply(this, arguments);
    currentPath = path;
    try { sessionStorage.setItem('fm_cwd', path); } catch (_) {}
    updateBreadcrumb();
    setState('loading');
    st.cursor = 0; st.anchor = null; clearSel();
    try {
      const url = API + '/files?path=' + encPath(path) +
        '&sort=' + encodeURIComponent(st.sort) + '&order=' + encodeURIComponent(st.order);
      const res = await fetch(url);
      if (my !== reqSeq) return;            // 已被更新的请求取代，别再画
      if (!res.ok) throw new Error('HTTP ' + res.status);
      const data = await res.json();
      if (!Array.isArray(data)) throw new Error('bad payload');
      files = data;
      st.cursor = 0;
      render();
    } catch (e) {
      if (my !== reqSeq) return;            // 同上：过期的失败不该改 UI
      files = [];
      tree.innerHTML = '';
      tree.hidden = true;
      setState('error', '打不开这个文件夹：' + e.message);
    }
  };

  document.addEventListener('DOMContentLoaded', mount);
  if (document.readyState !== 'loading') mount();

  window.FM_P3 = { st, get tree() { return tree; }, render, visible, startRename, doDelete, doMove, toast };
})();
