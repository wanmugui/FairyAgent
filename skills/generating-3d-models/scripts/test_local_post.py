"""后处理函数单元测试：不依赖 TripoSR 权重，几秒跑完。

用合成网格覆盖三件容易被"看起来能跑"掩盖的事：
1. keep_largest 真的只留最大块（真实跑图常常只有 1 块，这段逻辑走不到）
2. decimate 真的减面，且**顶点色不丢**
3. orient stand 真的把躺着的网格立起来
"""
from __future__ import annotations

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import numpy as np
import trimesh

from local_triposr import decimate, keep_largest, orient

fail = 0


def check(name: str, ok: bool, detail: str = "") -> None:
    global fail
    print(f"{'OK  ' if ok else 'FAIL'} {name}{(' | ' + detail) if detail else ''}")
    if not ok:
        fail += 1


# --- 1. 去碎片：大球 + 小球，应只留下大球 ---
big = trimesh.creation.icosphere(subdivisions=3)
big.apply_translation([0, 0, 0])
small = trimesh.creation.icosphere(subdivisions=2)
small.apply_translation([3, 0, 0])  # 放远一点，确保 split 判成两块
scene = trimesh.util.concatenate([big, small])
check("构造两体网格", len(scene.faces) > len(big.faces),
      f"{len(scene.faces)} 面（单球 {len(big.faces)}）")

kept, nparts = keep_largest(scene, verbose=False)
check("keep_largest 识别出 2 块", nparts == 2, f"nparts={nparts}")
check("keep_largest 留下的是大球", abs(kept.volume - big.volume) / big.volume < 0.02,
      f"kept.volume={kept.volume:.4f} vs big.volume={big.volume:.4f}")

# --- 2. 减面 + 顶点色 ---
m = trimesh.creation.icosphere(subdivisions=4)
m.visual = trimesh.visual.ColorVisuals(mesh=m)
m.visual.vertex_colors = np.tile([200, 100, 50, 255], (len(m.vertices), 1))
before_faces = len(m.faces)
d = decimate(m, target_faces=2000, verbose=False)
check("decimate 面数下降", len(d.faces) < before_faces, f"{before_faces} -> {len(d.faces)}")
check("decimate 接近目标", 1500 <= len(d.faces) <= 2500, f"{len(d.faces)}")
has_colors = d.visual is not None and getattr(d.visual, "vertex_colors", None) is not None
check("decimate 保留顶点色", bool(has_colors))
if has_colors:
    vc = d.visual.vertex_colors
    check("顶点色长度匹配新顶点", len(vc) == len(d.vertices), f"{len(vc)} vs {len(d.vertices)}")
    uniq = np.unique(vc[:, :3].reshape(-1, 3), axis=0)
    check("顶点色内容合理（非全黑）", len(uniq) >= 1 and vc[:, :3].max() > 0,
          f"unique_colors={len(uniq)} max={vc[:, :3].max()}")

# --- 3. 朝向：躺着的网格（长轴在 X）应被立成 Y ---
lying = trimesh.creation.box(extents=[2.0, 0.5, 0.3])
st = orient(lying.copy(), "stand", verbose=False)
ext = np.asarray(st.extents).round(3).tolist()
check("orient stand 把最长轴立成 Y", ext[1] > ext[0], f"extents={ext}")

auto = orient(lying.copy(), "y_up", verbose=False)
aext = np.asarray(auto.extents).round(3).tolist()
check("orient y_up 自动立起", max(aext) == aext[1], f"extents={aext}")

raw = orient(lying.copy(), "raw", verbose=False)
check("orient raw 不动", np.allclose(np.asarray(raw.extents), np.asarray(lying.extents)))

print(f"\n{'UNIT_ALL_PASS' if fail == 0 else f'UNIT_FAIL={fail}'}")
sys.exit(1 if fail else 0)
