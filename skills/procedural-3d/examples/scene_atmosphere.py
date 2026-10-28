"""场景示例：大气与光照（3d-scene-quality 层 4）。

    /path/to/miniforge3/envs/bpyenv/bin/python3.13 scene_atmosphere.py

做一张黄金时刻的山谷远景，只解决四件事：

  1. Nishita 物理天空（地平线发白 → 远景提亮的第一层空气透视）
  2. 与天空同参数的暖色太阳灯（色温显式给 K 值，不手调 RGB）
  3. 高度衰减 + 团块噪声的体积雾（雾的层次全在这两项上）
  4. 四排等高树当远近标尺，用来肉眼看空气透视有没有真的发生

参数全在同目录 scene_atmosphere.json，含 notes 说明每个数字为什么是这个值。
脚本一次渲两张：开雾 / 关雾。**判据不是「渲出来了」，是关雾那张对比之下能不能看出远近差别。**
"""

import json
import math
import os
import sys

import bpy
import numpy as np
from mathutils import Vector

HERE = os.path.dirname(os.path.abspath(__file__))
CONFIG = os.path.join(HERE, "scene_atmosphere.json")

TAU = math.tau


# ---------------------------------------------------------------- config

def load_config(path=CONFIG):
    with open(path, "r", encoding="utf-8") as fh:
        cfg = json.load(fh)
    if "output_dir" in cfg:
        cfg["output_dir"] = os.path.normpath(
            os.path.join(HERE, "..", "..", "..", cfg["output_dir"]))
    return cfg


# ---------------------------------------------------------------- terrain

def _value_noise_2d(rng, res):
    """res*res 的随机格点，之后靠 numpy 双三次插值当 value noise 用。"""
    return rng.random((res, res))


def _sample_lattice(lat, x, y, freq, res):
    """把连续坐标映射到 [0,res) 的格点上做双三次插值。"""
    u = (x * freq) % res
    v = (y * freq) % res
    u0, v0 = int(math.floor(u)), int(math.floor(v))
    fu, fv = u - u0, v - v0

    def smooth(t):
        return t * t * (3.0 - 2.0 * t)

    fu, fv = smooth(fu), smooth(fv)
    # 用 wrap 取格点，纯 numpy，不进 Python 循环
    i = np.arange(4)
    xi = (u0 + i) % res
    yi = (v0 + i) % res
    g = lat[np.ix_(yi, xi)]                      # (4,4)
    wx = np.array([(1 - fu) ** 3, 3 * (1 - fu) ** 2 * fu, 3 * (1 - fu) * fu ** 2, fu ** 3])
    wy = np.array([(1 - fv) ** 3, 3 * (1 - fv) ** 2 * fv, 3 * (1 - fv) * fv ** 2, fv ** 3])
    return float(wx @ g @ wy)


def make_height_fn(cfg):
    """返回一个可标量调用的 fbm 高度函数。放树、摆相机都要用它，所以必须是纯 python 可查的。"""
    seed = int(cfg["seed"])
    amp = float(cfg["amplitude"])
    freq0 = float(cfg["base_frequency"])
    octaves = int(cfg["octaves"])
    lac = float(cfg["lacunarity"])
    gain = float(cfg["gain"])
    rng = np.random.default_rng(seed)
    lattices = [_value_noise_2d(rng, 8 * (2 ** o)) for o in range(octaves)]

    def height(x, y):
        total, freq, weight, norm = 0.0, freq0, 1.0, 0.0
        for o in range(octaves):
            total += weight * (_sample_lattice(lattices[o], x, y, freq, 8 * (2 ** o)) - 0.5)
            norm += weight
            weight *= gain
            freq *= lac
        return amp * (total / norm) * 2.0
    return height


def build_terrain(cfg, height):
    size = float(cfg["size"])
    res = int(cfg["resolution"])
    half = size / 2.0
    axis = np.linspace(-half, half, res + 1)
    xs, ys = np.meshgrid(axis, axis, indexing="xy")
    zs = np.array([[height(float(x), float(y)) for y in axis] for x in axis], dtype=np.float64)

    verts = np.stack([xs.ravel(), ys.ravel(), zs.ravel()], axis=1).tolist()
    idx = np.arange(res + 1)
    a = idx[:, None] * (res + 1) + idx[None, :]
    faces = np.stack([a[:-1, :-1], a[:-1, 1:], a[1:, 1:], a[1:, :-1]], axis=-1).reshape(-1, 4).tolist()

    mesh = bpy.data.meshes.new("Terrain")
    mesh.from_pydata(verts, [], faces)
    mesh.update()

    # 顶点色：低处冷绿、高处干黄。只为让远近色相差看得出来，不是植被着色。
    lo = np.array(cfg["color_low"], dtype=np.float64)
    hi = np.array(cfg["color_high"], dtype=np.float64)
    t = (zs.ravel() - zs.min()) / max(1e-6, (zs.max() - zs.min()))
    t = (t ** 0.7)[:, None]
    cols = (lo[None, :] + (hi - lo)[None, :] * t).astype(np.float32)

    col_attr = mesh.color_attributes.new(name="Col", type='FLOAT_COLOR', domain='POINT')
    col_attr.data.foreach_set("color", cols.ravel())

    obj = bpy.data.objects.new("Terrain", mesh)
    bpy.context.collection.objects.link(obj)
    for poly in mesh.polygons:
        poly.use_smooth = True

    mat = bpy.data.materials.new("Terrain")
    mat.use_nodes = True
    nt = mat.node_tree
    bsdf = next(n for n in nt.nodes if n.type == 'BSDF_PRINCIPLED')
    attr = nt.nodes.new("ShaderNodeAttribute")
    attr.attribute_name = "Col"
    nt.links.new(attr.outputs["Color"], bsdf.inputs["Base Color"])
    obj.data.materials.append(mat)
    return obj


# ---------------------------------------------------------------- props

def build_trees(cfg, height, y_ref):
    """四排等高树 = 空气透视的远近标尺。rows[].distance 是**距相机**的米数，不是世界坐标。"""
    rng = np.random.default_rng(int(cfg["seed"]))
    mat = bpy.data.materials.new("Tree")
    mat.use_nodes = True
    bsdf = next(n for n in mat.node_tree.nodes if n.type == 'BSDF_PRINCIPLED')
    bsdf.inputs["Base Color"].default_value = tuple(cfg["color"])
    bsdf.inputs["Roughness"].default_value = 0.95

    count = 0
    for row in cfg["rows"]:
        d, spread, h = float(row["distance"]), float(row["spread"]), float(row["height"])
        y = y_ref + d
        # 只把树放在山脊线上：相机高于谷底时，平地上的树会被前一道山脊整个挡掉
        # （第一版就是这么把 38 棵树全种没了——渲完画面里一棵树影都没有，还以为树没生成）。
        # 做法：沿该排扫一段 x，取地势前 35% 的高点候选，随机抽 count 个。
        probes = np.linspace(-spread, spread, 96)
        zs = np.array([height(float(px), y) for px in probes])
        crest_z = float(np.percentile(zs, 65))
        idx = np.where(zs >= crest_z)[0]
        if len(idx) == 0:
            idx = np.arange(len(probes))
        picks = rng.choice(idx, size=min(int(row["count"]), len(idx)), replace=False)
        for i in picks:
            x = float(probes[int(i)])
            yy = y + (rng.random() - 0.5) * spread * 0.06
            hh = h * (0.8 + 0.4 * rng.random())
            bpy.ops.mesh.primitive_cone_add(
                vertices=7, radius1=hh * 0.22, radius2=0.0, depth=hh,
                location=(x, yy, height(x, yy) + hh / 2.0 - 1.0))
            obj = bpy.context.active_object
            obj.name = f"Tree_{count:03d}"
            obj.rotation_euler[2] = rng.random() * TAU
            obj.data.materials.append(mat)
            count += 1
    return count


# ---------------------------------------------------------------- sky / light

def kelvin_to_rgb(k):
    """Planckian locus 近似（Tanner Helland 拟合），够用且不用依赖。"""
    t = k / 100.0
    if t <= 66:
        r = 255.0
        g = 99.4708025861 * math.log(t) - 161.1195681661
    else:
        r = 329.698727446 * ((t - 60) ** -0.1332047592)
        g = 288.1221695283 * ((t - 60) ** -0.0755148492)
    if t >= 66:
        b = 255.0
    elif t <= 19:
        b = 0.0
    else:
        b = 138.5177312231 * math.log(t - 10) - 305.0447927307
    return tuple(min(1.0, max(0.0, c / 255.0)) for c in (r, g, b))


def build_sky(cfg):
    s = cfg["sky"]
    world = bpy.data.worlds.new("SkyWorld")
    world.use_nodes = True
    bpy.context.scene.world = world
    nt = world.node_tree
    nt.nodes.clear()
    out = nt.nodes.new("ShaderNodeOutputWorld")
    bg = nt.nodes.new("ShaderNodeBackground")
    sky = nt.nodes.new("ShaderNodeTexSky")
    sky.sky_type = s["type"]
    sky.sun_disc = bool(s["sun_disc"])
    # 5.1 上这些是 Sky 节点自己的属性，不是 socket；名字对不上就明确告警，不静默跳过
    for attr in ("sun_elevation", "sun_rotation", "altitude", "air_density", "ozone_density"):
        if attr not in s:
            continue
        if hasattr(sky, attr):
            setattr(sky, attr, float(s[attr]))
        else:
            print(f"  WARN sky.{attr} 在本版本不存在，参数 {s[attr]} 未生效（不是设过了）")
    bg.inputs["Strength"].default_value = float(s["strength"])
    nt.links.new(sky.outputs[0], bg.inputs["Color"])
    nt.links.new(bg.outputs[0], out.inputs["Surface"])
    return world


def build_sun(cfg):
    s = cfg["sun"]
    elevation = math.radians(float(s["elevation"]))
    rotation = math.radians(float(s["azimuth"]))

    def sun_lamp(name, elev, rot, energy, color, angle):
        d = Vector((math.cos(elev) * math.cos(rot), math.cos(elev) * math.sin(rot), math.sin(elev)))
        data = bpy.data.lights.new(name, type='SUN')
        data.energy = energy
        data.angle = math.radians(angle)
        data.color = color
        obj = bpy.data.objects.new(name, data)
        obj.location = d * 300.0
        obj.rotation_euler = (-d).to_track_quat('-Z', 'Y').to_euler()
        bpy.context.collection.objects.link(obj)
        return obj

    rgb = kelvin_to_rgb(float(s["color_kelvin"]))
    sun = sun_lamp("Sun", elevation, rotation, float(s["energy"]), rgb, float(s["angle"]))

    f = cfg["fill"]
    # 补光用第二盏 SUN 而不是区域光：低太阳角时朝向相机的坡面几乎吃不到直射光，
    # 会黑成一条带；区域光在这个尺度（几百米）要几万瓦才等效，不如太阳灯直观。
    fill = sun_lamp("Fill",
                    math.radians(float(f["elevation"])), math.radians(float(f["azimuth"])),
                    float(f["energy"]), tuple(f["color"])[:3], float(f["angle"]))
    return sun, fill, rgb


# ---------------------------------------------------------------- volume

def build_fog(cfg):
    f = cfg["fog"]
    if not f["enabled"]:
        return None
    sx, sy, sz = [float(v) for v in f["box"]]
    cx, cy, cz = [float(v) for v in f["box_center"]]

    bpy.ops.mesh.primitive_cube_add(size=1.0, location=(cx, cy, cz))
    box = bpy.context.active_object
    box.name = "FogBox"
    box.scale = (sx, sy, sz)
    box.display_type = 'WIRE'

    mat = bpy.data.materials.new("Fog")
    mat.use_nodes = True
    nt = mat.node_tree
    nt.nodes.clear()
    out = nt.nodes.new("ShaderNodeOutputMaterial")
    vol = nt.nodes.new("ShaderNodeVolumePrincipled")
    vol.inputs["Color"].default_value = tuple(f["color"])
    vol.inputs["Anisotropy"].default_value = float(f["anisotropy"])

    texco = nt.nodes.new("ShaderNodeTexCoord")
    sep = nt.nodes.new("ShaderNodeSeparateXYZ")
    nt.links.new(texco.outputs["Generated"], sep.inputs[0])

    # 高度衰减：h = (1 - genZ) * 盒高（米），密度 ∝ exp(-h / falloff)
    inv = nt.nodes.new("ShaderNodeMath"); inv.operation = 'SUBTRACT'
    inv.inputs[0].default_value = 1.0
    nt.links.new(sep.outputs["Z"], inv.inputs[1])
    hgt = nt.nodes.new("ShaderNodeMath"); hgt.operation = 'MULTIPLY'
    hgt.inputs[1].default_value = sz
    nt.links.new(inv.outputs[0], hgt.inputs[0])
    k = nt.nodes.new("ShaderNodeMath"); k.operation = 'MULTIPLY'
    k.inputs[1].default_value = -1.0 / float(f["height_falloff"])
    nt.links.new(hgt.outputs[0], k.inputs[0])
    falloff = nt.nodes.new("ShaderNodeMath"); falloff.operation = 'EXPONENT'
    nt.links.new(k.outputs[0], falloff.inputs[0])

    # 团块噪声：fac = (noise - 0.5) * 2 * amount + 1
    noise = nt.nodes.new("ShaderNodeTexNoise")
    noise.inputs["Scale"].default_value = float(f["noise_scale"])
    noise.inputs["Detail"].default_value = 4.0
    nt.links.new(texco.outputs["Generated"], noise.inputs["Vector"])
    amount = float(f["noise_amount"])
    nz = nt.nodes.new("ShaderNodeMath"); nz.operation = 'MULTIPLY_ADD'
    nz.inputs[1].default_value = 2.0 * amount
    nz.inputs[2].default_value = 1.0 - amount
    nt.links.new(noise.outputs["Fac"], nz.inputs[0])

    dens = nt.nodes.new("ShaderNodeMath"); dens.operation = 'MULTIPLY'
    nt.links.new(falloff.outputs[0], dens.inputs[0])
    nt.links.new(nz.outputs[0], dens.inputs[1])
    base = nt.nodes.new("ShaderNodeMath"); base.operation = 'MULTIPLY'
    base.inputs[1].default_value = float(f["density"])
    nt.links.new(dens.outputs[0], base.inputs[0])
    nt.links.new(base.outputs[0], vol.inputs["Density"])
    nt.links.new(vol.outputs[0], out.inputs["Volume"])

    box.data.materials.append(mat)
    return box


# ---------------------------------------------------------------- camera / render

def build_camera(cfg):
    c = cfg["camera"]
    data = bpy.data.cameras.new("Cam")
    data.lens = float(c["lens"])
    # clip_end 默认 1000m。雾盒有 1600m，超出部分不积分——实测：调到 1000 时雾只出现在地形上、
    # 天空完全没雾（整个上半幅对雾密度零响应）；调到 5000 后雾才铺满整幅。远景场景必改这一项。
    data.clip_end = float(c.get("clip_end", 5000.0))
    obj = bpy.data.objects.new("Cam", data)
    obj.location = tuple(float(v) for v in c["location"])
    direction = Vector(tuple(float(v) for v in c["look_at"])) - obj.location
    obj.rotation_euler = direction.to_track_quat('-Z', 'Y').to_euler()
    bpy.context.collection.objects.link(obj)
    bpy.context.scene.camera = obj
    return obj


def setup_render(cfg):
    r = cfg["render"]
    scene = bpy.context.scene
    scene.render.engine = r.get("engine", "CYCLES")
    scene.render.resolution_x = int(r["width"])
    scene.render.resolution_y = int(r["height"])
    scene.render.resolution_percentage = 100
    scene.render.film_transparent = bool(r["film_transparent"])
    if scene.render.engine == 'CYCLES':
        scene.cycles.device = r.get("device", "CPU")
        scene.cycles.samples = int(r["samples"])
        scene.cycles.use_denoising = bool(r["denoise"])
        scene.cycles.volume_step_rate = float(r.get("volume_step_rate", 1.0))
    if "exposure" in r:
        scene.view_settings.exposure = float(r["exposure"])
    for attr, key in (("view_transform", "view_transform"), ("look", "look")):
        try:
            setattr(scene.view_settings, attr, r[key])
        except Exception as exc:
            print(f"  WARN view_settings.{attr}={r[key]} 不被接受: {exc}")


def render_to(scene, path):
    scene.render.filepath = path
    scene.render.image_settings.file_format = 'PNG'
    bpy.ops.render.render(write_still=True)
    return path


# ---------------------------------------------------------------- main

def build(cfg):
    bpy.ops.wm.read_factory_settings(use_empty=True)
    setup_render(cfg)
    build_sky(cfg)
    sun, _fill, sun_rgb = build_sun(cfg)
    height = make_height_fn(cfg["terrain"])
    terrain = build_terrain(cfg["terrain"], height)
    trees = build_trees(cfg["trees"], height, float(cfg["camera"]["location"][1]))
    fog = build_fog(cfg)
    build_camera(cfg)

    zs = [v.co.z for v in terrain.data.vertices]
    print("[ATM] sun_rgb(K=%.0f) = (%.3f, %.3f, %.3f)"
          % (cfg["sun"]["color_kelvin"], *sun_rgb))
    print("[ATM] terrain verts=%d faces=%d z=[%.1f, %.1f]"
          % (len(terrain.data.vertices), len(terrain.data.polygons), min(zs), max(zs)))
    print("[ATM] trees=%d fog=%s engine=%s samples=%d"
          % (trees, "on" if fog else "off", bpy.context.scene.render.engine,
             bpy.context.scene.cycles.samples))
    return terrain, fog


def main():
    cfg = load_config()
    out = cfg["output_dir"]
    os.makedirs(out, exist_ok=True)

    _terrain, fog = build(cfg)
    blend = os.path.join(out, "scene_atmosphere.blend")
    bpy.ops.wm.save_as_mainfile(filepath=blend)

    on = render_to(bpy.context.scene, os.path.join(out, "fog_on.png"))
    print("[ATM] rendered %s (%d bytes)" % (on, os.path.getsize(on)))

    fog.hide_render = True
    off = render_to(bpy.context.scene, os.path.join(out, "fog_off.png"))
    print("[ATM] rendered %s (%d bytes)" % (off, os.path.getsize(off)))
    fog.hide_render = False

    print("[ATM] BLEND %s" % blend)
    print("[ATM] 判据：对比 fog_on / fog_off，四排树必须从「四样东西」变成「四个深浅不同的剪影」")


if __name__ == "__main__":
    main()
