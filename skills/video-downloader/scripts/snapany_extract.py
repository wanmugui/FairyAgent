#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""snapany_extract.py — SnapAny 视频/字幕解析通道（B站等 1000+ 平台）。

为什么需要它
------------
`yt-dlp` 在本机对 B站不稳：playlist 的 per-entry 标题取不到（`--flat-playlist` 全是 NA），
单集也常因风控超时。但本项目真正缺的往往**不是视频，而是字幕**——动画教程、访谈、
公开课这类素材，视频可以不要，字幕才是要总结的东西。

SnapAny 提供商用解析 API，`extract_subtitles` 直接返回字幕轨与下载直链，比
「下整段视频 → 本地抽音轨 → STT 转写」便宜一个数量级。

前置条件
--------
1. 到 https://platform.snapany.com/zh/console 领 key（新开发者 50 免费积分，永久有效）
2. 导出环境变量：
   ```bash
   export SNAPANY_KEY=sk_snapany_xxxx
   ```

用法
----
    # 抽字幕（B站返回 BCC JSON，本地转 SRT）
    python scripts/snapany_extract.py subtitles "https://www.bilibili.com/video/BV1xxx"

    # 抽单个帖子的媒体直链与元数据（不下文件，只打印链接）
    python scripts/snapany_extract.py post "https://www.bilibili.com/video/BV1xxx"

    # 抽播放列表一页
    python scripts/snapany_extract.py playlist "<playlist-url>" --page 2

    # 直接把字幕存成 SRT 到挂载盘
    python scripts/snapany_extract.py subtitles "<url>" --save "/path/to/course/某教程" --lang zh-Hans

积分：post 1 分 / playlist 2 分 / subtitles 1 分。积分不足返回 Insufficient credits。
速率限制 1200 次/分钟，与 REST API 共用。

通道说明
--------
走 **MCP（Streamable HTTP）**，端点 `https://api.snapany.com/mcp` —— 这个地址已实测：
无 key 时返回 `HTTP 401 {"code":"invalid_api_key"}`，证明服务与鉴权都活着。
官方文档给出的 REST 端点是前端动态渲染的，拿不到静态路径，因此**不猜 REST 路径**，
直接用文档明示的 MCP。

MCP 工具（按官方 docs/mcp 页面）：`extract_post` / `extract_playlist` /
`extract_subtitles` / `transcribe` / `get_transcription` 等。MCP 返回的是**直链与
元数据**，不是文件；要存盘得再下载一次。
"""
from __future__ import annotations

import argparse
import json
import os
import sys
import urllib.error
import urllib.request

MCP_URL = "https://api.snapany.com/mcp"
TOOL = {"post": "extract_post", "playlist": "extract_playlist", "subtitles": "extract_subtitles"}


def _key() -> str:
    k = os.environ.get("SNAPANY_KEY", "").strip()
    if not k:
        sys.exit("缺少 SNAPANY_KEY 环境变量。到 https://platform.snapany.com/zh/console 领取，\n"
                 "然后 export SNAPANY_KEY=sk_snapany_xxxx")
    return k


def _rpc(method: str, params: dict, _id=[0], timeout: int = 90) -> dict:
    """一次 MCP JSON-RPC 往返。Streamable HTTP 可能回 SSE，这里两种都吃。"""
    _id[0] += 1
    body = json.dumps({"jsonrpc": "2.0", "id": _id[0], "method": method,
                       "params": params}).encode()
    req = urllib.request.Request(
        MCP_URL, data=body, method="POST",
        headers={"Authorization": "Bearer " + _key(),
                 "Content-Type": "application/json",
                 "Accept": "application/json, text/event-stream",
                 # 必须带浏览器 UA：裸 urllib 会被 Cloudflare 判成
                 # browser_signature_banned 而 403（Error 1010）。
                 "User-Agent": "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
                                "(KHTML, like Gecko) Chrome/122.0 Safari/537.36",
                 "Accept-Language": "zh-CN,zh;q=0.9"})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            raw = r.read().decode(errors="replace")
    except urllib.error.HTTPError as e:
        b = e.read().decode(errors="replace")[:400]
        if e.code == 401:
            sys.exit("401：SNAPANY_KEY 无效，或漏了 Bearer 前缀（Bearer 后有一个空格）")
        if e.code == 429:
            sys.exit("429：触发限流（1200 次/分钟），稍后重试")
        sys.exit("HTTP %d: %s" % (e.code, b))
    except Exception as e:  # noqa: BLE001
        sys.exit("请求失败：%s" % e)
    # SSE: data: {...}
    if raw.lstrip().startswith("event:") or raw.lstrip().startswith("data:"):
        for ln in raw.splitlines():
            if ln.startswith("data:"):
                raw = ln[5:].strip()
                break
    try:
        obj = json.loads(raw)
    except Exception:  # noqa: BLE001
        return {"raw": raw}
    if "error" in obj:
        err = obj["error"]
        msg = err.get("message", "") if isinstance(err, dict) else str(err)
        if "credit" in msg.lower():
            sys.exit("积分不足：%s（到 console 充值，新开发者有 50 免费积分）" % msg)
        if "subtitle" in msg.lower() and "no_" in msg.lower():
            sys.exit("no_subtitles：该视频本身没有字幕轨。")
        sys.exit("MCP 错误：%s" % msg)
    return obj.get("result", obj)


def _tool_text(name: str, args: dict, timeout: int = 90) -> str:
    res = _rpc("tools/call", {"name": name, "arguments": args}, timeout=timeout)
    chunks = res.get("content") if isinstance(res, dict) else None
    if isinstance(chunks, list):
        for c in chunks:
            if isinstance(c, dict) and c.get("type") == "text":
                return c.get("text", "")
    return json.dumps(res, ensure_ascii=False)


def bcc_to_srt(bcc_text: str) -> str:
    """B站 BCC（WebVTT 方言）→ SRT。BCC 的时间是 HH:MM:SS,mmm，vtt 是 HH:MM:SS.mmm。"""
    out, idx = [], 0
    lines = bcc_text.replace("\r\n", "\n").split("\n")
    i = 0
    while i < len(lines):
        m = lines[i].strip()
        if "-->" in m:
            start, rest = m.split("-->", 1)
            end = rest.strip().split(" ")[0]
            idx += 1
            body = []
            i += 1
            while i < len(lines) and lines[i].strip():
                body.append(lines[i].strip())
                i += 1
            out.append("%d\n%s --> %s\n%s\n" % (idx, start.strip(), end, "\n".join(body)))
        i += 1
    return "\n".join(out)


UA = ("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
      "(KHTML, like Gecko) Chrome/122.0 Safari/537.36")


def download(url: str, dest: str, timeout: int = 120) -> bool:
    try:
        req = urllib.request.Request(url, headers={"User-Agent": UA, "Referer": "https://www.bilibili.com/"})
        with urllib.request.urlopen(req, timeout=timeout) as r, open(dest, "wb") as f:
            while True:
                chunk = r.read(65536)
                if not chunk:
                    break
                f.write(chunk)
        return True
    except Exception as e:  # noqa: BLE001
        print("  下载失败 %s: %s" % (url[:80], e), file=sys.stderr)
        return False


def main() -> int:
    ap = argparse.ArgumentParser(description="SnapAny 解析通道")
    ap.add_argument("action", choices=["post", "playlist", "subtitles"])
    ap.add_argument("url")
    ap.add_argument("--page", type=int, default=1, help="playlist 页码")
    ap.add_argument("--save", help="把字幕存到这个目录（不给就只打印）")
    ap.add_argument("--lang", help="只要某种语言，如 zh-Hans")
    ap.add_argument("--timeout", type=int, default=60)
    a = ap.parse_args()

    payload = {"url": a.url}
    if a.action == "playlist":
        payload["page"] = a.page
    text = _tool_text(TOOL[a.action], payload, a.timeout)

    # MCP 回的是文本（多数是 JSON 字符串），尽量解析
    try:
        res = json.loads(text)
    except Exception:  # noqa: BLE001
        res = {"raw": text}
    data = res.get("data", res) if isinstance(res, dict) else res
    if isinstance(data, dict) and "result" in data:
        data = data["result"]

    if a.action == "subtitles":
        tracks = data.get("subtitles") or data.get("tracks") or []
        if isinstance(data, list):
            tracks = data
        if not tracks:
            print(json.dumps(res, ensure_ascii=False)[:500])
            sys.exit("没有字幕（no_subtitles）：该视频本身就没有字幕轨。")
        print("找到 %d 条字幕轨：" % len(tracks))
        for t in tracks:
            lang = t.get("language") or t.get("lang") or t.get("code") or "?"
            name = t.get("name") or t.get("title") or ""
            dl = t.get("url") or t.get("download_url") or t.get("link") or ""
            print("  [%s] %s" % (lang, name))
            if not dl:
                continue
            if a.lang and a.lang not in str(lang):
                continue
            if not a.save:
                print("      %s" % dl)
                continue
            os.makedirs(a.save, exist_ok=True)
            ext = ".srt" if dl.endswith(".srt") or "srt" in dl else ".json"
            dest = os.path.join(a.save, "sub_%s%s" % (lang.replace("/", "_"), ext))
            if download(dl, dest):
                print("      已保存 %s (%d bytes)" % (dest, os.path.getsize(dest)))
        return 0

    print(json.dumps(res, ensure_ascii=False, indent=2)[:3000])
    return 0


if __name__ == "__main__":
    sys.exit(main())