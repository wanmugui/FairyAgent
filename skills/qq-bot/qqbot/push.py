"""主动推送：把文本和产物送到指定 QQ 会话。

与 bot.py 的入站链路完全解耦——不依赖事件循环 ready，也不依赖任何人先发过消息。
任何后台进程都可以：

    python3 -m qqbot.push --text "任务跑完了" --image /path/产物.png
    python3 -m qqbot.push --text "..." --to group:123456

走 QQ 开放平台的 HTTP OpenAPI，不经过 SDK 的事件派发层。
"""
from __future__ import annotations

import argparse
import base64
import json
import pathlib
import sys
import time
from typing import Any

import requests

API = "https://api.sgroup.qq.com"
TOKEN_URL = "https://bots.qq.com/app/getAppAccessToken"
CREDS = pathlib.Path.home() / ".config" / "fairy" / "qq-bot.json"

_token_cache: tuple[str, float] | None = None


def _creds() -> tuple[str, str]:
    data = json.loads(CREDS.read_text(encoding="utf-8"))
    return data["appId"], data["appSecret"]


def get_token() -> str:
    """取 access_token，带进程内缓存，剩余不足 60 秒时刷新。"""
    global _token_cache
    if _token_cache and time.time() < _token_cache[1] - 60:
        return _token_cache[0]
    appid, secret = _creds()
    resp = requests.post(
        TOKEN_URL,
        json={"appId": appid, "clientSecret": secret},
        timeout=20,
    )
    resp.raise_for_status()
    body = resp.json()
    token = body.get("access_token")
    if not token:
        raise RuntimeError(f"未取到 access_token: {body}")
    _token_cache = (token, time.time() + int(body.get("expires_in", 7200)))
    return token


def _headers() -> dict[str, str]:
    return {
        "Authorization": f"QQBot {get_token()}",
        "X-Union-Appid": _creds()[0],
    }


def _split_target(target: str) -> tuple[str, str]:
    """把 'c2c:<openid>' / 'group:<group_openid>' 拆成两段。"""
    if ":" not in target:
        raise ValueError(f"目标需带类型前缀，如 c2c:<openid>，收到: {target!r}")
    kind, conv = target.split(":", 1)
    if kind not in ("c2c", "group"):
        raise ValueError(f"未知会话类型 {kind!r}，只支持 c2c / group")
    if not conv:
        raise ValueError(f"目标缺少会话 id: {target!r}")
    return kind, conv


def send_text(target: str, text: str) -> dict[str, Any]:
    """纯文本。target 形如 'c2c:<openid>' 或 'group:<group_openid>'。"""
    kind, conv = _split_target(target)
    path = f"/v2/users/{conv}/messages" if kind == "c2c" else f"/v2/groups/{conv}/messages"
    resp = requests.post(
        API + path,
        headers=_headers(),
        json={"content": text, "msg_type": 0},
        timeout=30,
    )
    _raise(resp)
    return resp.json()


def upload_media(target: str, image: str | pathlib.Path) -> str:
    """上传本地图片，返回 file_info。上传与发送分离，便于被动回复复用。

    这个接口要的是 **JSON**：`file_data`（base64）或 `url`（公网可访问）。
    用 multipart 传文件会让平台认为"没有可下载的素材"，统一回
    40093007「富媒体文件下载失败」——那个报错和文件大小、格式、后台权限都
    无关，实测 16x16 的 PNG 与 64x64 的一样，换成 JSON+base64 立刻 200。
    """
    kind, conv = _split_target(target)
    file_path = pathlib.Path(image).expanduser()
    if not file_path.is_file():
        raise FileNotFoundError(f"图片不存在: {file_path}")

    base = f"/v2/users/{conv}" if kind == "c2c" else f"/v2/groups/{conv}"
    # file_type 是必填：1 图片 2 视频 3 语音 4 文件。缺了会报 40093006。
    payload = {
        "file_type": file_type_for(file_path),
        "file_data": base64.b64encode(file_path.read_bytes()).decode("ascii"),
        "srv_send_msg": False,
    }
    up = requests.post(API + base + "/files", headers=_headers(), json=payload, timeout=120)
    _raise(up)
    file_info = up.json().get("file_info")
    if not file_info:
        raise RuntimeError(f"上传未返回 file_info: {up.text[:200]}")
    return file_info


def file_type_for(path: pathlib.Path) -> int:
    """按扩展名给富媒体的 file_type 编号（沿用 qqbot 的约定）。"""
    suffix = path.suffix.lower()
    if suffix in (".mp4", ".mov", ".mkv", ".webm"):
        return 2
    if suffix in (".mp3", ".wav", ".m4a", ".amr", ".silk"):
        return 3
    if suffix in (".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp"):
        return 1
    return 4


def send_image(target: str, image: str | pathlib.Path) -> dict[str, Any]:
    """本地图片 -> 上传富媒体 -> 发消息。"""
    kind, conv = _split_target(target)
    file_info = upload_media(target, image)
    base = f"/v2/users/{conv}" if kind == "c2c" else f"/v2/groups/{conv}"

    resp = requests.post(
        API + base + "/messages",
        headers=_headers(),
        # msg_type 7 是富媒体。botpy 的文档写得很直接：
        # 「0 文本，1 图文混排，2 markdown，3 ark，4 embed，7 media 富媒体」，
        # 用 1 带 media 字段会被平台当成参数错误。
        json={"msg_type": 7, "media": {"file_info": file_info}},
        timeout=30,
    )
    _raise(resp)
    return resp.json()


def send(target: str, text: str = "", images: list[str] | None = None) -> list[dict]:
    """先发文本，再逐张发图。返回每一步的响应。"""
    out: list[dict] = []
    if text:
        out.append(send_text(target, text))
    for img in images or []:
        out.append(send_image(target, img))
    return out


def _raise(resp: requests.Response) -> None:
    if resp.status_code >= 400:
        raise RuntimeError(f"HTTP {resp.status_code}: {resp.text[:300]}")


CHANNELS = pathlib.Path(__file__).resolve().parents[3] / "config" / "channels.json"


def default_target() -> str:
    """从 channels.json 的 qq.allowC2C 取第一个会话作为默认推送目标。"""
    try:
        data = json.loads(CHANNELS.read_text(encoding="utf-8"))
        allowed = (data.get("qq") or {}).get("allowC2C") or []
    except (OSError, ValueError) as exc:
        raise RuntimeError(f"读不到频道配置 {CHANNELS}: {exc}") from exc
    if not allowed:
        raise RuntimeError(f"qq.allowC2C 为空，请在 {CHANNELS} 里配一个会话，或用 --to 指定")
    return f"c2c:{allowed[0]}"


def main(argv: list[str] | None = None) -> int:
    pol = argparse.ArgumentParser(description="主动推送文本和产物到 QQ")
    pol.add_argument("--to", help="c2c:<openid> 或 group:<group_openid>；省略则用配置里的默认会话")
    pol.add_argument("--text", default="", help="文本内容")
    pol.add_argument("--image", action="append", default=[], help="图片路径，可重复")
    pol.add_argument("--dry-run", action="store_true", help="只校验参数和凭据，不真发")
    args = pol.parse_args(argv)

    if not args.text and not args.image:
        pol.error("至少要给 --text 或 --image")

    target = args.to or default_target()

    if args.dry_run:
        token = get_token()
        print(f"凭据有效，token 已获取（{token[:6]}…，长 {len(token)}）")
        print(f"目标: {target}")
        print(f"文本: {len(args.text)} 字  图片: {len(args.image)} 张")
        for img in args.image:
            p = pathlib.Path(img).expanduser()
            print(f"  {'OK ' if p.is_file() else '缺失'} {p}")
        return 0

    results = send(target, args.text, args.image)
    for r in results:
        print("推送成功:", json.dumps(r, ensure_ascii=False)[:200])
    return 0


if __name__ == "__main__":
    sys.exit(main())
