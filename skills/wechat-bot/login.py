#!/usr/bin/env python3
"""扫码登录 iLink 机器人并保存凭据（可按 --label 存多个账号）。"""
from __future__ import annotations

import argparse
import time

import qrcode

from wechatbot.config import Account, account_path, save_account
from wechatbot.ilink import ILinkClient


def main() -> int:
    parser = argparse.ArgumentParser(description="登录微信 iLink 机器人")
    parser.add_argument("--label", default="default", help="账号标签，决定凭据文件名")
    parser.add_argument("--qr", default="/tmp/wechat-ilink-qr.png", help="二维码图片输出路径")
    parser.add_argument("--timeout", type=int, default=300, help="等待扫码的秒数")
    args = parser.parse_args()

    client = ILinkClient()
    deadline = time.time() + args.timeout
    while time.time() < deadline:
        data = client.new_qrcode()
        qrcode_value = str(data.get("qrcode") or "")
        qr_url = str(data.get("qrcode_img_content") or "")
        if not qrcode_value or not qr_url:
            raise SystemExit(f"二维码响应异常：{data}")
        qrcode.make(qr_url).save(args.qr)
        print(f"二维码已写入 {args.qr}，请用微信扫码（有效期约 5 分钟）")

        while time.time() < deadline:
            time.sleep(2)
            status = client.qrcode_status(qrcode_value)
            state = str(status.get("status") or "wait")
            if state == "confirmed":
                if not status.get("bot_token"):
                    raise SystemExit("登录成功但未返回 bot_token")
                account = Account(
                    bot_token=str(status["bot_token"]),
                    ilink_bot_id=str(status.get("ilink_bot_id") or ""),
                    ilink_user_id=str(status.get("ilink_user_id") or ""),
                    base_url=str(status.get("baseurl") or "https://ilinkai.weixin.qq.com"),
                    label=args.label,
                )
                path = save_account(account)
                print(f"登录成功：bot_id={account.ilink_bot_id} 凭据已保存到 {path}")
                return 0
            if state == "expired":
                print("二维码过期，重新获取")
                break
    print("等待超时，未完成登录")
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
