"""ask_user 在微信通道里的问答桥。

与 qq-bot 的 ask_bridge 同源同语义（两个包各自独立部署，所以这里刻意复制一份
而不是互相 import）。背景、实现与取舍见 qqbot/ask_bridge.py 的模块注释。
"""
from __future__ import annotations

import json
import re
import time
from pathlib import Path

from .config import REPO_ROOT

RESULTS_PATH = REPO_ROOT / "workspace" / "ask_user" / "ask_results.json"

# 后端的等待时限。必须与 agent/internal/biz/tool/builtin/ask_user_wait.go 里的
# AskUserAutoSelectSeconds 一致（那边是 30，不是 90）。
#
# 这不是个 cosmetic 的数字：Go 侧在超时后会自己按 recommended 选项落答案并标
# auto_selected。也就是说微信这种没有倒计时 UI 的通道，主人只有这么多时间看到
# 问题并回话。桥如果对主人说一个错的、更宽松的数，主人会以为还来得及，结果
# 答复在后端那边永远输给了超时。
ASK_AUTO_SELECT_SECONDS = 30

# 段与段之间必须能被下一个题号切开。原来的 (.*) 加了 DOTALL 会把 "1A 2A"
# 整段吞进第一题，后面几题全丢 —— 看起来就是「我只答了第一题」。
_QUESTION_TOKEN = re.compile(
    r"(\d+)\s*[.、:：)）]?\s*(.*?)(?=\s*\d+\s*[A-Za-z0-9]|\n\s*\d+\s|\Z)",
    re.S,
)


def results_path() -> Path:
    return RESULTS_PATH


def _read_entries() -> list[dict]:
    path = results_path()
    if not path.is_file():
        return []
    try:
        entries = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return []
    return entries if isinstance(entries, list) else []


def _write_entries(entries: list[dict]) -> bool:
    path = results_path()
    try:
        path.parent.mkdir(parents=True, exist_ok=True)
        tmp = path.with_suffix(".json.tmp")
        tmp.write_text(json.dumps(entries, ensure_ascii=False, indent=2), encoding="utf-8")
        tmp.replace(path)
        return True
    except OSError:
        return False


def _questions_of(entry: dict) -> list[dict]:
    raw = (entry or {}).get("questions")
    if isinstance(raw, str):
        try:
            raw = json.loads(raw)
        except ValueError:
            return []
    if not isinstance(raw, list):
        return []
    return [q for q in raw if isinstance(q, dict)]


def format_questions(entry: dict, auto_after: int = 0) -> str:
    questions = _questions_of(entry)
    ask_type = str((entry or {}).get("ask_type") or "")
    lines: list[str] = ["【需要你定一下】"]
    for i, q in enumerate(questions, start=1):
        text = str(q.get("question") or q.get("text") or "").strip() or f"第 {i} 题"
        lines.append(f"\n{i}. {text}")
        options = q.get("options")
        if isinstance(options, list) and options:
            for j, opt in enumerate(options):
                if not isinstance(opt, dict):
                    continue
                label = str(opt.get("label") or opt.get("description") or "").strip()
                if not label:
                    continue
                mark = " ★推荐" if opt.get("recommended") else ""
                lines.append(f"   {chr(ord('A') + j)}. {label}{mark}")
    if ask_type and "confirm" in ask_type:
        lines.append("\n（这是个确认，回「确认」或直接说要改哪儿都行）")
    lines.append("\n回复方式：回序号选选项（如 1A 2B），或直接用文字说你的想法。")
    if auto_after:
        lines.append(f"（{auto_after} 秒内没回，我就按推荐项自动定。）")
    return "\n".join(lines)


def pending_index(seen_count: int) -> tuple[int, dict] | None:
    entries = _read_entries()
    for index in range(min(seen_count, len(entries)), len(entries)):
        entry = entries[index]
        if isinstance(entry, dict) and not entry.get("answered"):
            return index, entry
    return None


def current_count() -> int:
    return len(_read_entries())


def submit_reply(index: int, reply: str) -> bool:
    reply = str(reply or "").strip()
    if not reply:
        return False
    entries = _read_entries()
    if index < 0 or index >= len(entries):
        return False
    entries[index]["answered"] = True
    entries[index]["answer"] = _build_answer(entries[index], reply)
    entries[index]["answered_at"] = int(time.time())
    return _write_entries(entries)


def _build_answer(entry: dict, reply: str) -> object:
    questions = _questions_of(entry)
    segments = [m for m in _QUESTION_TOKEN.findall(reply) if m[0].strip().isdigit()]
    if not questions or not segments:
        return reply
    answers: dict[str, object] = {}
    for token, text in segments:
        idx = int(token) - 1
        if idx < 0 or idx >= len(questions):
            continue
        qid = str(questions[idx].get("id") or questions[idx].get("key") or f"q{idx + 1}")
        chosen, desc = _resolve_choice(questions[idx], text.strip())
        # 与后端 autoSelectAskAnswers 同构：值是 [label] 列表，描述另存 {id}_desc。
        # 网页端一直产出这个形状；桥要是塞个裸字符串，模型看到的就是一个
        # 从没见过的答案结构，很容易把哪题配哪个答案读错。
        answers[qid] = [chosen or text.strip()]
        if chosen and desc:
            answers[qid + "_desc"] = desc
    return answers or reply


def _resolve_choice(question: dict, text: str) -> tuple[str | None, str]:
    """把 "A" / "2" / "A. 描述" 落到选项上，返回 (label, description)。"""
    if not text:
        return None, ""
    options = question.get("options")
    if not isinstance(options, list) or not options:
        return None, ""
    head = re.split(r"[\s.、:：)）]", text, maxsplit=1)[0].strip().upper()
    if not head:
        return None
    for j, opt in enumerate(options):
        if not isinstance(opt, dict):
            continue
        letter = chr(ord("A") + j)
        if head in (letter, str(j + 1)):
            label = str(opt.get("label") or "").strip()
            desc = str(opt.get("description") or "").strip()
            return (label or desc or text), desc
    return None, ""
