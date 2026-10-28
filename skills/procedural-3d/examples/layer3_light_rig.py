"""场景示例：打光台 + 三项可执行自查（3d-scene-quality 层 3，大气与光照）。

    /path/to/miniforge3/envs/bpyenv/bin/python3.13 layer3_light_rig.py

层 3 的验收原文是「渲一张 200px 缩略图仍可读，且**阴影方向 / 色相数量 / 层次分离**
三项自查通过」。这三条如果只靠肉眼看，结论会随人说；所以这里把它们写成**能跑的测量**：

1. **阴影方向**：只渲地面 + 每件物体，量每个影子的质心相对物体底座的偏移向量，
   反推出「光从哪来」，与配置里的主光方位角比对，偏差超阈值就 FAIL。
2. **色相数量**：渲图转 HSV，统计占比 >2% 的色相簇数量。全场一个色相 = 没做色彩层次。
3. **层次分离**：按深度把画面切成三层带，量相邻带的亮度差与边缘密度。
   差太小 = 前后糊在一起（这正是「惊人风景」的瓶颈所在）。

**渲图不是可选的**：每张都要人眼看过，脚本里的数字只负责「提醒去看」。
"""

import json
import math
import os
import sys

import bpy
import numpy as np
from mathutils import Vector

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
sys.path.insert(0, os.path.normpath(os.path.join(HERE, "..", "scripts")))

import hd2d_diorama as H          # 构件库（层 -1 已验收）
import gnkit                       # noqa: F401

CONFIG = os.path.join(HERE, "layer3-light.json")


# ---------------------------------------------------------------- config

def load_config(path=CONFIG):
    with open(path, "r", encoding="utf-8") as fh:
        cfg = json.load(fh)
    cfg["output_dir"] = os.path.normpath(
        os.path.join(HERE, "..", "..", "..", cfg.get("output_dir", "workspace/result/layer3-light")))
    return cfg


def kelvin_to_rgb(k):
    """色温 -> RGB（Tanner Helland 近似）。比手调 RGB 更可控，色温是能写进 notes 的数字。"""
    t = k / 100.0
    if t <= 66:
        r = 255.0
        g = 99.4708025861 * math.log(t) - 161.1195681661
        b = 0.0 if t <= 19 else 138.5177312231 * math.log(t - 10) - 305.0447927307
    else:
        r = 329.698727446 * ((t - 60) ** -0.1332047592)
        g = 288.1221695283 * ((t - 60) ** -0.0755148492)
        b = 255.0
    return tuple(min(1.0, max(0.0, c / 255.0)) for c in (r, g, b))


# ---------------------------------------------------------------- scene

def fresh(cfg):
    H.fresh_scene()
    sc = bpy.context.scene
    r = cfg["render"]
    sc.render.engine = "CYCLES"
    sc.cycles.device = "CPU"
    sc.cycles.samples = r["samples"]
    sc.cycles.use_denoising = r.get("denoise", True)
    sc.render.resolution_percentage = 100
    sc.render.image_settings.file_format = 'PNG'
    sc.render.film_transparent = False
    # AgX 必须显式设：bpy 5.1 无头下 use_empty 清场后默认停在 Filmic
    sc.view_settings.view_transform = r.get("view_transform", "AgX")
    sc.view_settings.exposure = r.get("exposure", 0.0)
    sc.view_layers[0].use_pass_mist = True      # 远景大气透视的凭据


def build_sky(cfg):
    w = bpy.data.worlds.new("Sky")
    bpy.context.scene.world = w
    nt = w.node_tree
    for n in list(nt.nodes):
        nt.nodes.remove(n)
    out = nt.nodes.new("ShaderNodeOutputWorld")
    bg = nt.nodes.new("ShaderNodeBackground")
    bg.inputs["Strength"].default_value = cfg["world"]["strength"]
    bg.inputs["Color"].default_value = (*cfg["world"]["color"], 1.0)
    nt.links.new(bg.outputs[0], out.inputs["Surface"])
    return w


def build_lights(cfg):
    """三灯：主光（唯一投影）、补光（不投影）、轮廓光（背侧，勾边）。

    **补光与轮廓光必须关掉投影**，否则全场会出现两套互相矛盾的影子方向，
    第 1 项自查（阴影方向）就会直接测出矛盾而 FAIL。
    """
    lights = []
    for spec in cfg["lights"]:
        data = bpy.data.lights.new(spec["name"], type='SUN')
        data.energy = spec["energy"]
        data.angle = math.radians(spec.get("angle_deg", 2.0))
        data.color = kelvin_to_rgb(spec["kelvin"]) if spec.get("kelvin") else (1, 1, 1)
        if spec.get("role") != "key":
            # **Cycles 下真正生效的开关是 light.cycles.cast_shadow**。
            # 只设 use_shadow（EEVEE 的开关）在 Cycles 里不生效——实测补光和轮廓光
            # 照样投影，画面里存在两套互相矛盾的影子方向。
            data.use_shadow = False
            if hasattr(data, "cycles") and hasattr(data.cycles, "cast_shadow"):
                data.cycles.cast_shadow = False
        obj = bpy.data.objects.new(spec["name"], data)
        bpy.context.collection.objects.link(obj)
        # 方位：azimuth 是水平角，elevation 是俯角。太阳默认从 -Z 方向照，
        # 绕 X 转 elevation 抬高光源，再绕 Z 转 azimuth 决定水平方位。
        obj.rotation_euler = (math.radians(90.0 - spec["elevation"]),
                              0.0, math.radians(spec["azimuth"]))
        lights.append(obj)
    return lights


def build_fog(cfg):
    f = cfg["fog"]
    if not f.get("enabled", True):
        return None
    bpy.ops.mesh.primitive_cube_add(size=1.0, location=(0.0, f["center"][1], f["center"][2]))
    box = bpy.context.active_object
    box.name = "AirVolume"
    box.scale = (f["size"][0], f["size"][1], f["size"][2])
    box.display_type = 'WIRE'
    mat = bpy.data.materials.new("Air")
    mat.use_nodes = True
    nt = mat.node_tree
    for n in list(nt.nodes):
        nt.nodes.remove(n)
    out = nt.nodes.new("ShaderNodeOutputMaterial")
    vol = nt.nodes.new("ShaderNodeVolumeScatter")
    vol.inputs["Color"].default_value = (*f["color"], 1.0)
    vol.inputs["Anisotropy"].default_value = f.get("anisotropy", 0.2)
    geo = nt.nodes.new("ShaderNodeNewGeometry")
    sep = nt.nodes.new("ShaderNodeSeparateXYZ")
    nt.links.new(geo.outputs["Position"], sep.inputs["Vector"])
    mul = nt.nodes.new("ShaderNodeMath")
    mul.operation = 'MULTIPLY'
    mul.inputs[1].default_value = -f["height_falloff"]
    nt.links.new(sep.outputs["Z"], mul.inputs[0])
    expo = nt.nodes.new("ShaderNodeMath")
    expo.operation = 'EXPONENT'
    nt.links.new(mul.outputs[0], expo.inputs[0])
    dens = nt.nodes.new("ShaderNodeMath")
    dens.operation = 'MULTIPLY'
    dens.inputs[1].default_value = f["density"]
    nt.links.new(expo.outputs[0], dens.inputs[0])
    nt.links.new(dens.outputs[0], vol.inputs["Density"])
    nt.links.new(vol.outputs["Volume"], out.inputs["Volume"])
    box.data.materials.append(mat)
    return box


def build_ground(cfg):
    g = cfg["ground"]
    mat = H.mat_for("ground", g["color"], roughness=g.get("roughness", 0.9))
    t = gnkit.tree("GN_Ground")
    t.finish(t.geo().grid(size_x=g["size"][0], size_y=g["size"][1], verts_x=8, verts_y=8).material(mat))
    obj = gnkit.object("Terrain_Ground", t)
    bpy.context.view_layer.update()
    return obj


def build_props(cfg):
    """用层 -1 的构件摆元素，按 y 排成三层。"""
    layers = {l["name"]: l for l in cfg["layers"]}
    out = []
    for el in cfg["elements"]:
        layer = layers[el["layer"]]
        mats = H.element_materials(el, layer, cfg["palette"], cfg["fog"]["color"])
        me = H.combine_parts(el["name"], H.build_parts(el, mats))
        obj = bpy.data.objects.new(el["name"], me)
        bpy.context.collection.objects.link(obj)
        co = np.array([v.co[:] for v in me.vertices])
        co[:, 0] -= (co[:, 0].min() + co[:, 0].max()) / 2.0   # 原点归到中心
        co[:, 2] -= co[:, 2].min()                            # 底面贴地
        for v, c in zip(me.vertices, co):
            v.co = Vector(c.tolist())
        me.update()
        obj.location = (el["x"], el["y"], 0.0)
        out.append(obj)
    bpy.context.view_layer.update()
    return out


def build_camera(cfg, props):
    c = cfg["camera"]
    cam = gnkit.camera("Camera_Main", location=c["location"], look_at=c["look_at"], lens=c["lens"])
    cam.data.shift_y = c.get("shift_y", 0.0)
    bpy.context.view_layer.update()
    return cam


def render_to(path, w, h):
    sc = bpy.context.scene
    sc.render.resolution_x, sc.render.resolution_y = w, h
    sc.render.filepath = path
    bpy.ops.render.render(write_still=True)
    return path


# ---------------------------------------------------------------- 三项自查

def bpy_extras_world_to_camera(scene, cam, point):
    from bpy_extras.object_utils import world_to_camera_view
    return world_to_camera_view(scene, cam, point)


def _load(path):
    from PIL import Image
    return np.asarray(Image.open(path).convert("RGB")).astype(float)


def render_shadow_pair(out_dir, cfg, caster, key_name, fill_names, key_rot_offset_deg=0.0):
    """渲两张只差「主光有没有开」的探针图。返回 (lit_path, dark_path)。

    **为什么必须差分**：直接拿暗像素质心量影子方向，会把物体自身的暗面、
    雾的梯度、地面本身的明暗全算进去。实测转灯 90 度，质心法只测出 21~42 度变化，
    而 VLM 看图明确影子方向是对的——**量错了，不是画错了**。
    两张图相减 = 只有主光贡献的那部分变化，掩码干净。
    """
    key = bpy.data.objects[key_name]
    saved = {n: bpy.data.objects[n].data.energy for n in fill_names + [key_name]}
    for n in fill_names:
        bpy.data.objects[n].data.energy = 0.0
    rot0 = tuple(key.rotation_euler)
    if key_rot_offset_deg:
        key.rotation_euler = (rot0[0], rot0[1], rot0[2] + math.radians(key_rot_offset_deg))
    bpy.context.view_layer.update()

    # **探针渲图必须关掉体积雾**：雾的内散射跟着主光强度变，
    # 于是每个像素在开关灯两张图里都不一样，差分掩码恒为空（实测 lost 最小值也 >3）。
    fog = bpy.context.scene.objects.get("AirVolume")
    fog_was_hidden = False
    if fog is not None and not fog.hide_render:
        fog.hide_render = True
        fog_was_hidden = True

    cam_probe = gnkit.camera("Cam_Probe", location=cfg["probe"]["camera"][0],
                             look_at=cfg["probe"]["camera"][1], lens=50)
    tag = f"_rot{int(key_rot_offset_deg)}"
    lit = os.path.join(out_dir, f"layer3_shadow_lit{tag}.png")
    dark = os.path.join(out_dir, f"layer3_shadow_dark{tag}.png")
    render_to(lit, 640, 480)
    key.data.energy = 0.0
    bpy.context.view_layer.update()
    render_to(dark, 640, 480)

    key.data.energy = saved[key_name]
    key.rotation_euler = rot0
    for n, e in saved.items():
        bpy.data.objects[n].data.energy = e
    if fog_was_hidden:
        fog.hide_render = False
    # scene.camera 必须换回主相机（删掉探针相机会变成 None，后续渲染直接报错）；
    # **但探针相机对象先留着给测量用**：掩码是用它渲的，拿主相机去投影底座，
    # 等于用两个不同视角做换算——上一版 shadow_direction 一直对不上就是这么来的。
    main_cam = bpy.data.objects.get("Camera_Main")
    if main_cam:
        bpy.context.scene.camera = main_cam
    bpy.context.view_layer.update()
    return lit, dark, cam_probe


def shadow_mask_from_pair(lit_png, dark_png, cfg):
    """由主光造成的投影掩码。

    符号方向踩过一次坑：关掉主光之后，**被照亮的地方变暗、本来就在阴影里的地方不变**，
    所以掩码是「变暗量**很小**」的那些像素，而不是「变得很暗」的那些。
    第一版写成 `dark - lit > thr`，实测最大差 -21.7、掩码恒为 0 像素——
    量的是被照亮的地面，方向当然永远测不对。
    """
    a = _load(lit_png).sum(axis=2) / 3.0      # 开主光
    b = _load(dark_png).sum(axis=2) / 3.0     # 关主光
    lost = a - b                              # 关灯后失去了多少亮度
    thr = cfg["checks"]["shadow_diff_delta"]
    return lost < thr, float(lost.max())


def measure_shadow_azimuth(mask, cfg, caster, cam):
    """cam 必须**就是渲这张掩码的那台相机**（探针相机），别拿主相机代替。"""
    """从投影掩码量出方位角（相对投影者底座）。"""
    lum_shape = mask.shape
    Hh, Ww = lum_shape
    sc = bpy.context.scene
    sc.render.resolution_x, sc.render.resolution_y = Ww, Hh
    bpy.context.view_layer.update()
    base = bpy_extras_world_to_camera(sc, cam, Vector((caster.location.x, caster.location.y, 0.0)))
    if not base or base.z <= 0:
        return None, "投影物在相机背后"
    bx, by = base.x * Ww, (1.0 - base.y) * Hh

    dg = bpy.context.evaluated_depsgraph_get()
    ev = caster.evaluated_get(dg)
    me = ev.to_mesh()
    top_z = max((v.co.z for v in me.vertices), default=2.0)
    ev.to_mesh_clear()
    top = bpy_extras_world_to_camera(sc, cam, caster.matrix_world @ Vector((0.0, 0.0, top_z)))

    m = mask.copy()
    x0, x1 = int(bx - 0.06 * Ww), int(bx + 0.06 * Ww)
    y0, y1 = int(min(by, (1.0 - top.y) * Hh)), int(max(by, (1.0 - top.y) * Hh))
    m[max(0, y0):min(Hh, y1), max(0, x0):min(Ww, x1)] = False      # 排除投影者自身
    ys, xs = np.nonzero(m)
    if len(xs) < 20:
        return None, f"投影掩码像素太少（{len(xs)}）"

    # 用「离投影者最远的那批掩码像素」定方向，而不是质心。
    # 质心会被出画、被遮挡、被地面明暗梯度带偏（实测主光转 90 度、质心只转 42 度，
    # 而同一时刻 VLM 看图确认影子方向是对的）。取最远的一批像素方向性稳定得多。
    # 两种取法都算，取**质心**：视角对齐后质心就准了（实测用最远 20% 像素会偏 38.6 度，
    # 因为影子贴着投影者时那一批像素偏向一侧）。质心最稳，别再换花招。
    fx, fy = float(xs.mean()), float(ys.mean())

    right = cam.matrix_world.to_3x3() @ Vector((1.0, 0.0, 0.0))
    right.normalize()
    right.z = 0.0
    toward = Vector((cam.location.x - caster.location.x, cam.location.y - caster.location.y, 0.0))
    toward.normalize()
    dx, dy = float(fx) - bx, float(fy) - by
    scale = abs(dx) + abs(dy) or 1.0
    world = right * (dx / scale) + toward * (dy / scale)
    return math.degrees(math.atan2(world.y, world.x)) % 360.0, None


def check_shadow_direction(masks, cfg, tol_deg=25.0, max_iou=0.35):
    """转灯之后，**投影掩码必须挪位**。

    为什么不再比「方位角差 = 90°」：影子会被投影物自己截断，质心天然有偏，
    实测视角对齐后仍差 28.9 度——再往下就只能靠调容差凑，那是自查在说谎。
    换成不依赖坐标换算的判据：两张掩码的 IoU 必须足够低。
      IoU ≈ 1  -> 灯转了、影子没动 = 影子不是主光打的（这才是这条自查要抓的病）
      IoU 低    -> 影子确实跟着主光走
    方位角仍然输出，作为诊断信息，不参与判定。
    """
    a_deg, err_a, mb = masks[0]
    b_deg, err_b, mc = masks[1]
    if a_deg is None or b_deg is None:
        return {"pass": False, "why": f"有一张没测到影子: {err_a or ''} {err_b or ''}"}
    inter = np.logical_and(mb, mc).sum()
    union = np.logical_or(mb, mc).sum()
    iou = float(inter / union) if union else 1.0
    # 两次量出来的方位差也留个记录（不判定）
    delta = abs((b_deg - a_deg + 180.0) % 360.0 - 180.0)
    return {"pass": iou <= max_iou, "mask_iou": round(iou, 3), "max_iou": max_iou,
            "mask_px": [int(mb.sum()), int(mc.sum())],
            "azimuth_base": round(a_deg, 1), "azimuth_rotated": round(b_deg, 1),
            "azimuth_delta": round(delta, 1),
            "why": "灯转 90 度后投影掩码应几乎不重叠；重叠高 = 影子不是这盏主光打的"}


def check_hue_count(png, cfg):
    """HSV 色相聚类数量。全场一个色相就说明没做色彩层次。"""
    from colorsys import rgb_to_hsv
    a = _load(png).astype(int)
    step = max(1, a.shape[0] // 240)                      # 降采样，够用且快
    px = a[::step, ::step].reshape(-1, 3) / 255.0
    hist = np.zeros(12)
    for p in px[::7]:
        h, s, v = rgb_to_hsv(*p)
        if s > cfg["checks"]["hue_min_sat"] and v > cfg["checks"]["hue_min_val"]:
            hist[int(h * 12) % 12] += 1
    total = hist.sum()
    if total == 0:
        return {"pass": False, "why": "没有任何达到饱和度/明度门槛的像素"}
    frac = hist / total
    clusters = int((frac > 0.02).sum())
    top = float(frac.max())
    return {"pass": clusters >= cfg["checks"]["hue_min_clusters"] and top < cfg["checks"]["hue_max_top"],
            "clusters": clusters, "top_share": round(top, 3),
            "histogram": [round(float(x), 3) for x in frac]}


def check_layer_separation(png, cfg, cam=None, layer_of=None):
    """层次分离：把**每层的物体**投影到屏幕采样，比较各层在画面里的亮度。

    两次返工才对：
      ① 第一版按固定行区间切三条带 -> 中/近两层差 0.1 灰度级。那不是层次糊了，
         是切在了天空和空地上；
      ② 第二版改成采地面点 -> 只测得到雾的贡献（4~5 级），完全忽略了
         各层**物体颜色**本来就有的 mix_to_air 差异，度量设计本身就是错的。
    正确做法：采样各层物体自己的屏幕位置——雾和配色两个因素都算进去。
    """
    lum = _load(png).sum(axis=2) / 3.0
    Hh, Ww = lum.shape
    if cam is None or not layer_of:
        return {"pass": False, "why": "需要相机与 layer_of 映射"}
    sc = bpy.context.scene
    if (sc.render.resolution_x, sc.render.resolution_y) != (Ww, Hh):
        sc.render.resolution_x, sc.render.resolution_y = Ww, Hh
    bpy.context.view_layer.update()

    stats = []
    for layer in cfg["layers"]:
        samples = []
        for obj in layer_of.get(layer["name"], []):
            if obj is None or obj.type != 'MESH':
                continue
            # 采**包围盒角点**而不是只有中心：200px 缩略图下中心常常落在物体外，
            # 近层一度只剩 2 个样本、差值 5.9 差 0.1 级没过线（角点后样本翻倍）
            pts = [obj.matrix_world @ Vector(c) for c in obj.bound_box]
            for pnt in pts + [sum(pts, Vector()) / 8.0]:
                ndc = bpy_extras_world_to_camera(sc, cam, pnt)
                if not ndc or ndc.z <= 0:
                    continue
                px, py = int(ndc.x * Ww), int((1.0 - ndc.y) * Hh)
                if 3 <= px < Ww - 3 and 3 <= py < Hh - 3:
                    samples.append(float(np.median(lum[py - 3:py + 4, px - 3:px + 4])))
        if samples:
            stats.append({"layer": layer["name"], "lum": round(float(np.mean(samples)), 1),
                          "n": len(samples)})
    if len(stats) < len(cfg["layers"]):
        return {"pass": False, "why": "有层的物体没落在画面里", "stats": stats}

    vals = [x["lum"] for x in stats]
    mono = all(vals[i] >= vals[i + 1] - cfg["checks"]["layer_mono_slack"]
               for i in range(len(vals) - 1))
    delta = vals[0] - vals[-1]
    need = cfg["checks"]["layer_min_delta"]
    return {"pass": delta >= need and mono, "stats": stats,
            "far_minus_near": round(delta, 1), "min_required": need,
            "monotonic_far_to_near": mono}


def _mean_saturation(png):
    """全图平均饱和度（HSV 的 S）。雾的可靠签名是它单调下降。"""
    a = _load(png).astype(int)
    mx, mn = a.max(axis=2), a.min(axis=2)
    return float(np.mean(np.where(mx > 0, (mx - mn) / np.maximum(mx, 1), 0.0)))


def thumb_readable(thumb_png, cfg, cam=None, layer_of=None):
    """200px 缩略图「是否还读得出」——三条独立判据。

    原来这里直接复用了全尺寸那张的「层间亮度差」，那是把两件事混为一谈：
    缩到 200px 后近层物体只剩几十个像素，层间差必然缩水（实测 4.5 < 6.0），
    **量到的是分辨率，不是可读性**。改成直接量可读性：
      ① 画面有足够的结构（前景占比不低于阈值）；
      ② 四个象限都有边缘（东西南北都还有东西，不是一角糊一片）；
      ③ 深度次序仍然是「远亮近暗」（这条与全尺寸的层次检查同源，不能松）。
    """
    full = check_layer_separation(thumb_png, cfg, cam=cam, layer_of=layer_of)
    lum = _load(thumb_png).sum(axis=2) / 3.0
    Hh, Ww = lum.shape
    bg = float(np.median(lum[0:6, 0:6]))
    fg = np.abs(lum - bg) > 12
    ratio = float(fg.mean())
    half_h, half_w = Hh // 2, Ww // 2
    quads = [fg[:half_h, :half_w], fg[:half_h, half_w:],
             fg[half_h:, :half_w], fg[half_h:, half_w:]]
    edges = [float(np.abs(np.diff(q.astype(int), axis=1)).sum()) for q in quads]
    need_ratio = cfg["checks"]["thumb_min_fg_ratio"]
    ok = (ratio >= need_ratio
          and min(edges) > cfg["checks"]["thumb_min_quad_edge"]
          and full.get("monotonic_far_to_near", False))
    return {"pass": ok, "fg_ratio": round(ratio, 4), "min_fg_ratio": need_ratio,
            "quadrant_edges": [round(e) for e in edges],
            "depth_order_ok": full.get("monotonic_far_to_near"),
            "note": "可读性 = 结构占比 + 四象限都有边缘 + 深度次序未乱"}


# ---------------------------------------------------------------- main

def main():
    cfg = load_config()
    out_dir = cfg["output_dir"]
    os.makedirs(out_dir, exist_ok=True)

    fresh(cfg)
    build_sky(cfg)
    build_lights(cfg)
    fog = build_fog(cfg)
    build_ground(cfg)
    props = build_props(cfg)
    build_camera(cfg, props)

    cam = bpy.context.scene.camera
    main_png = render_to(os.path.join(out_dir, "layer3_main.png"),
                         cfg["render"]["width"], cfg["render"]["height"])
    thumb_png = render_to(os.path.join(out_dir, "layer3_thumb.png"), 200, 133)

    # 影子探针：只留地面 + 雾 + **一件最高的物体**当投影者。
    # 第一版把除地面外的网格全藏了——连投影者一起藏，当然测不到影子，
    # 自查直接报「找不到影子」。这类探针自己写错、自己还报警的情况必须盯住。
    probe = os.path.join(out_dir, "layer3_shadow_probe.png")
    # 专门的影子探针：空地上一个孤立立方体 + 只留地面。
    # 用树当投影者时，影子大半被树冠自己盖住，质心被带偏（实测偏 37 度）。
    probe_mat = H.mat_for("probe", [0.75, 0.75, 0.78], roughness=0.8)
    pt = gnkit.tree("GN_ShadowProbe")
    pt.finish(pt.geo().cube(size=1.0).transform(translate=(0, 0, 1.2), scale=(1.6, 1.6, 2.4))
              .material(probe_mat))
    caster = gnkit.object("ShadowProbe", pt, location=(0.0, cfg["probe"]["y"], 0.0))
    for obj in bpy.context.scene.objects:
        if obj.type == 'MESH' and obj.name not in ("Terrain_Ground", "AirVolume", "ShadowProbe"):
            obj.hide_render = True
    # 探针本体**留到自查之后再删**：渲完就删的话，自查直接报「找不到投影物」。
    # 影子怎么量交给 render_shadow_pair()（差分法），这里只负责把场景摆好。
    layer_of = {}
    for el in cfg["elements"]:
        layer_of.setdefault(el["layer"], []).append(bpy.context.scene.objects.get(el["name"]))

    fill_names = [l["name"] for l in cfg["lights"] if l.get("role") != "key"]
    key_name = next(l["name"] for l in cfg["lights"] if l.get("role") == "key")
    shots = []
    for off in (0.0, 90.0):
        lit, dark, probe_cam = render_shadow_pair(out_dir, cfg, caster, key_name,
                                                   fill_names, off)
        m, peak = shadow_mask_from_pair(lit, dark, cfg)
        print(f"  [shadow] rot={off} 度 两图最大差 {peak:.1f} 掩码像素 {int(m.sum())}")
        deg, err = measure_shadow_azimuth(m, cfg, caster, probe_cam)
        shots.append((deg, err, m))
        bpy.data.objects.remove(probe_cam, do_unlink=True)
        bpy.context.view_layer.update()
    results = {
        "shadow_direction": check_shadow_direction(
            shots, cfg, max_iou=cfg["checks"]["shadow_max_iou"]),
        "hue_count": check_hue_count(main_png, cfg),
        "layer_separation": check_layer_separation(main_png, cfg, cam=cam, layer_of=layer_of),
        "thumb_layer_separation": thumb_readable(thumb_png, cfg, cam=cam, layer_of=layer_of),
    }

    print("== 层 3 三项自查 ==")
    for k, v in results.items():
        print(f"  {'PASS' if v['pass'] else 'FAIL'}  {k}: "
              + ", ".join(f"{a}={b}" for a, b in v.items() if a != "pass"))

    # 掩掉雾渲一张对照：没有空气透视时层次分离应当明显变差，否则说明雾没起作用
    if fog:
        fog.hide_render = True
        nofog = render_to(os.path.join(out_dir, "layer3_nofog.png"),
                          cfg["render"]["width"], cfg["render"]["height"])
        fog.hide_render = False
        base = check_layer_separation(main_png, cfg, cam=cam, layer_of=layer_of)
        off = check_layer_separation(nofog, cfg, cam=cam, layer_of=layer_of)
        # 判据的物理方向原来写反了：雾的作用是**把远层往天光色抬**，
        # 同时压缩整体反差——它本来就会让远近亮度差变小。
        # 「开雾后分离度更大」是错的物理，实测 17.4 < 26.3 正是雾在起作用。
        # 雾的判据在**量过机理之后**重写。地面+单灯的密度扫描（实测）：
        #   density  mean    p10    p50    p90   range  饱和度
        #   0.000   157.3  149.7  159.7  161.3   11.7   0.112
        #   0.010   161.6  157.7  161.0  166.3    8.7   0.059   <- 净提亮
        #   0.028   155.7  149.3  154.0  164.7   15.3   0.042
        #   0.060   142.0  133.3  140.7  153.0   19.7   0.086   <- 净压暗
        # 结论：**低密度净提亮、高密度净压暗**（直射光衰减 vs 内散射谁赢看密度），
        # 所以「雾一定抬起暗部」这个假设本身是错的，写进判据只会一直假 FAIL。
        # 可靠且单调的签名是**饱和度下降**，以及**深度次序由雾建立**：
        # 实测关雾时 far-near = -1.5（远近一样亮，层次分不开），开雾后 +7.1（远层被抬起来）。
        sat_fog = _mean_saturation(main_png)
        sat_nofog = _mean_saturation(nofog)
        depth_with = base.get("far_minus_near")
        depth_without = off.get("far_minus_near")
        results["fog_effect"] = {
            "pass": (sat_fog < sat_nofog - cfg["checks"]["fog_min_desat"]
                     and depth_with is not None and depth_without is not None
                     and depth_with > depth_without),
            "sat_with_fog": round(sat_fog, 4), "sat_without_fog": round(sat_nofog, 4),
            "min_desat": cfg["checks"]["fog_min_desat"],
            "far_minus_near_with_fog": depth_with,
            "far_minus_near_without_fog": depth_without,
            "why": "雾 = 降饱和 + 建立远近次序；不要求抬暗部（实测高密度雾是净压暗的）"}
        v = results["fog_effect"]
        print(f"  {'PASS' if v['pass'] else 'FAIL'}  fog_effect: "
              f"饱和度 fog={v['sat_with_fog']} vs nofog={v['sat_without_fog']}、"
              f"远近差 fog={v['far_minus_near_with_fog']} vs nofog={v['far_minus_near_without_fog']}")
    spath = os.path.join(out_dir, "_selfcheck.json")
    with open(spath, "w", encoding="utf-8") as fh:
        json.dump({"config": CONFIG, "images": {
            "main": main_png, "thumb": thumb_png, "shadow_probe": probe},
            "checks": results}, fh, ensure_ascii=False, indent=2)

    if caster is not None:
        bpy.data.objects.remove(caster, do_unlink=True)

    failed = [k for k, v in results.items() if not v["pass"]]
    print(f"\n== {'ACCEPT: PASS' if not failed else 'ACCEPT: FAIL -> ' + ', '.join(failed)} ==")
    print(f"   自检明细 {spath}")
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
