#!/usr/bin/env python3
"""图 → 视频（mp4）：提交 → 轮询 → 下载落盘，走 MiniMax。

用法:
    python3 to_video.py --image out/miyabi_front.png -o out/miyabi.mp4 \
        --prompt "she walks forward, hair and coat fluttering"
    python3 to_video.py --prompt "..." -o out/text2video.mp4   # 不给 --image 就是文生视频

实测要点：
- 首帧用 base64 内联（first_frame_image），不需要公网 URL，status_code=0 可过。
- resolution 只收 512P / 768P / 1080P，传 720P 会被拒。
- HTTP 永远 200，失败看 base_resp.status_code。
"""

from __future__ import annotations

import argparse
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
import mmclient as mm


def main() -> int:
    ap = argparse.ArgumentParser(description="图生视频 / 文生视频（MiniMax）")
    ap.add_argument("--image", default=None, help="首帧图；不给则文生视频")
    ap.add_argument("--prompt", required=True, help="运动描述，越具体越稳")
    ap.add_argument("-o", "--out", required=True, help="输出 mp4 路径")
    ap.add_argument("--model", default="MiniMax-Hailuo-02")
    ap.add_argument("--duration", type=int, default=6, help="秒")
    ap.add_argument("--resolution", default="768P", choices=mm.RESOLUTIONS)
    ap.add_argument("--interval", type=int, default=15, help="轮询间隔秒，别太短")
    ap.add_argument("--max-wait", type=int, default=900)
    args = ap.parse_args()

    if args.image and not Path(args.image).is_file():
        print(f"❌ 首帧图不存在: {args.image}", flush=True)
        return 2

    key = mm.load_key()
    print(f"[video] model={args.model} {args.duration}s {args.resolution} "
          f"first_frame={'yes' if args.image else 'no(text2video)'}", flush=True)
    path = mm.generate_video(
        args.prompt,
        key=key,
        first_frame=args.image,
        out_path=args.out,
        model=args.model,
        duration=args.duration,
        resolution=args.resolution,
        interval=args.interval,
        max_wait=args.max_wait,
    )
    print(f"[video] done: {path} ({path.stat().st_size/1024/1024:.2f} MB)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
