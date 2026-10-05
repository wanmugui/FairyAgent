"""白名单、会话映射、绑定指令与入站文本整形（与平台无关的纯逻辑）。"""
from __future__ import annotations

import re

from .config import QQSettings

C2C = "c2c"
GROUP = "group"

# A bind command is the only thing this bridge acts on without asking the model.
# The argument is deliberately not validated here: an expired or mistyped code is
# still a bind attempt, and the person deserves the "generate a new code" reply
# instead of having their message forwarded to the agent.
_BIND_RE = re.compile(r"^/bind(?P<args>(?:\s+.*)?)$", re.IGNORECASE | re.DOTALL)
# Group messages arrive as "<@!botid> /bind CODE", so the mention has to come off
# before the command is visible.
_MENTION_RE = re.compile(r"<@!?[^>]*>")


def strip_mention(text: str) -> str:
    return _MENTION_RE.sub("", str(text or "")).strip()


def parse_bind_command(conversation_type: str, text: str) -> str | None:
    """Code argument of a `/bind CODE`, or None when this is ordinary text.

    Returns "" for a bare `/bind`, which is still a bind attempt: replying
    "code invalid" beats forwarding it to the model.
    """
    body = str(text or "").strip()
    if conversation_type == GROUP:
        body = strip_mention(body)
    match = _BIND_RE.match(body)
    if not match:
        return None
    args = str(match.group("args") or "").strip()
    if not args:
        return ""
    return args.split()[0]


def is_allowed(settings: QQSettings, conversation_type: str, conversation_id: str) -> bool:
    """默认拒绝：名单为空即拒绝一切。"""
    if conversation_type == C2C:
        return conversation_id in settings.allow_c2c
    if conversation_type == GROUP:
        return conversation_id in settings.allow_groups
    return False


def session_name(conversation_type: str, conversation_id: str) -> str:
    return f"qq:{conversation_type}:{conversation_id}"


def format_inbound(conversation_type: str, sender_name: str, text: str) -> str:
    """群聊带上发言人，便于 agent 区分；私聊保持原样。"""
    body = str(text or "").strip()
    if conversation_type == GROUP:
        name = str(sender_name or "").strip()
        if name:
            return f"[{name}] {body}"
    return body
