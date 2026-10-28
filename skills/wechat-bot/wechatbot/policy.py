"""白名单与会话映射（与平台无关的纯逻辑）。"""
from __future__ import annotations

from dataclasses import replace

from .config import Account, ChatSettings


def is_allowed(settings: ChatSettings, user_id: str) -> bool:
    """默认拒绝：名单为空即拒绝一切。"""
    return str(user_id or "").strip() in settings.allow_users


def effective_settings(settings: ChatSettings, account: Account) -> ChatSettings:
    """账号自带的名单优先于共享配置。

    一个机器上可能挂着好几个人的 bot，共享的那份名单是给"还没扫码接入"的老
    用法兜底的。接入之后每个账号把自己的 id 写进凭据文件，于是谁的 bot 只回
    谁的消息——家庭组里别人的微信不该被这个 bot 代答。
    """
    if not account.allow_users:
        return settings
    return replace(settings, allow_users=account.allow_users)


def session_name(user_id: str) -> str:
    return f"wechat:{str(user_id or '').strip()}"
