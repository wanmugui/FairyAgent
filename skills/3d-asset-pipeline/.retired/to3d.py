#!/usr/bin/env python3
"""三视图 / 单张图 → 3D 网格（glb + obj），走本机 TripoSR（CPU，不花钱）。

用法:
    python3 to3d.py --image out/miyabi_front.png -o out/miyabi
    python3 to3d.py --image out/miyabi_sheet.png --view front -o out/miyabi

关键：必须抠背景。不抠的话白底图会被重建成一圈"托盘"状多余几何（实测过）。
所以默认 --remove-bg，走 rembg，再裁紧主体补成正方形喂进去。

TripoSR 的两处本机适配（只做加载期适配，不改它的源码）：
1. run.py 顶层 import moderngl，headless 下不可用 → 这里直接调 TSR API，绕开 run.py。
2. 权重是旧版 transformers 命名，本机新版不认 → 加载时重映射键名。
"""

from __future__ import annotations

import argparse
import os
import re
import sys
import time
from pathlib import Path

HERE = Path(__file__).resolve().parent
DEFAULT_TRIPOSR = "/home/user/Fairy/workspace/triposr"

# 旧 CLIP 命名 -> 新版命名，仅作用于 image_tokenizer 主干
_SUB = [
    (r"attention\.attention\.query", "attention.q_proj"),
    (r"attention\.attention\.key", "attention.k_proj"),
    (r"attention\.attention\.value", "attention.v_proj"),
    (r"attention\.output\.dense", "attention.o_proj"),
    (r"intermediate\.dense", "mlp.fc1"),
    (r"(?<!intermediate\.)output\.dense", "mlp.fc2"),
]
_PREFIX = re.compile(r"^image_tokenizer\.model\.encoder\.layer\.(\d+)\.")


def remap(state: dict) -> dict:
    out = {}
    for k, v in state.items():
        m = _PREFIX.match(k)
        if m:
            k = f"image_tokenizer.model.layers.{m.group(1)}." + k[m.end():]
            for pat, rep in _SUB:
                k = re.sub(pat, rep, k)
        out[k] = v
    return out


def prep_image(path: Path, remove_bg: bool, pad_square: bool) -> "object":
    """抠背景 + 裁紧主体 + 补正方形。返回 PIL Image。"""
    from PIL import Image

    image = Image.open(path).convert("RGBA")
    if remove_bg:
        print("  [1/4] rembg 抠背景 ...", flush=True)
        t = time.time()
        try:
            from rembg import remove
            import numpy as np

            cut = remove(image)
            arr = np.array(cut).astype(np.float32)
            alpha = arr[..., 3:4] / 255.0
            bg = np.ones_like(arr[..., :3]) * 128.0  # 中灰底，贴近 run.py
            image = Image.fromarray((arr[..., :3] * alpha + bg * (1 - alpha)).astype("uint8")).convert("RGBA")
        except Exception as exc:
            print(f"      rembg 失败（{type(exc).__name__}），退回原图", flush=True)
            image = image.convert("RGB")
        print(f"      耗时 {time.time()-t:.1f}s", flush=True)

    image = image.convert("RGB")
    if pad_square:
        print("  [2/4] 裁紧主体 + 补正方形 ...", flush=True)
        t = time.time()
        import numpy as np

        a = np.array(image.convert("RGBA"))
        if remove_bg and a[..., 3].min() < 255:
            mask = a[..., 3] > 8
        else:
            # 没抠图时按「非白」估主体
            mask = (a[..., :3].astype(int).sum(axis=2) < 720)
        if mask.any():
            ys, xs = np.where(mask)
            pad = int(0.04 * max(ys.max() - ys.min(), xs.max() - xs.min()))
            box = (
                max(int(xs.min()) - pad, 0),
                max(int(ys.min()) - pad, 0),
                min(int(xs.max()) + pad, a.shape[1]),
                min(int(ys.max()) + pad, a.shape[0]),
            )
            image = image.crop(box)
        w, h = image.size
        side = max(w, h)
        canvas = Image.new("RGB", (side, side), (128, 128, 128))
        canvas.paste(image, ((side - w) // 2, (side - h) // 2))
        image = canvas
        print(f"      -> {image.size} 耗时 {time.time()-t:.1f}s", flush=True)
    return image


def main() -> int:
    ap = argparse.ArgumentParser(description="图片 → 3D 网格（本机 TripoSR CPU）")
    ap.add_argument("--image", required=True, help="输入图，通常是 threeview 的 _front.png")
    ap.add_argument("--view", default=None, choices=["front", "side", "back"],
                    help="输入是 sheet 时指定裁哪一块")
    ap.add_argument("-o", "--out", required=True, help="输出前缀，如 out/miyabi")
    ap.add_argument("--resolution", type=int, default=256, help="网格分辨率，越大越慢（CPU）")
    ap.add_argument("--chunk", type=int, default=8192)
    ap.add_argument("--no-remove-bg", action="store_true")
    ap.add_argument("--orient", default="stand", choices=["stand", "y_up", "raw"],
                    help="导出朝向。stand=绕Z转90°（实测 TripoSR 默认要这样才直立）；"
                         "y_up=自动把最长轴立成Y；raw=不转，保留躺姿")
    ap.add_argument("--triposr", default=DEFAULT_TRIPOSR, help="TripoSR 代码目录")
    args = ap.parse_args()

    import numpy as np
    import torch
    import trimesh.transformations as trimesh_transform
    from omegaconf import OmegaConf

    triposr = Path(args.triposr)
    if not (triposr / "tsr").is_dir():
        print(f"❌ 找不到 TripoSR: {triposr}/tsr（用 --triposr 指定）", flush=True)
        return 2
    sys.path.insert(0, str(triposr))
    from tsr.system import TSR  # noqa: E402

    src = Path(args.image)
    if args.view:
        sheet = src if "sheet" in src.stem else src.parent / f"{src.stem.rsplit('_', 1)[0]}_sheet.png"
        if not sheet.is_file():
            print(f"❌ 找不到 sheet: {sheet}", flush=True)
            return 2
        from PIL import Image

        im = Image.open(sheet).convert("RGB")
        W, H = im.size
        i = {"front": 0, "side": 1, "back": 2}[args.view]
        cut = W // 3
        src.parent.mkdir(parents=True, exist_ok=True)
        src = src.parent / f"{src.stem}_{args.view}.png"
        im.crop((i * cut, 0, W if i == 2 else (i + 1) * cut, H)).save(src)
        print(f"[0/4] 从 sheet 裁出 {args.view} -> {src}", flush=True)

    image = prep_image(src, not args.no_remove_bg, True)
    image.save(Path(args.out + "_input.png"))

    t_all = time.time()
    cfg = OmegaConf.load(str(triposr / "weights" / "config.yaml"))
    OmegaConf.resolve(cfg)
    model = TSR(cfg)
    ckpt = torch.load(str(triposr / "weights" / "model.ckpt"), map_location="cpu")
    model.load_state_dict(remap(ckpt))
    model.renderer.set_chunk_size(args.chunk)
    model.to("cpu").eval()
    print("  [3/4] 前向推理 (CPU) ...", flush=True)

    with torch.no_grad():
        codes = model([image], device="cpu")
        mesh = model.extract_mesh(codes, has_vertex_color=True, resolution=args.resolution)[0]
    print("  [4/4] 导出 ...", flush=True)

    # TripoSR 原始输出是**躺着**的：长轴在 X 上（实测 ext≈[0.98, 0.58, 0.30]）。
    # 绕 Z 转 90° 才站起来（实测四种旋转里只有这个方向得到直立人形）。
    if args.orient == "stand":
        mesh.apply_transform(trimesh_transform.rotation_matrix(np.pi / 2, [0, 0, 1]))
        print("      已按 --orient stand 绕 Z 轴旋转 90° 立起来", flush=True)
    elif args.orient == "raw":
        pass
    else:  # y_up：让最高的那根轴变成 Y，网格朝向任意输入都能立住
        ext = np.asarray(mesh.extents)
        axis = int(np.argmax(ext))
        if axis == 1:
            pass
        elif axis == 0:
            mesh.apply_transform(trimesh_transform.rotation_matrix(np.pi / 2, [0, 0, 1]))
        else:
            mesh.apply_transform(trimesh_transform.rotation_matrix(-np.pi / 2, [1, 0, 0]))
        print(f"      已按 --orient y_up 自动立起来（原最长轴 = {axis}）", flush=True)

    out = Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    glb, obj = Path(f"{out}.glb"), Path(f"{out}.obj")
    mesh.export(str(glb))
    mesh.export(str(obj))

    print(f"\n=== 总耗时 {time.time()-t_all:.1f}s ===")
    print(f"顶点 {len(mesh.vertices)}  面 {len(mesh.faces)}")
    print(f"包围盒 {mesh.bounds.round(4).tolist()}")
    print(f"glb {glb} ({glb.stat().st_size/1024:.1f} KB)")
    print(f"obj {obj} ({obj.stat().st_size/1024:.1f} KB)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
