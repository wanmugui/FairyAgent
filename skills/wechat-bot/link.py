#!/usr/bin/env python3
"""扫码接入 iLink 机器人：把二维码和状态按 JSON 行吐给调用方。

和 login.py 的区别只有一处：login.py 是给人在终端里用的，二维码写文件、进度
打到 stdout；这个是给前端按钮用的，所以每个事件都是一行 JSON，二维码直接带
base64 的 PNG，调用方不需要跟这个进程共享文件系统以外的任何约定。

    调用方（frontend/wechat_link.cjs）负责：谁有资格接入、凭据落到哪个账号名下、
    接入之后做什么。这里只负责协议本身。

事件：
    {"event":"qr","qrcode":"...","png_base64":"...","expires_in":300}
    {"event":"expired"}                      二维码过期，紧接着会再来一条 qr
    {"event":"confirmed","bot_token":...,"ilink_bot_id":...,"ilink_user_id":...,"base_url":...}
    {"event":"timeout"} / {"event":"error","error":"..."}
"""
from __future__ import annotations

import argparse
import base64
import io
import json
import sys
import time

import qrcode

from wechatbot.ilink import ILinkClient

# 微信侧的二维码大约 5 分钟过期，刷新几次足够一个人掏出手机。
QR_REFRESH_LIMIT = 3


def emit(**payload: object) -> None:
    # flush 是必需的，不是谨慎：调用方按行读、按行显示进度，缓冲住的话前端
    # 会一直停在"正在获取二维码"。
    sys.stdout.write(json.dumps(payload, ensure_ascii=False) + "\n")
    sys.stdout.flush()


def png_base64(text: str) -> str:
    image = qrcode.make(text)
    buffer = io.BytesIO()
    image.save(buffer, format="PNG")
    return base64.b64encode(buffer.getvalue()).decode("ascii")


def main() -> int:
    parser = argparse.ArgumentParser(description="生成 iLink 登录二维码并等待扫码")
    parser.add_argument("--label", default="default", help="账号标签，只用于日志")
    parser.add_argument("--timeout", type=int, default=300, help="整轮等待的秒数")
    parser.add_argument("--poll", type=float, default=2.0, help="状态轮询间隔秒")
    args = parser.parse_args()

    client = ILinkClient()
    deadline = time.time() + args.timeout
    refreshes = 0

    while time.time() < deadline:
        try:
            data = client.new_qrcode()
        except Exception as error:  # noqa: BLE001
            emit(event="error", error=f"获取二维码失败：{error}")
            return 1
        qrcode_value = str(data.get("qrcode") or "")
        qr_url = str(data.get("qrcode_img_content") or "")
        if not qrcode_value or not qr_url:
            emit(event="error", error=f"二维码响应异常：{data}")
            return 1
        try:
            emit(event="qr", qrcode=qrcode_value, png_base64=png_base64(qr_url),
                 expires_in=max(1, int(deadline - time.time())))
        except Exception as error:  # noqa: BLE001
            emit(event="error", error=f"渲染二维码失败：{error}")
            return 1

        while time.time() < deadline:
            time.sleep(args.poll)
            try:
                status = client.qrcode_status(qrcode_value)
            except Exception as error:  # noqa: BLE001
                # 单次查询失败不代表二维码失效，继续等下一轮。
                emit(event="status_error", error=str(error))
                continue
            state = str(status.get("status") or "wait")
            if state == "confirmed":
                if not status.get("bot_token"):
                    emit(event="error", error="登录成功但未返回 bot_token")
                    return 1
                emit(
                    event="confirmed",
                    bot_token=str(status["bot_token"]),
                    ilink_bot_id=str(status.get("ilink_bot_id") or ""),
                    ilink_user_id=str(status.get("ilink_user_id") or ""),
                    base_url=str(status.get("baseurl") or "https://ilinkai.weixin.qq.com"),
                )
                return 0
            if state == "expired":
                emit(event="expired")
                break
            if state == "scaned":
                emit(event="scanned")

        refreshes += 1
        if refreshes >= QR_REFRESH_LIMIT:
            break

    emit(event="timeout")
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
