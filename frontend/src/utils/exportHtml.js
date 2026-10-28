// 会话导出工具：聊天 HTML + Trace 完整原样导出
// 两个文件都是"单文件自包含"：打开即看，不依赖平台/网络。
//
// bundle 与样式都用 ?raw 内联，静态 import 会把它们整份塞进主产物
// （trace bundle 约 269 KB、App.css 约 104 KB），可这两个内容只在用户
// 真正点"导出"时才需要。所以一律改成动态 import：主包不再为导出功能买单。
// 动态取 ?raw 资源。Vite 的 ?raw 返回 { default: string }。
async function loadRaw(loader) {
  const mod = await loader();
  return typeof mod === 'string' ? mod : mod.default;
}

// 转义 </script> 字面，避免内联脚本提前截断。
function inlineScriptSafe(source) {
  return String(source || '').replace(/<\/script>/gi, '<\\/script>');
}

export function downloadFile(filename, content, mime = 'text/html;charset=utf-8') {
  const blob = new Blob([content], { type: mime });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
}

export function escapeHtml(s) {
  return String(s ?? '').replace(/[&<>"']/g, c => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  }[c]));
}

function roleLabel(role) {
  if (role === 'user') return '用户';
  if (role === 'assistant') return 'AI';
  if (role === 'tool') return '工具';
  return role || '?';
}

// ---------- 聊天完整原样导出：内嵌前端 ChatArea 打包 bundle ----------
// 异步：bundle 与 css 在真正导出时才加载，调用方需要 await。
export async function buildChatHtml(name, messages, mode) {
  const data = JSON.stringify(messages || []);
  const [bundle, appCss] = await Promise.all([
    loadRaw(() => import('./chat-view-bundle.js?raw')),
    loadRaw(() => import('../styles/App.css?raw')),
  ]);
  const bundleSafe = inlineScriptSafe(bundle);
  return '<!doctype html><html lang="zh"><head><meta charset="utf-8"><title>' + escapeHtml(name) + '</title>'
    + '<style>html,body{margin:0;height:100vh;background:#f6f7f9}'
    + '#root{height:100vh;overflow:auto}'
    + appCss
    + '</style></head><body>'
    + '<div id="root"></div>'
    + '<scr' + 'ipt>window.__CHAT_DATA__=' + data + ';window.__CHAT_MODE__=' + JSON.stringify(mode || 'chat') + ';<\/scr' + 'ipt>'
    + '<scr' + 'ipt>' + bundleSafe + '<\/scr' + 'ipt>'
    + '<scr' + 'ipt>ChatExport.renderChat(document.getElementById("root"), window.__CHAT_DATA__, window.__CHAT_MODE__);<\/scr' + 'ipt>'
    + '</body></html>';
}

// ---------- Trace 完整原样导出：内嵌前端 TracePanel 打包 bundle ----------
// 异步：bundle 与 css 在真正导出时才加载，调用方需要 await。
export async function buildTraceHtml(name, trace) {
  const data = JSON.stringify(trace || { main: [], subtasks: [] });
  const [bundle, appCss] = await Promise.all([
    loadRaw(() => import('./trace-view-bundle.js?raw')),
    loadRaw(() => import('../styles/App.css?raw')),
  ]);
  // bundle 里可能含 </script> 字面，转义避免提前截断
  const bundleSafe = inlineScriptSafe(bundle);
  return '<!doctype html><html lang="zh"><head><meta charset="utf-8"><title>Trace · ' + escapeHtml(name) + '</title>'
    + '<style>html,body{margin:0;height:100vh;overflow:hidden;background:#fff}'
    + '#root{height:100vh;display:flex;flex-direction:column}'
    + appCss
    + '</style></head><body>'
    + '<div id="root"></div>'
    + '<scr' + 'ipt>window.__TRACE_DATA__=' + data + ';window.__TRACE_NAME__=' + JSON.stringify(name) + ';<\/scr' + 'ipt>'
    + '<scr' + 'ipt>' + bundleSafe + '<\/scr' + 'ipt>'
    + '<scr' + 'ipt>TraceExport.renderTrace(document.getElementById("root"), window.__TRACE_DATA__, window.__TRACE_NAME__);<\/scr' + 'ipt>'
    + '</body></html>';
}
