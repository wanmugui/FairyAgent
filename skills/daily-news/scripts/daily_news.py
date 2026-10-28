#!/usr/bin/env python3
# Daily News fetcher.
# Pulls headlines from authoritative Chinese sources + hot-search boards, filters
# ads / celebrity noise, dedupes, and emits a Markdown brief at
# <workspace>/output/daily_news_<YYYY-MM-DD>.md (or $DAILY_NEWS_OUTDIR).
#
# Originally embedded as a Python heredoc in scripts/news_trigger.sh; extracted so
# the same logic can run as a stand-alone skill under Fairy's "no bash" rule.
# Network calls are best-effort: any single source failing should not abort the
# rest of the brief. Each fetcher has its own timeout so a slow upstream does
# not stall the whole pipeline.

from __future__ import annotations

import argparse
import datetime as dt
import html
import io
import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request

# Windows console (cp936/cp1252) chokes on emojis in stdout. Force UTF-8 for
# both streams so dry-run previews render the 📰/🔴 markers the brief uses.
try:
    sys.stdout.reconfigure(encoding="utf-8")
    sys.stderr.reconfigure(encoding="utf-8")
except (AttributeError, io.UnsupportedOperation):
    pass

USER_AGENT = (
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
)

# Words we never want to push — pure ads, marketing, or celebrity gossip.
AD_KEYWORDS = (
    "开户", "下载", "app", "领券", "优惠", "抽奖", "免费", "扫码", "小程序",
    "公众号", "广告", "直播", "秒杀", "请客", "狂撒", "小样", "限量",
    "福利", "礼包", "抢购", "特惠", "爆款", "打卡",
)
STAR_KEYWORDS = (
    "王俊凯", "吴宇恒", "杨幂", "刘德华", "周杰伦", "蔡徐坤", "王一博",
    "肖战", "迪丽热巴", "杨紫", "赵丽颖", "Angelababy", "杨颖",
    "李现", "成毅", "赵露思", "虞书欣", "宋雨琦", "张凌赫",
    "刘昊然", "董子健", "韩昊霖", "于适", "辛芷蕾", "马斯克",
    "王濛", "梅西", "C罗", "姆巴佩", "樊振东", "孙颖莎", "马龙",
    "全红婵", "谷爱凌", "苏炳添", "吴艳妮", "林雨薇", "董宇辉",
    "小杨哥", "疯狂小杨哥", "李佳琦", "薇娅", "罗永浩", "俞敏洪",
    "张雪峰", "黄子韬", "杨超越", "王鹤棣", "魏大勋", "白敬亭",
    "宋轶", "张颂文", "吴京", "沈腾", "马丽", "贾玲", "韩红",
    "那英", "汪峰", "周深", "华晨宇", "毛不易", "薛之谦", "林俊杰",
    "五月天", "邓紫棋", "王心凌", "蔡依林", "萧亚轩", "林宥嘉",
    "胡彦斌", "张杰", "陈奕迅", "张学友", "郭富城", "黎明",
    "谢霆锋", "古天乐", "刘青云", "周润发", "成龙", "李连杰",
    "甄子丹", "吴亦凡", "鹿晗", "张艺兴", "华晨宇",
)


def http_get(url: str, timeout: int = 10, extra_headers: dict | None = None) -> bytes | None:
    headers = {"User-Agent": USER_AGENT}
    if extra_headers:
        headers.update(extra_headers)
    req = urllib.request.Request(url, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return resp.read()
    except (urllib.error.URLError, TimeoutError, OSError) as exc:
        print(f"[warn] GET {url} failed: {exc}", file=sys.stderr)
        return None


def is_ad(text: str) -> bool:
    low = text.lower()
    return any(kw in low for kw in AD_KEYWORDS)


def is_star(text: str) -> bool:
    return any(kw in text for kw in STAR_KEYWORDS)


def dedupe(items: list[tuple[str, str]]) -> list[tuple[str, str]]:
    seen = set()
    out: list[tuple[str, str]] = []
    for title, url in items:
        title = (title or "").strip()
        url = (url or "").strip()
        if not title or len(title) < 8:
            continue
        if title in seen:
            continue
        seen.add(title)
        out.append((title[:80], url))
    return out


def fetch_rmrb() -> list[tuple[str, str]]:
    """People's Daily mobile RSS."""
    body = http_get("https://m.peopleapp.com/rss/")
    if not body:
        return []
    text = body.decode("utf-8", errors="ignore")
    out: list[tuple[str, str]] = []
    for title in re.findall(r"<title>([^<]+)</title>", text):
        title = html.unescape(title).strip()
        if title and "RSS" not in title and len(title) >= 8:
            out.append((title[:50], "https://m.peopleapp.com/"))
        if len(out) >= 3:
            break
    return out


def fetch_xinhua() -> list[tuple[str, str]]:
    body = http_get("https://www.xinhuanet.com/politics/")
    if not body:
        return []
    text = body.decode("utf-8", errors="ignore")
    out: list[tuple[str, str]] = []
    for href, title in re.findall(
        r'<a[^>]+href="(https?://[^"]+)"[^>]*>([^<]{10,60})</a>', text
    ):
        title = html.unescape(title).strip()
        if title and "politics" in href:
            out.append((title, href))
        if len(out) >= 3:
            break
    return out


def fetch_cctv() -> list[tuple[str, str]]:
    body = http_get("https://news.cctv.com/politics/")
    if not body:
        return []
    text = body.decode("utf-8", errors="ignore")
    out: list[tuple[str, str]] = []
    for title in re.findall(r"<title>([^<]+)</title>", text):
        title = html.unescape(title).strip()
        if title and "CCTV" in title and len(title) >= 10:
            out.append((title[:50], "https://news.cctv.com/politics/"))
        if len(out) >= 3:
            break
    return out


def fetch_weibo() -> list[tuple[str, str]]:
    body = http_get(
        "https://weibo.com/ajax/side/hotSearch",
        extra_headers={"Referer": "https://weibo.com"},
    )
    if not body:
        return []
    try:
        data = json.loads(body.decode("utf-8", errors="ignore"))
    except json.JSONDecodeError:
        return []
    out: list[tuple[str, str]] = []
    for item in data.get("data", {}).get("realtime", [])[:15]:
        word = (item.get("word") or "").strip()
        if word and not is_ad(word) and not is_star(word):
            url = "https://s.weibo.com/weibo?q=" + urllib.parse.quote(word, safe="")
            out.append((word, url))
    return out[:8]


def fetch_baidu() -> list[tuple[str, str]]:
    body = http_get("https://top.baidu.com/board?tab=realtime")
    if not body:
        return []
    text = body.decode("utf-8", errors="ignore")
    out: list[tuple[str, str]] = []
    for word in re.findall(r'"word":"([^"]+)"', text)[:15]:
        word = html.unescape(word).strip()
        if word and not is_ad(word) and not is_star(word):
            encoded = urllib.parse.quote(word, safe="")
            url = f"https://www.baidu.com/s?wd={encoded}&sa=fyb_hp_news"
            out.append((word, url))
    return out[:8]


def fetch_zhihu() -> list[tuple[str, str]]:
    body = http_get(
        "https://www.zhihu.com/api/v3/feed/topstory/hot-lists/total?limit=10"
    )
    if not body:
        return []
    try:
        data = json.loads(body.decode("utf-8", errors="ignore"))
    except json.JSONDecodeError:
        return []
    out: list[tuple[str, str]] = []
    for item in data.get("data", [])[:10]:
        target = item.get("target", {}) or {}
        title = (target.get("title") or "").strip()
        if title and not is_ad(title) and not is_star(title):
            url = (target.get("url") or "").replace("//www.zhihu.com", "https://www.zhihu.com")
            out.append((title[:30], url))
    return out[:6]


def fetch_globaltimes() -> list[tuple[str, str]]:
    body = http_get("https://www.globaltimes.cn/")
    if not body:
        return []
    text = body.decode("utf-8", errors="ignore")
    out: list[tuple[str, str]] = []
    for href, title in re.findall(
        r'<a[^>]+href="(https?://[^"]+)"[^>]*>([^<]+)</a>', text
    ):
        title = re.sub(r"<[^>]+>", "", html.unescape(title)).strip()
        if len(title) >= 15 and "page" in href:
            out.append((title, href))
        if len(out) >= 4:
            break
    return out


def fetch_finance() -> list[tuple[str, str]]:
    body = http_get("https://finance.sina.com.cn/")
    if not body:
        return []
    text = body.decode("utf-8", errors="ignore")
    out: list[tuple[str, str]] = []
    for href, title in re.findall(
        r'<a[^>]+href="(https?://[^"]+)"[^>]*>([^<]+)</a>', text
    ):
        title = re.sub(r"<[^>]+>", "", html.unescape(title)).strip()
        if len(title) >= 8:
            if not href.endswith("/"):
                href = href + "/"
            out.append((title, href))
        if len(out) >= 4:
            break
    return [f for f in out if not is_ad(f[0])][:4]


def render_brief(today: str, time_str: str, domestic, world, finance) -> str:
    lines = [
        f"📰 Daily News {today}",
        "",
        "【国内热点】🔴 权威媒体 + 百度热搜",
    ]
    for t, u in domestic:
        if u and u != "#":
            lines.append(f"• [{t}]({u})")
        else:
            lines.append(f"• {t}")

    lines.extend(["", "【国际热点】🌐 环球时报"])
    for t, u in world:
        if u and u != "#":
            lines.append(f"• [{t}]({u})")
        else:
            lines.append(f"• {t}")

    lines.extend(["", "📈 【金融风向标】今日股市"])
    for t, u in finance:
        if u and u != "#":
            lines.append(f"• [{t}]({u})")
        else:
            lines.append(f"• {t}")

    lines.extend(["", f"* {time_str} (skill: daily-news)"])
    return "\n".join(lines)


def main() -> int:
    parser = argparse.ArgumentParser(description="Generate daily news brief")
    parser.add_argument(
        "--outdir",
        default=os.environ.get(
            "DAILY_NEWS_OUTDIR",
            os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "..", "workspace", "output"),
        ),
        help="Directory to write the daily_news_<DATE>.md file",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Print the brief to stdout instead of writing the file",
    )
    args = parser.parse_args()

    today = dt.datetime.now().strftime("%Y-%m-%d")
    time_str = dt.datetime.now().strftime("%H:%M")

    print(f"[info] generating daily news for {today}", file=sys.stderr)

    authoritative = fetch_rmrb() + fetch_xinhua() + fetch_cctv()
    hot = fetch_weibo() + fetch_baidu() + fetch_zhihu()
    world = fetch_globaltimes()
    finance = fetch_finance()

    # Build the domestic block: authoritative first (capped at 4), then hot
    # boards fill the rest up to 10. Both go through dedupe + star filter.
    domestic: list[tuple[str, str]] = []
    for title, url in dedupe(authoritative):
        if not is_star(title):
            domestic.append((title, url))
        if len(domestic) >= 4:
            break
    if len(domestic) < 10:
        existing_titles = {t for t, _ in domestic}
        for title, url in dedupe(hot):
            if title in existing_titles or is_star(title):
                continue
            domestic.append((title, url))
            existing_titles.add(title)
            if len(domestic) >= 10:
                break

    domestic = domestic[:10]
    world = dedupe(world)[:4] if world else [("暂无", "#")]
    finance = finance[:4] if finance else [("暂无", "#")]

    brief = render_brief(today, time_str, domestic, world, finance)

    if args.dry_run:
        print(brief)
        return 0

    outdir = os.path.abspath(args.outdir)
    os.makedirs(outdir, exist_ok=True)
    out_path = os.path.join(outdir, f"daily_news_{today}.md")
    with open(out_path, "w", encoding="utf-8") as fh:
        fh.write(brief)
    print(f"[ok] wrote {out_path}", file=sys.stderr)
    print(out_path)
    return 0


if __name__ == "__main__":
    sys.exit(main())