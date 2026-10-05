"""botpy 客户端：把 QQ 事件接到 Fairy，并把结果发回去。"""
from __future__ import annotations

import asyncio
import logging
import socket
from pathlib import Path

import aiohttp
import botpy
from botpy import Intents

from .config import QQSettings
from .fairy_client import (ChannelUnbound, SessionBusy, TurnResult, follow_turn,
                           inject_message, redeem_bind_code, run_turn)
from .media import describe_unsendable, pick_sendable
from .policy import C2C, GROUP, format_inbound, is_allowed, parse_bind_command, session_name
from .push import upload_media
from .sender import ACK_TEXT, Sequencer, plan_texts

log = logging.getLogger("qq-bot")

# Spoken answers, not UI copy: these are read in a chat window on a phone.
UNBOUND_HINT = (
    "你还没有绑定 Fairy 账号。请在这台机器的 Fairy 设置 → 账号 → 通道绑定 里生成绑定码，"
    "然后给我发：/bind <绑定码>"
)
BAD_CODE_HINT = "绑定码无效或已过期，请在 Fairy 设置 → 账号 → 通道绑定 里重新生成"
BOUND_OK = "绑定成功，你现在可以用 QQ 指挥 Fairy 了"
BUSY_HINT = "我正在处理上一个请求，稍后再发一次"
INSERTED_HINT = "收到，已插入当前回合，结果稍后发给你"
MID_TURN_REJECT_HINT = "上一条还在处理中，请稍后再发一次。"
# 被动回复的 msg_seq 是按 msg_id 记的，窗口只有 5 分钟；几百条之后旧条目再也
# 不会被用到，直接按插入顺序淘汰，避免一张永远增长的表。
SEQ_CACHE_LIMIT = 512

# 被动回复窗口只有 5 分钟，一次 DNS 抖动 / 连接超时就会让主人的消息彻底消失。
# 只对「请求根本没到 QQ 平台」的错误重试；平台已明确拒绝的（4xx/5xx，含 40054005
# 去重）重试没有意义，只会拖慢后面的消息。
SEND_RETRY_ATTEMPTS = 3
SEND_RETRY_BACKOFF = (0.6, 1.8)


def _is_transient(exc: BaseException) -> bool:
    """判断异常是否为可重试的传输层故障（请求大概率没被平台处理过）。"""
    if isinstance(exc, (aiohttp.ClientConnectionError, aiohttp.ClientConnectorError,
                        aiohttp.ServerTimeoutError, asyncio.TimeoutError,
                        ConnectionError, socket.gaierror, socket.herror)):
        return True
    # botpy 把平台返回的 4xx/5xx 包成 ServerError / BadRequest 等，不该重试。
    return isinstance(exc, OSError) and not isinstance(exc, (FileNotFoundError, PermissionError))


class QQBotService(botpy.Client):
    def __init__(self, settings: QQSettings, fairy_token: str = "", **kwargs) -> None:
        # botpy 的默认 HTTP 超时只有 5 秒（botpy/http.py 用 aiohttp 的 total 超时），
        # 而超时分支只打一行 warning 就返回 None，不像 ConnectionResetError 那样重试。
        # 结果就是 QQ 接口稍慢时回复被静默丢弃，日志只留「发送文本失败」。
        # 这里抬到 30 秒；显式传入的 timeout 仍然优先。
        kwargs.setdefault("timeout", 30)
        super().__init__(intents=Intents(public_messages=True), **kwargs)
        self.settings = settings
        self.fairy_token = str(fairy_token or "").strip()
        self._locks: dict[str, asyncio.Lock] = {}
        # QQ 的去重键是 msg_id + msg_seq（botpy 文档原话：「相同的 msg_id + msg_seq
        # 重复发送会失败」）。之前每次 _reply_texts 都新建 Sequencer，永远从 1
        # 开始，于是同一条消息的「收到，正在处理…」和最终回复都用 seq=1，第二条
        # 必然被「消息被去重，请检查请求msgseq」（40054005）拒收。
        # 计数必须按 msg_id 记：按会话共享会让 5 次的被动回复额度在会话级别耗尽。
        self._seqs: dict[str, Sequencer] = {}

    async def on_ready(self):
        log.info("QQ 机器人已上线")

    async def on_c2c_message_create(self, message):
        await self._handle(message, C2C, message.author.user_openid, "", message.id)

    async def on_group_at_message_create(self, message):
        await self._handle(message, GROUP, message.group_openid,
                           message.author.member_openid or "", message.id)

    async def _handle(self, message, kind: str, conversation_id: str, sender_name: str, msg_id: str):
        text = str(getattr(message, "content", "") or "").strip()
        if not text:
            return
        if not is_allowed(self.settings, kind, conversation_id):
            log.warning("拒绝未授权来源 kind=%s id=%s text=%r", kind, conversation_id, text[:50])
            return

        # /bind never reaches the model and never needs a session: it is the
        # one command the bridge answers itself. In a group the conversation is
        # the group, so whoever types it hands the whole group to that account.
        bind_code = parse_bind_command(kind, text)
        if bind_code is not None:
            await self._handle_bind(kind, conversation_id, msg_id, bind_code)
            return

        session = session_name(kind, conversation_id)
        # `kind` only names the conversation's own session branch; the account is
        # still decided by the binding table on the server.
        channel = {"name": "qq", "kind": kind, "conversation_id": conversation_id}
        lock = self._locks.setdefault(session, asyncio.Lock())
        if lock.locked():
            # 本轮已经有一条在跑（同一个进程的锁），主人又发了一条。
            await self._insert_into_running_turn(
                kind, conversation_id, msg_id, session, channel, sender_name, text)
            return

        async with lock:
            ack = asyncio.create_task(self._ack_later(kind, conversation_id, msg_id))
            try:
                result = await run_turn(
                    session,
                    format_inbound(kind, sender_name, text),
                    self.settings,
                    self.fairy_token,
                    # Report the conversation, never an identity: the server maps
                    # it to an account through the binding table.
                    channel=channel,
                )
            except ChannelUnbound:
                # Refused before the model ran. Saying so is the whole point of
                # the rule: a stranger learns what to do, and learns nothing else.
                log.info("未绑定来源被拒 kind=%s id=%s", kind, conversation_id)
                ack.cancel()
                await self._reply_texts(kind, conversation_id, msg_id, [UNBOUND_HINT])
                return
            except SessionBusy:
                # 后端已经有活跃回合（网页端在跑，或上一回合刚收尾）。
                # 这就是「中途插入」的场景：消息属于那个回合，不属于队列，回
                # 一句「稍后再发」等于把它丢掉——主人今天就是这么被丢了三次。
                ack.cancel()
                log.info("会话忙，改为插入进行中的回合 kind=%s id=%s", kind, conversation_id)
                await self._insert_into_running_turn(
                    kind, conversation_id, msg_id, session, channel, sender_name, text,
                    fallback_hint=BUSY_HINT)
                return
            except Exception as error:  # noqa: BLE001
                log.exception("调用 Fairy 失败")
                ack.cancel()
                await self._reply_texts(kind, conversation_id, msg_id, [f"服务暂不可用：{error}"])
                return
            ack.cancel()

        await self._deliver(kind, conversation_id, msg_id, result)

    async def _insert_into_running_turn(self, kind: str, conversation_id: str, msg_id: str,
                                       session: str, channel: dict, sender_name: str,
                                       text: str, fallback_hint: str = MID_TURN_REJECT_HINT):
        """把中途发出的消息送进正在跑的回合，并把那个回合的答案取回 QQ。

        注入成功就不要再自己开一回合——后端会以 409 拒绝。注入只把问题送进当前
        回合，而这条会话自己的回合本来就是由 QQ 桥发起并订阅的；上面那种「网页端
        正在跑同一会话」的情况不会再有（渠道消息现在落在各自的分支会话里），所以
        订阅只用于兜住「同一会话里上一条还没跑完」这一幕。
        """
        try:
            info = await inject_message(session, format_inbound(kind, sender_name, text),
                                        self.settings, self.fairy_token, channel=channel)
        except Exception as error:  # noqa: BLE001
            log.warning("中途注入失败，回提示: %s", error)
            await self._reply_texts(kind, conversation_id, msg_id, [fallback_hint])
            return
        # 服务端回的是它自己解析出的会话名（该会话的分支，如
        # 2026-10-01__qq-c2c-4b395add）。桥传进去的 `qq:c2c:<openid>` 只是本
        # 进程的锁键，服务端不会拿它当会话名。
        resolved = str(info.get("session") or session)
        log.info("已插入进行中的回合: session=%s text=%r", resolved, text[:60])
        await self._reply_texts(kind, conversation_id, msg_id, [INSERTED_HINT])
        try:
            result = await follow_turn(resolved, self.settings, self.fairy_token,
                                       channel=channel, run_id=str(info.get("run_id") or ""))
        except Exception as error:  # noqa: BLE001
            log.warning("订阅进行中的回合失败: %s", error)
            return
        await self._deliver(kind, conversation_id, msg_id, result, already_used=1)

    async def _handle_bind(self, kind: str, conversation_id: str, msg_id: str, code: str):
        try:
            outcome = await redeem_bind_code(code, self.settings, conversation_id)
        except Exception as error:  # noqa: BLE001
            log.exception("兑换绑定码失败 kind=%s id=%s", kind, conversation_id)
            await self._reply_texts(kind, conversation_id, msg_id, [BAD_CODE_HINT])
            return
        if not outcome.get("ok"):
            log.info("绑定码无效 kind=%s id=%s code=%s reason=%s",
                     kind, conversation_id, code[:12], outcome.get("code"))
            await self._reply_texts(kind, conversation_id, msg_id, [BAD_CODE_HINT])
            return
        user = outcome.get("user") or {}
        log.info("绑定成功 kind=%s id=%s user=%s", kind, conversation_id, user.get("username"))
        await self._reply_texts(kind, conversation_id, msg_id, [BOUND_OK])

    async def _ack_later(self, kind: str, conversation_id: str, msg_id: str):
        try:
            await asyncio.sleep(self.settings.ack_delay_seconds)
            await self._reply_texts(kind, conversation_id, msg_id, [ACK_TEXT])
        except asyncio.CancelledError:
            raise

    async def _deliver(self, kind: str, conversation_id: str, msg_id: str, result: TurnResult,
                       already_used: int = 0):
        sendable: list[Path] = []
        unsendable: list[dict] = []
        for artifact in result.files:
            picked = pick_sendable(artifact)
            if picked is None:
                unsendable.append(artifact)
            else:
                sendable.append(picked)
        body = result.text if result.completed else "（回复中断）"
        texts = plan_texts(body, describe_unsendable(unsendable), self.settings, include_ack=False)
        if already_used:
            # 被动回复额度是每条 msg_id 共用的，插入场景已经先发过一条。
            texts = texts[: max(1, int(self.settings.max_passive_replies) - int(already_used))]
        await self._reply_texts(kind, conversation_id, msg_id, texts)
        if self.settings.send_files and sendable:
            await self._send_images(kind, conversation_id, msg_id, sendable)

    def _seq_for(self, kind: str, conversation_id: str, msg_id: str) -> Sequencer:
        """按 (会话, msg_id) 共享 Sequencer。

        去重键是 msg_id + msg_seq，所以计数必须跟着 msg_id 走：同一会话里两条
        不同的消息各自从 1 开始是合法的，而同一条消息的 ack 与最终回复必须
        用 1、2。额度（max_passive_replies）也是每条 msg_id 的额度，按会话共享
        会让一条会话聊三五回就把额度耗尽，之后所有回复都发不出去。
        """
        key = f"{kind}:{conversation_id}:{msg_id}"
        seq = self._seqs.get(key)
        if seq is None:
            seq = Sequencer(self.settings.max_passive_replies)
            self._seqs[key] = seq
            while len(self._seqs) > SEQ_CACHE_LIMIT:
                self._seqs.pop(next(iter(self._seqs)))
        return seq

    async def _post_with_retry(self, post, msg_id: str, value: int, **payload):
        """发送一条消息，传输层故障时退避重试。

        重试必须沿用同一个 msg_seq：平台是按 msg_id + msg_seq 去重的，平台没收到
        过这次请求才允许用同一个序号重来；一旦平台已经处理过（返回 4xx/5xx），
        换号只会撞 40054005，所以那种情况直接放弃。
        """
        attempt = 0
        while True:
            try:
                await post(msg_id=msg_id, msg_seq=value, **payload)
                return True
            except Exception as exc:  # noqa: BLE001
                attempt += 1
                if attempt >= SEND_RETRY_ATTEMPTS or not _is_transient(exc):
                    log.exception("发送失败 seq=%s", value)
                    return False
                delay = SEND_RETRY_BACKOFF[min(attempt - 1, len(SEND_RETRY_BACKOFF) - 1)]
                log.warning(
                    "发送失败 seq=%s 第 %s 次尝试，%.1fs 后重试: %r",
                    value, attempt, delay, exc,
                )
                await asyncio.sleep(delay)

    async def _reply_texts(self, kind: str, conversation_id: str, msg_id: str, texts: list[str]):
        seq = self._seq_for(kind, conversation_id, msg_id)
        for text in texts:
            value = seq.take()
            if value is None:
                log.warning("被动回复额度用尽，剩余文本未发送")
                return
            if kind == GROUP:
                post, extra = self.api.post_group_message, {"group_openid": conversation_id}
            else:
                post, extra = self.api.post_c2c_message, {"openid": conversation_id}
            await self._post_with_retry(
                post, msg_id, value, msg_type=0, content=text, **extra,
            )

    async def _send_images(self, kind: str, conversation_id: str, msg_id: str, paths: list[Path]):
        """图片发送：先直连底层富媒体路由上传拿 file_info，再作为 media 消息发出。

        走 push.upload_media（参数已按 QQ 要求带 file_type），上传是同步 requests，
        放进线程执行避免阻塞事件循环。
        msg_type 必须是 7（富媒体）：botpy 文档写的是「0 文本，1 图文混排，2
        markdown，3 ark，4 embed，7 media 富媒体」，用 1 发 media 字段是错的。
        msg_seq 复用同一个 msg_id 的计数器，否则会和前面几条文本撞号，又被
        40054005 去重拒收。
        40093007「富媒体文件下载失败」曾经被当成"后台没开图片能力"——不是。
        那个接口要的是 JSON 的 file_data/url，用 multipart 传文件就会被它当成
        "没有可下载的素材"，换成 JSON+base64 立刻成功（见 push.upload_media）。
        """
        target = f"{kind}:{conversation_id}"
        seq = self._seq_for(kind, conversation_id, msg_id)
        for path in paths:
            try:
                file_info = await asyncio.to_thread(upload_media, target, path)
            except Exception:  # noqa: BLE001
                log.exception("上传图片失败 %s", path.name)
                continue
            value = seq.take()
            if value is None:
                log.warning("被动回复额度用尽，剩余图片未发送: %s", path.name)
                return
            # botpy 的 message.Media 是 TypedDict，运行时就是普通 dict，
            # 不能当类构造；直接传 {"file_info": ...}。
            media = {"file_info": file_info}
            try:
                if kind == GROUP:
                    await self.api.post_group_message(
                        group_openid=conversation_id, msg_type=7,
                        media=media, msg_id=msg_id, msg_seq=value)
                else:
                    await self.api.post_c2c_message(
                        openid=conversation_id, msg_type=7,
                        media=media, msg_id=msg_id, msg_seq=value)
                log.info("图片已发送: %s", path.name)
            except Exception:  # noqa: BLE001
                log.exception("发送图片失败 %s", path.name)
