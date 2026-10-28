from pathlib import Path

from qqbot.media import describe_unsendable, pick_sendable


def test_pick_sendable_returns_image_file(tmp_path: Path):
    img = tmp_path / "chart.png"
    img.write_bytes(b"\x89PNG")

    assert pick_sendable({"kind": "file", "path": str(img), "name": "chart.png"}) == img


def test_pick_sendable_accepts_non_image_file(tmp_path: Path):
    # 契约变更：QQ 的 upload_media 本来就支持 file_type=4（文件），此前在这里
    # 被当成"发不出去"，主人只能收到一句本机路径。非图片必须能发。
    doc = tmp_path / "report.pdf"
    doc.write_bytes(b"%PDF")

    assert pick_sendable({"kind": "file", "path": str(doc), "name": "report.pdf"}) == doc


def test_pick_sendable_accepts_markdown_artifact(tmp_path: Path):
    md = tmp_path / "报告.md"
    md.write_text("# 标题", encoding="utf-8")

    assert pick_sendable({"kind": "file", "path": str(md), "name": "报告.md"}) == md


def test_pick_sendable_rejects_empty_file(tmp_path: Path):
    empty = tmp_path / "half-written.txt"
    empty.write_bytes(b"")

    assert pick_sendable({"kind": "file", "path": str(empty)}) is None


def test_pick_sendable_finds_image_inside_ppt_directory(tmp_path: Path):
    deck = tmp_path / "deck"
    deck.mkdir()
    (deck / "page-1.png").write_bytes(b"\x89PNG")
    (deck / "page-2.png").write_bytes(b"\x89PNG")

    assert pick_sendable({"kind": "ppt", "path": str(deck), "name": "PPT 演示"}) == deck / "page-1.png"


def test_pick_sendable_finds_any_file_inside_directory(tmp_path: Path):
    deck = tmp_path / "deck"
    deck.mkdir()
    (deck / "deck.pptx").write_bytes(b"PK")

    assert pick_sendable({"kind": "ppt", "path": str(deck), "name": "PPT 演示"}) == deck / "deck.pptx"


def test_pick_sendable_returns_none_for_missing_path(tmp_path: Path):
    assert pick_sendable({"kind": "file", "path": str(tmp_path / "nope.png")}) is None


def test_describe_unsendable_lists_name_and_path(tmp_path: Path):
    doc = tmp_path / "report.pdf"
    doc.write_bytes(b"%PDF")

    described = describe_unsendable([{"kind": "file", "path": str(doc), "name": "report.pdf"}])

    assert described == [f"report.pdf（本机路径：{doc}）"]


def test_ppt_deck_sends_a_page_not_a_readme(tmp_path):
    """PPT 产物传的是 deck 目录，不能发成 README.md。

    show_result 的契约就是把 deck 目录交给下游。原来「取排序后第一个能发的
    文件」会挑中 README.md/deck.json —— 主人要一套 PPT，收到的却是说明文档，
    而且看起来投递成功了。
    """
    deck = tmp_path / "deck"
    (deck / "pages").mkdir(parents=True)
    (deck / "deck.json").write_text("{}", encoding="utf-8")
    (deck / "README.md").write_text("readme", encoding="utf-8")
    page = deck / "pages" / "page_01.png"
    page.write_bytes(b"\x89PNG")

    assert pick_sendable({"path": str(deck)}) == page


def test_plain_directory_still_picks_any_file(tmp_path):
    """普通打包目录没有 pages 时退回「随便一个文件」，不能变成发不出去。"""
    plain = tmp_path / "bundle"
    plain.mkdir()
    (plain / "notes.txt").write_text("x", encoding="utf-8")

    assert pick_sendable({"path": str(plain)}) == plain / "notes.txt"
