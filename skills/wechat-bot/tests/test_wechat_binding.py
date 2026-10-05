"""Channel binding, silent refusal and busy retry for the WeChat bridge."""
import asyncio

import pytest

from wechatbot.fairy_client import (
    BUSY_RETRY_DELAYS,
    ChannelUnbound,
    SessionBusy,
    build_chat_body,
    classify_error,
    run_turn,
)


def _settings():
    from wechatbot.config import ChatSettings

    return ChatSettings(api_base="http://127.0.0.1:5173", model="m", turn_timeout_seconds=5)


# --- request body ----------------------------------------------------------

def test_a_wechat_turn_names_the_wechat_account_it_came_from():
    body = build_chat_body("s", "在吗", "m", "wx-user-9")
    assert body["channel"] == {"name": "wechat", "conversation_id": "wx-user-9"}


def test_without_a_conversation_id_no_channel_is_reported():
    # Leaving it out is how the bridge used to call Fairy, and the server then
    # treated it as the machine owner. It must not be the default any more.
    assert "channel" not in build_chat_body("s", "在吗", "m", "")


def test_a_blank_conversation_id_is_not_reported():
    assert "channel" not in build_chat_body("s", "在吗", "m", "   ")


# --- error classification --------------------------------------------------

def test_unbound_is_told_apart_from_busy():
    assert isinstance(classify_error(403, '{"ok":false,"code":"unbound"}'), ChannelUnbound)
    assert isinstance(classify_error(409, '{"ok":false,"retry":true}'), SessionBusy)


def test_an_unrelated_failure_is_ordinary():
    error = classify_error(500, "boom")
    assert not isinstance(error, (ChannelUnbound, SessionBusy))


# --- run_turn behaviour ----------------------------------------------------

def test_run_turn_waits_and_retries_a_busy_session(monkeypatch):
    calls = []

    async def fake_post(settings, body):
        calls.append(body)
        if len(calls) <= 2:
            raise SessionBusy("busy")
        return "done"

    monkeypatch.setattr("wechatbot.fairy_client._post_chat", fake_post)
    slept = []

    async def fake_sleep(seconds):
        slept.append(seconds)

    result = asyncio.run(run_turn("s", "在吗", _settings(), "wx-user-9", fake_sleep))
    assert result == "done"
    assert slept == [3, 3]


def test_run_turn_gives_up_after_five_retries(monkeypatch):
    calls = []

    async def fake_post(settings, body):
        calls.append(body)
        raise SessionBusy("busy")

    monkeypatch.setattr("wechatbot.fairy_client._post_chat", fake_post)

    async def fake_sleep(seconds):
        return None

    with pytest.raises(SessionBusy):
        asyncio.run(run_turn("s", "在吗", _settings(), "wx-user-9", fake_sleep))
    assert len(calls) == len(BUSY_RETRY_DELAYS) + 1


def test_an_unbound_account_is_not_retried(monkeypatch):
    calls = []

    async def fake_post(settings, body):
        calls.append(body)
        raise ChannelUnbound("unbound")

    monkeypatch.setattr("wechatbot.fairy_client._post_chat", fake_post)
    with pytest.raises(ChannelUnbound):
        asyncio.run(run_turn("s", "在吗", _settings(), "wx-user-9"))
    # Retrying would be pointless and would just be noise in the log.
    assert len(calls) == 1
