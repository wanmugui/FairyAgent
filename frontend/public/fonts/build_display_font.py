"""Build the ZZZ display font (Simplified Chinese) as a small woff2.

The `AliMamaShuHeiBold.woff2` that shipped in `public/fonts/` was a 956-byte
stub: the browser refused it (`document.fonts.check('40px AliMamaShuHei')`
came back false) and every Chinese display glyph silently fell back to the
system default, which is what made the skin look cheap.

The font is rebuilt here from Noto Sans CJK SC Black — a 900-weight
geometric grotesque, which is the right weight for the industrial ZZZ
look. Only the glyphs the UI actually needs are kept, plus a generous set
of common Chinese characters so a new label cannot fall back mid-word.

Run from the repository root:

    python3 frontend/public/fonts/build_display_font.py
"""

from pathlib import Path

from fontTools import subset
from fontTools.ttLib import TTCollection

HERE = Path(__file__).resolve().parent
REPO = HERE.parent.parent.parent          # .../Fairy
SRC_TTC = "/usr/share/fonts/opentype/noto/NotoSansCJK-Black.ttc"
SRC_INDEX = 2                              # Noto Sans CJK SC Black
OUT = HERE / "zzz-display-cn.woff2"

# Characters beyond the ones found in the source. Punctuation, the symbols
# the skin draws with, and the 3500 most common Chinese characters, so a
# label added later does not drop back to a different face mid-sentence.
EXTRA = (
    "　、。，．；：？！…—·—・"
    "“”‘’（）［］｛｝〈〉《》「」『』【】〔〕"
    "／＼｜＋－×÷＝＜＞％＃＠＆＊＃＠～"
    "０１２３４５６７８９"
    "ABCXYZ"
    "▸▾▴◂●○■□◆◇▲▼★☆✓✕→←↑↓"
    "·•∙▏▎▍▌▋▊▉█░▒▓"
)

# The most common Chinese characters, used as a safety net so a label
# added later cannot fall back to a different face mid-sentence. The full
# GB2312 level-1 set (3755 glyphs) tripled the file size for characters
# the UI never shows; the ones actually in the source are always included
# on top of this list, so this only covers future edits.
def _common_chinese() -> str:
    """The ~1000 most frequent Chinese characters (GB2312 level 1 head)."""
    chars = []
    for hi in range(0xB0, 0xB0 + 18):     # the first 18 rows of level 1
        for lo in range(0xA1, 0xFF):
            try:
                chars.append(bytes([hi, lo]).decode("gb2312"))
            except UnicodeDecodeError:
                pass
    return "".join(chars)


def source_chinese() -> str:
    """Every CJK character that appears in the front-end source."""
    chars = set()
    for p in REPO.joinpath("frontend/src").rglob("*"):
        if p.suffix not in {".jsx", ".js", ".css", ".html"}:
            continue
        try:
            text = p.read_text(encoding="utf-8")
        except (OSError, UnicodeDecodeError):
            continue
        for ch in text:
            if 0x3000 <= ord(ch) <= 0x9FFF or 0xFF00 <= ord(ch) <= 0xFFEF:
                chars.add(ch)
    return "".join(sorted(chars))


def main():
    text = set(source_chinese()) | set(_common_chinese()) | set(EXTRA)
    # Basic latin and digits, always.
    text |= set("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
    text |= set(" .,:;!?'\"-_/\\|()[]{}<>+=*&^%$#@~`")
    print(f"characters: {len(text)}")

    coll = TTCollection(SRC_TTC, lazy=False)
    font = coll.fonts[SRC_INDEX]
    print(f"source: {font['name'].getDebugName(4)}")

    opts = subset.Options()
    opts.flavor = "woff2"
    opts.desubroutinize = True
    opts.layout_features = ["*"]
    opts.name_IDs = ["*"]
    opts.name_legacy = True
    opts.notdef_outline = True
    opts.recalc_bounds = True
    opts.drop_tables += ["DSIG"]

    sub = subset.Subsetter(options=opts)
    sub.populate(text="".join(sorted(text)))
    sub.subset(font)

    OUT.parent.mkdir(parents=True, exist_ok=True)
    font.flavor = "woff2"
    font.save(str(OUT))
    print(f"wrote {OUT}  {OUT.stat().st_size/1024:.1f} KB")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
