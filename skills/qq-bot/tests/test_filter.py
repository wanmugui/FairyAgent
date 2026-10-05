from qqbot.filter import collapse_whitespace, render, split_chunks, strip_markdown


def test_strip_markdown_removes_fences_and_keeps_code():
    src = "看这个：\n```python\nprint(1)\n```\n好了"
    out = strip_markdown(src)

    assert "```" not in out
    assert "print(1)" in out


def test_strip_markdown_removes_emphasis_and_headings():
    out = strip_markdown("## 标题\n**粗体** 和 *斜体* 和 `代码`")

    assert out == "标题\n粗体 和 斜体 和 代码"


def test_strip_markdown_turns_links_into_text():
    out = strip_markdown("见 [文档](https://example.com/a)")

    assert out == "见 文档"


def test_strip_markdown_drops_images():
    out = strip_markdown("图：![截图](https://example.com/a.png) 完")

    assert "example.com" not in out
    assert "完" in out


def test_collapse_whitespace_squashes_blank_runs():
    assert collapse_whitespace("a\n\n\n\nb   \n") == "a\n\nb"


def test_split_chunks_prefers_line_boundaries():
    assert split_chunks("aaaa\nbbbb\ncccc", 10) == ["aaaa\nbbbb", "cccc"]


def test_split_chunks_hard_splits_oversized_line():
    assert split_chunks("x" * 25, 10) == ["x" * 10, "x" * 10, "x" * 5]


def test_render_applies_all_steps():
    assert render("## 标题\n\n\n\n**正文**", 10) == ["标题\n\n正文"]


def test_strip_internal_blocks_removes_thinking():
    from qqbot.filter import strip_internal_blocks

    assert strip_internal_blocks("<think>内心独白</think>\n\n收到").strip() == "收到"


def test_strip_internal_blocks_removes_reflection():
    from qqbot.filter import strip_internal_blocks

    out = strip_internal_blocks("答案\n<reflection>自省内容</reflection>")

    assert "自省内容" not in out
    assert "答案" in out


def test_strip_internal_blocks_drops_markdown_reflection_tail():
    from qqbot.filter import strip_internal_blocks

    assert strip_internal_blocks("答案\n\n### 反思与改进建议\n一些自省").strip() == "答案"


def test_strip_internal_blocks_unwraps_report():
    from qqbot.filter import strip_internal_blocks

    assert strip_internal_blocks("<report>对外答案</report>") == "对外答案"


def test_render_strips_thinking_before_sending():
    assert render("<think>内心</think>收到", 100) == ["收到"]
