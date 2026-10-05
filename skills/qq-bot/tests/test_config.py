import json
from pathlib import Path

import pytest

from qqbot.config import Credentials, QQSettings, load_credentials, load_settings


def test_load_credentials_reads_appid_and_secret(tmp_path: Path):
    p = tmp_path / "qq-bot.json"
    p.write_text(json.dumps({"appId": "123", "appSecret": "sek"}), encoding="utf-8")

    creds = load_credentials(p)

    assert creds == Credentials(app_id="123", app_secret="sek", sandbox=False)


def test_load_credentials_rejects_incomplete_file(tmp_path: Path):
    p = tmp_path / "qq-bot.json"
    p.write_text(json.dumps({"appId": "123"}), encoding="utf-8")

    with pytest.raises(ValueError):
        load_credentials(p)


def test_load_settings_uses_defaults_when_file_missing(tmp_path: Path):
    settings = load_settings(tmp_path / "absent.json")

    assert settings == QQSettings()
    assert settings.allow_c2c == ()
    assert settings.allow_groups == ()


def test_load_settings_reads_policy_file(tmp_path: Path):
    p = tmp_path / "channels.json"
    p.write_text(json.dumps({"qq": {
        "enabled": True,
        "model": "deepseek-v4-flash",
        "allowC2C": ["u1", "u2"],
        "allowGroups": ["g1"],
        "ackDelaySeconds": 3,
        "maxFileSizeMB": 5,
    }}), encoding="utf-8")

    s = load_settings(p)

    assert s.model == "deepseek-v4-flash"
    assert s.allow_c2c == ("u1", "u2")
    assert s.allow_groups == ("g1",)
    assert s.ack_delay_seconds == 3.0
    assert s.max_file_size_mb == 5.0
    assert s.reply_chunk_size == 3500


def test_resolve_model_prefers_channel_override():
    from qqbot.config import resolve_model

    s = QQSettings(model="minimax-m3")
    assert resolve_model(s, {"default_model": "deepseek-v4-flash"}) == "minimax-m3"


def test_resolve_model_uses_ui_default_before_top_level():
    from qqbot.config import resolve_model

    cfg = {"default_model": "deepseek-v4-flash", "settings": {"default_model": "minimax-m3"}}
    assert resolve_model(QQSettings(), cfg) == "minimax-m3"


def test_resolve_model_falls_back_to_top_level_default():
    from qqbot.config import resolve_model

    assert resolve_model(QQSettings(), {"default_model": "deepseek-v4-flash"}) == "deepseek-v4-flash"


def test_resolve_model_returns_empty_when_nothing_configured():
    from qqbot.config import resolve_model

    assert resolve_model(QQSettings(), {}) == ""


def test_load_credentials_reads_fairy_token_for_account_binding(tmp_path: Path):
    p = tmp_path / "qq-bot.json"
    p.write_text(json.dumps({"appId": "123", "appSecret": "sek", "fairyToken": " abc123 "}),
                 encoding="utf-8")

    creds = load_credentials(p)

    assert creds.fairy_token == "abc123"


def test_load_credentials_defaults_to_no_account_binding(tmp_path: Path):
    p = tmp_path / "qq-bot.json"
    p.write_text(json.dumps({"appId": "123", "appSecret": "sek"}), encoding="utf-8")

    assert load_credentials(p).fairy_token == ""
