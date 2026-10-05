"""bot.py 关键路径的回归测试：msg_seq 计数、富媒体类型、忙时插入。"""
import asyncio
from pathlib import Path

import botpy
import pytest

from qqbot import bot as bot_module
from qqbot.bot import QQBotService


def _settings(**overrides):
    base = dict(
        enabled=True,
        model="minimax-m3",
        api_base="http://127.0.0.1:8081",
        allow_c2c=("OPENID",),
        allow_groups=("GID",),
        ack_delay_seconds=8.0,
        reply_chunk_size=3500,
        max_passive_replies=5,
        send_files=True,
        max_file_size_mb=20.0,
        turn_timeout_seconds=1800.0,
    )
    base.update(overrides)
    return type("Settings", (), base)()


@pytest.fixture
def service(monkeypatch):
    """一个不连网的 QQBotService。"""
    monkeypatch.setattr(bot_module, "Intents", lambda **_kw: type("I", (), {"value": 1})())
    monkeypatch.setattr(botpy.Client, "__init__", lambda self, *a, **k: None)
    return QQBotService(settings=_settings(), fairy_token="")


@pytest.fixture
def captured_client_init(monkeypatch):
    """替换 botpy.Client.__init__，捕获调用参数，避免触发真实网络逻辑。"""
    captured: dict = {}

    def fake_init(self, *args, **kwargs):
        captured["args"] = args
        captured["kwargs"] = kwargs

    monkeypatch.setattr(botpy.Client, "__init__", fake_init)
    return captured


def test_default_timeout_raised_to_30_seconds(captured_client_init, monkeypatch):
    monkeypatch.setattr(bot_module, "Intents", lambda **_kw: type("I", (), {"value": 1})())
    QQBotService(settings=type("Settings", (), {})(), fairy_token="")
    assert captured_client_init["kwargs"].get("timeout") == 30, (
        "botpy 默认 HTTP 超时是 5 秒 + 超时不重试，"
        "QQ 接口稍慢时回复会被静默丢弃。QQBotService 必须抬到至少 30 秒。"
    )


def test_explicit_timeout_overrides_default(captured_client_init, monkeypatch):
    monkeypatch.setattr(bot_module, "Intents", lambda **_kw: type("I", (), {"value": 1})())
    QQBotService(settings=type("Settings", (), {})(), fairy_token="", timeout=60)
    assert captured_client_init["kwargs"]["timeout"] == 60


def test_msg_seq_is_counted_per_message_not_per_conversation(service):
    """同一条消息的两次回复必须用 seq 1、2；两条不同的消息各自从 1 开始。

    之前每次回复都新建 Sequencer（永远 seq=1），同一条消息的第二条回复被
    「消息被去重，请检查请求msgseq」（40054005）拒收。改成按会话共享又会把
    5 次的被动回复额度在会话级别一次用光，之后整条会话都发不出消息。
    """
    first = service._seq_for("c2c", "OPENID", "MSG-1")
    assert (first.take(), first.take()) == (1, 2)

    second = service._seq_for("c2c", "OPENID", "MSG-2")
    assert second.take() == 1, "不同 msg_id 各从 1 开始是合法的，也必须如此"

    assert service._seq_for("c2c", "OPENID", "MSG-1") is first, "同一条消息必须复用同一个计数器"


def test_passive_reply_budget_is_per_message_not_per_conversation(service):
    """额度是每条 msg_id 的：同一会话里的第二条消息不该被第一条用掉额度。"""
    for index in range(6):
        seq = service._seq_for("c2c", "OPENID", f"MSG-{index}")
        assert [seq.take() for _ in range(5)] == [1, 2, 3, 4, 5]
        assert seq.take() is None, "超过 5 次才算这条消息的额度用尽"


def test_send_images_uses_rich_media_msg_type_and_continues_seq(service, monkeypatch):
    uploaded: list = []
    posted: list = []

    def fake_upload(target, image):
        uploaded.append((target, Path(image).name))
        return "FAKE_FILE_INFO"

    class FakeApi:
        async def post_c2c_message(self, **kw):
            posted.append(("c2c", kw))

        async def post_group_message(self, **kw):
            posted.append(("group", kw))

    monkeypatch.setattr(bot_module, "upload_media", fake_upload)
    img = Path("/tmp/x.png")
    img.write_bytes(b"\x89PNG")
    service.api = FakeApi()

    asyncio.run(service._reply_texts("c2c", "OPENID", "MSG-IMG", ["第一段"]))
    asyncio.run(service._send_images("c2c", "OPENID", "MSG-IMG", [img]))

    assert uploaded == [("c2c:OPENID", "x.png")], "每张图都必须真正走 upload_media 上传"
    assert posted[0] == (
        "c2c",
        {"openid": "OPENID", "msg_type": 0, "content": "第一段",
         "msg_id": "MSG-IMG", "msg_seq": 1},
    )
    assert posted[1][1]["msg_type"] == 7, (
        "botpy 文档：0 文本 / 1 图文混排 / 2 markdown / 3 ark / 4 embed / 7 media。"
        "富媒体图片必须用 7。"
    )
    assert posted[1][1]["msg_seq"] == 2, "图片要接着同一 msg_id 已用的序号，否则撞号被去重"
    assert posted[1][1]["media"] == {"file_info": "FAKE_FILE_INFO"}


def test_transient_send_failure_retries_with_the_same_msg_seq(service, monkeypatch):
    """传输层抖动必须重试，而且沿用同一个 msg_seq。

    线上真实故障：一次 DNS/连接抖动让 seq=2 的回复直接消失，日志只留一条
    「发送文本失败」。被动回复窗口只有 5 分钟，不重试等于把主人的消息丢掉；
    而重试时换 seq 会撞 40054005 去重，所以必须原样重发。
    """
    attempts: list[int] = []

    class FlakyApi:
        async def post_c2c_message(self, **kw):
            attempts.append(kw["msg_seq"])
            if len(attempts) < 3:
                raise bot_module.aiohttp.ClientConnectionError("临时 DNS 抖动")

    async def _no_wait(*_a, **_k):
        return None

    monkeypatch.setattr(bot_module.asyncio, "sleep", _no_wait)
    service.api = FlakyApi()

    asyncio.run(service._reply_texts("c2c", "OPENID", "MSG-DNS", ["到了"]))

    assert attempts == [1, 1, 1], (
        f"重试必须沿用同一个 msg_seq，实际发出 {attempts}：换号会被 40054005 去重拒收"
    )


def test_platform_rejection_is_not_retried(service, monkeypatch):
    """平台已明确拒绝（4xx/5xx）时重试没有意义，只会让后面的消息更晚发出去。"""
    attempts: list[int] = []

    class RejectingApi:
        async def post_c2c_message(self, **kw):
            attempts.append(kw["msg_seq"])
            raise botpy.errors.ServerError({"code": 40054005, "message": "消息被去重"})

    async def _no_wait(*_a, **_k):
        return None

    monkeypatch.setattr(bot_module.asyncio, "sleep", _no_wait)
    service.api = RejectingApi()

    asyncio.run(service._reply_texts("c2c", "OPENID", "MSG-REJ", ["一句话"]))

    assert attempts == [1], f"平台拒绝不该重试，实际尝试 {len(attempts)} 次"


def test_busy_backend_inserts_into_the_running_turn(service, monkeypatch):
    """后端 409（网页端正在跑同一个主会话）时必须插入，而不是回「稍后再发」。

    主人今天就是这样被丢了三条消息：消息既没进主会话，也没进队列。
    """
    injected: dict = {}
    followed: dict = {}
    sent: list[str] = []

    async def fake_run_turn(*_args, **_kwargs):
        raise bot_module.SessionBusy("busy")

    async def fake_inject(session, text, settings, token="", channel=None):
        injected.update(session=session, text=text, channel=channel)
        return {"ok": True, "session": "2026-10-01", "run_id": "RUN-1"}

    async def fake_follow(session, settings, token="", channel=None, run_id="", since=0, **kw):
        followed.update(session=session, channel=channel, run_id=run_id)
        return bot_module.TurnResult(text="答案是 42", files=[], completed=True)

    async def fake_reply(kind, conversation_id, msg_id, texts, **kwargs):
        sent.extend(texts)

    monkeypatch.setattr(bot_module, "run_turn", fake_run_turn)
    monkeypatch.setattr(bot_module, "inject_message", fake_inject)
    monkeypatch.setattr(bot_module, "follow_turn", fake_follow)
    monkeypatch.setattr(service, "_reply_texts", fake_reply)

    async def fake_ack(*_a, **_k):
        return None

    monkeypatch.setattr(service, "_ack_later", fake_ack)

    message = type("Message", (), {"content": "再帮我看一眼这个", "id": "MSG-9"})()
    asyncio.run(service._handle(message, "c2c", "OPENID", "", "MSG-9"))

    assert injected["channel"] == {"name": "qq", "kind": "c2c", "conversation_id": "OPENID"}, (
        "插入必须带 channel：服务端靠绑定表决定账号，靠会话自己决定落哪条分支会话"
    )
    assert injected["text"] == "再帮我看一眼这个"
    assert followed["session"] == "2026-10-01", "订阅要用服务端解析出来的会话名"
    assert followed["run_id"] == "RUN-1"
    assert bot_module.INSERTED_HINT in sent
    assert "答案是 42" in sent, "回合结束后必须把答案发回 QQ，否则回答只留在网页端"
    assert bot_module.BUSY_HINT not in sent
