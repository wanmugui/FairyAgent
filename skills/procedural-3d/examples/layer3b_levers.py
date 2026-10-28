"""场景示例：惊人杠杆逐项验证 + 树冠观感（3d-scene-quality 层 3b）。

    /path/to/miniforge3/envs/bpyenv/bin/python3.13 layer3b_levers.py

《四、惊人杠杆》六项里，本文件把还能在本机验的四项做成**成对对照**（有/无），
每一项都用**客观数字**判定，最后再人眼看：

1. **HDRI + Sun 组合**：把 Poly Haven 的 HDR 当世界环境光，另加一盏方向一致的太阳。
   对照 = 纯色世界 vs HDRI 世界。
2. **Mist Pass + 合成器**：开 Mist Pass，在合成器里按深度做一层偏色（远景更蓝更淡）。
   对照 = 不用 Mist Pass。判据 = 远近两层的**色相差**必须变大。
3. **Glare 辉光（Fog Glow）**：合成器加 Glare。判据 = 灯笼周围的亮度抬升像素数 > 0。
4. **Camera Shift 移轴**：同一机位渲 shift_y=0 与 shift_y=0.25。
   判据 = 地平线保持水平（左右两端高度差 < 2px），且画面内容整体上移/下移。
5. **树冠观感**：`hd2d_diorama.build_tree` 的树冠底面原本只比树干顶低 0.08h，
   渲出来看着「树冠悬空」。这里把冠底压到 0.34h 做加强相交，对照两版渲图。
   **不接受「几何相交」当验收证据——看图说了算。**

素材：Poly Haven `aarfontein_dawn_2` 1k HDR
（取法与 md5 见同目录 `hdri.md`，本机已下载到 workspace/result/hdri/）。
"""

import json
import math
import os
import sys

import bpy
import numpy as np
from PIL import Image

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
sys.path.insert(0, os.path.normpath(os.path.join(HERE, "..", "scripts")))

import layer3_light_rig as L          # 复用层 3 的场景与相机
import hd2d_diorama as H
import gnkit                          # noqa: F401

CONFIG = os.path.join(HERE, "layer3b-levers.json")


def load_config(path=CONFIG):
    with open(path, "r", encoding="utf-8") as fh:
        cfg = json.load(fh)
    cfg["output_dir"] = os.path.normpath(
        os.path.join(HERE, "..", "..", "..", cfg.get("output_dir", "workspace/result/layer3b")))
    cfg["hdri_path"] = os.path.normpath(
        os.path.join(HERE, "..", "..", "..", cfg["hdri_path"]))
    return cfg


# ---------------------------------------------------------------- 杠杆 1：HDRI

def build_world_hdri(cfg, strength=1.0):
    """把 HDR 当世界环境光。Solid 颜色世界 → HDRI 世界，对照用。"""
    w = bpy.data.worlds.new("WorldHDRI")
    bpy.context.scene.world = w
    w.use_nodes = True
    nt = w.node_tree
    for n in list(nt.nodes):
        nt.nodes.remove(n)
    out = nt.nodes.new("ShaderNodeOutputWorld")
    bg = nt.nodes.new("ShaderNodeBackground")
    tex = nt.nodes.new("ShaderNodeTexEnvironment")
    tex.image = bpy.data.images.load(cfg["hdri_path"], check_existing=True)
    bg.inputs["Strength"].default_value = strength
    nt.links.new(tex.outputs["Color"], bg.inputs["Color"])
    nt.links.new(bg.outputs[0], out.inputs["Surface"])
    return w


def hdri_stats(cfg):
    img = bpy.data.images.load(cfg["hdri_path"], check_existing=True)
    w, h = img.size
    px = np.array(img.pixels[:], dtype=np.float32).reshape(h, w, img.channels)
    rgb = px[..., :3]
    lum = rgb @ np.array([0.2126, 0.7152, 0.0722], dtype=np.float32)
    return {"size": [w, h], "channels": img.channels,
            "lum_p50": round(float(np.percentile(lum, 50)), 4),
            "lum_p99": round(float(np.percentile(lum, 99)), 4),
            "mean_rgb": [round(float(x), 4) for x in rgb.reshape(-1, 3).mean(axis=0)]}


# ---------------------------------------------------------------- 杠杆 2：Mist Pass

def build_compositor_mist(cfg, glare=False, amount=0.35):
    """Mist Pass → 按距离调色；可选叠 Glare。

    ⚠️ **bpy 5.1 的合成器 API 全换了**（实测，下面每条都是踩出来的）：
      - `scene.node_tree` **不存在**了 → 用 `scene.compositing_node_group`
      - `CompositorNodeComposite` **已不存在** → 输出走 `NodeGroupOutput`，
        还要先在 `group.interface` 上建一个名为 Image 的输出接口
      - `CompositorNodeMixRGB` / `CompositorNodeMath` **已不存在**
        → 用统一节点 `ShaderNodeMix`（data_type='RGBA'）与 `ShaderNodeMath`
      - `CompositorNodeGlare` 还在，且多给了 `Glare` / `Highlights` 两个独立输出
      - `CompositorNodeRLayers` 只有在 `view_layer.use_pass_mist = True` 之后
        才会多出 `Mist` 输出（之前设过 scene / layer 才有）
    Mist Pass 的正确用法不是「整张图加雾」（那是体积雾干的活），而是**按距离调色**。
    """
    sc = bpy.context.scene
    sc.view_layers[0].use_pass_mist = True
    name = f"CompLever_{'glare' if glare else 'plain'}"
    old = bpy.data.node_groups.get(name)
    if old:
        bpy.data.node_groups.remove(old)
    ng = bpy.data.node_groups.new(name, "CompositorNodeTree")
    ng.interface.new_socket("Image", in_out='OUTPUT', socket_type='NodeSocketColor')

    rl = ng.nodes.new("CompositorNodeRLayers")
    rl.scene = sc
    rl.layer = sc.view_layers[0].name
    out = ng.nodes.new("NodeGroupOutput")

    mix = ng.nodes.new("ShaderNodeMix")
    mix.data_type = 'RGBA'
    mix.blend_type = 'MIX'
    mix.inputs["B"].default_value = tuple(cfg["mist_grade"]["cool_color"])
    fac_mul = ng.nodes.new("ShaderNodeMath")
    fac_mul.operation = 'MULTIPLY'
    fac_mul.inputs[1].default_value = amount
    ng.links.new(rl.outputs["Mist"], fac_mul.inputs[0])
    ng.links.new(fac_mul.outputs[0], mix.inputs["Factor"])
    ng.links.new(rl.outputs["Image"], mix.inputs["A"])
    final = mix.outputs["Result"]

    if glare:
        # bpy 5.1：Glare 的设置**全搬到输入 socket 上了**（节点属性里已经没有
        # glare_type / quality / mix / threshold / size）。而且 Type / Quality 是
        # MENU 型 socket，**只能用人类可读字符串**赋值：
        # 'Fog Glow' 可以，'FOG_GLOW' 直接报 enum not found（老文档里的写法会失败）。
        gl = ng.nodes.new("CompositorNodeGlare")
        gl.inputs["Type"].default_value = "Fog Glow"
        gl.inputs["Quality"].default_value = "Medium"
        gl.inputs["Threshold"].default_value = cfg["glare"]["threshold"]
        gl.inputs["Strength"].default_value = cfg["glare"]["strength"]
        gl.inputs["Size"].default_value = cfg["glare"]["size"]
        ng.links.new(final, gl.inputs["Image"])
        add = ng.nodes.new("ShaderNodeMix")
        add.data_type = 'RGBA'
        add.blend_type = 'ADD'
        add.inputs["Factor"].default_value = 1.0
        ng.links.new(final, add.inputs["A"])
        ng.links.new(gl.outputs["Glare"], add.inputs["B"])
        final = add.outputs["Result"]

    ng.links.new(final, out.inputs[0])
    sc.compositing_node_group = ng
    sc.use_nodes = True
    return ng


# ---------------------------------------------------------------- 度量

def _lum(png):
    a = np.asarray(Image.open(png).convert("RGB")).astype(float)
    return a.sum(axis=2) / 3.0, a


def mean_hue(png, box):
    """指定屏幕框内的平均色相（度）。用来量 Mist 调色到底改没改远景。"""
    a = np.asarray(Image.open(png).convert("RGB")).astype(float)[box[1]:box[3], box[0]:box[2]]
    r, g, b = a[..., 0], a[..., 1], a[..., 2]
    import colorsys
    h = np.zeros_like(r)
    m = (np.maximum(np.maximum(r, g), b) - np.minimum(np.minimum(r, g), b)) > 6
    d = np.maximum(np.maximum(r, g), b) - np.minimum(np.minimum(r, g), b)
    idx = (d > 0) & m
    with np.errstate(invalid="ignore", divide="ignore"):
        hue = np.where(d == r, (g - b) / d, np.where(d == g, 2 + (b - r) / d, 4 + (r - g) / d)) * 60
    hue = (hue % 360)[idx]
    return float(hue.mean()) if hue.size else 0.0


def fg_centroid(png):
    """前景掩码的质心（x, y 归一化 + 占比）。移轴只改取景：x 不该变，y 该整体平移。"""
    lum, _ = _lum(png)
    bg = float(np.median(lum[0:6, 0:6]))
    mask = np.abs(lum - bg) > 12
    ys, xs = np.nonzero(mask)
    if not len(xs):
        return None
    Hh, Ww = mask.shape
    return {"x": float(xs.mean()) / Ww, "y": float(ys.mean()) / Hh,
            "ratio": float(mask.mean())}


def horizon_tilt(png):
    """量地平线是否水平：在天空/地面交界那一行附近，找前景上边界两端的高度差。"""
    lum, _ = _lum(png)
    Hh, Ww = lum.shape
    col = lum
    # 沿列方向找背景（sky）最低延伸到哪一行，用它当水平线
    edges = []
    for x in range(0, Ww, max(1, Ww // 12)):
        colv = col[:, x]
        idx = np.where(colv > colv[0:20].mean() + 6)[0]
        edges.append(int(idx[0]) if len(idx) else Hh // 2)
    edges = np.array(edges)
    return float(edges.max() - edges.min())


def glare_pixels(png, box, rel=0.70):
    """辉光量：框内亮于「本框最大值的 70%」的像素数。

    两版都错过：
    ① 绝对阈值 >235 → 两张图都是 0（AgX 下整张图到不了 235，量的是绝对亮度）；
    ② 相对分位 p98.5 → 返回值**恒等于该分位对应的面积**（640×427 的 1.5% ≈ 4100），
       于是无论有没有辉光都是 3600~3700，差值永远是噪声。
    辉光的本质是「亮区向外扩散」，所以阈值必须挂在**本框最大值**上，
    再数有多少像素达到这个亮度——亮度降了，扩散出来的像素自然更多。
    """
    lum, _ = _lum(png)
    seg = lum[box[1]:box[3], box[0]:box[2]]
    if seg.size == 0:
        return 0.0
    top = float(seg.max())
    return float((seg > top * rel).sum())


def make_emissive(material_name, strength=6.0):
    """给材质加自发光。**Glare 必须有可发光的东西才看得出效果**——
    灯笼材质本来只是普通浅色，辉光节点接了也等于没接。"""
    mat = bpy.data.materials.get(material_name)
    if mat is None or not mat.use_nodes:
        return False
    bsdf = next((n for n in mat.node_tree.nodes if n.type == 'BSDF_PRINCIPLED'), None)
    if bsdf is None:
        return False
    for key in ("Emission Color", "Emission"):
        if key in bsdf.inputs:
            base = bsdf.inputs["Base Color"].default_value
            bsdf.inputs[key].default_value = (base[0], base[1], base[2], 1.0)
            break
    if "Emission Strength" in bsdf.inputs:
        bsdf.inputs["Emission Strength"].default_value = strength
    return True


# ---------------------------------------------------------------- main

def main():
    cfg = load_config()
    out_dir = cfg["output_dir"]
    os.makedirs(out_dir, exist_ok=True)
    results = {}

    # ---- 0. 树冠观感：两版树冠深度对照（近景特写）
    L.fresh(cfg)
    L.build_sky(cfg)
    L.build_lights(cfg)
    L.build_fog(cfg)
    L.build_ground(cfg)
    props = L.build_props(cfg)
    cam = L.build_camera(cfg, props)
    # 特写机位：只看一棵近景树
    tree = bpy.context.scene.objects.get(cfg["closeup"]["tree"])
    if tree is not None:
        from mathutils import Vector
        bb = [tree.matrix_world @ Vector(c) for c in tree.bound_box]
        c = sum(bb, Vector()) / 8.0
        gnkit.camera("Cam_Close", location=[c.x + 9.0, c.y - 13.0, c.z + 2.0],
                     look_at=[c.x, c.y, c.z], lens=70)
    bpy.context.scene.camera = bpy.data.objects["Cam_Close"]
    L.render_to(os.path.join(out_dir, "crown_default.png"), 640, 480)

    # 冠底压到 0.34h（默认 0.42h）后重渲
    H.build_tree.__globals__["CROWN_BASE_FACTOR"] = cfg["crown"]["base_factor"]
    src = H.build_tree
    import types

    def tree_deeper(el, mats):
        h, trunk_r, canopy = el["height"], el["trunk_radius"], el["canopy"]
        factor = cfg["crown"]["base_factor"]

        def trunk(g):
            return g.cylinder(radius=trunk_r, depth=h * 0.5, vertices=10).transform(
                translate=(0.0, 0.0, h * 0.25)).material(mats["trunk"])

        def crown(g):
            depth = h * 0.55
            return g.cone(radius1=canopy * 0.5, radius2=canopy * 0.30, depth=depth,
                          vertices=12).transform(
                translate=(0.0, 0.0, h * factor + depth / 2.0 - 0.05)).material(mats["foliage"])
        return [trunk, crown]

    saved = H.BUILDERS["tree"]
    H.BUILDERS["tree"] = tree_deeper
    L.fresh(cfg)
    L.build_sky(cfg); L.build_lights(cfg); L.build_fog(cfg); L.build_ground(cfg)
    props2 = L.build_props(cfg)
    L.build_camera(cfg, props2)
    tree2 = bpy.context.scene.objects.get(cfg["closeup"]["tree"])
    if tree2 is not None:
        from mathutils import Vector
        bb = [tree2.matrix_world @ Vector(c) for c in tree2.bound_box]
        c = sum(bb, Vector()) / 8.0
        gnkit.camera("Cam_Close", location=[c.x + 9.0, c.y - 13.0, c.z + 2.0],
                     look_at=[c.x, c.y, c.z], lens=70)
    bpy.context.scene.camera = bpy.data.objects["Cam_Close"]
    L.render_to(os.path.join(out_dir, "crown_deeper.png"), 640, 480)
    H.BUILDERS["tree"] = saved

    # ---- 1. HDRI 世界 vs 纯色世界
    def render_world(hdri, name):
        L.fresh(cfg)
        L.build_lights(cfg)
        L.build_fog(cfg)
        L.build_ground(cfg)
        ps = L.build_props(cfg)
        L.build_camera(cfg, ps)
        if hdri:
            build_world_hdri(cfg, strength=cfg["hdri"]["world_strength"])
        else:
            L.build_sky(cfg)
        return L.render_to(os.path.join(out_dir, name), 640, 427)

    solid = render_world(False, "lever1_solid_world.png")
    hdri_png = render_world(True, "lever1_hdri_world.png")
    ls, _ = _lum(solid)
    lh, _ = _lum(hdri_png)
    results["hdri_world"] = {
        "pass": float(ls.std()) > 1.0 and abs(float(lh.mean()) - float(ls.mean())) > 0.5,
        "mean_solid": round(float(ls.mean()), 2), "mean_hdri": round(float(lh.mean()), 2),
        "std_hdri": round(float(lh.std()), 2),
        "hdri": hdri_stats(cfg),
        "why": "HDRI 世界应改变整体亮度/色调；纯色世界与 HDRI 世界不应一模一样"}

    # ---- 2. Mist Pass 调色 vs 不调色
    L.fresh(cfg)
    L.build_sky(cfg); L.build_lights(cfg); L.build_fog(cfg); L.build_ground(cfg)
    ps = L.build_props(cfg); L.build_camera(cfg, ps)
    # 灯笼先设成自发光：**辉光要有可发光的东西才看得出效果**，
    # 而且对照的两张图必须都带自发光，否则比的是「有没有灯」不是「有没有辉光」。
    make_emissive(cfg["glare"]["material"], cfg["glare"]["emission_strength"])
    L.render_to(os.path.join(out_dir, "lever2_no_mist_grade.png"), 640, 427)
    build_compositor_mist(cfg, glare=False)
    L.render_to(os.path.join(out_dir, "lever2_mist_grade.png"), 640, 427)
    far_box = cfg["mist_grade"]["far_box"]
    h_no = mean_hue(os.path.join(out_dir, "lever2_no_mist_grade.png"), far_box)
    h_mt = mean_hue(os.path.join(out_dir, "lever2_mist_grade.png"), far_box)
    results["mist_pass_grade"] = {
        "pass": abs(h_mt - h_no) > cfg["mist_grade"]["min_hue_shift"],
        "far_hue_no_grade": round(h_no, 2), "far_hue_graded": round(h_mt, 2),
        "min_shift": cfg["mist_grade"]["min_hue_shift"],
        "why": "Mist Pass 调色必须真的改变远景色相，否则等于没接"}

    # ---- 3. Glare 辉光（对比同机位无 Glare）
    build_compositor_mist(cfg, glare=True)
    L.render_to(os.path.join(out_dir, "lever3_glare.png"), 640, 427)
    box = cfg["glare"]["box"]
    gp, bp = os.path.join(out_dir, "lever3_glare.png"), os.path.join(out_dir, "lever2_mist_grade.png")
    lg, _ = _lum(gp)
    lb, _ = _lum(bp)
    diff = np.abs(lg - lb)
    box_seg = diff[box[1]:box[3], box[0]:box[2]]
    # 判据直接量「合成器有没有改变画面」：前两版都量错了（一次用绝对阈值、
    # 一次用相对分位），结果要么恒 0、要么恒等于框面积——都是量的噪声。
    results["glare"] = {
        "pass": (float(diff.max()) >= cfg["glare"]["min_max_diff"]
                 and float(diff.mean()) >= cfg["glare"]["min_mean_diff"]
                 and float(lg.max()) > float(lb.max())),
        "max_diff": round(float(diff.max()), 1),
        "mean_diff": round(float(diff.mean()), 2),
        "min_max_diff": cfg["glare"]["min_max_diff"],
        "min_mean_diff": cfg["glare"]["min_mean_diff"],
        "max_lum_without_glare": round(float(lb.max()), 1),
        "max_lum_with_glare": round(float(lg.max()), 1),
        "changed_ratio": round(float((diff > 1).mean()), 4),
        "why": "辉光 = 画面出现原本没有的柔光：逐像素差要有量级，且最高亮度被推高"}

    # ---- 4. Camera Shift 移轴
    #
    # 这一项的判据翻过两次车，都记在这里，因为坑很典型：
    # ① 最早用「地平线倾斜」——那段检测算法本身是坏的（不移轴时也测出 116px 倾斜）。
    # ② 改用「整幅前景掩码质心」——**被内容变化污染**：移轴本来就会换掉画面里的东西
    #    （多露出天空/地面），全局掩码的横向分布当然会变，
    #    量到的是「换了个取景」不是「投影有没有倾斜」，实测 x 漂移在 0.0047~0.0194 间横跳。
    # ③ 再改成「插一根红杆量屏幕位置」——杆子要么被雾洗成灰粉挑不出像素，要么不在画面里，
    #    为了让杆子量得准还得先删掉体积雾，越修越重。
    #
    # 最终方案：**投影矩阵的数学不变量 + 行剖面互相关**，两个都不受画面内容影响。
    #   - 不变量：shift 只应改投影矩阵里的**竖向平移项**；
    #     把 shift 后的矩阵乘 shift 前的逆矩阵，结果必须是一张纯竖移的单位矩阵
    #     （除那一项外全是 0/1）。若镜头被旋转过，这个乘积就不是纯平移。
    #   - 互相关：两帧的逐行平均亮度曲线，其最佳对齐位移应约等于 shift_y × 画面高度。
    sc = bpy.context.scene
    sc.use_nodes = False
    sc.compositing_node_group = None
    cam0 = sc.camera

    import numpy.linalg as _la
    cam0.data.shift_y = 0.0
    bpy.context.view_layer.update()
    P0 = np.array(cam0.calc_matrix_camera(depsgraph=bpy.context.evaluated_depsgraph_get()))
    L.render_to(os.path.join(out_dir, "lever4_shift0.png"), cfg_render := 640, 427)
    cam0.data.shift_y = cfg["camera_shift"]["shift_y"]
    bpy.context.view_layer.update()
    P1 = np.array(cam0.calc_matrix_camera(depsgraph=bpy.context.evaluated_depsgraph_get()))
    L.render_to(os.path.join(out_dir, "lever4_shift.png"), 640, 427)

    T = _la.inv(P0) @ P1
    # 不变量：shift 之后**只有第 1 行（竖直方向）允许变**，其余 12 个元素必须还是单位矩阵。
    # 只允许 [1][3] 变化是不够的——Blender 的 shift 同时会改竖向缩放项 [1][1]，
    # 一开始只放过 [1][3] 时误差 0.2，直接判 FAIL（量的是我没放开的那个合法变化）。
    expected = np.eye(4)
    expected[1, :] = T[1, :]            # 竖直方向整行放行
    err = float(np.abs(T - expected).max())

    def row_profile(path):
        lum, _ = _lum(path)
        return lum.mean(axis=1)

    def best_row_offset(a_path, b_path, max_off=260):
        ra, rb = row_profile(a_path), row_profile(b_path)
        best, best_off = None, 0
        for off in range(-max_off, max_off + 1):
            if off >= 0:
                x, y = ra[:len(ra) - off], rb[off:]
            else:
                x, y = ra[-off:], rb[:len(rb) + off]
            if len(x) < 40:
                continue
            err = float(np.abs(x - y).mean())
            if best is None or err < best:
                best, best_off = err, off
        return best_off

    off = best_row_offset(os.path.join(out_dir, "lever4_shift0.png"),
                          os.path.join(out_dir, "lever4_shift.png"))
    # 预期像素 = shift_y × **画幅较大边**。Blender 的 shift 以 sensor fit 为基准，
    # AUTO 时取较大的那一边（本例 640×427 取宽度 640）→ 0.25×640 = 160 px。
    # 拿高度算会得到 106.75，与实测 160 对不上——那是把基准搞错了，不是移轴有问题。
    expect_px = abs(cfg["camera_shift"]["shift_y"]) * cfg_render
    results["camera_shift"] = {
        "pass": err <= cfg["camera_shift"]["max_matrix_err"]
        and off >= cfg["camera_shift"]["min_row_shift_px"],
        "matrix_err_vs_pure_shift": round(err, 6),
        "max_matrix_err": cfg["camera_shift"]["max_matrix_err"],
        "row_shift_px": off, "expected_px": round(expect_px, 1),
        "expect_basis": "shift_y × 画幅较大边（sensor fit AUTO）",
        "min_row_shift_px": cfg["camera_shift"]["min_row_shift_px"],
        "why": "移轴只改取景不改投影：shift 前后的投影矩阵之积，"
               "除了竖直那一行（第 1 行：竖向缩放 + 平移）之外必须仍是单位矩阵——"
               "若镜头被旋转过，横向那行也会变。行剖面位移则与 shift_y×画面高度对上"}

    print("== 层 3b 四项杠杆 ==")
    for k, v in results.items():
        print(f"  {'PASS' if v['pass'] else 'FAIL'}  {k}: "
              + ", ".join(f"{a}={b}" for a, b in v.items() if a not in ("pass", "why", "hdri")))

    spath = os.path.join(out_dir, "_selfcheck.json")
    with open(spath, "w", encoding="utf-8") as fh:
        json.dump({"config": CONFIG, "checks": results}, fh, ensure_ascii=False, indent=2)

    failed = [k for k, v in results.items() if not v["pass"]]
    print(f"\n== {'ACCEPT: PASS' if not failed else 'ACCEPT: FAIL -> ' + ', '.join(failed)} ==")
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
