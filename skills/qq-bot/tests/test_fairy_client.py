import json

from qqbot.fairy_client import TurnResult, extract_final_text, parse_sse_stream


def _sse(obj) -> str:
    return "data: " + json.dumps(obj, ensure_ascii=False)


def _done_with(messages):
    return {"type": "done", "session": "s", "messages": {"data": {"messages": messages}}}


def test_extract_final_text_takes_last_assistant_and_joins_text_contents():
    payload = _done_with([
        {"role": "user", "contents": [{"type": "text", "content": "你好"}]},
        {"role": "assistant", "contents": [{"type": "text", "content": "第一段"},
                                           {"type": "tool_call", "content": "{}"}]},
        {"role": "assistant", "contents": [{"type": "text", "content": "最终答案"}]},
    ])

    assert extract_final_text(payload) == "最终答案"


def test_extract_final_text_ignores_non_text_contents():
    payload = _done_with([
        {"role": "assistant", "contents": [{"type": "thinking", "content": "内部"},
                                           {"type": "text", "content": "对外"}]},
    ])

    assert extract_final_text(payload) == "对外"


def test_extract_final_text_returns_empty_when_shape_unexpected():
    assert extract_final_text({"type": "done"}) == ""
    assert extract_final_text({}) == ""


def test_parse_sse_stream_collects_text_and_files_and_completion():
    lines = [
        _sse({"type": "status", "message": "运行中"}),
        _sse({"type": "file_result", "files": [{"kind": "file", "path": "/tmp/a.png", "name": "a.png"}]}),
        _sse(_done_with([{"role": "assistant", "contents": [{"type": "text", "content": "完成"}]}])),
        "data: [DONE]",
        "",
    ]

    result = parse_sse_stream(lines)

    assert result.completed is True
    assert result.text == "完成"
    assert result.files == [{"kind": "file", "path": "/tmp/a.png", "name": "a.png"}]


def test_parse_sse_stream_marks_incomplete_when_stream_ends_early():
    assert parse_sse_stream([_sse({"type": "status", "message": "运行中"})]) == TurnResult(text="", files=[], completed=False)


def test_parse_sse_stream_ignores_malformed_lines():
    lines = ["data: not-json",
             _sse(_done_with([{"role": "assistant", "contents": [{"type": "text", "content": "ok"}]}]))]

    assert parse_sse_stream(lines).text == "ok"


def test_build_chat_body_includes_resolved_model():
    from qqbot.fairy_client import build_chat_body

    body = build_chat_body("qq:c2c:u1", "你好", "minimax-m3")

    assert body["model"] == "minimax-m3"
    assert body["session"] == "qq:c2c:u1"
    assert body["stream"] is True
    assert "surface" not in body


def test_build_chat_body_rejects_missing_model():
    import pytest as _pytest

    from qqbot.fairy_client import build_chat_body

    with _pytest.raises(ValueError):
        build_chat_body("qq:c2c:u1", "你好", "")


def test_build_chat_body_carries_the_conversation_not_the_session():
    """桥只报告消息来自哪个会话，账号由服务端的绑定表决定。"""
    from qqbot.fairy_client import build_chat_body

    body = build_chat_body("qq:c2c:u1", "你好", "minimax-m3",
                           {"name": "qq", "conversation_id": "u1"})

    assert body["channel"] == {"name": "qq", "conversation_id": "u1"}
    assert body["session"] == "qq:c2c:u1", "桥的锁键也一并上报，服务端会按 channel 忽略它"


def test_build_headers_sends_bearer_token_when_bound():
    from qqbot.fairy_client import build_headers

    assert build_headers("tok-123") == {"Authorization": "Bearer tok-123"}


def test_build_headers_is_empty_when_unbound():
    from qqbot.fairy_client import build_headers

    assert build_headers("") == {}
    assert build_headers("   ") == {}
