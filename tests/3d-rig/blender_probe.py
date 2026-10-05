"""3D rig 回归探针：在 Blender 里跑一次，输出全部指标为一行 JSON。

为什么是探针而不是多个脚本：启动 Blender 一次要几十秒，全部指标一次收集，
pytest 侧只启动一次，避免回归套件本身变成速率瓶颈。

输出格式：以 PROBE_JSON= 开头的一行 JSON，交给 tests/3d-rig/test_rig_regression.py
做断言。所有基线均来自实测，不是拍脑袋填的。
"""
import bpy
import json
import numpy as np

BLEND = "/home/user/Fairy/workspace/result/character_rigged.blend"

POSES = {
    "limit": {
        "shoulder.L": (-0.55, 0, 0), "upper_arm.L": (-0.60, 0, 0.35),
        "forearm.L": (-1.30, 0, 0), "hand.L": (0, 0, 0.30),
        "shoulder.R": (0.55, 0, 0), "upper_arm.R": (0.60, 0, -0.35),
        "forearm.R": (-1.30, 0, 0), "hand.R": (0, 0, -0.30),
        "thigh.L": (0, 0, 0.18), "shin.L": (0.42, 0, 0),
        "thigh.R": (0, 0, -0.18), "shin.R": (0.42, 0, 0),
    },
    "curl": {
        "shoulder.L": (-0.20, 0, 0.10), "upper_arm.L": (0, 0, 0.90),
        "forearm.L": (0, 0, 2.20), "hand.L": (0, 0, 0.40),
        "shoulder.R": (-0.20, 0, -0.10), "upper_arm.R": (0, 0, -0.90),
        "forearm.R": (0, 0, 2.20), "hand.R": (0, 0, -0.40),
        "thigh.L": (0, 0, 0.08), "thigh.R": (0, 0, -0.08),
    },
    "lean": {
        "spine": (0.12, 0, 0), "chest": (0.10, 0, 0), "neck": (0.16, 0, 0),
        "head": (0.10, 0, 0), "hips": (0.06, 0, 0),
        "thigh.L": (-0.30, 0, 0), "thigh.R": (-0.30, 0, 0),
    },
}

bpy.ops.wm.open_mainfile(filepath=BLEND)
arm = bpy.data.objects["FairyRig"]
body = bpy.data.objects["Character_Body"]
me = body.data
vg = {g.index: g.name for g in body.vertex_groups}
out = {}

# ---------- 绑定结构 ----------
bone_names = [b.name for b in arm.data.bones]
out["bone_count"] = len(bone_names)
out["bone_pairs"] = len([n for n in bone_names if n.endswith(".L")])
empties = []
for b in arm.data.bones:
    if not any(v.groups and max(v.groups, key=lambda g: g.weight).group is not None
               and vg.get(max(v.groups, key=lambda g: g.weight).group) == b.name
               and max(v.groups, key=lambda g: g.weight).weight > 0.5
               for v in me.vertices):
        empties.append(b.name)
out["empty_bones"] = sorted(empties)
nweighted = sum(1 for v in me.vertices if v.groups)
out["free_vertices"] = len(me.vertices) - nweighted
mods = [m.type for m in body.modifiers]
out["armature_modifier_first"] = bool(mods) and mods[0] == "ARMATURE"
am = next((m for m in body.modifiers if m.type == "ARMATURE"), None)
out["use_deform_preserve_volume"] = bool(am and am.use_deform_preserve_volume)


# ---------- 选中集与边 ----------
def dominant(bone):
    bi = next(i for i, n in vg.items() if n == bone)
    return sorted(v.index for v in me.vertices
                  if v.groups and max(v.groups, key=lambda g: g.weight).group == bi
                  and max(v.groups, key=lambda g: g.weight).weight > 0.5)


FOREV = dominant("forearm.L")
HANDV = dominant("hand.L")
BAND = {i for i, v in enumerate(me.vertices) if 0.385 <= v.co.x <= 0.500}
EDGES = [(e.vertices[0], e.vertices[1]) for e in me.edges
         if e.vertices[0] in BAND or e.vertices[1] in BAND]
ALL_EDGES = [(e.vertices[0], e.vertices[1]) for e in me.edges]
out["selfcheck_forearm_extent_m"] = None   # 运行时填，见下


def set_pose(rot):
    bpy.context.view_layer.objects.active = arm
    for o in bpy.context.view_layer.objects:
        o.select_set(False)
    arm.select_set(True)
    bpy.ops.object.mode_set(mode="POSE")
    for pb in arm.pose.bones:
        pb.rotation_mode = "XYZ"
        pb.rotation_euler = (0, 0, 0)
    for n, v in rot.items():
        pb = arm.pose.bones.get(n)
        if pb:
            pb.rotation_mode = "XYZ"
            pb.rotation_euler = v
    bpy.ops.object.mode_set(mode="OBJECT")
    bpy.context.view_layer.update()


def coords():
    d = bpy.context.evaluated_depsgraph_get()
    e = body.evaluated_get(d)
    return np.array([(e.matrix_world @ v.co)[:] for v in e.data.vertices])


def edge_len(P, edges):
    return np.array([np.linalg.norm(P[a] - P[b]) for a, b in edges])


def flat(P, idx):
    Q = P[idx]
    w = np.sort(np.abs(np.linalg.eigvalsh(np.cov((Q - Q.mean(axis=0)).T))))
    return w[0] / max(w[-1], 1e-18)


set_pose({})
R = coords()
ext = R[FOREV].max(axis=0) - R[FOREV].min(axis=0)
out["selfcheck_forearm_extent_m"] = [round(float(x), 4) for x in ext]
out["forearm_vert_count"] = len(FOREV)
out["hand_vert_count"] = len(HANDV)

base_all = edge_len(R, ALL_EDGES)
base_band = edge_len(R, EDGES)

out["poses"] = {}
for name, rot in POSES.items():
    set_pose(rot)
    C = coords()
    r = edge_len(C, ALL_EDGES) / base_all
    out["poses"][name] = {"p50": round(float(np.median(r)), 4),
                          "p01": round(float(np.percentile(r, 1)), 4),
                          "pct_below_0_85": round(float((r < 0.85).mean()), 4)}
    if name == "curl":
        rb = edge_len(C, EDGES) / base_band
        out["forearm_flat_ratio"] = round(float(flat(C, FOREV) / flat(R, FOREV)), 4)
        out["hand_flat_ratio"] = round(float(flat(C, HANDV) / flat(R, HANDV)), 4)
        out["elbow_edge_median_ratio"] = round(float(np.median(rb)), 4)
        out["elbow_edge_pct_below_085"] = round(float((rb < 0.85).mean()), 4)

set_pose({})
print("PROBE_JSON=" + json.dumps(out, ensure_ascii=False))
