"""主动推送：把文本、图片、视频、文件送到指定微信用户。

给定时任务用——到点了要主动说一句话，而不是等对方先开口。和 qqbot.push 是同一
个角色的两条腿，差别在凭据：QQ 用 appId/appSecret 换 token 就能发（平台另外限
"最近互动过"），微信必须带 context_token，也就是对方最近跟这个 bot 说过话的
凭据，由桥落盘（见 state.py）。

    python -m wechatbot.push --to o9cq...@im.wechat --text "该吃药了"
"""
from __future__ import annotations

import argparse
import json
import pathlib
import sys

from .config import DEFAULT_ACCOUNT_DIR, Account, load_account
from .ilink import ILinkClient
from .state import context_for


def account_labels(directory: pathlib.Path | None = None) -> list[str]:
    base = pathlib.Path(directory or DEFAULT_ACCOUNT_DIR).expanduser()
    if not base.is_dir():
        return []
    labels = []
    for path in sorted(base.glob("*.json")):
        if path.name.endswith(".state.json") or path.name.endswith(".discarded"):
            continue
        labels.append(path.stem)
    return labels


def find_account_for(user_id: str, directory: pathlib.Path | None = None) -> Account | None:
    """找出这个微信 id 归哪个已接入的 bot：账号文件里写着它只回谁。"""
    for label in account_labels(directory):
        try:
            account = load_account(label, directory)
        except Exception:  # noqa: BLE001 - 一个坏账号不该挡住别的账号
            continue
        if user_id in account.allow_users or user_id == account.ilink_user_id:
            return account
    return None


def _client_and_token(user_id: str, directory: pathlib.Path | None = None):
    account = find_account_for(user_id, directory)
    if account is None:
        raise RuntimeError(f"这个微信不属于任何已接入的 bot：{user_id}")
    token = context_for(account.label, user_id, directory)
    if not token:
        raise RuntimeError(
            "微信只能发给最近跟这个 bot 说过话的人：还没有它的 context_token，"
            "让对方先给这个 bot 发一条消息"
        )
    return ILinkClient(account.base_url, account.bot_token), token


def send_text(user_id: str, text: str, directory: pathlib.Path | None = None) -> dict:
    client, token = _client_and_token(user_id, directory)
    return client.send_text(user_id, text, token)


def send_video(user_id: str, video: str, directory: pathlib.Path | None = None) -> dict:
    client, token = _client_and_token(user_id, directory)
    return client.send_video(user_id, video, token)


def send_file(user_id: str, file: str, directory: pathlib.Path | None = None) -> dict:
    client, token = _client_and_token(user_id, directory)
    return client.send_file(user_id, file, token)


def _check_paths(paths: list[str], kind: str) -> list[str]:
    """发之前先确认文件都在，免得传到一半才炸。返回缺失路径列表。"""
    missing = [p for p in paths if not pathlib.Path(p).expanduser().is_file()]
    for p in missing:
        print(f"{kind}文件不存在：{p}", file=sys.stderr)
    return missing


def send(user_id: str, text: str = "", images: list[str] | None = None,
         videos: list[str] | None = None, files: list[str] | None = None,
         directory: pathlib.Path | None = None) -> list[dict]:
    """先发文本，再依次发图、视频、文件。媒体都走 CDN 那套（见 ILinkClient.send_image）。"""
    client, token = _client_and_token(user_id, directory)
    out: list[dict] = []
    if text:
        out.append(client.send_text(user_id, text, token))
    for image in images or []:
        out.append(client.send_image(user_id, image, token))
    for video in videos or []:
        out.append(client.send_video(user_id, video, token))
    for file in files or []:
        out.append(client.send_file(user_id, file, token))
    return out


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="主动推送文本/图片/视频/文件到微信")
    parser.add_argument("--to", required=True, help="微信用户 id，如 o9cq...@im.wechat")
    parser.add_argument("--text", default="", help="文本内容")
    parser.add_argument("--image", action="append", default=[], help="图片路径，可重复")
    parser.add_argument("--video", action="append", default=[], help="视频路径，可重复")
    parser.add_argument("--file", action="append", default=[], help="任意文件路径（zip/pdf/文档），可重复")
    parser.add_argument("--dry-run", action="store_true", help="只检查账号与 token，不发")
    parser.add_argument("--directory", default=None, help="账号目录（默认 ~/.config/fairy/wechat，测试用）")
    args = parser.parse_args(argv)
    directory = pathlib.Path(args.directory) if args.directory else None

    if not (args.text or args.image or args.video or args.file or args.dry_run):
        parser.error("至少要给 --text / --image / --video / --file 之一")

    if _check_paths(args.image, "图片") or _check_paths(args.video, "视频") or _check_paths(args.file, "文件"):
        return 1

    if args.dry_run:
        account = find_account_for(args.to, directory)
        if account is None:
            print(f"账号：找不到（{args.to}）")
            return 1
        token = context_for(account.label, args.to, directory)
        print(f"账号：{account.label}（{account.ilink_bot_id or '?'}）")
        print(f"context_token：{'有（' + str(len(token)) + ' 字符）' if token else '没有——对方需要先发一条消息'}")
        print(f"文本：{len(args.text)} 字  图片：{len(args.image)} 张  "
              f"视频：{len(args.video)} 个  文件：{len(args.file)} 个")
        return 0 if token else 1

    results = send(args.to, args.text, args.image, args.video, args.file, directory)
    for result in results:
        print("推送成功:", json.dumps(result, ensure_ascii=False)[:200])
    return 0


if __name__ == "__main__":
    sys.exit(main())
