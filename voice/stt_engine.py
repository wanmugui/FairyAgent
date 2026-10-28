"""Local CPU speech-to-text for Fairy.

Faster-Whisper has no native "streaming" API, so this adapter keeps the
incoming 16 kHz PCM buffer and publishes stable partial transcripts at a
controlled interval.  The final transcript is always recomputed from the whole
utterance with a slightly larger beam.
"""

from __future__ import annotations

import json
import os
import re
import threading
import time
from difflib import SequenceMatcher
from dataclasses import dataclass, field
from functools import lru_cache
from pathlib import Path
from typing import Optional

import numpy as np
from opencc import OpenCC
from pypinyin import Style, lazy_pinyin

SAMPLE_RATE = 16_000
DEFAULT_COMMAND_SILENCE_SEC = 2.0
DEFAULT_COMMAND_MAX_SEC = 20.0
DEFAULT_PARTIAL_COALESCE_SEC = 0.45
DEFAULT_PARTIAL_INTERVAL_SEC = 0.35
DEFAULT_PARTIAL_SETTLE_SEC = 0.45
ALLOWED_TEXT_PATTERN = re.compile(
    r"[^\u3400-\u9fff"
    r"A-Za-z0-9"
    r"\s"
    r".,!?;:'\"()\\\-_/，。！？；：、“”‘’《》〈〉【】（）…"
    r"]+"
)
_TRADITIONAL_TO_SIMPLIFIED = OpenCC("t2s")
VOICE_CONFUSIONS_PATH = Path(
    os.environ.get(
        "FAIRY_VOICE_CONFUSIONS",
        str(Path(__file__).resolve().parent.parent / "config" / "voice_corrections.json"),
    )
)
PROTECTED_TEXT_PATTERN = re.compile(
    r"https?://\S+|[A-Za-z]:\\[^\s]+|(?:/[\w.@+-]+){2,}|"
    r"\b\d+(?:\.\d+)?\b|[A-Za-z_][A-Za-z0-9_.-]*"
)


def normalize_text(text: str) -> str:
    text = re.sub(r"\s+", " ", str(text or "")).strip()
    # All speech transcripts are normalized at one boundary. This applies to
    # Sherpa partials, Whisper finals and asynchronous corrections alike.
    text = _TRADITIONAL_TO_SIMPLIFIED.convert(text)
    # Fairy only accepts Chinese and English. Remove other scripts and emoji
    # before the text reaches the wake-word matcher or the agent.
    text = ALLOWED_TEXT_PATTERN.sub("", text)
    text = re.sub(r"\s+", " ", text).strip()
    # Whisper occasionally emits a short n-gram dozens of times on noisy or
    # very short PCM chunks (for example "作词作曲作曲作曲"). Collapse only
    # immediate repeats of 1-8 CJK/Latin characters/words.
    previous = None
    while previous != text:
        previous = text
        for size in range(1, 9):
            text = re.sub(
                rf"([\u3400-\u9fffA-Za-z]{{{size}}})\1{{2,}}",
                r"\1",
                text,
            )
        text = re.sub(r"\b([A-Za-z]+)(?:\s+\1\b){2,}", r"\1", text, flags=re.IGNORECASE)
    # Whisper occasionally inserts spaces between Chinese characters.
    text = re.sub(r"(?<=[\u3400-\u9fff])\s+(?=[\u3400-\u9fff])", "", text)
    return text.strip()


@lru_cache(maxsize=1)
def load_voice_confusions(path: str | None = None) -> dict[str, str]:
    source = Path(path) if path else VOICE_CONFUSIONS_PATH
    try:
        payload = json.loads(source.read_text(encoding="utf-8"))
    except Exception:
        return {}
    if not isinstance(payload, dict):
        return {}
    return {
        normalize_text(key): normalize_text(value)
        for key, value in payload.items()
        if normalize_text(key) and normalize_text(value)
    }


def _apply_confusion_map(text: str, confusion_map: dict[str, str]):
    output = text
    edits = []
    for wrong, right in sorted(confusion_map.items(), key=lambda item: len(item[0]), reverse=True):
        start = 0
        while True:
            index = output.find(wrong, start)
            if index < 0:
                break
            edits.append({
                "op": "replace",
                "start": index,
                "end": index + len(wrong),
                "before": wrong,
                "after": right,
                "reason": "confusion",
            })
            output = output[:index] + right + output[index + len(wrong):]
            start = index + len(right)
    return output, edits


def _pinyin_key(text: str) -> tuple[str, ...]:
    key = []
    for char in text:
        if re.match(r"[\u3400-\u9fff]", char):
            syllables = lazy_pinyin(char, style=Style.NORMAL, errors="ignore")
            key.append(syllables[0] if syllables else char)
        else:
            key.append(char.lower())
    return tuple(key)


def _has_protected_change(before: str, after: str) -> bool:
    if before == after:
        return False
    return bool(PROTECTED_TEXT_PATTERN.search(before) or PROTECTED_TEXT_PATTERN.search(after))


def build_voice_correction(
    partial_text: str,
    corrected_text: str,
    confusion_map: dict[str, str] | None = None,
):
    """Return stable text plus validated, structured Whisper edit operations."""
    partial = normalize_text(partial_text)
    corrected = normalize_text(corrected_text)
    if not partial:
        return corrected, []
    if not corrected:
        return partial, []
    confusions = load_voice_confusions() if confusion_map is None else confusion_map
    partial, edits = _apply_confusion_map(partial, confusions)
    corrected, _ = _apply_confusion_map(corrected, confusions)
    if corrected == partial:
        return partial, edits
    matcher = SequenceMatcher(None, partial, corrected, autojunk=False)
    ratio = matcher.ratio()
    length_delta = len(corrected) - len(partial)
    if ratio < 0.45:
        return partial, edits
    if length_delta < 0:
        return partial, edits
    if length_delta <= 2 and ratio < 0.8:
        return partial, edits
    output = []
    for tag, i1, i2, j1, j2 in matcher.get_opcodes():
        original = partial[i1:i2]
        replacement = corrected[j1:j2]
        if tag == "equal":
            output.append(original)
        elif tag == "replace":
            same_sound = _pinyin_key(original) == _pinyin_key(replacement)
            short_shape_edit = (
                len(original) == len(replacement) == 1
                and SequenceMatcher(None, original, replacement).ratio() >= 0.5
            )
            if (
                not _has_protected_change(original, replacement)
                and (same_sound or short_shape_edit)
            ):
                output.append(replacement)
                edits.append({
                    "op": "replace",
                    "start": i1,
                    "end": i2,
                    "before": original,
                    "after": replacement,
                    "reason": "homophone" if same_sound else "shape",
                })
            else:
                output.append(original)
        elif tag == "insert":
            if (
                not _has_protected_change("", replacement)
                and ((i1 == len(partial) and len(replacement) <= 8) or len(replacement) <= 2)
            ):
                output.append(replacement)
                edits.append({
                    "op": "insert",
                    "start": i1,
                    "end": i1,
                    "before": "",
                    "after": replacement,
                    "reason": "missing_tail" if i1 == len(partial) else "short_insert",
                })
        elif tag == "delete":
            if len(original) <= 1 and not _has_protected_change(original, ""):
                edits.append({
                    "op": "delete",
                    "start": i1,
                    "end": i2,
                    "before": original,
                    "after": "",
                    "reason": "extra_character",
                })
            else:
                output.append(original)
    return "".join(output).strip(), edits


def apply_voice_correction(
    partial_text: str,
    corrected_text: str,
    confusion_map: dict[str, str] | None = None,
) -> str:
    text, _ = build_voice_correction(partial_text, corrected_text, confusion_map)
    return text


def partial_decode_ready(
    sample_count: int,
    last_partial_samples: int,
    now: float,
    last_partial_at: float,
    *,
    coalesce_s: float = DEFAULT_PARTIAL_COALESCE_SEC,
    interval_s: float = DEFAULT_PARTIAL_INTERVAL_SEC,
) -> bool:
    """Keep partial decoding frequent enough for live voice feedback."""
    if sample_count - last_partial_samples < int(SAMPLE_RATE * coalesce_s):
        return False
    return now - last_partial_at >= interval_s


def command_endpoint_reached(
    now: float,
    last_voice_at: float,
    utterance_started_at: float,
    command_silence_s: float = DEFAULT_COMMAND_SILENCE_SEC,
    command_max_s: float = DEFAULT_COMMAND_MAX_SEC,
) -> bool:
    """Return true only after a real pause or a generous long-utterance cap."""
    if command_max_s > 0 and utterance_started_at > 0 and now - utterance_started_at >= command_max_s:
        return True
    return now - last_voice_at >= command_silence_s


def partial_endpoint_ready(
    now: float,
    last_voice_at: float,
    utterance_started_at: float,
    partial_changed_at: float,
    command_silence_s: float = DEFAULT_COMMAND_SILENCE_SEC,
    command_max_s: float = DEFAULT_COMMAND_MAX_SEC,
    partial_settle_s: float = DEFAULT_PARTIAL_SETTLE_SEC,
) -> bool:
    """Only submit after silence and a stable partial hypothesis."""
    if (
        command_max_s > 0
        and utterance_started_at > 0
        and now - utterance_started_at >= command_max_s
    ):
        return True
    if not command_endpoint_reached(
        now,
        last_voice_at,
        utterance_started_at,
        command_silence_s,
        command_max_s=0,
    ):
        return False
    return partial_changed_at > 0 and now - partial_changed_at >= partial_settle_s


def merge_partial_command_text(previous: str, incoming: str) -> str:
    """Merge cumulative or split streaming hypotheses without losing words."""
    before = normalize_text(previous)
    after = normalize_text(incoming)
    if not before:
        return after
    if not after:
        return before
    if after.startswith(before):
        return after
    if before.startswith(after):
        return before
    overlap = min(len(before), len(after))
    while overlap > 0 and before[-overlap:] != after[:overlap]:
        overlap -= 1
    return before + after[overlap:]


def wake_scan_window_complete(
    now: float,
    last_voice_at: float,
    sample_count: int,
    *,
    silence_s: float = DEFAULT_COMMAND_SILENCE_SEC,
    max_samples: int = SAMPLE_RATE * 5,
) -> bool:
    """Reset a wake-only scan only after partial text has had time to settle."""
    return sample_count >= max_samples or now - last_voice_at >= silence_s


def _resolve_model_path(default: str = "base") -> str:
    configured = os.environ.get("FAIRY_STT_MODEL", "").strip()
    if configured:
        return configured

    repo_model = Path(__file__).resolve().parent.parent / "models" / "voice" / "faster-whisper-base"
    if repo_model.is_dir():
        return str(repo_model)
    return default


def _load_hotwords() -> str:
    default = "FAIRY, 绯蕊, 菲蕊, 翡蕊, 绯睿, 菲雅, 绯瑞"
    values = [
        part.strip()
        for part in os.environ.get("FAIRY_STT_HOTWORDS", default).split(",")
        if part.strip()
    ]
    path = Path(__file__).resolve().parent.parent / "config" / "stt_hotwords.txt"
    try:
        for line in path.read_text(encoding="utf-8").splitlines():
            line = line.split("#", 1)[0].strip()
            if line:
                values.append(line)
    except OSError:
        pass
    return ", ".join(dict.fromkeys(values))


def _resolve_language() -> Optional[str]:
    value = os.environ.get("FAIRY_STT_LANGUAGE", "zh").strip()
    return None if value.lower() in ("", "auto", "none") else value


def _env_float(name: str, default: float, minimum: float, maximum: float) -> float:
    try:
        value = float(os.environ.get(name, str(default)))
    except ValueError:
        value = default
    return max(minimum, min(maximum, value))


def longest_common_prefix(left: str, right: str) -> str:
    """Character-level LocalAgreement prefix for Chinese/English partials."""
    limit = min(len(left), len(right))
    index = 0
    while index < limit and left[index] == right[index]:
        index += 1
    return left[:index]


@dataclass
class WhisperStreamingSession:
    """Per-WebSocket PCM buffer backed by a shared Whisper model."""

    engine: "FasterWhisperSTT"
    chunks: list[np.ndarray] = field(default_factory=list)
    sample_count: int = 0
    last_partial_at: float = 0.0
    last_partial_samples: int = 0
    last_partial_text: str = ""
    last_hypothesis: str = ""
    committed_text: str = ""
    context: str = ""  # Dynamic terms/phrases supplied by the active session.

    def reset(self) -> None:
        self.chunks.clear()
        self.sample_count = 0
        self.last_partial_at = 0.0
        self.last_partial_samples = 0
        self.last_partial_text = ""
        self.last_hypothesis = ""
        self.committed_text = ""
        # context intentionally survives reset: it describes the active session.

    def add_audio(self, samples: np.ndarray) -> None:
        audio = np.array(samples, dtype=np.float32, copy=True).reshape(-1)
        if audio.size == 0:
            return
        audio = np.nan_to_num(audio, copy=False)
        self.chunks.append(audio)
        self.sample_count += int(audio.size)

    def _audio(self) -> np.ndarray:
        if not self.chunks:
            return np.zeros(0, dtype=np.float32)
        return np.concatenate(self.chunks)

    def snapshot_audio(self) -> np.ndarray:
        return self._audio().copy()

    def should_transcribe_partial(self) -> bool:
        if self.sample_count < int(SAMPLE_RATE * 0.45):
            return False
        return partial_decode_ready(
            self.sample_count,
            self.last_partial_samples,
            time.monotonic(),
            self.last_partial_at,
            coalesce_s=self.engine.coalesce_s,
            interval_s=self.engine.partial_interval_s,
        )

    def transcribe_partial(self) -> str:
        if not self.should_transcribe_partial():
            return self.last_partial_text
        self.last_partial_at = time.monotonic()
        self.last_partial_samples = self.sample_count
        text = self.engine.transcribe(self._audio(), final=False, context=self.context)
        if text:
            stable = longest_common_prefix(self.last_hypothesis, text) if self.last_hypothesis else ""
            if len(stable) > len(self.committed_text):
                self.committed_text = stable
            if text.startswith(self.committed_text):
                display = text
            else:
                tail = text[len(stable):] if text.startswith(stable) else text
                display = self.committed_text + tail
            self.last_hypothesis = text
            self.last_partial_text = display
        return self.last_partial_text

    def finalize(self) -> str:
        text = self.engine.transcribe(self._audio(), final=True, context=self.context)
        if text:
            self.committed_text = text
            self.last_hypothesis = text
            self.last_partial_text = text
        return text or self.last_partial_text


class SherpaStreamingPartial:
    """Small CPU streaming partial backend built on sherpa-onnx Zipformer."""

    def __init__(self, model_dir: Optional[str] = None, num_threads: Optional[int] = None):
        try:
            import sherpa_onnx
        except ImportError as exc:
            raise RuntimeError("sherpa_onnx is not installed") from exc

        configured = model_dir or os.environ.get("FAIRY_SHERPA_MODEL_DIR", "").strip()
        if configured:
            self.model_dir = Path(configured)
        else:
            self.model_dir = (
                Path(__file__).resolve().parent.parent
                / ".tools"
                / "sherpa-models"
                / "sherpa-onnx-x-asr-480ms-streaming-zipformer-transducer-zh-en-punct-int8-2026-06-05"
            )
        self.num_threads = int(
            num_threads
            if num_threads is not None
            else os.environ.get("FAIRY_SHERPA_NUM_THREADS", "1")
        )
        self.coalesce_s = _env_float(
            "FAIRY_STT_COALESCE_SEC", DEFAULT_PARTIAL_COALESCE_SEC, 0.2, 5.0
        )
        self.partial_interval_s = _env_float(
            "FAIRY_STT_PARTIAL_INTERVAL", DEFAULT_PARTIAL_INTERVAL_SEC, 0.2, 5.0
        )
        self._lock = threading.Lock()

        files = {
            "tokens": self.model_dir / "tokens.txt",
            "encoder": self.model_dir / "encoder.int8.onnx",
            "decoder": self.model_dir / "decoder.onnx",
            "joiner": self.model_dir / "joiner.int8.onnx",
        }
        missing = [name for name, path in files.items() if not path.is_file()]
        if missing:
            raise RuntimeError(
                f"sherpa partial model is incomplete at {self.model_dir}: missing {', '.join(missing)}"
            )
        hotwords_file = os.environ.get("FAIRY_SHERPA_HOTWORDS_FILE", "").strip()
        if hotwords_file and not Path(hotwords_file).is_file():
            hotwords_file = ""
        self.recognizer = sherpa_onnx.OnlineRecognizer.from_transducer(
            tokens=str(files["tokens"]),
            encoder=str(files["encoder"]),
            decoder=str(files["decoder"]),
            joiner=str(files["joiner"]),
            num_threads=max(1, self.num_threads),
            sample_rate=SAMPLE_RATE,
            feature_dim=80,
            decoding_method="greedy_search",
            model_type="zipformer2",
            hotwords_file=hotwords_file,
        )

    def new_session(self) -> "SherpaStreamingSession":
        return SherpaStreamingSession(self)

    def create_stream(self):
        return self.recognizer.create_stream()


class SherpaStreamingSession:
    """Per-WebSocket incremental stream for the small sherpa partial model."""

    def __init__(self, engine: SherpaStreamingPartial):
        self.engine = engine
        self.stream = engine.create_stream()
        self.sample_count = 0
        self.last_partial_samples = 0
        self.last_partial_at = 0.0
        self.last_partial_text = ""
        self.context = ""

    def reset(self) -> None:
        self.stream = self.engine.create_stream()
        self.sample_count = 0
        self.last_partial_samples = 0
        self.last_partial_at = 0.0
        self.last_partial_text = ""

    def add_audio(self, samples: np.ndarray) -> None:
        audio = np.asarray(samples, dtype=np.float32).reshape(-1)
        if audio.size == 0:
            return
        self.stream.accept_waveform(SAMPLE_RATE, audio)
        self.sample_count += int(audio.size)

    def should_transcribe_partial(self) -> bool:
        return partial_decode_ready(
            self.sample_count,
            self.last_partial_samples,
            time.monotonic(),
            self.last_partial_at,
            coalesce_s=self.engine.coalesce_s,
            interval_s=self.engine.partial_interval_s,
        )

    def transcribe_partial(self) -> str:
        with self.engine._lock:
            while self.engine.recognizer.is_ready(self.stream):
                self.engine.recognizer.decode_stream(self.stream)
            text = normalize_text(self.engine.recognizer.get_result(self.stream))
        if text:
            self.last_partial_text = text
        self.last_partial_at = time.monotonic()
        self.last_partial_samples = self.sample_count
        return self.last_partial_text


class HybridStreamingSession:
    """Use sherpa for live partial text and Whisper for the final transcript."""

    def __init__(
        self,
        final_session: WhisperStreamingSession,
        partial_session: Optional[SherpaStreamingSession] = None,
    ):
        self.final_session = final_session
        self.partial_session = partial_session

    @property
    def sample_count(self) -> int:
        return self.final_session.sample_count

    @property
    def last_partial_text(self) -> str:
        if self.partial_session is not None:
            return self.partial_session.last_partial_text
        return self.final_session.last_partial_text

    @property
    def context(self) -> str:
        return self.final_session.context

    @context.setter
    def context(self, value: str) -> None:
        self.final_session.context = value
        if self.partial_session is not None:
            self.partial_session.context = value

    def add_audio(self, samples: np.ndarray) -> None:
        self.final_session.add_audio(samples)
        if self.partial_session is not None:
            self.partial_session.add_audio(samples)

    def reset(self) -> None:
        self.final_session.reset()
        if self.partial_session is not None:
            self.partial_session.reset()

    def should_transcribe_partial(self) -> bool:
        if self.partial_session is not None:
            return self.partial_session.should_transcribe_partial()
        return self.final_session.should_transcribe_partial()

    def transcribe_partial(self) -> str:
        if self.partial_session is not None:
            return self.partial_session.transcribe_partial()
        return self.final_session.transcribe_partial()

    def finalize(self) -> str:
        text = self.final_session.finalize()
        if not text and self.partial_session is not None:
            text = self.partial_session.last_partial_text
        return text

    def snapshot_audio(self) -> np.ndarray:
        return self.final_session.snapshot_audio()


class FasterWhisperSTT:
    """Small thread-safe wrapper around faster-whisper's WhisperModel."""

    def __init__(
        self,
        model_path: Optional[str] = None,
        *,
        device: Optional[str] = None,
        compute_type: Optional[str] = None,
        language: Optional[str] = None,
        partial_interval_s: Optional[float] = None,
    ) -> None:
        from faster_whisper import WhisperModel

        self.model_path = model_path or _resolve_model_path()
        self.device = device or os.environ.get("FAIRY_STT_DEVICE", "cpu")
        self.compute_type = compute_type or os.environ.get(
            "FAIRY_STT_COMPUTE_TYPE",
            "int8" if self.device == "cpu" else "float16",
        )
        self.language = language if language is not None else _resolve_language()
        self.partial_interval_s = float(
            partial_interval_s
            if partial_interval_s is not None
            else os.environ.get("FAIRY_STT_PARTIAL_INTERVAL", "0.75")
        )
        self.coalesce_s = _env_float("FAIRY_STT_COALESCE_SEC", 0.9, 0.2, 5.0)
        self.hotwords = _load_hotwords()
        # The prompt tells the recogniser which words are likely, and it is
        # what keeps the wake word recognisable: without it the recogniser
        # renders 「菲蕊」as 「丁瑞」or 「地里」and the wake match never fires.
        # The list covers the homophones the recogniser actually produces, not
        # just the canonical spelling.
        self.initial_prompt = os.environ.get(
            "FAIRY_STT_INITIAL_PROMPT",
            "Fairy voice assistant wake words: FAIRY, FERRY, FAIRLY, 绯蕊, 菲蕊, 绯瑞, 菲瑞, 飞蕊, 菲雅, 丁蕊, 丁瑞, 蒂蕊, 提蕊.",
        )
        try:
            cpu_threads = max(1, int(os.environ.get("FAIRY_STT_CPU_THREADS", "4")))
        except ValueError:
            cpu_threads = 4

        self.model = WhisperModel(
            self.model_path,
            device=self.device,
            compute_type=self.compute_type,
            cpu_threads=cpu_threads,
            num_workers=1,
        )
        final_model_path = os.environ.get("FAIRY_STT_FINAL_MODEL", "").strip()
        self.final_model = self.model
        if final_model_path and final_model_path != self.model_path:
            self.final_model = WhisperModel(
                final_model_path,
                device=self.device,
                compute_type=self.compute_type,
                cpu_threads=cpu_threads,
                num_workers=1,
            )
        self._lock = threading.Lock()

    def new_session(self) -> WhisperStreamingSession:
        return WhisperStreamingSession(self)

    def transcribe(
        self,
        audio: np.ndarray,
        *,
        final: bool = False,
        context: str = "",
    ) -> str:
        samples = np.asarray(audio, dtype=np.float32).reshape(-1)
        if samples.size < int(SAMPLE_RATE * 0.20):
            return ""

        # Keep the live pass bounded; Whisper's useful context is 30 seconds.
        max_samples = int(SAMPLE_RATE * (60 if final else 30))
        if samples.size > max_samples:
            samples = samples[-max_samples:]

        model = self.final_model if final else self.model
        initial_prompt = self.initial_prompt
        if final and context.strip():
            initial_prompt = (initial_prompt + " " + context.strip()).strip()
        with self._lock:
            segments, _info = model.transcribe(
                samples,
                language=self.language,
                beam_size=5 if final else 1,
                best_of=5 if final else 1,
                repetition_penalty=1.05 if final else 1.15,
                no_repeat_ngram_size=3,
                temperature=0.0,
                vad_filter=True,
                vad_parameters={"min_silence_duration_ms": 280 if final else 180},
                condition_on_previous_text=final,
                initial_prompt=initial_prompt if final else self.initial_prompt,
                hotwords=self.hotwords,
                without_timestamps=True,
                word_timestamps=False,
            )
            text = "".join(segment.text for segment in segments)
        return normalize_text(text)
