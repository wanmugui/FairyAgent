// P4-1 WebDAV 验收：全部走**真实 HTTP 请求**（裸 http.request，因为 fetch
// 不支持 PROPFIND/MOVE/COPY/MKCOL 这些自定义方法）。
//
// 每个用例都对着一个临时的白名单根跑，FM_DAV_ROOTS 显式指定，
// 绝不去碰仓库里真实的 workspace/。
'use strict';

const http = require('http');
const fs = require('fs');
const fsp = fs.promises;
const os = require('os');
const path = require('path');
const { spawn } = require('child_process');

const ROOT = path.resolve(__dirname, '..');
const PORT = Number(process.env.FM_DAV_TEST_PORT || 8096);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

let PASS = 0, FAIL = 0;
const results = [];
function check(cond, name, detail) {
  if (cond) { PASS++; console.log('  ok  ', name, detail ? `— ${detail}` : ''); }
  else { FAIL++; console.log('  FAIL', name, detail ? `— ${detail}` : ''); }
  results.push({ cond, name, detail });
}

/** 发一个原始 HTTP 请求，返回 status / headers / body(Buffer) */
function request(method, urlPath, { headers = {}, body = null } = {}) {
  return new Promise((resolve, reject) => {
    // encodeURI 会把 % 转成 %25（% 不在它的免转义表里），直接用它会把手工编码好的
    // %2e%2e 再套一层，穿越用例等于压根没送到服务端。
    // 先 encodeURI 解决中文与空格，再把被二次编码的 %xx 还原。
    const encPath = (p) => encodeURI(p).replace(/%25([0-9A-Fa-f]{2})/g, '%$1');
    const req = http.request({ host: '127.0.0.1', port: PORT, method, path: encPath(urlPath), headers }, (res) => {
      const chunks = [];
      res.on('data', (c) => chunks.push(c));
      res.on('end', () => resolve({
        status: res.statusCode, headers: res.headers, body: Buffer.concat(chunks),
      }));
    });
    req.on('error', reject);
    if (body != null) req.write(body);
    req.end();
  });
}
const countResponses = (xml) => (xml.match(/<D:response>/g) || []).length;
// 裸 socket 发请求：headerLines 里的值按**原始字节**写进报文，用来复现
// 「客户端没 percent-encode、直接把 UTF-8 塞进请求头」这种情况。
// http.request 会把头按 latin1 写，发不出这种请求，只能自己拼字节。
const net = require('net');
function rawRequest(method, urlPath, headerLines, body) {
  return new Promise((resolve, reject) => {
    const sock = net.connect(PORT, '127.0.0.1', () => {
      const lines = [method + ' ' + urlPath + ' HTTP/1.1', 'Host: 127.0.0.1', 'Connection: close']
        .concat(headerLines, ['', '']);
      sock.write(Buffer.concat([
        // 请求头要按 **UTF-8 字节** 上线：那才是真实客户端（Finder / Windows 资源管理器）
        // 实际发的字节。用 latin1 写会把中文截成低字节，线上根本不存在这种编码，
        // 测出来的 400 是自造的假象，不是被测代码的问题。
        Buffer.from(lines.join('\r\n'), 'utf8'),
        Buffer.isBuffer(body) ? body : Buffer.from(body || '', 'utf8'),
      ]));
    });
    const bufs = [];
    sock.on('data', (c) => bufs.push(c));
    sock.on('end', () => {
      const raw = Buffer.concat(bufs);
      const sep = raw.indexOf('\r\n\r\n');
      const headTxt = raw.slice(0, sep).toString('latin1');
      const status = Number(headTxt.split('\r\n')[0].split(' ')[1]);
      resolve({ status, raw: raw.slice(sep + 4), headTxt });
    });
    sock.on('error', reject);
  });
}
// Destination 放在 HTTP 头里，必须全 ASCII：路径段逐个 encodeURIComponent
const dest = (...segs) => `http://127.0.0.1:${PORT}/dav/` + segs.map(encodeURIComponent).join('/');

async function startServer(davRoots) {
  const xdg = fs.mkdtempSync(path.join(os.tmpdir(), 'fm-dav-xdg-'));
  const ch = spawn(process.execPath, [path.join(ROOT, 'frontend', 'server.cjs'), String(PORT)], {
    cwd: path.join(ROOT, 'frontend'),
    // FAIRY_AUTH_DB 必须单独指一份：默认是 memory/auth.db，也就是**生产服务
    // 正在用的那份**。不隔离的话，测试服务会和 8081 抢同一个库，
    // 结果就是 auth.db database is locked，而且测试等于在动生产鉴权数据。
    // 这里必须**显式**关掉鉴权。生产是 FAIRY_AUTH=1，而 env 里的 ...process.env
    // 会把它一起继承过来：早先的版本就借着生产 auth.db + localhost 免信任跑通了，
    // 等于每次跑测试都在动生产鉴权数据、还跟 8081 抢同一个库。
    // WebDAV 客户端本来也不会带 cookie/session，所以端点本身就该在无鉴权下验收。
    env: { ...process.env, FAIRY_AUTH: '0', XDG_DATA_HOME: xdg, FAIRY_AUTH_DB: path.join(xdg, 'auth.db'), FM_DAV_ROOTS: davRoots },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  ch.stderr.on('data', (d) => { const s = String(d); if (/\[dav\]/.test(s)) process.stderr.write(s); });
  for (let i = 0; i < 100; i++) {
    try { const r = await request('GET', '/api/usage'); if (r.status) return ch; } catch { /* 还没起来 */ }
    await sleep(300);
  }
  throw new Error('服务启动超时');
}

(async () => {
  const TMP = fs.mkdtempSync(path.join(os.tmpdir(), 'fm-dav-'));
  const DAV = path.join(TMP, 'davroot');
  const SECOND = path.join(TMP, '第二根');
  const OUTSIDE = path.join(TMP, 'outside');
  for (const d of [DAV, SECOND, OUTSIDE]) fs.mkdirSync(d, { recursive: true });
  fs.mkdirSync(path.join(DAV, '子目录'), { recursive: true });
  fs.writeFileSync(path.join(DAV, 'a.txt'), 'hello dav');
  fs.writeFileSync(path.join(DAV, '子目录', 'b.txt'), 'nested');
  fs.writeFileSync(path.join(OUTSIDE, 'secret.txt'), 'TOP SECRET');
  fs.symlinkSync(OUTSIDE, path.join(DAV, '逃逸链接'), 'dir');
  fs.symlinkSync(path.join(OUTSIDE, 'secret.txt'), path.join(DAV, '逃逸文件'), 'file');

  let ch = null;
  try {
    ch = await startServer(DAV + path.delimiter + SECOND);

    // ── OPTIONS ──────────────────────────────────────────────────────
    const opt = await request('OPTIONS', '/dav/');
    check(opt.status === 200, 'OPTIONS 返回 200', `实际 ${opt.status}`);
    check(/1, ?2/.test(String(opt.headers.dav || '')), 'OPTIONS 带 DAV: 1, 2', String(opt.headers.dav));
    check(/PROPFIND/.test(String(opt.headers.allow || '')), 'OPTIONS 的 Allow 列出 PROPFIND');

    // ── PROPFIND 三种 Depth ───────────────────────────────────────────
    const d0 = await request('PROPFIND', '/dav/', { headers: { Depth: '0' } });
    check(d0.status === 207, 'PROPFIND Depth:0 返回 207', `实际 ${d0.status}`);
    check(countResponses(d0.body.toString()) === 1, 'Depth:0 只回自己 1 条', `实际 ${countResponses(d0.body.toString())}`);
    check(/<D:collection\/>/.test(d0.body.toString()), '根是 collection');

    const d1 = await request('PROPFIND', '/dav/', { headers: { Depth: '1' } });
    check(d1.status === 207, 'PROPFIND Depth:1 返回 207', `实际 ${d1.status}`);
    // 根 + a.txt + 子目录 = 3。两个软链都指向白名单外，**不应该**被列进来，
    // 这正是 resolveDav 的包含判定在起作用（早先漏判时这里是 5）。
    const d1n = countResponses(d1.body.toString());
    check(d1n === 3, 'Depth:1 只回白名单内的直接子项', `实际 ${d1n}（期望 3，软链已被排除）`);
    check(!/逃逸/.test(d1.body.toString()), 'Depth:1 不列出指向根外的软链');

    const dinf = await request('PROPFIND', '/dav/', { headers: { Depth: 'infinity' } });
    check(dinf.status === 207, 'PROPFIND Depth:infinity 返回 207', `实际 ${dinf.status}`);
    // 多出 子目录/b.txt 与两个软链指向的 outside 内容
    check(countResponses(dinf.body.toString()) > countResponses(d1.body.toString()),
      'Depth:infinity 比 Depth:1 更深', `${countResponses(dinf.body.toString())} vs ${countResponses(d1.body.toString())}`);
    check(/secret\.txt/.test(dinf.body.toString()) === false,
      'infinity 不会把软链目标的名字列进来（逃逸在解析阶段就挡了）');

    // 缺省 Depth 按 RFC 4918 是 infinity
    const ddef = await request('PROPFIND', '/dav/');
    check(ddef.status === 207, 'PROPFIND 不带 Depth 也能工作（缺省 infinity）', `实际 ${ddef.status}`);

    // ── GET / HEAD / Range ────────────────────────────────────────────
    const g = await request('GET', '/dav/a.txt');
    check(g.status === 200 && g.body.toString() === 'hello dav', 'GET 拿到文件内容', `${g.status} ${JSON.stringify(g.body.toString().slice(0, 20))}`);
    check(/text\/plain/.test(String(g.headers['content-type'])), 'GET 的 Content-Type 正确', String(g.headers['content-type']));

    const h = await request('HEAD', '/dav/a.txt');
    check(h.status === 200 && h.body.length === 0, 'HEAD 只回头不回体', `${h.status} 体长 ${h.body.length}`);
    check(Number(h.headers['content-length']) === 9, 'HEAD 的 Content-Length 正确', String(h.headers['content-length']));

    const r1 = await request('GET', '/dav/a.txt', { headers: { Range: 'bytes=0-4' } });
    check(r1.status === 206 && r1.body.toString() === 'hello', 'Range 返回 206 与分片内容', `${r1.status} ${JSON.stringify(r1.body.toString())}`);
    check(/bytes 0-4\/9/.test(String(r1.headers['content-range'])), 'Range 的 Content-Range 正确', String(r1.headers['content-range']));

    const r2 = await request('GET', '/dav/a.txt', { headers: { Range: 'bytes=100-200' } });
    check(r2.status === 416, '越界 Range 返回 416', `实际 ${r2.status}`);

    // ── PUT ──────────────────────────────────────────────────────────
    const p1 = await request('PUT', '/dav/新文件.txt', { body: 'from dav' });
    check(p1.status === 201, 'PUT 新文件返回 201', `实际 ${p1.status}`);
    check(fs.readFileSync(path.join(DAV, '新文件.txt'), 'utf8') === 'from dav', 'PUT 真的落盘了');

    const p2 = await request('PUT', '/dav/新文件.txt', { body: 'overwritten' });
    check(p2.status === 204, 'PUT 覆盖已有文件返回 204', `实际 ${p2.status}`);
    check(fs.readFileSync(path.join(DAV, '新文件.txt'), 'utf8') === 'overwritten', '覆盖内容正确');

    const p3 = await request('PUT', '/dav/子目录/x.txt', { body: 'x' });
    check(p3.status === 201, 'PUT 进子目录可用', `实际 ${p3.status}`);

    const p4 = await request('PUT', '/dav/不存在父/x.txt', { body: 'x' });
    check(p4.status === 409, 'PUT 到不存在的父目录返回 409', `实际 ${p4.status}`);

    // ── MKCOL ────────────────────────────────────────────────────────
    const m1 = await request('MKCOL', '/dav/新目录');
    check(m1.status === 201 && fs.existsSync(path.join(DAV, '新目录')), 'MKCOL 建目录返回 201 并落盘', `实际 ${m1.status}`);
    const m2 = await request('MKCOL', '/dav/新目录');
    check(m2.status === 405, 'MKCOL 已存在返回 405', `实际 ${m2.status}`);
    const m3 = await request('MKCOL', '/dav/没有的父/子');
    check(m3.status === 409, 'MKCOL 父不存在返回 409', `实际 ${m3.status}`);

    // ── DELETE ───────────────────────────────────────────────────────
    const x1 = await request('PUT', '/dav/待删.txt', { body: 'bye' });
    check(x1.status === 201, '（前置）PUT 待删.txt', `实际 ${x1.status}`);
    const d = await request('DELETE', '/dav/待删.txt');
    check(d.status === 204 && !fs.existsSync(path.join(DAV, '待删.txt')), 'DELETE 删掉文件', `实际 ${d.status}`);
    const d2 = await request('DELETE', '/dav/待删.txt');
    check(d2.status === 404, 'DELETE 不存在的文件返回 404', `实际 ${d2.status}`);
    const d3 = await request('DELETE', '/dav/新目录');
    check(d3.status === 204, 'DELETE 空目录可用', `实际 ${d3.status}`);
    const d4 = await request('DELETE', '/dav/子目录');
    check(d4.status === 409, 'DELETE 非空目录返回 409（拒绝递归删）', `实际 ${d4.status}`);
    const d5 = await request('DELETE', '/dav/');
    check(d5.status === 403, 'DELETE 白名单根返回 403', `实际 ${d5.status}`);

    // ── COPY / MOVE ───────────────────────────────────────────────────
    const cp = await request('COPY', '/dav/a.txt', { headers: { Destination: dest('副本.txt') } });
    check(cp.status === 201, 'COPY 返回 201', `实际 ${cp.status}`);
    check(fs.existsSync(path.join(DAV, '副本.txt')) && fs.existsSync(path.join(DAV, 'a.txt')), 'COPY 后源和目标都在');

    const cp2 = await request('COPY', '/dav/子目录', { headers: { Destination: dest('子目录副本') } });
    check(cp2.status === 201 && fs.existsSync(path.join(DAV, '子目录副本', 'b.txt')), 'COPY 递归复制目录', `实际 ${cp2.status}`);

    const ow0 = await request('COPY', '/dav/a.txt', { headers: { Destination: dest('副本.txt') } });
    check(ow0.status === 412, 'Overwrite 缺省即 F，撞已有目标返回 412', `实际 ${ow0.status}`);

    const owF = await request('COPY', '/dav/a.txt', { headers: { Destination: dest('副本.txt'), Overwrite: 'F' } });
    check(owF.status === 412, 'Overwrite: F 撞已有目标返回 412', `实际 ${owF.status}`);

    await request('PUT', '/dav/副本.txt', { body: 'OLD CONTENT' });
    const owT = await request('COPY', '/dav/a.txt', { headers: { Destination: dest('副本.txt'), Overwrite: 'T' } });
    check(owT.status === 204, 'Overwrite: T 覆盖已有目标返回 204', `实际 ${owT.status}`);
    check(fs.readFileSync(path.join(DAV, '副本.txt'), 'utf8') === 'hello dav', 'Overwrite: T 的内容真的换了');

    const mv = await request('MOVE', '/dav/副本.txt', { headers: { Destination: dest('搬走的.txt') } });
    check(mv.status === 201, 'MOVE 返回 201', `实际 ${mv.status}`);
    check(!fs.existsSync(path.join(DAV, '副本.txt')) && fs.existsSync(path.join(DAV, '搬走的.txt')), 'MOVE 后源消失目标出现');

    const noDest = await request('MOVE', '/dav/a.txt');
    check(noDest.status === 400, 'MOVE 缺 Destination 返回 400', `实际 ${noDest.status}`);

    const badDest = await request('MOVE', '/dav/a.txt', { headers: { Destination: `http://127.0.0.1:${PORT}/api/files` } });
    check(badDest.status === 403, 'Destination 在 /dav/ 之外返回 403', `实际 ${badDest.status}`);

    // 不能把集合搬进自己内部
    const intoSelf = await request('COPY', '/dav/子目录', { headers: { Destination: dest('子目录', '内部') } });
    // curl 这类客户端会把中文 Destination 按**原始 UTF-8 字节**塞进请求头
    //（规范要求 percent-encode，但现实中不照做的客户端不少）。Node 按 latin1 解头，
    // 不还原的话就会在旁边留一个乱码文件，而 MOVE 明明回的是 201。
    await request('PUT', '/dav/源文件.txt', { body: 'src payload' });
    const destRaw = 'http://127.0.0.1:' + PORT + '/dav/目标中文名.txt';
    const rr = await rawRequest('MOVE', '/dav/' + encodeURIComponent('源文件.txt'), ['Destination: ' + destRaw]);
    check(rr.status === 201, 'raw-UTF-8 的 Destination：MOVE 仍成功', '实际 ' + rr.status);
    const landed = await request('GET', '/dav/' + encodeURIComponent('目标中文名.txt'));
    check(landed.status === 200 && landed.body.toString() === 'src payload',
      'raw-UTF-8 的 Destination：内容真的搬到了正确的中文名', landed.status + ' ' + landed.body.toString().slice(0, 20));
    check(intoSelf.status === 403, 'COPY 集合到自身内部返回 403', `实际 ${intoSelf.status}`);

    // ── 第二个白名单根 ────────────────────────────────────────────────
    const root2 = await request('PROPFIND', `/dav/第二根/`, { headers: { Depth: '1' } });
    check(root2.status === 207, '第二个白名单根按名字可访问', `实际 ${root2.status}`);

    // ── 安全：穿越 / 软链逃逸 ─────────────────────────────────────────
    // 断言的是**安全属性**（4xx 且没漏出 /etc 内容），不钉死是哪一层拦的——
    // 这条路径上确实有两道独立的防线：
    //   1) WHATWG URL 解析器按 RFC 3986 消 dot 段（%2e%2e 这类形式也算），
    //      于是 /dav/%2e%2e/... 在到达任何处理器之前就变成了 /etc/...；
    //   2) ..%2f 那种混在单段里的不算 dot 段，会真的进 DAV 处理器，
    //      由 resolveDav 逐段 decode 后判非法路径段挡下。
    // 钉死某一层反而会让另一层悄悄退化也测不出来。
    const leaked = (r) => /root:|nobody:|BEGIN RSA|\$6\$/.test(r.body.toString());
    const t1 = await request('GET', '/dav/..%2f..%2fetc/passwd');
    check(t1.status === 403 && !leaked(t1), '穿越 · 混段 ..%2f：被 DAV 处理器拒绝且无泄露', `${t1.status} ${t1.body.toString().slice(0, 24)}`);
    const t2 = await request('GET', '/dav/%2e%2e/%2e%2e/etc/passwd');
    check(t2.status >= 400 && t2.status < 500 && !leaked(t2), '穿越 · %2e%2e：URL 层已折叠，无泄露', `${t2.status}`);
    const t3 = await request('PROPFIND', '/dav/子目录/%2e%2e/%2e%2e/etc/', { headers: { Depth: '1' } });
    check(t3.status >= 400 && t3.status < 500 && !leaked(t3), '穿越 · 夹在子目录之后：无泄露', `${t3.status}`);

    const s1 = await request('GET', `/dav/逃逸文件`);
    check(s1.status === 403, '软链指向根外的文件被挡（403）', `实际 ${s1.status}`);
    check(s1.body.toString().indexOf('TOP SECRET') < 0, '软链目标的内容没有泄露');

    const s2 = await request('PROPFIND', `/dav/逃逸链接/`, { headers: { Depth: '1' } });
    check(s2.status === 403, '软链指向根外的目录被挡（403）', `实际 ${s2.status}`);

    const s3 = await request('PUT', `/dav/逃逸链接/偷偷写入.txt`, { body: 'x' });
    check(s3.status === 403, '不能借软链往根外写', `实际 ${s3.status}`);
    check(!fs.existsSync(path.join(OUTSIDE, '偷偷写入.txt')), '根外确实没有出现新文件');

    // ── 未知方法 ─────────────────────────────────────────────────────
    const u1 = await request('PROPPATCH', '/dav/a.txt', { body: '<x/>' });
    check(u1.status === 405, '未实现的方法返回 405', `实际 ${u1.status}`);

    // ── 原有 /api 路由没被破坏 ───────────────────────────────────────
    const probeOk = await request('GET', '/api/files?path=' + DAV);
    const api = await request('GET', '/api/files?path=' + encodeURIComponent(DAV));
    check(api.status === 200, '原有 /api/files 路由仍然正常', `实际 ${api.status} body=${api.body.toString().slice(0, 80)}`);

    ch.kill('SIGKILL');
    await sleep(600);

    // ── 白名单置空 = 端点关闭 ─────────────────────────────────────────
    const ch2 = await startServer('');
    const off = await request('PROPFIND', '/dav/', { headers: { Depth: '1' } });
    check(off.status === 404, 'FM_DAV_ROOTS 置空时端点关闭（404）', `实际 ${off.status}`);
    ch2.kill('SIGKILL');
  } catch (e) {
    FAIL++;
    console.log('  FAIL 用例执行异常 —', e && e.stack || e);
  } finally {
    if (ch) { try { ch.kill('SIGKILL'); } catch { } }
    await fsp.rm(TMP, { recursive: true, force: true }).catch(() => { });
  }

  console.log(`\nWebDAV：${PASS} 通过 / ${FAIL} 失败`);
  process.exit(FAIL ? 1 : 0);
})();
