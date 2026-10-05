// trace 时间口径：主线程墙钟跨度 / 扣除「轮间用户空档」后的活跃跨度。
//
// 约定：agent 每进入一次 loop 都会写 loop_start（含 messages_count / step），
// 但 loop_end 只在正常收口（no_tool_calls）或撞 loop_limit 时才写，
// 所以这里只用 loop_start 切分轮次，不依赖 loop_end。
// 服务端 CJS 同源实现见 frontend/trace_timing.cjs，改动请同步。

export function mainTraceSpanMs(events) {
  let first = Infinity;
  let last = -Infinity;
  for (const event of events || []) {
    const timestamp = event && event.timestamp;
    if (typeof timestamp !== 'number' || !Number.isFinite(timestamp)) continue;
    if (timestamp < first) first = timestamp;
    if (timestamp > last) last = timestamp;
  }
  return Number.isFinite(first) && Number.isFinite(last) ? Math.max(0, last - first) : 0;
}

// 多轮会话里「上一轮最后一个事件 → 下一轮 loop_start」之间是用户离开的空档，
// 不该算进耗时（否则跨度会被用户挂机的时间撑大）。逐轮扣掉这些空档；
// 拿不到 loop_start（或扣完结果异常）时退回首末事件跨度。
// 注意：传入的必须是同一条 trace（主线程），不要把子任务事件混进来。
export function activeTraceSpanMs(events) {
  const list = (events || []).filter(e => e && typeof e.timestamp === 'number' && Number.isFinite(e.timestamp));
  if (!list.length) return 0;
  const sorted = list.slice().sort((a, b) => a.timestamp - b.timestamp);
  const span = Math.max(0, sorted[sorted.length - 1].timestamp - sorted[0].timestamp);
  let prevTs = null;
  let seenStart = 0;
  let idle = 0;
  for (const e of sorted) {
    if (e.event === 'loop_start') {
      seenStart++;
      if (seenStart > 1 && prevTs != null) idle += Math.max(0, e.timestamp - prevTs);
    }
    prevTs = e.timestamp;
  }
  if (seenStart < 2) return span;
  const active = span - idle;
  return (active > 0 && active <= span) ? active : span;
}
