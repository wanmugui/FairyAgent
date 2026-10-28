// 事件游标的持久化。
//
// 背景：SSE 事件流用 event_id 做游标，前端靠它避免重复处理同一个事件。
// 原来游标只存在 React 的 useRef 里（纯内存），页面一刷新就全没了。
// 刷新后重连时 since=0，后端会把缓冲里的事件从头重放一遍，
// 于是 waiting_user_input 这类事件被处理第二次，弹窗又弹一次。
//
// 这里把游标存到 sessionStorage：刷新后仍在，标签页关闭即失效，
// 不会让一个早就结束的 run 在下次打开时误判成「还在流」。
const STORAGE_KEY = 'fairy.eventCursors.v1';

function readStoredCursors() {
  try {
    const raw = window.sessionStorage.getItem(STORAGE_KEY);
    if (!raw) return new Map();
    const parsed = JSON.parse(raw);
    if (!parsed || typeof parsed !== 'object') return new Map();
    return new Map(Object.entries(parsed));
  } catch {
    return new Map();
  }
}

function writeStoredCursors(map) {
  try {
    window.sessionStorage.setItem(STORAGE_KEY, JSON.stringify(Object.fromEntries(map)));
  } catch {
    // 隐私模式或配额满：退回纯内存行为，不影响功能。
  }
}

export function createEventCursorStore() {
  const cursors = readStoredCursors();
  return {
    get(key) {
      return cursors.get(key) || 0;
    },
    set(key, value) {
      cursors.set(key, value);
      writeStoredCursors(cursors);
    },
    clear() {
      cursors.clear();
      writeStoredCursors(cursors);
    },
  };
}
