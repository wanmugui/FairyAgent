#!/usr/bin/env python3
"""Build a summary-ready digest of a video.

This script does **not** write the summary. Summarising is what the agent's own
model is for, and letting it read a compact digest is both cheaper and better
than adding another API call. The job here is to turn a video into the smallest
honest evidence bundle that supports a summary:

    transcript   - timestamped, from subtitles when possible, otherwise local STT
    keyframes    - a bounded set of frames, each with the on-screen text read by
                   the local OCR service (the same backend image_vqa uses)
    outline      - coarse time blocks so the model can talk about structure

Three principles, in priority order:

1. **Subtitles beat STT.** Platform captions are authored text and cost nothing
   to fetch. STT runs only when no caption track exists.
2. **Never download more than needed.** Subtitle-only videos never pull the
   video stream; audio is extracted only when STT is actually required.
3. **Degrade loudly.** Every stage reports what it used and what it skipped, so
   the agent can say "this summary is based on auto-captions" instead of
   implying it watched the video.

Usage:
    python video_digest.py <url|file> [--out DIR] [--lang zh,en] [--stt auto]
                           [--interval 30] [--max-frames 40] [--no-frames]
                           [--json-only]
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import shutil
import sys
import tempfile
import time
import urllib.error
import urllib.request
import wave
from dataclasses import asdict, dataclass, field
from pathlib import Path
from typing import Any, Dict, List, Optional, Sequence, Tuple

import numpy as np

SAMPLE_RATE = 16_000
VISION_SERVICE = os.environ.get("FAIRY_VISION_URL", "http://127.0.0.1:8791")


# ---------------------------------------------------------------------------
# Small helpers
# ---------------------------------------------------------------------------


def log(message: str) -> None:
    print(message, file=sys.stderr, flush=True)


def resolve_ffmpeg() -> str:
    """Locate an ffmpeg binary, preferring one that is on PATH.

    The Windows box has several unrelated ffmpeg builds scattered around and
    needed the pinned static one shipped by imageio-ffmpeg. On Linux/macOS
    PATH already has a matched ffmpeg/ffprobe pair, and that pair must be used
    together: yt-dlp calls ffprobe from the same directory as ffmpeg, and an
    imageio build without a matching ffprobe makes every postprocessing step
    fail with "unable to obtain file audio codec with ffprobe".
    """
    if shutil.which("ffmpeg") and shutil.which("ffprobe"):
        return shutil.which("ffmpeg")
    try:
        import imageio_ffmpeg

        return imageio_ffmpeg.get_ffmpeg_exe()
    except Exception:
        return "ffmpeg"


FFMPEG = resolve_ffmpeg()


def run(command: Sequence[str], description: str) -> subprocess.CompletedProcess:
    log(f"[..] {description}")
    result = subprocess.run(
        command,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        encoding="utf-8",
        errors="replace",
    )
    if result.returncode != 0:
        tail = (result.stderr or "").strip().splitlines()[-6:]
        raise RuntimeError(f"{description} failed: " + " | ".join(tail))
    return result


def probe_duration(path: Path) -> float:
    try:
        result = subprocess.run(
            [FFMPEG, "-i", str(path)],
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            encoding="utf-8",
            errors="replace",
        )
        match = re.search(r"Duration: (\d+):(\d+):(\d+\.\d+)", result.stderr or "")
        if match:
            hours, minutes, seconds = match.groups()
            return int(hours) * 3600 + int(minutes) * 60 + float(seconds)
    except Exception:
        pass
    return 0.0


def slugify(value: str, limit: int = 60) -> str:
    cleaned = re.sub(r"[^\w\u3400-\u9fff-]+", "-", value, flags=re.UNICODE).strip("-")
    return (cleaned or "video")[:limit]


# ---------------------------------------------------------------------------
# Input resolution
# ---------------------------------------------------------------------------


@dataclass
class Media:
    """What we ended up with on disk after resolution."""

    title: str = ""
    webpage_url: str = ""
    duration_s: float = 0.0
    subtitle_path: Optional[str] = None
    subtitle_lang: str = ""
    subtitle_kind: str = ""  # "human" | "automatic"
    audio_path: Optional[str] = None
    video_path: Optional[str] = None
    notes: List[str] = field(default_factory=list)


SUBTITLE_EXTENSIONS = {".srt", ".vtt", ".ass", ".ssa", ".json3", ".ttml"}
AUDIO_EXTENSIONS = {".wav", ".mp3", ".m4a", ".aac", ".flac", ".ogg", ".opus", ".wma"}
VIDEO_EXTENSIONS = {".mp4", ".mkv", ".webm", ".mov", ".avi", ".flv", ".ts", ".m4v"}


def is_url(value: str) -> bool:
    return value.startswith("http://") or value.startswith("https://")


def youtube_dl_options(output_dir: Path) -> Dict[str, Any]:
    return {
        "paths": {"home": str(output_dir)},
        "outtmpl": "%(title).80s.%(ext)s",
        "quiet": True,
        "no_warnings": True,
        "noprogress": True,
        "retries": 3,
        "nocheckcertificate": True,
    }


def resolve_url(url: str, output_dir: Path, languages: Sequence[str], want_keyframes: bool = False) -> Media:
    """Fetch subtitles if the platform has them, otherwise fetch audio."""
    try:
        import yt_dlp
    except ImportError as exc:  # pragma: no cover
        raise RuntimeError("yt-dlp is not installed") from exc

    media = Media(webpage_url=url)
    output_dir.mkdir(parents=True, exist_ok=True)

    # Step 1: metadata only, to learn whether a caption track exists. This is
    # cheap and decides whether we need the audio stream at all.
    log("[..] probing platform metadata")
    with yt_dlp.YoutubeDL({**youtube_dl_options(output_dir), "skip_download": True}) as ydl:
        info = ydl.extract_info(url, download=False)

    media.title = str(info.get("title") or "")
    media.duration_s = float(info.get("duration") or 0)
    human = info.get("subtitles") or {}
    automatic = info.get("automatic_captions") or {}

    chosen_lang = ""
    chosen_kind = ""
    for language in languages:
        if language in human:
            chosen_lang, chosen_kind = language, "human"
            break
    if not chosen_lang:
        for language in languages:
            for key in automatic:
                if key == language or key.startswith(language + "-"):
                    chosen_lang, chosen_kind = key, "automatic"
                    break
            if chosen_lang:
                break
    if not chosen_lang and automatic:
        chosen_lang, chosen_kind = next(iter(automatic)), "automatic"

    if chosen_lang:
        log(f"[..] downloading subtitles: {chosen_lang} ({chosen_kind})")
        options = {
            **youtube_dl_options(output_dir),
            "skip_download": True,
            "writesubtitles": True,
            "writeautomaticsub": True,
            "subtitleslangs": [chosen_lang],
            "subtitlesformat": "srt/vtt/best",
        }
        with yt_dlp.YoutubeDL(options) as ydl:
            ydl.download([url])
        candidates = sorted(
            (
                path
                for path in output_dir.iterdir()
                if path.is_file()
                and path.suffix.lower() in SUBTITLE_EXTENSIONS
                and chosen_lang.split("-")[0] in path.name
            ),
            key=lambda path: path.stat().st_mtime,
            reverse=True,
        )
        if candidates:
            media.subtitle_path = str(candidates[0])
            media.subtitle_lang = chosen_lang
            media.subtitle_kind = chosen_kind
            return media
        media.notes.append("a caption track was advertised but no subtitle file was produced")
    else:
        media.notes.append("no caption track on this platform")

    # Step 2: no usable captions, so fetch audio for local speech recognition.
    # The video stream is only pulled when keyframes were actually requested:
    # it is several times larger than the audio, but without it the frame
    # extraction below is dead code and every product/site name the STT backend
    # mangles stays unverified.
    need_frames = want_keyframes
    log(f"[..] downloading audio for local speech recognition (video stream: {'yes' if need_frames else 'no'})")
    audio_base = output_dir / "audio"
    options = {
        **youtube_dl_options(output_dir),
        "format": (
            "bv*[height<=480][ext=mp4]/bv*[height<=480]/b[height<=480]/bestaudio/best"
            if need_frames
            else "bestaudio/best"
        ),
        "outtmpl": str(audio_base) + ".%(ext)s",
        "postprocessors": [
            {
                "key": "FFmpegExtractAudio",
                "preferredcodec": "wav",
                "preferredquality": "0",
            },
        ],
        # imageio-ffmpeg ships an ffmpeg binary with no ffprobe next to it, and
        # yt-dlp needs both; fall back to PATH when the bundled dir is partial.
        "ffmpeg_location": str(Path(FFMPEG).parent) if (Path(FFMPEG).parent / "ffprobe").exists() else None,
    }
    with yt_dlp.YoutubeDL(options) as ydl:
        ydl.download([url])
    wav_candidates = sorted(output_dir.glob("audio*.wav"), key=lambda p: p.stat().st_mtime, reverse=True)
    if wav_candidates:
        # yt-dlp 2026 dropped the generic FFmpegPostProcessor (split into
        # per-purpose remuxers), so the 16k mono resample is done by ffmpeg
        # here; routing it through yt-dlp used to work and now raises KeyError.
        source = wav_candidates[0]
        target = source.with_name(source.stem + "_16k.wav")
        result = subprocess.run(
            [str(FFMPEG), "-y", "-i", str(source), "-ar", str(SAMPLE_RATE), "-ac", "1", str(target)],
            check=False,
            capture_output=True,
        )
        # In-place resample (same path in and out) is unreliable, and swallowing
        # the error left a 48kHz file feeding a 16kHz-only recognizer, which
        # silently produced garbage transcripts. Resample to a separate file,
        # verify the result is really 16k mono, and only then swap it in.
        if result.returncode == 0 and target.exists() and _wav_is_16k_mono(target):
            target.replace(source)
        else:
            stderr = result.stderr.decode("utf-8", "replace")[-300:]
            media.notes.append(f"resample to 16k mono failed (rc={result.returncode}); {stderr}")
            if target.exists():
                target.unlink()
    if wav_candidates:
        media.audio_path = str(wav_candidates[0])
    else:
        media.notes.append("audio extraction produced no wav file")
    return media


def _wav_is_16k_mono(path: Path) -> bool:
    with wave.open(str(path)) as handle:
        return handle.getframerate() == SAMPLE_RATE and handle.getnchannels() == 1


def resolve_local(path: Path) -> Media:
    media = Media(title=path.stem)
    suffix = path.suffix.lower()
    if suffix in SUBTITLE_EXTENSIONS:
        media.subtitle_path = str(path)
        media.subtitle_lang = "unknown"
        media.subtitle_kind = "file"
    elif suffix in AUDIO_EXTENSIONS:
        media.audio_path = str(path)
    elif suffix in VIDEO_EXTENSIONS:
        media.video_path = str(path)
    else:
        raise RuntimeError(f"unsupported input type: {suffix or path.name}")
    media.duration_s = probe_duration(path)
    return media


# ---------------------------------------------------------------------------
# Subtitle parsing
# ---------------------------------------------------------------------------


TIMESTAMP = re.compile(
    r"(?P<h>\d{1,2}):(?P<m>\d{2}):(?P<s>\d{2})[,.](?P<ms>\d{1,3})\s*-->\s*"
    r"(?P<h2>\d{1,2}):(?P<m2>\d{2}):(?P<s2>\d{2})[,.](?P<ms2>\d{1,3})"
)
TAG = re.compile(r"<[^>]+>")


def _seconds(hours: str, minutes: str, seconds: str, millis: str) -> float:
    return int(hours) * 3600 + int(minutes) * 60 + int(seconds) + int(millis.ljust(3, "0")) / 1000


def parse_subtitles(path: Path) -> List[Dict[str, Any]]:
    """Parse SRT or VTT into timestamped segments, dropping caption artefacts."""
    raw = path.read_text(encoding="utf-8", errors="replace")
    segments: List[Dict[str, Any]] = []
    lines = raw.replace("\r\n", "\n").split("\n")
    index = 0
    while index < len(lines):
        match = TIMESTAMP.search(lines[index])
        if not match:
            index += 1
            continue
        start = _seconds(match.group("h"), match.group("m"), match.group("s"), match.group("ms"))
        end = _seconds(match.group("h2"), match.group("m2"), match.group("s2"), match.group("ms2"))
        index += 1
        text_lines: List[str] = []
        while index < len(lines) and lines[index].strip() and not TIMESTAMP.search(lines[index]):
            text_lines.append(TAG.sub("", lines[index]).strip())
            index += 1
        text = " ".join(part for part in text_lines if part).strip()
        if not text:
            continue
        # Auto-captions repeat the previous line as a rolling window; identical
        # consecutive segments add nothing to a summary.
        if segments and segments[-1]["text"] == text:
            segments[-1]["end"] = end
            continue
        segments.append({"start": round(start, 2), "end": round(end, 2), "text": text})
    return segments


# ---------------------------------------------------------------------------
# Speech recognition
# ---------------------------------------------------------------------------


def model_root() -> Path:
    """Where the local STT models live, on any platform.

    The old default was the literal string ``E:\\Fairy\\.tools\\sherpa-models``,
    which on Linux/macOS silently degrades to a relative path that never
    resolves, so the sherpa branch was a guaranteed miss and every run fell
    through to faster-whisper. Walk up from this file looking for a
    ``.tools/sherpa-models`` directory instead; the Windows path stays as the
    last resort.
    """
    env = os.environ.get("FAIRY_STT_MODEL_DIR")
    if env:
        return Path(env)
    for parent in Path(__file__).resolve().parents:
        candidate = parent / ".tools" / "sherpa-models"
        if candidate.is_dir():
            return candidate
    return Path(r"E:\Fairy\.tools\sherpa-models")


SENSE_VOICE_MODELS = (
    "sherpa-onnx-sense-voice-zh-en-ja-ko-yue-int8-2025-09-09",
    "sherpa-onnx-sense-voice-zh-en-ja-ko-yue-2024-07-17",
)

STREAMING_MODELS = (
    "sherpa-onnx-streaming-zipformer-small-bilingual-zh-en-2023-02-16",
    "sherpa-onnx-x-asr-480ms-streaming-zipformer-transducer-zh-en-punct-int8-2026-06-05",
)


def _usable_model_dir(root: Path, name: str) -> Optional[Path]:
    """A model dir only counts when its real payload is present.

    Directory existence alone is not enough: an interrupted download leaves an
    empty folder behind and that used to shadow a working model further down
    the list.
    """
    candidate = root / name
    if not candidate.is_dir():
        return None
    if name in SENSE_VOICE_MODELS:
        return candidate if (candidate / "model.int8.onnx").is_file() and (candidate / "tokens.txt").is_file() else None
    if any((candidate / part).is_file() for part in ("encoder.int8.onnx", "encoder.onnx")):
        return candidate
    return None


def default_sense_voice_model() -> Optional[Path]:
    root = model_root()
    for name in SENSE_VOICE_MODELS:
        found = _usable_model_dir(root, name)
        if found is not None:
            return found
    # Also honour a SenseVoice build dropped in under a name we do not know.
    for candidate in sorted(root.glob("*sense*voice*")):
        if (candidate / "model.int8.onnx").is_file() and (candidate / "tokens.txt").is_file():
            return candidate
    return None


def default_transducer_model() -> Optional[Path]:
    root = model_root()
    for name in STREAMING_MODELS:
        found = _usable_model_dir(root, name)
        if found is not None:
            return found
    return default_sense_voice_model()


def pick_model_file(directory: Path, *prefixes: str) -> Optional[Path]:
    files = sorted(directory.glob("*.onnx"))
    for prefix in prefixes:
        int8 = [f for f in files if f.name.startswith(prefix) and "int8" in f.name]
        if int8:
            return int8[0]
        plain = [f for f in files if f.name.startswith(prefix)]
        if plain:
            return plain[0]
    return None


def transcribe_with_sherpa(wav_path: Path, model_dir: Path, threads: int = 4) -> Tuple[List[Dict[str, Any]], str]:
    import sherpa_onnx
    import wave

    import numpy as np

    # SenseVoice is an offline (non-streaming) model with markedly better
    # accuracy on long speech; use it when the user has dropped one in.
    sense_voice_dir = model_dir if ((model_dir / "model.int8.onnx").is_file() and (model_dir / "tokens.txt").is_file()) else None
    if sense_voice_dir is not None:
        recognizer = sherpa_onnx.OfflineRecognizer.from_sense_voice(
            model=str(sense_voice_dir / "model.int8.onnx"),
            tokens=str(sense_voice_dir / "tokens.txt"),
            num_threads=threads,
            use_itn=True,
            provider="cpu",
        )
        with wave.open(str(wav_path)) as handle:
            sample_rate = handle.getframerate()
            frames = handle.readframes(handle.getnframes())
        samples = np.frombuffer(frames, dtype=np.int16).astype(np.float32) / 32768.0
        # SenseVoice is offline: decoding the whole file in one call yields a
        # single unsplittable blob with no timestamps, which is useless for a
        # summary. This sherpa-onnx build ships no VAD model, so split on
        # energy: walk 20ms frames, keep the speech runs, then decode each run
        # separately and stamp it with its real offset.
        segments: List[Dict[str, Any]] = []
        for run_start, run_end in _speech_runs(samples, sample_rate):
            chunk = samples[run_start:run_end]
            stream = recognizer.create_stream()
            stream.accept_waveform(sample_rate, chunk)
            recognizer.decode_stream(stream)
            text = (stream.result.text or "").strip()
            if text:
                segments.append(
                    {
                        "start": round(run_start / sample_rate, 2),
                        "end": round(run_end / sample_rate, 2),
                        "text": text,
                    }
                )
        return segments, f"sherpa-onnx/sensevoice ({model_dir.name})"

    tokens = model_dir / "tokens.txt"
    encoder = pick_model_file(model_dir, "encoder")
    decoder = pick_model_file(model_dir, "decoder")
    joiner = pick_model_file(model_dir, "joiner")
    if not (tokens.exists() and encoder and decoder and joiner):
        raise RuntimeError(f"incomplete model directory: {model_dir}")

    # Streaming transducer + endpoint detection is how we get sentence-like
    # segments with usable timestamps. Splicing the running hypothesis every
    # second instead loses text whenever the model revises what it already
    # emitted, which is exactly what a summary cannot afford.
    recognizer = sherpa_onnx.OnlineRecognizer.from_transducer(
        tokens=str(tokens),
        encoder=str(encoder),
        decoder=str(decoder),
        joiner=str(joiner),
        num_threads=threads,
        sample_rate=SAMPLE_RATE,
        feature_dim=80,
        decoding_method="greedy_search",
        enable_endpoint_detection=True,
        rule1_min_trailing_silence=2.4,
        rule2_min_trailing_silence=1.2,
        rule3_min_utterance_length=20.0,
        provider="cpu",
    )

    with wave.open(str(wav_path)) as handle:
        sample_rate = handle.getframerate()
        frames = handle.readframes(handle.getnframes())
    samples = np.frombuffer(frames, dtype=np.int16).astype(np.float32) / 32768.0
    if sample_rate != SAMPLE_RATE:
        raise RuntimeError(f"expected {SAMPLE_RATE} Hz audio, got {sample_rate}")

    stream = recognizer.create_stream()
    block = SAMPLE_RATE  # one second per step keeps memory flat on long videos
    segments: List[Dict[str, Any]] = []
    utterance_start = 0.0
    for offset in range(0, len(samples), block):
        chunk = samples[offset : offset + block]
        stream.accept_waveform(SAMPLE_RATE, chunk)
        while recognizer.is_ready(stream):
            recognizer.decode_stream(stream)
        if recognizer.is_endpoint(stream):
            text = recognizer.get_result(stream).strip()
            if text:
                segments.append(
                    {
                        "start": round(utterance_start, 2),
                        "end": round((offset + len(chunk)) / SAMPLE_RATE, 2),
                        "text": text,
                    }
                )
            recognizer.reset(stream)
            utterance_start = (offset + len(chunk)) / SAMPLE_RATE

    # Flush the trailing utterance, which has no endpoint silence after it.
    stream.accept_waveform(SAMPLE_RATE, np.zeros(int(SAMPLE_RATE * 0.5), dtype=np.float32))
    stream.input_finished()
    while recognizer.is_ready(stream):
        recognizer.decode_stream(stream)
    tail = recognizer.get_result(stream).strip()
    if tail:
        segments.append(
            {
                "start": round(utterance_start, 2),
                "end": round(len(samples) / SAMPLE_RATE + 0.5, 2),
                "text": tail,
            }
        )
    return segments, f"sherpa-onnx/zipformer ({model_dir.name})"


def _speech_runs(samples: np.ndarray, sample_rate: int, frame_ms: int = 20,
                  min_gap_ms: int = 400, min_run_ms: int = 400) -> List[Tuple[int, int]]:
    """Split 16k mono audio into speech runs by short-term energy.

    A real VAD (silero) would be better, but this sherpa-onnx build exposes no
    VAD binding and no vad model ships with the repo. Energy gating keeps the
    script dependency-free while still producing sentence-sized chunks with
    honest timestamps: the threshold is derived from the clip's own noise
    floor rather than a hardcoded constant, so quiet and loud recordings both
    work.
    """
    frame = max(1, int(sample_rate * frame_ms / 1000))
    n_frames = len(samples) // frame
    if n_frames == 0:
        return [(0, len(samples))] if len(samples) else []
    energies = np.sqrt((samples[: n_frames * frame].reshape(n_frames, frame) ** 2).mean(axis=1) + 1e-10)
    floor = float(np.percentile(energies, 10))
    peak = float(energies.max())
    if peak <= 0:
        return []
    threshold = max(floor * 3.0, peak * 0.06, 1e-4)
    voiced = energies >= threshold
    # close short holes so a pause between words does not split a phrase;
    # the gap has to be short enough that no clause is swallowed, but long
    # enough that a run stays phrase-sized rather than a couple of syllables
    gap = max(1, int(min_gap_ms / frame_ms))
    for i in range(len(voiced)):
        if not voiced[i] and i + gap < len(voiced) and voiced[i + 1]:
            voiced[i] = True
    min_run = max(1, int(min_run_ms / frame_ms))
    runs: List[Tuple[int, int]] = []
    start = None
    for i, v in enumerate(voiced):
        if v and start is None:
            start = i
        elif not v and start is not None:
            if i - start >= min_run:
                runs.append((start * frame, i * frame))
            start = None
    if start is not None and len(voiced) - start >= min_run:
        runs.append((start * frame, n_frames * frame))
    return runs or [(0, len(samples))]


def transcribe(wav_path: Path, backend: str, threads: int) -> Tuple[List[Dict[str, Any]], str]:
    # Order matters, and the reasoning is empirical. Measured on the same
    # 3:58 clip: whisper/base recovered "有ip的就不需要cdn" where the streaming
    # zipformer produced "邑邑AL CDN". Those zipformer models are streaming
    # transducers built for realtime partial results — quick (4.6s for 4
    # minutes) but weak on clean offline transcription.
    #
    # SenseVoice goes first when present: it is offline (no HuggingFace
    # download at run time, unlike faster-whisper), handles zh/en/ja/ko, and
    # is the most accurate of the three on Mandarin. The streaming zipformer
    # stays last as the always-offline safety net.
    #
    # Known tradeoff: on zh-en code-switched tech talk, SenseVoice is weak on
    # Latin brand names (it missed CDN/Cloudflare/xyz even on the unsliced
    # whole-clip decode, so it is not a slicing artifact) while faster-whisper
    # catches them. If a summary hinges on such terms, run --stt whisper.
    if backend in ("auto", "sensevoice", "sense-voice", "sv"):
        model_dir = default_sense_voice_model()
        if model_dir is not None:
            return transcribe_with_sherpa(wav_path, model_dir, threads)
        if backend != "auto":
            raise RuntimeError("no SenseVoice model directory was found")
    if backend in ("auto", "whisper"):
        try:
            from faster_whisper import WhisperModel
        except ImportError as exc:
            if backend == "whisper":
                raise RuntimeError("faster-whisper is not installed") from exc
        else:
            model = WhisperModel(os.environ.get("FAIRY_STT_MODEL", "base"), device="cpu", compute_type="int8")
            iterator, info = model.transcribe(str(wav_path), beam_size=5, vad_filter=True)
            segments = [
                {"start": round(item.start, 2), "end": round(item.end, 2), "text": item.text.strip()}
                for item in iterator
                if item.text.strip()
            ]
            language = getattr(info, "language", "unknown")
            return segments, f"faster-whisper/base ({language})"
    if backend in ("auto", "sherpa", "zipformer"):
        model_dir = default_transducer_model()
        if model_dir is not None:
            return transcribe_with_sherpa(wav_path, model_dir, threads)
        if backend != "auto":
            raise RuntimeError("no sherpa-onnx model directory was found")
    raise RuntimeError("no local speech recognition backend is available")


# ---------------------------------------------------------------------------
# Keyframes + on-screen text
# ---------------------------------------------------------------------------


def vision_available() -> bool:
    try:
        with urllib.request.urlopen(f"{VISION_SERVICE}/health", timeout=2) as response:
            return response.status == 200
    except Exception:
        return False


def start_vision_service() -> bool:
    """Start the same service the agent's computer_observe vision actions use."""
    script = Path(r"E:\Fairy\.tools\vision-service\cua_vision_service.py")
    python = Path(r"E:\Fairy\.tools\venv\Scripts\python.exe")
    if not (script.exists() and python.exists()):
        return False
    log("[..] starting the local vision service")
    creation = 0
    if os.name == "nt":
        creation = getattr(subprocess, "CREATE_NO_WINDOW", 0)
    subprocess.Popen(
        [str(python), str(script), "--port", "8791", "--idle-timeout", "600"],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        creationflags=creation,
    )
    for _ in range(60):
        time.sleep(0.5)
        if vision_available():
            return True
    return False


def ocr_frame(path: Path) -> str:
    payload = json.dumps({"image_path": str(path), "min_score": 0.5}).encode("utf-8")
    request = urllib.request.Request(
        f"{VISION_SERVICE}/ocr",
        data=payload,
        headers={"Content-Type": "application/json; charset=utf-8"},
    )
    try:
        with urllib.request.urlopen(request, timeout=180) as response:
            body = json.loads(response.read().decode("utf-8"))
    except (urllib.error.URLError, TimeoutError, ValueError) as exc:
        return f"<OCR failed: {exc}>"
    texts = body.get("texts") or []
    # Keyframe OCR exists to catch slides, titles and lower thirds. Running
    # order matters more than geometry here, and dedupe keeps repeated chyrons
    # from flooding the digest.
    seen: List[str] = []
    for item in texts:
        value = str(item.get("text", "")).strip()
        if value and value not in seen:
            seen.append(value)
    return " | ".join(seen[:40])


def extract_keyframes(video: Path, output_dir: Path, interval: float, max_frames: int) -> List[Dict[str, Any]]:
    output_dir.mkdir(parents=True, exist_ok=True)
    pattern = str(output_dir / "frame-%04d.png")
    run(
        [
            FFMPEG,
            "-hide_banner",
            "-loglevel",
            "error",
            "-i",
            str(video),
            "-vf",
            f"fps=1/{interval},scale=1280:-2",
            "-frames:v",
            str(max_frames),
            "-y",
            pattern,
        ],
        f"extracting keyframes every {interval:g}s",
    )
    frames: List[Dict[str, Any]] = []
    for index, path in enumerate(sorted(output_dir.glob("frame-*.png"))):
        frames.append({"t": round(index * interval, 2), "path": str(path)})
    return frames


def dhash(path: Path, size: int = 8) -> Optional[int]:
    """Perceptual hash of a frame, so near-identical shots are not re-analysed.

    This collapses *identical* frames - slides held on screen, a static desktop,
    a paused game - which is where the waste actually is. It does not merge
    "same subject, different camera angle": a filmed speech cuts between angles
    and the hashes legitimately differ. Deciding that six shots of the same
    podium carry one idea is the agent's judgement, not this function's.
    """
    try:
        import cv2
        import numpy as np

        image = cv2.imread(str(path), cv2.IMREAD_GRAYSCALE)
        if image is None:
            return None
        resized = cv2.resize(image, (size + 1, size), interpolation=cv2.INTER_AREA)
        diff = resized[:, 1:] > resized[:, :-1]
        value = 0
        for bit in diff.flatten():
            value = (value << 1) | int(bit)
        return value
    except Exception:
        return None


def dedupe_frames(frames: List[Dict[str, Any]], max_distance: int = 4) -> List[Dict[str, Any]]:
    """Keep the first frame of each visually identical run (see dhash)."""
    kept: List[Dict[str, Any]] = []
    last_hash: Optional[int] = None
    dropped = 0
    for frame in frames:
        current = dhash(Path(frame["path"]))
        if current is None:
            kept.append(frame)
            continue
        if last_hash is not None:
            distance = bin(current ^ last_hash).count("1")
            if distance <= max_distance:
                dropped += 1
                continue
        frame["phash"] = f"{current:016x}"
        kept.append(frame)
        last_hash = current
    if dropped:
        log(f"[..] dropped {dropped} near-duplicate frame(s)")
    return kept


# ---------------------------------------------------------------------------
# Assembly
# ---------------------------------------------------------------------------


def build_outline(segments: Sequence[Dict[str, Any]], duration: float, block_s: float = 120) -> List[Dict[str, Any]]:
    if not segments:
        return []
    span = duration or (segments[-1]["end"] if segments else 0)
    blocks: List[Dict[str, Any]] = []
    start = 0.0
    while start < max(span, 1):
        end = start + block_s
        chunk = [s for s in segments if s["start"] >= start and s["start"] < end]
        if chunk:
            text = " ".join(s["text"] for s in chunk)
            blocks.append(
                {
                    "start": round(start, 2),
                    "end": round(min(end, span), 2),
                    "chars": len(text),
                    "preview": text[:160],
                }
            )
        start = end
    return blocks


def format_timestamp(seconds: float) -> str:
    total = int(seconds)
    return f"{total // 3600:02d}:{(total % 3600) // 60:02d}:{total % 60:02d}"


def render_markdown(digest: Dict[str, Any]) -> str:
    source = digest["source"]
    transcript = digest["transcript"]
    lines = [
        f"# 视频摘要素材：{source.get('title') or source.get('path')}",
        "",
        "## 元信息",
        "",
        f"- 时长：{format_timestamp(source.get('duration_s') or 0)}",
        f"- 文字来源：`{transcript.get('engine')}`",
    ]
    if source.get("subtitle_kind"):
        lines.append(f"- 字幕类型：{source['subtitle_kind']}（{source.get('subtitle_lang')}）")
    for note in source.get("notes") or []:
        lines.append(f"- 注意：{note}")
    lines += ["", "## 结构概览", ""]
    for block in digest.get("outline") or []:
        lines.append(
            f"- [{format_timestamp(block['start'])}–{format_timestamp(block['end'])}] "
            f"{block['chars']} 字：{block['preview']}"
        )
    if digest.get("keyframes"):
        if digest.get("frames_need_review"):
            lines += [
                "",
                "## 关键帧：待人工/agent 核对",
                "",
                "本地没有视觉服务，这些帧**没有 OCR**。语音转写在英文站名、品牌名、"
                "专有名词上错误率高（实测同一次转写把 `Cloudflare` 听成 `邑邑AL`、"
                "把站点名听成 `DGTO PLAT`），而这些词通常就写在画面上。",
                "",
                "**请逐帧查看下面的图片核对专名，再据此撰写摘要**：",
                "",
            ]
            for frame in digest["keyframes"]:
                lines.append(f"- [{format_timestamp(frame['t'])}] {frame['path']}")
        else:
            lines += ["", "## 关键帧画面文字（本地 OCR）", ""]
            for frame in digest["keyframes"]:
                text = frame.get("text") or "（无文字）"
                lines.append(f"- [{format_timestamp(frame['t'])}] {text}")
    lines += ["", "## 完整转写", ""]
    for segment in transcript.get("segments") or []:
        lines.append(f"[{format_timestamp(segment['start'])}] {segment['text']}")
    return "\n".join(lines) + "\n"


def main() -> int:
    parser = argparse.ArgumentParser(description="Build a summary-ready digest of a video")
    parser.add_argument("input", help="URL, video, audio or subtitle file")
    parser.add_argument("--out", default="", help="output directory (default workspace/result/video-summary/<slug>)")
    parser.add_argument("--lang", default="zh-Hans,zh-CN,zh,en", help="comma separated subtitle language preference")
    parser.add_argument(
        "--stt",
        default="auto",
        choices=["auto", "sensevoice", "sense-voice", "sv", "sherpa", "zipformer", "whisper"],
        help=(
            "speech recognition backend. auto prefers SenseVoice (offline, most accurate), "
            "then faster-whisper, then the streaming sherpa zipformer as a last-resort net"
        ),
    )
    parser.add_argument("--threads", type=int, default=4, help="STT threads")
    parser.add_argument("--interval", type=float, default=30.0, help="seconds between keyframes")
    parser.add_argument("--max-frames", type=int, default=40, help="keyframe cap")
    parser.add_argument("--no-frames", action="store_true", help="skip keyframes and on-screen text")
    parser.add_argument(
        "--keep-duplicate-frames",
        action="store_true",
        help="keep visually identical frames instead of collapsing each shot to one",
    )
    parser.add_argument("--json-only", action="store_true", help="print JSON instead of the markdown digest")
    args = parser.parse_args()

    started = time.time()
    languages = [item.strip() for item in args.lang.split(",") if item.strip()]
    work_dir = Path(tempfile.mkdtemp(prefix="fairy-video-"))
    input_value = args.input.strip()
    source_path = Path(input_value)

    if is_url(input_value):
        media = resolve_url(input_value, work_dir, languages, want_keyframes=not args.no_frames)
        if not media.title:
            media.title = input_value
    else:
        if not source_path.exists():
            log(f"input not found: {source_path}")
            return 2
        media = resolve_local(source_path)

    transcript_segments: List[Dict[str, Any]] = []
    engine = ""

    if media.subtitle_path:
        transcript_segments = parse_subtitles(Path(media.subtitle_path))
        engine = f"subtitles/{media.subtitle_kind} ({media.subtitle_lang})"
    else:
        wav_path = Path(media.audio_path) if media.audio_path else None
        if wav_path is None and media.video_path:
            wav_path = work_dir / "audio.wav"
            try:
                run(
                    [
                        FFMPEG,
                        "-hide_banner",
                        "-loglevel",
                        "error",
                        "-i",
                        media.video_path,
                        "-vn",
                        "-ar",
                        str(SAMPLE_RATE),
                        "-ac",
                        "1",
                        "-y",
                        str(wav_path),
                    ],
                    "extracting audio track",
                )
            except RuntimeError:
                # Silent screen recordings, animations and muted clips have no
                # audio stream. That is not a reason to abandon the whole
                # digest: the keyframes may still carry everything worth
                # summarising.
                media.notes.append("no audio track; the transcript was skipped and only the picture was analysed")
                wav_path = None
                if not media.duration_s:
                    media.duration_s = probe_duration(Path(media.video_path))
        if wav_path is None:
            engine = "none (no subtitles and no audio track)"
        else:
            if not media.duration_s and media.video_path:
                media.duration_s = probe_duration(Path(media.video_path))
            transcript_segments, engine = transcribe(wav_path, args.stt, args.threads)

    if not transcript_segments:
        log("transcript is empty")

    out_dir = Path(args.out) if args.out else Path(r"E:\Fairy\workspace\result\video-summary") / slugify(media.title or source_path.stem)
    out_dir.mkdir(parents=True, exist_ok=True)

    keyframes: List[Dict[str, Any]] = []
    frames_need_review = False
    if not args.no_frames and media.video_path:
        # Frames must land in out_dir, not the temp work_dir: they used to be
        # written to work_dir and then dropped when the run finished, so every
        # default run threw away the very evidence that fixes the names the STT
        # backend mangles (brands, sites, jargon). A named-captions video is
        # exactly where on-screen text beats transcription.
        frames = extract_keyframes(Path(media.video_path), out_dir / "frames", args.interval, args.max_frames)
        if frames and not args.keep_duplicate_frames:
            frames = dedupe_frames(frames)
        if frames:
            if not vision_available():
                start_vision_service()
            if vision_available():
                for frame in frames:
                    frame["text"] = ocr_frame(Path(frame["path"]))
                    log(f"[..] OCR {Path(frame['path']).name} -> {(frame['text'] or '(none)')[:60]}")
            else:
                # No vision endpoint. Do not silently drop the frames: the
                # calling agent usually has native vision and can read them, so
                # hand it the file list plus an explicit instruction.
                frames_need_review = True
                media.notes.append(
                    "vision service unavailable; keyframes were kept under "
                    f"{out_dir / 'frames'} for manual review. Read them to confirm "
                    "product names, sites and jargon the STT backend likely misheard."
                )
            keyframes = frames
    elif is_url(input_value) and not media.video_path:
        media.notes.append("no video stream was downloaded, so keyframes and on-screen text were skipped")

    duration = media.duration_s
    if not duration and transcript_segments:
        duration = transcript_segments[-1]["end"]

    digest = {
        "source": {
            "input": input_value,
            "path": media.video_path or media.audio_path or media.subtitle_path or "",
            "title": media.title,
            "webpage_url": media.webpage_url,
            "duration_s": round(duration, 2),
            "subtitle_path": media.subtitle_path,
            "subtitle_lang": media.subtitle_lang,
            "subtitle_kind": media.subtitle_kind,
            "notes": media.notes,
        },
        "transcript": {
            "engine": engine,
            "segment_count": len(transcript_segments),
            "char_count": sum(len(segment["text"]) for segment in transcript_segments),
            "segments": transcript_segments,
        },
        "outline": build_outline(transcript_segments, duration),
        "keyframes": keyframes,
        "frames_need_review": frames_need_review,
        "elapsed_s": round(time.time() - started, 2),
    }

    json_path = out_dir / "digest.json"
    json_path.write_text(json.dumps(digest, ensure_ascii=False, indent=2), encoding="utf-8")
    markdown_path = out_dir / "digest.md"
    markdown_path.write_text(render_markdown(digest), encoding="utf-8")

    log(f"[ok] engine={engine} segments={len(transcript_segments)} keyframes={len(keyframes)}")
    log(f"[ok] {json_path}")
    log(f"[ok] {markdown_path}")

    if args.json_only:
        print(json.dumps(digest, ensure_ascii=False))
    else:
        print(render_markdown(digest))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
