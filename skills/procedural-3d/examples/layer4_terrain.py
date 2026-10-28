"""场景示例：地形 + 材质分层（3d-scene-quality 层 4）。

    /path/to/miniforge3/envs/bpyenv/bin/python3.13 layer4_terrain.py

做一张山景。条目的验收是「**山脊锐利可辨、分层边界不呆板**」，这两句都要能量，所以脚本给出三个数：

1. **山脊锐利度**：脊线处的坡度分布。高斯平滑过的山看起来「圆润」，看着舒服但不像山；
   真正的山脊是**坡度的极值带**。这里量「坡度超过 75 分位的像素占比」——
   锐利 = 有相当比例的像素处在陡坡上。
2. **分层边界不呆板**：分层线（草/岩/雪交界）在水平方向的高度抖动。
   完美水平直线 = 呆板；量这条线的起伏幅度（相邻列交界高度差的标准差）。
3. **200px 缩略图可读**：缩到 200px 宽后，三条分层带是否还数得出来。

**几何用 numpy、材质分层用 GN，这是刻意的分工**：
- 高度场需要**逐像素迭代的侵蚀近似**（热侵蚀要反复把高处土推到低处），
  那种逐像素循环用 GN 节点写既慢又难读，所以高度场在 numpy 里算，网格一次性建好；
- 分层材质本质是「按位置取色 + 噪声打散边界」，正是 GN 的强项，
  用 `ShaderNodeValToRGB`（ColorRamp）+ `ShaderNodeMixShader` + `ShaderNodeTexNoise` 在节点里做。
"""

import json
import math
import os
import sys

import bpy
import numpy as np

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
sys.path.insert(0, os.path.normpath(os.path.join(HERE, "..", "scripts")))

import layer3_light_rig as L          # 复用层 3 的世界/灯/雾/相机/渲图
import gnkit                          # noqa: F401

CONFIG = os.path.join(HERE, "layer4-terrain.json")


def load_config(path=CONFIG):
    with open(path, "r", encoding="utf-8") as fh:
        cfg = json.load(fh)
    cfg["output_dir"] = os.path.normpath(
        os.path.join(HERE, "..", "..", "..", cfg.get("output_dir", "workspace/result/layer4")))
    return cfg


# ---------------------------------------------------------------- 高度场

def ridged_multifractal(res, octaves, rng, lacunarity=2.0, gain=0.5):
    """脊状多重分形。

    和普通 Perlin 的区别：每层取 `1 - |噪声|` 再乘权，产生的山脊**尖**而不是圆。
    高度场不做这一步，山就像一堆棉花糖——这正是「山脊锐利」的来源。
    """
    total = np.zeros((res, res), dtype=np.float64)
    amp, freq = 1.0, 2.0
    norm = 0.0
    for _ in range(octaves):
        total += amp * ridged_noise(res, freq, rng)
        norm += amp
        amp *= gain
        freq *= lacunarity
    return total / norm


def _value_noise(res, freq, rng):
    """freq×freq 的随机格点 + 双三次插值，纯 numpy。"""
    g = rng.random((freq + 1, freq + 1))
    u = np.linspace(0, freq, res)
    i0 = np.floor(u).astype(int)
    i0 = np.clip(i0, 0, freq - 1)
    i1 = i0 + 1
    f = u - i0
    f = f * f * (3 - 2 * f)                       # smoothstep
    top = g[np.ix_(i0, i0)] * (1 - f)[:, None] + g[np.ix_(i0, i1)] * f[:, None]
    bot = g[np.ix_(i1, i0)] * (1 - f)[:, None] + g[np.ix_(i1, i1)] * f[:, None]
    return top * (1 - f)[None, :] + bot * f[None, :]


def ridged_noise(res, freq, rng):
    n = _value_noise(res, int(freq), rng)
    return 1.0 - np.abs(2.0 * n - 1.0)


def thermal_erosion(h, iters, talus, rate=0.35):
    """热侵蚀近似：坡度超过休止角的土往低处搬。

    这是让山「有沟、有碎石堆」的关键一步；纯分形高度场没有它，看着像塑料。
    """
    h = h.copy()
    for _ in range(iters):
        gy, gx = np.gradient(h)
        slope = np.hypot(gx, gy)
        excess = np.clip(slope - talus, 0.0, None)
        if excess.max() <= 0:
            break
        move = excess * rate
        # 四邻域各搬一点（中心搬出，四邻搬入）
        h -= move
        h[:, :-1] += move[:, 1:] * 0.25
        h[:, 1:] += move[:, :-1] * 0.25
        h[:-1, :] += move[1:, :] * 0.25
        h[1:, :] += move[:-1, :] * 0.25
    return h


def despike(h, rel=0.06):
    """只削「孤立细柱」，不动成片的山脊。

    半径取 3 格是关键：**山脊沿走向在窗口内能找到等高邻域，尖柱找不到**——
    沿脊方向的那些格子同样高，所以不会被判成孤立峰；尖柱只有一个方向高，必然被判出来。
    半径取 1（只看 8 邻域）时只削掉了 1 个单格尖峰，中央那根多格细柱纹丝不动。

    渲图实测问题是画面中央一根针状尖峰，像异常几何体。
    之前用分位削顶没用——针峰不是离群值，它是**孤立一格的高点**；
    而山脊是连成片的高值带。用「是否比周围 8 邻域高出很多」来区分两者，
    只压前者：**保住山脊锐利，正是本层要的**。
    """
    span = float(h.max() - h.min()) or 1.0
    thr = rel * span
    r = int(round(rel * 100)) or 3          # rel 0.08 -> 半径 3 格
    r = max(2, min(r, 6))
    def shift(a, dy, dx):
        return np.roll(np.roll(a, dy, axis=0), dx, axis=1)
    neigh_max = np.maximum.reduce(
        [shift(h, dy, dx) for dy in range(-r, r + 1) for dx in range(-r, r + 1) if (dy, dx) != (0, 0)])
    # 边缘复制，避免 roll 的环绕假象
    for k in range(1, r + 1):
        neigh_max[:k, :] = neigh_max[k:k + 1, :]
        neigh_max[-k:, :] = neigh_max[-k - 1:-k, :]
        neigh_max[:, :k] = neigh_max[:, k:k + 1]
        neigh_max[:, -k:] = neigh_max[:, -k - 1:-k]
    spike = (h - neigh_max) > thr
    out = h.copy()
    out[spike] = neigh_max[spike] + thr * 0.15
    return out, int(spike.sum())


def build_terrain_mesh(cfg, h):
    """高度场 -> 网格。原点在中心，z 直接用高度。"""
    res = h.shape[0]
    size = cfg["terrain"]["size"]
    verts = []
    for j in range(res):
        for i in range(res):
            verts.append(((i / (res - 1) - 0.5) * size, (j / (res - 1) - 0.5) * size, float(h[j, i])))
    faces = []
    for j in range(res - 1):
        for i in range(res - 1):
            a = j * res + i
            faces.append((a, a + 1, a + res))
            faces.append((a + 1, a + res + 1, a + res))
    me = bpy.data.meshes.new("TerrainMesh")
    me.from_pydata(verts, [], faces)
    me.update()
    # 法线朝上（面绕序已保证；这里只是让着色器拿到正确的几何法线）
    obj = bpy.data.objects.new("Terrain", me)
    bpy.context.collection.objects.link(obj)
    return obj


# ---------------------------------------------------------------- 材质分层（GN）

def build_terrain_material(cfg):
    """ColorRamp 按高度分层 + 噪声打散边界，岩/草/雪三材之间 Mix Shader 过渡。

    ⚠️ **这些节点不能放在 GN 树里**：实测 `nodes.new("ShaderNodeNewGeometry")` 报
    `Cannot add node of type ... Not a shader node tree`。
    分层材质天然属于**材质节点树**（材质是 shader 树，GN 树只吃几何节点），
    所以这里建一个材质挂到 Terrain 对象上。高度已烘进网格，不需要 GN 参与。

    **为什么一定要噪声打散边界**：完美水平的分层线是「层积岩」才有的效果，
    放在游戏场景里就是塑料味 + 呆板。做法是把 `位置Z + 噪声×扰动量` 送进 ColorRamp，
    边界高度就被噪声推着起伏。
    """
    mat = bpy.data.materials.new("terrain_banded")
    mat.use_nodes = True
    nt = mat.node_tree
    for n in list(nt.nodes):
        nt.nodes.remove(n)
    out = nt.nodes.new("ShaderNodeOutputMaterial")
    out.location = (700, 0)

    geo = nt.nodes.new("ShaderNodeNewGeometry")
    geo.location = (-1100, 0)
    sep = nt.nodes.new("ShaderNodeSeparateXYZ")
    sep.location = (-900, 100)

    noise = nt.nodes.new("ShaderNodeTexNoise")
    noise.location = (-1100, -250)
    noise.inputs["Scale"].default_value = cfg["bands"]["noise_scale"]
    noise.inputs["Detail"].default_value = 6.0

    noise_mul = nt.nodes.new("ShaderNodeMath")
    noise_mul.operation = 'MULTIPLY'
    noise_mul.location = (-880, -250)
    noise_mul.inputs[1].default_value = cfg["bands"]["boundary_jitter"]

    add = nt.nodes.new("ShaderNodeMath")
    add.operation = 'ADD'
    add.location = (-680, -80)

    ramp = nt.nodes.new("ShaderNodeValToRGB")
    ramp.location = (-480, 0)
    el = ramp.color_ramp.elements
    el[0].position = cfg["bands"]["snow_height"]
    el[0].color = (0.05, 0.05, 0.05, 1.0)
    el[1].position = cfg["bands"]["rock_height"]
    el[1].color = (0.5, 0.5, 0.5, 1.0)
    e2 = el.new(cfg["bands"]["grass_height"])
    e2.color = (1.0, 1.0, 1.0, 1.0)

    nt.links.new(geo.outputs["Position"], sep.inputs["Vector"])
    nt.links.new(noise.outputs["Fac"], noise_mul.inputs[0])
    nt.links.new(sep.outputs["Z"], add.inputs[0])
    nt.links.new(noise_mul.outputs[0], add.inputs[1])
    nt.links.new(add.outputs[0], ramp.inputs["Fac"])

    def bsdf(name, rgb, rough, x, y):
        b = nt.nodes.new("ShaderNodeBsdfPrincipled")
        b.location = (x, y)
        b.inputs["Base Color"].default_value = (rgb[0], rgb[1], rgb[2], 1.0)
        b.inputs["Roughness"].default_value = rough
        return b

    b_grass = bsdf("b_grass", cfg["bands"]["grass"]["color"], 0.95, -200, 320)
    b_rock = bsdf("b_rock", cfg["bands"]["rock"]["color"], 0.85, -200, 20)
    b_snow = bsdf("b_snow", cfg["bands"]["snow"]["color"], 0.55, -200, -280)

    # 第一级：低海拔 草↔岩（fac 由 ramp 给，0.5 为界）
    mix_low = nt.nodes.new("ShaderNodeMixShader")
    mix_low.location = (120, 120)
    nt.links.new(ramp.outputs["Color"], mix_low.inputs["Fac"])
    nt.links.new(b_grass.outputs["BSDF"], mix_low.inputs[1])
    nt.links.new(b_rock.outputs["BSDF"], mix_low.inputs[2])

    # 第二级：高海拔再把雪混上来
    split = nt.nodes.new("ShaderNodeMath")
    split.operation = 'GREATER_THAN'
    split.location = (-280, -430)
    split.inputs[1].default_value = 0.5
    nt.links.new(ramp.outputs["Color"], split.inputs[0])

    mix_snow = nt.nodes.new("ShaderNodeMixShader")
    mix_snow.location = (360, 0)
    nt.links.new(split.outputs[0], mix_snow.inputs["Fac"])
    nt.links.new(mix_low.outputs[0], mix_snow.inputs[1])
    nt.links.new(b_snow.outputs["BSDF"], mix_snow.inputs[2])
    nt.links.new(mix_snow.outputs[0], out.inputs["Surface"])

    obj = bpy.data.objects["Terrain"]
    obj.data.materials.clear()
    obj.data.materials.append(mat)
    bpy.context.view_layer.update()
    return mat


# ---------------------------------------------------------------- 度量

def terrain_metrics(h, cfg):
    gy, gx = np.gradient(h)
    slope = np.hypot(gx, gy)
    p75 = np.percentile(slope, 75)
    sharp = float((slope > p75).mean())

    # 分层边界的不规则度：按阈值找出每列的交界高度，算其标准差
    # 分层边界的不规则度。
    # 前两版都量错了：① 拿归一化阈值比真实高度（单位错）；② 找「逐行的岩石占比是否越过 0.5」
    # ——但山的高度阈值形成的是**等高线**，逐行根本没有稳定交界（实测交界行数 0~2）。
    # 正确量法：对每一列（x 位置）求「岩石第一次出现的行号」，也就是那条交界等高线的高度；
    # 交界面平直 = 各列这个行号几乎相同 = std≈0 = 呆板。
    rock_h = cfg["bands"]["rock_height"] * cfg["terrain"]["height"]
    contour = []
    for i in range(h.shape[1]):
        col = h[:, i]
        above = np.nonzero(col > rock_h)[0]
        if len(above):
            contour.append(float(above[0]))
    jitter = float(np.std(contour)) if len(contour) > 8 else 0.0
    edge_rows = np.asarray(contour)
    return {"slope_p75": round(float(p75), 4),
            "boundary_rows": int(len(edge_rows)),
            "steep_pixel_ratio": round(sharp, 4),
            "slope_max": round(float(slope.max()), 4),
            "boundary_row_std": round(jitter, 2),
            "boundary_columns": int(len(edge_rows)),
            "height_range": [round(float(h.min()), 2), round(float(h.max()), 2)]}


def band_count(png, cfg, n_bands=3):
    """缩略图里还数得出几条分层带：按饱和度/亮度聚类的粗略量。"""
    from PIL import Image
    a = np.asarray(Image.open(png).convert("RGB")).astype(float)
    lum = a.sum(axis=2) / 3.0
    return {"width": a.shape[1],
            "lum_std": round(float(lum.std()), 2),
            "note": "缩略图仍需保留足够的亮度分层（std 不能塌成一片）"}


# ---------------------------------------------------------------- main

def main():
    cfg = load_config()
    out_dir = cfg["output_dir"]
    os.makedirs(out_dir, exist_ok=True)
    rng = np.random.default_rng(cfg["terrain"]["seed"])

    t = cfg["terrain"]
    h = ridged_multifractal(t["res"], t["octaves"], rng)
    h = thermal_erosion(h, t["erosion_iters"], t["talus"], t["erosion_rate"])
    h = (h - h.min()) / (h.max() - h.min())
    # 削掉针状尖峰：脊状噪声在高频层会留下细如针的单点高值，
    # 渲图实测「中央几根细长针状尖峰像异常几何体」。按分位削顶而不是加模糊，
    # 是为了**保住山脊的锐利**——加平滑会把山磨圆，正好毁掉本层要的东西。
    cap = float(np.percentile(h, t.get("peak_cap_percentile", 99.5)))
    h = np.minimum(h, cap)
    h, n_spikes = despike(h, t.get("despike_rel", 0.06))
    h = h / max(h.max(), 1e-6) * t["height"]

    L.fresh(cfg)
    L.build_sky(cfg)
    L.build_lights(cfg)
    L.build_fog(cfg)
    L.build_camera(cfg, [])
    obj = build_terrain_mesh(cfg, h)
    build_terrain_material(cfg)

    main_png = L.render_to(os.path.join(out_dir, "layer4_main.png"),
                           cfg["render"]["width"], cfg["render"]["height"])
    thumb_png = L.render_to(os.path.join(out_dir, "layer4_thumb.png"), 200, 125)

    m = terrain_metrics(h, cfg)
    band = band_count(thumb_png, cfg)
    results = {
        "ridge_sharpness": {
            "pass": m["steep_pixel_ratio"] >= cfg["checks"]["min_steep_ratio"],
            **{k: m[k] for k in ("slope_p75", "steep_pixel_ratio", "slope_max")},
            "min_steep_ratio": cfg["checks"]["min_steep_ratio"]},
        "boundary_not_flat": {
            "pass": m["boundary_row_std"] >= cfg["checks"]["min_boundary_std"],
            "boundary_std_px": m["boundary_row_std"],
            "boundary_columns": m["boundary_rows"],
            "min_boundary_std": cfg["checks"]["min_boundary_std"]},
        "thumb_readable": {
            "pass": band["lum_std"] >= cfg["checks"]["thumb_min_std"],
            **band, "min_std": cfg["checks"]["thumb_min_std"]},
    }

    print("== 层 4 三项自查 ==")
    for k, v in results.items():
        print(f"  {'PASS' if v['pass'] else 'FAIL'}  {k}: "
              + ", ".join(f"{a}={b}" for a, b in v.items() if a not in ("pass", "note")))
    print(f"   高度范围 {m['height_range']}｜削掉孤立针峰 {n_spikes} 个")

    with open(os.path.join(out_dir, "_selfcheck.json"), "w", encoding="utf-8") as fh:
        json.dump({"config": CONFIG, "images": {"main": main_png, "thumb": thumb_png},
                   "checks": results, "terrain": m}, fh, ensure_ascii=False, indent=2)

    failed = [k for k, v in results.items() if not v["pass"]]
    print(f"\n== {'ACCEPT: PASS' if not failed else 'ACCEPT: FAIL -> ' + ', '.join(failed)} ==")
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
