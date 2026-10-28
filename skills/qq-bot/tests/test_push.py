from pathlib import Path

import pytest

from qqbot import push


def test_target_is_split_into_kind_and_conversation():
    assert push._split_target("c2c:ABC123") == ("c2c", "ABC123")
    assert push._split_target("group:XYZ") == ("group", "XYZ")


def test_target_without_kind_is_rejected():
    with pytest.raises(ValueError):
        push._split_target("ABC123")


def test_send_text_posts_to_c2c_path(monkeypatch):
    seen = {}

    def fake_post(url, headers=None, json=None, timeout=None):
        seen["url"] = url
        seen["json"] = json
        seen["headers"] = headers

        class R:
            status_code = 200
            text = "{}"

            def json(self):
                return {"id": "MSG1"}

        return R()

    monkeypatch.setattr(push, "_headers", lambda: {"Authorization": "QQBot t"})
    monkeypatch.setattr(push.requests, "post", fake_post)

    assert push.send_text("c2c:OPENID", "跑完了") == {"id": "MSG1"}
    assert seen["url"] == f"{push.API}/v2/users/OPENID/messages"
    assert seen["json"] == {"content": "跑完了", "msg_type": 0}


def test_send_text_uses_group_path(monkeypatch):
    seen = {}

    def fake_post(url, headers=None, json=None, timeout=None):
        seen["url"] = url

        class R:
            status_code = 200
            text = "{}"

            def json(self):
                return {}

        return R()

    monkeypatch.setattr(push, "_headers", lambda: {})
    monkeypatch.setattr(push.requests, "post", fake_post)

    push.send_text("group:GID", "hi")
    assert seen["url"] == f"{push.API}/v2/groups/GID/messages"


def test_send_text_raises_on_http_error(monkeypatch):
    class R:
        status_code = 401
        text = '{"message":"unauthorized"}'

    monkeypatch.setattr(push, "_headers", lambda: {})
    monkeypatch.setattr(push.requests, "post", lambda *a, **k: R())

    with pytest.raises(RuntimeError, match="401"):
        push.send_text("c2c:OPENID", "x")


def test_send_sends_text_before_images(tmp_path, monkeypatch):
    img = tmp_path / "chart.png"
    img.write_bytes(b"\x89PNG")
    order = []

    monkeypatch.setattr(push, "send_text", lambda t, x: order.append(("text", x)))
    monkeypatch.setattr(push, "send_image", lambda t, p: order.append(("image", Path(p).name)))

    push.send("c2c:OPENID", "报告好了", [str(img)])

    assert order == [("text", "报告好了"), ("image", "chart.png")]


def test_send_image_missing_file_fails_fast():
    with pytest.raises(FileNotFoundError):
        push.send_image("c2c:OPENID", "/tmp/definitely-not-here-9f2a.png")


def test_send_image_uses_rich_media_msg_type(monkeypatch, tmp_path):
    """富媒体是 msg_type 7；1 是图文混排，用它带 media 字段是参数错误。"""
    img = tmp_path / "t.png"
    img.write_bytes(b"\x89PNG")
    seen = {}

    class R:
        status_code = 200
        text = "{}"

        def json(self):
            return {"id": "MSG1"}

    def fake_post(url, headers=None, json=None, timeout=None):
        seen["url"] = url
        seen["json"] = json
        return R()

    monkeypatch.setattr(push, "upload_media", lambda target, image: "FILE_INFO")
    monkeypatch.setattr(push, "_headers", lambda: {})
    monkeypatch.setattr(push.requests, "post", fake_post)

    push.send_image("c2c:OPENID", img)

    assert seen["json"] == {"msg_type": 7, "media": {"file_info": "FILE_INFO"}}


def test_upload_sends_json_base64_not_multipart(monkeypatch, tmp_path):
    """40093007 的真因，别再赖平台权限。

    这个接口要的是 JSON 里的 file_data(base64) 或 url；用 multipart 传文件会被
    当成"没有可下载的素材"，16x16 和 64x64 的 PNG 一样报
    「富媒体文件下载失败」。实测换成 JSON 立刻返回 file_uuid/file_info。
    """
    import base64

    img = tmp_path / "t.png"
    img.write_bytes(b"\x89PNG\r\n\x1a\n" + b"payload" * 8)
    seen = {}

    class R:
        status_code = 200
        text = "{}"

        def json(self):
            return {"file_info": "FILE_INFO"}

    def fake_post(url, headers=None, json=None, timeout=None, **kwargs):
        seen["url"] = url
        seen["json"] = json
        seen["multipart"] = "files" in kwargs
        return R()

    monkeypatch.setattr(push, "_headers", lambda: {})
    monkeypatch.setattr(push.requests, "post", fake_post)

    assert push.upload_media("c2c:OPENID", img) == "FILE_INFO"
    assert seen["multipart"] is False, "富媒体上传必须用 JSON，multipart 必 40093007"
    assert seen["url"].endswith("/v2/users/OPENID/files")
    assert seen["json"]["file_type"] == 1
    assert seen["json"]["srv_send_msg"] is False
    assert base64.b64decode(seen["json"]["file_data"]) == img.read_bytes()


def test_a_platform_error_still_surfaces_its_text(monkeypatch, tmp_path):
    img = tmp_path / "t.png"
    img.write_bytes(b"\x89PNG")

    class R:
        status_code = 400
        text = '{"code":40093007,"message":"富媒体文件下载失败"}'

        def json(self):
            return {}

    monkeypatch.setattr(push, "_headers", lambda: {})
    monkeypatch.setattr(push.requests, "post", lambda *a, **k: R())

    with pytest.raises(RuntimeError, match="40093007"):
        push.send_image("c2c:OPENID", img)
