"""回归测试：QQ 桥读 SSE 时撞 512KB 单行上限。

/api/chat 的 done 帧带整段对话历史，长会话极易超过 aiohttp
StreamReader.readline() 的硬编码上限（524288），整轮会以
`LineTooLong: 400, Got more than 524288 bytes` 收场。这些用例把修好的行为钉住。
"""
from __future__ import annotations

import asyncio
import json

from qqbot.fairy_client import (
    decode_sse_event,
    iter_sse_events,
    parse_sse_events,
)

ONE_MIB = 1024 * 1024


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


def test_done_frame_larger_than_512k_is_still_parsed():
    payload = _sse([_done_with_history(ONE_MIB, "最终回复在这里")])
    assert len(payload) > 524288, "用例本身必须超过旧的单行上限"

    async def run():
        return [e async for e in iter_sse_events(_FakeResp(payload))]

    result = parse_sse_events(asyncio.run(run()))
    assert result.completed is True
    assert result.text == "最终回复在这里"


def test_chunk_boundary_inside_event_does_not_drop_it():
    payload = _sse([
        {"type": "file_result", "files": [{"path": "/tmp/a.png"}]},
        _done_with_history(200 * 1024, "跨越 chunk 边界的回复"),
    ])

    async def run():
        return [e async for e in iter_sse_events(_FakeResp(payload, chunk_size=1000))]

    result = parse_sse_events(asyncio.run(run()))
    assert result.files == [{"path": "/tmp/a.png"}]
    assert result.text == "跨越 chunk 边界的回复"


def test_final_line_without_trailing_newline_is_not_lost():
    payload = b"data: " + json.dumps(_done_with_history(10, "没有换行结尾")).encode("utf-8")

    async def run():
        return [e async for e in iter_sse_events(_FakeResp(payload))]

    assert parse_sse_events(asyncio.run(run())).text == "没有换行结尾"


def test_decode_sse_event_ignores_noise_and_bad_json():
    assert decode_sse_event("event: ping") is None
    assert decode_sse_event("data: [DONE]") is None
    assert decode_sse_event("data: {不是合法 JSON") is None
    assert decode_sse_event('data: {"type":"assistant"}') == {"type": "assistant"}