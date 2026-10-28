import test from 'node:test';
import assert from 'node:assert/strict';

// sessionStorage 不存在于 node 环境，给一个最小的内存实现，
// 用来验证游标在「页面刷新」后仍然可用。
function installSessionStorage() {
  const store = new Map();
  global.window = {
    sessionStorage: {
      getItem: (k) => (store.has(k) ? store.get(k) : null),
      setItem: (k, v) => { store.set(k, String(v)); },
      removeItem: (k) => { store.delete(k); },
      clear: () => { store.clear(); },
    },
  };
  return store;
}

test('event cursor survives a page reload', async () => {
  installSessionStorage();
  const { createEventCursorStore } = await import('./eventCursorStore.js');

  const first = createEventCursorStore();
  first.set('session\0run-1', 412);
  assert.equal(first.get('session\0run-1'), 412);

  // 模拟刷新：新的 store 实例，但 sessionStorage 还在。
  const afterReload = createEventCursorStore();
  assert.equal(
    afterReload.get('session\0run-1'),
    412,
    '刷新后游标必须仍在，否则后端会重放全部事件，ask_user 弹窗会再弹一次',
  );
});

test('unknown cursor key reads as 0', async () => {
  installSessionStorage();
  const { createEventCursorStore } = await import('./eventCursorStore.js');
  const store = createEventCursorStore();
  assert.equal(store.get('never-seen'), 0);
});

test('corrupted storage degrades to an empty store', async () => {
  installSessionStorage();
  global.window.sessionStorage.setItem('fairy.eventCursors.v1', '{not json');
  const { createEventCursorStore } = await import('./eventCursorStore.js?corrupt=1');
  const store = createEventCursorStore();
  assert.equal(store.get('any'), 0);
});
