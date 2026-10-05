"""入站语音按服务端转写文本处理。

背景：ilink 的入站 item 里，语音走 ITEM_VOICE，转写结果放在 voice_item.text。
原来 extract_text 只读 text_item，语音因此变成空文本，被 handle() 直接丢弃，
表现为「给 bot 发语音完全没有回应」。
"""

import pathlib
import tempfile

import pytest

from wechatbot.bot import Bridge
from wechatbot.config import Account, ChatSettings
from wechatbot.ilink import ITEM_IMAGE, ITEM_VOICE, ILinkClient


def _msg(*items, message_id="m1", user_id="u1"):
    return {
        "message_id": message_id,
        "from_user_id": user_id,
        "item_list": list(items),
    }


def _voice(text=None, duration=3200):
    voice = {"duration": duration}
    if text is not None:
        voice["text"] = text
    return {"type": ITEM_VOICE, "voice_item": voice}


# --- 正例 -------------------------------------------------------------------

def test_voice_transcript_becomes_text():
    text = ILinkClient.extract_text(_msg(_voice("帮我看下今天的日程")))

    assert text == "帮我看下今天的日程"


def test_voice_transcript_joins_plain_text_in_one_message():
    text = ILinkClient.extract_text(
        _msg(
            {"type": 1, "text_item": {"text": "早上好"}},
            _voice("再帮我泡杯茶"),
        )
    )

    assert text == "早上好\n再帮我泡杯茶"


def test_voice_transcript_reaches_fairy(monkeypatch):
    """转写必须真的送进对话，而不只是 extract_text 自己看着对。"""
    sent = _run_with_capture(monkeypatch, _msg(_voice("帮我看下今天的日程")))

    assert sent == "帮我看下今天的日程"


def test_voice_without_transcript_still_gets_a_turn(monkeypatch):
    """转写为空也不能静默——对着黑洞说话比报错更难排查。"""
    sent = _run_with_capture(monkeypatch, _msg(_voice(text=None)))

    assert sent, "无转写的语音应该仍然产生一次对话，而不是被丢弃"
    assert "语音" in sent


# --- 反例：原有路径不受影响 -------------------------------------------------

def test_plain_text_is_untouched():
    text = ILinkClient.extract_text(_msg({"type": 1, "text_item": {"text": "今天天气怎么样"}}))

    assert text == "今天天气怎么样"


def test_plain_text_reaches_fairy_unchanged(monkeypatch):
    sent = _run_with_capture(monkeypatch, _msg({"type": 1, "text_item": {"text": "今天天气怎么样"}}))

    assert sent == "今天天气怎么样"


def test_image_still_produces_no_text():
    text = ILinkClient.extract_text(_msg({"type": ITEM_IMAGE, "image_item": {"url": "x"}}))

    assert text == ""


def test_image_placeholder_is_not_rewritten(monkeypatch):
    """图片仍然走「[图片]」占位，不会被误标成语音。"""
    sent = _run_with_capture(
        monkeypatch,
        _msg(
            {
                "type": ITEM_IMAGE,
                "image_item": {
                    "media": {"encrypt_query_param": "p"},
                    "aeskey": "k",
                },
            }
        ),
    )

    assert sent == "[图片]"
    assert "语音" not in sent


def test_voice_item_without_text_key_is_tolerated():
    assert ILinkClient.extract_text(_msg({"type": ITEM_VOICE, "voice_item": {}})) == ""


def test_item_without_payload_does_not_raise():
    assert ILinkClient.extract_text(_msg({"type": ITEM_VOICE})) == ""
    assert ILinkClient.extract_text(_msg({"type": ITEM_VOICE, "voice_item": None})) == ""
    assert ILinkClient.extract_text({}) == ""


# --- 脚手架 -----------------------------------------------------------------

def _run_with_capture(monkeypatch, msg):
    """跑通 handle() 到把文本交给 Fairy 那一步，返回实际送出的文本。

    真正的 run_turn 会被替换掉：这里要断言的是「Fairy 收到了什么」，
    不该真的发一次 HTTP。
    """
    import wechatbot.bot as bot_mod

    captured = {}

    class _Result:
        text = "收到"
        completed = True
        files = []

    async def fake_run_turn(session, text, settings, user_id, files=None):
        captured["text"] = text
        return _Result()

    monkeypatch.setattr(bot_mod, "run_turn", fake_run_turn)
    monkeypatch.setattr(bot_mod, "remember_context", lambda *a, **k: None)

    tmp = pathlib.Path(tempfile.mkdtemp())
    # bot.py 在函数体内 `from .state import state_path`，且该函数只按 label
    # 解析到真实配置目录。测试必须把 wechatbot.state.state_path 接到临时目录，
    # 否则每跑一次都会往 ~/.config/fairy/wechat/ 里写一份样本文件。
    import wechatbot.state as state_mod

    monkeypatch.setattr(
        state_mod, "state_path", lambda label, directory=None: tmp / f"{label}.state.json"
    )

    account = Account(bot_token="test-token", label="test")
    settings = ChatSettings(ack_delay_seconds=0.0, allow_users=("u1",), reply_chunk_size=200)

    class _Client:
        def download_media(self, param, aeskey):
            return b"fake-image-bytes"

    bridge = Bridge.__new__(Bridge)
    bridge.account = account
    bridge.settings = settings
    bridge.client = _Client()
    bridge.state_dir = tmp
    bridge._seen = set()
    bridge._contexts = {}
    bridge._locks = {}
    bridge.reply = lambda user_id, texts: None
    # 图片附件不落真盘，测试只需要它「存在」。
    bridge.write_attachment = lambda msg, data, index: tmp / f"img{index}.jpg"

    bridge.handle(msg)
    return captured.get("text")
