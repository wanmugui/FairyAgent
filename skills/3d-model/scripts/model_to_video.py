#!/usr/bin/env python3
"""建模 → 渲染 → 剪辑 的端到端编排器。

把三块能力串成一条命令：
  1. bpy_model.py    按 JSON 建场景 → 渲出帧序列（或单图）
  2. timeline_edit.py 把帧序列/图片排进时间线 → 出成片

因为建模和剪辑本来就在两个 Python 环境里（bpy 只发 cp313 wheel，
要跑在 bpyenv/3.13；剪辑脚本跑在系统 3.10 即可），本编排器负责跨环境调度。

子命令：
  frames   bpy 建场景并渲出帧序列（PNG）
  turntable 用 bpy 渲一个物体旋转一圈的序列（做展示片最常用）
  compile  把帧序列 + 时间线 JSON 交给 timeline_edit 出片
  demo     端到端演示：渲一段 turntable → 剪成 mp4（需要 bpy 就绪）
"""
from __future__ import annotations

import argparse
import json
import os
import shlex
import subprocess
import sys
from pathlib import Path

SKILL_3D = Path(__file__).resolve().parents[1]      # skills/3d-model
VIDEO_EDIT = Path("/home/user/Fairy/skills/video-edit")
BPY_PY = "/home/user/miniforge3/envs/bpyenv/bin/python3.13"
BPY_MODEL = SKILL_3D / "scripts" / "bpy_model.py"
TL_EDIT = VIDEO_EDIT / "scripts" / "timeline_edit.py"
TIMELINE_PY = sys.executable  # timeline_edit 跑在当前解释器


def run(cmd: list[str], **kw) -> int:
    print("$ " + " ".join(shlex.quote(c) for c in cmd), flush=True)
    return subprocess.call(cmd, **kw)


def have_bpy() -> bool:
    if not Path(BPY_PY).exists():
        return False
    r = subprocess.run([BPY_PY, "-c", "import bpy"],
                       capture_output=True)
    return r.returncode == 0


def require_bpy() -> None:
    if not have_bpy():
        print(
            "bpy 尚不可用。请先在 Python 3.13 环境安装：\n"
            f"  /home/user/miniforge3/bin/conda create -n bpyenv python=3.13 -y\n"
            f"  {BPY_PY} -m pip install bpy",
            file=sys.stderr,
        )
        sys.exit(2)


def cmd_frames(args) -> int:
    require_bpy()
    # bpy_model render --animation 渲 .blend 的整个帧范围
    return run([BPY_PY, str(BPY_MODEL), "scene", args.scene_json,
                "--blend", args.blend])


def cmd_turntable(args) -> int:
    require_bpy()
    tmp = Path(args.out_dir).resolve()
    tmp.mkdir(parents=True, exist_ok=True)
    scene = {
        "objects": [
            {"type": args.type, "name": "Subject", "scale": [1, 1, 1],
             "material": "mat", "color": [0.9, 0.5, 0.2, 1.0],
             "metallic": 0.4, "roughness": 0.35}
        ],
        "spin": {"axis": "Z", "degrees": 360},   # 整圈旋转
        "render": {"width": args.width, "height": args.height,
                   "fps": args.fps, "engine": args.engine,
                   "frame_start": 1, "frame_end": args.frames},
    }
    sj = tmp / "turntable_scene.json"
    sj.write_text(json.dumps(scene, ensure_ascii=False, indent=2), encoding="utf-8")
    blend = tmp / "turntable.blend"
    rc = run([BPY_PY, str(BPY_MODEL), "scene", str(sj), "--blend", str(blend)])
    if rc != 0:
        return rc
    # 渲序列
    rc = run([BPY_PY, str(BPY_MODEL), "render", str(blend),
              "--out", str(tmp / "frame_"), "--animation"])
    return rc


def cmd_compile(args) -> int:
    return run([TIMELINE_PY, str(TL_EDIT), "build", args.timeline, args.out])


def cmd_demo(args) -> int:
    require_bpy()
    out_dir = Path(args.out_dir).resolve()
    out_dir.mkdir(parents=True, exist_ok=True)

    print("步骤 1/4：用 bpy 建场景并渲旋转帧序列", flush=True)
    if run([TIMELINE_PY, __file__, "turntable", "--type", args.type,
            "--out-dir", str(out_dir / "frames"),
            "--width", str(args.width), "--height", str(args.height),
            "--frames", str(args.frames), "--fps", str(args.fps)]) != 0:
        print("turntable 渲染失败", file=sys.stderr)
        return 1

    print("步骤 2/4：渲帧序列", flush=True)
    seqs = sorted((out_dir / "frames").glob("frame_*.png"))
    if not seqs:
        print("没有渲出帧序列", file=sys.stderr)
        return 1

    print("步骤 3/4：帧序列 → mp4（ffmpeg）", flush=True)
    clip = out_dir / "turntable.mp4"
    rc = run(["ffmpeg", "-y", "-framerate", str(args.fps),
              "-i", str(out_dir / "frames" / "frame_%04d.png"),
              "-c:v", "libx264", "-pix_fmt", "yuv420p", str(clip)])
    if rc != 0:
        return rc

    print("步骤 4/4：进时间线层，叠文字+字幕，出成片", flush=True)
    total = len(seqs) / float(args.fps)
    tl = {
        "clips": [{"path": str(clip), "in": 0.0, "out": total,
                   "transition": "fade", "transition_duration": 0.4}],
        "canvas": {"width": args.width, "height": args.height},
        "fps": args.fps,
        "overlays": [{"type": "text", "text": args.title,
                      "start": 0.3, "end": max(1.0, total - 0.3),
                      "position": "bl", "size": max(28, args.width // 22),
                      "color": "white"}],
        "subtitles": {"items": [{"start": 0.2, "end": min(total, 4.0),
                                 "text": args.subtitle or "bpy 建模 → 时间线剪辑"}]},
        "export": {"crf": 20, "preset": "veryfast", "encoder": "libx264"},
    }
    tl_path = out_dir / "demo_timeline.json"
    tl_path.write_text(json.dumps(tl, ensure_ascii=False, indent=2), encoding="utf-8")
    rc = run([TIMELINE_PY, str(TL_EDIT), "build", str(tl_path),
              str(out_dir / "demo.mp4")])
    if rc == 0:
        print(f"\n完成: {out_dir/'demo.mp4'}")
    return rc


def main() -> int:
    ap = argparse.ArgumentParser(description="建模→渲染→剪辑 编排器")
    sub = ap.add_subparsers(dest="cmd", required=True)

    f = sub.add_parser("frames", help="bpy 建场景渲帧")
    f.add_argument("scene_json"); f.add_argument("--blend", required=True)
    f.set_defaults(func=cmd_frames)

    t = sub.add_parser("turntable", help="渲物体旋转一圈的序列")
    t.add_argument("--type", default="torus")
    t.add_argument("--out-dir", default="/tmp/3d_turntable")
    t.add_argument("--width", type=int, default=960)
    t.add_argument("--height", type=int, default=720)
    t.add_argument("--frames", type=int, default=60)
    t.add_argument("--fps", type=int, default=30)
    t.add_argument("--engine", default="BLENDER_EEVEE")
    t.set_defaults(func=cmd_turntable)

    c = sub.add_parser("compile", help="时间线 JSON → 成片")
    c.add_argument("timeline"); c.add_argument("out")
    c.set_defaults(func=cmd_compile)

    d = sub.add_parser("demo", help="端到端演示（需 bpy 就绪）")
    d.add_argument("--out-dir", default="/tmp/3d_demo")
    d.add_argument("--type", default="torus")
    d.add_argument("--title", default="bpy 建模演示")
    d.add_argument("--subtitle", default="")
    d.add_argument("--width", type=int, default=960)
    d.add_argument("--height", type=int, default=720)
    d.add_argument("--frames", type=int, default=60)
    d.add_argument("--fps", type=int, default=30)
    d.set_defaults(func=cmd_demo)

    args = ap.parse_args()
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
