"""Channel binding, unbound refusal and busy retry for the QQ bridge."""
import asyncio
import json
import sys

import pytest

from qqbot.fairy_client import (
    BUSY_RETRY_DELAYS,
    ChannelUnbound,
    SessionBusy,
    build_chat_body,
    build_headers,
    classify_error,
    redeem_bind_code,
    run_turn,
)
from qqbot.policy import C2C, GROUP, parse_bind_command


class _Ctx:
    async def __aenter__(self):
        return self

    async def __aexit__(self, *exc):
        return False


class _FakeAiohttp:
    """Minimal stand-in: aiohttp is imported inside the function under test."""

    ClientTimeout = staticmethod(lambda total=None: None)

    def __init__(self, client_cls):
        self._client_cls = client_cls

    def ClientSession(self, timeout=None):
        outer = self

        class _Session:
            async def __aenter__(self):
                return outer._client_cls()

            async def __aexit__(self, *exc):
                return False

        return _Session()


# --- /bind parsing ---------------------------------------------------------

def test_bind_command_is_recognised_in_private_chat():
    assert parse_bind_command(C2C, "/bind QK7M-2F3P") == "QK7M-2F3P"


def test_bind_command_is_recognised_after_a_group_mention():
    assert parse_bind_command(GROUP, "<@!12345> /bind QK7M-2F3P") == "QK7M-2F3P"
    assert parse_bind_command(GROUP, "<@!12345>  /bind   QK7M-2F3P  ") == "QK7M-2F3P"


def test_bind_command_case_and_trailing_words_do_not_matter():
    assert parse_bind_command(C2C, "/BIND QK7M-2F3P 快好了吗") == "QK7M-2F3P"


def test_a_bare_bind_command_is_still_a_bind_attempt():
    # Returning "" keeps it on the bind path, so the person gets the
    # "generate a code" instruction instead of a confusing model reply.
    assert parse_bind_command(C2C, "/bind") == ""
    assert parse_bind_command(GROUP, "<@!1> /bind") == ""


def test_ordinary_messages_are_not_mistaken_for_bind_commands():
    assert parse_bind_command(C2C, "帮我查一下天气") is None
    assert parse_bind_command(C2C, "/binding 是什么") is None
    assert parse_bind_command(C2C, "请搜索 /bind 相关的资料") is None
    assert parse_bind_command(C2C, "") is None


# --- request body ----------------------------------------------------------

def test_a_channel_turn_reports_the_conversation_and_nothing_else():
    body = build_chat_body("s", "在吗", "m", {"name": "qq", "conversation_id": "openid-A"})
    assert body["channel"] == {"name": "qq", "conversation_id": "openid-A"}
    # No identity field: the bridge must not be able to claim to be anyone.
    assert "session_owner" not in body
    assert "user" not in body


def test_a_plain_web_style_turn_carries_no_channel():
    assert "channel" not in build_chat_body("s", "在吗", "m")


def test_an_empty_conversation_id_is_not_reported_as_a_channel():
    assert "channel" not in build_chat_body("s", "在吗", "m", {"name": "qq", "conversation_id": ""})


def test_the_static_token_is_still_available_as_a_fallback():
    assert build_headers("tok") == {"Authorization": "Bearer tok"}
    assert build_headers("") == {}


# --- error classification --------------------------------------------------

def test_a_403_unbound_means_refuse_without_running_the_model():
    assert isinstance(classify_error(403, '{"ok":false,"code":"unbound"}'), ChannelUnbound)


def test_409_and_the_retry_flag_both_mean_busy():
    assert isinstance(classify_error(409, '{"ok":false,"retry":true}'), SessionBusy)
    assert isinstance(classify_error(200, '{"ok":false,"retry":true}'), SessionBusy)


def test_anything_else_stays_an_ordinary_error():
    error = classify_error(500, "boom")
    assert not isinstance(error, (ChannelUnbound, SessionBusy))
    assert "500" in str(error)


# --- run_turn behaviour ----------------------------------------------------

def _settings():
    from qqbot.config import QQSettings

    return QQSettings(api_base="http://127.0.0.1:5173", model="m", turn_timeout_seconds=5)


def test_run_turn_retries_a_busy_session_before_handing_it_to_the_insert_path(monkeypatch):
    """忙的短暂重试仍要保留，但只到能区分「刚收尾」和「真在跑」为止。

    真正的插入由 bot.py 在 SessionBusy 之后走 /api/chat/inject；这里等太久
    只会把主人的消息拖到十秒后才开始处理。
    """
    calls = []

    async def fake_post(settings, body, headers):
        calls.append(body)
        if len(calls) <= 3:
            raise SessionBusy("busy")
        return "done"

    monkeypatch.setattr("qqbot.fairy_client._post_chat", fake_post)
    slept = []

    async def fake_sleep(seconds):
        slept.append(seconds)

    result = asyncio.run(run_turn("s", "在吗", _settings(), "", {"name": "qq", "conversation_id": "o"}, fake_sleep))
    assert result == "done"
    assert len(calls) == 4
    assert slept == list(BUSY_RETRY_DELAYS)
    assert sum(BUSY_RETRY_DELAYS) <= 10, "总等待必须短到让人感觉是「插入」而不是「排队」"


def test_run_turn_gives_up_after_five_retries(monkeypatch):
    calls = []

    async def fake_post(settings, body, headers):
        calls.append(body)
        raise SessionBusy("busy")

    monkeypatch.setattr("qqbot.fairy_client._post_chat", fake_post)
    slept = []

    async def fake_sleep(seconds):
        slept.append(seconds)

    with pytest.raises(SessionBusy):
        asyncio.run(run_turn("s", "在吗", _settings(), "", {"name": "qq", "conversation_id": "o"}, fake_sleep))
    # The first attempt plus BUSY_RETRY_DELAYS retries.
    assert len(calls) == len(BUSY_RETRY_DELAYS) + 1
    assert slept == list(BUSY_RETRY_DELAYS)


def test_run_turn_does_not_retry_an_unbound_conversation(monkeypatch):
    calls = []

    async def fake_post(settings, body, headers):
        calls.append(body)
        raise ChannelUnbound("unbound")

    monkeypatch.setattr("qqbot.fairy_client._post_chat", fake_post)
    with pytest.raises(ChannelUnbound):
        asyncio.run(run_turn("s", "在吗", _settings(), "", {"name": "qq", "conversation_id": "o"}))
    # One attempt: retrying an unbound conversation would just be noise.
    assert len(calls) == 1


def test_a_channel_turn_never_sends_the_static_token(monkeypatch):
    seen = {}

    async def fake_post(settings, body, headers):
        seen["headers"] = headers
        return "done"

    monkeypatch.setattr("qqbot.fairy_client._post_chat", fake_post)
    asyncio.run(run_turn("s", "在吗", _settings(), "robot-token", {"name": "qq", "conversation_id": "o"}))
    assert seen["headers"] == {}


# --- redeem ----------------------------------------------------------------

def test_redeem_sends_the_code_and_the_conversation_it_was_typed_in(monkeypatch):
    seen = {}

    class _Resp(_Ctx):
        status = 200

        async def json(self, content_type=None):
            return {"ok": True, "user": {"id": 1, "username": "harry"}}

    class _Client:
        def post(self, url, json=None, **kw):
            seen["url"] = url
            seen["body"] = json
            return _Resp()

    monkeypatch.setitem(sys.modules, "aiohttp", _FakeAiohttp(_Client))
    out = asyncio.run(redeem_bind_code("QK7M-2F3P", _settings(), "openid-A"))
    assert out == {"ok": True, "user": {"id": 1, "username": "harry"}}
    assert seen["url"].endswith("/api/bind/redeem")
    assert seen["body"] == {"code": "QK7M-2F3P", "channel": "qq", "conversation_id": "openid-A"}
    # The code is only meaningful to the binding table, never a bearer secret.
    assert "Authorization" not in json.dumps(seen["body"])


def test_redeem_reports_a_bad_code_instead_of_raising(monkeypatch):
    class _Resp(_Ctx):
        status = 400

        async def json(self, content_type=None):
            return {"ok": False, "code": "bad_code"}

    monkeypatch.setitem(sys.modules, "aiohttp", _FakeAiohttp(type("C", (), {
        "post": lambda self, url, json=None, **kw: _Resp()
    })))
    assert asyncio.run(redeem_bind_code("QK7M-2F3P", _settings(), "openid-A")) == {
        "ok": False, "code": "bad_code"
    }
