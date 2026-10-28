const fs = require('fs');
const path = require('path');

const DECK_ID_RE = /^pptid_[A-Za-z0-9._-]+$/;

function resolveDeckId(deckDir, { workspaceRoot, virtualDeckRoot }) {
  const value = String(deckDir || '').trim().replace(/\\/g, '/');
  const root = String(virtualDeckRoot || '/mnt/data/result/').replace(/\\/g, '/').replace(/\/+$/, '') + '/';
  if (!value.startsWith(root)) return null;
  const relative = value.slice(root.length).replace(/^\/+|\/+$/g, '');
  if (!DECK_ID_RE.test(relative)) return null;
  try {
    const realRoot = fs.realpathSync(workspaceRoot);
    const candidate = fs.realpathSync(path.join(realRoot, relative));
    if (path.dirname(candidate) !== realRoot || !fs.statSync(candidate).isDirectory()) return null;
    return relative;
  } catch {
    return null;
  }
}

function listDeckPages(deckId, workspaceRoot) {
  if (!DECK_ID_RE.test(String(deckId || ''))) return [];
  // 优先 htmls/*.html（模板/无模板模式）；没有时回退 pages/*.png（创意模式）
  const candidates = [
    { subdir: 'htmls', pattern: /^page_\d+\.html$/i },
    { subdir: 'pages', pattern: /^page_\d+\.png$/i },
  ];
  for (const { subdir, pattern } of candidates) {
    const dirPath = path.join(workspaceRoot, deckId, subdir);
    try {
      const files = fs.readdirSync(dirPath)
        .filter(name => pattern.test(name) && fs.statSync(path.join(dirPath, name)).isFile())
        .sort((a, b) => a.localeCompare(b, undefined, { numeric: true }));
      if (files.length) return files;
    } catch {
      // 目录不存在或不可读，尝试下一个候选目录
    }
  }
  return [];
}

function resolveDeckFile(deckId, relativePath, workspaceRoot) {
  if (!DECK_ID_RE.test(String(deckId || ''))) return null;
  try {
    const deckRoot = fs.realpathSync(path.join(workspaceRoot, deckId));
    const candidate = fs.realpathSync(path.join(deckRoot, String(relativePath || '')));
    const prefix = deckRoot.endsWith(path.sep) ? deckRoot : deckRoot + path.sep;
    if (!candidate.startsWith(prefix) || !fs.statSync(candidate).isFile()) return null;
    return candidate;
  } catch {
    return null;
  }
}

function buildDeckPreviewHtml(deckId, pages) {
  const pageUrls = pages.map(page => '/api/ppt-decks/' + encodeURIComponent(deckId) + '/' + (/\.png$/i.test(page) ? 'pages' : 'htmls') + '/' + encodeURIComponent(page));
  const cards = pageUrls.map((src, index) => {
    return '<article class="slide"><div class="slide-head">第 ' + (index + 1) + ' 页</div>'
      + '<div class="frame">' + (/\.png$/i.test(src)
        ? '<img class="page-img" loading="lazy" src="' + src + '" alt="第 ' + (index + 1) + ' 页">'
        : '<iframe loading="lazy" src="' + src + '" title="第 ' + (index + 1) + ' 页"></iframe>') + '</div></article>';
  }).join('');
  const thumbnails = pageUrls.map((src, index) => '<button class="thumb' + (index === 0 ? ' active' : '') + '" type="button" data-page="' + index + '">'
    + '<span class="thumb-number">' + (index + 1) + '</span><span class="thumb-frame frame">' + (/\.png$/i.test(src)
      ? '<img class="thumb-img" loading="lazy" src="' + src + '" alt="第 ' + (index + 1) + ' 页缩略图">'
      : '<iframe loading="lazy" src="' + src + '" title="第 ' + (index + 1) + ' 页缩略图" tabindex="-1"></iframe>') + '</span></button>').join('');
  return '<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">'
    + '<meta name="viewport" content="width=device-width,initial-scale=1"><title>PPT 页面预览</title>'
    + '<style>*{box-sizing:border-box}[hidden]{display:none!important}body{margin:0;background:#eef1f6;color:#172033;font-family:Inter,"Microsoft YaHei",sans-serif}'
    + 'header{position:sticky;top:0;z-index:2;display:flex;align-items:center;justify-content:space-between;gap:20px;padding:14px 24px;background:rgba(255,255,255,.94);border-bottom:1px solid #dfe4ec;backdrop-filter:blur(8px)}'
    + 'h1{margin:0;font-size:20px}.meta{margin-top:4px;color:#667085;font-size:13px}.mode-toggle{padding:8px 14px;border:1px solid #3157d5;border-radius:8px;background:#fff;color:#3157d5;font-size:13px;font-weight:600;cursor:pointer}.mode-toggle:hover{background:#eef2ff}'
    + '.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(520px,1fr));gap:20px;padding:24px}'
    + '.slide{overflow:hidden;border:1px solid #d8dee9;border-radius:12px;background:#fff;box-shadow:0 6px 20px rgba(19,33,68,.08)}.slide-head{padding:9px 12px;color:#475467;font-size:13px;border-bottom:1px solid #e7eaf0}'
    + '.frame{position:relative;width:100%;aspect-ratio:16/9;background:#111;overflow:hidden}.frame iframe{position:absolute;inset:0;width:1280px;height:720px;border:0;transform-origin:top left}.frame img{position:absolute;inset:0;width:100%;height:100%;object-fit:contain;display:block;background:#111}'
    + '.focus-mode{display:none;height:calc(100vh - 69px);min-height:480px;background:#d9dde5}.focus-mode.open{display:grid;grid-template-columns:196px minmax(0,1fr)}.thumbs{overflow-y:auto;padding:12px 10px;background:#f7f8fb;border-right:1px solid #cbd1dc}.thumb{display:grid;grid-template-columns:24px 1fr;align-items:start;gap:6px;width:100%;margin:0 0 10px;padding:5px;border:2px solid transparent;border-radius:6px;background:transparent;cursor:pointer}.thumb:hover{background:#eceff5}.thumb.active{border-color:#3157d5;background:#e8edff}.thumb-number{padding-top:2px;color:#667085;font-size:11px;text-align:center}.thumb-frame{display:block;aspect-ratio:16/9;border:1px solid #c9ced8;background:#fff;pointer-events:none}.thumb-frame img{width:100%;height:100%;object-fit:contain;display:block;background:#fff}'
    + '.focus-stage{position:relative;overflow:hidden;background:#cdd2da}.focus-canvas{position:absolute;width:1280px;height:720px;background:#fff;box-shadow:0 12px 40px rgba(17,24,39,.28);transform-origin:top left}.focus-canvas iframe{width:1280px;height:720px;border:0}.focus-canvas img{width:1280px;height:720px;object-fit:contain;display:block;background:#fff}.page-indicator{position:absolute;right:18px;bottom:14px;z-index:1;padding:5px 10px;border-radius:16px;background:rgba(17,24,39,.72);color:#fff;font-size:12px}'
    + '@media(max-width:700px){.grid{grid-template-columns:1fr;padding:12px}.focus-mode.open{grid-template-columns:116px minmax(0,1fr)}.thumb{grid-template-columns:1fr}.thumb-number{display:none}.thumbs{padding:8px 5px}}'
    + '</style></head><body><header><div><h1>PPT 页面预览</h1><div class="meta">' + deckId + ' · ' + pages.length + ' 页</div></div><button id="mode-toggle" class="mode-toggle" type="button">切换大图</button></header><main id="grid" class="grid">'
    + cards + '</main><section id="focus-mode" class="focus-mode" hidden><aside class="thumbs">' + thumbnails + '</aside><div id="focus-stage" class="focus-stage"><div id="focus-canvas" class="focus-canvas"><iframe id="focus-iframe" title="PPT 大图预览"></iframe><img id="focus-img" class="focus-img" alt="PPT 大图预览" hidden></div><div id="page-indicator" class="page-indicator"></div></div></section>'
    + '<script>(()=>{const urls=' + JSON.stringify(pageUrls) + ';const grid=document.getElementById("grid"),focus=document.getElementById("focus-mode"),toggle=document.getElementById("mode-toggle"),stage=document.getElementById("focus-stage"),canvas=document.getElementById("focus-canvas"),viewer=document.getElementById("focus-iframe"),pngViewer=document.getElementById("focus-img"),indicator=document.getElementById("page-indicator"),thumbs=[...document.querySelectorAll(".thumb")];let selected=0,wheelLock=false;'
    + 'const resizeFrame=frame=>{const iframe=frame.querySelector("iframe");if(iframe)iframe.style.transform="scale("+(frame.clientWidth/1280)+")"};const resizeFocus=()=>{const scale=Math.min((stage.clientWidth-48)/1280,(stage.clientHeight-48)/720);canvas.style.transform="scale("+Math.max(scale,.1)+")";canvas.style.left=Math.max(24,(stage.clientWidth-1280*Math.max(scale,.1))/2)+"px";canvas.style.top=Math.max(24,(stage.clientHeight-720*Math.max(scale,.1))/2)+"px"};'
    + 'const show=index=>{selected=Math.max(0,Math.min(urls.length-1,index));if(/\.png$/i.test(urls[selected])){viewer.hidden=true;pngViewer.hidden=false;if(pngViewer.getAttribute("src")!==urls[selected])pngViewer.src=urls[selected];}else{pngViewer.hidden=true;viewer.hidden=false;if(viewer.getAttribute("src")!==urls[selected])viewer.src=urls[selected];}indicator.textContent=(selected+1)+" / "+urls.length;thumbs.forEach((thumb,i)=>thumb.classList.toggle("active",i===selected));thumbs[selected]?.scrollIntoView({block:"nearest"})};'
    + 'const observer=new ResizeObserver(entries=>entries.forEach(entry=>entry.target===stage?resizeFocus():resizeFrame(entry.target)));document.querySelectorAll(".frame").forEach(frame=>{resizeFrame(frame);observer.observe(frame)});observer.observe(stage);thumbs.forEach(thumb=>thumb.addEventListener("click",()=>show(Number(thumb.dataset.page))));toggle.addEventListener("click",()=>{const open=focus.hidden;focus.hidden=!open;focus.classList.toggle("open",open);grid.hidden=open;toggle.textContent=open?"返回网格":"切换大图";if(open){show(selected);requestAnimationFrame(resizeFocus)}});stage.addEventListener("wheel",event=>{event.preventDefault();if(wheelLock||Math.abs(event.deltaY)<4)return;wheelLock=true;show(selected+(event.deltaY>0?1:-1));setTimeout(()=>wheelLock=false,180)},{passive:false});show(0)})()</script></body></html>';
}

module.exports = { resolveDeckId, listDeckPages, resolveDeckFile, buildDeckPreviewHtml };
