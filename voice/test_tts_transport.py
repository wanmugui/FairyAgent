import shutil
import unittest

import numpy as np

from tts_transport import encode_tts_mp3


class TTSAudioTransportTests(unittest.TestCase):
    @unittest.skipUnless(shutil.which("ffmpeg"), "ffmpeg is not installed")
    def test_voice_audio_is_compressed_to_playable_mp3(self):
        sample_rate = 48_000
        samples = np.arange(sample_rate // 2, dtype=np.float32) / sample_rate
        mono = 0.12 * np.sin(2 * np.pi * 220 * samples)
        stereo = np.stack([mono, mono])

        mp3 = encode_tts_mp3(stereo, sample_rate)

        self.assertGreater(len(mp3), 0)
        self.assertTrue(mp3.startswith(b"ID3") or mp3[0] == 0xFF)
        self.assertLess(len(mp3), stereo.nbytes // 10)

    def test_empty_audio_is_rejected(self):
        with self.assertRaises(ValueError):
            encode_tts_mp3(np.empty((2, 0), dtype=np.float32), 48_000)


if __name__ == "__main__":
    unittest.main()
