"""回归：detect_bands 在 4 张已知 sheet 上的表现。

生产契约就一条：**人形数必须正好 3 才认，否则拒掉重试**。
（具体数到几不重要——6 个人的 sheet 也可能因为靠太近被并成 4 段，
  但 4 != 3，同样会被拒。所以这里断言 accept/reject，不断言精确段数。）

已知真值（来自当时的 VLM 判读）：
  A2_169           -> 3 个人形（正面/侧面/背面）  应 accept
  A2_219           -> 3 个人形（正面/侧面/背面）  应 accept
  e2e/miyabi       -> 3 个人形但中间朝向错        应 accept（朝向错要靠 VLM 复核，脚本管不了）
  e2e/miyabi_try2  -> 6 个人形                    应 reject
"""
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from PIL import Image

from threeview import detect_bands

ROOT = Path("/home/user/Fairy/workspace/3d-pipeline-dev")
CASES = [
    (ROOT / "out/routeA2_169_sheet.png", True),
    (ROOT / "out/routeA2_219_sheet.png", True),
    (ROOT / "e2e/miyabi_sheet.png", True),
    (ROOT / "e2e/miyabi_try2_sheet.png", False),
]

fail = 0
for path, should_accept in CASES:
    if not path.is_file():
        print(f"MISSING {path}")
        fail += 1
        continue
    im = Image.open(path).convert("RGB")
    bands = detect_bands(im)
    accepted = len(bands) == 3
    ok = accepted == should_accept
    print(f"{'OK ' if ok else 'BAD'} {path.name}: size={im.size} bands={len(bands)} "
          f"accept={accepted} expect_accept={should_accept} -> {bands}")
    if not ok:
        fail += 1

print(f"\n{'ALL_PASS' if fail == 0 else f'FAIL={fail}'}")
sys.exit(1 if fail else 0)

