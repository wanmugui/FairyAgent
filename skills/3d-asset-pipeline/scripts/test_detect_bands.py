"""回归：detect_bands 在已知 sheet 上的表现。

生产契约就一条：**主体数必须正好 3 才认，否则拒掉重试**。
（具体数到几不重要——6 个人的 sheet 也可能因为靠太近被并成 4 段，
  但 4 != 3，同样会被拒。所以这里断言 accept/reject，不断言精确段数。）

角色真值（VLM 判读）：
  A2_169 / A2_219 / miyabi -> 3 个人形，应 accept
  miyabi_try2               -> 6 个人形，reject

**场景元素真值（2026-10-16 新增，VLM 逐张判读）**：
这三张是专门为「阴影把三个物体桥接成一条带」这个 bug 留的证据。
原阈值 245 会把物体下方大片柔和投影当成墨，导致三物体间的白缝被桥接，
整张图只切出 1~2 段 → 明明画对了却被拒（岩石那张 VLM 明确是 3 块石头）。
  prop_rock_3objects_shadowed  -> 3 块石头（VLM 确认），原阈值只切出 2 段
  prop_house_shadowed          -> 3 个视角（VLM 确认），原阈值只切出 1 段
  prop_grass_light             -> 3 束草（VLM 确认）；这张是**反向护栏**：
                                 阈值再调深（180/160/140）就会把它切碎甚至切出 0 段，
                                 所以它是「不许继续往深调」的凭据。

跑法：python3 test_detect_bands.py
"""
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from PIL import Image

from threeview import detect_bands, INK_THRESHOLD_DEFAULT

ROOT = Path("/path/to/Fairy/workspace/3d-pipeline-dev")
# (路径, 是否应被接受, 说明)
CASES = [
    (ROOT / "out/routeA2_169_sheet.png", True, "角色 A2_169"),
    (ROOT / "out/routeA2_219_sheet.png", True, "角色 A2_219"),
    (ROOT / "e2e/miyabi_sheet.png", True, "角色 miyabi"),
    (ROOT / "e2e/miyabi_try2_sheet.png", False, "角色 miyabi_try2（6 个人形）"),
    (HERE / "testdata/prop_rock_3objects_shadowed_sheet.png", True, "石头×3，带阴影"),
    (HERE / "testdata/prop_house_shadowed_sheet.png", True, "房子×3 视角，带阴影"),
    (HERE / "testdata/prop_grass_light_sheet.png", True, "浅色草地（阈值调深就会碎）"),
]

fail = 0
print(f"默认 ink_threshold={INK_THRESHOLD_DEFAULT}\n")
for path, should_accept, label in CASES:
    if not path.is_file():
        print(f"MISSING {path}  ({label})")
        fail += 1
        continue
    im = Image.open(path).convert("RGB")
    bands = detect_bands(im)
    accepted = len(bands) == 3
    ok = accepted == should_accept
    print(f"{'OK ' if ok else 'BAD'} {label}: size={im.size} bands={len(bands)} "
          f"accept={accepted} expect={should_accept} -> {bands}")
    if not ok:
        fail += 1

# 负对照：新阈值必须真的比旧的 245 强，否则这几张 fixture 就没有存在意义
print()
NEG = [
    (HERE / "testdata/prop_rock_3objects_shadowed_sheet.png", 3, 2),
    (HERE / "testdata/prop_house_shadowed_sheet.png", 3, 1),
]
for path, new_expect, old_expect in NEG:
    if not path.is_file():
        continue
    im = Image.open(path).convert("RGB")
    new = len(detect_bands(im))
    old = len(detect_bands(im, ink_threshold=245))
    ok = new == new_expect and old == old_expect
    print(f"{'OK ' if ok else 'BAD'} 负对照 {path.name}: 新阈值 {new}（期望 {new_expect}）、"
          f"旧阈值 245 {old}（期望 {old_expect}）")
    if not ok:
        fail += 1

print(f"\n{'ALL_PASS' if fail == 0 else f'FAIL={fail}'}")
sys.exit(1 if fail else 0)
