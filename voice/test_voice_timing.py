import unittest

from stt_engine import (
    DEFAULT_COMMAND_SILENCE_SEC,
    command_endpoint_reached,
    partial_endpoint_ready,
    partial_decode_ready,
    wake_scan_window_complete,
)


class VoiceTimingTests(unittest.TestCase):
    def test_partial_text_can_update_after_450ms_of_new_audio(self):
        self.assertFalse(
            partial_decode_ready(
                sample_count=7_000,
                last_partial_samples=0,
                now=10.0,
                last_partial_at=9.5,
            )
        )
        self.assertTrue(
            partial_decode_ready(
                sample_count=7_200,
                last_partial_samples=0,
                now=10.0,
                last_partial_at=9.5,
            )
        )

    def test_command_waits_for_full_natural_pause(self):
        self.assertFalse(
            command_endpoint_reached(
                now=10.0,
                last_voice_at=10.0 - 1.5,
                utterance_started_at=2.0,
            )
        )
        self.assertFalse(
            command_endpoint_reached(
                now=10.0,
                last_voice_at=10.0 - DEFAULT_COMMAND_SILENCE_SEC + 0.05,
                utterance_started_at=2.0,
            )
        )
        self.assertTrue(
            command_endpoint_reached(
                now=10.0,
                last_voice_at=10.0 - DEFAULT_COMMAND_SILENCE_SEC - 0.01,
                utterance_started_at=2.0,
            )
        )

    def test_partial_wake_scan_does_not_reset_before_the_tail_settles(self):
        self.assertFalse(
            wake_scan_window_complete(
                now=10.0,
                last_voice_at=8.8,
                sample_count=16_000,
            )
        )
        self.assertTrue(
            wake_scan_window_complete(
                now=10.0,
                last_voice_at=8.0,
                sample_count=16_000,
            )
        )

    def test_endpoint_waits_while_partial_text_is_still_growing(self):
        self.assertFalse(
            partial_endpoint_ready(
                now=10.0,
                last_voice_at=7.5,
                utterance_started_at=3.0,
                partial_changed_at=9.8,
            )
        )
        self.assertTrue(
            partial_endpoint_ready(
                now=10.0,
                last_voice_at=7.5,
                utterance_started_at=3.0,
                partial_changed_at=9.0,
            )
        )


if __name__ == "__main__":
    unittest.main()
