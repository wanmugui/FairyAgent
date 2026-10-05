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


async def _post_chat(settings: ChatSettings, body: dict):
    import aiohttp

    timeout = aiohttp.ClientTimeout(total=settings.turn_timeout_seconds)
    lines: list[str] = []
    async with aiohttp.ClientSession(timeout=timeout) as http:
        async with http.post(f"{settings.api_base}/api/chat", json=body) as resp:
            if resp.status != 200:
                raise classify_error(resp.status, await resp.text())
            async for raw in resp.content:
                lines.append(raw.decode("utf-8", "ignore"))
    return parse_sse_stream(lines)


async def run_turn(session: str, message: str, settings: ChatSettings,
                   conversation_id: str = "", sleep=asyncio.sleep,
                   *, files: list[dict] | None = None) -> TurnResult:
    """Send one message, waiting out a busy session instead of failing.

    `sleep` is injectable purely so tests need not really wait three seconds.
    """
    body = build_chat_body(session, message, resolve_model(settings), conversation_id, files)
    last: Exception | None = None
    for delay in (None, *BUSY_RETRY_DELAYS):
        if delay is not None:
            await sleep(delay)
        try:
            return await _post_chat(settings, body)
        except SessionBusy as busy:
            last = busy
    raise last or SessionBusy("busy")
