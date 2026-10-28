"""产物挑选。QQ 开放图片/视频/语音/文件四类富媒体，这里按「本机能读到的单个文件」挑。"""
from __future__ import annotations

from pathlib import Path

IMAGE_SUFFIXES = frozenset({".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp"})

# 走 qqbot.push.upload_media 时这些是 file_type=1，其余按 file_type=4（文件）上传。
# 上传接口本身早就支持非图片（file_type_for 已经映射好了），此前只是在这里被
# 挡掉：产物里只要有一个 pdf/zip/md，就整个被当成"发不出去"，于是主人只能收到
# 一句路径。宁可发文件，也不要假装没有。
_KNOWN_NON_FILE_SUFFIXES = frozenset({".tmp", ".part", ".swp", ".lock"})


def _is_image(path: Path) -> bool:
    return path.suffix.lower() in IMAGE_SUFFIXES


def _is_sendable_file(path: Path) -> bool:
    if not path.is_file():
        return False
    if path.suffix.lower() in _KNOWN_NON_FILE_SUFFIXES:
        return False
    # 0 字节的产物通常是写了一半或已被清掉，发过去是个坏文件。
    try:
        return path.stat().st_size > 0
    except OSError:
        return False


def pick_sendable(artifact: dict) -> Path | None:
    """返回可直接发送的单个文件路径；挑不出时返回 None。

    目录（例如 PPT deck 工作目录）取一个真正有内容的文件——上传接口一次只吃
    一个文件，多打 zip 是这里不该做的事。
    """
    raw = str((artifact or {}).get("path") or "").strip()
    if not raw:
        return None
    path = Path(raw)
    if path.is_file():
        return path if _is_sendable_file(path) else None
    if not path.is_dir():
        return None
    # deck 目录（PPT 产物）优先取页面图，其次 pptx。
    # 「取排序后第一个能发的文件」会挑中 README.md 或 deck.json —— 主人要的
    # 是一套 PPT，收到的却是一份说明文档，而且看起来投递成功了，比不发更糟。
    pages = sorted(path.glob("pages/page_*.png")) or sorted(path.glob("pages/*.png"))
    for candidate in pages:
        if _is_sendable_file(candidate):
            return candidate
    for candidate in sorted(path.glob("*.pptx")):
        if _is_sendable_file(candidate):
            return candidate
    for child in sorted(path.iterdir()):
        if _is_sendable_file(child):
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
