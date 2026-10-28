#!/usr/bin/env python3
"""时间线剪辑层：多素材 + 转场 + 多轨图层 + 背景音乐 + 字幕。

video_edit.py 的超集入口。video_edit.py 面向"单个视频按 plan 剪"，
本脚本面向"一条时间线上多个素材拼成片"，二者共用 probe/run/ffmpeg/字幕助手。

设计：主人给一份 timeline.json，直接出 mp4。子命令：
  probe   列出各素材时长/参数，便于写时间线
  build   按 timeline.json 出片（默认）
  preview 出片后生成三样回看物：contact-sheet / middle-frame / 缩略
  dump    打印将要执行的 ffmpeg 滤镜图，排查用

时间线 JSON 结构见本文件 TIMELINE_SCHEMA。
"""
from __future__ import annotations

import argparse
import json
import math
import shlex
import sys
from pathlib import Path

# 复用 video_edit 的稳定助手，不复制实现
try:
    from video_edit import (  # type: ignore
        build_subtitles, escape_filter_text, ffmpeg, ffprobe, log, probe, run,
    )
except Exception:  # pragma: no cover
    sys.path.insert(0, str(Path(__file__).resolve().parent))
    from video_edit import (
        build_subtitles, escape_filter_text, ffmpeg, ffprobe, log, probe, run,
    )

FONT = "/usr/share/fonts/truetype/wqy/wqy-microhei.ttc"
DEFAULT_W, DEFAULT_H = 1920, 1080

TIMELINE_SCHEMA = """
{
  "clips": [                     // 主轨：按顺序拼接
    {"path": "a.mp4", "in": 0.0, "out": 5.0, "speed": 1.0, "transition": "fade"},
    {"path": "b.mp4", "in": 2.0, "out": 8.0, "speed": 1.25}   // transition 可省
  ],
  "canvas": {"width": 1920, "height": 1080},   // 可省
  "fps": 30,                                     // 可省
  "audio": {                                      // 可省
    "mute": false,
    "music": {"path": "bgm.mp3", "volume": 0.25, "loop": true},  // loop 可省
    "volume": 1.0
  },
  "overlays": [                                  // 可省：上层轨道，按 start 叠
    {"type": "image", "path": "logo.png", "start": 0.5, "end": 3.0,
     "position": "tr", "scale": 0.15},
    {"type": "text", "text": "标题", "start": 1.0, "end": 4.0,
     "position": "bl", "size": 64, "color": "white"}
  ],
  "subtitles": {"items": [                       // 可省：srt 路径或 items
    {"start": 0.5, "end": 3.0, "text": "第一句"}
  ]},
  "export": {"crf": 20, "preset": "veryfast", "encoder": "libx264"}  // 可省
}
clip.transition: fade|fadeblack|fadewhite|wipeleft|wiperight|slideleft|slideup|circleopen|dissolve
"""


def _pos_expr(pos: str, w: int, h: int) -> str:
    """position 关键字 -> overlay x/y 表达式。"""
    table = {
        "tl": ("0", "0"), "t": (f"(W-w)/2", "0"), "tr": (f"W-w", "0"),
        "l": ("0", f"(H-h)/2"), "c": (f"(W-w)/2", f"(H-h)/2"), "r": (f"W-w", f"(H-h)/2"),
        "bl": ("0", "H-h"), "b": (f"(W-w)/2", "H-h"), "br": (f"W-w", "H-h"),
    }
    if pos not in table:
        raise SystemExit(f"未知 position: {pos}（可选 {sorted(table)}）")
    x, y = table[pos]
    return x, y


def build_timeline(tl: dict, work: Path, dry: bool = False) -> list[str]:
    """构造 ffmpeg 参数列表。多输入 concat/xfade，overlay 图层，混音。"""
    clips = tl.get("clips") or []
    if not clips:
        raise SystemExit("时间线里没有 clips")
    canvas = tl.get("canvas") or {}
    W = int(canvas.get("width") or DEFAULT_W)
    H = int(canvas.get("height") or DEFAULT_H)
    fps = int(tl.get("fps") or 30)
    audio_cfg = tl.get("audio") or {}
    export = tl.get("export") or {}

    cmd = [ffmpeg(), "-y"]
    # ---- 输入：每个 clip 一个；音乐若开 loop 再加一个 ----
    music = audio_cfg.get("music")
    inputs: list[str] = []
    for c in clips:
        p = str(Path(c["path"]).expanduser())
        inputs.append(p)
        cmd += ["-i", p]
    music_idx = None
    if music:
        music_idx = len(inputs)
        inputs.append(str(Path(music["path"]).expanduser()))
        cmd += ["-stream_loop", "-1" if music.get("loop", True) else "0", "-i", inputs[-1]]

    # ---- 每个 clip 预处理：trim + 变速 + 统一画布 ----
    parts: list[str] = []
    vid_labels, aud_labels = [], []
    dur_info: list[float] = []
    for i, c in enumerate(clips):
        c_in = float(c.get("in", 0.0))
        c_out = float(c["out"]) if c.get("out") is not None else None
        speed = float(c.get("speed", 1.0))
        info = probe(Path(inputs[i]))
        total = float(info.duration)
        if c_out is None:
            c_out = total
        c_out = min(c_out, total)
        if c_out <= c_in:
            raise SystemExit(f"clip {i} 的 out({c_out}) <= in({c_in})")
        raw = c_out - c_in
        dur_info.append(raw / speed if speed > 0 else raw)

        v, a = f"{i}v", f"{i}a"
        # 视频：裁剪 → 变速 → 缩放居中(补背景) → 统一帧率/像素格式
        # 注意：变速片段要合并 setpts（先减起点再除速度），不能两个 setpts 串联
        if speed != 1:
            vset = f"setpts=(PTS-STARTPTS)/{speed}"
        else:
            vset = "setpts=(PTS-STARTPTS)"
        parts.append(
            f"[{i}:v]trim=start={c_in}:end={c_out},{vset},"
            f"scale={W}:{H}:force_original_aspect_ratio=decrease,"
            f"pad={W}:{H}:(ow-iw)/2:(oh-ih)/2:color=black,"
            f"fps={fps},format=yuv420p,settb=AVTB[{v}]"
        )
        # 音频：裁剪 → 变速(atempo 0.5-2) → 静音(若 mute)
        # 关键：素材可能根本没有音轨（如由 PNG 序列合成的视频），
        # 此时硬引用 [i:a] 会让 ffmpeg 报 "matches no streams" 而整体失败，
        # 必须用 anullsrc 补一条等长静音，保证音频链始终存在。
        ap = f"[{i}:a]atrim=start={c_in}:end={c_out},asetpts=PTS-STARTPTS"
        if speed != 1:
            chain = []
            s = speed
            while s > 2:
                chain.append("atempo=2.0"); s /= 2
            while s < 0.5:
                chain.append("atempo=0.5"); s /= 0.5
            if abs(s - 1) > 1e-3:
                chain.append(f"atempo={s:.4f}")
            ap += "," + ",".join(chain)
        if audio_cfg.get("mute"):
            ap += ",volume=0"
        ap += f"[{a}]"
        if info.has_audio:
            parts.append(ap)
        else:
            silent = dur_info[-1]
            parts.append(
                f"anullsrc=r=48000:cl=stereo:d={silent:.3f}[{a}]"
            )
        vid_labels.append(v)
        aud_labels.append(a)

    # ---- 拼接：单个 clip 直接用；多个按 transition 链式 xfade ----
    cur_v, cur_a = vid_labels[0], aud_labels[0]
    acc = dur_info[0]
    for i in range(1, len(clips)):
        tr = clips[i].get("transition", clips[i - 1].get("transition", "fade"))
        # 时长要与 transition 同样的兜底顺序，否则写在上一段的时长会被静默忽略
        tdur = float(
            clips[i].get(
                "transition_duration", clips[i - 1].get("transition_duration", 0.5)
            )
        )
        tdur = max(0.0, min(tdur, acc, dur_info[i]))
        nv, na = f"x{i}v", f"x{i}a"
        if tdur > 0:
            off = max(0.0, acc - tdur)
            parts.append(
                f"[{cur_v}][{vid_labels[i]}]xfade=transition={tr}:"
                f"duration={tdur}:offset={off:.3f}[{nv}]"
            )
            # 音频交叉淡化
            parts.append(f"[{cur_a}][{aud_labels[i]}]acrossfade=d={tdur:.3f}[{na}]")
            acc = acc + dur_info[i] - tdur
        else:
            parts.append(f"[{cur_v}][{vid_labels[i]}]concat=n=2:v=1:a=0[{nv}]")
            parts.append(f"[{cur_a}][{aud_labels[i]}]concat=n=2:v=0:a=1[{na}]")
            acc += dur_info[i]
        cur_v, cur_a = nv, na

    # ---- 背景音乐：amix 到主音频 ----
    mix = "0:a"
    if music and not audio_cfg.get("mute"):
        mv = float(music.get("volume", 0.25))
        parts.append(
            f"[{music_idx}:a]volume={mv},atrim=0:{acc:.3f},"
            f"asetpts=PTS-STARTPTS[aout]"
        )
        parts.append(f"[{cur_a}]volume={float(audio_cfg.get('volume', 1.0))}[main_a]")
        parts.append(f"[main_a][aout]amix=inputs=2:duration=first:dropout_transition=0[cur_a2]")
        cur_a = "cur_a2"

    # ---- 字幕（ASS 烧录）----
    cur_v2 = cur_v
    subs = tl.get("subtitles")
    if subs:
        srt = build_subtitles(tl, work)   # build_subtitles 吃整个 plan，自己取 plan["subtitles"]
        parts.append(f"[{cur_v}]subtitles='{escape_filter_text(str(srt))}'[vs]")
        cur_v2 = "vs"

    # ---- overlay 图层：按 start 依次叠在主视频上 ----
    ov_idx = 0
    for ov in tl.get("overlays") or []:
        st = float(ov.get("start", 0))
        en = float(ov.get("end", acc))
        dur = max(0.0, en - st)
        if dur <= 0:
            continue
        pos = ov.get("position", "tr")
        x, y = _pos_expr(pos, W, H)
        nexto = f"ov{ov_idx}"
        if ov.get("type") == "image":
            img = str(Path(ov["path"]).expanduser())
            sc = float(ov.get("scale", 0.2))
            iw = max(2, int(W * sc))
            parts.append(
                f"[{ov_idx + 1}:v]scale={iw}:-1,format=rgba[img{ov_idx}]"
            )
            parts.append(
                f"[{cur_v2}][img{ov_idx}]overlay=x={x}:y={y}:"
                f"enable='between(t,{st},{en})'[{nexto}]"
            )
        elif ov.get("type") == "text":
            txt = escape_filter_text(ov.get("text", ""))
            size = int(ov.get("size", 64))
            col = ov.get("color", "white")
            parts.append(
                f"[{cur_v2}]drawtext=fontfile='{FONT}':text='{txt}':"
                f"fontsize={size}:fontcolor={col}:x={x}:y={y}:"
                f"enable='between(t,{st},{en})'[{nexto}]"
            )
        cur_v2 = nexto
        ov_idx += 1

    # ---- 输出 ----
    enc = export.get("encoder", "libx264")
    cmd += ["-filter_complex", ";".join(parts), "-map", f"[{cur_v2}]", "-map", f"[{cur_a}]"]
    crf = int(export.get("crf", 20))
    preset = export.get("preset", "veryfast")
    if enc == "libx264":
        cmd += ["-c:v", "libx264", "-crf", str(crf), "-preset", preset]
    else:
        cmd += ["-c:v", enc]
    cmd += ["-c:a", "aac", "-b:a", "192k", "-movflags", "+faststart", "-shortest"]
    return cmd, acc


def cmd_build(args) -> int:
    tl = json.loads(Path(args.timeline).read_text(encoding="utf-8"))
    work = Path(args.out).resolve().parent
    work.mkdir(parents=True, exist_ok=True)
    cmd, total = build_timeline(tl, work, dry=args.dry_run)
    cmd += [args.out]
    log(f"时间线 {len(tl.get('clips', []))} 段 → 约 {total:.2f}s → {args.out}")
    if args.dump:
        print(" ".join(shlex.quote(c) for c in cmd))
    if args.dry_run:
        return 0
    run(cmd)
    return 0


def cmd_probe(args) -> int:
    tl = json.loads(Path(args.timeline).read_text(encoding="utf-8"))
    for i, c in enumerate(tl.get("clips", [])):
        info = probe(Path(c["path"]).expanduser())
        print(f"clip{i}  {Path(c['path']).name}  时长 {info.duration:.2f}s  "
              f"{info.width}x{info.height}  {info.fps}fps  "
              f"音轨 {'有' if info.has_audio else '无'}")
    return 0


def cmd_dump(args) -> int:
    tl = json.loads(Path(args.timeline).read_text(encoding="utf-8"))
    cmd, total = build_timeline(tl, Path(args.out).resolve().parent, dry=True)
    print("总时长约：", round(total, 2), "s")
    print("== ffmpeg ==")
    print(" ".join(shlex.quote(c) for c in cmd))
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description="时间线剪辑层（video_edit 的多素材超集）")
    sub = ap.add_subparsers(dest="cmd", required=True)

    b = sub.add_parser("build", help="按时间线出片")
    b.add_argument("timeline"); b.add_argument("out")
    b.add_argument("--dry-run", action="store_true"); b.add_argument("--dump", action="store_true")
    b.set_defaults(func=cmd_build)

    p = sub.add_parser("probe", help="列出各素材信息")
    p.add_argument("timeline"); p.set_defaults(func=cmd_probe)

    d = sub.add_parser("dump", help="打印滤镜图/命令，不执行")
    d.add_argument("timeline"); d.add_argument("out", nargs="?", default="/tmp/tl_out.mp4")
    d.set_defaults(func=cmd_dump)

    args = ap.parse_args()
    return args.func(args)


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception as error:  # noqa: BLE001
        log(f"错误：{error}")
        sys.exit(1)
