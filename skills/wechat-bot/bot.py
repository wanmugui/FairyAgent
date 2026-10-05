#!/usr/bin/env python3
"""微信 iLink 私聊桥接入口。"""
from __future__ import annotations

import argparse
import logging
import sys

from wechatbot.bot import Bridge
from wechatbot.config import load_account, load_settings


def main() -> int:
    parser = argparse.ArgumentParser(description="Fairy 微信 iLink 私聊桥接")
    parser.add_argument("--account", default="default", help="账号标签（凭据文件名）")
    args = parser.parse_args()

    logging.basicConfig(level=logging.INFO,
                        format="%(asctime)s %(levelname)s %(name)s %(message)s")
    account = load_account(args.account)
    settings = load_settings()
    if not settings.enabled:
        print("wechat 通道在 config/channels.json 中被禁用", file=sys.stderr)
        return 1
    if not settings.allow_users:
        print("白名单为空：将拒绝所有消息。先发一条消息，从日志取 user_id 填进配置。", file=sys.stderr)
    Bridge(account, settings).run_forever()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
