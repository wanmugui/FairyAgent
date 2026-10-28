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
    apply_voice_correction,
    build_voice_correction,
    longest_common_prefix,
    merge_partial_command_text,
    normalize_text,
)


class CommandEndpointTests(unittest.TestCase):
    def test_short_pause_does_not_end_command(self):
        self.assertFalse(
            command_endpoint_reached(
                now=10.0,
                last_voice_at=9.5,
                utterance_started_at=2.0,
            )
        )

    def test_default_silence_window_ends_command(self):
        self.assertTrue(
            command_endpoint_reached(
                now=10.0,
                last_voice_at=10.0 - DEFAULT_COMMAND_SILENCE_SEC - 0.01,
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
    def test_transcripts_are_always_converted_to_simplified_chinese(self):
        self.assertEqual(
            normalize_text("我們來測試一下劉世輸入 Pasher"),
            "我们来测试一下刘世输入 Pasher",
        )

    def test_short_whisper_result_cannot_replace_a_complete_partial(self):
        partial = "我们来测试一下完整的语音识别句子"
        self.assertEqual(apply_voice_correction(partial, "二的"), partial)

    def test_credible_whisper_correction_can_replace_partial(self):
        self.assertEqual(
            apply_voice_correction(
                "我是菲亚, 欢迎使用",
                "你好我是菲雅, 欢迎使用仙女助手",
            ),
            "你好我是菲雅, 欢迎使用仙女助手",
        )

    def test_patch_engine_returns_structured_edit_operations(self):
        text, edits = build_voice_correction(
            "我是菲亚, 欢迎使用",
            "你好我是菲雅, 欢迎使用仙女助手",
        )
        self.assertEqual(text, "你好我是菲雅, 欢迎使用仙女助手")
        reasons = {edit["reason"] for edit in edits}
        self.assertTrue({"homophone", "confusion"} & reasons)
        self.assertIn("missing_tail", reasons)

    def test_non_homophone_short_replacement_is_rejected(self):
        self.assertEqual(
            apply_voice_correction("请打开设置", "请打开设备"),
            "请打开设置",
        )

    def test_protected_numbers_are_not_changed(self):
        self.assertEqual(
            apply_voice_correction("端口是8081", "端口是8082"),
            "端口是8081",
        )

    def test_custom_confusion_map_is_applied_with_a_structured_edit(self):
        text, edits = build_voice_correction(
            "打开怕舍",
            "打开怕舍",
            confusion_map={"怕舍": "Pasher"},
        )
        self.assertEqual(text, "打开Pasher")
        self.assertEqual(edits[0]["reason"], "confusion")

    def test_large_whisper_rewrite_keeps_partial_text(self):
        partial = "我们来测试一下完整的语音识别句子"
        corrected = "完全不同的另一段很长的识别内容"
        self.assertEqual(apply_voice_correction(partial, corrected), partial)

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

    def test_cumulative_partial_replaces_stale_command_prefix(self):
        self.assertEqual(
            merge_partial_command_text("我", "我想知道深圳今天的天气"),
            "我想知道深圳今天的天气",
        )

    def test_split_partial_segments_are_joined_without_losing_overlap(self):
        self.assertEqual(
            merge_partial_command_text("我想知", "知道深圳今天的天气"),
            "我想知道深圳今天的天气",
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

            def snapshot_audio(self):
                return [0.1, 0.2]

        final_session = FakeSession("whisper")
        partial_session = FakeSession("sherpa")
        hybrid = HybridStreamingSession(final_session, partial_session)
        hybrid.context = "RsuSer"
        hybrid.add_audio([0.1, 0.2])
        self.assertEqual(final_session.sample_count, 2)
        self.assertEqual(partial_session.sample_count, 2)
        self.assertEqual(hybrid.transcribe_partial(), "sherpa-partial")
        self.assertEqual(hybrid.finalize(), "whisper-final")
        self.assertEqual(hybrid.snapshot_audio(), [0.1, 0.2])
        hybrid.reset()
        self.assertEqual(final_session.reset_count, 1)
        self.assertEqual(partial_session.reset_count, 1)


if __name__ == "__main__":
    unittest.main()
