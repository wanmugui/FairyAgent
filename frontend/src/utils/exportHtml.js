// 会话导出工具：聊天 HTML + Trace 完整原样导出
// 两个文件都是"单文件自包含"：打开即看，不依赖平台/网络。
import traceViewBundle from './trace-view-bundle.js?raw';
import chatViewBundle from './chat-view-bundle.js?raw';
import appCss from '../styles/App.css?raw';

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
export function buildChatHtml(name, messages, mode) {
  const data = JSON.stringify(messages || []);
  const bundleSafe = chatViewBundle.replace(/<\/script>/gi, '<\\/script>');
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
export function buildTraceHtml(name, trace) {
  const data = JSON.stringify(trace || { main: [], subtasks: [] });
  // bundle 里可能含 </script> 字面，转义避免提前截断
  const bundleSafe = traceViewBundle.replace(/<\/script>/gi, '<\\/script>');
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
