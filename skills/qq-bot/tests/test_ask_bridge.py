import json

import pytest

from qqbot import ask_bridge


@pytest.fixture(autouse=True)
def isolated_results(tmp_path, monkeypatch):
    path = tmp_path / "ask_user" / "ask_results.json"
    path.parent.mkdir(parents=True, exist_ok=True)
    monkeypatch.setattr(ask_bridge, "RESULTS_PATH", path)
    return path


def _write(entries):
    ask_bridge.RESULTS_PATH.write_text(json.dumps(entries, ensure_ascii=False), encoding="utf-8")


def _entry(answered=False, ask_type="select_mode"):
    """题目里带 description，好验证 {id}_desc 那条通道。"""
    return {
        "timestamp": 1,
        "ask_type": ask_type,
        "answered": answered,
        "questions": [
            {
                "id": "q1",
                "question": "用哪条路线？",
                "options": [
                    {"label": "本地跑", "description": "在本机直接跑", "recommended": True},
                    {"label": "打包 zip", "description": "打成压缩包"},
                ],
            },
            {"id": "q2", "question": "什么时候要？", "options": [{"label": "今天"}]},
        ],
    }


def test_format_questions_lists_all_in_one_message():
    text = ask_bridge.format_questions(_entry(), auto_after=ask_bridge.ASK_AUTO_SELECT_SECONDS)
    # 主人选的「一次列全部，一次回完」
    assert "1. 用哪条路线？" in text
    assert "2. 什么时候要？" in text
    assert "A. 本地跑 ★推荐" in text
    # 倒计时必须等于后端 AskUserAutoSelectSeconds，不能编一个更宽松的数
    assert f"{ask_bridge.ASK_AUTO_SELECT_SECONDS} 秒" in text
    assert ask_bridge.ASK_AUTO_SELECT_SECONDS == 30


def test_format_questions_marks_confirmation():
    text = ask_bridge.format_questions(_entry(ask_type="ppt_mode.confirm_outline"))
    assert "确认" in text


def test_pending_index_finds_only_new_unanswered():
    _write([_entry(answered=True), _entry(answered=False)])
    assert ask_bridge.pending_index(0)[0] == 1
    # 已经报过的那批不该再报一遍
    assert ask_bridge.pending_index(2) is None


def test_submit_reply_by_letter_resolves_to_label():
    _write([_entry()])
    assert ask_bridge.submit_reply(0, "1A 2A") is True
    answer = json.loads(ask_bridge.RESULTS_PATH.read_text(encoding="utf-8"))[0]["answer"]
    # 与 autoSelectAskAnswers 同构：值必须是 [label] 列表，不是裸字符串
    assert answer["q1"] == ["本地跑"]
    assert answer["q2"] == ["今天"]
    assert answer["q1_desc"] == "在本机直接跑"


def test_submit_reply_by_option_index():
    _write([_entry()])
    ask_bridge.submit_reply(0, "1. 2")
    answer = json.loads(ask_bridge.RESULTS_PATH.read_text(encoding="utf-8"))[0]["answer"]
    assert answer["q1"] == ["打包 zip"]


def test_submit_reply_free_text_is_kept():
    """主人选的「序号与自由文本都认」：直接打一句也算数。"""
    _write([_entry()])
    assert ask_bridge.submit_reply(0, "就用 Godot 那个吧") is True
    stored = json.loads(ask_bridge.RESULTS_PATH.read_text(encoding="utf-8"))[0]
    assert stored["answered"] is True
    assert stored["answer"] == "就用 Godot 那个吧"
    assert stored["answered_at"] > 0


def test_submit_reply_mixed_number_and_text():
    _write([_entry()])
    ask_bridge.submit_reply(0, "1 打包 zip\n2 越快越好")
    answer = json.loads(ask_bridge.RESULTS_PATH.read_text(encoding="utf-8"))[0]["answer"]
    assert answer["q1"] == ["越快越好"] or answer["q1"] == ["打包 zip"]
    assert "q2" in answer


def test_submit_reply_rejects_out_of_range():
    _write([_entry()])
    assert ask_bridge.submit_reply(5, "1A") is False
    assert ask_bridge.submit_reply(0, "   ") is False


def test_pending_index_survives_corrupt_file():
    ask_bridge.RESULTS_PATH.write_text("{不是 json", encoding="utf-8")
    assert ask_bridge.pending_index(0) is None
    assert ask_bridge.current_count() == 0


def test_answer_shape_matches_backend_convention():
    """锁死形状：裸字符串会让模型读错答案，这是踩过的坑，别让它回去。

    对照 agent/internal/biz/tool/builtin/ask_user_wait.go 的
    autoSelectAskAnswers：answers[id] = []any{label}，描述另存 {id}_desc。
    网页端一直产出这个形状，桥必须一模一样。
    """
    _write([_entry()])
    ask_bridge.submit_reply(0, "1A 2A")
    answer = json.loads(ask_bridge.RESULTS_PATH.read_text(encoding="utf-8"))[0]["answer"]
    for qid in ("q1", "q2"):
        assert isinstance(answer[qid], list), f"{qid} 必须是列表，裸字符串会被模型读错"
        assert len(answer[qid]) == 1
