"""Send a wav file through the Fairy streaming STT WebSocket.

Usage:
    python voice/smoke_stt.py path\\to\\sample.wav
"""

from __future__ import annotations

import argparse
import asyncio
import json
import time
import wave
from pathlib import Path

import numpy as np

SAMPLE_RATE = 16_000


def load_pcm16(path: Path) -> np.ndarray:
    with wave.open(str(path), "rb") as wav:
        channels = wav.getnchannels()
        rate = wav.getframerate()
        audio = np.frombuffer(wav.readframes(wav.getnframes()), dtype=np.int16)
    audio = audio.astype(np.float32).reshape(-1, channels).mean(axis=1) / 32768.0
    if rate != SAMPLE_RATE:
        x_old = np.linspace(0.0, 1.0, num=audio.size, endpoint=False)
        x_new = np.linspace(0.0, 1.0, num=round(audio.size * SAMPLE_RATE / rate), endpoint=False)
        audio = np.interp(x_new, x_old, audio).astype(np.float32)
    return audio


async def run(path: Path, url: str, wake: bool = False) -> None:
    import websockets

    audio = load_pcm16(path)
    started = time.monotonic()
    async with websockets.connect(url, max_size=4_000_000) as ws:
        if wake:
            await ws.send(json.dumps({
                "type": "config",
                "mode": "wake",
                "wakeWords": ["fairy", "绯蕊", "菲蕊", "菲雅"],
                "silenceResetSec": 30,
                "commandSilenceSec": 2.0,
            }))
        status = json.loads(await ws.recv())
        print(f"{time.monotonic() - started:.2f}s {status}")
        if status.get("status") == "loading":
            ready = json.loads(await asyncio.wait_for(ws.recv(), timeout=60))
            print(f"{time.monotonic() - started:.2f}s {ready}")
        if wake:
            wake_state = json.loads(await asyncio.wait_for(ws.recv(), timeout=10))
            print(f"{time.monotonic() - started:.2f}s {wake_state}")
        chunk_size = 1_365
        for start in range(0, audio.size, chunk_size):
            chunk = np.ascontiguousarray(audio[start:start + chunk_size], dtype=np.float32)
            await ws.send(chunk.tobytes())
            await asyncio.sleep(0.10)
        await ws.send(json.dumps({"type": "end"}))
        while True:
            message = json.loads(await asyncio.wait_for(ws.recv(), timeout=20))
            print(f"{time.monotonic() - started:.2f}s {message}")
            if message.get("type") == "final":
                return


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("wav", type=Path)
    parser.add_argument("--url", default="ws://127.0.0.1:8788/ws/stt")
    parser.add_argument("--wake", action="store_true")
    args = parser.parse_args()
    asyncio.run(run(args.wav, args.url, wake=args.wake))


if __name__ == "__main__":
    main()
