"""产物挑选。官方只开放图片/视频/语音，v1 只发图片，其余给路径。"""
from __future__ import annotations

from pathlib import Path

IMAGE_SUFFIXES = frozenset({".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp"})


def _is_image(path: Path) -> bool:
    return path.suffix.lower() in IMAGE_SUFFIXES


def pick_sendable(artifact: dict) -> Path | None:
    """返回可直接发送的图片路径；不可发送时返回 None。"""
    raw = str((artifact or {}).get("path") or "").strip()
    if not raw:
        return None
    path = Path(raw)
    if path.is_file():
        return path if _is_image(path) else None
    if not path.is_dir():
        return None
    for child in sorted(path.iterdir()):
        if child.is_file() and _is_image(child):
            return child
    return None


def describe_unsendable(artifacts: list[dict]) -> list[str]:
    """给用户列出无法发送的产物及其本机路径。"""
    out: list[str] = []
    for artifact in artifacts or []:
        path = str((artifact or {}).get("path") or "").strip()
        if not path:
            continue
        name = str((artifact or {}).get("name") or Path(path).name)
        out.append(f"{name}（本机路径：{path}）")
    return out
