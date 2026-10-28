// Author: Harry Qiu
// filemanager 的开发期热重载包装。
//
// 为什么需要它：Node 的 require 会把模块缓存进模块注册表，server.cjs 只在启动时
// require 一次本文件。所以改 filemanager 的后端实现后，线上不会生效，必须重启整个
// 8081 —— 而 8081 是 agent 进程的父进程，重启代价极大，还会打断会话。
//
// 这里把真正的实现挪到 fm-impl.cjs，本文件只做一层按 mtime 判定的转发：
// 实现文件变了就丢弃 require 缓存重新加载，之后改后端无需重启服务。
// 只在开发/本地实例生效（FAIRY_FM_HOTRELOAD=0 可关闭）。
'use strict';

const fs = require('fs');
const path = require('path');

const IMPL = path.join(__dirname, 'fm-impl.cjs');
const HOT = process.env.FAIRY_FM_HOTRELOAD !== '0';

let cachedImpl = null;
let cachedMtime = 0;

function load() {
  if (!HOT) return require(IMPL);
  let mtime = 0;
  try {
    mtime = fs.statSync(IMPL).mtimeMs;
  } catch (e) {
    mtime = 0;
  }
  if (!cachedImpl || mtime !== cachedMtime) {
    // 删缓存后重新 require 才会真正重新执行实现文件。
    try { delete require.cache[require.resolve(IMPL)]; } catch (e) { /* 首次加载还没有缓存 */ }
    cachedImpl = require(IMPL);
    cachedMtime = mtime;
  }
  return cachedImpl;
}

// 透明转发任意导出：server.cjs 访问 fm.tryHandleFileManager 时才解析到当前实现。
module.exports = new Proxy({}, {
  get(_t, key) {
    const impl = load();
    const v = impl[key];
    return typeof v === 'function' ? v.bind(impl) : v;
  },
  has(_t, key) { return key in load(); },
  ownKeys() { return Reflect.ownKeys(load()); },
  getOwnPropertyDescriptor(_t, key) {
    const d = Object.getOwnPropertyDescriptor(load(), key);
    return d ? { ...d, configurable: true } : undefined;
  },
});
