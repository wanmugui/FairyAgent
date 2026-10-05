"""落盘的运行期状态：每个人最近一次会话的 context_token。

微信的 sendmessage 必须带 context_token，而它只在收到对方消息时才有。桥本来
只把它放在内存里，于是"到点提醒我"这种主动消息没法发——发消息的是另一个进程
（定时任务的调度器），它看不到桥的内存表。这里把它落一份盘：桥收到新 token 就
写，推送模块读，两边都不用改协议。

顺带这也解释了微信主动消息的规矩：**只能发给最近跟这个 bot 说过话的人**，
不是任意好友，token 过期或被清理后就发不出去。
"""
from __future__ import annotations

import json
import time
from pathlib import Path

from .config import account_path


def state_path(label: str, directory: Path | None = None) -> Path:
    return account_path(label, directory).with_suffix(".state.json")


def load_state(label: str, directory: Path | None = None) -> dict:
    try:
        raw = json.loads(state_path(label, directory).read_text(encoding="utf-8"))
        if isinstance(raw, dict) and isinstance(raw.get("contexts"), dict):
            return raw
    except (OSError, ValueError):
        pass
    return {"contexts": {}}


def load_contexts(label: str, directory: Path | None = None) -> dict[str, str]:
    """user_id -> context_token，桥启动时用它恢复上次的状态。"""
    contexts = {}
    for user_id, entry in load_state(label, directory).get("contexts", {}).items():
        token = str((entry or {}).get("token") or "").strip()
        if user_id and token:
            contexts[str(user_id)] = token
    return contexts


def remember_context(label: str, user_id: str, token: str, directory: Path | None = None) -> None:
    if not user_id or not token:
        return
    path = state_path(label, directory)
    state = load_state(label, directory)
    state["contexts"][str(user_id)] = {"token": str(token), "at": int(time.time() * 1000)}
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(path.name + ".tmp")
    temporary.write_text(json.dumps(state, ensure_ascii=False, indent=2), encoding="utf-8")
    temporary.replace(path)


def context_for(label: str, user_id: str, directory: Path | None = None) -> str:
    entry = load_state(label, directory).get("contexts", {}).get(str(user_id)) or {}
    return str(entry.get("token") or "").strip()
