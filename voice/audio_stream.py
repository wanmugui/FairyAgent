"""Incremental browser-audio decoding for the voice WebSocket."""

import queue
import subprocess
import threading
import time
import wave
from pathlib import Path

import numpy as np


class PCMDebugCapture:
    """Temporarily persist decoded microphone PCM for voice diagnostics."""

    def __init__(self, sample_rate=16_000, max_seconds=60, directory="/tmp"):
        self.sample_rate = int(sample_rate)
        self.max_samples = self.sample_rate * int(max_seconds)
        self.samples_written = 0
        self.path = Path(directory) / (
            f"fairy-stt-capture-{int(time.time())}-{threading.get_ident()}.wav"
        )
        self._wave = wave.open(str(self.path), "wb")
        self._wave.setnchannels(1)
        self._wave.setsampwidth(2)
        self._wave.setframerate(self.sample_rate)

    def write(self, samples):
        if self.samples_written >= self.max_samples:
            return
        audio = np.asarray(samples, dtype=np.float32).reshape(-1)
        audio = audio[: self.max_samples - self.samples_written]
        if audio.size == 0:
            return
        pcm = np.clip(audio * 32767.0, -32768, 32767).astype("<i2")
        self._wave.writeframes(pcm.tobytes())
        self.samples_written += int(audio.size)

    def close(self):
        if self._wave is None:
            return
        self._wave.close()
        self._wave = None


class StreamingAudioDecoder:
    """Decode MediaRecorder WebM/Ogg/MP4 chunks into 16 kHz mono PCM."""

    def __init__(self, sample_rate=16_000, debug_path=None):
        self.sample_rate = int(sample_rate)
        self._chunks = queue.Queue()
        self._closed = False
        self._stderr_lines = []
        self.debug_path = Path(debug_path) if debug_path else None
        self._debug_file = (
            self.debug_path.open("wb") if self.debug_path is not None else None
        )
        self.process = subprocess.Popen(
            [
                "ffmpeg",
                "-hide_banner",
                "-loglevel",
                "error",
                "-fflags",
                "nobuffer",
                "-flags",
                "low_delay",
                "-i",
                "pipe:0",
                "-f",
                "f32le",
                "-ac",
                "1",
                "-ar",
                str(self.sample_rate),
                "pipe:1",
            ],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
        self._reader = threading.Thread(
            target=self._read_stdout,
            daemon=True,
            name="fairy-voice-decode",
        )
        self._reader.start()
        self._stderr_reader = threading.Thread(
            target=self._read_stderr,
            daemon=True,
            name="fairy-voice-decode-log",
        )
        self._stderr_reader.start()

    def _read_stdout(self):
        remainder = b""
        while True:
            block = self.process.stdout.read(4096)
            if not block:
                break
            data = remainder + block
            usable = len(data) - (len(data) % 4)
            if usable:
                samples = np.frombuffer(data[:usable], dtype="<f4").copy()
                self._chunks.put(samples)
            remainder = data[usable:]

    def _read_stderr(self):
        for line in self.process.stderr:
            text = line.decode("utf-8", errors="replace").strip()
            if not text:
                continue
            self._stderr_lines.append(text)
            if len(self._stderr_lines) > 20:
                del self._stderr_lines[0]

    def feed(self, data):
        if self._closed or not data:
            return
        if self._debug_file is not None:
            self._debug_file.write(data)
            self._debug_file.flush()
        try:
            self.process.stdin.write(data)
            self.process.stdin.flush()
        except (BrokenPipeError, ValueError):
            self._closed = True

    def read(self, timeout=0.08):
        try:
            first = self._chunks.get(timeout=timeout)
        except queue.Empty:
            return np.zeros(0, dtype=np.float32)
        chunks = [first]
        while True:
            try:
                chunks.append(self._chunks.get_nowait())
            except queue.Empty:
                break
        return np.concatenate(chunks).astype(np.float32, copy=False)

    def close(self):
        if self._closed:
            return
        self._closed = True
        try:
            self.process.stdin.close()
        except Exception:
            pass
        try:
            self.process.wait(timeout=2)
        except subprocess.TimeoutExpired:
            self.process.terminate()
            try:
                self.process.wait(timeout=1)
            except subprocess.TimeoutExpired:
                self.process.kill()
        self._reader.join(timeout=1)
        self._stderr_reader.join(timeout=1)
        if self._debug_file is not None:
            self._debug_file.close()
            self._debug_file = None

    @property
    def error_text(self):
        return "\n".join(self._stderr_lines)
