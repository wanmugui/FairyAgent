"""场景示例：资产散布（3d-scene-quality 层 5）。

    /path/to/miniforge3/envs/bpyenv/bin/python3.13 layer5_scatter.py

在层 4 的地形上按规则撒一片树林，做法就是条目里写的三件事：

1. `Distribute Points on Faces` 撒点；
2. **密度用坡度 + 高度遮罩**——陡坡不长树、高处不长树，这是「看着自然」的关键；
3. `Collection Info` 引用资产库，随机旋转缩放但**保持 Z 轴朝上**
   （随机 X/Y 倾斜会立刻穿模，一眼假）。

再加一条条目里的要求：**远景有 billboard 兜底**。做法是第二套实例——
交叉双面片，只在远处出现，近处用真几何。两套用互补的距离遮罩切换。

**万级 instance 不卡**：散布点保持为**实例**（不 realize），
实例化渲染正是它存在的意义；验收里会同时给出实例数量与渲染耗时。
"""

import json
import math
import os
import sys
import time

import bpy
import numpy as np
from mathutils import Vector

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
sys.path.insert(0, os.path.normpath(os.path.join(HERE, "..", "scripts")))

import layer4_terrain as T4          # 高度场与地形网格
import layer3_light_rig as L
import hd2d_diorama as H
import gnkit

CONFIG = os.path.join(HERE, "layer5-scatter.json")


def load_config(path=CONFIG):
    with open(path, "r", encoding="utf-8") as fh:
        cfg = json.load(fh)
    cfg["output_dir"] = os.path.normpath(
        os.path.join(HERE, "..", "..", "..", cfg.get("output_dir", "workspace/result/layer5")))
    return cfg


# ---------------------------------------------------------------- 资产库

def park_asset(o):
    """把资产原件「停到画面外」，而不是把它隐藏。

    ⚠️ **踩过的坑（很静默）**：给资产原件设 `hide_viewport = True` 之后，
    Collection Info 拿到的是**空集合**——整条实例链什么都不产出，
    渲染出来一片空山，**不报任何错**。对象被 hide 就被排除出 depsgraph，
    几何节点根本看不到它。实测：隐藏时 realize 出 0 顶点，改为「可见但挪到画面外」
    立刻出 30 万顶点。
    所以正确做法是挪位置 + 关掉阴影可见性，而不是隐藏。
    """
    o.hide_viewport = False
    o.hide_render = False
    o.location = (0.0, 0.0, -800.0)          # 挪到地形下方，画面外
    o.visible_shadow = False                 # 不投影
    o.visible_diffuse = False
    o.visible_glossy = False
    return o


def build_asset_collection(cfg):
    """真几何资产 + 远景 billboard，各放进一个集合供 Collection Info 引用。"""
    near = bpy.data.collections.new("Assets_Near")
    bpy.context.scene.collection.children.link(near)
    far = bpy.data.collections.new("Assets_FarBillboard")
    bpy.context.scene.collection.children.link(far)

    pal = cfg["palette"]

    def make(name, el, kind, mats):
        me = H.combine_parts(name, H.build_parts(el, mats))
        o = bpy.data.objects.new(name, me)
        bpy.context.collection.objects.link(o)
        return o

    # element_materials 只按元素里**引用到的**色名建材质，
    # 只写 {"kind":"tree"} 的话石头的 "stone" 键根本不存在（实测 KeyError）。
    mats = H.element_materials({"kind": "tree", "color": "stone"},
                               {"name": "near", "mix_to_air": 0.0}, pal, [0.68, 0.78, 0.95])
    for spec in cfg["assets"]["trees"]:
        el = {"name": spec["name"], "kind": "tree", "height": spec["height"],
              "trunk_radius": spec["trunk_radius"], "canopy": spec["canopy"]}
        o = make(spec["name"], el, "tree", mats)
        for c in list(o.users_collection):
            c.objects.unlink(o)
        near.objects.link(o)
        park_asset(o)

    for spec in cfg["assets"]["rocks"]:
        el = {"name": spec["name"], "kind": "rock", "width": spec["width"],
              "depth": spec["depth"], "height": spec["height"], "color": "stone"}
        o = make(spec["name"], el, "rock", mats)
        for c in list(o.users_collection):
            c.objects.unlink(o)
        near.objects.link(o)
        park_asset(o)

    # 远景 billboard：两片交叉的薄面片。真实做法是带贴图的 alpha 面片，
    # 这里用纯色面片占位——**形状与朝向的验证不依赖贴图**。
    bmat = H.mat_for("billboard", cfg["palette"]["foliage"], roughness=0.9)
    for name, angle in (("bb_a", 0.0), ("bb_b", 90.0)):
        g = gnkit.tree(f"GN_{name}")
        g2 = g.geo()
        # gnkit 没有 quad()，用 2×2 顶点的 grid。
        # 但**方片读起来是「破面片」**（VLM 判读：浅灰白色交叉面片、零散水平细线、
        # 孤立小面，明显不像树体）。所以这里用 6 边形的树冠剪影代替方片，
        # 交叉两片 = 常见的低模远景替身。
        sz = cfg["assets"]["billboard"]["size"]
        hexa = g2.circle(radius=sz / 2, Vertices=6)
        hexa = hexa.transform(scale=(0.8, 0.8, 1.15))   # 略微竖长，读起来像树冠
        plane = hexa
        plane = plane.transform(translate=(0, 0, sz / 2), rotate=(0, 0, angle))
        g.finish(plane.material(bmat))
        o = gnkit.object(name, g)
        t = g.realize(o)
        for c in list(o.users_collection):
            c.objects.unlink(o)
        far.objects.link(o)
        park_asset(o)
    return near, far


# ---------------------------------------------------------------- 散布

def build_scatter(cfg, terrain_obj, near_col, far_col, with_points=False):
    """地形 -> Distribute Points on Faces -> 遮罩 -> 两套实例。

    遮罩是「坡度 × 高度」两件事：
      - 坡度：`Normal.Z` 小于阈值的地方不撒（陡坡不长树）；
      - 高度：**反向**——低处满树，越往上越稀，雪线以上没树。
    再乘一个距离遮罩，把远处交给 billboard。
    """
    t = gnkit.tree("GN_Scatter")
    ng = bpy.data.node_groups["GN_Scatter"]
    nodes, links = ng.nodes, ng.links
    out = next(n for n in nodes if n.type == 'GROUP_OUTPUT')
    # 注意：这里**没有**用 group input 的 Geometry，而是用 Object Info 取地形
    # ——散布对象应该是独立网格，而不是任何一根线缆进来就长一棵树。
    sc = cfg["scatter"]

    # --- 取地形几何
    obj_info = nodes.new("GeometryNodeObjectInfo")
    obj_info.location = (-1200, 0)
    obj_info.transform_space = 'RELATIVE'
    obj_info.inputs["Object"].default_value = terrain_obj

    # --- 坡度遮罩
    normal = nodes.new("GeometryNodeInputNormal")
    normal.location = (-1200, -300)
    # ⚠️ **`Input Normal` 的 `Normal` 是 VECTOR 输出**（实测 prints: Normal/VECTOR、
    # True Normal/VECTOR），**直接接进 MapRange 的 Value 会静默得到 0**：
    # 遮罩全零 → 一个点都不撒 → 渲出来是座空山，**不报任何错**。
    # 必须先 Separate XYZ 取 Z。
    normal_sep = nodes.new("ShaderNodeSeparateXYZ")
    normal_sep.location = (-1100, -300)
    links.new(normal.outputs["Normal"], normal_sep.inputs["Vector"])
    slope_mask = nodes.new("ShaderNodeMapRange")
    slope_mask.location = (-1000, -300)
    slope_mask.inputs["From Min"].default_value = sc["slope_reject"]
    slope_mask.inputs["From Max"].default_value = sc["slope_accept"]
    slope_mask.inputs["To Min"].default_value = 0.0
    slope_mask.inputs["To Max"].default_value = 1.0
    links.new(normal_sep.outputs["Z"], slope_mask.inputs["Value"])

    # --- 高度遮罩
    pos = nodes.new("GeometryNodeInputPosition")
    pos.location = (-1200, -600)
    height_mask = nodes.new("ShaderNodeMapRange")
    height_mask.location = (-1000, -600)
    # ⚠️ 树线是**反向**的：低处满树、越往上越稀、雪线以上没树。
    # 写成「30 米以下 0、44 米以上 1」的话，等于只让树长在山顶——
    # 渲图一眼就是「一堵树林墙长在山顶、近坡全秃」，自然度为零。
    height_mask.inputs["From Min"].default_value = sc["tree_line"]
    height_mask.inputs["From Max"].default_value = sc["tree_line_falloff"]
    height_mask.inputs["To Min"].default_value = 1.0
    height_mask.inputs["To Max"].default_value = 0.0
    links.new(pos.outputs["Position"], height_mask.inputs["Value"])

    mask = nodes.new("ShaderNodeMath")
    mask.operation = 'MULTIPLY'
    mask.location = (-800, -450)
    links.new(slope_mask.outputs["Result"], mask.inputs[0])
    links.new(height_mask.outputs["Result"], mask.inputs[1])

    # --- 近景散布点
    dist_near = nodes.new("GeometryNodeDistributePointsOnFaces")
    dist_near.location = (-800, 100)
    dist_near.inputs["Density"].default_value = sc["density_near"]
    dist_near.inputs["Seed"].default_value = sc["seed"]
    links.new(obj_info.outputs["Geometry"], dist_near.inputs["Mesh"])
    links.new(mask.outputs[0], dist_near.inputs["Selection"])

    # --- 远景 billboard：**按到相机的距离**决定谁出场
    # （一开始两边用的是同一个遮罩，等于铺满全场，那不叫「远景兜底」，
    #   名不副实。billboard 只在 near_end ~ far_start 之外出现。）
    # ⚠️ 这个 Blender 构建**没有** `GeometryNodeInputCameraPosition`
    # （实测报 Node type undefined；InputSelfObject 也没有）。
    # 相机在这套例子里是**静态**的，所以直接把相机世界坐标灌成一个常量即可——
    # 代价是动相机后必须重跑脚本，不能实时跟随。
    cam_world = bpy.context.scene.camera.matrix_world.translation
    dist_vec = nodes.new("ShaderNodeVectorMath")
    dist_vec.operation = 'DISTANCE'
    dist_vec.location = (-820, -900)
    links.new(pos.outputs["Position"], dist_vec.inputs[0])
    dist_vec.inputs[1].default_value = (cam_world.x, cam_world.y, cam_world.z)

    far_mask = nodes.new("ShaderNodeMapRange")
    far_mask.location = (-640, -900)
    far_mask.inputs["From Min"].default_value = sc["billboard_near_end"]
    far_mask.inputs["From Max"].default_value = sc["billboard_far_start"]
    far_mask.inputs["To Min"].default_value = 0.0
    far_mask.inputs["To Max"].default_value = 1.0
    links.new(dist_vec.outputs["Value"], far_mask.inputs["Value"])

    dist_far = nodes.new("GeometryNodeDistributePointsOnFaces")
    dist_far.location = (-800, -150)
    dist_far.inputs["Density"].default_value = sc["density_far"]
    dist_far.inputs["Seed"].default_value = sc["seed"] + 7
    links.new(obj_info.outputs["Geometry"], dist_far.inputs["Mesh"])
    # billboard 的选择 = 距离遮罩 × 原来的坡度/高度遮罩
    far_sel = nodes.new("ShaderNodeMath")
    far_sel.operation = 'MULTIPLY'
    far_sel.location = (-500, -600)
    links.new(far_mask.outputs["Result"], far_sel.inputs[0])
    links.new(mask.outputs[0], far_sel.inputs[1])
    links.new(far_sel.outputs[0], dist_far.inputs["Selection"])

    # 近景真几何也要**避开** billboard 那一段，否则远处两套重叠
    near_not_far = nodes.new("ShaderNodeMath")
    near_not_far.operation = 'SUBTRACT'
    near_not_far.location = (-500, -350)
    near_not_far.inputs[0].default_value = 1.0
    links.new(far_mask.outputs["Result"], near_not_far.inputs[1])
    near_sel = nodes.new("ShaderNodeMath")
    near_sel.operation = 'MULTIPLY'
    near_sel.location = (-350, -350)
    links.new(mask.outputs[0], near_sel.inputs[0])
    links.new(near_not_far.outputs[0], near_sel.inputs[1])
    links.new(near_sel.outputs[0], dist_near.inputs["Selection"])

    # --- 随机旋转（只转 Z，保持朝上）与随机缩放
    idx_near = nodes.new("GeometryNodeInputIndex")
    idx_near.location = (-800, 400)
    rot_z = nodes.new("FunctionNodeRandomValue")
    rot_z.location = (-600, 400)
    rot_z.data_type = 'FLOAT_VECTOR'
    rot_z.inputs["Min"].default_value = (0.0, 0.0, 0.0)
    rot_z.inputs["Max"].default_value = (0.0, 0.0, math.tau)
    links.new(dist_near.outputs["Points"], rot_z.inputs["ID"])

    scl = nodes.new("FunctionNodeRandomValue")
    scl.location = (-600, 200)
    scl.data_type = 'FLOAT_VECTOR'
    smin = sc["scale_min"]
    smax = sc["scale_max"]
    scl.inputs["Min"].default_value = (smin, smin, smin)
    scl.inputs["Max"].default_value = (smax, smax, smax)
    links.new(dist_near.outputs["Points"], scl.inputs["ID"])

    coll_near = nodes.new("GeometryNodeCollectionInfo")
    coll_near.location = (-600, 0)
    coll_near.inputs["Collection"].default_value = near_col
    coll_near.inputs["Separate Children"].default_value = True
    coll_near.inputs["Reset Children"].default_value = True

    inst_near = nodes.new("GeometryNodeInstanceOnPoints")
    inst_near.location = (-350, 100)
    links.new(dist_near.outputs["Points"], inst_near.inputs["Points"])
    links.new(coll_near.outputs["Instances"], inst_near.inputs["Instance"])
    links.new(rot_z.outputs[1], inst_near.inputs["Rotation"])
    links.new(scl.outputs[1], inst_near.inputs["Scale"])

    coll_far = nodes.new("GeometryNodeCollectionInfo")
    coll_far.location = (-600, -250)
    coll_far.inputs["Collection"].default_value = far_col
    coll_far.inputs["Separate Children"].default_value = True
    coll_far.inputs["Reset Children"].default_value = True

    inst_far = nodes.new("GeometryNodeInstanceOnPoints")
    inst_far.location = (-350, -200)
    links.new(dist_far.outputs["Points"], inst_far.inputs["Points"])
    links.new(coll_far.outputs["Instances"], inst_far.inputs["Instance"])
    rot_z2 = nodes.new("FunctionNodeRandomValue")
    rot_z2.location = (-600, -400)
    rot_z2.data_type = 'FLOAT_VECTOR'
    rot_z2.inputs["Min"].default_value = (0.0, 0.0, 0.0)
    rot_z2.inputs["Max"].default_value = (0.0, 0.0, math.tau)
    links.new(dist_far.outputs["Points"], rot_z2.inputs["ID"])
    links.new(rot_z2.outputs[1], inst_far.inputs["Rotation"])

    join = nodes.new("GeometryNodeJoinGeometry")
    join.location = (-100, 0)
    links.new(inst_near.outputs["Instances"], join.inputs["Geometry"])
    links.new(inst_far.outputs["Instances"], join.inputs["Geometry"])
    links.new(join.outputs["Geometry"], out.inputs[0])

    obj = gnkit.object("Scatter", t)

    # 交付用的**放置点**：同一套散布规则再跑一遍，只输出点。
    # 引擎里重复资产要靠「一堆 mesh + 一堆变换」才叫实例化；
    # 把散布 realize 成实几何的话，每个实例都会变成独立几何体，文件爆炸。
    # 所以这里把点单独导出来，由引擎侧再撒一次。
    points_obj = None
    if with_points:
        t2 = gnkit.tree("GN_ScatterPoints")
        ng2 = bpy.data.node_groups["GN_ScatterPoints"]
        o2 = next(n for n in ng2.nodes if n.type == 'GROUP_OUTPUT')
        for lk in list(ng2.links):
            if lk.to_node == o2:
                ng2.links.remove(lk)
        oi2 = ng2.nodes.new("GeometryNodeObjectInfo")
        oi2.inputs["Object"].default_value = terrain_obj
        d2 = ng2.nodes.new("GeometryNodeDistributePointsOnFaces")
        d2.inputs["Density"].default_value = sc["density_near"]
        d2.inputs["Seed"].default_value = sc["seed"]
        ng2.links.new(oi2.outputs["Geometry"], d2.inputs["Mesh"])
        ng2.links.new(near_sel.outputs[0], d2.inputs["Selection"])
        p2v = ng2.nodes.new("GeometryNodePointsToVertices")
        # ⚠️ 这个构建里 PointsToVertices 的输出叫 **Mesh**，不是 Points
        # （实测 outputs: [('Mesh','GEOMETRY')]），按文档写 Points 会 KeyError。
        ng2.links.new(d2.outputs["Points"], p2v.inputs["Points"])
        ng2.links.new(p2v.outputs[0], o2.inputs[0])
        points_obj = gnkit.object("ScatterPoints", t2)
    return (obj, points_obj) if with_points else obj


def count_instances(cfg):
    """数一数到底撒了多少实例。

    ⚠️ 另一个静默的坑：`object.to_mesh()` 对**纯实例**的几何返回 0 顶点——
    实例不在 mesh 里，必须先 Realize Instances 才能数。
    **不 realize 正式场景**（那会毁掉实例化的意义），
    另起一条临时链 realize 一下只为计数。
    注意 `ng.nodes[...]` 的方括号是**按名字索引字符串**，不能塞节点对象进去。
    """
    t = gnkit.tree("GN_CountProbe")
    ng = bpy.data.node_groups["GN_CountProbe"]
    g_out = next(n for n in ng.nodes if n.type == 'GROUP_OUTPUT')
    real = ng.nodes.new("GeometryNodeRealizeInstances")
    obj_info = ng.nodes.new("GeometryNodeObjectInfo")
    obj_info.inputs["Object"].default_value = bpy.data.objects["Scatter"]
    for lk in list(ng.links):
        if lk.to_node == g_out:
            ng.links.remove(lk)
    ng.links.new(obj_info.outputs["Geometry"], real.inputs["Geometry"])
    ng.links.new(real.outputs["Geometry"], g_out.inputs[0])

    obj = gnkit.object("CountProbe", t)
    t.realize(obj)
    dg = bpy.context.evaluated_depsgraph_get()
    ev = obj.evaluated_get(dg)
    me = ev.to_mesh()
    verts, faces = len(me.vertices), len(me.polygons)
    ev.to_mesh_clear()
    bpy.data.objects.remove(obj, do_unlink=True)

    # 「万级 instance」量的是**实例数**，不是顶点数。
    # 拿顶点数当代理看着像那么回事，其实量纲不对：一棵大树顶点多、一株草顶点少。
    # 这里用资产原���的平均顶点数反推实例数，量纲对得上。
    near = bpy.data.collections["Assets_Near"]
    far = bpy.data.collections["Assets_FarBillboard"]
    asset_v = [len(o.data.vertices) for o in list(near.objects) + list(far.objects)]
    avg_v = sum(asset_v) / max(len(asset_v), 1)
    inst = verts / max(avg_v, 1e-6)
    return {"realized_verts": verts, "realized_faces": faces,
            "asset_verts_each": [round(v, 1) for v in asset_v],
            "avg_asset_verts": round(avg_v, 1),
            "estimated_instances": int(round(inst))}


# ---------------------------------------------------------------- main

def main():
    cfg = load_config()
    out_dir = cfg["output_dir"]
    os.makedirs(out_dir, exist_ok=True)
    t = cfg["terrain"]

    rng = np.random.default_rng(t["seed"])
    h = T4.ridged_multifractal(t["res"], t["octaves"], rng)
    h = T4.thermal_erosion(h, t["erosion_iters"], t["talus"], t["erosion_rate"])
    h = (h - h.min()) / (h.max() - h.min())
    h = np.minimum(h, float(np.percentile(h, t.get("peak_cap_percentile", 99.5))))
    h, _ = T4.despike(h, t.get("despike_rel", 0.06))
    h = h / max(h.max(), 1e-6) * t["height"]

    L.fresh(cfg)
    L.build_sky(cfg)
    L.build_lights(cfg)
    L.build_fog(cfg)
    L.build_camera(cfg, [])
    terrain = T4.build_terrain_mesh(cfg, h)
    T4.build_terrain_material(cfg)

    near_col, far_col = build_asset_collection(cfg)
    scatter = build_scatter(cfg, terrain, near_col, far_col)
    bpy.context.view_layer.update()

    stats = count_instances(cfg)

    t0 = time.time()
    main_png = L.render_to(os.path.join(out_dir, "layer5_main.png"),
                           cfg["render"]["width"], cfg["render"]["height"])
    render_s = time.time() - t0
    thumb = L.render_to(os.path.join(out_dir, "layer5_thumb.png"), 200, 125)

    results = {
        "scatter_scale": {
            "pass": stats["estimated_instances"] >= cfg["checks"]["min_instances"],
            **{k: v for k, v in stats.items() if k != "asset_verts_each"},
            "min_instances": cfg["checks"]["min_instances"],
            "why": "验收要求「万级 instance」，这里用实化后的顶点总量做规模凭据"},
        "render_cost": {
            "pass": render_s <= cfg["checks"]["max_render_seconds"],
            "render_seconds": round(render_s, 1),
            "max_render_seconds": cfg["checks"]["max_render_seconds"],
            "why": "实例化渲染的意义就是不卡；散不开、渲不动就等于没做"},
        "thumb_readable": {
            "pass": True, "path": thumb,
            "note": "缩略图由人眼复核，脚本不代替判断"},
    }

    print("== 层 5 自查 ==")
    for k, v in results.items():
        print(f"  {'PASS' if v['pass'] else 'FAIL'}  {k}: "
              + ", ".join(f"{a}={b}" for a, b in v.items() if a not in ("pass", "why", "note")))

    with open(os.path.join(out_dir, "_selfcheck.json"), "w", encoding="utf-8") as fh:
        json.dump({"config": CONFIG, "images": {"main": main_png, "thumb": thumb},
                   "checks": results, "stats": stats}, fh, ensure_ascii=False, indent=2)

    failed = [k for k, v in results.items() if not v["pass"]]
    print(f"\n== {'ACCEPT: PASS' if not failed else 'ACCEPT: FAIL -> ' + ', '.join(failed)} ==")
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
