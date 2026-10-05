"""Fairy voice service: MOSS-TTS (streaming + voice clone) + Faster-Whisper STT."""
import asyncio, base64, json, os, re, sys, threading, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import numpy as np

REPO = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO / "voice"))
VOICE_VENDOR = REPO / ".tools" / "voice-py310"
VOICE_PORT = int(os.getenv("FAIRY_VOICE_PORT", "8787"))
VOICE_WS_PORT = int(os.getenv("FAIRY_VOICE_WS_PORT", str(VOICE_PORT + 1)))
if VOICE_VENDOR.is_dir():
    sys.path.insert(0, str(VOICE_VENDOR))

try:
    import webrtcvad
except Exception:
    webrtcvad = None

from tts_engine import MossTTSNanoEngine
from stt_engine import (
    DEFAULT_COMMAND_MAX_SEC,
    DEFAULT_COMMAND_SILENCE_SEC,
    FasterWhisperSTT,
    HybridStreamingSession,
    SAMPLE_RATE,
    SherpaStreamingPartial,
    command_endpoint_reached,
    normalize_text,
)

TTS_DIR = REPO / "models" / "voice" / "MOSS-TTS-Nano-100M-ONNX"
CODEC_DIR = REPO / "models" / "voice" / "MOSS-Audio-Tokenizer-Nano-ONNX"
# Custom voice reference wav (repo root by default, override with FAIRY_REFERENCE_WAV).
REFERENCE_WAV = Path(os.environ.get("FAIRY_REFERENCE_WAV") or (REPO / "fairy_vocals_48k.wav"))

_tts = None
_tts_lock = threading.Lock()
_stt = None
_stt_lock = threading.Lock()
_sherpa_partial = None
_sherpa_partial_lock = threading.Lock()
_voice_codes = None
_voice_lock = threading.Lock()
_voice_name = "builtin-junhao"
DEFAULT_WAKE_WORDS = (
    "fairy", "绯蕊", "菲蕊", "翡蕊", "斐蕊", "非蕊", "绯睿", "菲睿",
    "绯瑞", "菲瑞", "飞蕊", "飞瑞", "费蕊", "菲雅", "菲亚",
    "ferry", "fairie", "fary", "faery", "fayrie", "ferri", "ferrie",
)


def get_tts():
    global _tts
    if _tts is None:
        with _tts_lock:
            if _tts is None:
                print("[voice] loading MOSS-TTS...", flush=True)
                _tts = MossTTSNanoEngine(str(TTS_DIR), str(CODEC_DIR), seed=int(os.environ.get("FAIRY_TTS_SEED", "2")))
                print("[voice] TTS ready", flush=True)
    return _tts


def get_voice():
    """Return (prompt_codes_or_None, voice_name). Encodes the reference wav once."""
    global _voice_codes, _voice_name
    if _voice_codes is None and _voice_name == "builtin-junhao":
        with _voice_lock:
            if _voice_codes is None:
                if REFERENCE_WAV.exists():
                    try:
                        codes = get_tts().encode_reference_audio(str(REFERENCE_WAV))
                        _voice_codes = codes
                        _voice_name = "fairy-custom"
                        print(f"[voice] custom voice from {REFERENCE_WAV.name}: {len(codes)} frames", flush=True)
                    except Exception as e:
                        print(f"[voice] reference encode failed ({e}); using builtin voice", flush=True)
                        _voice_name = "builtin-junhao"
                else:
                    _voice_name = "builtin-junhao"
    return _voice_codes, _voice_name


def get_stt():
    global _stt
    if _stt is None:
        with _stt_lock:
            if _stt is None:
                print("[voice] loading Faster-Whisper STT...", flush=True)
                _stt = FasterWhisperSTT()
                print("[voice] STT ready", flush=True)
    return _stt


def get_sherpa_partial():
    global _sherpa_partial
    if _sherpa_partial is None:
        with _sherpa_partial_lock:
            if _sherpa_partial is None:
                print("[voice] loading Sherpa streaming partial...", flush=True)
                _sherpa_partial = SherpaStreamingPartial()
                print("[voice] Sherpa partial ready", flush=True)
    return _sherpa_partial


class HTTPHandler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        pass

    def _cors(self):
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Access-Control-Allow-Headers", "Content-Type")
        self.send_header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")

    def do_OPTIONS(self):
        self.send_response(204)
        self._cors()
        self.end_headers()

    def _json(self, obj, code=200):
        body = json.dumps(obj, ensure_ascii=False).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self._cors()
        self.end_headers()
        self.wfile.write(body)

    def _read_json(self):
        length = int(self.headers.get("Content-Length", 0))
        return json.loads(self.rfile.read(length).decode("utf-8"))

    def do_GET(self):
        if self.path == "/health":
            codes, name = get_voice()
            self._json({
                "ok": True,
                "tts": _tts is not None,
                "stt": _stt is not None,
                "voice": name,
                "voice_frames": len(codes) if codes else 0,
            })
            return
        self._json({"error": "not found"}, 404)

    def do_POST(self):
        if self.path not in ("/api/tts", "/api/tts/stream"):
            self._json({"error": "not found"}, 404)
            return
        try:
            data = self._read_json()
        except Exception as e:
            self._json({"error": f"bad json: {e}"}, 400)
            return
        text = (data.get("text") or "").strip()
        if not text:
            self._json({"error": "empty text"}, 400)
            return
        if self.path == "/api/tts":
            self._tts_once(text)
        else:
            self._tts_stream(text)

    def _log_received_tts(self, text):
        try:
            with open(str(REPO / "voice_received.log"), "a", encoding="utf-8") as _f:
                _f.write((str(text)[:300] + "\n"))
        except Exception:
            pass

    def _tts_once(self, text):
        self._log_received_tts(text)
        try:
            t0 = time.time()
            wav, sr, _ = get_tts().synthesize(text, prompt_codes=get_voice()[0])
            if wav.shape[1] == 0:
                self._json({"error": "no audio", "text": text}, 500)
                return
            interleaved = np.zeros(wav.shape[1] * 2, dtype=np.float32)
            interleaved[0::2] = wav[0]
            interleaved[1::2] = wav[1]
            b64 = base64.b64encode(interleaved.tobytes()).decode("ascii")
            self._json({
                "text": text,
                "voice": get_voice()[1],
                "audio_b64": b64,
                "sample_rate": sr,
                "seconds": round(wav.shape[1] / sr, 2),
                "synthesize_sec": round(time.time() - t0, 2),
            })
        except Exception as e:
            self._json({"error": str(e)}, 500)

    def _sse(self, obj):
        payload = f"data: {json.dumps(obj, ensure_ascii=False)}\n\n".encode("utf-8")
        self.wfile.write(f"{len(payload):X}\r\n".encode("ascii") + payload + b"\r\n")
        self.wfile.flush()

    def _tts_stream(self, text):
        """SSE streaming TTS: audio chunks are emitted as the model decodes them."""
        self._log_received_tts(text)
        try:
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream; charset=utf-8")
            self.send_header("Cache-Control", "no-cache")
            self.send_header("Transfer-Encoding", "chunked")
            self._cors()
            self.end_headers()
            t0 = time.time()
            codes, voice = get_voice()
            self._sse({"type": "start", "text": text, "voice": voice})
            if _tts is None:
                get_tts()
            chunk_frames = int(os.environ.get("FAIRY_TTS_CHUNK_FRAMES", "4"))
            chunk_frames = max(1, min(32, chunk_frames))

            def on_chunk(wav, sr):
                b64 = base64.b64encode(np.ascontiguousarray(wav).tobytes()).decode("ascii")
                self._sse({"type": "audio", "sample_rate": sr, "channels": 2, "audio_b64": b64})

            frames = get_tts().synthesize_stream(
                text,
                prompt_codes=codes,
                chunk_frames=chunk_frames,
                on_chunk=on_chunk,
            )
            self._sse({"type": "done", "text": text, "frames": frames, "synthesize_sec": round(time.time() - t0, 2)})
        except Exception as e:
            try:
                self._sse({"type": "error", "error": str(e)})
            except Exception:
                pass
        finally:
            try:
                self.wfile.write(b"0\r\n\r\n")
                self.wfile.flush()
            except Exception:
                pass


def _extract_after_wake(text, wake_words):
    """Return the meaningful command segment around any wake word, or None.

    Whisper sometimes emits the wake word before the command and sometimes
    repeats it after the command. We therefore remove every wake-word span and
    choose the longest remaining text segment instead of blindly taking the
    text after the first match.
    """
    normalized = normalize_text(text)
    if not normalized:
        return None
    lowered = normalized.lower()
    matches = []
    for raw_word in wake_words:
        word = normalize_text(raw_word)
        if not word:
            continue
        if word.isascii():
            pattern = re.compile(
                rf"(?<![a-z]){re.escape(word.lower())}(?![a-z])",
                re.IGNORECASE,
            )
            for match in pattern.finditer(lowered):
                matches.append((match.start(), match.end()))
        else:
            start = 0
            while True:
                index = normalized.find(word, start)
                if index < 0:
                    break
                matches.append((index, index + len(word)))
                start = index + len(word)
    if not matches:
        return None

    matches.sort()
    merged = []
    for start, end in matches:
        if merged and start <= merged[-1][1]:
            merged[-1] = (merged[-1][0], max(merged[-1][1], end))
        else:
            merged.append((start, end))

    segments = []
    cursor = 0
    for start, end in merged:
        if start > cursor:
            segments.append(normalized[cursor:start])
        cursor = end
    if cursor < len(normalized):
        segments.append(normalized[cursor:])

    filler = re.compile(
        r"^(?:wait|hey|ok|okay|um|uh|well|嗯|呃|啊|那个|你好|喂)[\s,，。.!！?？:：;；\-—]*",
        re.IGNORECASE,
    )
    candidates = []
    for segment in segments:
        value = segment.strip(" \t\r\n,，。.!！?？:：;；-—")
        while value:
            cleaned = filler.sub("", value, count=1).strip(" \t\r\n,，。.!！?？:：;；-—")
            if cleaned == value:
                break
            value = cleaned
        if value:
            candidates.append(value)
    if not candidates:
        return ""
    return max(
        candidates,
        key=lambda value: len(re.sub(r"[^A-Za-z0-9\u3400-\u9fff]", "", value)),
    )


def _audio_rms(samples):
    audio = np.asarray(samples, dtype=np.float32)
    if audio.size == 0:
        return 0.0
    return float(np.sqrt(np.mean(np.square(audio))))


def _pcm16_bytes(samples):
    audio = np.asarray(samples, dtype=np.float32)
    return (np.clip(audio, -1.0, 1.0) * 32767.0).astype("<i2").tobytes()


async def handle_stt(ws):
    print("[voice] STT client connected", flush=True)
    try:
        await ws.send(json.dumps({"type": "status", "status": "loading"}, ensure_ascii=False))
        stt = await asyncio.to_thread(get_stt)
        partial_session = None
        partial_backend = os.environ.get("FAIRY_STT_PARTIAL_BACKEND", "sherpa").strip().lower()
        if partial_backend in ("sherpa", "auto"):
            try:
                partial_session = await asyncio.to_thread(get_sherpa_partial)
                partial_session = partial_session.new_session()
            except Exception as partial_error:
                partial_session = None
                print(f"[voice] Sherpa partial unavailable, using Whisper partial: {partial_error}", flush=True)
        session = HybridStreamingSession(stt.new_session(), partial_session)
        await ws.send(json.dumps({"type": "status", "status": "ready"}, ensure_ascii=False))
    except Exception as e:
        print("[voice] stt init error:", e, flush=True)
        try:
            await ws.send(json.dumps({"type": "error", "error": str(e)}, ensure_ascii=False))
        except Exception:
            pass
        return

    mode = "command"
    wake_words = list(DEFAULT_WAKE_WORDS)
    partial_enabled = os.environ.get("FAIRY_STT_PARTIAL_ENABLED", "1").strip().lower() in ("1", "true", "yes", "on")
    silence_reset_s = float(os.environ.get("FAIRY_WAKE_SILENCE_RESET", "30"))
    command_silence_s = float(
        os.environ.get("FAIRY_WAKE_COMMAND_SILENCE", str(DEFAULT_COMMAND_SILENCE_SEC))
    )
    command_max_s = float(
        os.environ.get("FAIRY_WAKE_COMMAND_MAX_SEC", str(DEFAULT_COMMAND_MAX_SEC))
    )
    command_start_timeout_s = float(os.environ.get("FAIRY_WAKE_COMMAND_START_TIMEOUT", "4.0"))
    voice_rms_threshold = float(os.environ.get("FAIRY_STT_VAD_RMS", "0.006"))
    vad = webrtcvad.Vad(2) if webrtcvad is not None else None
    vad_frame_size = int(SAMPLE_RATE * 0.03)
    vad_buffer = np.zeros(0, dtype=np.float32)
    paused = False
    wake_required = False
    command_started = False
    command_text = ""
    wake_at = 0.0
    last_voice_at = time.monotonic()
    last_listen_preview = ""
    last_heard_text = ""
    last_heard_at = 0.0
    utterance_has_voice = False
    utterance_started_at = 0.0
    conversation_active = False

    async def send(payload):
        await ws.send(json.dumps(payload, ensure_ascii=False))

    async def arm_wake():
        nonlocal paused, wake_required, command_started, command_text, wake_at, last_voice_at, last_listen_preview, last_heard_text, last_heard_at, utterance_has_voice, utterance_started_at, conversation_active, vad_buffer
        session.reset()
        paused = False
        wake_required = mode == "wake"
        conversation_active = False
        command_started = False
        command_text = ""
        wake_at = 0.0
        last_voice_at = time.monotonic()
        last_listen_preview = ""
        last_heard_text = ""
        last_heard_at = 0.0
        utterance_has_voice = False
        utterance_started_at = 0.0
        vad_buffer = np.zeros(0, dtype=np.float32)
        if mode == "wake":
            await send({"type": "wake_state", "state": "waiting"})

    async def continue_conversation():
        nonlocal paused, wake_required, command_started, command_text, wake_at, last_voice_at, last_listen_preview, last_heard_text, last_heard_at, utterance_has_voice, utterance_started_at, conversation_active, vad_buffer
        session.reset()
        paused = False
        wake_required = False
        conversation_active = True
        command_started = False
        command_text = ""
        wake_at = 0.0
        last_voice_at = time.monotonic()
        last_listen_preview = ""
        last_heard_text = ""
        last_heard_at = 0.0
        utterance_has_voice = False
        utterance_started_at = 0.0
        vad_buffer = np.zeros(0, dtype=np.float32)
        if mode == "wake":
            await send({"type": "wake_state", "state": "conversation"})

    async for raw in ws:
        try:
            if isinstance(raw, bytes):
                arr = np.frombuffer(raw, dtype=np.float32)
                if arr.size == 0 or paused:
                    continue
                now = time.monotonic()
                energy_level = _audio_rms(arr)
                if vad is not None:
                    vad_buffer = np.concatenate((vad_buffer, arr))
                    voiced = False
                    while vad_buffer.size >= vad_frame_size:
                        frame = vad_buffer[:vad_frame_size]
                        vad_buffer = vad_buffer[vad_frame_size:]
                        if vad.is_speech(_pcm16_bytes(frame), SAMPLE_RATE):
                            voiced = True
                    # WebRTC VAD can occasionally reject quiet-but-real speech.
                    if not voiced and energy_level >= max(voice_rms_threshold * 4.0, 0.02):
                        voiced = True
                else:
                    voiced = energy_level >= voice_rms_threshold
                session.add_audio(arr)
                if voiced:
                    last_voice_at = now
                    if not utterance_started_at:
                        utterance_started_at = now
                    utterance_has_voice = True
                    if mode == "wake" and not wake_required:
                        command_started = True

                # faster-whisper is not a streaming model. While waiting for
                # the wake word, keep the decoding window bounded to one short
                # utterance; otherwise long noise/music makes Whisper repeat
                # hallucinated phrases such as "作曲作曲作曲".
                if partial_enabled and mode == "wake" and wake_required and session.sample_count:
                    if now - last_voice_at >= 1.2 or session.sample_count >= SAMPLE_RATE * 5:
                        session.reset()
                        last_listen_preview = ""
                        last_heard_text = ""
                        last_heard_at = 0.0
                        continue

                # Long idle: forget the previous turn and require the wake word again.
                if (
                    mode == "wake"
                    and (wake_required or conversation_active)
                    and now - last_voice_at >= silence_reset_s
                ):
                    await arm_wake()
                    await send({"type": "reset", "reason": "silence_timeout"})
                    continue

                # Wake word alone, but no command followed it.
                if (
                    mode == "wake"
                    and not wake_required
                    and not command_started
                    and wake_at
                    and now - wake_at >= command_start_timeout_s
                ):
                    await arm_wake()
                    continue

                # Lightweight mode: faster-whisper is only called once after
                # the VAD has confirmed that the whole utterance ended.
                if (
                    not partial_enabled
                    and utterance_has_voice
                    and session.sample_count
                    and command_endpoint_reached(
                        now,
                        last_voice_at,
                        utterance_started_at,
                        command_silence_s,
                        command_max_s,
                    )
                ):
                    final_text = normalize_text(await asyncio.to_thread(session.finalize))
                    if final_text:
                        if wake_required:
                            remainder = _extract_after_wake(final_text, wake_words)
                            if remainder is None:
                                await send({"type": "listen_partial", "text": final_text})
                            elif remainder:
                                wake_required = False
                                wake_at = now
                                command_started = True
                                command_text = remainder
                                await send({"type": "wake"})
                                await send({"type": "partial", "text": remainder})
                                await send({"type": "final", "text": remainder})
                                await continue_conversation()
                                continue
                            else:
                                wake_required = False
                                conversation_active = True
                                wake_at = now
                                command_started = False
                                command_text = ""
                                await send({"type": "wake"})
                        else:
                            remainder = _extract_after_wake(final_text, wake_words)
                            command = (remainder if remainder is not None else final_text).strip()
                            if command:
                                await send({"type": "partial", "text": command})
                                await send({"type": "final", "text": command})
                            await continue_conversation()
                            continue

                    session.reset()
                    command_started = False
                    command_text = ""
                    last_listen_preview = ""
                    last_heard_text = ""
                    last_heard_at = 0.0
                    utterance_has_voice = False
                    last_voice_at = now
                    continue

                # End the command after a short silence once speech has started.
                if (
                    partial_enabled
                    and mode == "wake"
                    and not wake_required
                    and command_started
                    and command_endpoint_reached(
                        now,
                        last_voice_at,
                        utterance_started_at,
                        command_silence_s,
                        command_max_s,
                    )
                ):
                    final_text = await asyncio.to_thread(session.finalize)
                    remainder = _extract_after_wake(final_text, wake_words)
                    command = (remainder if remainder is not None else command_text).strip()
                    if not command and last_heard_text and now - last_heard_at <= 8.0:
                        command = last_heard_text.strip()
                    if command:
                        await send({"type": "final", "text": command})
                    await continue_conversation()
                    continue

                # Do not run Whisper on long silence/noise; this is what
                # causes repeated hallucinated phrases on pseudo-streaming.
                if not partial_enabled:
                    continue

                if now - last_voice_at > 1.4:
                    continue

                if not session.should_transcribe_partial():
                    continue

                text = await asyncio.to_thread(session.transcribe_partial)
                if not text:
                    continue

                if mode == "wake" and wake_required:
                    remainder = _extract_after_wake(text, wake_words)
                    if remainder is None:
                        # Give the user live feedback that STT is hearing them,
                        # without treating ambient speech as a command.
                        if text != last_listen_preview:
                            last_listen_preview = text
                            await send({"type": "listen_partial", "text": text})
                        continue
                    wake_required = False
                    wake_at = now
                    command_started = bool(remainder)
                    command_text = remainder
                    await send({"type": "wake"})
                    if remainder:
                        await send({"type": "partial", "text": remainder})
                    continue

                if mode == "wake":
                    remainder = _extract_after_wake(text, wake_words)
                    if remainder is not None:
                        command_text = remainder
                        if remainder:
                            command_started = True
                    elif not command_text:
                        command_text = text
                    if command_text:
                        command_started = True
                        await send({"type": "partial", "text": command_text})
                else:
                    await send({"type": "partial", "text": text})
            else:
                msg = json.loads(raw)
                message_type = msg.get("type")
                if message_type == "config":
                    mode = str(msg.get("mode") or "command")
                    configured_words = msg.get("wakeWords")
                    if isinstance(configured_words, list):
                        wake_words = [
                            str(word) for word in configured_words
                            if str(word).strip()
                        ] or list(DEFAULT_WAKE_WORDS)
                    silence_reset_s = max(
                        5.0,
                        float(msg.get("silenceResetSec", silence_reset_s)),
                    )
                    command_silence_s = max(
                        2.0,
                        float(msg.get("commandSilenceSec", command_silence_s)),
                    )
                    if "context" in msg:
                        session.context = str(msg.get("context") or "")[:2000]
                    await arm_wake()
                elif message_type == "context":
                    session.context = str(msg.get("text") or msg.get("context") or "")[:2000]
                elif message_type == "pause":
                    await arm_wake()
                    paused = True
                elif message_type == "resume":
                    if paused:
                        paused = False
                        last_voice_at = time.monotonic()
                    if mode == "wake":
                        await send({"type": "wake_state", "state": "waiting"})
                elif message_type == "reset":
                    await arm_wake()
                elif message_type == "end":
                    final_text = await asyncio.to_thread(session.finalize)
                    if not final_text:
                        final_text = session.last_partial_text
                    if mode == "wake":
                        remainder = _extract_after_wake(final_text, wake_words)
                        final_text = (
                            remainder
                            if remainder is not None
                            else (command_text or (final_text if not wake_required else ""))
                        )
                    await send({"type": "final", "text": final_text.strip()})
                    await arm_wake()
        except Exception as e:
            print("[voice] stt error:", e, flush=True)
            try:
                await ws.send(json.dumps({"type": "error", "error": str(e)}, ensure_ascii=False))
            except Exception:
                pass


def run_http():
    srv = ThreadingHTTPServer(("127.0.0.1", VOICE_PORT), HTTPHandler)
    srv.serve_forever()


async def main():
    print(f"[voice] HTTP :{VOICE_PORT}  WS :{VOICE_WS_PORT}", flush=True)
    threading.Thread(target=run_http, daemon=True).start()
    threading.Thread(target=get_stt, daemon=True, name="fairy-stt-preload").start()
    async with __import__("websockets").serve(handle_stt, "127.0.0.1", VOICE_WS_PORT, max_size=4_000_000):
        await asyncio.Future()


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        pass
