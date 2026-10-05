import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  ArrowLeft,
  ArrowRight,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  Download,
  ExternalLink,
  Expand,
  File,
  FileCode2,
  FileImage,
  FileSpreadsheet,
  FileText,
  Folder,
  FolderOpen,
  FolderTree,
  Globe2,
  Loader2,
  Plus,
  Presentation,
  RefreshCw,
  Search,
  Shrink,
  X,
} from 'lucide-react';
import { renderReportHtml } from '../utils/reportHtml';
import { isMarkdownArtifactPath } from '../utils/reportArtifacts';
import { uploadFile } from '../api/chat';

const CODE_EXTS = new Set(['js', 'jsx', 'ts', 'tsx', 'mjs', 'cjs', 'go', 'rs', 'py', 'java', 'c', 'cpp', 'h', 'hpp', 'json', 'css', 'html', 'xml', 'yml', 'yaml', 'toml', 'sh', 'ps1']);

function nameOf(pathValue) {
  return String(pathValue || '').split(/[\\/]/).pop() || '文件';
}

function extensionOf(pathValue) {
  const name = nameOf(pathValue);
  const index = name.lastIndexOf('.');
  return index >= 0 ? name.slice(index + 1).toLowerCase() : '';
}

function samePath(left, right) {
  return String(left || '').replace(/\\/g, '/').toLowerCase()
    === String(right || '').replace(/\\/g, '/').toLowerCase();
}

function reportTabHash(text) {
  let hash = 2166136261;
  const value = String(text || '');
  for (let index = 0; index < value.length; index += 1) {
    hash ^= value.charCodeAt(index);
    hash = Math.imul(hash, 16777619);
  }
  return (hash >>> 0).toString(36);
}

function fileTabId(file) {
  if (file && file.kind === 'report') {
    return 'report:' + (file.path || ((file.name || '报告详情') + ':' + reportTabHash(file.bodyText)));
  }
  if (file && file.kind === 'browser-live') return 'browser-live:' + (file.page_url || file.live_url || '');
  if (file && file.kind === 'ppt') return 'ppt:' + (file.path || '');
  if (file && file.kind === 'workspace') return 'workspace:files';
  return 'file:' + ((file && file.path) || '');
}

function fileTabName(file) {
  if (file && file.kind === 'report') return file.name || '报告详情';
  if (file && file.kind === 'browser-live') return file.name || '浏览器实时页面';
  if (file && file.kind === 'ppt') return file.name || 'PPT 演示';
  if (file && file.kind === 'workspace') return file.name || '文件';
  return nameOf(file && file.path);
}

function fileKindLabel(file) {
  if (file && file.kind === 'browser-live') return 'LIVE';
  if (file && file.kind === 'report') return 'REPORT';
  if (file && file.kind === 'ppt') return 'SLIDES';
  if (file && file.kind === 'workspace') return 'FILES';
  if (file && isImageFile(file)) return 'IMAGE';
  return 'FILE';
}

function fileContextDetail(file) {
  if (!file) return '';
  if (file.kind === 'browser-live') return file.page_url || file.live_url || '';
  return file.path || '';
}

function ViewerIcon({ file, size = 14, className = '' }) {
  const props = { size, strokeWidth: 1.8, className, 'aria-hidden': true };
  const ext = extensionOf(file && file.path);
  if (file && file.kind === 'browser-live') return <Globe2 {...props} />;
  if (file && file.kind === 'report') return <FileText {...props} />;
  if (file && file.kind === 'ppt') return <Presentation {...props} />;
  if (file && file.kind === 'workspace') return <FolderOpen {...props} />;
  if (file && isImageFile(file)) return <FileImage {...props} />;
  if (ext === 'xlsx' || ext === 'xls' || ext === 'csv') return <FileSpreadsheet {...props} />;
  if (CODE_EXTS.has(ext)) return <FileCode2 {...props} />;
  return <File {...props} />;
}

export function fileApiUrl(pathValue, opts) {
  const query = new URLSearchParams({ path: pathValue || '' });
  if (opts && opts.download) query.set('download', '1');
  if (opts && opts.version != null && opts.version !== '') query.set('v', String(opts.version));
  return '/api/file-content?' + query.toString();
}

function viewerUrl(pathValue) {
  const query = new URLSearchParams({
    file: pathValue || '',
    embedded: '1',
  });
  return '/viewer.html?' + query.toString();
}

function pptPreviewUrl(pathValue) {
  return '/api/ppt-preview?deck_dir=' + encodeURIComponent(pathValue || '');
}

const IMAGE_EXTS = new Set(['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'bmp', 'avif']);

function isImageFile(file) {
  if (file && file.kind === 'image') return true;
  return IMAGE_EXTS.has(extensionOf(file && file.path));
}

function WorkspaceTree({ onOpenFile, activePath }) {
  const [root, setRoot] = useState('');
  const [childrenByPath, setChildrenByPath] = useState({});
  const [expanded, setExpanded] = useState(() => new Set());
  const [loading, setLoading] = useState(new Set());
  const [error, setError] = useState('');
  const [query, setQuery] = useState('');

  const loadDirectory = useCallback(async (pathValue, force = false) => {
    if (!pathValue || (!force && childrenByPath[pathValue]) || loading.has(pathValue)) return;
    setLoading(previous => new Set(previous).add(pathValue));
    try {
      const response = await fetch('/api/files?path=' + encodeURIComponent(pathValue));
      const rows = await response.json().catch(() => []);
      const normalized = Array.isArray(rows) ? rows.map(item => ({
        ...item,
        openPath: /^[A-Za-z]:[\\/]/.test(String(item.path || '')) || String(item.path || '').startsWith('/')
          ? item.path
          : root + '/' + String(item.path || '').replace(/^\/+/, ''),
      })) : [];
      setChildrenByPath(previous => ({ ...previous, [pathValue]: normalized }));
      setError('');
    } catch {
      setError('无法读取目录');
    } finally {
      setLoading(previous => {
        const next = new Set(previous);
        next.delete(pathValue);
        return next;
      });
    }
  }, [childrenByPath, loading, root]);

  useEffect(() => {
    let cancelled = false;
    fetch('/api/workspace-root')
      .then(response => response.json())
      .then(payload => {
        if (!cancelled && payload && payload.root) setRoot(String(payload.root));
      })
      .catch(() => {});
    return () => { cancelled = true; };
  }, []);

  useEffect(() => {
    if (!root) return;
    setExpanded(previous => new Set(previous).add(root));
    loadDirectory(root);
  }, [root, loadDirectory]);

  const toggle = async item => {
    if (!item.isDirectory) {
      onOpenFile({ path: item.openPath || item.path, name: item.name });
      return;
    }
    const pathValue = item.path;
    const isOpen = expanded.has(pathValue);
    setExpanded(previous => {
      const next = new Set(previous);
      if (next.has(pathValue)) next.delete(pathValue);
      else next.add(pathValue);
      return next;
    });
    if (!isOpen && !childrenByPath[pathValue]) await loadDirectory(pathValue);
  };

  const flattenLoaded = useCallback((pathValue, depth = 0, output = []) => {
    const rows = childrenByPath[pathValue] || [];
    for (const item of rows) {
      output.push({ item, depth });
      if (item.isDirectory && childrenByPath[item.path]) flattenLoaded(item.path, depth + 1, output);
    }
    return output;
  }, [childrenByPath]);

  const searchRows = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    if (!normalized) return null;
    return flattenLoaded(root).filter(({ item }) => String(item.name || '').toLowerCase().includes(normalized));
  }, [flattenLoaded, query, root]);

  const renderRow = (item, depth = 0) => {
    const open = expanded.has(item.path);
    const active = samePath(item.openPath || item.path, activePath);
    return (
      <React.Fragment key={item.path}>
        <button
          type="button"
          className={'file-tree-row' + (item.isDirectory ? ' is-dir' : '') + (active ? ' is-active' : '')}
          style={{ paddingLeft: (8 + depth * 14) + 'px' }}
          title={item.path}
          data-active={active ? 'true' : undefined}
          onClick={() => toggle(item)}
        >
          <span className="file-tree-chevron">
            {item.isDirectory
              ? (open ? <ChevronDown size={13} strokeWidth={1.8} /> : <ChevronRight size={13} strokeWidth={1.8} />)
              : null}
          </span>
          <span className="file-tree-icon">
            {item.isDirectory
              ? (open ? <FolderOpen size={14} strokeWidth={1.8} /> : <Folder size={14} strokeWidth={1.8} />)
              : <File size={14} strokeWidth={1.8} />}
          </span>
          <span className="file-tree-name">{item.name}</span>
        </button>
        {item.isDirectory && open && renderRows(item.path, depth + 1)}
      </React.Fragment>
    );
  };

  const renderRows = (pathValue, depth = 0) => (
    (childrenByPath[pathValue] || []).map(item => renderRow(item, depth))
  );

  const rootLabel = root ? root.split(/[\\/]/).filter(Boolean).pop() : 'Workspace';

  return (
    <aside className="file-tree-pane" aria-label="工作区文件">
      <div className="file-tree-head">
        <span className="file-tree-title"><FolderTree size={14} />{rootLabel || 'Workspace'}</span>
        <button type="button" onClick={() => loadDirectory(root, true)} title="刷新目录" aria-label="刷新目录">
          <RefreshCw size={13} className={loading.has(root) ? 'is-spinning' : ''} />
        </button>
      </div>
      <label className="file-tree-search">
        <Search size={13} aria-hidden="true" />
        <input
          value={query}
          onChange={event => setQuery(event.target.value)}
          placeholder="筛选文件"
          spellCheck={false}
        />
        {query && <button type="button" onClick={() => setQuery('')} aria-label="清空筛选"><X size={12} /></button>}
      </label>
      <div className="file-tree-scroll">
        {searchRows
          ? searchRows.map(({ item, depth }) => renderRow(item, depth))
          : renderRows(root)}
        {loading.size > 0 && <div className="file-tree-loading"><Loader2 size={13} />读取中…</div>}
        {error && <div className="file-tree-error">{error}</div>}
        {!loading.size && !error && searchRows && searchRows.length === 0 && (
          <div className="file-tree-loading">没有匹配的文件</div>
        )}
      </div>
    </aside>
  );
}

function normalizeBrowserAddress(value) {
  const raw = String(value || '').trim();
  if (!raw) return '';
  if (/^(https?|file|data|about):/i.test(raw)) return raw;
  if (/^[a-zA-Z]:[\\/]/.test(raw)) return 'file:///' + raw.replace(/\\/g, '/');
  if (/^(localhost|127\.0\.0\.1)(?::\d+)?(?:\/|$)/i.test(raw)) return 'http://' + raw;
  return 'https://' + raw;
}

function isLoopbackHost(host) {
  const value = String(host || '').toLowerCase();
  return value === 'localhost' || value === '127.0.0.1' || value === '::1' || value === '[::1]';
}

function isFairyAppAddress(value) {
  if (!value || typeof window === 'undefined') return false;
  try {
    const target = new URL(value, window.location.href);
    const current = new URL(window.location.href);
    // "Same origin as the page I am running in" is the real test. Requiring the
    // target to be loopback locked the embedded viewer to desktop use: opened
    // over the LAN, every /viewer.html link resolved to a non-loopback host and
    // was treated as a foreign site, so previews stopped working on a tablet
    // even though the link itself was relative and perfectly valid.
    const sameHost = target.hostname === current.hostname ||
      (isLoopbackHost(target.hostname) && isLoopbackHost(current.hostname));
    if (!sameHost) return false;
    const targetPort = String(target.port || '');
    const currentPort = String(current.port || '');
    const knownFairyPort = ['5173', '5174'].includes(targetPort);
    if (!knownFairyPort && targetPort !== currentPort) return false;
    if (target.pathname.startsWith('/api/')) return false;
    return true;
  } catch {
    return false;
  }
}

function withEmbeddedFairyFlag(value) {
  if (!isFairyAppAddress(value)) return value;
  try {
    const target = new URL(value, window.location.href);
    target.searchParams.set('__fairy_embed', '1');
    return target.toString();
  } catch {
    return value;
  }
}

function BrowserLiveView({ file }) {
  // streamUrl 是 agent 抓的本机 Playwright 真实像素(MJPEG)，优先使用。
  // page_url 只是"让观者自己再打开一次这个地址"，观者不在本机时连不上，
  // 所以它降级为地址栏/回退 iframe，不再抢占渲染优先位。
  const streamUrl = file.live_url || '';
  const initialUrl = file.page_url || file.live_url || '';
  const [address, setAddress] = useState(initialUrl);
  const [frameUrl, setFrameUrl] = useState(initialUrl);
  const [history, setHistory] = useState(initialUrl ? [initialUrl] : []);
  const [historyIndex, setHistoryIndex] = useState(initialUrl ? 0 : -1);
  const [reloadKey, setReloadKey] = useState(0);
  const [status, setStatus] = useState(streamUrl ? '实时画面' : (initialUrl ? '载入中' : '没有页面地址'));
  const canOpenExternally = /^(https?|file):/i.test(frameUrl);
  const renderUrl = useMemo(() => withEmbeddedFairyFlag(frameUrl), [frameUrl]);

  // 缩放适配：预览面板通常只有 700~900px 宽，页面直接塞进去会吃到 760/720/700px
  // 这些移动端断点，布局被重排成「残的」。这里固定按桌面宽度渲染再整体缩放，
  // 看到的才是页面真实的桌面版式。
  const VIEWPORT_W = 1440;
  const surfaceRef = useRef(null);
  const [box, setBox] = useState({ w: 0, h: 0 });
  const [fitMode, setFitMode] = useState(true);

  useEffect(() => {
    const el = surfaceRef.current;
    if (!el) return undefined;
    const read = () => setBox({ w: el.clientWidth, h: el.clientHeight });
    read();
    if (typeof ResizeObserver === 'undefined') {
      window.addEventListener('resize', read);
      return () => window.removeEventListener('resize', read);
    }
    const observer = new ResizeObserver(read);
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  const scale = fitMode && box.w > 0 ? Math.min(1, (box.w - 2) / VIEWPORT_W) : 1;
  const frameW = fitMode ? VIEWPORT_W : Math.max(320, box.w || VIEWPORT_W);
  const frameH = box.h > 0 ? Math.round(box.h / scale) : 900;

  useEffect(() => {
    if (!initialUrl) return;
    setAddress(initialUrl);
    setFrameUrl(initialUrl);
    setHistory([initialUrl]);
    setHistoryIndex(0);
    // MJPEG 是持续连接，"载入中"永远不会结束，状态会一直挂着。
    // 只有在没有实时流时才提示载入中。
    setStatus(streamUrl ? '实时画面' : '载入中');
  }, [initialUrl, file.version, streamUrl]);

  const navigate = target => {
    if (!target || target === frameUrl) return;
    const base = history.slice(0, historyIndex + 1);
    const nextHistory = base[base.length - 1] === target ? base : [...base, target].slice(-50);
    setHistory(nextHistory);
    setHistoryIndex(nextHistory.length - 1);
    setAddress(target);
    setFrameUrl(target);
    setStatus('载入中');
  };

  const submitAddress = event => {
    event.preventDefault();
    navigate(normalizeBrowserAddress(address));
  };

  const goBack = () => {
    if (historyIndex <= 0) return;
    const nextIndex = historyIndex - 1;
    setHistoryIndex(nextIndex);
    setFrameUrl(history[nextIndex]);
    setAddress(history[nextIndex]);
    setStatus('载入中');
  };

  const goForward = () => {
    if (historyIndex >= history.length - 1) return;
    const nextIndex = historyIndex + 1;
    setHistoryIndex(nextIndex);
    setFrameUrl(history[nextIndex]);
    setAddress(history[nextIndex]);
    setStatus('载入中');
  };

  const reload = () => {
    if (!frameUrl) return;
    setReloadKey(value => value + 1);
    setStatus('刷新中');
  };

  return (
    <div className="browser-live-panel">
      <div className="browser-live-toolbar">
        <div className="browser-live-nav">
          <button type="button" title="后退" aria-label="后退" disabled={historyIndex <= 0} onClick={goBack}><ArrowLeft size={14} /></button>
          <button type="button" title="前进" aria-label="前进" disabled={historyIndex >= history.length - 1} onClick={goForward}><ArrowRight size={14} /></button>
          <button type="button" title="刷新" aria-label="刷新" disabled={!frameUrl} onClick={reload}><RefreshCw size={14} /></button>
        </div>
        <form className="browser-live-address" onSubmit={submitAddress}>
          <Globe2 size={14} aria-hidden="true" />
          <input
            value={address}
            onChange={event => setAddress(event.target.value)}
            spellCheck={false}
            aria-label="地址"
            placeholder="https://example.com 或本地 HTML 路径"
          />
          <button type="submit" disabled={!address.trim()}>打开</button>
        </form>
        <button
          type="button"
          className="file-panel-btn browser-live-fit"
          onClick={() => setFitMode(value => !value)}
          title={fitMode ? '缩放适配（按 1440px 桌面宽度渲染），点击切换 1:1' : '1:1 原始尺寸，点击切换缩放适配'}
          aria-label={fitMode ? '切换到 1:1 原始尺寸' : '切换到缩放适配'}
        >{fitMode ? <Shrink size={15} /> : <Expand size={15} />}</button>
        {canOpenExternally && (
          <a className="browser-live-tool" href={frameUrl} target="_blank" rel="noreferrer" title="在浏览器中打开" aria-label="在浏览器中打开">
            <ExternalLink size={14} />
          </a>
        )}
        <span className="browser-live-status" title={status}>
          {status === '载入中' && <Loader2 size={12} className="is-spinning" />}
          {status || '就绪'}
        </span>
      </div>
      <div className="browser-live-surface" ref={surfaceRef}>
        <div
          className="browser-live-canvas"
          style={{ width: Math.round(frameW * scale), height: Math.max(0, Math.round(frameH * scale)) }}
        >
          {streamUrl ? (
            // 远端真实画面：agent 的 Playwright 用 CDP screencast 抓帧，
            // 由 server.cjs 推 MJPEG。这是本机浏览器的像素。
            // 不用 iframe 是因为 iframe 只会让观者自己去开那个地址，
            // 观者不在本机时必然连不上，而且看到的东西与实际被操作的页面无关。
            <img
              key={streamUrl + ':' + reloadKey}
              className="browser-live-frame"
              style={{
                flex: '0 0 auto',
                width: frameW,
                height: frameH,
                objectFit: 'contain',
                background: '#0b0d10',
                transform: scale === 1 ? 'none' : 'scale(' + scale + ')',
              }}
              src={streamUrl}
              alt={file.name || '浏览器实时画面'}
              onLoad={() => setStatus('实时画面')}
            />
          ) : renderUrl ? (
            <iframe
              key={renderUrl + ':' + reloadKey}
              className="browser-live-frame"
              style={{
                flex: '0 0 auto',
                width: frameW,
                height: frameH,
                transform: scale === 1 ? 'none' : 'scale(' + scale + ')',
              }}
              src={renderUrl}
              title={file.name || '浏览器页面'}
              onLoad={() => setStatus('已载入')}
              allow="clipboard-read; clipboard-write; fullscreen"
            />
          ) : (
            <div className="file-panel-hint">没有可打开的页面地址</div>
          )}
        </div>
      </div>
    </div>
  );
}

export default function FilePreviewPanel({ file: incomingFile, onClose, onOpenFile, sessionName }) {
  const initialTab = incomingFile ? { id: fileTabId(incomingFile), file: incomingFile } : null;
  const [tabs, setTabs] = useState(initialTab ? [initialTab] : []);
  const [activeTabId, setActiveTabId] = useState(initialTab ? initialTab.id : '');
  const [treeOpen, setTreeOpen] = useState(Boolean(incomingFile && incomingFile.kind === 'workspace'));
  const [panelCollapsed, setPanelCollapsed] = useState(false);
  const [plusOpen, setPlusOpen] = useState(false);
  const [panelWidth, setPanelWidth] = useState(null);
  const [resizing, setResizing] = useState(false);
  const plusRef = useRef(null);
  const fileInputRef = useRef(null);
  const panelRef = useRef(null);

  // 点空白处收起加号菜单
  useEffect(() => {
    if (!plusOpen) return undefined;
    const onDocDown = event => {
      if (plusRef.current && !plusRef.current.contains(event.target)) setPlusOpen(false);
    };
    const onKey = event => { if (event.key === 'Escape') setPlusOpen(false); };
    document.addEventListener('mousedown', onDocDown);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDocDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [plusOpen]);

  // 「打开文件」走和输入框附件一样的上传通道，拿到服务端 path 后交给面板渲染，
  // 这样本地选的文件和产物文件走的是同一套预览能力。
  const openLocalFiles = useCallback(async event => {
    const list = Array.from((event.target && event.target.files) || []);
    event.target.value = '';
    if (!list.length || !onOpenFile) return;
    for (const file of list) {
      try {
        // 必须带上当前会话，否则 uploadFile 会回落到 'default' 目录，
    // 不同会话的本机文件会混在一起。
    const res = await uploadFile(file, sessionName);
        if (res && res.path) onOpenFile({ path: res.path, name: res.name || file.name, size: res.size });
      } catch (err) {
        window.alert('打开失败：' + (err && err.message ? err.message : err));
      }
    }
  }, [onOpenFile]);

  const openBrowserTab = () => {
    setPlusOpen(false);
    // 复用已有的 browser-live 视图：地址栏留着给用户自己输网址。
    onOpenFile({ kind: 'browser-live', name: '新标签页', page_url: 'about:blank' });
  };

  // + 菜单里的「文件」= 切到工作区文件页（应用内文件浏览器 + 左侧文件树），
  // 不是弹系统选文件框。选本机文件走工具栏的「打开文件」按钮。
  const openWorkspaceTab = () => {
    setPlusOpen(false);
    // 顺便把文件树打开：不打开的话这页只有一个「打开文件」空占位，等于没进来。
    setTreeOpen(true);
    onOpenFile({ kind: 'workspace', name: '工作区文件' });
  };

  // 面板左侧拖拽调宽
  const startResize = event => {
    if (event.button !== 0) return;
    event.preventDefault();
    const panel = panelRef.current;
    if (!panel) return;
    const startX = event.clientX;
    const startW = panel.getBoundingClientRect().width;
    const maxW = Math.round(window.innerWidth * 0.78);
    setResizing(true);
    const onMove = moveEvent => {
      const next = Math.max(360, Math.min(maxW, startW - (moveEvent.clientX - startX)));
      setPanelWidth(Math.round(next));
    };
    const onUp = () => {
      setResizing(false);
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
    };
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onUp);
  };

  useEffect(() => {
    if (!incomingFile) return;
    const tab = { id: fileTabId(incomingFile), file: incomingFile };
    setTabs(previous => {
      const index = previous.findIndex(item => item.id === tab.id);
      if (index < 0) return [...previous, tab];
      const next = previous.slice();
      next[index] = tab;
      return next;
    });
    setActiveTabId(tab.id);
  }, [incomingFile]);

  const file = (tabs.find(tab => tab.id === activeTabId) || tabs[0] || {}).file || incomingFile;
  const isWorkspace = !!(file && file.kind === 'workspace');
  const isBrowserLive = !!(file && file.kind === 'browser-live' && (file.page_url || file.live_url));

  // 文件树按钮与文件树都只属于「文件页」：浏览器标签里不显示，
  // 否则 264px 的树会把预览挤窄（实测 749px 面板里浏览器只剩 481px）。
  // 但这里只做显示层的过滤，绝不在切标签时 setTreeOpen(false)：
  // 那样会永久改掉用户自己的开关状态，切回文件页时树就莫名其妙没了。
  const showTree = treeOpen && !isBrowserLive;

  // 聊天区右侧留白由 .content-row.preview-open .chat-col 的 margin-right 控制，
  // 但面板是 portal 到 document.body 的，content-row 看不到它，:has() 用不上。
  // 所以把「该留多少」这个数值挂到 :root 上，让聊天区直接读。
  // 折叠时留白必须一起归零，否则面板滑出视口后右边会空出一块。
  useEffect(() => {
    const root = document.documentElement;
    if (panelCollapsed) {
      root.style.setProperty('--preview-chat-inset', '0px');
      return undefined;
    }
    root.style.setProperty('--preview-chat-inset', panelWidth ? panelWidth + 14 + 'px' : '');
    return () => root.style.removeProperty('--preview-chat-inset');
  }, [panelCollapsed, panelWidth]);
  const isReport = !!(file && file.kind === 'report');
  const reportPath = isReport ? ((file && file.path) || '') : '';
  const isPPT = !!(file && file.kind === 'ppt');
  const pptPath = isPPT ? ((file && file.path) || '') : '';
  const isImage = !isWorkspace && !isBrowserLive && isImageFile(file);
  const filePath = isReport || isPPT || isWorkspace ? '' : ((file && file.path) || '');
  const name = isBrowserLive
    ? (file.name || '浏览器页面')
    : (isReport
      ? (file.name || '报告详情')
      : (isPPT ? (file.name || 'PPT 演示') : (isWorkspace ? '工作区文件' : nameOf(filePath))));
  const detail = fileContextDetail(file);

  const handleReportClick = event => {
    const anchor = event.target && event.target.closest
      ? event.target.closest('a.report-file-link')
      : null;
    if (!anchor || !onOpenFile) return;
    const href = anchor.getAttribute('href') || '';
    if (!href.startsWith('/api/file-content?')) return;
    let pathValue = '';
    try {
      pathValue = new URL(href, window.location.origin).searchParams.get('path') || '';
    } catch {
      return;
    }
    if (!isMarkdownArtifactPath(pathValue)) return;
    event.preventDefault();
    onOpenFile({ path: pathValue, name: nameOf(pathValue) });
  };

  const closeTab = (event, tabId) => {
    event.stopPropagation();
    const index = tabs.findIndex(tab => tab.id === tabId);
    const next = tabs.filter(tab => tab.id !== tabId);
    if (!next.length) {
      onClose();
      return;
    }
    setTabs(next);
    if (tabId === activeTabId) {
      setActiveTabId(next[Math.min(index, next.length - 1)].id);
    }
  };

  return (
    <>
    <aside
      ref={panelRef}
      className={'file-panel' + (panelCollapsed ? ' collapsed' : '')}
      style={panelWidth ? { '--file-panel-w': panelWidth + 'px' } : undefined}
    >
      {!panelCollapsed && (
        <div
          className="file-panel-resizer"
          role="separator"
          aria-orientation="vertical"
          title="拖拽调整预览面板宽度"
          onPointerDown={startResize}
        />
      )}
      <div className="file-panel-toolbar">
        <button
          className="file-panel-open-file"
          type="button"
          onClick={() => fileInputRef.current && fileInputRef.current.click()}
          title="从本机打开文件"
        >
          <FolderOpen size={14} />
          <span>打开文件</span>
        </button>
        <div className="file-panel-tabs" role="tablist" aria-label="预览文件">
          {tabs.map(tab => (
            <div
              key={tab.id}
              className={'file-panel-tab' + (tab.id === activeTabId ? ' active' : '')}
              role="tab"
              aria-selected={tab.id === activeTabId}
              tabIndex={0}
              title={fileTabName(tab.file)}
              onClick={() => setActiveTabId(tab.id)}
              onKeyDown={event => {
                if (event.key === 'Enter' || event.key === ' ') {
                  event.preventDefault();
                  setActiveTabId(tab.id);
                }
              }}
            >
              <ViewerIcon file={tab.file} size={13} />
              <span className="file-panel-tab-label">{fileTabName(tab.file)}</span>
              <button
                type="button"
                className="file-panel-tab-close"
                title="关闭标签页"
                aria-label={'关闭 ' + fileTabName(tab.file)}
                onClick={event => closeTab(event, tab.id)}
              ><X size={12} /></button>
            </div>
          ))}
        </div>
        <div className="file-panel-plus" ref={plusRef}>
          <button
            className="file-panel-btn"
            type="button"
            onClick={() => setPlusOpen(value => !value)}
            title="新建标签页"
            aria-label="新建标签页"
            aria-expanded={plusOpen}
          ><Plus size={15} /></button>
          {plusOpen && (
            <div className="file-panel-plus-menu" role="menu">
              <button type="button" role="menuitem" onClick={openWorkspaceTab}>
                <FolderTree size={14} />
                <span>文件</span>
              </button>
              <button type="button" role="menuitem" onClick={openBrowserTab}>
                <Globe2 size={14} />
                <span>浏览器</span>
              </button>
            </div>
          )}
        </div>
        <input
          ref={fileInputRef}
          type="file"
          multiple
          className="file-panel-file-input"
          onChange={openLocalFiles}
        />
        <div className="file-panel-toolbar-actions">
          {!isBrowserLive && (
            <button
              className="file-panel-btn"
              type="button"
              data-active={showTree ? 'true' : undefined}
              onClick={() => setTreeOpen(value => !value)}
              title="显示或隐藏文件目录"
              aria-label="显示或隐藏文件目录"
            ><FolderTree size={15} /></button>
          )}
          {!isReport && !isPPT && !isWorkspace && !isBrowserLive && filePath && (
            <a className="file-panel-btn" href={fileApiUrl(filePath, { download: true })}
              download={name} title="下载" aria-label="下载"><Download size={15} /></a>
          )}
          <button className="file-panel-btn" type="button"
            onClick={() => setPanelCollapsed(true)}
            title="收起预览" aria-label="收起预览"><ChevronRight size={15} /></button>
          <button className="file-panel-btn" type="button" onClick={onClose}
            title="关闭预览" aria-label="关闭预览"><X size={15} /></button>
        </div>
      </div>
      <div className="file-panel-contextbar">
        <div className="file-panel-context-main">
          <ViewerIcon file={file} size={15} />
          <span className="file-panel-title" title={detail || name}>{name}</span>
          {detail && <span className="file-panel-path" title={detail}>{detail}</span>}
        </div>
        <span className="file-panel-kind">{fileKindLabel(file)}</span>
      </div>
      <div className="file-panel-main">
      <div className={'file-panel-body' + (isReport || isWorkspace ? '' : isBrowserLive ? ' file-panel-body-browser-live' : ' file-panel-body-viewer')}>
        {isBrowserLive && <BrowserLiveView file={file} />}
        {isReport && file.bodyText && (
          <div className="file-report report-rendered"
            onClick={handleReportClick}
            dangerouslySetInnerHTML={{ __html: renderReportHtml(file.bodyText || '') }} />
        )}
        {isReport && !file.bodyText && reportPath && (
          <iframe
            key={activeTabId}
            className="file-panel-viewer"
            src={viewerUrl(reportPath)}
            title={name}
          />
        )}
        {isReport && !file.bodyText && !reportPath && <div className="file-panel-hint">报告预览不可用</div>}
        {!isReport && isPPT && pptPath && (
          <iframe
            key={activeTabId}
            className="file-panel-viewer"
            src={pptPreviewUrl(pptPath)}
            title={name}
          />
        )}
        {!isReport && !isPPT && !isWorkspace && isImage && filePath && (
          <img className="file-panel-img" src={fileApiUrl(filePath, { version: file.version })} alt={name} />
        )}
        {!isReport && !isPPT && !isWorkspace && !isImage && filePath && (
          <iframe
            key={activeTabId}
            className="file-panel-viewer"
            src={viewerUrl(filePath)}
            title={name}
          />
        )}
        {!isReport && isPPT && !pptPath && <div className="file-panel-hint">PPT 预览不可用</div>}
        {!isReport && !isPPT && !isWorkspace && !isBrowserLive && !filePath && <div className="file-panel-hint">没有可预览的文件</div>}
        {isWorkspace && (
          <div className="file-panel-empty">
            <FolderOpen size={28} strokeWidth={1.4} />
            <strong>工作区文件</strong>
            <span>从文件树选择文件，或让 Agent 打开浏览器实时页面。</span>
          </div>
        )}
      </div>
      {showTree && <WorkspaceTree onOpenFile={onOpenFile} activePath={filePath} />}
      </div>
    </aside>
    {panelCollapsed && (
      <button
        className="file-panel-expand-tab"
        type="button"
        onClick={() => setPanelCollapsed(false)}
        title="展开预览"
        aria-label="展开预览"
      ><ChevronRight size={15} /></button>
    )}
    </>
  );
}
