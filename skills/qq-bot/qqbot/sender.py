"""回复编排：一前一后两条，且永远不超出被动回复额度。"""
from __future__ import annotations

from .config import QQSettings
from .filter import render

ACK_TEXT = "收到，正在处理…"
EMPTY_TEXT = "（本轮没有可发送的文本结果）"


class Sequencer:
    """为同一个 msg_id 递增 msg_seq；重复的组合会被 QQ 拒绝。"""

    def __init__(self, max_replies: int) -> None:
        self._max = max(1, int(max_replies))
        self._next = 1

    def take(self) -> int | None:
        if self._next > self._max:
            return None
        value = self._next
        self._next += 1
        return value


def plan_texts(result_text: str, unsendable: list[str], settings: QQSettings,
               include_ack: bool) -> list[str]:
    chunks = render(result_text, settings.reply_chunk_size) or [EMPTY_TEXT]
    if unsendable:
        notice = "以下产物无法通过 QQ 发送，请到本机取用：\n" + "\n".join(unsendable)
        chunks = chunks + render(notice, settings.reply_chunk_size)
    if include_ack:
        chunks = [ACK_TEXT] + chunks
    return chunks[: max(1, int(settings.max_passive_replies))]
