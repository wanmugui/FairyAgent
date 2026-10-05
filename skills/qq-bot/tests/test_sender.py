from qqbot.config import QQSettings
from qqbot.sender import ACK_TEXT, Sequencer, plan_texts


def test_sequencer_starts_at_one_and_increments():
    seq = Sequencer(max_replies=3)

    assert seq.take() == 1
    assert seq.take() == 2
    assert seq.take() == 3
    assert seq.take() is None


def test_plan_texts_without_ack_returns_only_body():
    assert plan_texts("最终结果", [], QQSettings(reply_chunk_size=100), include_ack=False) == ["最终结果"]


def test_plan_texts_with_ack_puts_ack_first():
    texts = plan_texts("最终结果", [], QQSettings(reply_chunk_size=100), include_ack=True)

    assert texts[0] == ACK_TEXT
    assert texts[1] == "最终结果"


def test_plan_texts_appends_unsendable_notice():
    texts = plan_texts("结果", ["report.pdf（本机路径：/tmp/report.pdf）"],
                       QQSettings(reply_chunk_size=200), include_ack=False)

    assert "report.pdf" in texts[-1]
    assert "本机路径" in texts[-1]


def test_plan_texts_is_capped_by_passive_budget():
    texts = plan_texts("一" * 50, [], QQSettings(reply_chunk_size=5, max_passive_replies=2), include_ack=True)

    assert len(texts) <= 2


def test_plan_texts_falls_back_when_body_is_empty():
    assert plan_texts("", [], QQSettings(reply_chunk_size=100), include_ack=False) == \
        ["（本轮没有可发送的文本结果）"]
