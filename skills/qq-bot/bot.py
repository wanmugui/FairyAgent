#!/usr/bin/env python3
"""QQ 机器人入口：读配置并启动 botpy 客户端。"""
from __future__ import annotations

import logging
import os
import sys

from qqbot.bot import QQBotService
from qqbot.config import load_credentials, load_settings


def main() -> int:
    logging.basicConfig(level=logging.INFO,
                        format="%(asctime)s %(levelname)s %(name)s %(message)s")
    # botpy 会在 import 阶段自建 handler，basicConfig 常常变成空操作，
    # 导致应用层日志（本模块的 log.info / 启动 print）静默丢失。
    # 这里显式接管 root logger，保证应用日志一定落到 stderr。
    root = logging.getLogger()
    root.setLevel(logging.DEBUG if os.environ.get("FAIRY_QQ_DEBUG") else logging.INFO)
    if not any(getattr(h, "_fairy_owned", False) for h in root.handlers):
        handler = logging.StreamHandler(sys.stderr)
        handler.setFormatter(logging.Formatter(
            "%(asctime)s %(levelname)s [%(name)s] %(message)s"))
        handler._fairy_owned = True
        root.addHandler(handler)
    if os.environ.get("FAIRY_QQ_DEBUG"):
        logging.getLogger("botpy").setLevel(logging.DEBUG)
        logging.getLogger("qq-bot").info("已开启 DEBUG：将记录原始网关帧")
    creds = load_credentials()
    settings = load_settings()
    if not settings.enabled:
        print("qq-bot 在 config/channels.json 中被禁用", file=sys.stderr)
        return 1
    if not settings.allow_c2c and not settings.allow_groups:
        print("白名单为空：将拒绝所有消息。先发一条消息，从日志取 openid 填进配置。", file=sys.stderr)
    if creds.fairy_token:
        print("已绑定 Fairy 账户会话：QQ 消息会写进该账户的会话树。", file=sys.stderr)
    else:
        print("未绑定 Fairy 账户：QQ 消息按本机受信身份落进第一个账户的会话树。", file=sys.stderr)
    QQBotService(settings, creds.fairy_token).run(appid=creds.app_id, secret=creds.app_secret)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
