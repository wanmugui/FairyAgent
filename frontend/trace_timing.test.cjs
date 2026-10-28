const assert = require('node:assert/strict');
const test = require('node:test');

const { mainTraceSpanMs, activeTraceSpanMs } = require('./trace_timing.cjs');


test("active span of a single turn equals the whole span", () => {
  assert.equal(activeTraceSpanMs([
    { event: "loop_start", timestamp: 1000 },
    { event: "llm_call", timestamp: 1500 },
    { event: "tool_result", timestamp: 3000 },
    { event: "loop_end", timestamp: 4000 },
  ]), 3000);
});

test("active span drops the idle gap between two user turns", () => {
  const hour = 3600 * 1000;
  assert.equal(activeTraceSpanMs([
    { event: "loop_start", timestamp: 1000 },
    { event: "tool_result", timestamp: 4000 },
    { event: "loop_end", timestamp: 5000 },
    { event: "loop_start", timestamp: 5000 + hour },
    { event: "llm_call", timestamp: 5000 + hour + 2000 },
    { event: "loop_end", timestamp: 5000 + hour + 3000 },
  ]), 4000 + 3000);
});

test("active span still drops idle when loop_end is never written", () => {
  // agent 只在正常收口/撞上限时写 loop_end，中途等待用户输入的那一轮没有 loop_end
  assert.equal(activeTraceSpanMs([
    { event: "loop_start", timestamp: 0 },
    { event: "waiting_user_input", timestamp: 4000 },
    { event: "loop_start", timestamp: 6000 },
    { event: "tool_result", timestamp: 9000 },
    { event: "loop_start", timestamp: 60000 },
    { event: "llm_call", timestamp: 62000 },
  ]), 9000);
});

test("active span falls back to the raw span when there is no loop_start", () => {
  assert.equal(activeTraceSpanMs([
    { timestamp: 1000 },
    { timestamp: 4500 },
  ]), 3500);
});

test("active span ignores empty input and bad timestamps", () => {
  assert.equal(activeTraceSpanMs([]), 0);
  assert.equal(activeTraceSpanMs(null), 0);
  assert.equal(activeTraceSpanMs([{ event: "loop_start" }, {}]), 0);
});
