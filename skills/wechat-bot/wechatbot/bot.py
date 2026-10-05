"""iLink 私聊桥接：长轮询收消息 → 白名单 → Fairy /api/chat → 回复。"""
from __future__ import annotations

import asyncio
import json
import logging
import pathlib
import threading
import time

from .config import REPO_ROOT, Account, ChatSettings
from .fairy_client import ChannelUnbound, SessionBusy, run_turn
from .filter import render
from .ilink import ITEM_IMAGE, ITEM_VOICE, ILinkClient
from .policy import effective_settings, is_allowed, session_name
from .state import load_contexts, remember_context

log = logging.getLogger("wechat-bot")
ACK_TEXT = "收到，正在处理…"
EMPTY_TEXT = "（本轮没有可发送的文本结果）"
BUSY_TEXT = "我正在处理上一个请求，稍后再发一次"
# The local lock (one turn per WeChat account) is a different queue from the
# server's daily-session lock, so it keeps its own wording.
LOCAL_BUSY_TEXT = "上一条还在处理中，请稍后再发一次。"


class Bridge:
    def __init__(self, account: Account, settings: ChatSettings, client: ILinkClient | None = None) -> None:
        self.account = account
        # 账号自带的名单优先，见 policy.effective_settings。
        self.settings = effective_settings(settings, account)
        self.client = client or ILinkClient(account.base_url, account.bot_token)
        self._buf = ""
        # Resume whatever the last run of this bridge knew, so a restart does not
        # silently take away the ability to send someone a reminder until they
        # happen to speak again.
        self._contexts: dict[str, str] = load_contexts(account.label)
        self._locks: dict[str, threading.Lock] = {}
        self._seen: set[str] = set()

    def run_forever(self) -> None:
        log.info("微信 iLink 桥接启动：bot=%s base=%s",
                 self.account.ilink_bot_id or "?", self.account.base_url)
        while True:
            try:
                data = self.client.get_updates(self._buf)
            except Exception as error:  # noqa: BLE001
                log.warning("getupdates 失败，5 秒后重试：%s", error)
                time.sleep(5)
                continue
            if data.get("get_updates_buf"):
                self._buf = str(data["get_updates_buf"])
            for msg in data.get("msgs") or []:
                try:
                    self.handle(msg)
                except Exception:  # noqa: BLE001
                    log.exception("处理消息失败")

    def handle(self, msg: dict) -> None:
        message_id = str(msg.get("message_id") or "")
        if message_id and message_id in self._seen:
            return
        if message_id:
            self._seen.add(message_id)

        group_id = str(msg.get("group_id") or "").strip()
        if group_id:
            log.info("忽略群消息（本期仅私聊） group_id=%s", group_id)
            return

        user_id = str(msg.get("from_user_id") or "").strip()
        text = ILinkClient.extract_text(msg)
        if not user_id:
            return
        attachments = self.collect_attachments(msg)
        if not text and not attachments:
            # 图片/文件/语音这类消息目前不处理，但它们的 item 结构是我们要的：
            # iLink 没有公开文档，"主动发一张图"该拼什么字段、上传接口要什么
            # 参数，只能从对方真发过来的媒体消息里读。日志里只放摘要，完整
            # 一份存到 <账号>.last-media.json 里，方便对着字段排查。
            items = msg.get("item_list")
            if items:
                log.info("收到非文本消息（结构采样） user=%s items=%s",
                         user_id, json.dumps(items, ensure_ascii=False)[:800])
                try:
                    from .state import state_path
                    dump = state_path(self.account.label).with_name(
                        f"{self.account.label}.last-media.json")
                    dump.parent.mkdir(parents=True, exist_ok=True)
                    dump.write_text(json.dumps(msg, ensure_ascii=False, indent=2), encoding="utf-8")
                except Exception:  # noqa: BLE001 - 采样失败不该影响收消息
                    log.debug("写入媒体样本失败", exc_info=True)
            # A voice message with no transcript still deserves a turn. Returning
            # here meant speaking to the bot was answered with silence, and the
            # raw payload dumped above is what lets a wrong field name be fixed.
            if not any(
                (it or {}).get("type") == ITEM_VOICE
                for it in (items or [])
            ):
                return
            text = "[语音]（这次没听清）"
        if not text:
            # 图片消息本身没有文字，但会话里要有句话可读，前端也才会建出这一轮。
            text = "[图片]" if len(attachments) == 1 else f"[图片 x{len(attachments)}]"

        context_token = str(msg.get("context_token") or "").strip()
        if context_token:
            self._contexts[user_id] = context_token
            # 定时任务在另一个进程里跑，它读不到这张内存表，所以顺手落盘一份。
            remember_context(self.account.label, user_id, context_token)

        if not is_allowed(self.settings, user_id):
            log.warning("拒绝未授权来源 user=%s text=%r", user_id, text[:40])
            return

        session = session_name(user_id)
        lock = self._locks.setdefault(session, threading.Lock())
        if lock.locked():
            # Our own queue, not the server's: this message never left the bridge.
            self.reply(user_id, [LOCAL_BUSY_TEXT])
            return

        with lock:
            timer = threading.Timer(self.settings.ack_delay_seconds, self.reply, args=(user_id, [ACK_TEXT]))
            timer.daemon = True
            timer.start()
            try:
                # Report the WeChat account, not an identity: the server maps the
                # conversation to an account through the binding table that the
                # QR scan wrote.
                result = asyncio.run(run_turn(session, text, self.settings, user_id, files=attachments))
            except ChannelUnbound:
                # Silent on purpose. WeChat has no "prompt and refuse" habit, and
                # a stranger should get neither an answer nor a hint that a bot
                # exists here. The operator finds out from the log.
                timer.cancel()
                log.warning("未绑定来源被拒 user=%s（该微信没有绑定任何 Fairy 账号）", user_id)
                return
            except SessionBusy:
                timer.cancel()
                log.info("会话忙，重试后仍忙 user=%s", user_id)
                self.reply(user_id, [BUSY_TEXT])
                return
            except Exception as error:  # noqa: BLE001
                timer.cancel()
                log.exception("调用 Fairy 失败")
                self.reply(user_id, [f"服务暂不可用：{error}"])
                return
            timer.cancel()

        body = result.text if result.completed else "（回复中断）"
        chunks = render(body, self.settings.reply_chunk_size) or [EMPTY_TEXT]
        if result.files:
            self.send_artifacts(user_id, result.files)
        self.reply(user_id, chunks)

    IMAGE_SUFFIXES = (".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp")

    def collect_attachments(self, msg: dict) -> list[dict]:
        """把消息里的图片下载解密落盘，返回给 Fairy 的附件描述。

        下载走微信 CDN（参数是对方那条消息里的 encrypt_query_param + aeskey），
        落盘后用 files 带给 /api/chat —— 这样前端会显示附件，模型也能顺着路径
        真的看到图，而不是只收到一句"[图片]"。
        """
        attachments: list[dict] = []
        for index, item in enumerate(msg.get("item_list") or [], start=1):
            if int((item or {}).get("type") or 0) != ITEM_IMAGE:
                continue
            image = (item or {}).get("image_item") or {}
            media = image.get("media") or {}
            param = str(media.get("encrypt_query_param") or "").strip()
            aeskey = str(image.get("aeskey") or "").strip()
            if not param or not aeskey:
                continue
            try:
                data = self.client.download_media(param, aeskey)
            except Exception:  # noqa: BLE001
                log.exception("下载微信图片失败（msg_id=%s）", msg.get("message_id"))
                continue
            path = self.write_attachment(msg, data, index)
            attachments.append({"kind": "image", "path": str(path), "name": path.name})
        return attachments

    def write_attachment(self, msg: dict, data: bytes, index: int) -> pathlib.Path:
        """把一张图落到 workspace/wechat-inbox/<账号>/ 下（相对仓库的路径给服务端）。"""
        directory = REPO_ROOT / "workspace" / "wechat-inbox" / self.account.label
        directory.mkdir(parents=True, exist_ok=True)
        suffix = ".jpg" if data[:3] == b"\xff\xd8\xff" else ".png" if data[:4] == b"\x89PNG" else ".bin"
        stem = str(msg.get("message_id") or "msg").replace(":", "_").replace("/", "_")
        path = directory / f"{stem}-{index}{suffix}"
        path.write_bytes(data)
        return path

    def send_artifacts(self, user_id: str, files: list[dict]) -> None:
        """产物里的图片能直接发；其它类型跳过。

        微信这条通道只到图片这一层：富媒体要先上传拿加密引用（见 ilink.upload_media），
        文件/视频没有对应的发送路径，硬发就是给对方一个"已过期"的空壳。
        """
        context_token = self._contexts.get(user_id, "")
        skipped = 0
        for item in files or []:
            path = str((item or {}).get("path") or "")
            if not path or pathlib.Path(path).suffix.lower() not in self.IMAGE_SUFFIXES:
                skipped += 1
                continue
            try:
                self.client.send_image(user_id, path, context_token)
            except Exception:  # noqa: BLE001
                log.exception("发送图片失败 %s", path)
                skipped += 1
        if skipped:
            log.info("微信私聊跳过 %d 个非图片产物", skipped)

    def reply(self, user_id: str, texts: list[str]) -> None:
        context_token = self._contexts.get(user_id, "")
        if not context_token:
            log.warning("缺少 context_token，无法回复 %s（需对方先发一条消息）", user_id)
            return
        for text in texts:
            try:
                self.client.send_text(user_id, text, context_token)
            except Exception:  # noqa: BLE001
                log.exception("发送失败 user=%s", user_id)
            time.sleep(0.6)
