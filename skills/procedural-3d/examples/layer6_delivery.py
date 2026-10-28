"""场景示例：游戏素材交付（3d-scene-quality 层 6）。

    /path/to/miniforge3/envs/bpyenv/bin/python3.13 layer6_delivery.py     # 导出
    python3 inspect_delivery.py                                               # 独立检查

条目验收：「glTF 过 inspect，LOD 链完整、可实例化」。这里把「交付」拆成四件实事：

1. **LOD 链**：LOD0 全精度、LOD1 简化（Decimate）、LOD2 = 远景 billboard。
   面数门槛照条目写死：LOD0 < 50k、LOD1 < 10k。**面数是从求值后的网格数出来的**，
   不是把 Decimate 的 ratio 当结果——ratio 是意图，网格才是事实。
2. **碰撞代理**：单独的、低模的、单独一个 glb。碰撞和表现**不该塞进同一个文件**。
3. **引擎友好**：集合分类 Terrain/Assets/Lights/Camera、snake_case 命名、
   原点轴向（glTF 是 Y-up，导出器会做 Z-up→Y-up 的转换，这里**实测验证**而不是假设）。
4. **可实例化**：树/石头这类重复资产要**共用同一个 mesh 数据**，节点只带变换——
   这样引擎里才是一批 instance 而不是 N 份几何。

⚠️ **检查必须由导出方之外来做**：`inspect_delivery.py` 用系统 python 的 trimesh 独立读回 glb。
同一个库导出自检，等于自己判自己卷面及格。
"""

import json
import os
import sys

import bpy
import numpy as np

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
sys.path.insert(0, os.path.normpath(os.path.join(HERE, "..", "scripts")))

import layer4_terrain as T4
import layer5_scatter as T5
import layer3_light_rig as L
import gnkit

CONFIG = os.path.join(HERE, "layer6-delivery.json")


def load_config(path=CONFIG):
    with open(path, "r", encoding="utf-8") as fh:
        cfg = json.load(fh)
    cfg["output_dir"] = os.path.normpath(
        os.path.join(HERE, "..", "..", "..", cfg.get("output_dir", "workspace/result/layer6")))
    return cfg


def build_scene(cfg):
    """和层 4/5 同一座山 + 同一片林，这样三层之间可以直接对照。"""
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
    near, far = T5.build_asset_collection(cfg)
    scatter, points = T5.build_scatter(cfg, terrain, near, far, with_points=True)
    bpy.context.view_layer.update()
    return terrain, scatter, points


def build_lod_chain(cfg, terrain):
    """地形 LOD 链：LOD0 原网格、LOD1 Decimate、LOD2 最低模。

    用 **复制体** 而不是给同一个物体挂一串 modifier——
    引擎要的是三个独立网格，挂在同一物体上的 modifier 在导出时只会得到最后一档。
    """
    t = cfg["lod"]
    lods = {}
    for name, ratio, dec_type in (("terrain_lod0", None, None),
                                  ("terrain_lod1", t["lod1_ratio"], 'COLLAPSE'),
                                  ("terrain_lod2", t["lod2_ratio"], 'COLLAPSE')):
        dup = terrain.copy()
        dup.data = terrain.data.copy()
        dup.name = name
        bpy.context.collection.objects.link(dup)
        if ratio is not None:
            m = dup.modifiers.new("Decimate", 'DECIMATE')
            m.ratio = ratio
            m.decimate_type = dec_type
            m.use_collapse_triangulate = True
        lods[name] = dup
    return lods


def build_collision(cfg, lods):
    """碰撞代理：低模、单独对象、单独文件。

    地形碰撞体用**最狠**的一档简化——碰撞体越粗越快，多余的三角形纯属浪费；
    树/石头用基座盒（box proxy），比三角网便宜得多。
    """
    src = lods["terrain_lod2"]
    proxy = src.copy()
    proxy.data = src.data.copy()
    proxy.name = "collision_terrain"
    bpy.context.collection.objects.link(proxy)
    return {"terrain": proxy}


def mesh_stats(obj):
    """求值后的真实面数/点数——不是 modifier 上写的 ratio。"""
    dg = bpy.context.evaluated_depsgraph_get()
    ev = obj.evaluated_get(dg)
    me = ev.to_mesh()
    verts, faces = len(me.vertices), len(me.polygons)
    zmin = min((me.vertices[i].co.z for i in range(len(me.vertices))), default=0.0)
    ev.to_mesh_clear()
    return {"name": obj.name, "verts": verts, "faces": faces, "z_min": round(zmin, 3)}


def organize_collections(cfg):
    """按条目要求的四类分集合：Terrain / Assets / Lights / Camera。"""
    scene = bpy.context.scene
    made = {}
    for cname in cfg["collections"]:
        c = bpy.data.collections.get(cname)
        if c is None:
            c = bpy.data.collections.new(cname)
            scene.collection.children.link(c)
        made[cname] = c

    def move(obj, cname):
        target = made[cname]
        for c in list(obj.users_collection):
            if c is not target:
                c.objects.unlink(obj)
        if obj.name not in target.objects:
            target.objects.link(obj)

    for o in bpy.data.objects:
        if o.name.startswith("terrain_") or o.name.startswith("collision_"):
            move(o, "Terrain")
        elif o.type == 'LIGHT':
            move(o, "Lights")
        elif o.type == 'CAMERA':
            move(o, "Camera")
        elif o.name in ("Scatter",) or o.name.startswith(("tree_", "rock_", "bb_")):
            move(o, "Assets")
    return made


def export_gltf(path, obj_names=None, export_format='GLB'):
    """glTF 导出，**只导点名的这些物体**。

    ⚠️ 第一版是「选中所有可见物体」，结果三个文件**字节数一模一样**
    （都是 12,951,292 B）——每个文件里装的其实是同一份整场景，
    LOD1 和 collision 只是改了名字。交付文件必须按物体分别导出。
    另外资产原件停在 z=-800，靠 `obj_names` 点名天然排除。
    """
    os.makedirs(os.path.dirname(path), exist_ok=True)
    bpy.ops.object.select_all(action='DESELECT')
    names = obj_names or []
    for n in names:
        o = bpy.data.objects.get(n)
        assert o is not None, f"导出对象不存在：{n}"
        o.select_set(True)
    first = bpy.data.objects.get(names[0]) if names else None
    bpy.context.view_layer.objects.active = first
    if first is not None:
        first.select_set(True)
    bpy.ops.export_scene.gltf(filepath=path, export_format=export_format,
                              use_selection=True,
                              export_apply=True,       # 应用 modifier，导出求值后的网格
                              export_yup=True)
    return {"path": path, "bytes": os.path.getsize(path) if os.path.exists(path) else 0}


def main():
    cfg = load_config()
    out_dir = cfg["output_dir"]
    os.makedirs(out_dir, exist_ok=True)

    terrain, scatter, points = build_scene(cfg)
    # 资产名字必须**在 organize_collections 之前**取：
    # 整理集合会把 tree_/rock_/bb_ 从 Assets_Near 搬进统一��� Assets，
    # 之后再查 Assets_Near 就是空集合，导出会得到一个 132 字节的空 glb。
    asset_names = [o.name for o in bpy.data.collections["Assets_Near"].objects]
    lods = build_lod_chain(cfg, terrain)
    collision = build_collision(cfg, lods)
    organize_collections(cfg)

    stats = {name: mesh_stats(o) for name, o in lods.items()}
    stats["collision_terrain"] = mesh_stats(collision["terrain"])

    # --- 渲 LOD 预览（判据之外，人眼必须自己看）
    previews = {}
    cam = bpy.context.scene.camera
    for name in ("terrain_lod0", "terrain_lod1"):
        for o in bpy.data.objects:
            if o.type == 'MESH':
                o.hide_render = (o.name != name and not o.name.startswith("collision_"))
        previews[name] = L.render_to(os.path.join(out_dir, f"{name}.png"),
                                     cfg["render"]["width"], cfg["render"]["height"])
    for o in bpy.data.objects:
        if o.type == 'MESH':
            o.hide_render = False

    # 交付拆成三个互不掺假的文件：
    #   scene_lod.glb  = 地形 LOD0；assets.glb = 资产库（少量 mesh，供引擎实例化）；
    #   collision.glb  = 碰撞代理；放置点单独走 scatter_points.json。
    # 把散布 realize 成实几何的话，每个实例都会变成独立几何体——那不叫实例化。
    exports = {
        "scene_lod0": export_gltf(os.path.join(out_dir, "scene_lod.glb"), ["terrain_lod0"]),
        "scene_lod1": export_gltf(os.path.join(out_dir, "scene_lod1.glb"), ["terrain_lod1"]),
        "collision": export_gltf(os.path.join(out_dir, "collision.glb"), ["collision_terrain"]),
        "assets": export_gltf(os.path.join(out_dir, "assets.glb"), asset_names),
    }

    # --- 放置点：引擎按这份数据自己撒，才是真的「可实例化」
    dg = bpy.context.evaluated_depsgraph_get()
    ev = points.evaluated_get(dg)
    me = ev.to_mesh()
    # ⚠️ 这里必须做 **Z-up → Y-up** 转换，不能直接把 Blender 原生坐标写出去。
    # 第一版图省事写了 [v.co.x, v.co.y, v.co.z]，同时头部声明 up_axis="+Y (glTF)"
    # —— 于是文件**自己宣称 Y 是竖直轴，实际存的却是 Blender 的 [X, Y, Z]**
    # （Y 是景深、Z 才是高度）。
    # 引擎侧照声明按 [x,y,z] 读，每棵树的高度会取到 ±130，
    # 整片植被被丢到 130 米高空或埋进地下，而且**两边都不报错**。
    # 这个 bug 是 Godot 引擎验证（verify_load.gd）抓出来的；
    # glTF 层面的独立检查（inspect_delivery.py）全绿也照样放行。
    # Blender(x,y,z) → glTF(x, z, -y)：X 不变，Y 取 Blender 的 Z（高度），
    # Z 取 Blender 的 -y（景深翻转，与 Blender 导出 glTF 的规则一致）。
    placements = [[round(v.co.x, 3), round(v.co.z, 3), round(-v.co.y, 3)]
                  for v in me.vertices]
    ev.to_mesh_clear()
    ppath = os.path.join(out_dir, "scatter_points.json")
    with open(ppath, "w", encoding="utf-8") as fh:
        json.dump({"up_axis": "+Y (glTF)", "axis_order": "[X, Y, Z]",
                   "count": len(placements),
                   "mesh_choices": asset_names,
                   "note": "引擎侧按这份点 + mesh_choices 实例化；"
                           "不要把散布 realize 成实几何，那会让每个实例变成独立几何体。"
                           "坐标已从 Blender 原生 (X,Y,Z) 转成 glTF 的 (X,Y,Z)："
                           "Y 分量是高度（非负），Z 分量是景深（可正可负）",
                   "points": placements}, fh, ensure_ascii=False)

    # ---- 轴序硬断言：当场崩，别等引擎来发现 ----
    # 层 6 本身没有自检环节（它只管导出），所以轴序写反这个 bug 一路活到了
    # 引擎验证（verify_load.gd）才暴露，而 glTF 层面的独立检查全绿也照样放行。
    # 在这里加一条廉价断言：既然声明了 Y 是高度，第 1 列就必须整体非负，
    # 且它的跨度远小于第 2 列（地形 260x260，景深才是大跨度的那一维）。
    # 一旦有人把转换省掉写成 Blender 原生 (X,Y,Z)，这里立刻抛错。
    col_y = [p[1] for p in placements]
    col_z = [p[2] for p in placements]
    span_y = max(col_y) - min(col_y)
    span_z = max(col_z) - min(col_z)
    assert min(col_y) >= -0.001, (
        "散布点第 1 列(Y=高度)出现负值 %.3f —— 高度不该为负，"
        "八成又写成 Blender 原生 (X,Y,Z) 了" % min(col_y))
    assert span_y < span_z * 0.5, (
        "散布点 Y(高度)跨度 %.1f 不该 >= Z(景深)跨度 %.1f 的一半 —— 轴序反了"
        % (span_y, span_z))
    print("  散布点轴序自检通过: Y(高度) ∈ [%.1f, %.1f] 跨度 %.1f；"
          "Z(景深) 跨度 %.1f" % (min(col_y), max(col_y), span_y, span_z))

    # --- Y-sort 清单：2D 引擎里哪些对象要按 Y 排序，以及各自的枢轴高度
    ys = []
    for o in bpy.data.objects:
        if o.type == 'MESH' and not o.name.startswith(("terrain_lod", "collision")):
            ys.append({"name": o.name, "pivot_z": round(o.location.z, 3),
                       "y_sort": not o.name.startswith("terrain")})
    manifest = {
        "convention": {
            "up_axis_in_blender": "+Z",
            "up_axis_in_gltf": "+Y (导出器 export_yup=True 转换)",
            "origin": "每个资产 pivot 在其几何底面中心；散布点在坡面上",
            "collections": cfg["collections"],
            "naming": "snake_case，层级前缀 <类别>_<名称>（如 terrain_lod0）",
        },
        "lod": stats,
        "exports": exports,
        "previews": previews,
        "y_sort": ys,
        "scatter_points": {"path": ppath, "count": len(placements)},
    }
    mpath = os.path.join(out_dir, "_delivery_manifest.json")
    with open(mpath, "w", encoding="utf-8") as fh:
        json.dump(manifest, fh, ensure_ascii=False, indent=2)

    print("== 层 6 导出侧自查（面数取自求值后的网格，不是 Decimate ratio）==")
    for k, v in stats.items():
        print(f"  {k}: verts={v['verts']} faces={v['faces']}")
    for k, v in exports.items():
        print(f"  export {k}: {v['bytes']} B")
    print(f"  scatter_points: {len(placements)} 个放置点 -> {ppath}")
    print(f"  manifest: {mpath}")
    print("  ⚠️ 过 inspect 的判定交给独立脚本 inspect_delivery.py（本脚本不当自己的判卷人）")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
