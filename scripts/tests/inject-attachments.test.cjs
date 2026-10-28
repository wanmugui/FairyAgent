#!/usr/bin/env node
// inject-attachments 验收。
//
// 这里刻意不写「grep 到某个字符串就算过」——旧版测试就是那么写的，结果
// 特性完全坏掉时它依然 5/5 全绿。真正的判据是行为：
//   1) 拿 chat.js 里的真函数跑一遍线上真实报文格式；
//   2) 断言 injected_user 的 SSE 处理确实把 content 交给它、并挂上 files
//      （这是当初图片不显示的真因，注释里写对了但代码没做）。
const fs = require('fs');
const path = require('path');

const ROOT = path.resolve(__dirname, '..', '..');
const CHAT = path.join(ROOT, 'frontend/src/api/chat.js');
const APP = path.join(ROOT, 'frontend/src/App.jsx');

let failed = 0;
const ok = (cond, name, extra) => {
  console.log((cond ? '  ok   ' : '  FAIL ') + name + (extra ? `  — ${extra}` : ''));
  if (!cond) failed++;
};

// ---------- 1. 取 chat.js 里真的 splitFileContext 并执行 ----------
const chatSrc = fs.readFileSync(CHAT, 'utf8');
const m = chatSrc.match(/export function splitFileContext\([\s\S]*?\n\}/);
ok(!!m, 'chat.js 导出 splitFileContext');
if (!m) { console.error('无法继续'); process.exit(1); }
const splitFileContext = new Function(`${m[0].replace('export ', '')}; return splitFileContext;`)();

// ---------- 2. 按 App.jsx 的拼法造真实报文，跑真函数 ----------
// App.jsx 注入时: '<file_context>\n' + JSON.stringify(attachmentFiles) + '\n</file_context>\n' + text
const attachmentFiles = [
  { name: 'shot.png', path: '/tmp/shot.png', kind: 'image',
    image_url: 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUg==', size: 5 }
];
const text = '顺便看下这张图';
const wire = '<file_context>\n' + JSON.stringify(attachmentFiles, null, 2) + '\n</file_context>\n' + text;

const out = splitFileContext(wire);
ok(out.text === text, '拆分后正文不含 <file_context>', JSON.stringify(out.text));
ok(Array.isArray(out.files) && out.files.length === 1, '拆出 1 个附件');
ok(out.files[0] && out.files[0].image_url === attachmentFiles[0].image_url,
  '附件带 image_url（MessageBubble 靠它出图）');
ok(out.files[0] && out.files[0].kind === 'image', '附件 kind=image');

// 仅带文件、正文为空
const onlyFile = splitFileContext('<file_context>\n' + JSON.stringify(attachmentFiles) + '\n</file_context>\n');
ok(onlyFile.text === '' && onlyFile.files.length === 1, '仅带图片、正文为空时不报错');

// ---------- 3. toInjectedUserMessage：注入回显的唯一构造入口 ----------
const m2 = chatSrc.match(/export function toInjectedUserMessage\([\s\S]*?\n\}/);
ok(!!m2, 'chat.js 导出 toInjectedUserMessage');
if (m2) {
  const build = new Function(`const splitFileContext = ${m[0].replace('export ', '')}; ${m2[0].replace('export ', '')}; return toInjectedUserMessage;`)();
  const r = build(wire, { injected: true, injectId: 'x1' });
  ok(r.content === text, '注入回显正文只剩用户输入', JSON.stringify(r.content));
  ok(Array.isArray(r.files) && r.files.length === 1, '注入回显带 1 个附件');
  ok(r.role === 'user' && r.injected === true && r.injectId === 'x1', '注入标记与 id 保留');
  const onlyFileR = build('<file_context>\n' + JSON.stringify(attachmentFiles) + '\n</file_context>\n', {});
  ok(onlyFileR.content === '' && onlyFileR.files.length === 1, '仅带图片、正文为空时仍出附件');
  // 旧行为对照：不拆的话附件会变成正文
  const broken = { role: 'user', content: wire, files: undefined };
  ok(broken.content.includes('<file_context>'), '（对照）不拆就会把 file_context 漏进正文');
}

// ---------- 4. 断言 injected_user 真的走了这条路（真因所在） ----------
const appSrc = fs.readFileSync(APP, 'utf8');
const hIdx = appSrc.indexOf("event.type === 'injected_user'");
ok(hIdx > 0, '找到 injected_user 的 SSE 处理');
if (hIdx > 0) {
  // 截到下一个同级分支为止，避免把无关代码算进来
  const next = appSrc.indexOf("} else if (event.type ===", hIdx + 10);
  const handler = appSrc.slice(hIdx, next > 0 ? next : appSrc.length);
  ok(/toInjectedUserMessage\(\s*event\.content/.test(handler),
    'SSE 处理用 toInjectedUserMessage 构造消息');
  const appendLine = (handler.split('\n').find((l) => l.includes('return [...prev')) || '');
  ok(appendLine.includes('toInjectedUserMessage'),
    '新建分支同样走同一入口', appendLine.trim().slice(0, 60));
  ok(!/content:\s*event\.content/.test(handler), '没有把原始 content 直接塞进气泡');
  ok(!!appSrc.match(/import[^;]*\btoInjectedUserMessage\b[^;]*from '\.\/api\/chat'/s) ||
     /toInjectedUserMessage,/.test(appSrc.split('\n').slice(0, 80).join('\n')), 'toInjectedUserMessage 已导入');
}

// ---------- 5. 只挂附件时，线上报文并不是空串 ----------
// 这条是之前漏掉的关键证据。后端 inject 处理器有 `if (!text) → 400 text is required`，
// 而旧结论「仅带 files 不返回 400」是从「三种情况都回了 409」推出来的——可 409 是在
// **所有**校验之前就短路返回的，根本走不到那个 400，所以那个推论是无效的。
// 真正让这条路成立的是前端：App.jsx 拼的是 '<file_context>…</file_context>\n' + text.trim()，
// 只要挂了附件，线上报文必定非空，后端那个 !text 就永远不会被真实客户端触发。
const onlyAttachWire = `<file_context>${JSON.stringify(attachmentFiles)}</file_context>\n` + ''.trim();
ok(onlyAttachWire.trim() !== '', '只挂附件、用户没打字时，线上报文仍非空', `${Buffer.byteLength(onlyAttachWire)} 字节`);
ok(/const\s+injectText\s*=\s*attachmentFiles\.length\b/.test(appSrc), 'App.jsx 有附件就拼 file_context 包裹');
ok(/if\s*\(\s*!injectText\.trim\(\)\s*\)/.test(appSrc), 'App.jsx 对空 injectText 早退（真客户端发不出空 text）');

// ---------- 6. 真实 HTTP：409 预检排在所有校验之前 ----------
// 这段专门用来钉死上一条推论为什么无效：只要会话没有活跃 run，**任何**载荷
// 都回 409，包括形状非法的。所以「返回了 409」对「正文校验存不存在」零信息量。
(async () => {
  const http = require('http');
  const os = require('os');
  const { spawn } = require('child_process');
  // 自选空闲端口：固定端口一旦被上一次跑崩留下的孤儿服务占着，
  // 这个测试会**连到别人的服务上**、拿一堆看似合理实则错误的结论——
  // 这轮开发里残留进程占 8087/8093/8094/8096 已经是第三次了。
  const net = require('net');
  const freePort = () => new Promise((res) => {
    const srv = net.createServer();
    srv.listen(0, '127.0.0.1', () => { const p = srv.address().port; srv.close(() => res(p)); });
  });
  const PORT = Number(process.env.FM_INJECT_TEST_PORT) || await freePort();
  const mem = fs.mkdtempSync(path.join(os.tmpdir(), 'inject-mem-'));
  const xdg = fs.mkdtempSync(path.join(os.tmpdir(), 'inject-xdg-'));
  const srv = spawn(process.execPath, [path.join(ROOT, 'frontend', 'server.cjs'), String(PORT)], {
    cwd: path.join(ROOT, 'frontend'),
    // FAIRY_MEMORY_ROOT 把会话根整个挪到临时目录：绝不碰 memory/sessions，
    // 这正是旧记录里"只能上真实实例做、否则会写 memory/sessions"的那个卡点，
    // 其实有干净解法。FAIRY_AUTH=0 免得跟生产抢 auth.db。
    env: { ...process.env, FAIRY_AUTH: '0', FAIRY_TEST_INJECT_HOOK: '1', FAIRY_MEMORY_ROOT: mem, XDG_DATA_HOME: xdg,
           FAIRY_AUTH_DB: path.join(xdg, 'auth.db') },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  const post = (p, body) => new Promise((res) => {
    const d = Buffer.from(JSON.stringify(body));
    const r = http.request({ host: '127.0.0.1', port: PORT, path: p, method: 'POST',
      headers: { 'Content-Type': 'application/json', 'Content-Length': d.length } }, (x) => {
      let b = ''; x.on('data', (c) => b += c); x.on('end', () => res({ s: x.statusCode, b }));
    });
    r.on('error', (e) => res({ s: 'ERR', b: e.message })); r.write(d); r.end();
  });
  const get = (p) => new Promise((res) => {
    const r = http.request({ host: '127.0.0.1', port: PORT, path: p, method: 'GET' }, (x) => {
      let b = ''; x.on('data', (c) => b += c); x.on('end', () => res({ s: x.statusCode, b }));
    });
    r.on('error', (e) => res({ s: 'ERR', b: e.message })); r.end();
  });
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  try {
    for (let i = 0; i < 100; i++) { const r = await post('/api/usage', {}); if (r.s !== 'ERR') break; await sleep(300); }
    const IMG = { name: 'a.png', path: '/tmp/a.png', kind: 'image', image_url: 'data:image/png;base64,AA==' };
    const a = await post('/api/chat/inject', { session: 'req-test-attach', files: [IMG] });
    ok(a.s === 409, '完全不带 text 字段 → 409', `${a.s} ${a.b.trim()}`);
    const b = await post('/api/chat/inject', { session: 'req-test-attach', text: '', files: [IMG] });
    ok(b.s === 409, 'text 为空串 → 409', `${b.s} ${b.b.trim()}`);
    // 决定性的一条：files 形状都非法也回 409，而不是 400。
    const c = await post('/api/chat/inject', { session: 'req-test-attach', text: 'x', files: 'nope' });
    ok(c.s === 409, 'files 形状非法同样回 409 → 409 早于一切校验', `${c.s} ${c.b.trim()}`);
    ok(JSON.parse(c.b).error === 'no active chat for session', '409 的语义是「该会话没有活跃 run」');
    // ---- 拿到一次真实 ok:true：不碰模型，只靠测试钩子造一个活跃 run ----
    // 这一段是本条一直缺的那块证据。以前只能验到 409，因为没有任何办法
    // 在不调模型的前提下造出「活跃 run」；钩子补上之后才第一次能走通全链路。
    const LIVE = 'req-test-live';
    const att = await post('/api/chat/_test/attach-run', { session: LIVE });
    ok(att.s === 200 && JSON.parse(att.b).ok, '钩子挂上活跃 run', `${att.s} ${att.b.trim()}`);
    const runId = JSON.parse(att.b).run_id;

    const wire = `<file_context>${JSON.stringify(attachmentFiles)}</file_context>\n仅看这张图`;
    const good = await post('/api/chat/inject', { session: LIVE, text: wire, files: attachmentFiles });
    const gj = JSON.parse(good.b);
    ok(good.s === 200 && gj.ok === true, '有活跃 run + 非空 text → ok:true（真实拿到）', `${good.s} ${good.b.trim()}`);
    ok(gj.run_id === runId && !!gj.run_id, '响应带出该 run 的 run_id', String(gj.run_id));

    await sleep(400);
    const got = await get('/api/chat/_test/received?session=' + encodeURIComponent(LIVE));
    const lines = JSON.parse(got.b).received || [];
    ok(lines.length === 1, '注入的报文真的落到了 agent 的 stdin', `收到 ${lines.length} 行`);
    let frame = {}; try { frame = JSON.parse(lines[0] || '{}'); } catch {}
    ok(frame.op === 'inject', 'stdin 上是 NDJSON 帧 op=inject', String(frame.op));
    ok(frame.text === wire, 'file_context 包裹原文送达，未被服务端改写', JSON.stringify(String(frame.text).slice(0, 46)) + '…');

    // 决定性的一条：有了活跃 run，闸门放行之后空正文校验终于**露出来了**。
    // 旧记录"三种都回 409 所以没有空正文校验"到此被正面推翻。
    const emptyLive = await post('/api/chat/inject', { session: LIVE, text: '', files: attachmentFiles });
    ok(emptyLive.s === 400 && JSON.parse(emptyLive.b).error === 'text is required',
       '有活跃 run 时，空 text → 400 text is required（更正：空正文校验确实存在）', `${emptyLive.s} ${emptyLive.b.trim()}`);

    await post('/api/chat/_test/detach-run', { session: LIVE });
    const afterDetach = await post('/api/chat/inject', { session: LIVE, text: wire });
    ok(afterDetach.s === 409, '摘掉 run 后又回到 409（两个 map 都得在）', `${afterDetach.s} ${afterDetach.b.trim()}`);

  } finally {
    srv.kill('SIGKILL');
  }
  console.log(failed ? `\n注入附件：${failed} 项失败` : '\n注入附件：全部通过');
  process.exit(failed ? 1 : 0);
})();