"""配置加载：凭据在仓库外，策略在仓库内。"""
from __future__ import annotations

import json
import os
from dataclasses import dataclass
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]
DEFAULT_CREDENTIALS = Path.home() / ".config" / "fairy" / "qq-bot.json"
DEFAULT_POLICY = REPO_ROOT / "config" / "channels.json"


@dataclass(frozen=True)
class Credentials:
    app_id: str
    app_secret: str
    sandbox: bool = False
    # Fairy session token issued by `node frontend/auth.cjs issue-token <user>`.
    # Empty means "no binding": the bridge then speaks as the loopback owner,
    # which is fine for a single-user machine but wrong once the QQ bot should
    # write into a specific family member's conversation tree.
    fairy_token: str = ""


@dataclass(frozen=True)
class QQSettings:
    enabled: bool = True
    model: str = ""
    api_base: str = "http://127.0.0.1:8081"
    allow_c2c: tuple[str, ...] = ()
    allow_groups: tuple[str, ...] = ()
    ack_delay_seconds: float = 8.0
    reply_chunk_size: int = 3500
    max_passive_replies: int = 5
    send_files: bool = True
    max_file_size_mb: float = 20.0
    turn_timeout_seconds: float = 1800.0


def _credentials_path(path: Path | None) -> Path:
    override = os.environ.get("FAIRY_QQ_CREDENTIALS", "").strip()
    if override:
        return Path(override).expanduser()
    return path or DEFAULT_CREDENTIALS


def load_credentials(path: Path | None = None) -> Credentials:
    p = _credentials_path(path)
    raw = json.loads(p.read_text(encoding="utf-8"))
    app_id = str(raw.get("appId") or "").strip()
    app_secret = str(raw.get("appSecret") or "").strip()
    if not app_id or not app_secret:
        raise ValueError(f"凭据不完整（缺少 appId 或 appSecret）: {p}")
    return Credentials(
        app_id=app_id,
        app_secret=app_secret,
        sandbox=bool(raw.get("sandbox", False)),
        fairy_token=str(raw.get("fairyToken") or "").strip(),
    )


def load_settings(path: Path | None = None) -> QQSettings:
    p = path or DEFAULT_POLICY
    if not p.exists():
        return QQSettings()
    raw = json.loads(p.read_text(encoding="utf-8"))
    qq = raw.get("qq") or {}
    return QQSettings(
        enabled=bool(qq.get("enabled", True)),
        model=str(qq.get("model") or ""),
        api_base=str(qq.get("apiBase") or "http://127.0.0.1:8081").rstrip("/"),
        allow_c2c=tuple(str(x) for x in (qq.get("allowC2C") or ())),
        allow_groups=tuple(str(x) for x in (qq.get("allowGroups") or ())),
        ack_delay_seconds=float(qq.get("ackDelaySeconds", 8)),
        reply_chunk_size=int(qq.get("replyChunkSize", 3500)),
        max_passive_replies=int(qq.get("maxPassiveReplies", 5)),
        send_files=bool(qq.get("sendFiles", True)),
        max_file_size_mb=float(qq.get("maxFileSizeMB", 20)),
        turn_timeout_seconds=float(qq.get("turnTimeoutSeconds", 1800)),
    )


def app_config() -> dict:
    """读取仓库的 config/config.json（含 UI settings）。"""
    p = REPO_ROOT / "config" / "config.json"
    try:
        return json.loads(p.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return {}


def resolve_model(settings: QQSettings, config: dict | None = None) -> str:
    """与网页端一致：显式指定 > settings.default_model > 顶层 default_model。"""
    if settings.model.strip():
        return settings.model.strip()
    data = config if config is not None else app_config()
    ui_default = str(((data.get("settings") or {}).get("default_model")) or "").strip()
    top_default = str(data.get("default_model") or "").strip()
    return ui_default or top_default
