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


async def _read_chunks(resp):
    """按任意边界吐字节块，绕开 readline 的单行上限。"""
    while True:
        chunk = await resp.content.readany()
        if not chunk:
            return
        yield chunk


def decode_sse_event(raw) -> dict | None:
    """把一行 SSE 文本解成事件 dict。非 data 行或解析失败返回 None。"""
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
    /api/chat 的 done 帧带整段对话历史，长会话极易超过 512KB，整轮会以
    `LineTooLong: 400, Got more than 524288 bytes` 收场——回复早已生成，
    只是读不出来。QQ 侧与微信侧踩的是同一个坑，因此用同一套修法。
    """
    buf = b""
    async for chunk in _read_chunks(resp):
        buf += chunk
        while b"\n" in buf:
            line, buf = buf.split(b"\n", 1)
            event = decode_sse_event(line)
            if event is not None:
                yield event
    tail = decode_sse_event(buf)
    if tail is not None:
        yield tail


def parse_sse_events(events) -> TurnResult:
    result = TurnResult()
    for event in events:
        kind = str(event.get("type") or "")
        if kind == "file_result":
            result.files.extend(event.get("files") or [])
        elif kind == "done":
            result.text = extract_final_text(event)
            result.completed = True
    return result


async def _post_chat(settings: QQSettings, body: dict, headers: dict, on_event=None):
    import aiohttp

    timeout = aiohttp.ClientTimeout(total=settings.turn_timeout_seconds)
    events: list[dict] = []
    async with aiohttp.ClientSession(timeout=timeout) as http:
        async with http.post(f"{settings.api_base}/api/chat", json=body, headers=headers) as resp:
            if resp.status != 200:
                raise classify_error(resp.status, await resp.text())
            async for event in iter_sse_events(resp):
                events.append(event)
                if on_event is not None:
                    # 必须 await：on_event 可能返回协程（QQ 侧发送要经
                    # Sequencer 记账），不 await 等于没调用，警告还会说
                    # "coroutine was never awaited"。
                    result = on_event(event)
                    if asyncio.iscoroutine(result):
                        await result
    return parse_sse_events(events)


def pick_interim_note(event: dict) -> str:
    """从 assistant 事件里挑一句可以先发出去的自然语言。

    与微信侧同源同语义：优先 `intermediate_description`（后端为中间轮
    专门算的进度说明），为空时回退 `content`——实测模型走纯工具调用
    （route=tool_only）时前者就是空串，而那恰恰是最该报进度的场景。

    最终轮（is_final=true）不参与：那一轮的完整内容会由 done 帧单独发出，
    先发一遍等于同一条消息在 QQ 里出现两次。
    """
    if str(event.get("type") or "") != "assistant":
        return ""
    if event.get("is_final"):
        return ""
    text = str(event.get("intermediate_description") or "").strip()
    if not text:
        text = str(event.get("content") or "").strip()
    return text


def collect_interim_note(events) -> str:
    """取本轮最后一条中间说明：越靠后越接近结论。"""
    note = ""
    for event in events:
        candidate = pick_interim_note(event)
        if candidate:
            note = candidate
    return note


class InterimNotifier:
    """按需推送中间说明，但同一句不重复发，且总条数封顶。

    与微信侧的关键差异是 `max_notes`。QQ 每个 msg_id 只有
    `max_passive_replies`（默认 5）次被动回复机会，最终回复还要占掉其中
    若干条；无节制地推中间说明会把额度耗光，导致主人最后连答案都收不到。
    所以这里限制最多推 2 条，剩下的进展宁可不说，也不能挤掉最终回复。
    """

    def __init__(self, emit, max_notes: int = 2):
        self._emit = emit
        self._max_notes = max(0, int(max_notes))
        self._last_sent = ""
        self._sent = 0

    def __call__(self, event: dict):
        """投递一条中间说明。

        返回 awaitable 而非直接发送：QQ 侧发送是异步的（要经 Sequencer 记
        账被动回复额度），微信侧是同步的。调用方用 `await notifier(event)`
        即可两种都兼容；早期版本在这里直接调用 async 回调，结果协程从未被
        await，表现为一条中间说明都发不出去。
        """
        if self._sent >= self._max_notes:
            return _completed()
        note = pick_interim_note(event)
        if not note or note == self._last_sent:
            return _completed()
        self._last_sent = note
        self._sent += 1
        try:
            result = self._emit(note)
        except TypeError:
            # 同步回调
            return _completed()
        if asyncio.iscoroutine(result):
            return result
        return _completed()

    @property
    def sent_count(self) -> int:
        return self._sent


async def _completed():
    return None


async def run_turn(session: str, message: str, settings: QQSettings, token: str = "",
                   channel: dict | None = None, sleep=asyncio.sleep,
                   *, on_interim=None) -> TurnResult:
    """Send one message, retrying while the account's daily session is busy.

    The busy retry lives here rather than in the bot so QQ and WeChat behave
    identically; `sleep` is injectable purely so tests need not really wait.
    `on_interim` 可选：收到中间说明时同步回调，用于先把短文本推出去，
    最终回复仍由返回值单独交付。
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
            return await _post_chat(settings, body, headers, on_event=on_interim)
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
