"""回归测试：SSE 读取撞 512KB 上限，以及“先短后长”回复。

背景：微信桥之前用 `async for raw in resp.content` 读 SSE，走的是
StreamReader.readline()，单行上限硬编码 524288 且客户端无法覆盖
（实测 post(max_line_size=4MB) 仍然抛 LineTooLong）。
/api/chat 的 done 帧带整段对话历史，长会话极易超过 512KB，整轮以
`LineTooLong: 400, Got more than 524288 bytes` 收场——回复其实早已生成，
只是读不出来。下面这些用例把该行为钉住。
"""
from __future__ import annotations

import asyncio
import json

from wechatbot.fairy_client import (
    InterimNotifier,
    collect_interim_note,
    decode_sse_event,
    iter_sse_events,
    parse_sse_events,
    pick_interim_note,
)

ONE_MIB = 1024 * 1024
LIMIT = 524288


class _FakeContent:
    """按 chunk 吐字节，chunk 边界故意错开，用来模拟真实 TCP 分块。"""

    def __init__(self, data: bytes, chunk_size: int):
        self._buf = data
        self._step = chunk_size

    async def readany(self) -> bytes:
        if not self._buf:
            return b""
        out, self._buf = self._buf[: self._step], self._buf[self._step:]
        return out


class _FakeResp:
    def __init__(self, data: bytes, chunk_size: int = 8192):
        self.content = _FakeContent(data, chunk_size)


def _sse(events: list[dict]) -> bytes:
    out = b""
    for event in events:
        out += ("\ndata: " + json.dumps(event, ensure_ascii=False) + "\n\n").encode("utf-8")
    return out


def _done_with_history(payload_bytes: int, final_text: str) -> dict:
    filler = "x" * payload_bytes
    return {
        "type": "done",
        "messages": {
            "data": {
                "messages": [
                    {"role": "user", "contents": [{"type": "text", "content": filler}]},
                    {"role": "assistant", "contents": [{"type": "text", "content": final_text}]},
                ]
            }
        },
    }


def _collect(resp) -> list[dict]:
    async def _run():
        return [event async for event in iter_sse_events(resp)]

    return asyncio.run(_run())


# ---------------------------------------------------------------- 读取层


def test_done_frame_larger_than_512k_is_still_parsed():
    """done 帧超过 512KB 时必须仍能读出最终回复。"""
    payload = _sse([_done_with_history(ONE_MIB, "最终回复在这里")])
    assert len(payload) > LIMIT, "用例本身必须超过旧的单行上限"

    result = parse_sse_events(_collect(_FakeResp(payload)))

    assert result.completed is True
    assert result.text == "最终回复在这里"


def test_chunk_boundary_inside_event_does_not_drop_it():
    """chunk 边界切在事件中间时，该事件不能被整段丢掉。"""
    payload = _sse([
        {"type": "file_result", "files": [{"path": "/tmp/a.png"}]},
        _done_with_history(200 * 1024, "跨越 chunk 边界的回复"),
    ])

    result = parse_sse_events(_collect(_FakeResp(payload, chunk_size=1000)))

    assert result.files == [{"path": "/tmp/a.png"}]
    assert result.text == "跨越 chunk 边界的回复"


def test_final_line_without_trailing_newline_is_not_lost():
    """结尾没有换行符时，最后一条事件仍要产出。"""
    payload = b"data: " + json.dumps(_done_with_history(10, "没有换行结尾")).encode("utf-8")

    assert parse_sse_events(_collect(_FakeResp(payload))).text == "没有换行结尾"


def test_decode_sse_event_ignores_noise_and_bad_json():
    assert decode_sse_event("event: ping") is None
    assert decode_sse_event("data: [DONE]") is None
    assert decode_sse_event("data: {不是合法 JSON") is None
    assert decode_sse_event("data: ") is None
    assert decode_sse_event('data: {"type":"assistant"}') == {"type": "assistant"}


# ------------------------------------------------------- 先短后长：选材


def test_interim_note_comes_from_intermediate_description():
    """中间说明优先取后端算好的 intermediate_description。"""
    assert pick_interim_note({
        "type": "assistant",
        "is_final": False,
        "content": "这里有工具调用细节",
        "intermediate_description": "我先查一下今天的日期",
    }) == "我先查一下今天的日期"


def test_interim_note_falls_back_to_content_when_description_empty():
    """纯工具轮里 intermediate_description 实测为空，要回退到 content。

    这是真机观测到的行为：模型走 route=tool_only 时选择不说话，
    intermediate_description 就是 ""。而那恰恰是最该先给用户报个进度的
    场景，所以必须回退，否则工具密集的长任务全程没有中间反馈。
    """
    assert pick_interim_note({
        "type": "assistant",
        "is_final": False,
        "route": "tool_only",
        "intermediate_description": "",
        "content": "我先看看今天几号",
    }) == "我先看看今天几号"


def test_interim_note_empty_when_neither_field_has_text():
    assert pick_interim_note({
        "type": "assistant",
        "is_final": False,
        "intermediate_description": "",
        "content": "   ",
    }) == ""


def test_final_assistant_turn_is_never_sent_as_interim():
    """最终轮不参与先发，否则同一句会在微信里出现两次。"""
    assert pick_interim_note({
        "type": "assistant",
        "is_final": True,
        "content": "最终回复全文",
        "intermediate_description": "这是最终回复",
    }) == ""


def test_non_assistant_events_yield_no_note():
    assert pick_interim_note({"type": "tool", "name": "bash"}) == ""
    assert pick_interim_note({"type": "done"}) == ""


def test_collect_interim_note_takes_the_latest():
    """越靠后的中间轮越接近结论，取最后一条。"""
    assert collect_interim_note([
        {"type": "assistant", "is_final": False, "intermediate_description": "先做第一步"},
        {"type": "assistant", "is_final": False, "intermediate_description": "接着做第二步"},
        {"type": "assistant", "is_final": True, "intermediate_description": "最终回复"},
    ]) == "接着做第二步"


# ------------------------------------------------------- 先短后长：节流


def test_notifier_dedupes_identical_notes():
    """同一句重复出现只发一次，长任务不该把微信刷爆。"""
    sent: list[str] = []
    notify = InterimNotifier(sent.append, min_interval=45.0)

    for _ in range(3):
        notify({"type": "assistant", "is_final": False, "intermediate_description": "同一个进度"})

    assert sent == ["同一个进度"]


def test_notifier_sends_first_immediately_then_throttles():
    """首条必须立刻发；之后受最小间隔约束，不会连发。"""
    sent: list[str] = []
    notify = InterimNotifier(sent.append, min_interval=45.0)

    notify({"type": "assistant", "is_final": False, "intermediate_description": "第一条进度"})
    notify({"type": "assistant", "is_final": False, "intermediate_description": "第二条进度"})

    assert sent == ["第一条进度"]


def test_notifier_ignores_events_without_note():
    sent: list[str] = []
    notify = InterimNotifier(sent.append, min_interval=45.0)

    notify({"type": "done"})
    notify({"type": "assistant", "is_final": True, "intermediate_description": "x"})

    assert sent == []


def test_interim_and_final_are_both_delivered_in_order():
    """先短后长：中间说明先到，最终回复由 done 帧单独交付。"""
    payload = _sse([
        {"type": "assistant", "is_final": False, "intermediate_description": "我先查一下"},
        _done_with_history(ONE_MIB, "这是最终完整回复"),
    ])
    assert len(payload) > LIMIT

    events = _collect(_FakeResp(payload))

    seen = [pick_interim_note(event) for event in events]
    result = parse_sse_events(events)

    assert [s for s in seen if s] == ["我先查一下"]
    assert result.text == "这是最终完整回复"