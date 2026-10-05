#!/usr/bin/env python3
"""Extract still frames from a video for on-screen-text verification.

Kept out of video_digest.py on purpose. That script's URL path pulls the
video stream through yt-dlp postprocessing, which fails here with
"unable to obtain file audio codec with ffprobe" and leaves no video to sample.
Frame sampling is also a different job from transcription: it exists to check
names the speech backend mangles (brands, sites, jargon), and it needs eyes --
either a vision endpoint or an agent that can read the images. Folding it into
the transcribe pipeline only made a silent failure look like a working feature.

Usage:
  extract_frames.py <video-or-url> --out DIR [--times 48,60,96] [--interval 25]
                    [--max 12] [--width 1280]

  # times  : second offsets, comma separated
  # interval: sample every N seconds instead of explicit times
Frames land in DIR/frames/NNN_<seconds>s.jpg
"""
from __future__ import annotations

import argparse
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path
from typing import List, Optional


def resolve_ffmpeg() -> str:
    """Use an ffmpeg/ffprobe pair that live in the same directory.

    A lone ffmpeg breaks any tool that shells out to ffprobe next door, so both
    must come from one install.
    """
    if shutil.which("ffmpeg") and shutil.which("ffprobe"):
        return shutil.which("ffmpeg")
    try:
        import imageio_ffmpeg

        return imageio_ffmpeg.get_ffmpeg_exe()
    except Exception:
        return "ffmpeg"


def fetch_video(source: str, dest_dir: Path) -> Path:
    """Download just the video stream, audio dropped. Returns the local path.

    Calls the yt-dlp CLI rather than the Python API because the API's
    postprocessor chain is what fails in this environment; the CLI downloads
    the mp4 and stops.
    """
    dest_dir.mkdir(parents=True, exist_ok=True)
    out_tmpl = str(dest_dir / "vid.%(ext)s")
    # 480p keeps a screen-recording legible while staying small; these are
    # talking-head tech videos, 1080p is wasted bandwidth.
    command = [
        "yt-dlp",
        "-f",
        "bv*[height<=480][ext=mp4]/bv*[height<=480]/b[height<=480]/b",
        "--no-playlist",
        "-o",
        out_tmpl,
        source,
    ]
    print(f"[..] yt-dlp: fetching video stream -> {dest_dir}")
    result = subprocess.run(command, capture_output=True, text=True)
    if result.returncode != 0:
        tail = (result.stderr or result.stdout or "").strip().splitlines()[-4:]
        raise SystemExit("yt-dlp failed:\n  " + "\n  ".join(tail))
    videos = sorted(dest_dir.glob("vid.*"), key=lambda p: p.stat().st_size, reverse=True)
    if not videos:
        raise SystemExit(f"yt-dlp reported success but no file appeared in {dest_dir}")
    return videos[0]


def sample_times(video_seconds: float, interval: float, cap: int) -> List[float]:
    times: List[float] = []
    step = max(1.0, interval)
    current = 0.0
    while current < video_seconds and len(times) < cap:
        times.append(round(current, 1))
        current += step
    return times


def probe_duration(video: Path) -> Optional[float]:
    if not shutil.which("ffprobe"):
        return None
    result = subprocess.run(
        [
            "ffprobe",
            "-v",
            "error",
            "-show_entries",
            "format=duration",
            "-of",
            "default=noprint_wrappers=1:nokey=1",
            str(video),
        ],
        capture_output=True,
        text=True,
    )
    try:
        return float(result.stdout.strip())
    except (TypeError, ValueError):
        return None


def extract(video: Path, times: List[float], frames_dir: Path, width: int) -> List[dict]:
    ffmpeg = resolve_ffmpeg()
    frames_dir.mkdir(parents=True, exist_ok=True)
    written: List[dict] = []
    for seconds in times:
        target = frames_dir / f"{int(round(seconds)):04d}_{seconds:g}s.jpg"
        result = subprocess.run(
            [
                ffmpeg,
                "-v",
                "error",
                "-y",
                "-ss",
                str(seconds),
                "-i",
                str(video),
                "-frames:v",
                "1",
                "-vf",
                f"scale={width}:-1",
                "-q:v",
                "2",
                str(target),
            ],
            capture_output=True,
            text=True,
        )
        if result.returncode != 0 or not target.exists():
            # Seeking past the end of a short clip is normal, not fatal.
            print(f"[..] skip {seconds}s: {(result.stderr or '').strip()[:100]}")
            continue
        written.append({"t": seconds, "path": str(target), "bytes": target.stat().st_size})
        print(f"[ok] {seconds:>7.1f}s -> {target.name} ({written[-1]['bytes'] // 1024} KB)")
    return written


def main() -> int:
    parser = argparse.ArgumentParser(description="Sample still frames from a video for text verification")
    parser.add_argument("source", help="local video path or page URL")
    parser.add_argument("--out", required=True, help="output directory; frames go to <out>/frames")
    parser.add_argument("--times", help="comma separated second offsets")
    parser.add_argument("--interval", type=float, help="sample every N seconds")
    parser.add_argument("--max", type=int, default=12, help="cap on frame count")
    parser.add_argument("--width", type=int, default=1280, help="output width in px")
    args = parser.parse_args()

    if not args.times and not args.interval:
        parser.error("pass --times 48,60,96 or --interval 25")

    out_dir = Path(args.out)
    out_dir.mkdir(parents=True, exist_ok=True)

    tmp_dir: Optional[tempfile.TemporaryDirectory] = None
    if Path(args.source).exists():
        video = Path(args.source)
    else:
        tmp_dir = tempfile.TemporaryDirectory()
        video = fetch_video(args.source, Path(tmp_dir.name))

    try:
        if args.times:
            times = [float(t) for t in args.times.split(",") if t.strip()]
        else:
            duration = probe_duration(video) or 0.0
            if duration <= 0:
                raise SystemExit("could not read video duration; use --times with explicit offsets")
            times = sample_times(duration, args.interval, args.max)

        frames = extract(video, times, out_dir / "frames", args.width)
        if not frames:
            raise SystemExit("no frames were written; check the offsets against the real duration")
        print(f"\ndone: {len(frames)} frames in {out_dir / 'frames'}")
        print("These frames have NO OCR. Read them before trusting any product, site or")
        print("person name in the transcript: speech backends routinely mangle Latin names.")
        return 0
    finally:
        if tmp_dir is not None:
            tmp_dir.cleanup()


if __name__ == "__main__":
    sys.exit(main())
