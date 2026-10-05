from pathlib import Path

from qqbot.media import describe_unsendable, pick_sendable


def test_pick_sendable_returns_image_file(tmp_path: Path):
    img = tmp_path / "chart.png"
    img.write_bytes(b"\x89PNG")

    assert pick_sendable({"kind": "file", "path": str(img), "name": "chart.png"}) == img


def test_pick_sendable_rejects_non_image_file(tmp_path: Path):
    doc = tmp_path / "report.pdf"
    doc.write_bytes(b"%PDF")

    assert pick_sendable({"kind": "file", "path": str(doc), "name": "report.pdf"}) is None


def test_pick_sendable_finds_image_inside_ppt_directory(tmp_path: Path):
    deck = tmp_path / "deck"
    deck.mkdir()
    (deck / "page-1.png").write_bytes(b"\x89PNG")
    (deck / "page-2.png").write_bytes(b"\x89PNG")

    assert pick_sendable({"kind": "ppt", "path": str(deck), "name": "PPT 演示"}) == deck / "page-1.png"


def test_pick_sendable_returns_none_for_directory_without_images(tmp_path: Path):
    deck = tmp_path / "deck"
    deck.mkdir()
    (deck / "deck.pptx").write_bytes(b"PK")

    assert pick_sendable({"kind": "ppt", "path": str(deck), "name": "PPT 演示"}) is None


def test_pick_sendable_returns_none_for_missing_path(tmp_path: Path):
    assert pick_sendable({"kind": "file", "path": str(tmp_path / "nope.png")}) is None


def test_describe_unsendable_lists_name_and_path(tmp_path: Path):
    doc = tmp_path / "report.pdf"
    doc.write_bytes(b"%PDF")

    described = describe_unsendable([{"kind": "file", "path": str(doc), "name": "report.pdf"}])

    assert described == [f"report.pdf（本机路径：{doc}）"]
