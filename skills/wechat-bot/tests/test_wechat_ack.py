"""微信桥的"收到，正在处理…"：慢回合先应一声，快回合别多话。

主人反馈"微信没有中间态"——查下来是阈值问题：ACK 是 ack_delay_seconds 之后
才发的，而他那条微信 5 秒就答完了（阈值 8 秒），于是"中间态"根本没机会出现。
这里把这件事钉死：慢回合必须有 ACK，快回合必须没有。
"""
import time

import pytest

from wechatbot import bot as bot_module
from wechatbot.bot import ACK_TEXT, Bridge
from wechatbot.config import Account, ChatSettings
from wechatbot.fairy_client import TurnResult

USER = "u1@im.wechat"


class FakeClient:
    def __init__(self) -> None:
        self.sent: list[str] = []

    def send_text(self, user_id: str, text: str, context_token: str) -> dict:
        self.sent.append(text)
        return {}


def inbound(text: str = "在吗") -> dict:
    return {
        "message_id": "m1",
        "from_user_id": USER,
        "context_token": "ctx",
        "item_list": [{"type": 1, "text_item": {"text": text}}],
    }


@pytest.fixture(autouse=True)
def isolated_state_dir(tmp_path, monkeypatch):
    """把状态目录指到临时目录。

    Bridge 每次收到消息都会把 context_token 落盘（这是定时任务能主动发消息的
    依据），默认落在 ~/.config/fairy/wechat。测试如果不隔离，就会往真机的账号
    目录里写一条假 token——仓库里的 auth 测试特意不碰真库，这里同理。
    """
    monkeypatch.setenv("FAIRY_WECHAT_DIR", str(tmp_path / "wechat"))
    return tmp_path


def bridge(client: FakeClient, ack_delay: float) -> Bridge:
    account = Account(bot_token="t", allow_users=(USER,))
    settings = ChatSettings(allow_users=(USER,), ack_delay_seconds=ack_delay)
    return Bridge(account, settings, client=client)


def test_a_slow_turn_gets_acknowledged_first(monkeypatch):
    client = FakeClient()
    service = bridge(client, ack_delay=0.05)

    async def slow_turn(session, text, settings, user_id, **kwargs):
        time.sleep(0.4)
        return TurnResult(text="答案", files=[], completed=True)

    monkeypatch.setattr(bot_module, "run_turn", slow_turn)
    service.handle(inbound())

    assert client.sent == [ACK_TEXT, "答案"], f"慢回合应当先应一声，实际 {client.sent}"


def test_a_fast_turn_answers_without_an_ack(monkeypatch):
    """答案比 ACK 还先到的时候，别再补一句"正在处理"。"""
    client = FakeClient()
    service = bridge(client, ack_delay=0.5)

    async def fast_turn(session, text, settings, user_id, **kwargs):
        return TurnResult(text="答案", files=[], completed=True)

    monkeypatch.setattr(bot_module, "run_turn", fast_turn)
    service.handle(inbound())

    assert client.sent == ["答案"], f"快回合不该有中间态，实际 {client.sent}"


def test_the_ack_threshold_is_short_enough_to_be_seen(monkeypatch):
    """默认阈值必须短到"人还在等"的时候就看到中间态。

    8 秒对 5 秒就答完的回合来说等于没有——这正是主人报的那个问题。
    """
    from wechatbot.config import ChatSettings as Settings

    assert Settings().ack_delay_seconds <= 3.0
