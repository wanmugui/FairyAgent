#!/usr/bin/env python3
"""工作区清理：默认只报告，不删。

背景：workspace/result 里「中间产物」和「交付物」长在同一棵树上，交付区被撑到
17G；runs/rollback 每次 run 留一份快照、没有回收策略，堆到 2000+ 个目录；
截图类文件用完就留在原地。没有一个地方管这些，于是只能靠人肉看着乱。

刻意保守：
  * 默认 dry-run。必须显式 --apply 才动手。
  * 永远不动 mv-* 目录（主人明确要求）。
  * 永远不动任何 deliver/ 目录下的东西——那是真正要交给主人的东西。
  * 每一类都单独报「会删多少、占多少」，先看清楚再决定。

用法：
  python3 scripts/workspace-cleanup.py            # 只看清单
  python3 scripts/workspace-cleanup.py --apply    # 真的删（先跑一次 dry-run）
"""
from __future__ import annotations

import argparse
import shutil
import time
from pathlib import Path

REPO = Path(__file__).resolve().parents[1]
RESULT = REPO / "workspace" / "result"
ROLLBACK = REPO / "runs" / "rollback"
UPLOAD = REPO / "workspace" / "upload"

# 主人明确说过：MV 产物不管。
NEVER_TOUCH_PREFIX = ("mv-",)


def human(size: float) -> str:
    for unit in ("B", "KB", "MB", "GB"):
        if size < 1024 or unit == "GB":
            return f"{size:.1f}{unit}" if unit != "B" else f"{int(size)}B"
        size /= 1024
    return f"{size:.1f}GB"


def dir_size(path: Path) -> int:
    return sum(f.stat().st_size for f in path.rglob("*") if f.is_file())


def protected(path: Path) -> bool:
    """路径里出现 MV 前缀目录或 deliver 目录，一律不碰。"""
    for part in path.parts:
        if part.startswith(NEVER_TOUCH_PREFIX) or part == "deliver":
            return True
    return False


def rollback_plan(keep_days: int) -> tuple[list[Path], int, int, int]:
    """按天保留快照。返回 (待删目录, 目录大小合计, 留下的数量, 留下的数量)。"""
    if not ROLLBACK.is_dir():
        return [], 0, 0, 0
    cutoff = time.time() - keep_days * 86400
    stale, kept, freed = [], 0, 0
    for child in sorted(ROLLBACK.iterdir()):
        if not child.is_dir():
            continue
        try:
            mtime = child.stat().st_mtime
        except OSError:
            continue
        if mtime < cutoff:
            stale.append(child)
            freed += dir_size(child)
        else:
            kept += 1
    return stale, freed, kept, len(stale)


def screenshot_plan(days: int) -> tuple[list[Path], int, int]:
    """result 根目录下的过程图。默认只挑 _ 前缀的——那种几乎都是中间产物。

    默认 7 天而不是 30：实测这批图全在 14 天内，30 天的阈值一张都咬不到，
    写 30 等于给了一个「我设了保留期」的错觉，实际什么都没做。
    """
    if not RESULT.is_dir():
        return [], 0, 0
    cutoff = time.time() - days * 86400
    victims, freed, skipped = [], 0, 0
    for child in RESULT.glob("*.png"):
        if protected(child):
            continue
        try:
            if child.stat().st_mtime >= cutoff:
                skipped += 1
                continue
        except OSError:
            continue
        # _ 前缀是过程图；没有前缀的先列出来不删，等人确认
        if child.name.startswith("_"):
            victims.append(child)
            freed += child.stat().st_size
        else:
            skipped += 1
    return victims, freed, skipped


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--apply", action="store_true", help="真的删（默认只报告）")
    parser.add_argument("--rollback-keep-days", type=int, default=7)
    parser.add_argument("--screenshot-keep-days", type=int, default=7)
    args = parser.parse_args()

    print("=" * 62)
    print("  工作区清理清单" + ("（--apply：会真删）" if args.apply else "（dry-run，不会删任何东西）"))
    print("=" * 62)

    stale, freed, kept, n = rollback_plan(args.rollback_keep_days)
    print(f"\n[1] runs/rollback 快照")
    print(f"    共 {n + kept} 个目录 → 保留最近 {args.rollback_keep_days} 天（{kept} 个），删除 {n} 个")
    print(f"    可释放: {human(freed)}")

    shots, shot_freed, shot_skip = screenshot_plan(args.screenshot_keep_days)
    print(f"\n[2] workspace/result 根目录的过程图（_ 前缀，超过 {args.screenshot_keep_days} 天）")
    print(f"    可删 {len(shots)} 张，可释放 {human(shot_freed)}")
    print(f"    另有 {shot_skip} 张无前缀的**不自动删**——它们可能是你真要留的图")

    if UPLOAD.is_dir():
        uploads = [f for f in UPLOAD.rglob("*") if f.is_file()]
        print(f"\n[3] workspace/upload（你上传的截图）")
        print(f"    {len(uploads)} 个文件，占 {human(dir_size(UPLOAD))} —— 本脚本不动它")

    print(f"\n[4] 保护项：mv-* 目录与任何 deliver/ 目录，脚本永不触碰")

    if not args.apply:
        print("\n" + "-" * 62)
        print("  这是 dry-run。加 --apply 才会真的删。")
        return 0

    print("\n" + "-" * 62)
    print("  开始删除…")
    for d in stale:
        shutil.rmtree(d, ignore_errors=True)
    print(f"  rollback: 删了 {len(stale)} 个目录")
    for f in shots:
        try:
            f.unlink()
        except OSError:
            pass
    print(f"  过程图: 删了 {len(shots)} 张")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
