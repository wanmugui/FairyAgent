from wechatbot.config import Account, ChatSettings
from wechatbot.policy import effective_settings, is_allowed, session_name


def test_empty_allowlist_denies_everything():
    assert is_allowed(ChatSettings(), "u1@im.wechat") is False


def test_allowlist_allows_only_listed_users():
    s = ChatSettings(allow_users=("u1@im.wechat",))
    assert is_allowed(s, "u1@im.wechat") is True
    assert is_allowed(s, "u2@im.wechat") is False


def test_session_name_is_namespaced():
    assert session_name("u1@im.wechat") == "wechat:u1@im.wechat"


def test_account_allowlist_wins_over_the_shared_one():
    """扫码接入之后，每个 bot 只回它自己那个微信。

    共享名单是给"还没接入"的老用法兜底的；如果它继续生效，一台机器上挂着
    几个人的 bot 时，别人的微信会被这个 bot 代答。
    """
    shared = ChatSettings(allow_users=("someone-else@im.wechat",))
    account = Account(bot_token="t", allow_users=("me@im.wechat",))
    effective = effective_settings(shared, account)
    assert is_allowed(effective, "me@im.wechat") is True
    assert is_allowed(effective, "someone-else@im.wechat") is False


def test_an_account_without_its_own_list_keeps_the_shared_one():
    shared = ChatSettings(allow_users=("u1@im.wechat",))
    assert effective_settings(shared, Account(bot_token="t")) is shared


def test_text_extraction_from_item_list():
    from wechatbot.ilink import ILinkClient
    msg = {"item_list": [{"type": 1, "text_item": {"text": "你好"}},
                         {"type": 2, "image_item": {}},
                         {"type": 1, "text_item": {"text": "第二段"}}]}
    assert ILinkClient.extract_text(msg) == "你好\n第二段"
    assert ILinkClient.extract_text({}) == ""
