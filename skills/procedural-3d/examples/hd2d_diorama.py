"""场景示例：HD-2D 分层立体场景拼装（3d-scene-quality 层 -1）。

    /path/to/miniforge3/envs/bpyenv/bin/python3.13 hd2d_diorama.py

**层 -1 的产出物**：把教程《HD-2D游戏场景》/《HD-3D场景生成》里可复用的做法落成
可执行代码，不是只读笔记。四条落地做法与它们的出处：

1. **元素清单驱动** —— 教程要「把参考图里的建筑/植物/元素逐个提取、每个独立一张图，
   然后分别导入场景摆放」。这里就是那份清单的可执行形式：`hd2d_diorama.json` 的
   `elements`，一条 = 一个元素 = 一个 GN 树 = 一个对象。加元素只改 JSON。
2. **锚点默认在下边缘中间** —— 教程原话「默认情况下，中心点位于下边缘中间」。
   三维等价物是元素局部原点在底面中心：所有几何从 z=0 往上长。
   **脚本对此做硬断言**（每个元素世界包围盒 min_z 必须为 0），不允许悬空 ——
   教程的「被遮挡部分要补全、不应该有任何悬空的地方」就是这条。
3. **远景分层** —— 教程「最远处场景可以分层提取为独立的几层建筑群」。
   `depth_layers` 三层，每层一个 Y 位置 + 一个往天光方向混色的比例，
   空气透视由此产生（不是靠后期滤镜）。
4. **碰撞随缩放** —— 教程「自动识别贴图尺寸并智能匹配碰撞区域，且碰撞区域能随场景一同缩放」。
   碰撞盒直接从**求值后的几何**算包围盒，所以元素一改尺寸盒子必跟着变，
   不存在「手填的碰撞数值与模型脱钩」这种静态资产的老问题。

**验收不是「渲出来了」**，脚本自己断言三件事并在末尾打印 ACCEPT/FAIL：
元素全部贴地、每个元素求值出真实顶点、缩略图已渲染。
判图标准：**并排看主图与关雾对照图，远景是否真的被空气吃掉、三层是否分得开。**

渲三张：主图 / 200px 缩略图 / 关雾对照图。
"""

import json
import math
import os
import sys

# 箱庭的引擎侧导出目录。默认开：这条用例现在不只产 PNG，还要产 glb，
# 否则「箱庭能进引擎」这件事永远没人验。
EXPORT_DIR = os.environ.get(
    "HD2D_EXPORT_DIR",
    "/path/to/Fairy/workspace/result/hd2d_export",
)
import sys

import bpy
from mathutils import Vector

HERE = os.path.dirname(os.path.abspath(__file__))
CONFIG = os.path.join(HERE, "hd2d_diorama.json")
SKILL_SCRIPTS = os.path.normpath(os.path.join(HERE, "..", "scripts"))
sys.path.insert(0, SKILL_SCRIPTS)

import gnkit  # noqa: E402  （必须在 sys.path 调整之后）


# ---------------------------------------------------------------- config

def load_config(path=CONFIG):
    with open(path, "r", encoding="utf-8") as fh:
        cfg = json.load(fh)
    out = cfg.get("output_dir", "workspace/result/hd2d_diorama")
    cfg["output_dir"] = os.path.normpath(os.path.join(HERE, "..", "..", "..", out))
    return cfg


def mix_to_air(rgb, air_rgb, amount):
    """把颜色往天光色混。空气透视的第一层就是降彩度，不是降亮度。"""
    return [c + (a - c) * amount for c, a in zip(rgb, air_rgb)]


# ---------------------------------------------------------------- scene scaffolding

def fresh_scene():
    bpy.ops.wm.read_factory_settings(use_empty=True)
    # use_empty=True 之后 scene.world 是 None，得自己建一个（bpy 5.1 实测）
    world = bpy.data.worlds.new("Sky")
    bpy.context.scene.world = world
    return world


def make_collection(name):
    coll = bpy.data.collections.new(name)
    bpy.context.scene.collection.children.link(coll)
    return coll


def move_to_collection(obj, coll):
    for c in list(obj.users_collection):
        c.objects.unlink(obj)
    coll.objects.link(obj)


# ---------------------------------------------------------------- materials

def mat_for(name, rgb, roughness=0.75):
    return gnkit.material(name, color=(rgb[0], rgb[1], rgb[2], 1.0), roughness=roughness)


def element_materials(el, layer, palette, air_rgb, roughness=0.78):
    """按层混色后建材质。同层同色的元素共用一个材质，省得堆几十个。"""
    mats = {}

    def build(key, rough=roughness):
        base = palette[key]
        mix = layer["mix_to_air"]
        mats[key] = mat_for(
            f"{layer['name']}_{key}", mix_to_air(base, air_rgb, mix), roughness=rough)
    # 建筑的两个色槽由元素自己指定（wall / roof_color 指向 palette 里的键名），
    # 所以按「用到的键」建材质，而不是按固定的一套键名。
    for key in (el.get("wall"), el.get("roof_color"), el.get("color")):
        if key:
            build(key)
    if el["kind"] in ("tree", "grass"):
        build("trunk", 0.9)
        build("foliage", 0.85)
    if el["kind"] in ("post", "lantern"):
        build("post", 0.7)
    if el["kind"] == "lantern":
        build("lantern", 0.5)
    return mats


# ---------------------------------------------------------------- elements
# 所有几何都从 z=0 往上长：锚点在底面中心（教程「中心点位于下边缘中间」）。

def build_building(el, mats):
    """返回分件工厂列表，每个工厂在**自己的树**里造一件带自己材质的几何。

    为什么不在 GN 里 join：实测 JoinGeometry 会把分件材质压平成同一个槽
    （树干的棕色被树冠的绿色覆盖，整棵树一个颜色），而且**不报错**。
    分开造、各自求值、在 Python 里拼，材质才靠得住。
    """
    w, d, h, roof = el["width"], el["depth"], el["height"], el["roof"]

    def body(g):
        return g.cube(size=1.0).transform(
            translate=(0.0, 0.0, h / 2.0), scale=(w, d, h)).material(mats[el["wall"]])

    def top(g):
        # 四棱锥屋顶：vertices=4 时底面是正方形（边长 = R*sqrt(2)，且已轴对齐），
        # 旋转 45 度反而会把屋檐对到墙角上——这跟直觉相反，故不转。
        r = (w * 1.12) / math.sqrt(2.0)
        return g.cone(radius1=r, radius2=0.0, depth=roof, vertices=4).transform(
            translate=(0.0, 0.0, h + roof / 2.0),
            scale=(1.0, d / w * 1.12, 1.0)).material(mats[el["roof_color"]])

    return [body, top]


def build_tree(el, mats):
    h, trunk_r, canopy = el["height"], el["trunk_radius"], el["canopy"]

    def trunk(g):
        return g.cylinder(radius=trunk_r, depth=h * 0.5, vertices=10).transform(
            translate=(0.0, 0.0, h * 0.25)).material(mats["trunk"])

    def crown(g):
        # 树冠单层锥。原来是「下层锥 + 上层锥」两层叠的，两锥互相穿插，
        # 渲出来树冠中段有一道硬色带断层 —— 教程《HD-3D 模型生成提示词》明令禁止的穿模。
        # radius2 给 0.30（不是 0）：太尖会变成圣诞树，渲图实测不像阔叶树。
        # 冠底压到 0.34h：原来 0.42h 只比树干顶(0.5h)低 0.08h，
        # 渲出来像「树冠浮在树干上方」；压低后冠干明显连成一体（层 3b 特写对照验证过）。
        return g.cone(radius1=canopy * 0.5, radius2=canopy * 0.30,
                      depth=h * 0.55, vertices=12).transform(
            translate=(0.0, 0.0, h * 0.34 + h * 0.55 / 2.0 - 0.05)).material(mats["foliage"])

    return [trunk, crown]


def build_rock(el, mats):
    """低模块石：一级细分的 icosphere（20 面）压扁，天然就是「有棱面的石头」。

    为什么不拿 block 冒充石头：方块在低模场景里一眼就是方块，剪影骗不了人。
    20 面是刻意的预算选择——教程给的场景元素预算是 <=6000 tris，这里远在下面。
    """
    w, d, h = el["width"], el["depth"], el["height"]

    def rock(g):
        # IcoSphere 半径 0.5、subdivisions=1 -> 直径 1 的 20 面体；缩放到目标包围盒
        g = g.ico_sphere(radius=0.5, subdivisions=1).transform(scale=(w, d, h))
        # 压到地面并略微埋一点，避免「浮在地面上的一颗球」
        return g.transform(translate=(0.0, 0.0, h * 0.42)).material(mats[el["color"]])

    return [rock]


def build_block(el, mats):
    """通体长方体：绿篱、矮墙、台阶这类「横着铺开压住画面边」的元素。

    为什么需要它：横向跨度大时自动取景会把相机推远，脚下就剩一大片空地。
    渲图实测过——光靠几根等距立柱压不住，必须有一条连续的低矮体块兜住画面下缘。
    """
    h = el["height"]

    def box(g):
        return g.cube(size=1.0).transform(
            translate=(0.0, 0.0, h / 2.0),
            scale=(el["width"], el.get("depth", 2.0), h)).material(mats[el["color"]])

    return [box]


def build_grass(el, mats):
    """草丛：四片交叉的细叶（薄盒），每片转不同角度。

    之前用矮方块占位，渲图实测「就是一个浅绿色长方体，完全没有草的轮廓」——
    占位件混进交付物比缺件更危险，所以这里补成真构件。
    """
    blades = int(el.get("blades", 4))

    def blade(i):
        def build(g):
            ang = 360.0 / blades * i
            return g.cube(size=1.0).transform(
                translate=(0.0, 0.0, el["height"] / 2.0),
                rotate=(0.0, el.get("tilt", 18.0), ang),
                scale=(el["width"] / blades * 0.9, 0.03, el["height"]),
            ).material(mats["foliage"])
        return build

    return [blade(i) for i in range(blades)]


def build_post(el, mats):
    h = el["height"]

    def post(g):
        return g.cylinder(radius=el["post_radius"], depth=h, vertices=8).transform(
            translate=(0.0, 0.0, h / 2.0)).material(mats["post"])

    return [post]


def build_lantern(el, mats):
    """灯笼：柱 + 灯箱。灯箱用自发光材质，HD-2D 里是唯一的人工光源点缀。"""
    h = el["height"]

    def post(g):
        return g.cylinder(radius=el["post_radius"], depth=h, vertices=8).transform(
            translate=(0.0, 0.0, h / 2.0)).material(mats["post"])

    def head(g):
        # 灯罩做成「六棱柱灯身 + 小尖顶」，别用纯方块——渲图实测方块看着像标牌不像灯。
        g = g.cylinder(radius=0.34, depth=0.72, vertices=6).transform(
            translate=(0.0, 0.0, h + 0.5)).material(mats["lantern"])
        cap = g.cone(radius1=0.40, radius2=0.0, depth=0.34, vertices=6).transform(
            translate=(0.0, 0.0, h + 1.02)).material(mats["post"])
        return g.join(cap)

    return [post, head]


def build_parts(element, mats):
    """把构件的分件工厂跑成各自独立的树并求值，返回 [(verts, faces, face_mats), ...]。

    合并放在 Python 侧：GN 的 JoinGeometry 会压平材质槽（见 build_building 注释）。
    **必须在 to_mesh_clear() 之前把数据取出来**——clear 之后那个 Mesh 的 StructRNA
    就失效了，拿它读顶点会报 `StructRNA of type Mesh has been removed`。
    """
    out = []
    for i, factory in enumerate(BUILDERS[element["kind"]](element, mats)):
        t = gnkit.tree(f"GN_{element['name']}_p{i}")
        t.finish(factory(t.geo()))
        obj = gnkit.object(f"__part_{element['name']}_{i}", t)
        t.realize(obj)
        dg = bpy.context.evaluated_depsgraph_get()
        ev = obj.evaluated_get(dg)
        me = ev.to_mesh()
        if me is None or len(me.vertices) == 0:
            ev.to_mesh_clear()
            raise ValueError(f"{element['name']} 第 {i} 件求值出 0 顶点")
        verts = [tuple(v.co) for v in me.vertices]
        faces, face_mats = [], []
        for poly in me.polygons:
            faces.append(tuple(poly.vertices))
            slot = poly.material_index
            mat = me.materials[slot] if slot < len(me.materials) else None
            face_mats.append(mat.name if mat else None)
        ev.to_mesh_clear()
        out.append((verts, faces, face_mats))
        # 分件对象只是求值用的脚手架，留在场景里会越积越多
        # （实测跑 6 个元素后场景里堆到 16 个对象）
        bpy.data.objects.remove(obj, do_unlink=True)
    return out


def combine_parts(name, parts):
    """把分件拼成一个网格对象，**逐面带自己的材质**。

    这是修「树干被染成树冠绿」的那一步：光把材质挂到 slots 上没用，
    还得把每个面的 material_index 指回它来自哪一件。
    """
    me = bpy.data.meshes.new(f"{name}_mesh")
    verts, faces, mat_idx, materials = [], [], [], []
    slot_of = {}
    for pv, pf, pm in parts:
        base = len(verts)
        verts.extend(pv)
        for f, matname in zip(pf, pm):
            faces.append(tuple(base + i for i in f))
            if matname and matname not in slot_of:
                slot_of[matname] = len(materials)
                materials.append(bpy.data.materials[matname])
            mat_idx.append(slot_of.get(matname, 0))
    me.from_pydata(verts, [], faces)
    me.update()
    for mat in materials:
        me.materials.append(mat)
    for poly, idx in zip(me.polygons, mat_idx):
        poly.material_index = idx
    return me


BUILDERS = {
    "building": build_building,
    "block": build_block,
    "rock": build_rock,
    "grass": build_grass,
    "tree": build_tree,
    "post": build_post,
    "lantern": build_lantern,
}


# ---------------------------------------------------------------- ground / fog

def build_ground(cfg):
    gcfg = cfg["ground"]
    t = gnkit.tree("GN_Ground")
    g = t.geo()
    mat = mat_for("ground", gcfg["color"], roughness=gcfg["roughness"])
    # 纯平面，无位移无起伏——教程「地面单独提取成一张平面贴图，不要透视」
    grid = g.grid(size_x=gcfg["size"][0], size_y=gcfg["size"][1],
                  verts_x=8, verts_y=8).material(mat)
    t.finish(grid)
    obj = gnkit.object("Terrain_Ground", t)
    move_to_collection(obj, make_collection("Terrain"))
    return t


def build_fog(cfg):
    """全场景一层雾：体积散射 + 高度衰减。密度公式 exp(-k*z) 直接用节点算。"""
    acfg = cfg["air"]

    # 踩过的坑：这里原本用 `bpy.data.objects.new(name, bpy.data.meshes.new(...))`
    # 造罩子 —— 那个 mesh 是**空的**，物体有名字有材质但没有任何面，
    # 体积散射因此无处发生，「雾渲不出来」。造实体要用 primitive_cube_add。
    bpy.ops.mesh.primitive_cube_add(size=1.0, location=(0.0, 30.0, 20.0))
    cube = bpy.context.active_object
    cube.name = "AirVolume"
    cube.scale = (400.0, 400.0, 120.0)   # 罩住全场；scale 全是 1 的自检项管不到这种角色
    cube.display_type = 'WIRE'
    move_to_collection(cube, make_collection("Atmosphere"))

    mat = bpy.data.materials.new("AirVolume")
    mat.use_nodes = True
    nt = mat.node_tree
    for n in list(nt.nodes):
        nt.nodes.remove(n)
    out = nt.nodes.new("ShaderNodeOutputMaterial")
    out.location = (600, 0)
    scatter = nt.nodes.new("ShaderNodeVolumeScatter")
    scatter.location = (400, 0)
    scatter.inputs["Color"].default_value = (acfg["fog_color"][0],
                                            acfg["fog_color"][1],
                                            acfg["fog_color"][2], 1.0)

    geo = nt.nodes.new("ShaderNodeNewGeometry")
    geo.location = (-600, 0)
    sep = nt.nodes.new("ShaderNodeSeparateXYZ")
    sep.location = (-420, 0)
    nt.links.new(geo.outputs["Position"], sep.inputs["Vector"])

    mul = nt.nodes.new("ShaderNodeMath")
    mul.operation = 'MULTIPLY'
    mul.location = (-240, 0)
    mul.inputs[1].default_value = -acfg["fog_height_falloff"]
    nt.links.new(sep.outputs["Z"], mul.inputs[0])

    expo = nt.nodes.new("ShaderNodeMath")
    expo.operation = 'EXPONENT'          # Blender 的 EXPONENT 是 e^x
    expo.location = (-60, 0)
    nt.links.new(mul.outputs[0], expo.inputs[0])

    dens = nt.nodes.new("ShaderNodeMath")
    dens.operation = 'MULTIPLY'
    dens.location = (120, 0)
    dens.inputs[1].default_value = acfg["fog_density"]
    nt.links.new(expo.outputs[0], dens.inputs[0])
    nt.links.new(dens.outputs[0], scatter.inputs["Density"])
    nt.links.new(scatter.outputs["Volume"], out.inputs["Volume"])

    cube.data.materials.append(mat)
    return cube


def toggle_fog(obj, on):
    obj.hide_render = not on


# ---------------------------------------------------------------- lighting / camera

def build_lighting(cfg):
    lcfg = cfg["light"]
    sun = gnkit.light("Sun_Key", kind='SUN', energy=lcfg["sun_energy"],
                      angle=lcfg["sun_angle_deg"], location=(0, 0, 30))
    sun.rotation_euler = (math.radians(52.0), 0.0, math.radians(lcfg["sun_rotation_z"]))
    fill = gnkit.light("Sun_Fill", kind='SUN', energy=lcfg["fill_energy"],
                       location=(0, 0, 30))
    fill.data.color = lcfg["fill_color"]
    fill.rotation_euler = (math.radians(20.0), 0.0, math.radians(210.0))
    for obj in (sun, fill):
        move_to_collection(obj, make_collection("Lights"))
    return sun, fill


def build_camera(cfg):
    ccfg = cfg["camera"]
    cam = gnkit.camera("Camera_Main", location=ccfg["location"],
                       look_at=ccfg["look_at"], lens=ccfg["lens"])
    cam.data.shift_y = ccfg.get("shift_y", 0.0)
    move_to_collection(cam, make_collection("Camera"))
    return cam


def aim_camera(cam, location, target):
    from mathutils import Vector as V
    cam.location = V(location)
    cam.rotation_euler = (V(target) - V(location)).to_track_quat('-Z', 'Y').to_euler()


def element_corners(report):
    """取参与取景的元素的世界包围盒 8 个角点当取景依据。

    `fit: false` 的元素（前景压边件）**不进取景集合**：构图里前景本来就该被画面边缘裁掉，
    把它们算进去会让自动取景为了容下裁切而把相机推得越来越远——实测这是一个发散的循环，
    相机从 y=-51 一路被推到 y=-100，主体反而越缩越小。
    """
    from mathutils import Vector as V
    dg = bpy.context.evaluated_depsgraph_get()
    corners = []
    for obj_name in report:
        obj = bpy.data.objects[obj_name]
        ev = obj.evaluated_get(dg)
        mesh = ev.to_mesh()
        if mesh is None or len(mesh.vertices) == 0:
            ev.to_mesh_clear()
            continue
        mw = obj.matrix_world
        pts = [mw @ v.co for v in mesh.vertices]
        ev.to_mesh_clear()
        xs = [p.x for p in pts]
        ys = [p.y for p in pts]
        zs = [p.z for p in pts]
        for x in (min(xs), max(xs)):
            for y in (min(ys), max(ys)):
                for z in (min(zs), max(zs)):
                    corners.append(V((x, y, z)))
    return corners


def frame_camera(cfg, corners, iters=40, margin=0.05):
    """把相机自动推到「所有元素都在画面内、且居中填满」的位置。

    手写 `location` / `look_at` 治不了「远景被画面上缘裁掉」：元素一改高度就得重调。
    这里保持视角方向不变，只迭代两件事——沿视线推拉距离、把主体在画面里摆正。
    判据是 NDC 包围盒（world_to_camera_view），不是肉眼看。
    """
    from bpy_extras.object_utils import world_to_camera_view
    scene = bpy.context.scene
    cam = scene.camera
    ccfg = cfg["camera"]

    center = Vector((0.0, 0.0, 0.0))
    for p in corners:
        center += p
    center /= len(corners)

    offset = Vector(ccfg["location"]) - Vector(ccfg["look_at"])
    dirv = offset.normalized()          # 方向锁死：自动取景不该顺手改掉俯角
    dist = offset.length
    fill_target = 1.0 - 2.0 * margin

    for _ in range(iters):
        loc = center + dirv * dist
        aim_camera(cam, loc, center)
        bpy.context.view_layer.update()
        dg = bpy.context.evaluated_depsgraph_get()
        ndc = [world_to_camera_view(scene, cam, p) for p in corners]
        xs = [n.x for n in ndc]
        ys = [n.y for n in ndc]
        w = max(xs) - min(xs)
        h = max(ys) - min(ys)
        fill = max(w, h, 1e-6)
        # 主体偏心就在画面平面上把它推回去：NDC 偏移 × 取景框世界尺寸
        mw = cam.matrix_world
        right = mw.to_3x3() @ Vector((1.0, 0.0, 0.0))
        up = mw.to_3x3() @ Vector((0.0, 1.0, 0.0))
        aspect = scene.render.resolution_y / scene.render.resolution_x
        frame_w = cam.data.sensor_width / cam.data.lens * dist
        frame_h = frame_w * aspect
        dx = (min(xs) + max(xs)) / 2.0 - 0.5
        dy = (min(ys) + max(ys)) / 2.0 - 0.5
        shift = right * (dx * frame_w) + up * (dy * frame_h)
        center += shift
        dist *= fill / fill_target

    loc = center + dirv * dist
    aim_camera(cam, loc, center)
    bpy.context.view_layer.update()

    ndc = [world_to_camera_view(scene, cam, p) for p in corners]
    xs = [n.x for n in ndc]
    ys = [n.y for n in ndc]
    return {
        "iters": iters,
        "distance": round(dist, 3),
        "camera_location": [round(v, 3) for v in cam.location],
        "look_at": [round(v, 3) for v in center],
        "ndc_x": [round(min(xs), 3), round(max(xs), 3)],
        "ndc_y": [round(min(ys), 3), round(max(ys), 3)],
        "culled": any(n.z <= 0.0 for n in ndc),
    }


# ---------------------------------------------------------------- acceptance

def world_bounds(obj):
    dg = bpy.context.evaluated_depsgraph_get()
    ev = obj.evaluated_get(dg)
    mesh = ev.to_mesh()
    if mesh is None or len(mesh.vertices) == 0:
        ev.to_mesh_clear()
        return None
    mat = obj.matrix_world
    pts = [mat @ v.co for v in mesh.vertices]
    ev.to_mesh_clear()
    return pts


def check_anchor_and_collider(obj, pad):
    """贴地断言 + 碰撞盒尺寸。碰撞盒取求值后的世界包围盒，故随缩放自动跟随。"""
    pts = world_bounds(obj)
    if pts is None:
        return {"verts": 0, "grounded": False}
    zs = [p.z for p in pts]
    xs = [p.x for p in pts]
    ys = [p.y for p in pts]
    grounded = abs(min(zs)) < 1e-4
    return {
        "verts": len(pts),
        "grounded": grounded,
        "z_min": min(zs),
        "z_max": max(zs),
        "collider": (max(xs) - min(xs) + 2 * pad,
                     max(ys) - min(ys) + 2 * pad,
                     max(zs) - min(zs)),
    }


# ---------------------------------------------------------------- render

def place_framing_elements(target_ndc_y=None, content_bottom=None):
    """把 `fit: false` 的前景压边件自动摆到画面下缘。

    为什么必须自动摆：相机在 25 米高处俯视，一个放在镜头前 9 米的绿篱，
    投影落在 ndc_y = -1.6 —— 整个在画面**下方之外**，等于没放。手填 y 全是撞运气。
    这里对每个前景件沿 +y 二分，直到它顶点的投影落在 target_ndc_y 附近。
    判据是投影坐标，不是肉眼。

    **target 要贴着主体下缘，不能写死**：主体横向铺开时自动取景只受宽度约束，
    画面上下会空出一大截（实测主体只占 ndc_y 0.24~0.76）。压边件若还固定放在 0.14，
    主体与压边之间就多出一段裸地——实测底部 35% 是空地，压边件孤零零杵在那儿。
    """
    from bpy_extras.object_utils import world_to_camera_view
    scene = bpy.context.scene
    cam = scene.camera
    cam_y = cam.location.y

    if target_ndc_y is None:
        # 压在主体下缘再往下一点，让它当画面底边用
        bottom = 0.14 if content_bottom is None else content_bottom
        target_ndc_y = max(0.02, bottom - 0.03)

    placed = []
    for obj in bpy.data.objects:
        if not obj.name.startswith(("fg_",)):
            continue
        dg = bpy.context.evaluated_depsgraph_get()
        ev = obj.evaluated_get(dg)
        mesh = ev.to_mesh()
        if mesh is None or len(mesh.vertices) == 0:
            ev.to_mesh_clear()
            continue
        mw = obj.matrix_world
        top = max((mw @ v.co for v in mesh.vertices), key=lambda p: p.z)
        ev.to_mesh_clear()

        # 踩过的坑：改 obj.location.y 后只调 view_layer.update()，
        # world_to_camera_view 拿到的还是旧矩阵，投影值**一个像素都不动**，
        # 二分因此直接收敛到边界上（实测 y 全被推到 35.76，ndc 恒为 -1.2）。
        # 正解：试探点的世界坐标自己算，别指望物体矩阵会跟着刷新。
        lo, hi = cam_y + 4.0, cam_y + 90.0
        for _ in range(40):
            mid = (lo + hi) / 2.0
            n = world_to_camera_view(scene, cam, Vector((top.x, mid, top.z)))
            if n.y < target_ndc_y:
                lo = mid
            else:
                hi = mid
        obj.location.y = round((lo + hi) / 2.0, 3)
        n = world_to_camera_view(scene, cam, Vector((top.x, obj.location.y, top.z)))
        placed.append((obj.name, obj.location.y, round(n.y, 3)))
    return placed


def setup_render(cfg, width, height, fog_on=True):
    sc = bpy.context.scene
    r = cfg["render"]
    sc.render.engine = "CYCLES"
    sc.cycles.device = r.get("device", "CPU")
    sc.cycles.samples = r["samples"]
    if r.get("denoise", True):
        sc.cycles.use_denoising = True
    sc.render.resolution_x = width
    sc.render.resolution_y = height
    sc.render.resolution_percentage = 100
    sc.render.image_settings.file_format = 'PNG'
    # AgX 必须显式设：bpy 5.1 无头下 use_empty 清场后默认停在 Filmic
    sc.view_settings.view_transform = r.get("view_transform", "AgX")
    sc.view_settings.exposure = r.get("exposure", 0.0)
    return fog_on


def render_to(path, width, height):
    sc = bpy.context.scene
    sc.render.resolution_x = width
    sc.render.resolution_y = height
    sc.render.filepath = path
    bpy.ops.render.render(write_still=True)
    return path


# ---------------------------------------------------------------- main

def _cleanup_scene(scene):
    """把本脚本建的所有构件从场景里摘掉，只留灯光相机。

    层 -1 原本只渲图，从不导出，所以没有这一步。
    加导出之后必须清干净，否则下面 reset 到空场景时会漏删对象，
    内存里留着几百个 mesh 还导出，会直接吃满内存。
    """
    for obj in list(bpy.data.objects):
        if obj.type == 'MESH':
            bpy.data.objects.remove(obj, do_unlink=True)


def _export_glb(path):
    """按当前选择导出 glb。调用前必须先 make_single_user。"""
    bpy.ops.export_scene.gltf(
        filepath=path,
        export_format='GLB',
        use_selection=True,
        export_apply=True,
        export_yup=True,
        export_cameras=False,
        export_lights=False,
    )
    print("  exported %-28s %8.1f KB" % (
        os.path.basename(path), os.path.getsize(path) / 1024))


def _deselect_all():
    for obj in bpy.context.view_layer.objects:
        obj.select_set(False)


def _select_objs(objs):
    _deselect_all()
    for obj in objs:
        obj.select_set(True)


def export_scene_assets(out_dir):
    """把 24 元素箱庭导出成引擎可用的 glb。

    为什么层 -1 要做这件事：这条用例原本的验收是「渲出一张好看的图」，
    产物只有 PNG —— 也就是说箱庭**从来没进过任何引擎**，主人在 Godot 里
    能看到的只有层 2 那 6 件散件，看不到拼装后的村落。
    补上导出，这条链才算两头都验到。

    导出两样：
      1. diorama.glb     —— 整个箱庭合成一份，供引擎里整体摆放
      2. parts/<kind>.glb —— 按类别拆开，供引擎里自由组合/做 LOD
    """
    os.makedirs(out_dir, exist_ok=True)
    parts_dir = os.path.join(out_dir, 'parts')
    os.makedirs(parts_dir, exist_ok=True)

    scene = bpy.context.scene
    # 只导「24 个场景元素」，导图脚手架一律排除：
    #   Terrain_Ground —— 渲图用的地面平板，不属于箱庭内容
    #   Cube.001      —— 体积雾测试用的实体方块（见 place_ground 里的注释）
    # 第一版没排除，misc.glb 的 z 跨度因此是 -230~170，
    # 而其余 6 类都在 0~15 —— 一眼就是混进了不该交付的东西。
    SCAFFOLD = {"Terrain_Ground", "Terrain_Ground.001", "Cube", "Cube.001"}
    objs = [o for o in bpy.data.objects
            if o.type == 'MESH'
            and o.get("kind") not in (None, "")
            and o.name not in SCAFFOLD]
    skipped = len([o for o in bpy.data.objects if o.type == 'MESH']) - len(objs)
    if skipped:
        print("  排除脚手架/未标记对象 %d 个（地面平板、雾测试方块等）" % skipped)
    if not objs:
        print("  WARN: 场景里没有可导出的场景元素 mesh")
        return {}

    # ---- 1. 整体 ----
    for obj in objs:
        obj.data = obj.data.copy()   # 打破链接，才能单独 make_single_user
    bpy.context.view_layer.update()
    _select_objs(objs)
    bpy.ops.object.make_single_user(
        object=True, obdata=True, material=True, animation=False)
    _export_glb(os.path.join(out_dir, 'diorama.glb'))

    # ---- 2. 按类别拆开 ----
    by_kind = {}
    for obj in objs:
        by_kind.setdefault(obj.get('kind', 'misc'), []).append(obj)

    report = {'whole': os.path.join(out_dir, 'diorama.glb'), 'parts': {}}
    for kind, group in sorted(by_kind.items()):
        if len(group) < 1:
            continue
        gpath = os.path.join(parts_dir, '%s.glb' % kind)
        _select_objs(group)
        bpy.ops.object.make_single_user(
            object=True, obdata=True, material=True, animation=False)
        _export_glb(gpath)
        report['parts'][kind] = gpath

    _deselect_all()
    print("  箱庭导出完成：整体 1 份 + 分类 %d 组" % len(report['parts']))
    return report


def main():
    cfg = load_config()
    air_rgb = cfg["air"]["fog_color"]
    world_rgb = cfg["light"]["world_color"]
    fresh_scene()

    # 世界给蓝天空色：暗部靠它出彩度，纯白会把画面洗灰
    bg = bpy.context.scene.world.node_tree.nodes["Background"]
    bg.inputs["Color"].default_value = (world_rgb[0], world_rgb[1], world_rgb[2], 1.0)
    bg.inputs["Strength"].default_value = cfg["light"]["world_strength"]

    build_ground(cfg)
    fog = build_fog(cfg)
    build_lighting(cfg)
    build_camera(cfg)

    layers = {layer["name"]: layer for layer in cfg["depth_layers"]}
    palette = cfg["palette"]
    colliders = cfg.get("collider", {})
    pad = colliders.get("pad", 0.0)

    failures = []
    report = []
    names = []
    for el in cfg["elements"]:
        layer = layers[el["layer"]]
        mats = element_materials(el, layer, palette, air_rgb)
        # 元素默认站在所属层上；需要做前景遮挡的可以单条覆盖 y
        y = float(el.get("y", layer["y"]))
        me = combine_parts(el["name"], build_parts(el, mats))
        obj = bpy.data.objects.new(el["name"], me)
        bpy.context.collection.objects.link(obj)
        obj.location = (el["x"], y, 0.0)
        # 把 JSON 里的分类写进对象自定义属性 —— 导出时按它分组。
        # 第一版导出只拿到 1 个 misc.glb，就是漏了这一步：
        # kind 只活在 JSON 里，从没落到 Blender 对象上。
        obj["kind"] = el.get("kind", "misc")
        obj["layer"] = el.get("layer", "?")
        move_to_collection(obj, make_collection(f"Elements_{layer['name']}"))
        counts = {"verts": len(me.vertices), "faces": len(me.polygons), "objects": 1}
        info = check_anchor_and_collider(obj, pad)
        info.update(counts)
        info["name"] = el["name"]
        info["layer"] = el["layer"]
        info["y"] = y
        report.append(info)
        names.append(el["name"])
        if not info.get("grounded"):
            failures.append(f"{el['name']} 悬空: z_min={info.get('z_min')}")
        if counts.get("verts", 0) <= 0:
            failures.append(f"{el['name']} 求值出 0 顶点")

    setup_render(cfg, 960, 640)
    # 取景：渲染分辨率必须先定下来，frame_camera 要按画幅算框宽
    fit_names = [el["name"] for el in cfg["elements"] if el.get("fit", True)]
    fit = frame_camera(cfg, element_corners(fit_names),
                       margin=cfg["camera"].get("margin", 0.05))
    if fit["culled"]:
        failures.append("有元素落在相机背后（NDC z<=0）")
    if fit["ndc_x"][0] < -0.001 or fit["ndc_x"][1] > 1.001:
        failures.append(f"水平方向出画: {fit['ndc_x']}")
    if fit["ndc_y"][0] < -0.001 or fit["ndc_y"][1] > 1.001:
        failures.append(f"垂直方向出画: {fit['ndc_y']}")

    # 前景压边件不在取景集合里，它们的位置要单独摆：摆到画面下缘去
    placed = place_framing_elements(
        cfg["camera"].get("framing_ndc_y"),
        content_bottom=fit["ndc_y"][0])
    for name, y, ndc in placed:
        print(f"  FRAMING {name:<12} y={y:<9} ndc_y={ndc}")

    out_dir = cfg["output_dir"]
    os.makedirs(out_dir, exist_ok=True)

    fog.hide_render = False
    main_png = render_to(os.path.join(out_dir, "hd2d_diorama.png"), 960, 640)
    thumb_png = render_to(os.path.join(out_dir, "hd2d_diorama_thumb.png"), 200, 133)
    fog.hide_render = True
    nofog_png = render_to(os.path.join(out_dir, "hd2d_diorama_nofog.png"), 960, 640)
    fog.hide_render = False

    print("== 自动取景（world_to_camera_view 迭代，方向锁定只推拉 + 摆正）==")
    print(f"  主体下缘 ndc_y={fit['ndc_y'][0]:.3f} -> 压边件目标 ndc_y="
          f"{max(0.02, fit['ndc_y'][0] - 0.03):.3f}")
    print(f"  取景集合 {len(fit_names)}/{len(cfg['elements'])} 个元素，"
          f"排除 fit:false 的前景压边件 {len(cfg['elements']) - len(fit_names)} 个")
    print("  {camera_location} look_at={look_at} dist={distance} "
          "ndc_x={ndc_x} ndc_y={ndc_y}".format(**fit))

    print("== 元素贴地 / 碰撞盒（碰撞盒 = 求值后世界包围盒 + pad）==")
    for info in report:
        c = info["collider"]
        print("  {name:<14} {layer:<5} y={y:<6} verts={verts:<5} z=[{z_min:.4f},{z_max:.2f}] "
              "collider={c[0]:.2f}x{c[1]:.2f}x{c[2]:.2f}".format(
                  name=info["name"], layer=info["layer"], y=info["y"], verts=info["verts"],
                  z_min=info["z_min"], z_max=info["z_max"], c=c))

    for p in (main_png, thumb_png, nofog_png):
        print(f"  RENDER {p} {os.path.getsize(p)} bytes")

    # ---- 导出引擎可用 glb ----
    # 层 -1 的原始验收只有「渲出一张图」，所以导出这一步是新加的：
    # 没有它，箱庭就只能活在 PNG 里，进不了任何引擎。
    exp_report = {}
    if "--export" in sys.argv or EXPORT_DIR:
        print("== 导出 glb（供引擎加载）==")
        try:
            exp_report = export_scene_assets(EXPORT_DIR)
        except Exception as exc:            # 导出失败不许连累渲图验收
            print("  WARN: 导出失败（渲图结果仍有效）: %s" % exc)

    if failures:
        print("== ACCEPT: FAIL ==")
        for f in failures:
            print("  " + f)
        raise SystemExit(1)
    print(f"== ACCEPT: PASS ({len(report)} 元素全部贴地且求值出几何) ==")


if __name__ == "__main__":
    main()
