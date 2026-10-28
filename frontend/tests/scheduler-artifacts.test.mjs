import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

import { extractFinalText, extractArtifacts, artifactArgs } from "../scheduler.cjs";

// The shape below is copied from a real `/api/chat` `done` event, not invented:
// {messages:{data:{messages:[{role,contents:[{type,content}]}]}}}, where a
// show_result tool result is a JSON *string* carrying `path` and `ok`.
function donePayload(parts, extra = []) {
  return {
    messages: {
      data: {
        messages: [
          { role: "user", contents: [{ type: "text", content: "做个支架" }] },
          { role: "assistant", contents: [...extra, ...parts] },
        ],
      },
    },
  };
}

function showResult(obj) {
  return { type: "tool_result", name: "show_result", content: JSON.stringify(obj) };
}

const IMG = path.join(os.tmpdir(), "sched-test.png");
const ZIP = path.join(os.tmpdir(), "sched-test.zip");
fs.writeFileSync(IMG, Buffer.alloc(2048, 1));
fs.writeFileSync(ZIP, Buffer.alloc(2048, 2));

test("extractArtifacts 读出 show_result 交付的产物路径", () => {
  const payload = donePayload([
    { type: "text", content: "搞定" },
    showResult({ ok: true, kind: "file", path: IMG }),
  ]);
  assert.deepEqual(extractArtifacts(payload), [IMG]);
});

test("extractArtifacts 同一个文件出现多次只留一份", () => {
  const payload = donePayload([
    showResult({ ok: true, path: IMG }),
    showResult({ ok: true, path: IMG }),
  ]);
  assert.deepEqual(extractArtifacts(payload), [IMG]);
});

test("extractArtifacts 忽略失败的 show_result 与非 show_result 工具", () => {
  const payload = donePayload([
    showResult({ ok: false, error: "路径不合法", path: "/nope/x.png" }),
    { type: "tool_result", name: "bash", content: JSON.stringify({ ok: true, path: "/etc/passwd" }) },
    { type: "tool_result", name: "show_result", content: "这不是 JSON" },
  ]);
  assert.deepEqual(extractArtifacts(payload), []);
});

test("extractArtifacts 不从正文里猜路径", () => {
  // 文本里提到路径，但并没有真的 show_result 交付过——不该被当成产物。
  const payload = donePayload([
    { type: "text", content: `图在 ${IMG}` },
  ]);
  assert.deepEqual(extractArtifacts(payload), []);
});

test("artifactArgs 按扩展名分派图片与普通文件", () => {
  const { args, skipped } = artifactArgs([IMG, ZIP]);
  assert.deepEqual(args, ["--image", IMG, "--file", ZIP]);
  assert.deepEqual(skipped, []);
});

test("artifactArgs 跳过不存在和空文件，但不影响其它产物", () => {
  const { args, skipped } = artifactArgs(["/nope/missing.png", ZIP]);
  assert.deepEqual(args, ["--file", ZIP]);
  assert.deepEqual(skipped, ["/nope/missing.png"]);
});

test("artifactArgs 限制一次最多附带 4 个产物", () => {
  const many = Array.from({ length: 9 }, (_, i) => {
    const p = path.join(os.tmpdir(), `sched-many-${i}.png`);
    fs.writeFileSync(p, Buffer.alloc(16, i));
    return p;
  });
  const { args } = artifactArgs(many);
  assert.equal(args.length / 2, 4);
});

test("artifactArgs 对 undefined 安全（command 类任务没有 artifacts）", () => {
  assert.deepEqual(artifactArgs(undefined), { args: [], skipped: [] });
});

test("extractFinalText 把最后一条 assistant 的文本段拼起来（无回归）", () => {
  const payload = donePayload([
    { type: "text", content: "第一段" },
    { type: "text", content: "" },
    { type: "text", content: "第二段" },
  ]);
  assert.equal(extractFinalText(payload), "第一段第二段");
});

test("文本与产物能同时从同一个 payload 取到", () => {
  const payload = donePayload([
    { type: "text", content: "渲染完了" },
    showResult({ ok: true, path: IMG }),
  ]);
  assert.equal(extractFinalText(payload), "渲染完了");
  assert.deepEqual(extractArtifacts(payload), [IMG]);
});

test.after(() => {
  for (const p of [IMG, ZIP]) { try { fs.unlinkSync(p); } catch {} }
});
