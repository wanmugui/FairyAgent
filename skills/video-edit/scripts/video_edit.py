#!/usr/bin/env python3
"""Edit a video the way the model can actually drive it: plan in, evidence out.

The thing worth copying from how ChatGPT/Claude handle video is not a magic
button, it is the *loop*:

    look  ->  decide  ->  execute  ->  look at the result  ->  iterate

The first "look" already exists in this repo: `video-summary`'s digest turns a
video into keyframes + OCR + a transcript. What was missing is the middle and
the end:

  * `edit` takes an explicit plan (EDL) and produces one file. The model writes
    the plan, this script does the cutting - no per-edit shell archaeology, and
    the plan is a file you can diff, review and reuse.
  * `frames`/`preview` give the model something it can *see* afterwards: a
    contact sheet (one image showing the whole cut), a mid-frame, and a short
    low-res clip. Without this the model is editing blind.

Everything here is ffmpeg + PIL, both already installed; VAAPI is offered for
export speed but the default is CPU libx264 because correctness beats speed.

Usage:
    video_edit.py probe   <video>
    video_edit.py frames  <video> [--count 12] [--out sheet.png] [--ocr]
    video_edit.py edit    <video> --plan plan.json --out out.mp4 [--encoder cpu|vaapi] [--dry-run]
    video_edit.py preview <video> --out DIR [--seconds 6]
    video_edit.py srt     --plan plan.json --out subs.srt   (或从 --items-json 生成)

Plan (JSON) - every key optional except `segments` when you want a cut:
    {
      "segments": [{"start": 2.5, "end": 8.0}, {"start": 12.4, "end": 20.0}],
      "speed": 1.25,                       # 0.5-2.0, audio follows (atempo)
      "crop": "vertical",                  # or {"width":1080,"height":1920}
      "scale_width": 1080,                 # 等比缩放到这个宽度
      "subtitles": {"srt": "subs.srt"},    # 或 {"items":[{"start":..,"end":..,"text":".."}]}
      "watermark": {"text": "Fairy", "position": "br"},
      "audio": {"mute": false, "replace": "bgm.mp3", "volume": 1.0},
      "export": {"crf": 20, "preset": "veryfast", "fps": null}
    }
"""

from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Optional

from PIL import Image, ImageDraw, ImageFont

VISION_SERVICE = os.environ.get("FAIRY_VISION_URL", "http://127.0.0.1:8791")
FONT_CANDIDATES = (
    "/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc",
    "/usr/share/fonts/opentype/noto/NotoSansCJK-Bold.ttc",
    "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
)


def log(message: str) -> None:
    print(message, file=sys.stderr, flush=True)


def run(args: list[str], *, capture: bool = True, check: bool = True) -> subprocess.CompletedProcess:
    result = subprocess.run(
        args,
        stdout=subprocess.PIPE if capture else None,
        stderr=subprocess.PIPE if capture else None,
        text=True,
    )
    if check and result.returncode != 0:
        tail = (result.stderr or "").strip().splitlines()[-6:]
        raise RuntimeError(f"{args[0]} 失败（退出 {result.returncode}）：\n  " + "\n  ".join(tail))
    return result


def ffmpeg() -> str:
    return os.environ.get("FAIRY_FFMPEG") or shutil.which("ffmpeg") or "ffmpeg"


def ffprobe() -> str:
    return os.environ.get("FAIRY_FFPROBE") or shutil.which("ffprobe") or "ffprobe"


def font_file() -> Optional[str]:
    for candidate in FONT_CANDIDATES:
        if Path(candidate).is_file():
            return candidate
    return None


# ---------------------------------------------------------------------------
# probe / frames  -  "look at it"
# ---------------------------------------------------------------------------


@dataclass
class VideoInfo:
    path: str
    duration: float
    width: int
    height: int
    fps: float
    video_codec: str
    audio_codec: str
    has_audio: bool
    size_mb: float

    def as_dict(self) -> dict[str, Any]:
        return {
            "path": self.path,
            "duration": round(self.duration, 3),
            "width": self.width,
            "height": self.height,
            "fps": round(self.fps, 3),
            "video_codec": self.video_codec,
            "audio_codec": self.audio_codec,
            "has_audio": self.has_audio,
            "size_mb": round(self.size_mb, 2),
            "aspect": round(self.width / self.height, 4) if self.height else 0,
        }


def probe(path: Path) -> VideoInfo:
    if not path.is_file():
        raise FileNotFoundError(f"文件不存在: {path}")
    raw = run([
        ffprobe(), "-v", "error", "-print_format", "json",
        "-show_format", "-show_streams", str(path),
    ]).stdout
    data = json.loads(raw or "{}")
    streams = data.get("streams") or []
    video = next((s for s in streams if s.get("codec_type") == "video"), {})
    audio = next((s for s in streams if s.get("codec_type") == "audio"), {})
    duration = float((data.get("format") or {}).get("duration") or video.get("duration") or 0.0)
    fps = 0.0
    for key in ("avg_frame_rate", "r_frame_rate"):
        value = str(video.get(key) or "")
        if "/" in value:
            num, _, den = value.partition("/")
            try:
                fps = float(num) / float(den) if float(den) else 0.0
            except ValueError:
                fps = 0.0
        elif value:
            try:
                fps = float(value)
            except ValueError:
                fps = 0.0
        if fps:
            break
    return VideoInfo(
        path=str(path),
        duration=duration,
        width=int(video.get("width") or 0),
        height=int(video.get("height") or 0),
        fps=fps,
        video_codec=str(video.get("codec_name") or ""),
        audio_codec=str(audio.get("codec_name") or ""),
        has_audio=bool(audio),
        size_mb=path.stat().st_size / 1024 / 1024,
    )


def sample_times(duration: float, count: int) -> list[float]:
    """Even sample points, kept off the very first/last frame."""
    if count <= 0:
        return []
    if duration <= 0:
        return [0.0]
    span = max(duration - 0.10, 0.0)
    if count == 1:
        return [span / 2]
    return [round(span * i / (count - 1) + 0.05, 3) for i in range(count)]


def grab_frame(video: Path, at: float, out: Path) -> bool:
    out.parent.mkdir(parents=True, exist_ok=True)
    result = run([
        ffmpeg(), "-hide_banner", "-loglevel", "error", "-y",
        "-ss", f"{at:.3f}", "-i", str(video), "-frames:v", "1", str(out),
    ], check=False)
    return result.returncode == 0 and out.is_file() and out.stat().st_size > 0


def ocr_frame(image_path: Path) -> str:
    """Read on-screen text through the same vision service image_vqa uses."""
    try:
        import urllib.request

        payload = json.dumps({"image_path": str(image_path), "mode": "local"}).encode()
        request = urllib.request.Request(
            f"{VISION_SERVICE}/vqa", data=payload,
            headers={"Content-Type": "application/json"},
        )
        with urllib.request.urlopen(request, timeout=45) as response:
            body = json.loads(response.read().decode() or "{}")
        return str(body.get("text") or body.get("ocr") or "").strip()
    except Exception as error:  # noqa: BLE001 - OCR 是加分项，不是必需品
        log(f"[frames] OCR 跳过（{VISION_SERVICE} 不可用：{type(error).__name__}）")
        return ""


def contact_sheet(entries: list[tuple[float, Path]], out: Path, columns: int = 3) -> Path:
    """One image showing the whole video - what the model looks at instead of frames."""
    tiles: list[tuple[float, Image.Image]] = []
    for at, path in entries:
        try:
            image = Image.open(path).convert("RGB")
        except Exception:  # noqa: BLE001
            continue
        image.thumbnail((420, 420))
        tiles.append((at, image))
    if not tiles:
        raise RuntimeError("没有取到任何帧，无法生成缩略图")

    width, height = tiles[0][1].size
    rows = (len(tiles) + columns - 1) // columns
    label_height = 26
    sheet = Image.new("RGB", (columns * width, rows * (height + label_height)), (18, 20, 26))
    draw = ImageDraw.Draw(sheet)
    font = ImageFont.truetype(font_file(), 16) if font_file() else ImageFont.load_default()
    for index, (at, image) in enumerate(tiles):
        x = (index % columns) * width
        y = (index // columns) * (height + label_height)
        sheet.paste(image, (x, y))
        draw.text((x + 6, y + height + 4), f"{at:6.2f}s", fill=(200, 210, 225), font=font)
    sheet.save(out)
    return out


# ---------------------------------------------------------------------------
# edit  -  "do something about it"
# ---------------------------------------------------------------------------


def escape_filter_text(text: str) -> str:
    """drawtext 的文本要先躲开它自己的分隔符。"""
    escaped = str(text).replace("\\", "\\\\").replace("'", "\\'")
    return escaped.replace(":", "\\:").replace("%", "\\%")


def build_subtitles(plan: dict, work: Path) -> Optional[Path]:
    """把 plan 里的字幕写成 .srt（libass 烧录最稳，中文也不会变方框）。"""
    subtitles = plan.get("subtitles") or {}
    if not subtitles:
        return None
    if subtitles.get("srt"):
        path = Path(subtitles["srt"]).expanduser()
        if not path.is_file():
            raise FileNotFoundError(f"字幕文件不存在: {path}")
        return path
    items = subtitles.get("items") or []
    if not items:
        return None
    lines = []
    for index, item in enumerate(items, start=1):
        start = float(item.get("start") or 0)
        end = float(item.get("end") or start + 2)
        lines.append(f"{index}\n{_srt_time(start)} --> {_srt_time(end)}\n{item.get('text', '').strip()}\n")
    path = work / "subtitles.srt"
    path.write_text("\n".join(lines), encoding="utf-8")
    return path


def _srt_time(seconds: float) -> str:
    milliseconds = int(round(seconds * 1000))
    hours, rest = divmod(milliseconds, 3600_000)
    minutes, rest = divmod(rest, 60_000)
    secs, millis = divmod(rest, 1000)
    return f"{hours:02d}:{minutes:02d}:{secs:02d},{millis:03d}"


def build_filter_graph(plan: dict, info: VideoInfo, subtitle_file: Optional[Path], work: Path) -> str:
    """一个 filter_complex 干完：切段 -> 拼接 -> 变速 -> 画面处理 -> 字幕/水印。"""
    segments = plan.get("segments") or []
    if not segments:
        segments = [{"start": 0.0, "end": info.duration}]

    parts: list[str] = []
    video_labels: list[str] = []
    audio_labels: list[str] = []
    for index, segment in enumerate(segments):
        start = float(segment.get("start") or 0.0)
        end = float(segment.get("end") or info.duration)
        if end <= start:
            raise ValueError(f"segment {index} 的 end <= start：{segment}")
        parts.append(
            f"[0:v]trim=start={start:.3f}:end={end:.3f},setpts=PTS-STARTPTS[v{index}]"
        )
        video_labels.append(f"[v{index}]")
        if info.has_audio:
            parts.append(
                f"[0:a]atrim=start={start:.3f}:end={end:.3f},asetpts=PTS-STARTPTS[a{index}]"
            )
            audio_labels.append(f"[a{index}]")

    if len(segments) > 1:
        joined = "".join(f"{v}{a}" for v, a in zip(video_labels, audio_labels)) if info.has_audio \
            else "".join(video_labels)
        n = len(segments)
        parts.append(f"{joined}concat=n={n}:v=1:a={1 if info.has_audio else 0}[vcat]" + ("[acat]" if info.has_audio else ""))
        current_video, current_audio = "[vcat]", ("[acat]" if info.has_audio else None)
    else:
        current_video, current_audio = video_labels[0], (audio_labels[0] if info.has_audio else None)

    speed = float(plan.get("speed") or 1.0)
    if abs(speed - 1.0) > 0.001:
        if not 0.5 <= speed <= 2.0:
            raise ValueError("speed 只支持 0.5–2.0（要更快的效果就分段做）")
        parts.append(f"{current_video}setpts=PTS/{speed}[vspeed]")
        current_video = "[vspeed]"
        if current_audio:
            parts.append(f"{current_audio}atempo={speed}[aspeed]")
            current_audio = "[aspeed]"

    crop = plan.get("crop")
    if crop == "vertical":
        parts.append(f"{current_video}crop=ih*9/16:ih,scale=1080:1920:flags=lanczos[vfmt]")
        current_video = "[vfmt]"
    elif isinstance(crop, dict) and crop.get("width") and crop.get("height"):
        parts.append(f"{current_video}scale={int(crop['width'])}:{int(crop['height'])}:force_original_aspect_ratio=increase,"
                     f"crop={int(crop['width'])}:{int(crop['height'])}[vfmt]")
        current_video = "[vfmt]"
    elif plan.get("scale_width"):
        width = int(plan["scale_width"])
        parts.append(f"{current_video}scale={width}:-2:flags=lanczos[vfmt]")
        current_video = "[vfmt]"

    font = font_file()
    watermark = plan.get("watermark") or {}
    if watermark.get("text"):
        size = int(watermark.get("font_size") or 28)
        position = str(watermark.get("position") or "br")
        margin = int(watermark.get("margin") or 24)
        x = {"br": f"w-text_w-{margin}", "bl": f"{margin}", "tr": f"w-text_w-{margin}", "tl": f"{margin}"}.get(position, f"w-text_w-{margin}")
        y = {"br": f"h-text_h-{margin}", "bl": f"h-text_h-{margin}", "tr": f"{margin}", "tl": f"{margin}"}.get(position, f"h-text_h-{margin}")
        alpha = float(watermark.get("opacity") or 0.7)
        drawtext = (
            f"drawtext=text='{escape_filter_text(watermark['text'])}'"
            f":x={x}:y={y}:fontsize={size}:fontcolor=white@{alpha}:box=1:boxcolor=black@0.25:boxborderw=8"
        )
        if font:
            drawtext += f":fontfile={font}"
        parts.append(f"{current_video}{drawtext}[vmark]")
        current_video = "[vmark]"

    if subtitle_file:
        escaped = str(subtitle_file).replace("\\", "/").replace(":", "\\:").replace("'", "\\'")
        style = plan.get("subtitles", {}).get("force_style") or "FontName=Noto Sans CJK SC,FontSize=22,Outline=1,Shadow=0,MarginV=40"
        parts.append(f"{current_video}subtitles='{escaped}':force_style='{style}'[vsub]")
        current_video = "[vsub]"

    audio_plan = plan.get("audio") or {}
    if current_audio and audio_plan.get("volume"):
        parts.append(f"{current_audio}volume={float(audio_plan['volume'])}[avol]")
        current_audio = "[avol]"
    if audio_plan.get("mute"):
        current_audio = None

    parts.append(f"{current_video}null[outv]")
    if current_audio:
        parts.append(f"{current_audio}anull[outa]")
    return ";".join(parts)


def edit(video: Path, plan: dict, out: Path, encoder: str, dry_run: bool = False) -> dict[str, Any]:
    info = probe(video)
    out.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="video-edit-") as tmp:
        work = Path(tmp)
        subtitle_file = build_subtitles(plan, work)
        graph = build_filter_graph(plan, info, subtitle_file, work)
        export = plan.get("export") or {}
        crf = int(export.get("crf") or 20)
        preset = str(export.get("preset") or "veryfast")

        command = [ffmpeg(), "-hide_banner", "-loglevel", "error", "-stats", "-y", "-i", str(video)]
        audio_plan = plan.get("audio") or {}
        replaced_audio = audio_plan.get("replace")
        if replaced_audio:
            if not Path(replaced_audio).expanduser().is_file():
                raise FileNotFoundError(f"替换音轨不存在: {replaced_audio}")
            command += ["-i", str(Path(replaced_audio).expanduser())]
            graph = graph.replace("[outa]", "[olda]")
            graph += ";[1:a]anull[outa]"
        command += ["-filter_complex", graph, "-map", "[outv]"]
        if "[outa]" in graph:
            command += ["-map", "[outa]", "-c:a", "aac", "-b:a", "160k", "-shortest"]
        else:
            command += ["-an"]

        if encoder == "vaapi":
            device = os.environ.get("FAIRY_VAAPI_DEVICE", "/dev/dri/renderD128")
            command[:1] = command[:1]  # keep ffmpeg first
            command = [ffmpeg(), "-hide_banner", "-loglevel", "error", "-stats", "-y",
                       "-vaapi_device", device, "-i", str(video)]
            if replaced_audio:
                command += ["-i", str(Path(replaced_audio).expanduser())]
            graph_vaapi = graph.replace("[outv]", "[outv0]") + ";[outv0]format=nv12,hwupload[outv]"
            command += ["-filter_complex", graph_vaapi, "-map", "[outv]"]
            if "[outa]" in graph_vaapi:
                command += ["-map", "[outa]", "-c:a", "aac", "-b:a", "160k", "-shortest"]
            else:
                command += ["-an"]
            command += ["-c:v", "h264_vaapi", "-qp", str(crf), "-movflags", "+faststart", str(out)]
        else:
            command += ["-c:v", "libx264", "-preset", preset, "-crf", str(crf),
                        "-pix_fmt", "yuv420p", "-movflags", "+faststart", str(out)]

        if export.get("fps"):
            command.insert(command.index("-c:v"), "-r")
            command.insert(command.index("-r") + 1, str(export["fps"]))

        log("[edit] " + " ".join(command))
        if dry_run:
            return {"dry_run": True, "command": command, "filter": graph}
        run(command, capture=True)
        if not out.is_file() or out.stat().st_size == 0:
            raise RuntimeError("导出完成但文件是空的")

    return {"out": str(out), "result": probe(out).as_dict(), "source": info.as_dict()}


# ---------------------------------------------------------------------------
# preview  -  "look at what you made"
# ---------------------------------------------------------------------------


def preview(video: Path, out_dir: Path, seconds: float = 6.0, count: int = 9) -> dict[str, Any]:
    out_dir.mkdir(parents=True, exist_ok=True)
    info = probe(video)
    clip = out_dir / "preview.mp4"
    run([
        ffmpeg(), "-hide_banner", "-loglevel", "error", "-y",
        "-t", f"{min(seconds, info.duration):.2f}", "-i", str(video),
        "-vf", "scale=480:-2", "-c:v", "libx264", "-preset", "ultrafast", "-crf", "30",
        "-c:a", "aac", "-b:a", "96k", "-movflags", "+faststart", str(clip),
    ], check=False)

    times = sample_times(info.duration, count)
    entries: list[tuple[float, Path]] = []
    with tempfile.TemporaryDirectory(prefix="video-frames-") as tmp:
        for index, at in enumerate(times):
            frame = Path(tmp) / f"f{index:03d}.jpg"
            if grab_frame(video, at, frame):
                entries.append((at, frame))
        sheet = contact_sheet(entries, out_dir / "contact-sheet.jpg")
        middle = min(entries, key=lambda item: abs(item[0] - info.duration / 2))[1] if entries else None
        middle_out = out_dir / "middle-frame.jpg"
        if middle:
            shutil.copyfile(middle, middle_out)

    return {
        "clip": str(clip) if clip.is_file() else "",
        "contact_sheet": str(sheet),
        "middle_frame": str(middle_out) if middle_out.is_file() else "",
        "video": info.as_dict(),
    }


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------


def main(argv: Optional[list[str]] = None) -> int:
    parser = argparse.ArgumentParser(description="用计划文件剪辑视频，并产出可看的预览")
    sub = parser.add_subparsers(dest="command", required=True)

    p_probe = sub.add_parser("probe", help="读一遍元信息（时长/分辨率/编码/有没有音轨）")
    p_probe.add_argument("video")
    p_probe.add_argument("--json", action="store_true")

    p_frames = sub.add_parser("frames", help="抽帧并拼成一张缩略图（给模型看）")
    p_frames.add_argument("video")
    p_frames.add_argument("--count", type=int, default=12)
    p_frames.add_argument("--out", default="")
    p_frames.add_argument("--ocr", action="store_true", help="顺带用 OCR 服务读画面文字")
    p_frames.add_argument("--json", action="store_true")

    p_edit = sub.add_parser("edit", help="按计划剪辑并导出")
    p_edit.add_argument("video")
    p_edit.add_argument("--plan", required=True)
    p_edit.add_argument("--out", required=True)
    p_edit.add_argument("--encoder", choices=["cpu", "vaapi"], default="cpu")
    p_edit.add_argument("--dry-run", action="store_true")
    p_edit.add_argument("--json", action="store_true")

    p_preview = sub.add_parser("preview", help="生成回看用的预览（短片+缩略图+中间帧）")
    p_preview.add_argument("video")
    p_preview.add_argument("--out", required=True)
    p_preview.add_argument("--seconds", type=float, default=6.0)
    p_preview.add_argument("--count", type=int, default=9)
    p_preview.add_argument("--json", action="store_true")

    args = parser.parse_args(argv)

    if args.command == "probe":
        info = probe(Path(args.video))
        print(json.dumps(info.as_dict(), ensure_ascii=False, indent=2))
        return 0

    if args.command == "frames":
        video = Path(args.video)
        info = probe(video)
        times = sample_times(info.duration, args.count)
        out = Path(args.out).expanduser() if args.out else video.with_name(video.stem + "-sheet.jpg")
        entries: list[tuple[float, Path]] = []
        texts: dict[str, str] = {}
        with tempfile.TemporaryDirectory(prefix="video-frames-") as tmp:
            for index, at in enumerate(times):
                frame = Path(tmp) / f"f{index:03d}.jpg"
                if grab_frame(video, at, frame):
                    entries.append((at, frame))
                    if args.ocr:
                        text = ocr_frame(frame)
                        if text:
                            texts[f"{at:.2f}"] = text
            sheet = contact_sheet(entries, out)
        payload = {"sheet": str(sheet), "frames": [t for t, _ in entries], "video": info.as_dict()}
        if texts:
            payload["ocr"] = texts
        print(json.dumps(payload, ensure_ascii=False, indent=2))
        return 0

    if args.command == "edit":
        plan = json.loads(Path(args.plan).expanduser().read_text(encoding="utf-8"))
        result = edit(Path(args.video), plan, Path(args.out).expanduser(), args.encoder, args.dry_run)
        print(json.dumps(result, ensure_ascii=False, indent=2))
        return 0

    if args.command == "preview":
        result = preview(Path(args.video), Path(args.out).expanduser(), args.seconds, args.count)
        print(json.dumps(result, ensure_ascii=False, indent=2))
        return 0

    parser.error("未知命令")
    return 1


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception as error:  # noqa: BLE001
        log(f"错误：{error}")
        sys.exit(1)
