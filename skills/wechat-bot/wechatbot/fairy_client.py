"""调用 Fairy 的 /api/chat 并解析 SSE。判断逻辑是纯函数，异步只在边界。"""
from __future__ import annotations

import asyncio
import json
from dataclasses import dataclass, field
from typing import Iterable

from .config import ChatSettings, resolve_model

# A bound WeChat account shares one daily session with the web UI and QQ, so a
# busy answer is a normal queueing condition rather than an outage. The caller
# gets the same wait-and-retry treatment QQ gets.
BUSY_RETRY_DELAYS = (3, 3, 3, 3, 3)


class ChannelUnbound(RuntimeError):
    """The WeChat account behind this bridge has no Fairy account yet."""


class SessionBusy(RuntimeError):
    """Another turn already holds this account's daily session."""


@dataclass
class TurnResult:
    text: str = ""
    files: list[dict] = field(default_factory=list)
    completed: bool = False


def extract_final_text(done_payload: dict) -> str:
    """取最后一条 assistant 消息里 type == 'text' 的内容。"""
    messages = (((done_payload or {}).get("messages") or {}).get("data") or {}).get("messages") or []
    for message in reversed(messages):
        if str(message.get("role")) != "assistant":
            continue
        parts = [str(c.get("content") or "")
                 for c in (message.get("contents") or [])
                 if str(c.get("type")) == "text"]
        text = "".join(parts).strip()
        if text:
            return text
    return ""


def parse_sse_stream(lines: Iterable[str]) -> TurnResult:
    result = TurnResult()
    for raw in lines:
        line = str(raw or "").strip()
        if not line.startswith("data:"):
            continue
        payload = line[len("data:"):].strip()
        if not payload or payload == "[DONE]":
            continue
        try:
            event = json.loads(payload)
        except (TypeError, ValueError):
            continue
        kind = str(event.get("type") or "")
        if kind == "file_result":
            result.files.extend(event.get("files") or [])
        elif kind == "done":
            result.text = extract_final_text(event)
            result.completed = True
    return result


def build_chat_body(session: str, message: str, model: str, conversation_id: str = "",
                    files: list[dict] | None = None) -> dict:
    """构造 /api/chat 请求体。服务端不提供默认模型，这里必须给出可用值。

    The WeChat account is reported as the conversation id. It is the only
    identity the bridge sends, because a bare local call is the machine owner by
    default - which is how a second person's messages used to land in the first
    account's transcript.
    """
    resolved = str(model or "").strip()
    if not resolved:
        raise ValueError(
            "未解析出可用模型：请在 config/channels.json 的 qq.model 指定，"
            "或在 config/config.json 设置 settings.default_model"
        )
    body = {"message": message, "session": session, "model": resolved, "stream": True}
    # 附件（微信图片下载解密后落盘的路径）：服务端会把它写进 <file_context>，
    # 前端据此显示附件，模型也能顺着路径去看图。
    if files:
        body["files"] = [
            {"path": str(item.get("path")), "name": str(item.get("name") or "")}
            for item in files if (item or {}).get("path")
        ]
    if str(conversation_id or "").strip():
        body["channel"] = {"name": "wechat", "conversation_id": str(conversation_id).strip()}
    return body


def classify_error(status: int, detail: str) -> Exception | None:
    """Split "not bound" from "busy" so the caller can refuse and wait respectively."""
    text = str(detail or "")
    try:
        payload = json.loads(text)
    except (TypeError, ValueError):
        payload = {}
    if not isinstance(payload, dict):
        payload = {}
    if str(payload.get("code") or "") == "unbound" or status == 403:
        return ChannelUnbound(text[:200] or "unbound")
    if status == 409 or payload.get("retry") is True:
        return SessionBusy(text[:200] or "busy")
    return RuntimeError(f"Fairy API {status}: {text[:200]}") if status != 200 else None


async def _read_chunks(resp):
    """按任意边界吐字节块，绕开 readline 的单行上限。"""
    while True:
        chunk = await resp.content.readany()
        if not chunk:
            return
        yield chunk


def decode_sse_event(raw) -> dict | None:
    """把一行 SSE 文本解成事件 dict。不是 data 行或解析失败都返回 None。"""
    line = raw.decode("utf-8", "ignore") if isinstance(raw, (bytes, bytearray)) else str(raw or "")
    line = line.strip()
    if not line.startswith("data:"):
        return None
    payload = line[len("data:"):].strip()
    if not payload or payload == "[DONE]":
        return None
    try:
        event = json.loads(payload)
    except (TypeError, ValueError):
        return None
    return event if isinstance(event, dict) else None


async def iter_sse_events(resp):
    """逐事件产出 SSE dict，不受 aiohttp 单行长度限制。

    为什么不用 `async for line in resp.content`：那条路走
    StreamReader.readline()，其 max_line_length 硬编码 524288 且客户端
    无法覆盖（实测 post(max_line_size=4MB) 照样抛 LineTooLong）。
    /api/chat 的 `done` 帧携带整段对话历史，长会话极易超过 512KB，
    于是整轮以 `LineTooLong: 400, Got more than 524288 bytes` 收场——
    回复其实早已生成完毕，只是读不出来。改用 readany() 自行按换行切分，
    跨块半行由 buf 拼回。
    """
    buf = b""
    async for chunk in _read_chunks(resp):
        buf += chunk
        while b"\n" in buf:
            line, buf = buf.split(b"\n", 1)
            event = decode_sse_event(line)
            if event is not None:
                yield event
    # 结尾没有换行符的残块也要过一遍，否则最后一条事件会被丢掉。
    tail = decode_sse_event(buf)
    if tail is not None:
        yield tail


async def _post_chat(settings: ChatSettings, body: dict, on_event=None):
    import aiohttp

    timeout = aiohttp.ClientTimeout(total=settings.turn_timeout_seconds)
    events: list[dict] = []
    async with aiohttp.ClientSession(timeout=timeout) as http:
        async with http.post(f"{settings.api_base}/api/chat", json=body) as resp:
            if resp.status != 200:
                raise classify_error(resp.status, await resp.text())
            async for event in iter_sse_events(resp):
                events.append(event)
                # 先短后长：边收边把自然语言片段交给上层，
                # 让人不用干等整轮结束才看到第一句。
                if on_event is not None:
                    on_event(event)
    return parse_sse_events(events)


def parse_sse_events(events: Iterable[dict]) -> TurnResult:
    result = TurnResult()
    for event in events:
        kind = str(event.get("type") or "")
        if kind == "file_result":
            result.files.extend(event.get("files") or [])
        elif kind == "done":
            result.text = extract_final_text(event)
            result.completed = True
    return result


def pick_interim_note(event: dict) -> str:
    """从 assistant 事件里挑一句可以先发出去的自然语言。

    走 `intermediate_description` 而不是自己截 `content`：那是后端在
    agentloop 里为中间轮（带工具调用的那些）单独算好的字段，内容就是模型
    那一轮自己写的自然语言进度说明。前端拿它渲染"正在做什么"，语义天然吻合，
    不需要桥再猜哪些句子适合单独发。

    最终轮（is_final=true）不参与：那一轮的完整内容会由 done 帧单独发出，
    先发一遍等于让同一条消息在微信里出现两次。
    """
    if str(event.get("type") or "") != "assistant":
        return ""
    if event.get("is_final"):
        return ""
    # 优先用 intermediate_description：那是后端为中间轮单独算好的进度说明。
    # 但它常常是空的——实测一轮纯工具调用时（route=tool_only）模型选择
    # 不说话，该字段就是 ""。而那恰恰是最该让用户知道"它在动"的场景，
    # 所以回退到该轮 content：只要模型在中间轮开了口，那就是给用户看的话。
    text = str(event.get("intermediate_description") or "").strip()
    if not text:
        text = str(event.get("content") or "").strip()
    return text


def collect_interim_note(events: Iterable[dict]) -> str:
    """取本轮最后一条中间说明。取最后一条是因为越靠后越接近结论。"""
    note = ""
    for event in events:
        candidate = pick_interim_note(event)
        if candidate:
            note = candidate
    return note


class InterimNotifier:
    """按需推送中间说明，但同一句不重复发。

    长任务里 agent 会连续吐多条中间说明。微信端每条都推会把聊天刷爆，
    所以做两件事：去重（内容完全相同就跳过），以及节流（至少间隔
    settings.interim_min_interval_seconds 才允许再发一条）。
    """

    def __init__(self, emit, min_interval: float = 45.0):
        self._emit = emit
        self._min_interval = float(min_interval)
        self._last_sent = ""
        self._sent_any = False
        self._monotonic = 0.0

    def __call__(self, event: dict) -> None:
        note = pick_interim_note(event)
        if not note or note == self._last_sent:
            return
        self._monotonic += 1.0
        # 首条立刻发；之后的按事件计数粗略节流，避免刷屏。
        if self._sent_any and self._monotonic < self._min_interval:
            return
        self._last_sent = note
        self._sent_any = True
        self._monotonic = 0.0
        self._emit(note)


async def run_turn(session: str, message: str, settings: ChatSettings,
                   conversation_id: str = "", sleep=asyncio.sleep,
                   *, files: list[dict] | None = None,
                   on_interim=None) -> TurnResult:
    """Send one message, waiting out a busy session instead of failing.

    `sleep` is injectable purely so tests need not really wait three seconds.
    `on_interim` 可选：收到中间说明时同步回调，用来先把短文本发出去，
    最终回复仍由返回值单独交付。
    """
    body = build_chat_body(session, message, resolve_model(settings), conversation_id, files)
    last: Exception | None = None
    for delay in (None, *BUSY_RETRY_DELAYS):
        if delay is not None:
            await sleep(delay)
        try:
            return await _post_chat(settings, body, on_event=on_interim)
        except SessionBusy as busy:
            last = busy
    raise last or SessionBusy("busy")
