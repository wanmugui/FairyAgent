"""产物投递与 ask 作答的**接线**测试。

test_media / test_ask_bridge 证明的是「选哪个文件」「怎么解析回复」这些纯函数；
这里证明的是这些结果真的被用上了：非图片产物有没有走 send_file、主人回复有没有
在进锁之前被吃掉当成答案。两者缺一不可 —— 只测纯函数的话，最容易出的正是
「函数对了但没接上」这种线上才发现的坑。
"""

import json
import pathlib
import tempfile

import pytest

from wechatbot import ask_bridge
from wechatbot.bot import Bridge


class FakeClient:
    def __init__(self):
        self.sent_images = []
        self.sent_files = []
        self.sent_texts = []

    def send_image(self, user_id, path, context_token=""):
        self.sent_images.append(path)

    def send_file(self, user_id, path, context_token=""):
        self.sent_files.append(path)

    def send_text(self, user_id, text, context_token=""):
        self.sent_texts.append(text)


def _bridge(tmp_path, client, account=None, settings=None):
    bridge = Bridge.__new__(Bridge)
    from wechatbot.config import Account, ChatSettings
    bridge.account = account or Account(label="acct", bot_token="t", allow_users=["u1"])
    bridge.settings = settings or ChatSettings(allow_users=["u1"])
    bridge.client = client
    bridge.state_dir = tmp_path
    bridge._contexts = {"u1": "ctx"}
    bridge._locks = {}
    bridge._seen = set()
    bridge._ask_open = {}
    return bridge


def test_non_image_artifact_goes_through_send_file(tmp_path):
    client = FakeClient()
    bridge = _bridge(tmp_path, client)
    doc = tmp_path / "报告.md"
    doc.write_text("# 报告", encoding="utf-8")
    img = tmp_path / "chart.png"
    img.write_bytes(b"\x89PNG")

    bridge.send_artifacts("u1", [
        {"path": str(doc), "name": "报告.md"},
        {"path": str(img), "name": "chart.png"},
    ])

    assert client.sent_files == [str(doc)], "非图片产物必须真的走 send_file，不能只留一句路径"
    assert client.sent_images == [str(img)], "图片仍走 send_image"


def test_directory_artifact_picks_first_file(tmp_path):
    client = FakeClient()
    bridge = _bridge(tmp_path, client)
    deck = tmp_path / "deck"
    deck.mkdir()
    (deck / "page-1.png").write_bytes(b"\x89PNG")

    bridge.send_artifacts("u1", [{"path": str(deck), "name": "PPT"}])

    assert client.sent_images == [str(deck / "page-1.png")]


def test_reply_to_open_ask_is_consumed_by_handle(tmp_path, monkeypatch):
    """走真实的 handle()：主人回「1B 2A」必须被当成答案吃掉，且不再开新一轮。

    这里必须调 handle 本身。早先的版本在测试里复刻了一遍判定分支，
    那只证明我脑子里的逻辑对，证明不了真代码走了这条路 —— 正是"函数对了
    但没接上"最容易藏的地方。
    """
    results = tmp_path / "ask_user" / "ask_results.json"
    results.parent.mkdir(parents=True)
    monkeypatch.setattr(ask_bridge, "RESULTS_PATH", results)
    results.write_text(json.dumps([{
        "timestamp": 1, "ask_type": "select_mode", "answered": False,
        "questions": [
            {"id": "q1", "question": "哪条？", "options": [{"label": "甲"}, {"label": "乙"}]},
            {"id": "q2", "question": "何时？", "options": [{"label": "今天"}]},
        ],
    }], ensure_ascii=False), encoding="utf-8")

    client = FakeClient()
    bridge = _bridge(tmp_path, client)
    from wechatbot.policy import session_name
    bridge._ask_open = {session_name("u1"): 0}

    # 提问那一轮正占着锁：锁被占住时，答案若走到锁分支只会被当成"本地忙"丢掉。
    lock = bridge._locks.setdefault(session_name("u1"), __import__("threading").Lock())
    lock.acquire()
    started = []
    try:
        import wechatbot.bot as botmod
        original = botmod.run_turn
        botmod.run_turn = lambda *a, **k: started.append(a) or (_ for _ in ()).throw(AssertionError("不该开新一轮"))
        bridge.handle({
            "message_id": "m1",
            "from_user_id": "u1",
            "context_token": "ctx",
            "item_list": [{"type": 1, "text_item": {"text": "1B 2A"}}],
        })
    finally:
        lock.release()
        import wechatbot.bot as botmod
        botmod.run_turn = original

    stored = json.loads(results.read_text(encoding="utf-8"))[0]
    assert stored["answered"] is True, "答案必须写回文件，工具才等得到"
    # 值必须是 [label] 列表，与后端 autoSelectAskAnswers 同构
    assert stored["answer"] == {"q1": ["乙"], "q2": ["今天"]}
    assert started == [], "作答不能被当成新请求又开一轮"
    assert client.sent_texts == ["收到，按你说的来。"], "主人要收到确认，否则不知道答没答上"


def test_ask_receipt_replies_confirm_text(tmp_path):
    client = FakeClient()
    bridge = _bridge(tmp_path, client)
    bridge._asks["wechat-u1"] = 0
    bridge.reply("u1", ["收到，按你说的来。"])
    assert client.sent_texts == ["收到，按你说的来。"]


def test_watch_ask_message_includes_countdown():
    """真实调用点必须把时限说出来。

    之前 bot.py 调 format_questions(entry) 没传 auto_after，纯函数测试全都显式传了
    参数，于是全绿——但真发到微信的消息里根本没有「多少秒内回复」这句。
    这就是「函数对了但没接上」，今晚第二次栽在同一类坑上，所以钉死它。
    """
    from wechatbot.bot import Bridge as _B
    src = __import__("inspect").getsource(_B._watch_ask)
    assert "ASK_AUTO_SELECT_SECONDS" in src, "调用点必须用真实时限，不能默认 0"
    assert "format_questions(entry)" not in src, "不能再漏掉 auto_after"


def test_failed_artifact_is_announced_to_user(tmp_path):
    """发不出去的产物必须告诉主人，不能只写日志。

    这正是当初「文件传不过去」难查的原因：桥只 log.info，主人看到的只是
    「Fairy 说做完了，但文件没来」，既不知道是哪个、也不知道该不该重试。
    """
    client = FakeClient()
    bridge = _bridge(tmp_path, client)
    bridge._contexts = {"u1": "ctx"}

    def boom(user_id, path, context_token=""):
        raise RuntimeError("平台拒绝")

    bridge.client.send_file = boom
    missing = tmp_path / "不存在.md"

    bridge.send_artifacts("u1", [{"path": str(missing), "name": "不存在.md"}])

    assert client.sent_texts, "失败时必须有一条给主人看的告知"
    assert "没能发出去" in client.sent_texts[0]
    assert "不存在.md" in client.sent_texts[0], "要指名是哪个文件，否则没法重试"


def test_successful_artifact_announces_nothing(tmp_path):
    """成功时不许多发废话——那是消耗主人注意力和被动回复额度。"""
    client = FakeClient()
    bridge = _bridge(tmp_path, client)
    bridge._contexts = {"u1": "ctx"}
    doc = tmp_path / "ok.md"
    doc.write_text("x", encoding="utf-8")

    bridge.send_artifacts("u1", [{"path": str(doc), "name": "ok.md"}])

    assert client.sent_files == [str(doc)]
    assert client.sent_texts == [], "投递成功时不该再发一条告知"


def test_ppt_deck_directory_sends_a_page_not_a_readme(tmp_path):
    """PPT 产物传的是 deck 目录，不能发成 README.md。

    show_result 的契约就是把 deck 目录交给下游。原来「取排序后第一个文件」
    会挑中 README.md/deck.json —— 主人要一套 PPT，收到的却是说明文档，
    而且看起来投递成功了。这比不发更糟。
    """
    deck = tmp_path / "deck"
    (deck / "pages").mkdir(parents=True)
    (deck / "htmls").mkdir()
    (deck / "deck.json").write_text("{}", encoding="utf-8")
    (deck / "README.md").write_text("readme", encoding="utf-8")
    (deck / "pages" / "page_01.png").write_bytes(b"\x89PNG")
    (deck / "htmls" / "page_01.html").write_text("<p>1</p>", encoding="utf-8")

    client = FakeClient()
    bridge = _bridge(tmp_path, client)
    bridge._contexts = {"u1": "ctx"}
    bridge.send_artifacts("u1", [{"path": str(deck), "name": "PPT"}])

    assert client.sent_images == [str(deck / "pages" / "page_01.png")], \
        "deck 必须优先发页面图，而不是 README/deck.json"
    assert client.sent_files == []


def test_plain_directory_still_falls_back_to_any_file(tmp_path):
    """普通打包目录没有 pages/htmls 时，退回「随便一个文件」，不能变成发不出去。"""
    plain = tmp_path / "bundle"
    plain.mkdir()
    (plain / "notes.txt").write_text("x", encoding="utf-8")

    client = FakeClient()
    bridge = _bridge(tmp_path, client)
    bridge._contexts = {"u1": "ctx"}
    bridge.send_artifacts("u1", [{"path": str(plain), "name": "bundle"}])

    assert client.sent_files == [str(plain / "notes.txt")]
