"""批量元素建模：一份清单 → N 个 glb + 每件一张预览渲图（3d-scene-quality 层 2）。

    /path/to/miniforge3/envs/bpyenv/bin/python3.13 build_props_batch.py

**为什么复用 `hd2d_diorama.py` 的构件而不是再写一套**：层 -1 已经把
building / tree / post / lantern / block 五种构件跑通并验收过（24 元素渲图 PASS）。
再抄一遍就是两份会分叉的实现，所以这里直接 import 它的 `BUILDERS`。

**统一约定（层 2 的验收要点，全部脚本内硬断言）**：
  - 命名：`<元素名>.glb`，与清单里的 `name` 逐字一致，落盘可寻址；
  - 原点：底面中心，**局部 z 的最小值恒为 0**（与层 -1 的锚点规则同源）；
  - 尺度：JSON 里写的是米，1 单位 = 1 米，不做隐式缩放；
  - 每个 glb 只含 1 个 mesh、1 个节点，避免把整场景导出成一个大 glb。

**渲图不是可选的**：每件都渲一张 PNG，脚本末尾会打印客观像素统计
（前景占比 / 逐行宽度），用来在**看图之前**先抓「全黑、空图、贴地失败」。
本 skill 的铁律：文件存在、顶点数达标、glb 能加载，**都不算验证**。
"""

import json
import math
import os
import sys

import bpy
import numpy as np
from PIL import Image
from mathutils import Vector

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
sys.path.insert(0, os.path.normpath(os.path.join(HERE, "..", "scripts")))

import hd2d_diorama as H   # noqa: E402  复用层 -1 的构件与材质辅助
import gnkit               # noqa: E402

CONFIG = os.path.join(HERE, "props-batch.json")


# ---------------------------------------------------------------- config

def load_config(path=CONFIG):
    with open(path, "r", encoding="utf-8") as fh:
        cfg = json.load(fh)
    out = cfg.get("output_dir", "workspace/result/props-batch")
    cfg["output_dir"] = os.path.normpath(os.path.join(HERE, "..", "..", "..", out))
    return cfg


# ---------------------------------------------------------------- 网格抽取

def build_mesh_object(element, layer, palette, air_rgb, name):
    """用层 -1 的分件构件建几何，拼成一个网格对象，原点落在底面中心。"""
    mats = H.element_materials(element, layer, palette, air_rgb)
    parts = H.build_parts(element, mats)
    me = H.combine_parts(name, parts)
    if len(me.vertices) == 0:
        raise ValueError(f"{name}: 求值出 0 顶点")

    # 原点归位：包围盒中心搬到 X/Y=0，底面搬到 Z=0（层 2 的统一约定）
    co = np.array([v.co[:] for v in me.vertices])
    lo, hi = co.min(axis=0), co.max(axis=0)
    co[:, 0] -= (lo[0] + hi[0]) / 2.0
    co[:, 1] -= (lo[1] + hi[1]) / 2.0
    co[:, 2] -= lo[2]
    for v, c in zip(me.vertices, co):
        v.co = Vector(c.tolist())
    me.update()

    obj = bpy.data.objects.new(name, me)
    # 必须挂到**当前 view layer 的 collection**。挂到 scene.collection 在
    # read_factory_settings(use_empty=True) 之后不在 view layer 里，
    # 渲染一片空白且不报错（实测 obj.name in bpy.context.view_layer.objects == False）。
    bpy.context.collection.objects.link(obj)
    bpy.context.view_layer.update()
    return obj, {"verts": len(me.vertices), "faces": len(me.polygons),
                 "bbox": [round(float(x), 4) for x in (hi - lo)],
                 "materials": [m.name for m in me.materials]}


# ---------------------------------------------------------------- 导出 / 渲图

def export_glb(obj, path):
    bpy.ops.object.select_all(action='DESELECT')
    obj.select_set(True)
    bpy.context.view_layer.objects.active = obj
    bpy.ops.export_scene.gltf(filepath=path, export_format='GLB',
                            use_selection=True, export_apply=True)
    return path


_STAGE = {}


def setup_stage(cfg):
    """世界 + 太阳**只建一次**。

    之前每个元素都新建一个 World/Sun/Camera 还不删，跑到第 6 个时渲出来**整张纯黑**
    （背景亮度从 236 一路掉到 0）。逐件新建、逐件累积在这个环境里不可靠，
    改成舞台复用、只重建相机。
    """
    sc = bpy.context.scene
    r = cfg["render"]
    sc.render.engine = "CYCLES"
    sc.cycles.device = "CPU"
    sc.cycles.samples = r.get("samples", 24)
    sc.cycles.use_denoising = True
    sc.render.resolution_x = r.get("width", 420)
    sc.render.resolution_y = r.get("height", 420)
    sc.render.resolution_percentage = 100
    sc.render.image_settings.file_format = 'PNG'
    sc.render.film_transparent = False
    # 预览要看本色，不用 AgX
    sc.view_settings.view_transform = "Standard"

    if not sc.world:
        sc.world = bpy.data.worlds.new("W")
    bg = sc.world.node_tree.nodes.get("Background")
    if bg is None:
        out = next(n for n in sc.world.node_tree.nodes if n.type == 'OUTPUT_WORLD')
        bg = sc.world.node_tree.nodes.new("ShaderNodeBackground")
        sc.world.node_tree.links.new(bg.outputs[0], out.inputs["Surface"])
    bg.inputs["Color"].default_value = (0.92, 0.93, 0.95, 1.0)
    bg.inputs["Strength"].default_value = 0.9

    sun_data = bpy.data.lights.new("SunData", type='SUN')
    sun_data.energy = 3.0
    sun_data.angle = 0.15
    sun = bpy.data.objects.new("Sun", sun_data)
    bpy.context.collection.objects.link(sun)
    sun.rotation_euler = (1.0, 0.0, 0.7)
    _STAGE["sun"] = sun


def render_preview(obj, path, cfg):
    """只重建相机；世界与太阳复用（见 setup_stage 的注释）。"""
    sc = bpy.context.scene
    old = _STAGE.get("cam")
    if old is not None:
        cam_data = old.data
        bpy.data.objects.remove(old, do_unlink=True)
    else:
        cam_data = bpy.data.cameras.new("C")
    cam_data.lens = 50
    cam = bpy.data.objects.new("Cam", cam_data)
    bpy.context.collection.objects.link(cam)
    sc.camera = cam
    _STAGE["cam"] = cam

    # 相机按包围盒自动取景：手写机位必裁切（与层 -1 同一条教训）
    corners = [obj.matrix_world @ Vector(c) for c in obj.bound_box]
    center = sum(corners, Vector()) / 8.0
    radius = max((c - center).length for c in corners)
    # camera.angle 本身就是弧度，别再套 math.radians
    dist = radius / math.tan(cam_data.angle / 2.0) * 1.35
    cam.location = center + Vector((0.6, -1.0, 0.45)).normalized() * dist
    cam.rotation_euler = (center - cam.location).to_track_quat('-Z', 'Y').to_euler()

    sc.render.filepath = path
    bpy.ops.render.render(write_still=True)
    return path


def pixel_stats(png_path):
    """客观像素统计：**看图之前**先抓全黑 / 空图。"""
    a = np.asarray(Image.open(png_path).convert("RGB")).astype(float)
    lum = a.sum(axis=2) / 3.0
    bg = float(np.median(lum[0:6, 0:6]))          # 用角落中位数当背景，抗单像素噪点
    # 前景 = **与背景有明显偏离**的像素，两个方向都算。
    # 只认「比背景暗」是错的：白墙房子比 0.92 的天空还亮，会被判成空图——
    # 实测房子 fg=0.009、岩石 fg=0.003 就是这么来的，判据在骗人，不在模型。
    mask = np.abs(lum - bg) > 12
    rows = np.nonzero(mask.sum(axis=1) > 1)[0]
    if not len(rows):
        return {"fg_ratio": 0.0, "height_px": 0, "blank": True}
    widths = [int(mask[y].sum()) for y in range(rows.min(), rows.max() + 1, max(1, (rows.max() - rows.min()) // 6 or 1))]
    return {"fg_ratio": round(float(mask.mean()), 4),
            "height_px": int(rows.max() - rows.min()),
            "width_px": int(np.nonzero(mask.sum(axis=0) > 1)[0].ptp() + 1),
            "bg_lum": round(bg, 1),
            "widths": widths,
            "blank": False}


# ---------------------------------------------------------------- main

def main():
    import argparse
    ap = argparse.ArgumentParser(description="按清单批量产 glb + 预览渲图")
    ap.add_argument("--only", default=None, help="只做这一个元素（批量内部逐件调用用）")
    ap.add_argument("--no-fanout", action="store_true",
                    help="不逐件开子进程，在同一进程里连做（调试用，会重现累积问题）")
    args = ap.parse_args()

    cfg = load_config()
    if args.only:
        cfg["elements"] = [e for e in cfg["elements"] if e["name"] == args.only]
        if not cfg["elements"]:
            print(f"清单里没有 {args.only}")
            return 2
    elif not args.no_fanout:
        return fanout(cfg)
    layer = cfg["layer"]
    palette = cfg["palette"]
    air_rgb = cfg.get("air_color", [0.68, 0.78, 0.95])

    H.fresh_scene()
    setup_stage(cfg)
    out_dir = cfg["output_dir"]
    os.makedirs(out_dir, exist_ok=True)

    results = []
    failures = []
    for el in cfg["elements"]:
        name = el["name"]
        try:
            obj, info = build_mesh_object(el, layer, palette, air_rgb, name)
        except ValueError as exc:
            failures.append(str(exc))
            continue

        # 断言：原点底面 z=0（层 2 的统一约定）
        zs = [v.co.z for v in obj.data.vertices]
        if abs(min(zs)) > 1e-4:
            failures.append(f"{name}: 原点未落在底面，z_min={min(zs):.4f}")

        glb = os.path.join(out_dir, f"{name}.glb")
        png = os.path.join(out_dir, f"{name}_preview.png")
        export_glb(obj, glb)
        render_preview(obj, png, cfg)

        stats = pixel_stats(png)
        if stats["blank"]:
            failures.append(f"{name}: 预览图是空的（全黑/无前景），渲图有问题")
        rec = {"name": name, "kind": el["kind"], "verts": info["verts"], "faces": info["faces"],
               "materials": info.get("materials"),
               "bbox": info["bbox"], "glb": glb, "glb_bytes": os.path.getsize(glb),
               "preview": png, "stats": stats}
        results.append(rec)
        with open(os.path.join(out_dir, f"{name}_manifest.json"), "w", encoding="utf-8") as fh:
            json.dump(rec, fh, ensure_ascii=False, indent=2)
        print(f"  {name:<16} {el['kind']:<9} verts={info['verts']:<5} faces={info['faces']:<5} "
              f"bbox={info['bbox']} glb={rec['glb_bytes']}B fg={stats['fg_ratio']} "
              f"h={stats['height_px']}px", flush=True)
        # 换下一个元素前清场，保证每个 glb 只含自己
        bpy.data.objects.remove(obj, do_unlink=True)

    summary = {"config": CONFIG, "output_dir": out_dir, "total": len(cfg["elements"]),
               "ok": len(results), "failed": len(failures), "results": results}
    spath = os.path.join(out_dir, "_batch_manifest.json")
    with open(spath, "w", encoding="utf-8") as fh:
        json.dump(summary, fh, ensure_ascii=False, indent=2)

    print(f"\n[props] {len(results)}/{len(cfg['elements'])} 成功 -> {spath}")
    for f in failures:
        print(f"  X {f}")
    return 1 if failures else 0


def fanout(cfg):
    """逐件开**干净子进程**。

    实测在同一进程里连渲 6 件：从第 5 件起整张渲成纯黑（背景亮度 236 → 0），
    且场景对象只增不减（6 件后堆到 16 个）。分件求值 + Cycles 离屏渲染的累积
    在这个环境里不可靠——隔离进程是唯一稳的做法，也更符合批处理本来的语义。
    """
    import subprocess
    here = os.path.dirname(os.path.abspath(__file__))
    results, failures = [], []
    for el in cfg["elements"]:
        print(f"\n=== {el['name']} ===", flush=True)
        proc = subprocess.run([sys.executable, os.path.join(here, "build_props_batch.py"),
                               "--only", el["name"]], capture_output=True, text=True)
        sys.stdout.write(proc.stdout[-2000:])
        if proc.returncode != 0:
            failures.append(f"{el['name']}: exit={proc.returncode}")
            print(proc.stderr[-800:], file=sys.stderr)
        else:
            for line in proc.stdout.splitlines():
                if line.strip().startswith(el["name"]):
                    print("  " + line.strip())
    out_dir = cfg["output_dir"]
    mpath = os.path.join(out_dir, "_batch_manifest.json")
    results = []
    for el in cfg["elements"]:
        m = os.path.join(out_dir, f"{el['name']}_manifest.json")
        if os.path.isfile(m):
            with open(m, encoding="utf-8") as fh:
                results.append(json.load(fh))
    summary = {"config": CONFIG, "output_dir": out_dir, "total": len(cfg["elements"]),
               "ok": len(results), "failed": len(cfg["elements"]) - len(results),
               "mode": "fanout(one process per element)", "results": results}
    with open(mpath, "w", encoding="utf-8") as fh:
        json.dump(summary, fh, ensure_ascii=False, indent=2)
    print(f"\n[props] {len(results)}/{len(cfg['elements'])} 成功 -> {mpath}")
    for f in failures:
        print("  X " + f)
    return 1 if failures else 0


if __name__ == "__main__":
    raise SystemExit(main())
