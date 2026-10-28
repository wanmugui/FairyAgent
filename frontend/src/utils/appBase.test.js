import test from 'node:test';
import assert from 'node:assert/strict';
import { appBase, appPathname } from './appBase.js';

// 线上是 Caddy 挂 /agent 前缀（handle /agent/* + uri strip_prefix /agent），
// 浏览器里的 location.pathname 仍带这个前缀。任何拼绝对路径的地方都要先剥掉。

test('appBase detects the /agent mount prefix', () => {
  assert.equal(appBase('/agent'), '/agent');
  assert.equal(appBase('/agent/voice'), '/agent');
  assert.equal(appBase('/agent/timeline'), '/agent');
});

test('appBase is empty without the prefix', () => {
  assert.equal(appBase('/'), '');
  assert.equal(appBase('/voice'), '');
  assert.equal(appBase('/login.html'), '');
  // 前缀只认路径段，不能把 /agentx 当成前缀
  assert.equal(appBase('/agentx/voice'), '');
});

test('appPathname strips the mount prefix', () => {
  assert.equal(appPathname('/agent/voice'), '/voice');
  assert.equal(appPathname('/agent/timeline'), '/timeline');
  assert.equal(appPathname('/agent'), '/');
  assert.equal(appPathname('/agent/'), '/');
});

test('appPathname keeps paths without the prefix', () => {
  assert.equal(appPathname('/'), '/');
  assert.equal(appPathname('/voice'), '/voice');
  assert.equal(appPathname('/login.html'), '/login.html');
});
