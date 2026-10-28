"""iLink 私聊桥接：长轮询收消息 → 白名单 → Fairy /api/chat → 回复。"""
from __future__ import annotations

import asyncio
import json
import logging
import pathlib
import threading
import time

from . import ask_bridge
from .config import REPO_ROOT, Account, ChatSettings
from .fairy_client import ChannelUnbound, InterimNotifier, SessionBusy, run_turn
from .filter import render
from .ilink import ITEM_IMAGE, ITEM_VOICE, ILinkClient
from .policy import effective_settings, is_allowed, session_name
from .state import load_contexts, remember_context

log = logging.getLogger("wechat-bot")


def pick_from_deck(directory: pathlib.Path) -> pathlib.Path | None:
    """从一个产物目录里挑一个真正值得发给主人的文件。

    show_result 传 PPT 产物时给的是 deck 目录，里面是 pages/ 或 htmls/，
    外加 deck.json、README.md 之类的杂项。原来「取排序后第一个文件」会
    挑中 README.md —— 主人要的是那套 PPT，收到的却是一份说明文档。这比
    不发更糟：它看起来投递成功了。

    优先级按「微信里最可能直接看得懂」排：页面图 → 页面 HTML → pptx →
    最后才退到随便一个文件（覆盖普通打包目录）。
    """
    pages = sorted(directory.glob("pages/page_*.png")) or sorted(directory.glob("pages/*.png"))
    if pages:
        return pages[0]
    htmls = sorted(directory.glob("htmls/page_*.html")) or sorted(directory.glob("htmls/*.html"))
    if htmls:
        return htmls[0]
    decks = sorted(directory.glob("*.pptx"))
    if decks:
        return decks[0]
    return next((c for c in sorted(directory.iterdir()) if c.is_file()), None)
ACK_TEXT = "收到，正在处理…"
EMPTY_TEXT = "（本轮没有可发送的文本结果）"
BUSY_TEXT = "我正在处理上一个请求，稍后再发一次"
# The local lock (one turn per WeChat account) is a different queue from the
# server's daily-session lock, so it keeps its own wording.
LOCAL_BUSY_TEXT = "上一条还在处理中，请稍后再发一次。"
# 中间进度说明的最短间隔（秒）。长任务会连续吐好几条，按这个节流，
# 免得微信聊天被刷成一串"正在…正在…正在…"。
INTERIM_MIN_INTERVAL_SECONDS = 45.0


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
        # ask_user 在通道里的问答状态：session -> 未答条目的下标。
        # 提问时那一轮正占着锁，主人的回复必须在这里被吃掉；
        # 落到下面的 locked 分支只会被当成"本地忙，请稍后再发"丢掉。
        self._ask_open: dict[str, int] = {}

    @property
    def _asks(self) -> dict[str, int]:
        """未答问题表。

        懒加载是有原因的：测试用 Bridge.__new__ 直接造对象、绕过了 __init__，
        直接摸 self._ask_open 会 AttributeError。这里保证无论对象怎么来的都可用。
        """
        store = getattr(self, "_ask_open", None)
        if store is None:
            store = {}
            self._ask_open = store
        return store

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

        # 优先判定：这是一条对未答问题的回复吗？是就当答案吃掉，绝不下探到锁。
        # 提问时那一轮正占着锁，答案落到下面的 locked 分支只会得到一句
        # "本地忙，请稍后再发"，工具那边只能等超时自动选。
        if session in self._asks:
            index = self._asks.pop(session)
            if ask_bridge.submit_reply(index, text):
                log.info("已收到 ask_user 作答 session=%s index=%s", session, index)
                self.reply(user_id, ["收到，按你说的来。"])
            else:
                self.reply(user_id, ["这条没能对上问题，已经过期了。你再说一次需求，我重新问。"])
            return

        lock = self._locks.setdefault(session, threading.Lock())
        if lock.locked():
            # Our own queue, not the server's: this message never left the bridge.
            self.reply(user_id, [LOCAL_BUSY_TEXT])
            return

        with lock:
            ack_sent = threading.Event()

            def reply_once(text: str) -> None:
                # 中间说明取自原始 SSE 事件，带着 <msg> 这类只给前端看的标签。
                # 最终回复走 render()（见下方），这里同样必须走，否则主人会在
                # 微信里看到裸标签。
                parts = render(text, self.settings.reply_chunk_size) if text.strip() else []
                if not parts:
                    return
                if ack_sent.is_set():
                    self.reply(user_id, parts)
                else:
                    ack_sent.set()
                    timer.cancel()
                    self.reply(user_id, parts)

            # ACK 只是开场兜底：到期还没等到中间进度说明才发。
            # 一旦中间说明先到了，reply_once 会 cancel 掉它，
            # 所以不会出现「ACK + 真实进度」两条连着刷屏。
            timer = threading.Timer(
                self.settings.ack_delay_seconds,
                lambda: None if ack_sent.is_set() else reply_once(ACK_TEXT),
            )
            timer.daemon = True
            timer.start()

            try:
                # 盯 ask_results.json：ask_user 阻塞期间问题只落在这一个文件里，
                # SSE 上的 waiting_user_input 是工具返回之后才发的，那时候
                # 后端已经等满超时替你选完了。
                ask_stop = threading.Event()
                ask_thread = threading.Thread(
                    target=self._watch_ask,
                    args=(session, ask_bridge.current_count(), user_id, ask_stop),
                    daemon=True,
                )
                ask_thread.start()
                try:
                    # Report the WeChat account, not an identity: the server maps the
                    # conversation to an account through the binding table that the
                    # QR scan wrote.
                    notifier = InterimNotifier(reply_once, min_interval=INTERIM_MIN_INTERVAL_SECONDS)
                    result = asyncio.run(run_turn(session, text, self.settings, user_id,
                                                  files=attachments, on_interim=notifier))
                finally:
                    ask_stop.set()
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

    def _watch_ask(self, session: str, seen: int, user_id: str, stop: threading.Event) -> None:
        """这一轮跑着的时候，把新出现的 ask_user 问题推给主人。

        问一次就停：主人看到的第一条必须是问题，否则答题说明会重复刷屏。
        后端自带超时兜底，所以这里不负责"催"，也不负责替主人决定。
        """
        while not stop.is_set():
            if stop.wait(2.0):
                return
            try:
                found = ask_bridge.pending_index(seen)
                if not found:
                    continue
                index, entry = found
                self._asks[session] = index
                log.info("ask_user 需要主人作答 session=%s index=%s", session, index)
                parts = render(
                    ask_bridge.format_questions(entry, ask_bridge.ASK_AUTO_SELECT_SECONDS),
                    self.settings.reply_chunk_size,
                )
                if parts:
                    self.reply(user_id, parts)
                return
            except Exception:  # noqa: BLE001
                log.exception("推送 ask_user 失败 session=%s", session)
                return

    def send_artifacts(self, user_id: str, files: list[dict]) -> None:
        """产物里的图片能直接发；其它类型跳过。

        图片与文件各有各的发送路径：图片走 send_image，其余走 ilink.send_file
        （内部用 MEDIA_FILE 上传，FileItem 没有缩略图字段）。
        此前这里只发图片、非图片一律跳过 —— 理由写的是「文件/视频没有对应的
        发送路径」，但 ilink.send_file 一直存在，那条注释是错的。主人要的是
        产物能收到，宁可发文件，也不要只留一句本机路径。
        """
        context_token = self._contexts.get(user_id, "")
        unsent: list[str] = []
        for item in files or []:
            path = str((item or {}).get("path") or "").strip()
            if not path:
                unsent.append(str((item or {}).get("name") or "(未命名产物)"))
                continue
            local = pathlib.Path(path)
            # 目录（例如 PPT deck 工作目录）取一个真正有内容的东西发。
            if local.is_dir():
                local = pick_from_deck(local)
            if local is None or not local.is_file():
                unsent.append(f"{local.name if local else path}")
                continue
            try:
                if local.suffix.lower() in self.IMAGE_SUFFIXES:
                    self.client.send_image(user_id, str(local), context_token)
                else:
                    self.client.send_file(user_id, str(local), context_token)
            except Exception:  # noqa: BLE001
                log.exception("发送产物失败 %s", path)
                unsent.append(local.name)
        if unsent:
            log.info("微信私聊有 %d 个产物没发出去：%s", len(unsent), "、".join(unsent))
            # 必须让主人知道。只写日志的话，主人看到的是「Fairy 说做完了，但
            # 文件没来」，既不知道是哪个文件，也不知道该不该重试——这正是当初
            # 「文件传不过去」之所以难查的原因：失败是静默的。
            notice = "这几个产物没能发出去：" + "、".join(unsent[:5])
            if len(unsent) > 5:
                notice += f" 等共 {len(unsent)} 个"
            notice += "（本机路径有效，可能是格式或大小被拒）"
            self.reply(user_id, [notice])

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
