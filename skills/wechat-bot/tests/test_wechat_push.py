"""微信主动推送：token 落盘、按账号找人、缺 token 时的说法。"""
import json
from pathlib import Path

import pytest

from wechatbot import push, state

USER = "o9cq80yFIuvv8IYmxLA1yfJEISJ8@im.wechat"


def make_account(directory: Path, label: str = "harry") -> Path:
    directory.mkdir(parents=True, exist_ok=True)
    path = directory / f"{label}.json"
    path.write_text(json.dumps({
        "label": label,
        "bot_token": "bot-token",
        "ilink_bot_id": "f0d79a4ed709@im.bot",
        "ilink_user_id": USER,
        "allow_users": [USER],
    }), encoding="utf-8")
    return path


def test_context_token_round_trips_through_disk(tmp_path):
    """桥收到的 token 必须落盘：定时任务在另一个进程里跑，读不到内存表。"""
    directory = tmp_path / "wechat"
    make_account(directory)

    assert state.context_for("harry", USER, directory) == ""
    state.remember_context("harry", USER, "ctx-1", directory)
    assert state.context_for("harry", USER, directory) == "ctx-1"
    assert state.load_contexts("harry", directory) == {USER: "ctx-1"}

    # 再收到一条新消息就换成新的：旧 token 会被平台拒。
    state.remember_context("harry", USER, "ctx-2", directory)
    assert state.context_for("harry", USER, directory) == "ctx-2"
    entry = json.loads((directory / "harry.state.json").read_text(encoding="utf-8"))
    assert entry["contexts"][USER]["at"] > 0


def test_missing_token_says_which_rule_blocked_it(tmp_path):
    """没有任何 token 时不能装作发出去了。"""
    directory = tmp_path / "wechat"
    make_account(directory)

    with pytest.raises(RuntimeError, match="context_token"):
        push.send_text(USER, "到点了", directory)


def test_push_uses_the_account_that_owns_this_wechat(tmp_path, monkeypatch):
    directory = tmp_path / "wechat"
    make_account(directory)
    state.remember_context("harry", USER, "ctx-1", directory)
    sent = {}

    class FakeClient:
        def __init__(self, base_url, token):
            sent["base_url"] = base_url
            sent["token"] = token

        def send_text(self, user_id, text, context_token):
            sent["user_id"] = user_id
            sent["text"] = text
            sent["context_token"] = context_token
            return {"ok": True}

    monkeypatch.setattr(push, "ILinkClient", FakeClient)
    assert push.send_text(USER, "到点了", directory) == {"ok": True}
    assert sent["user_id"] == USER
    assert sent["context_token"] == "ctx-1"
    assert sent["token"] == "bot-token"
    assert sent["text"] == "到点了"


def test_unknown_wechat_is_reported_instead_of_silently_dropped(tmp_path):
    directory = tmp_path / "wechat"
    make_account(directory)
    state.remember_context("harry", "someone-else@im.wechat", "ctx", directory)

    with pytest.raises(RuntimeError, match="不属于任何已接入的 bot"):
        push.send_text("someone-else@im.wechat", "喂", directory)


def test_dry_run_reports_whether_a_push_is_possible(tmp_path, capsys):
    directory = tmp_path / "wechat"
    make_account(directory)
    assert push.main(["--to", USER, "--text", "x", "--dry-run", "--directory", str(directory)]) == 1
    assert "没有" in capsys.readouterr().out

    state.remember_context("harry", USER, "ctx-1", directory)
    assert push.main(["--to", USER, "--text", "x", "--dry-run", "--directory", str(directory)]) == 0
    assert "有" in capsys.readouterr().out


# ---- 媒体 item 形状：这些字段是照 photon-hq/wechat-ilink-client 的
# ---- FileItem / VideoItem 定义的，填错平台会静默丢消息，所以钉死。 ----

def _fake_media(**extra):
    base = {"encrypt_query_param": "qp", "aeskey_hex": "abcdef",
            "mid_size": 8, "filekey": "fk"}
    base.update(extra)
    return base


def _stub_client(monkeypatch, media):
    from wechatbot import ilink

    captured = {}
    client = ilink.ILinkClient("https://example.invalid", "tok")
    seen = {}

    def fake_upload(user_id, data, media_type, thumb=None):
        seen["media_type"] = media_type
        seen["size"] = len(data)
        return media

    monkeypatch.setattr(client, "upload_media", fake_upload)
    monkeypatch.setattr(
        client, "send_items",
        lambda uid, ctx, items: captured.update(items=items) or {"ok": True},
    )
    return client, captured, seen


def test_file_item_len_is_a_string_not_a_number(monkeypatch, tmp_path):
    """FileItem.len 是字符串。填成数字平台会把整条 item 静默丢掉。"""
    import base64
    import hashlib

    from wechatbot import ilink

    target = tmp_path / "报告.zip"
    payload = b"PK\x03\x04payload"
    target.write_bytes(payload)
    client, captured, seen = _stub_client(monkeypatch, _fake_media())

    client.send_file(USER, target, "ctx-1")

    assert seen["media_type"] == ilink.MEDIA_FILE
    item = captured["items"][0]
    assert item["type"] == ilink.ITEM_FILE
    file_item = item["file_item"]
    assert file_item["file_name"] == "报告.zip"
    assert file_item["len"] == str(len(payload))
    assert not isinstance(file_item["len"], int)
    assert file_item["md5"] == hashlib.md5(payload).hexdigest()
    assert file_item["media"]["aes_key"] == base64.b64encode(b"abcdef").decode()
    # FileItem 没有缩略图字段
    assert "thumb_media" not in file_item


def test_video_item_carries_duration_size_and_a_thumbnail(monkeypatch, tmp_path):
    """VideoItem 少缩略图的话，对面看到的是空白气泡。"""
    from wechatbot import ilink

    target = tmp_path / "clip.mp4"
    target.write_bytes(b"fake-mp4-bytes")
    client, captured, seen = _stub_client(
        monkeypatch, _fake_media(thumb_encrypt_query_param="tqp", thumb_mid_size=3),
    )
    monkeypatch.setattr(ilink, "make_video_thumbnail", lambda p, **kw: (b"JPEG", (200, 150)))
    monkeypatch.setattr(ilink, "probe_video", lambda p: (2.5, 320, 240))

    client.send_video(USER, target, "ctx-1")

    assert seen["media_type"] == ilink.MEDIA_VIDEO
    item = captured["items"][0]
    assert item["type"] == ilink.ITEM_VIDEO
    video_item = item["video_item"]
    assert video_item["play_length"] == 2
    assert video_item["video_size"] == len(b"fake-mp4-bytes")
    assert video_item["video_md5"]
    assert video_item["thumb_media"]["encrypt_query_param"] == "tqp"
    assert (video_item["thumb_width"], video_item["thumb_height"]) == (200, 150)
    # VideoItem 没有 ImageItem 那个顶层 aeskey
    assert "aeskey" not in video_item


def test_send_dispatches_text_images_videos_files_in_order(monkeypatch, tmp_path):
    directory = tmp_path / "wechat"
    make_account(directory)
    state.remember_context("harry", USER, "ctx-1", directory)
    order = []

    class FakeClient:
        def __init__(self, base_url, token):
            pass

        def send_text(self, user_id, text, context_token):
            order.append("text")
            return {"ok": True}

        def send_image(self, user_id, image, context_token):
            order.append("image")
            return {"ok": True}

        def send_video(self, user_id, video, context_token):
            order.append("video")
            return {"ok": True}

        def send_file(self, user_id, file, context_token):
            order.append("file")
            return {"ok": True}

    monkeypatch.setattr(push, "ILinkClient", FakeClient)
    push.send(USER, "hi", ["a.png"], ["b.mp4"], ["c.zip"], directory)
    assert order == ["text", "image", "video", "file"]


def test_missing_media_file_is_rejected_before_anything_is_sent(monkeypatch, tmp_path, capsys):
    """文件不存在时要在发之前就停下，不能发一半才炸。"""
    directory = tmp_path / "wechat"
    make_account(directory)
    state.remember_context("harry", USER, "ctx-1", directory)
    sent = []
    monkeypatch.setattr(
        push, "_client_and_token",
        lambda user_id, d: (type("C", (), {"send_text": lambda *a: sent.append(a)})(), "ctx"),
    )
    rc = push.main(["--to", USER, "--video", str(tmp_path / "不存在.mp4"),
                    "--directory", str(directory)])
    assert rc == 1
    assert "不存在" in capsys.readouterr().err
    assert sent == []
