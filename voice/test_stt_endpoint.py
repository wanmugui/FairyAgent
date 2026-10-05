import os
import unittest
from types import SimpleNamespace
from unittest.mock import patch

from stt_engine import (
    DEFAULT_COMMAND_MAX_SEC,
    DEFAULT_COMMAND_SILENCE_SEC,
    HybridStreamingSession,
    SAMPLE_RATE,
    WhisperStreamingSession,
    _load_hotwords,
    _resolve_language,
    command_endpoint_reached,
    longest_common_prefix,
)


class CommandEndpointTests(unittest.TestCase):
    def test_short_pause_does_not_end_command(self):
        self.assertFalse(
            command_endpoint_reached(
                now=10.0,
                last_voice_at=8.5,
                utterance_started_at=2.0,
            )
        )

    def test_default_silence_window_ends_command(self):
        self.assertTrue(
            command_endpoint_reached(
                now=10.0,
                last_voice_at=10.0 - DEFAULT_COMMAND_SILENCE_SEC,
                utterance_started_at=2.0,
            )
        )

    def test_eight_second_continuous_speech_is_not_cut_off(self):
        self.assertFalse(
            command_endpoint_reached(
                now=10.0,
                last_voice_at=9.9,
                utterance_started_at=2.0,
            )
        )

    def test_long_utterance_safety_cap_still_fires(self):
        self.assertTrue(
            command_endpoint_reached(
                now=30.0,
                last_voice_at=29.9,
                utterance_started_at=30.0 - DEFAULT_COMMAND_MAX_SEC,
            )
        )


class STTConfigTests(unittest.TestCase):
    def test_auto_language_disables_forced_language(self):
        with patch.dict(os.environ, {"FAIRY_STT_LANGUAGE": "auto"}, clear=False):
            self.assertIsNone(_resolve_language())

    def test_hotword_environment_is_loaded(self):
        with patch.dict(
            os.environ,
            {"FAIRY_STT_HOTWORDS": "RsuSer, 绯蕊"},
            clear=False,
        ):
            hotwords = _load_hotwords()
        self.assertIn("RsuSer", hotwords)
        self.assertIn("绯蕊", hotwords)

    def test_local_agreement_prefix(self):
        self.assertEqual(
            longest_common_prefix("请帮我打开侧边栏", "请帮我打开侧边栏和菜单"),
            "请帮我打开侧边栏",
        )
        self.assertEqual(
            longest_common_prefix("打开设置", "打开声音"),
            "打开",
        )

    def test_partial_coalescing_waits_for_enough_new_audio(self):
        engine = SimpleNamespace(coalesce_s=0.9, partial_interval_s=0.75)
        session = WhisperStreamingSession(engine=engine)
        session.sample_count = int(SAMPLE_RATE * 0.8)
        self.assertFalse(session.should_transcribe_partial())
        session.sample_count = int(SAMPLE_RATE * 0.9)
        self.assertTrue(session.should_transcribe_partial())

    def test_dynamic_context_survives_session_reset(self):
        engine = SimpleNamespace(coalesce_s=0.9, partial_interval_s=0.75)
        session = WhisperStreamingSession(engine=engine)
        session.context = "RsuSer, Fairy bridge"
        session.reset()
        self.assertEqual(session.context, "RsuSer, Fairy bridge")

    def test_hybrid_session_uses_sherpa_partial_and_whisper_final(self):
        class FakeSession:
            def __init__(self, name):
                self.name = name
                self.sample_count = 0
                self.context = ""
                self.last_partial_text = ""
                self.reset_count = 0

            def add_audio(self, samples):
                self.sample_count += len(samples)

            def reset(self):
                self.reset_count += 1

            def should_transcribe_partial(self):
                return True

            def transcribe_partial(self):
                return f"{self.name}-partial"

            def finalize(self):
                return "" if self.name == "sherpa" else f"{self.name}-final"

        final_session = FakeSession("whisper")
        partial_session = FakeSession("sherpa")
        hybrid = HybridStreamingSession(final_session, partial_session)
        hybrid.context = "RsuSer"
        hybrid.add_audio([0.1, 0.2])
        self.assertEqual(final_session.sample_count, 2)
        self.assertEqual(partial_session.sample_count, 2)
        self.assertEqual(hybrid.transcribe_partial(), "sherpa-partial")
        self.assertEqual(hybrid.finalize(), "whisper-final")
        hybrid.reset()
        self.assertEqual(final_session.reset_count, 1)
        self.assertEqual(partial_session.reset_count, 1)


if __name__ == "__main__":
    unittest.main()
