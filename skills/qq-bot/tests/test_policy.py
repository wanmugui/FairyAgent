from qqbot.config import QQSettings
from qqbot.policy import format_inbound, is_allowed, session_name


def test_empty_whitelist_denies_everything():
    s = QQSettings()

    assert is_allowed(s, "c2c", "u1") is False
    assert is_allowed(s, "group", "g1") is False


def test_whitelist_allows_only_listed_ids():
    s = QQSettings(allow_c2c=("u1",), allow_groups=("g1",))

    assert is_allowed(s, "c2c", "u1") is True
    assert is_allowed(s, "c2c", "u2") is False
    assert is_allowed(s, "group", "g1") is True
    assert is_allowed(s, "group", "g2") is False


def test_session_name_is_namespaced_by_channel_and_kind():
    assert session_name("c2c", "u1") == "qq:c2c:u1"
    assert session_name("group", "g1") == "qq:group:g1"


def test_group_message_prefixes_sender_name():
    assert format_inbound("group", "小明", "你好") == "[小明] 你好"


def test_c2c_message_is_not_prefixed():
    assert format_inbound("c2c", "小明", "你好") == "你好"


def test_group_message_without_sender_name_stays_clean():
    assert format_inbound("group", "", "你好") == "你好"
