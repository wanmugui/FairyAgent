"""Wake-word regression tests for Fairy's actual names."""

import unittest

import voice_service as vs
from stt_engine import normalize_text


class WakeWordMatchingTests(unittest.TestCase):
    def setUp(self):
        self.words = list(vs.DEFAULT_WAKE_WORDS)

    def extract(self, text):
        return vs._extract_after_wake(normalize_text(text), self.words)

    def test_assistant_nickname_is_not_a_wake_word(self):
        for variant in (
            "仙女助手",
            "献女助手",
            "心女助手",
            "先女助手",
            "鲜女助手",
            "纤女助手",
            "仙女",
        ):
            with self.subTest(variant=variant):
                self.assertIsNone(
                    self.extract(variant),
                    f"「{variant}」不是 Fairy 的名字，不能触发唤醒",
                )

    def test_supported_fairy_names_still_wake(self):
        for variant in ("fairy", "fair", "fairly", "绯蕊", "菲雅", "菲亞"):
            with self.subTest(variant=variant):
                self.assertIsNotNone(self.extract(variant))

    def test_command_after_wake_word_is_kept(self):
        self.assertEqual(self.extract("fairy 帮我看配置"), "帮我看配置")
        self.assertEqual(self.extract("fair 帮我看配置"), "帮我看配置")
        self.assertEqual(self.extract("Fairly 帮我看配置"), "帮我看配置")
        self.assertEqual(self.extract("菲雅，你好"), "你好")

    def test_greeting_before_wake_word_is_stripped(self):
        self.assertEqual(self.extract("喂 你好 fairy 帮我看配置"), "帮我看配置")

    def test_plain_greeting_is_not_a_wake_word(self):
        self.assertIsNone(self.extract("你好"))
        self.assertIsNone(self.extract("你好啊今天天气怎么样"))


if __name__ == "__main__":
    unittest.main()
