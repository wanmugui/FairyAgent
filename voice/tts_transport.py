"""Compact, fully-buffered transport for synthesized voice audio."""

import subprocess

import numpy as np


def encode_tts_mp3(audio, sample_rate=48_000, bitrate="48k"):
    """Encode channel-major float32 audio as a single MP3 file.

    The voice engine currently produces stereo float32 PCM. Sending that as
    Base64 makes a normal reply several megabytes and exposes every network
    stall as an audible gap. MP3 keeps the complete utterance in one response,
    which the browser can download and play without per-chunk underruns.
    """
    samples = np.asarray(audio, dtype=np.float32)
    if samples.ndim == 1:
        channels = 1
        frames = samples
    elif samples.ndim == 2:
        channels = int(samples.shape[0])
        if channels not in (1, 2):
            raise ValueError(f"unsupported channel count: {channels}")
        frames = samples.T.reshape(-1)
    else:
        raise ValueError(f"unsupported audio shape: {samples.shape}")
    if frames.size == 0:
        raise ValueError("audio contains no samples")

    command = [
        "ffmpeg",
        "-hide_banner",
        "-loglevel",
        "error",
        "-f",
        "f32le",
        "-ar",
        str(int(sample_rate)),
        "-ac",
        str(channels),
        "-i",
        "pipe:0",
        "-ac",
        "1",
        "-ar",
        "24000",
        "-c:a",
        "libmp3lame",
        "-b:a",
        str(bitrate),
        "-f",
        "mp3",
        "pipe:1",
    ]
    result = subprocess.run(
        command,
        input=np.ascontiguousarray(frames, dtype="<f4").tobytes(),
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    if result.returncode != 0:
        detail = result.stderr.decode("utf-8", errors="replace").strip()
        raise RuntimeError(f"ffmpeg MP3 encode failed: {detail or result.returncode}")
    if not result.stdout:
        raise RuntimeError("ffmpeg MP3 encode produced no audio")
    return result.stdout
