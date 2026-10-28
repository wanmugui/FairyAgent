// 外部文件/文件夹拖拽上传。
//
// 现状问题：项目里只有 fm-p3.js 的"站内拖动"（拖到文件夹上移动文件），
// 全项目没有任何一处读 dataTransfer.files，所以从桌面拖文件进页面毫无反应。
//
// 这里补上：窗口级拖拽侦测 -> 投放遮罩 -> 递归展开目录 -> 复用 fm-upload.js
// 已有的分片上传管线 __fmChunkedUpload，不重写上传逻辑。
//
// 关键点：必须与站内拖动共存。站内移动只带 text/plain，外部拖入才带 Files，
// 两者靠 dataTransfer.types 区分，互不干扰。
(function () {
  'use strict';

  // 幂等锁：这份代码可能既被 <script src> 引入、又被内联进 HTML，
  // 装两遍会导致遮罩叠加、事件处理跑两次。
  if (window.__fmDropInstalled) return;
  window.__fmDropInstalled = true;

  // 注意不能写成 const API = (typeof API !== 'undefined' ? API : '/api')：
  // 局部 const 会遮蔽全局 API，在自己的初始化表达式里读它会踩进暂时性死区，
  // 直接抛 ReferenceError: Cannot access 'API' before initialization。
  const FM_API = (typeof API !== 'undefined' ? API : '/api');
  const MAX_CONCURRENCY = 8;

  var MOVE_TYPE = 'application/x-fm-move';

  // 判定"外部文件拖入"。
  // 不能只认 dataTransfer.types 里的 'Files'：部分桌面 webview 在 dragover 阶段
  // 根本不填 types，导致整段逻辑被跳过（表现为拖上去毫无反应、也没遮罩）。
  // 站内移动由 fm-p3.js 打上 MOVE_TYPE 标记，据此排除即可。
  function isExternalDrag(e) {
    var dt = e && e.dataTransfer;
    if (!dt) return false;
    var t = dt.types ? Array.prototype.slice.call(dt.types) : [];
    if (t.indexOf(MOVE_TYPE) !== -1) return false;          // 站内移动
    if (t.indexOf('Files') !== -1) return true;
    if (dt.items && dt.items.length) return true;
    if (dt.files && dt.files.length) return true;
    return false;
  }

  // ---------------------------------------------------------------- 遮罩
  // 视觉对齐 agent 前端的 .chat-drop-hint：同一套主题变量、虚线描边、
  // 圆角胶囊，而不是另造一个通用弹窗。用户说"要和我们 agent 前端一样"。
  function themeVar(name, fallback) {
    const v = getComputedStyle(document.documentElement).getPropertyValue(name);
    return (v && v.trim()) || fallback;
  }

  function paintOverlay() {
    const accent = themeVar('--ui-accent', '#6366f1');
    const soft = themeVar('--ui-accent-soft', 'rgba(99,102,241,.10)');
    const line = themeVar('--ui-line', 'rgba(15,23,42,.12)');
    const bg = themeVar('--ui-panel-solid', 'rgba(255,255,255,.96)');
    const text = themeVar('--ui-text-2', '#475569');
    const isDark = (themeVar('--ui-bg', '#fff') || '').trim() !== '#fff';
    const s = overlay.style;
    s.background = isDark ? 'rgba(2,6,23,.55)' : 'rgba(15,23,42,.28)';
    overlay.innerHTML =
      '<div class="fm-drop-hint" style="border:1.5px dashed ' + accent
      + ';background:' + soft + ';color:' + accent
      + ';border-radius:18px;padding:26px 44px;text-align:center;'
      + 'font:600 15px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;'
      + 'box-shadow:0 8px 32px rgba(0,0,0,.18);backdrop-filter:blur(2px)">'
      + '<div style="font-size:15px;margin-bottom:6px">松开即可上传文件</div>'
      + '<div style="font-size:12px;font-weight:400;color:' + text
      + ';opacity:.85">文件与文件夹都会上传到「' + ((typeof currentPath !== 'undefined' && currentPath) || '此电脑')
      + '」</div></div>';
    void bg; void line;
  }

  const overlay = document.createElement('div');
  overlay.id = 'fm-drop-overlay';
  overlay.style.cssText = [
    'position:fixed', 'inset:0', 'z-index:10000',
    'display:none', 'align-items:center', 'justify-content:center',
    'pointer-events:none',
  ].join(';');
  paintOverlay();
  // 本模块可能由异步注入器加载，此时 DOMContentLoaded 早已触发完，
  // 只挂监听会永远不执行（曾表现为遮罩不进 DOM、拖拽无视觉反馈）。
  function mountOverlay() {
    if (document.getElementById('fm-drop-overlay')) return;
    document.body.appendChild(overlay);
    notifyReady();
    const bc = document.getElementById('breadcrumb');
    if (bc) new MutationObserver(paintOverlay).observe(bc, { childList: true, subtree: true });
  }
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', mountOverlay);
  } else {
    mountOverlay();
  }

  // 加载即提示：这既是功能提示，也是"模块到底有没有装上"的判据。
  // 之前若外链/内联任一环节失败，页面毫无痕迹，无法区分"没装"和"没识别到拖拽"。
  function notifyReady() {
    if (sessionStorage.getItem('fmDropNotified')) return;
    sessionStorage.setItem('fmDropNotified', '1');
    const t = document.createElement('div');
    t.textContent = '拖拽上传已就绪：可直接把文件或文件夹拖到本页面上传';
    t.style.cssText = [
      'position:fixed', 'left:50%', 'top:16px', 'transform:translateX(-50%)',
      'z-index:10002', 'background:#1f2937', 'color:#fff', 'font-size:13px',
      'padding:9px 16px', 'border-radius:8px', 'box-shadow:0 6px 20px rgba(0,0,0,.28)',
      'transition:opacity .3s ease', 'pointer-events:none',
    ].join(';');
    document.body.appendChild(t);
    setTimeout(() => { t.style.opacity = '0'; }, 3600);
    setTimeout(() => t.remove(), 4000);
  }

  function showOverlay() { paintOverlay(); overlay.style.display = 'flex'; }
  function hideOverlay() { overlay.style.display = 'none'; }

  // dragenter/dragleave 在子元素间穿梭时会疯狂触发，用计数器抵消。
  let dragDepth = 0;
  window.addEventListener('dragenter', (e) => {
    if (!isExternalDrag(e)) return;
    e.preventDefault();
    dragDepth++;
    showOverlay();
  }, true);

  window.addEventListener('dragover', (e) => {
    if (!isExternalDrag(e)) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'copy';
  }, true);

  window.addEventListener('dragleave', (e) => {
    if (!isExternalDrag(e)) return;
    dragDepth = Math.max(0, dragDepth - 1);
    if (dragDepth === 0) hideOverlay();
  }, true);

  window.addEventListener('drop', (e) => {
    if (!isExternalDrag(e)) return;   // 站内移动交给 fm-p3.js
    e.preventDefault();
    e.stopPropagation();
    dragDepth = 0;
    hideOverlay();
    const dt = e.dataTransfer;
    const items = dt.items ? Array.from(dt.items) : [];
    const entries = items
      .filter((it) => it.kind === 'file')
      .map((it) => (it.webkitGetAsEntry ? it.webkitGetAsEntry() : null));
    const hasEntry = entries.some(Boolean);
    if (hasEntry) {
      collectEntries(entries.filter(Boolean), []).then((list) => startUpload(list));
    } else {
      // 老浏览器拿不到 entry，退回扁平 File 列表
      startUpload(Array.from(dt.files || []).map((f) => ({ file: f, rel: f.name })));
    }
  }, true);

  // ------------------------------------------------------- 递归展开目录
  function readBatch(reader) {
    return new Promise((res, rej) => reader.readEntries(res, rej));
  }

  // readEntries 每次最多返回 100 条，必须循环读到返回空数组为止。
  async function walkEntry(entry, prefix, out) {
    if (entry.isFile) {
      const file = await new Promise((res, rej) => entry.file(res, rej));
      out.push({ file: file, rel: prefix ? prefix + '/' + entry.name : entry.name });
      return;
    }
    if (!entry.isDirectory) return;
    const next = prefix ? prefix + '/' + entry.name : entry.name;
    const reader = entry.createReader();
    for (;;) {
      const batch = await readBatch(reader);
      if (!batch || !batch.length) break;
      for (const child of batch) await walkEntry(child, next, out);
    }
  }

  async function collectEntries(entries, out) {
    for (const entry of entries) {
      try {
        await walkEntry(entry, '', out);
      } catch (err) {
        // 单个条目读失败不能中断整批
        console.warn('[fm-drop] 读取条目失败', entry && entry.name, err);
      }
    }
    return out;
  }

  // ------------------------------------------------------------ 进度面板
  function buildPanel(total) {
    const panel = document.createElement('div');
    panel.id = 'fm-drop-panel';
    panel.style.cssText = [
      'position:fixed', 'right:16px', 'bottom:16px', 'z-index:10001',
      // 与 index-fm.html 保持一致：横向滚动条来自 overflow:auto（它同时开 x/y），
      // 这里只需要纵向滚动。box-sizing 防止 padding 把实际占位撑出声明宽度。
      'width:min(380px,calc(100vw - 24px))', 'max-height:60vh',
      'overflow-y:auto', 'overflow-x:hidden', 'box-sizing:border-box',
      'background:#fff', 'border-radius:10px', 'padding:12px 14px',
      'box-shadow:0 8px 28px rgba(0,0,0,.22)',
      'font:12px/1.5 -apple-system,BlinkMacSystemFont,Segoe UI,sans-serif',
      'color:#1f2937',
    ].join(';');
    panel.innerHTML = '<div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:8px">'
      + '<b style="font-size:13px">上传中…</b><span id="fm-drop-count" style="color:#6b7280">0/' + total + '</span></div>';
    document.body.appendChild(panel);
    return panel;
  }

  function addRow(panel, rel) {
    const row = document.createElement('div');
    row.style.cssText = 'margin-top:8px';
    row.innerHTML = '<div style="display:flex;justify-content:space-between;gap:8px">'
      // 文件名必须能收缩（flex:1 1 auto + min-width:0），
      // 否则长文件会把进度文本挤出面板，用户只看到尾部残留。
      + '<span class="n" style="flex:1 1 auto;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap"></span>'
      + '<span class="p" style="color:#6b7280;flex:0 0 auto;white-space:nowrap;font-variant-numeric:tabular-nums">等待</span></div>'
      + '<div class="bar" style="height:4px;border-radius:2px;background:#e5e7eb;margin-top:4px;overflow:hidden">'
      + '<div class="fill" style="height:100%;width:0;background:#6366f1;transition:width .15s"></div></div>';
    row.querySelector('.n').textContent = rel;
    row.querySelector('.n').title = rel;
    panel.appendChild(row);
    return {
      set(pct, text, bad) {
        row.querySelector('.fill').style.width = Math.max(0, Math.min(100, pct)) + '%';
        row.querySelector('.fill').style.background = bad ? '#dc2626' : '#6366f1';
        const p = row.querySelector('.p');
        p.textContent = text;
        p.style.color = bad ? '#dc2626' : '#6b7280';
      },
    };
  }

  function human(n) {
    if (!n && n !== 0) return '';
    const u = ['B', 'KB', 'MB', 'GB', 'TB'];
    let v = n, i = 0;
    while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
    return (v >= 10 || i === 0 ? v.toFixed(0) : v.toFixed(1)) + ' ' + u[i];
  }

  // 同名不静默覆盖：先看目标目录现有名字，逐个加 "(1)" 后缀。
  async function listNames(dir) {
    try {
      const res = await fetch(FM_API + '/files?path=' + encodeURIComponent(dir));
      if (!res.ok) return new Set();
      const j = await res.json();
      return new Set((j || []).map((e) => e.name));
    } catch (e) {
      return new Set();
    }
  }

  function joinPath(base, rel) {
    if (!rel) return base;
    return (base ? base + '/' : '/') + rel;
  }

  // ------------------------------------------------------------- 上传
  async function startUpload(list) {
    if (!list || !list.length) return;
    const upload = window.__fmChunkedUpload;
    if (typeof upload !== 'function') {
      alert('上传组件未就绪，请刷新页面后重试');
      return;
    }
    const base = (typeof currentPath !== 'undefined' && currentPath) ? currentPath : '';
    const panel = buildPanel(list.length);
    const counter = panel.querySelector('#fm-drop-count');
    let done = 0;
    // 中断：AbortController 同时掐掉在途请求与待传队列。
    // 只掐队列的话，已经发出的那几个并发分片还会跑完，点了取消也得干等。
    const abort = new AbortController();
    let cancelled = false;
    const cancelBtn = document.createElement('button');
    cancelBtn.textContent = '取消';
    cancelBtn.className = 'fm-drop-cancel';
    cancelBtn.style.cssText = 'margin-left:auto;padding:1px 8px;font:inherit;cursor:pointer;' +
      'border:1px solid #c9ced6;border-radius:4px;background:#fff;color:#333;';
    cancelBtn.addEventListener('click', () => {
      if (cancelled) return;
      cancelled = true;
      abort.abort();
      cancelBtn.textContent = '已取消';
      cancelBtn.disabled = true;
    });
    const head = counter && counter.parentElement;
    if (head) head.appendChild(cancelBtn);

    // 同一个文件夹内的文件共用一份"已占用名字"集合，避免并发下重名撞车。
    const perDir = new Map();

    // 【必须先建目录】拖进来的是一整个文件夹，item.rel 里带着完整的相对层级。
    // 服务端两条上传路径对"目录不存在"的容忍度不一样：
    //   - 分片上传：initSession 里 mkdirSync(recursive) 会顺手把目录建出来；
    //   - 小文件直传 PUT /api/upload：硬性要求目录已存在，否则 400
    //     "target path is not a directory"。
    // 于是同一批文件里只有小文件失败、大文件成功，看起来像"随机坏了"。
    // 这里在上传前把缺失的目录补齐，把两条路径的行为对齐。
    //
    // 必须逐段建：/api/folder 的 name 会过 validateName，含路径分隔符直接 400。
    const madeDirs = new Set(['']);
    if (base) madeDirs.add(base);

    async function ensureDir(dir) {
      if (!dir || madeDirs.has(dir)) return;
      const slash = dir.lastIndexOf('/');
      if (slash === -1) return;               // 顶层目录由用户保证存在
      const parent = slash === 0 ? '' : dir.slice(0, slash);
      await ensureDir(parent);
      const name = dir.slice(slash + 1);
      if (madeDirs.has(dir)) return;
      // 先登记再发请求：并发 worker 同时命中同一目录时只发一次。
      // 失败再摘掉，否则一次网络抖动会让该目录在这一批里永久失效。
      madeDirs.add(dir);
      try {
        const res = await fetch(FM_API + '/folder', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ path: parent, name: name }),
        });
        if (!res.ok) {
          const j = await res.json().catch(() => null);
          throw new Error((j && j.error) || ('建目录失败 ' + res.status));
        }
      } catch (e) {
        madeDirs.delete(dir);
        throw e;
      }
    }
    // 返回"去重后的纯文件名"，不含目录。
    async function reserveName(dir, fileName) {
      if (!perDir.has(dir)) perDir.set(dir, await listNames(dir));
      const used = perDir.get(dir);
      const dot = fileName.lastIndexOf('.');
      const stem = dot > 0 ? fileName.slice(0, dot) : fileName;
      const ext = dot > 0 ? fileName.slice(dot) : '';
      let candidate = fileName, n = 1;
      while (used.has(candidate)) { candidate = stem + ' (' + n + ')' + ext; n++; }
      used.add(candidate);
      return candidate;
    }

    const queue = list.slice();
    async function worker() {
      for (;;) {
        if (cancelled) return;          // 已取消：不再领取新任务
        const item = queue.shift();
        if (!item) return;
        const row = addRow(panel, item.rel);
        try {
          const slash = item.rel.lastIndexOf('/');
          const relDir = slash === -1 ? '' : item.rel.slice(0, slash);
          const baseName = slash === -1 ? item.rel : item.rel.slice(slash + 1);
          const dir = joinPath(base, relDir);
          // 目录不存在时小文件直传会 400；先补齐，再谈上传。
          await ensureDir(dir);
          // 去重只针对文件名；目录单独传给上传接口。若把 '子目录/文件.txt'
          // 整个塞进 File.name，上传接口会再拼一次 dir + '/' + file.name，
          // 路径会重复成 '子目录/子目录/文件.txt'。
          const name = await reserveName(dir, baseName);
          // 传改名后的 File；lastModified 是属性不是方法。
          const f = name === item.file.name
            ? item.file
            : new File([item.file], name, { type: item.file.type, lastModified: item.file.lastModified });
          row.set(0, '0.0% · 0 B / ' + human(item.file.size));
          // 第三个参数是旧浮层的进度元素，这里传 null；面板这一行走 onProgress 回调。
          // 之前也传了 null，但没给回调，于是进度无处可去——面板那行永远是初始的
          // 「0% · 3.5 GB」，直到整个文件传完才跳到 ✓。大文件看着就像卡死了。
          const r = await upload(f, dir, null, abort.signal, (p) => {
            // 已完成才落到 100%，否则多路并发时进度条会因乱序完成而回退。
            const pct = Math.min(99.9, p.pct);
            row.set(pct, p.bare);
          });
          if (r && r.ok) {
            row.set(100, '✓ ' + human(item.file.size), false);
          } else {
            row.set(100, '失败：' + ((r && r.error) || '未知'), true);
          }
        } catch (err) {
          if (cancelled) row.set(100, '已取消', false);
          else row.set(100, '失败：' + (err && err.message || err), true);
        }
        done++;
        counter.textContent = done + '/' + list.length;
      }
    }

    const n = Math.min(MAX_CONCURRENCY, list.length);
    await Promise.all(Array.from({ length: n }, worker));
    if (typeof loadFiles === 'function') loadFiles();
    if (cancelled) {
      const title = panel.querySelector('b');
      if (title) title.textContent = '已取消';
      cancelBtn.remove();
    }
    setTimeout(() => panel.remove(), cancelled ? 6000 : 4000);
  }

  window.__fmDropUpload = startUpload;  // 供测试直接驱动
})();
