#!/usr/bin/env python3
"""本地图 → 3D 网格（glb/obj）。走本机 TripoSR，CPU 推理，不花钱、不外传。

这是**本地路径**，和 SKILL.md 里的云端 Tripo API 并列：云端要 key、要联网、按量计费；
本地全免费、离线可跑，代价是 CPU 慢、细节比云端糙。

用法:
    # 最简：图 → glb
    python3 local_triposr.py --image cat.png -o out/cat

    # 带后处理（交付用推荐这套）
    python3 local_triposr.py --image cat.png -o out/cat \\
        --keep-largest --smooth 3 --target-faces 40000

    # 从三视图 sheet 的指定块建
    python3 local_triposr.py --image out/char_sheet.png --view front -o out/char

    # 离线：强制只用本地缓存，不碰网络
    HF_HUB_OFFLINE=1 python3 local_triposr.py --image cat.png -o out/cat

三处本机适配（不改 TripoSR 源码）：
1. TripoSR 的 ``run.py`` 顶层 import moderngl，headless 下不可用 -> 这里直接调 TSR API 绕开它。
2. 权重是旧版 transformers 的键名，新版不认 -> 加载时重映射。
3. 原始输出是**躺着**的（长轴在 X 上）-> 默认绕 Z 转 90° 立起来。
"""

from __future__ import annotations

import argparse
import re
import sys
import time
from pathlib import Path

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


# ------------------------------------------------------------------ 图像预处理

def prep_image(path: Path, remove_bg: bool, verbose: bool = True):
    """抠背景 + 裁紧主体 + 补正方形。

    不抠背景的话，白底会被 TripoSR 重建成一圈「托盘」状多余几何。
    TripoSR 对构图敏感，所以必须裁紧，别丢一张大片留白的图给它。
    """
    from PIL import Image

    image = Image.open(path).convert("RGBA")
    alpha_mask = None
    if remove_bg:
        if verbose:
            print("  [1/5] rembg 抠背景 ...", flush=True)
        t = time.time()
        try:
            from rembg import remove
            import numpy as np

            cut = remove(image)
            arr = np.array(cut).astype(np.float32)
            alpha = arr[..., 3:4] / 255.0
            bg = np.ones_like(arr[..., :3]) * 128.0  # 中灰底，贴近 TripoSR 原 run.py
            image = Image.fromarray((arr[..., :3] * alpha + bg * (1 - alpha)).astype("uint8")).convert("RGBA")
            alpha_mask = arr[..., 3]
        except Exception as exc:
            if verbose:
                print(f"      rembg 失败（{type(exc).__name__}），退回原图", flush=True)
            image = image.convert("RGB")
        if verbose:
            print(f"      耗时 {time.time()-t:.1f}s", flush=True)

    image = image.convert("RGB")
    if verbose:
        print("  [2/5] 裁紧主体 + 补正方形 ...", flush=True)
    t = time.time()
    import numpy as np

    a = np.array(image.convert("RGBA"))
    if alpha_mask is not None:
        mask = alpha_mask > 8
    else:
        mask = a[..., :3].astype(int).sum(axis=2) < 720  # 没抠图就按「非白」估主体
    if mask.any():
        ys, xs = np.where(mask)
        pad = int(0.04 * max(ys.max() - ys.min(), xs.max() - xs.min()))
        image = image.crop((
            max(int(xs.min()) - pad, 0), max(int(ys.min()) - pad, 0),
            min(int(xs.max()) + pad, a.shape[1]), min(int(ys.max()) + pad, a.shape[0]),
        ))
    w, h = image.size
    side = max(w, h)
    canvas = Image.new("RGB", (side, side), (128, 128, 128))
    canvas.paste(image, ((side - w) // 2, (side - h) // 2))
    if verbose:
        print(f"      -> {canvas.size} 耗时 {time.time()-t:.1f}s", flush=True)
    return canvas


# ------------------------------------------------------------------ 后处理

def _colors(mesh):
    try:
        vc = mesh.visual.vertex_colors
        if vc is not None and len(vc) == len(mesh.vertices):
            return vc[:, :3]
    except Exception:
        pass
    return None


def keep_largest(mesh, verbose: bool = True):
    """去碎片：只保留最大的一块连通体。

    image-to-3D 很容易在主体旁边甩出一堆小渣（地面残影、飞散的三角）。
    按体积挑最大块；非水密时体积没意义，退回按面数。
    """
    parts = mesh.split(only_watertight=False)
    if len(parts) <= 1:
        if verbose:
            print("  [后处理] 去碎片：本来就只有 1 块，跳过", flush=True)
        return mesh, 1
    try:
        key = lambda p: p.volume
        if not all(abs(p.volume) > 0 for p in parts):
            raise ValueError
    except Exception:
        key = lambda p: len(p.faces)
    best = max(parts, key=key)
    if verbose:
        print(f"  [后处理] 去碎片：{len(parts)} 块 -> 保留最大 1 块"
              f"（顶 {len(best.vertices)} / 面 {len(best.faces)}）", flush=True)
    return best, len(parts)


def smooth(mesh, iterations: int, verbose: bool = True):
    """Taubin 平滑：去台阶噪点，又不像 Laplacian 那样把模型缩成一坨。"""
    if iterations <= 0:
        return mesh
    import trimesh

    t = time.time()
    sm = trimesh.smoothing.filter_taubin(mesh, lamb=0.5, nu=0.53, iterations=iterations)
    if verbose:
        print(f"  [后处理] Taubin 平滑 x{iterations}（{time.time()-t:.1f}s）", flush=True)
    return sm


def _edge_hist(mesh) -> dict:
    import numpy as np

    e = mesh.edges_sorted
    _, c = np.unique(e, axis=0, return_counts=True)
    vals, cnts = np.unique(c, return_counts=True)
    return {int(v): int(n) for v, n in zip(vals, cnts)}


def repair(mesh, verbose: bool = True, dedupe: bool = False):
    """减面后的收尾。**默认什么都不做破坏性操作**，只量、只报、只修法线。

    实测（resolution=256，原始 46096 面 / 69144 条边全部恰好 2 次共用，完全水密），
    ``simplify_quadric_decimation`` 到各档目标后的边共用次数分布：

    | target-faces | 结果 |
    |---|---|
    | 40000 | 2 条非流形边 |
    | 30000 | 5 条 |
    | 25000 | 4 条 |
    | 20000 | 3 条（hist `{2: 29994, 4: 3}`） |
    | 10000 | 4 条 |

    **只要减面就会破水密，且和减多少无关**——是简化器在这个网格上的固有行为，
    不是阈值问题。破坏形式是极少数边被 4 个面共用（非流形边），**没有洞**。

    另外实测**步骤顺序也有影响**：先平滑再减面会得到 9 条非流形边，
    先减面再平滑只有 3 条。所以 main() 里减面排在平滑前面。

    一度在这里写了「去重面 + fill_holes」来抢救，**实测是帮倒忙，已删掉**：
    - `update_faces(unique_faces())` 去掉 3 个重复面 → 非流形边没少，反而**凭空多出 6 条边界边（洞）**
    - `fill_holes()` 补这些洞 → 非流形边从 3 涨到 9，边界边再多 14
    - `nondegenerate_faces()` 和 `fix_normals()` 对这个网格**完全没有影响**

    所以正确做法是**别碰拓扑**，如实报数。`--dedupe-faces` 是留给愿意用洞换非流形的人的口子。
    """
    import trimesh

    trimesh.repair.fix_normals(mesh)  # 只动法线方向，不动拓扑，安全

    if dedupe:
        before = len(mesh.faces)
        mesh.update_faces(mesh.unique_faces())
        mesh.update_faces(mesh.nondegenerate_faces())
        mesh.remove_unreferenced_vertices()
        if verbose:
            print(f"      [主动 dedupe] 面 {before} -> {len(mesh.faces)}"
                  "（注意：这会让非流形边变成洞，通常不划算）", flush=True)

    hist = _edge_hist(mesh)
    nonmanifold = sum(n for k, n in hist.items() if k > 2)
    boundary = hist.get(1, 0)
    if verbose:
        print(f"      网格体检：面 {len(mesh.faces)}，边界边(洞) {boundary}，"
              f"非流形边 {nonmanifold}，水密 {'是' if mesh.is_watertight else '否'}", flush=True)
        if not mesh.is_watertight and nonmanifold:
            print(f"      说明：{nonmanifold} 条非流形边是简化器固有的（与减面比例无关），"
                  "本机没装 manifold3d/pymeshfix，无法自动重建。"
                  "影响很小；引擎报非流形错误就调高 --target-faces 或别减面。", flush=True)
    return mesh


def decimate(mesh, target_faces: int, verbose: bool = True, do_repair: bool = True, dedupe: bool = False):
    """减面。

    trimesh 的四边形简化在当前版本会把 visual 定义弄丢，**顶点色是另外补回去的**：
    简化后的顶点去原网格里找最近点继承颜色。fast_simplification 这版
    的 ``agg`` 参数不生效，所以只能走这条路。
    """
    if target_faces <= 0 or len(mesh.faces) <= target_faces:
        if verbose:
            print(f"  [后处理] 减面：当前 {len(mesh.faces)} 面 <= 目标 {target_faces}，跳过", flush=True)
        return mesh
    import numpy as np
    import trimesh

    t = time.time()
    src_colors = _colors(mesh)
    new = mesh.simplify_quadric_decimation(face_count=int(target_faces))
    if src_colors is not None:
        from scipy.spatial import cKDTree

        _, idx = cKDTree(np.asarray(mesh.vertices)).query(np.asarray(new.vertices))
        rgba = np.column_stack([src_colors[idx], np.full(len(idx), 255, dtype=np.uint8)])
        new.visual = trimesh.visual.ColorVisuals(mesh=new)
        new.visual.vertex_colors = rgba.astype(np.uint8)
        if verbose:
            print(f"      顶点色已从原网格最近邻继承（{len(new.vertices)} 个）", flush=True)
    if verbose:
        print(f"  [后处理] 减面：{len(mesh.faces)} -> {len(new.faces)} 面"
              f"（顶点 {len(mesh.vertices)} -> {len(new.vertices)}，{time.time()-t:.1f}s）", flush=True)
    if do_repair:
        repair(new, verbose=verbose, dedupe=dedupe)
    return new


def orient(mesh, mode: str, verbose: bool = True):
    """导出朝向。TripoSR 原始输出是躺着的。"""
    import numpy as np
    import trimesh.transformations as T

    if mode == "raw":
        return mesh
    if mode == "stand":
        mesh.apply_transform(T.rotation_matrix(np.pi / 2, [0, 0, 1]))
        if verbose:
            print("      朝向：绕 Z 转 90° 立起来（--orient stand）", flush=True)
        return mesh
    axis = int(np.argmax(np.asarray(mesh.extents)))
    if axis == 1:
        return mesh
    if axis == 0:
        mesh.apply_transform(T.rotation_matrix(np.pi / 2, [0, 0, 1]))
    else:
        mesh.apply_transform(T.rotation_matrix(-np.pi / 2, [1, 0, 0]))
    if verbose:
        print(f"      朝向：自动把最长轴（原 axis {axis}）立成 Y（--orient y_up）", flush=True)
    return mesh


# ------------------------------------------------------------------ 统计

def stats(mesh, tag: str) -> str:
    return (f"    {tag:<16s} 顶点 {len(mesh.vertices):>7d}  面 {len(mesh.faces):>7d}  "
            f"水密 {'是' if mesh.is_watertight else '否'}")


# 后处理是**逐级**的：减面有可能把水密性搞丢，必须一眼看出是哪一步丢的，
# 所以每一步都打一行，别等最后才发现。
def report(mesh, tag: str, warn: bool = True) -> None:
    line = stats(mesh, tag)
    print(line, flush=True)
    if warn and not mesh.is_watertight:
        print(f"      ⚠️ 这一步之后网格不水密了（后续步骤会继承这个状态）", flush=True)


# ------------------------------------------------------------------ main

def main() -> int:
    ap = argparse.ArgumentParser(
        description="本地图 → 3D 网格（TripoSR CPU）",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    ap.add_argument("--image", required=True, help="输入图")
    ap.add_argument("--view", default=None, choices=["front", "side", "back"],
                    help="输入是三视图 sheet 时，指定裁哪一块")
    ap.add_argument("-o", "--out", required=True, help="输出前缀（自动加 .glb/.obj）")
    ap.add_argument("--resolution", type=int, default=256, help="网格分辨率，越大越慢（CPU）")
    ap.add_argument("--chunk", type=int, default=8192)
    ap.add_argument("--no-remove-bg", action="store_true", help="不抠背景（一般别用）")

    g = ap.add_argument_group("后处理")
    g.add_argument("--keep-largest", action="store_true", help="去碎片，只保留最大连通体")
    g.add_argument("--smooth", type=int, default=0, metavar="N", help="Taubin 平滑迭代次数")
    g.add_argument("--target-faces", type=int, default=0, metavar="N", help="减面到约 N 个面")
    g.add_argument("--dedupe-faces", action="store_true",
                   help="减面后去掉重复面（默认不做：实测会凭空造出洞，通常不划算）")

    o = ap.add_argument_group("导出")
    o.add_argument("--orient", default="stand", choices=["stand", "y_up", "raw"],
                   help="朝向。stand=绕Z转90°（实测 TripoSR 默认要这样才直立）")
    o.add_argument("--format", default="glb", choices=["glb", "obj", "both"])

    ap.add_argument("--triposr", default=DEFAULT_TRIPOSR, help="TripoSR 代码目录")
    ap.add_argument("--offline", action="store_true", help="强制离线（等价 HF_HUB_OFFLINE=1）")
    ap.add_argument("--save-input", action="store_true", help="保存预处理后的输入图，便于排查")
    args = ap.parse_args()

    if args.offline:
        import os
        os.environ.setdefault("HF_HUB_OFFLINE", "1")
        os.environ.setdefault("TRANSFORMERS_OFFLINE", "1")

    import numpy as np
    import torch
    from omegaconf import OmegaConf

    triposr = Path(args.triposr)
    if not (triposr / "tsr").is_dir():
        print(f"❌ 找不到 TripoSR: {triposr}/tsr（用 --triposr 指定）", flush=True)
        return 2
    sys.path.insert(0, str(triposr))
    from tsr.system import TSR  # noqa: E402

    # sheet 裁块
    src = Path(args.image)
    if args.view:
        from PIL import Image

        sheet = src if "sheet" in src.stem else src.parent / f"{src.stem.rsplit('_', 1)[0]}_sheet.png"
        if not sheet.is_file():
            print(f"❌ 找不到 sheet: {sheet}", flush=True)
            return 2
        im = Image.open(sheet).convert("RGB")
        W, H = im.size
        cut = W // 3
        i = {"front": 0, "side": 1, "back": 2}[args.view]
        src = src.parent / f"{sheet.stem.rsplit('_', 1)[0]}_{args.view}.png"
        im.crop((i * cut, 0, W if i == 2 else (i + 1) * cut, H)).save(src)
        print(f"[0/5] 从 sheet 裁出 {args.view} -> {src}", flush=True)

    image = prep_image(src, not args.no_remove_bg)
    if args.save_input:
        ip = Path(f"{args.out}_input.png")
        ip.parent.mkdir(parents=True, exist_ok=True)
        image.save(ip)
        print(f"      预处理输入图 -> {ip}", flush=True)

    t_all = time.time()
    cfg = OmegaConf.load(str(triposr / "weights" / "config.yaml"))
    OmegaConf.resolve(cfg)
    model = TSR(cfg)
    ckpt = torch.load(str(triposr / "weights" / "model.ckpt"), map_location="cpu")
    model.load_state_dict(remap(ckpt))
    model.renderer.set_chunk_size(args.chunk)
    model.to("cpu").eval()

    print(f"  [3/5] 前向推理 (CPU, resolution={args.resolution}) ...", flush=True)
    t = time.time()
    with torch.no_grad():
        codes = model([image], device="cpu")
        mesh = model.extract_mesh(codes, has_vertex_color=True, resolution=args.resolution)[0]
    t_infer = time.time() - t
    print(f"      推理耗时 {t_infer:.1f}s", flush=True)

    print("  [4/5] 后处理 ...", flush=True)
    report(mesh, "原始")
    if args.keep_largest:
        mesh, _n = keep_largest(mesh)
        report(mesh, "去碎片后")
    # 顺序要紧：**先减面再平滑**。反过来会明显更差。
    # 实测（同一张图，原始 46096 面完全水密）：
    #   先平滑后减面 -> 9 条非流形边
    #   先减面后平滑 -> 3 条非流形边（和平滑与否无关）
    # 平滑改了顶点位置，二次误差度量跟着变，简化器就会挑出更烂的拓扑。
    # 先减面则拓扑损失最小，之后再平滑还能顺手磨掉简化引入的棱。
    if args.target_faces > 0:
        mesh = decimate(mesh, args.target_faces, dedupe=args.dedupe_faces)
        report(mesh, "减面后")
    if args.smooth > 0:
        mesh = smooth(mesh, args.smooth)
        report(mesh, "平滑后")

    print("  [5/5] 导出 ...", flush=True)
    mesh = orient(mesh, args.orient)

    out = Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    formats = ["glb", "obj"] if args.format == "both" else [args.format]
    written = []
    for fmt in formats:
        p = Path(f"{out}.{fmt}")
        mesh.export(str(p))
        written.append((p, p.stat().st_size))

    ext = np.asarray(mesh.extents).round(3).tolist()
    print("\n=== 本地建模完成 ===")
    print(f"输入        {src}")
    print(f"总耗时      {time.time()-t_all:.1f}s（其中推理 {t_infer:.1f}s）")
    print(f"包围盒 ext  {ext}")
    print(stats(mesh, "最终"))
    for p, size in written:
        print(f"输出        {p}  {size/1024:.0f} KB")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
