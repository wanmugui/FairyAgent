"""QQ 桥的"先短后长"回归测试。

与微信侧同源，但多一条硬约束：QQ 每个 msg_id 只有 max_passive_replies
（默认 5）次被动回复机会，最终回复还要占掉其中若干条。所以中间说明
必须封顶，否则主人最后连答案都收不到。下面重点钉住这个上限。

所有 notifier 调用都走 `await notifier(event)`：QQ 侧发送是异步的，
而 InterimNotifier 返回 awaitable 以同时兼容同步/异步回调。早期版本
在这里直接同步调用 async 回调，协程从未被 await，实际一条都发不出去。
"""
from __future__ import annotations

import asyncio
import json

from qqbot.fairy_client import (
    InterimNotifier,
    collect_interim_note,
    iter_sse_events,
    parse_sse_events,
    pick_interim_note,
)

ONE_MIB = 1024 * 1024
LIMIT = 524288


class _FakeContent:
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
    return {
        "type": "done",
        "messages": {
            "data": {
                "messages": [
                    {"role": "user", "contents": [{"type": "text", "content": "x" * payload_bytes}]},
                    {"role": "assistant", "contents": [{"type": "text", "content": final_text}]},
                ]
            }
        },
    }


def _collect(resp) -> list[dict]:
    async def _run():
        return [event async for event in iter_sse_events(resp)]

    return asyncio.run(_run())


def _note(text: str) -> dict:
    return {"type": "assistant", "is_final": False, "intermediate_description": text}


# ---------------------------------------------------------------- 选材


def test_interim_note_prefers_intermediate_description():
    assert pick_interim_note({
        "type": "assistant",
        "is_final": False,
        "content": "工具细节",
        "intermediate_description": "我先查一下今天的日期",
    }) == "我先查一下今天的日期"


def test_interim_note_falls_back_to_content_for_tool_only_turn():
    """route=tool_only 时 intermediate_description 实测为空，必须回退 content。"""
    assert pick_interim_note({
        "type": "assistant",
        "is_final": False,
        "route": "tool_only",
        "intermediate_description": "",
        "content": "我先看看今天几号",
    }) == "我先看看今天几号"


def test_final_turn_is_never_sent_as_interim():
    assert pick_interim_note({
        "type": "assistant",
        "is_final": True,
        "content": "最终回复全文",
    }) == ""


def test_non_assistant_events_yield_no_note():
    assert pick_interim_note({"type": "tool", "name": "bash"}) == ""
    assert pick_interim_note({"type": "done"}) == ""


def test_collect_interim_note_takes_the_latest():
    assert collect_interim_note([
        {"type": "assistant", "is_final": False, "intermediate_description": "第一步"},
        {"type": "assistant", "is_final": False, "intermediate_description": "第二步"},
        {"type": "assistant", "is_final": True, "intermediate_description": "最终"},
    ]) == "第二步"


# -------------------------------------------------- 额度上限（QQ 特有）


def test_notifier_respects_max_notes_cap():
    """中间说明必须封顶，把被动回复额度留给最终回复。"""
    sent: list[str] = []
    notify = InterimNotifier(sent.append, max_notes=2)

    async def _run():
        for i in range(10):
            await notify(_note(f"进度{i}"))

    asyncio.run(_run())
    assert sent == ["进度0", "进度1"]


def test_notifier_with_zero_cap_never_sends():
    sent: list[str] = []
    notify = InterimNotifier(sent.append, max_notes=0)

    asyncio.run(_run_notify(notify, [_note("任何进度")]))
    assert sent == []


def test_notifier_dedupes_identical_notes():
    sent: list[str] = []
    notify = InterimNotifier(sent.append, max_notes=3)

    asyncio.run(_run_notify(notify, [_note("同一个进度")] * 3))
    assert sent == ["同一个进度"]


def test_notifier_cap_and_dedupe_together():
    """重复句不占额度，额度只在真正不同的内容上消耗。"""
    sent: list[str] = []
    notify = InterimNotifier(sent.append, max_notes=2)

    asyncio.run(_run_notify(notify, [_note("A"), _note("A"), _note("B"), _note("C")]))
    assert sent == ["A", "B"]


def test_notifier_awaits_async_callback():
    """异步回调必须被真正 await，否则中间说明一条也发不出去。"""
    sent: list[str] = []

    async def emit(text: str) -> None:
        await asyncio.sleep(0)
        sent.append(text)

    notify = InterimNotifier(emit, max_notes=2)
    asyncio.run(_run_notify(notify, [_note("异步进度")]))

    assert sent == ["异步进度"]


def test_notifier_supports_sync_callback():
    sent: list[str] = []
    notify = InterimNotifier(sent.append, max_notes=2)
    asyncio.run(_run_notify(notify, [_note("同步进度")]))
    assert sent == ["同步进度"]


# --------------------------------------------------------- 读取层 + 顺序


def test_interim_then_final_order_with_large_done_frame():
    """先短后长，且 done 帧超 512KB 也不影响。"""
    payload = _sse([
        _note("我先查一下"),
        _note("再看看磁盘"),
        _done_with_history(ONE_MIB, "这是最终完整回复"),
    ])
    assert len(payload) > LIMIT

    events = _collect(_FakeResp(payload))

    sent: list[str] = []
    notify = InterimNotifier(sent.append, max_notes=2)
    asyncio.run(_run_notify(notify, events))

    result = parse_sse_events(events)

    assert sent == ["我先查一下", "再看看磁盘"]
    assert result.text == "这是最终完整回复"


def test_large_done_frame_still_parsed():
    payload = _sse([_done_with_history(ONE_MIB, "最终回复在这里")])
    assert len(payload) > LIMIT
    result = parse_sse_events(_collect(_FakeResp(payload)))
    assert result.completed is True
    assert result.text == "最终回复在这里"


# ------------------------------------------------------------- helpers


async def _run_notify(notify, events) -> None:
    for event in events:
        await notify(event)
