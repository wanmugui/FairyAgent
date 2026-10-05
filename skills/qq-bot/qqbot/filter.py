"""出站文本清洗：QQ 不渲染 Markdown，先去掉标记再按长度分片。"""
from __future__ import annotations

import re

_THINK = re.compile(r"<(?:think|thinking|mm:think)(?:\s[^>]*)?>[\s\S]*?</(?:think|thinking|mm:think)>", re.I)
_REFLECTION = re.compile(r"<reflection(?:\s[^>]*)?>[\s\S]*?</reflection>", re.I)
_REFLECTION_MD = re.compile(r"\n*#{2,4}[ \t]*反思[^\n]*\n[\s\S]*$", re.I)
_REPORT = re.compile(r"<report(?:\s[^>]*)?>([\s\S]*?)</report>", re.I)
_FENCE = re.compile(r"^\s*```[^\n]*\n?", re.MULTILINE)
_IMAGE = re.compile(r"!\[[^\]]*\]\([^)]*\)")
_LINK = re.compile(r"\[([^\]]*)\]\([^)]*\)")
_HEADING = re.compile(r"^\s{0,3}#{1,6}\s*", re.MULTILINE)
_RULE = re.compile(r"^\s{0,3}([-*_])(?:\s*\1){2,}\s*$", re.MULTILINE)
_BLOCKQUOTE = re.compile(r"^\s{0,3}>\s?", re.MULTILINE)
_INLINE_CODE = re.compile(r"`([^`]*)`")
_BOLD = re.compile(r"\*\*(?=\S)(.+?)(?<=\S)\*\*", re.DOTALL)
_ITALIC = re.compile(r"(?<!\*)\*(?=\S)(.+?)(?<=\S)\*(?!\*)", re.DOTALL)


def strip_internal_blocks(text: str) -> str:
    """剥离 agent 的内部输出块，规则与前端 chat.js 的 extractAssistantContent 一致。"""
    out = str(text or "")
    out = _THINK.sub("", out)
    out = _REFLECTION.sub("", out)
    out = _REFLECTION_MD.sub("", out)
    match = _REPORT.search(out)
    if match:
        return match.group(1).strip()
    return out


def strip_markdown(text: str) -> str:
    out = str(text or "")
    out = _IMAGE.sub("", out)
    out = _FENCE.sub("", out)
    out = _LINK.sub(r"\1", out)
    out = _HEADING.sub("", out)
    out = _RULE.sub("", out)
    out = _BLOCKQUOTE.sub("", out)
    out = _INLINE_CODE.sub(r"\1", out)
    out = _BOLD.sub(r"\1", out)
    out = _ITALIC.sub(r"\1", out)
    return out


def collapse_whitespace(text: str) -> str:
    out = str(text or "")
    out = re.sub(r"[ \t]+\n", "\n", out)
    out = re.sub(r"\n{3,}", "\n\n", out)
    return out.strip()


def split_chunks(text: str, limit: int) -> list[str]:
    if limit <= 0:
        raise ValueError("limit must be positive")
    body = str(text or "")
    if not body:
        return []
    chunks: list[str] = []
    current = ""
    for line in body.split("\n"):
        candidate = line if not current else f"{current}\n{line}"
        if len(candidate) <= limit:
            current = candidate
            continue
        if current:
            chunks.append(current)
            current = ""
        while len(line) > limit:
            chunks.append(line[:limit])
            line = line[limit:]
        current = line
    if current:
        chunks.append(current)
    return chunks


def render(text: str, limit: int) -> list[str]:
    cleaned = strip_markdown(strip_internal_blocks(text))
    return split_chunks(collapse_whitespace(cleaned), limit)
