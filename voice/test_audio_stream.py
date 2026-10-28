import shutil
import subprocess
import tempfile
import unittest

import numpy as np

from audio_stream import PCMDebugCapture, StreamingAudioDecoder


@unittest.skipUnless(shutil.which("ffmpeg"), "ffmpeg is not installed")
class StreamingAudioDecoderTests(unittest.TestCase):
    def test_streaming_opus_frames_decode_to_pcm(self):
        encoded = subprocess.run(
            [
                "ffmpeg",
                "-hide_banner",
                "-loglevel",
                "error",
                "-f",
                "lavfi",
                "-i",
                "sine=frequency=440:sample_rate=48000:duration=0.4",
                "-ac",
                "1",
                "-c:a",
                "libopus",
                "-b:a",
                "24k",
                "-f",
                "webm",
                "pipe:1",
            ],
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=True,
        ).stdout
        decoder = StreamingAudioDecoder()
        try:
            decoder.feed(encoded)
        finally:
            decoder.close()
        samples = decoder.read(timeout=0.1)

        self.assertGreaterEqual(len(samples), 5_000)


class PCMDebugCaptureTests(unittest.TestCase):
    def test_capture_writes_a_readable_wav(self):
        with tempfile.TemporaryDirectory() as directory:
            capture = PCMDebugCapture(directory=directory)
            capture.write(np.linspace(-0.2, 0.2, 1_600, dtype=np.float32))
            capture.close()
            self.assertTrue(capture.path.is_file())
            self.assertGreater(capture.path.stat().st_size, 44)


if __name__ == "__main__":
    unittest.main()
