"""配置：账号凭据按账号分文件存（为后续多账号/父母各一个账号留位），策略读仓库配置。"""
from __future__ import annotations

import json
import os
from dataclasses import dataclass, field
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]
DEFAULT_POLICY = REPO_ROOT / "config" / "channels.json"
DEFAULT_ACCOUNT_DIR = Path.home() / ".config" / "fairy" / "wechat"


@dataclass
class Account:
    bot_token: str
    ilink_bot_id: str = ""
    ilink_user_id: str = ""
    base_url: str = "https://ilinkai.weixin.qq.com"
    label: str = "default"
    # 这个 bot 只回谁。空元组表示"用共享配置里的名单"——扫码接入时会把它写成
    # 扫码那个人自己的 id，因为一个人扫的是自己的微信，别人的消息不该由这个
    # bot 代答。
    allow_users: tuple[str, ...] = ()


@dataclass(frozen=True)
class ChatSettings:
    enabled: bool = True
    model: str = ""
    api_base: str = "http://127.0.0.1:8081"
    allow_users: tuple[str, ...] = ()
    # 微信这边默认短一点：回合常常几秒就答完，8 秒的阈值等于没有中间态
    # （主人实测 5 秒答完，那条"正在处理"从来没出现过）。QQ 保持 8 秒。
    ack_delay_seconds: float = 3.0
    reply_chunk_size: int = 1800
    turn_timeout_seconds: float = 1800.0


def account_path(label: str = "default", directory: Path | None = None) -> Path:
    base = Path(os.environ.get("FAIRY_WECHAT_DIR") or (directory or DEFAULT_ACCOUNT_DIR)).expanduser()
    return base / f"{label}.json"


def load_account(label: str = "default", directory: Path | None = None) -> Account:
    p = account_path(label, directory)
    raw = json.loads(p.read_text(encoding="utf-8"))
    token = str(raw.get("bot_token") or "").strip()
    if not token:
        raise ValueError(f"账号凭据缺少 bot_token: {p}")
    return Account(
        bot_token=token,
        ilink_bot_id=str(raw.get("ilink_bot_id") or ""),
        ilink_user_id=str(raw.get("ilink_user_id") or ""),
        base_url=str(raw.get("base_url") or "https://ilinkai.weixin.qq.com").rstrip("/"),
        label=str(raw.get("label") or label),
        allow_users=tuple(str(x) for x in (raw.get("allow_users") or ()) if str(x).strip()),
    )


def save_account(account: Account, directory: Path | None = None) -> Path:
    p = account_path(account.label, directory)
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_text(json.dumps({
        "label": account.label,
        "bot_token": account.bot_token,
        "ilink_bot_id": account.ilink_bot_id,
        "ilink_user_id": account.ilink_user_id,
        "base_url": account.base_url,
    }, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    os.chmod(p, 0o600)
    return p


def load_settings(path: Path | None = None) -> ChatSettings:
    p = path or DEFAULT_POLICY
    if not p.exists():
        return ChatSettings()
    raw = json.loads(p.read_text(encoding="utf-8"))
    wx = raw.get("wechat") or {}
    return ChatSettings(
        enabled=bool(wx.get("enabled", True)),
        model=str(wx.get("model") or ""),
        api_base=str(wx.get("apiBase") or "http://127.0.0.1:8081").rstrip("/"),
        allow_users=tuple(str(x) for x in (wx.get("allowUsers") or ())),
        ack_delay_seconds=float(wx.get("ackDelaySeconds", 3)),
        reply_chunk_size=int(wx.get("replyChunkSize", 1800)),
        turn_timeout_seconds=float(wx.get("turnTimeoutSeconds", 1800)),
    )


def app_config() -> dict:
    p = REPO_ROOT / "config" / "config.json"
    try:
        return json.loads(p.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return {}


def resolve_model(settings: ChatSettings, config: dict | None = None) -> str:
    """与网页端一致：显式指定 > settings.default_model > 顶层 default_model。"""
    if settings.model.strip():
        return settings.model.strip()
    data = config if config is not None else app_config()
    ui_default = str(((data.get("settings") or {}).get("default_model")) or "").strip()
    return ui_default or str(data.get("default_model") or "").strip()
