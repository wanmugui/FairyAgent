"""双环境能力探测：sculpting-character-pipeline skill 的每条实测结论都在这里验证。

用法：
  4.5.14 : /tmp/assetpipe/blender-4.5.14-linux-x64/blender -b --factory-startup -P probe.py
  5.1.0  : /home/user/miniforge3/envs/bpyenv/bin/python3.13 probe.py
"""
import bpy  # noqa: F401  必须先于 bmesh
import bmesh  # noqa: F401

import addon_utils

print("PROBE_VERSION", bpy.app.version_string)

OUT = []


def chk(name, fn):
    try:
        r = fn()
        OUT.append("OK   %s%s" % (name, (" -> " + str(r)) if r else ""))
    except Exception as e:
        OUT.append("FAIL %s -> %s: %s" % (name, type(e).__name__, e))


def reset():
    bpy.ops.wm.read_factory_settings(use_empty=True)


def body():
    reset()
    bpy.ops.mesh.primitive_uv_sphere_add(radius=1.0, segments=16, ring_count=8)
    bpy.ops.mesh.primitive_cube_add(size=1.2, location=(0.8, 0, 0))
    return bpy.context.view_layer.objects.active


# --- 阶段 2：Voxel Remesh ---
def t_voxel():
    ob = body()
    ob.data.remesh_voxel_size = 0.05
    bpy.ops.object.mode_set(mode="OBJECT")
    bpy.ops.object.voxel_remesh()
    return "verts=%d" % len(ob.data.vertices)


chk("PROBE_VOXEL", t_voxel)


# --- 阶段 4：Quadriflow 参数名（skill: 4.5 是 target_faces，不是 target_number_of_faces）---
def t_quad_new():
    ob = body()
    bpy.ops.object.mode_set(mode="OBJECT")
    bpy.ops.object.quadriflow_remesh(target_faces=200)
    tri = sum(1 for p in ob.data.polygons if len(p.vertices) == 3)
    return "faces=%d tris=%d" % (len(ob.data.polygons), tri)


def t_quad_old():
    ob = body()
    bpy.ops.object.mode_set(mode="OBJECT")
    bpy.ops.object.quadriflow_remesh(target_number_of_faces=200)
    return "old param accepted"


chk("PROBE_QUAD_TARGET_FACES", t_quad_new)
chk("PROBE_QUAD_TARGET_NUMBER_OF_FACES", t_quad_old)


# --- 阶段 3：Multires ---
def t_multires():
    ob = body()
    m = ob.modifiers.new("multires", "MULTIRES")
    m.levels = 2
    m.render_levels = 3
    bpy.ops.object.modifier_apply(modifier="multires")
    return "verts=%d" % len(ob.data.vertices)


chk("PROBE_MULTIRES", t_multires)


# --- 阶段 3：Displace + 程序化纹理（skill: NOISE 无 noise_scale，用 VORONOI）---
def t_disp():
    ob = body()
    tex = bpy.data.textures.new("P", type="VORONOI")
    tex.noise_scale = 0.9
    d = ob.modifiers.new("d", "DISPLACE")
    d.texture = tex
    d.strength = 0.03
    d.mid_level = 0.5
    bpy.ops.object.modifier_apply(modifier="d")
    return "verts=%d" % len(ob.data.vertices)


def t_noise_attr():
    tex = bpy.data.textures.new("N", type="NOISE")
    return "noise_scale=%s" % getattr(tex, "noise_scale", "<ABSENT>")


chk("PROBE_DISPLACE_VORONOI", t_disp)
chk("PROBE_NOISE_ATTR", t_noise_attr)


# --- symmetrize 方向参数（skill: 4.5 要写全 POSITIVE_X）---
def t_sym():
    ob = body()
    bpy.ops.object.mode_set(mode="EDIT")
    bpy.ops.mesh.select_all(action="SELECT")
    try:
        bpy.ops.mesh.symmetrize(direction="POSITIVE_X")
        return "POSITIVE_X ok"
    except TypeError:
        bpy.ops.mesh.symmetrize(direction="X")
        return "POSITIVE_X REJECTED but 'X' works"


chk("PROBE_SYMMETRIZE", t_sym)


# --- 阶段 5：CYCLES 烘焙前置条件 ---
def t_cycles():
    bpy.context.scene.render.engine = "CYCLES"
    bpy.ops.object.mode_set(mode="EDIT")
    bpy.ops.mesh.select_all(action="SELECT")
    bpy.ops.uv.smart_project()
    return "smart_project ok"


chk("PROBE_UV_CYCLES", t_cycles)


# === 核心争议点：笔刷雕刻在无头环境到底能不能用 ===
def t_sculpt_state():
    st = bpy.context.scene.tool_settings.sculpt
    return "brush=%r asset_ref=%r" % (st.brush, st.brush_asset_reference)


chk("PROBE_SCULPT_STATE", t_sculpt_state)


def t_sculpt_assets():
    mods = []
    for m in addon_utils.modules():
        n = m.__name__
        if "brush" in n or "asset" in n:
            mods.append(n)
    return "modules=%s" % (mods or "<none>")


chk("PROBE_SCULPT_ASSETS", t_sculpt_assets)


def t_brush_stroke():
    """真调一次 brush_stroke，成功则顶点位置改变。"""
    ob = body()
    bpy.ops.object.mode_set(mode="EDIT")
    bpy.ops.mesh.select_all(action="SELECT")
    before = [v.co.copy() for v in ob.data.vertices]
    st = bpy.context.scene.tool_settings
    st.sculpt.brush = bpy.data.brushes.new("probe") if not st.sculpt.brush else st.sculpt.brush
    stroke = [{
        "name": "probe",
        "stroke": [{
            "location": (0.0, 0.0, 1.0),
            "pressure": 1.0,
            "size": 30.0,
            "is_start": True,
        }],
    }]
    bpy.ops.sculpt.brush_stroke(stroke=stroke, mode="SCULPT")
    after = [v.co.copy() for v in ob.data.vertices]
    moved = sum(1 for a, b in zip(after, before) if (a - b).length > 1e-6)
    return "moved=%d/%d" % (moved, len(after))


chk("PROBE_BRUSHSTROKE", t_brush_stroke)

print("\n".join(OUT))
print("PROBE_DONE")
