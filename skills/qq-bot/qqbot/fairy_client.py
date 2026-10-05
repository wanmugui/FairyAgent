"""调用 Fairy 的 /api/chat 与 /api/bind/redeem 并解析 SSE。判断逻辑是纯函数，异步只在边界。"""
from __future__ import annotations

import asyncio
import json
from dataclasses import dataclass, field
from typing import Iterable

from .config import QQSettings, resolve_model

# One account, one daily session means QQ and the web UI now queue on the same
# turn. A busy answer is therefore a normal, transient condition, not a failure -
# but only briefly: a 409 that survives these retries is a turn that is genuinely
# running, and the message belongs *inside* it (see inject_message) rather than
# in a queue behind it. Waiting the old 15 seconds just delayed that decision.
BUSY_RETRY_DELAYS = (2, 2, 2)


class ChannelUnbound(RuntimeError):
    """The conversation has no account behind it yet. The model must not run."""


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


def build_chat_body(session: str, message: str, model: str, channel: dict | None = None) -> dict:
    """构造 /api/chat 请求体。服务端不提供默认模型，这里必须给出可用值。

    `channel` names the conversation this message came from. It is the only
    identity signal the bridge sends: the server decides which account owns the
    conversation, so the bridge never claims to be anybody.
    """
    resolved = str(model or "").strip()
    if not resolved:
        raise ValueError(
            "未解析出可用模型：请在 config/channels.json 的 qq.model 指定，"
            "或在 config/config.json 设置 settings.default_model"
        )
    body = {"message": message, "session": session, "model": resolved, "stream": True}
    if channel and channel.get("conversation_id"):
        declared = {
            "name": str(channel.get("name") or "qq"),
            "conversation_id": str(channel["conversation_id"]),
        }
        # `kind` only shapes the conversation's own session name
        # (qq-c2c-<id> vs qq-group-<id>); it never decides ownership.
        if str(channel.get("kind") or "").strip():
            declared["kind"] = str(channel["kind"]).strip()
        body["channel"] = declared
    return body


def classify_error(status: int, detail: str) -> Exception | None:
    """Map an HTTP answer onto the two conditions the caller must act on.

    Pure so the mapping is testable without a server: an unbound conversation
    and a busy session both look like "not 200" to a naive caller, and they
    demand opposite behaviour - refuse quietly versus wait and retry.
    """
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


def build_headers(token: str) -> dict:
    """构造 /api/chat 请求头。

    现在只是没有 `channel` 时的可选兜底：带 token 时 Fairy 按 Bearer 会话认出
    背后那个账户。带 `channel` 的调用不带这个头，归属完全交给服务端的绑定表
    ——静态 token 代表整只机器人，多成员时是错的。
    """
    resolved = str(token or "").strip()
    if not resolved:
        return {}
    return {"Authorization": f"Bearer {resolved}"}


async def _post_chat(settings: QQSettings, body: dict, headers: dict):
    import aiohttp

    timeout = aiohttp.ClientTimeout(total=settings.turn_timeout_seconds)
    lines: list[str] = []
    async with aiohttp.ClientSession(timeout=timeout) as http:
        async with http.post(f"{settings.api_base}/api/chat", json=body, headers=headers) as resp:
            if resp.status != 200:
                raise classify_error(resp.status, await resp.text())
            async for raw in resp.content:
                lines.append(raw.decode("utf-8", "ignore"))
    return parse_sse_stream(lines)


async def run_turn(session: str, message: str, settings: QQSettings, token: str = "",
                   channel: dict | None = None, sleep=asyncio.sleep) -> TurnResult:
    """Send one message, retrying while the account's daily session is busy.

    The busy retry lives here rather than in the bot so QQ and WeChat behave
    identically; `sleep` is injectable purely so tests need not really wait.
    """
    body = build_chat_body(session, message, resolve_model(settings), channel)
    # A channel turn must not carry a static token: the binding table is the only
    # authority, and a token would reintroduce the "one token, one owner" bug.
    headers = build_headers("" if channel else token)
    last: Exception | None = None
    for delay in (None, *BUSY_RETRY_DELAYS):
        if delay is not None:
            await sleep(delay)
        try:
            return await _post_chat(settings, body, headers)
        except SessionBusy as busy:
            last = busy
    raise last or SessionBusy("busy")


async def redeem_bind_code(code: str, settings: QQSettings, conversation_id: str,
                           channel: str = "qq") -> dict:
    """Exchange a one-shot bind code for an account, on behalf of this conversation."""
    import aiohttp

    timeout = aiohttp.ClientTimeout(total=settings.turn_timeout_seconds)
    payload = {"code": str(code or ""), "channel": channel, "conversation_id": str(conversation_id or "")}
    async with aiohttp.ClientSession(timeout=timeout) as http:
        async with http.post(f"{settings.api_base}/api/bind/redeem", json=payload) as resp:
            try:
                data = await resp.json(content_type=None)
            except Exception:  # noqa: BLE001 - a non-JSON body is simply a bad answer
                data = {}
            if not isinstance(data, dict):
                data = {}
            if resp.status != 200 or not data.get("ok"):
                return {"ok": False, "code": str(data.get("code") or "bad_code")}
            user = data.get("user") or {}
            return {"ok": True, "user": {"id": user.get("id"), "username": user.get("username")}}


async def inject_message(session: str, text: str, settings, token: str = "",
                         channel: dict | None = None) -> dict:
    """把新消息注入到当前会话的活跃回合。

    主前端 VoiceDock.jsx 在主进程 thinking 时就是这么干的：调 /api/chat/inject，
    Fairy 的 agent_controller 把这条消息压进 pending queue，下一轮 LLM 调用会
    把它和当前回合上下文一起消费。等价于「主人在 QQ 端又发了一条，Fairy 不用
    打断就能立刻响应新指令」。

    `channel` 必须带上：服务端据此把「某个会话的中途插入」解析成「该会话自己的
    分支会话」（`<日期>__qq-c2c-<id前8位>`）。桥自己那个 `qq:c2c:<openid>` 不是
    会话名，服务端不会拿它去找正在跑的回合（那是桥内部锁的键），带 channel 的
    调用也不该再带静态 token——归属只由绑定表决定。

    失败抛 RuntimeError，让调用方决定是 fallback cancel+新回合，还是告诉用户稍候。
    用 10 秒短超时：注入路径必须非阻塞，不能因为 Fairy 慢而把锁住的 QQ 回合挂死。
    """
    import aiohttp

    if not session or not text:
        raise RuntimeError("inject 需要 session 和 text")
    timeout = aiohttp.ClientTimeout(total=10)
    headers = build_headers("" if channel else token)
    payload = {"session": session, "text": text}
    if channel and channel.get("conversation_id"):
        declared = {
            "name": str(channel.get("name") or "qq"),
            "conversation_id": str(channel["conversation_id"]),
        }
        if str(channel.get("kind") or "").strip():
            declared["kind"] = str(channel["kind"]).strip()
        payload["channel"] = declared
    async with aiohttp.ClientSession(timeout=timeout) as http:
        async with http.post(
            f"{settings.api_base}/api/chat/inject",
            json=payload, headers=headers,
        ) as resp:
            try:
                data = await resp.json(content_type=None)
            except Exception:  # noqa: BLE001
                data = {}
            if resp.status != 200 or not (isinstance(data, dict) and data.get("ok")):
                raise RuntimeError(
                    f"inject 失败 HTTP {resp.status}: {(data or {}).get('error') or resp.reason}"
                )
            return data


async def follow_turn(session: str, settings: QQSettings, token: str = "",
                      channel: dict | None = None, run_id: str = "", since: int = 0,
                      timeout_seconds: float | None = None) -> TurnResult:
    """订阅一个正在跑的回合，等它结束并把最终结果取回来。

    注入只把主人的问题**送进**那个回合；回合的输出是发给「发起它的人」的
    （网页端那条 SSE），所以 QQ 这边必须自己订阅
    `/api/sessions/<name>/events` 才拿得到答案——主前端离开会话后也是靠它继续
    收进度的。事件流以 `data: [DONE]` 结束，格式与 /api/chat 完全一致，因此
    直接复用 parse_sse_stream。
    """
    import aiohttp
    from urllib.parse import quote

    params = {"since": str(max(0, int(since)))}
    if run_id:
        params["run_id"] = str(run_id)
    if channel and channel.get("conversation_id"):
        params["channel"] = str(channel.get("name") or "qq")
        params["conversation_id"] = str(channel["conversation_id"])
    headers = build_headers("" if channel else token)
    timeout = aiohttp.ClientTimeout(
        total=None,
        sock_connect=15,
        sock_read=float(timeout_seconds or settings.turn_timeout_seconds),
    )
    lines: list[str] = []
    url = f"{settings.api_base}/api/sessions/{quote(str(session), safe='')}/events"
    async with aiohttp.ClientSession(timeout=timeout) as http:
        async with http.get(url, params=params, headers=headers) as resp:
            if resp.status != 200:
                raise RuntimeError(f"订阅会话事件失败 HTTP {resp.status}: {(await resp.text())[:200]}")
            async for raw in resp.content:
                line = raw.decode("utf-8", "ignore").rstrip("\r\n")
                if not line.startswith("data:"):
                    continue
                lines.append(line)
                if line[5:].strip() == "[DONE]":
                    break
    return parse_sse_stream(lines)


async def cancel_session(session: str, settings, token: str = "") -> dict:
    """主动取消指定会话的活跃回合。前端在 inject 失败时的 fallback，QQ 桥暂未使用。"""
    import aiohttp

    if not session:
        raise RuntimeError("cancel 需要 session")
    timeout = aiohttp.ClientTimeout(total=10)
    headers = build_headers(token or "")
    async with aiohttp.ClientSession(timeout=timeout) as http:
        async with http.post(
            f"{settings.api_base}/api/chat/cancel",
            json={"session": session}, headers=headers,
        ) as resp:
            try:
                data = await resp.json(content_type=None)
            except Exception:  # noqa: BLE001
                data = {}
            if resp.status != 200 or not (isinstance(data, dict) and data.get("ok")):
                raise RuntimeError(
                    f"cancel 失败 HTTP {resp.status}: {(data or {}).get('error') or resp.reason}"
                )
            return data
