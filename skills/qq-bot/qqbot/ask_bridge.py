"""ask_user 在 IM 通道里的问答桥。

背景（查证过，别再按旧说法改）：ask_user 由 Go 侧实现，它把问题追加写进
`<workspace>/ask_user/ask_results.json`（`answered: false`），然后**阻塞等待**，
自带 `AskUserAutoSelectSeconds` 兜底超时；超时没人答就自动选 recommended 项并
标 `auto_selected`。问题在等待期间**只存在于这个文件里**，`waiting_user_input`
SSE 事件是工具返回之后才发的 —— 所以桥必须盯文件，光处理 SSE 事件看不到题。

之前的症状：桥只认 file_result / done，事件与文件都不看，于是主人在微信/QQ
里根本看不到问题，后端等满超时替他做了决定，全程无感知。

这里做两件事：
  1. poll_pending()  —— 在这一轮还在跑的时候轮询文件，发现未答问题就渲染成文本。
  2. submit_reply()  —— 主人的回复按「序号 + 自由文本都认」解析后写回文件，
                       正在等待的工具就会拿到答案继续跑；后端仍然保有超时兜底。
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

# 回复里每道题一段："1 A"、"2：随便说点什么都行"。
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
    """把一道 ask 渲染成 IM 能读的纯文本：编号题目 + 字母选项 + 回复说明。"""
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
    """找出「在 seen_count 之后新出现、且还没人回答」的那一条。

    seen_count 由桥自己维护：每一轮开始时记下当前条数，之后只看新增的。
    """
    entries = _read_entries()
    for index in range(min(seen_count, len(entries)), len(entries)):
        entry = entries[index]
        if isinstance(entry, dict) and not entry.get("answered"):
            return index, entry
    return None


def current_count() -> int:
    return len(_read_entries())


def submit_reply(index: int, reply: str) -> bool:
    """把主人的回复写回对应条目，让正在等待的工具拿到答案。

    答案按主人选的「序号 + 自由文本都认」处理：能逐段对上就按题给结构化答案，
    对不上就整段当作自由文本。后端两种都收（normalizeAnswerPayload 同时接受
    结构化 map 和字符串），所以这里不必猜后端偏好。
    """
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
    """尽量按题拆分；拆不出来就整段交给后端当自由文本。"""
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


def _resolve_choice(question: dict, text: str) -> str | None:
    """把 "A" / "1" / "A. 描述" 这样的回复落到选项的 label 上。"""
    if not text:
        return None
    options = question.get("options")
    if not isinstance(options, list) or not options:
        return None
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
