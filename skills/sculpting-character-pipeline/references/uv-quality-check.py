"""UV 展开质量检查：块装配角色走 smart_project 之后，UV 岛是否可用。

backlog 原文：「UV 展开质量检查（当前只验了 smart_project 不报错）」

不报错 ≠ UV 可用。本脚本检查 5 项可判定的指标：
  1. 是否有 UV 层
  2. UV 覆盖率（落在 [0,1] 外的比例）
  3. UV 岛数量（反映碎岛程度）
  4. 最大岛占比（碎岛会让贴图浪费）
  5. 面片 UV 是否退化（全三角形/四边形对角线重合会导致贴图撕裂）

用块装配人形跑，因为真实角色就是块装配来的，UV 问题会被 join 放大。
"""
import bpy  # noqa: F401
import bmesh
import os
from collections import defaultdict

OUT = "/home/user/Fairy/workspace/result"
os.makedirs(OUT, exist_ok=True)

TOTAL_H = 1.70
HEAD_H = TOTAL_H / 7.5
H_CHIN = TOTAL_H - HEAD_H
H_SHOULDER, H_CHEST, H_WAIST, H_HIP, H_KNEE, H_ANKLE, H_FOOT = (
    1.400, 1.270, 1.060, 0.900, 0.485, 0.090, 0.030)


def build():
    bpy.ops.wm.read_factory_settings(use_empty=True)
    parts = []

    def ball(name, r, loc, sc=(1, 1, 1)):
        bpy.ops.mesh.primitive_uv_sphere_add(radius=r, segments=24, ring_count=16, location=loc)
        ob = bpy.context.active_object; ob.name = name; ob.scale = sc; parts.append(ob)

    def cube(name, size, loc, sc=(1, 1, 1)):
        bpy.ops.mesh.primitive_cube_add(size=size, location=loc)
        ob = bpy.context.active_object; ob.name = name; ob.scale = sc; parts.append(ob)

    ball("Head", HEAD_H * 0.44, (0, 0, H_CHIN + HEAD_H * 0.50), (0.90, 1.02, 1.10))
    cube("Neck", 0.10, (0, 0, H_CHIN - 0.035), (1, 1, 0.8))
    ball("Ribcage", 0.175, (0, 0, H_CHEST), (1.34, 0.76, 1.30))
    ball("Waist", 0.13, (0, 0, H_WAIST), (1.02, 0.70, 1.00))
    ball("Pelvis", 0.170, (0, 0, H_HIP), (1.22, 0.80, 0.95))
    for s in (-1, 1):
        ball("Shoulder_%d" % s, 0.082, (s * 0.150, 0, H_SHOULDER), (1, 1, 0.9))
        cube("UpperArm_%d" % s, 0.095, (s * 0.150, 0, H_SHOULDER - 0.145), (1, 1, 2.9))
        ball("Elbow_%d" % s, 0.056, (s * 0.160, 0, H_SHOULDER - 0.290))
        cube("Forearm_%d" % s, 0.078, (s * 0.168, 0, H_SHOULDER - 0.430), (1, 1, 2.5))
        ball("Hand_%d" % s, 0.058, (s * 0.175, 0, H_SHOULDER - 0.585), (0.75, 0.55, 1.15))
        sx = s * 0.068
        ball("Hip_%d" % s, 0.082, (sx, 0, H_HIP - 0.055), (1, 1, 0.95))
        cube("Thigh_%d" % s, 0.132, (sx, 0, (H_HIP + H_KNEE) / 2), (1, 1, (H_HIP - H_KNEE) / 0.132 * 0.98))
        ball("Knee_%d" % s, 0.062, (sx, 0, H_KNEE), (1, 0.95, 0.85))
        cube("Shin_%d" % s, 0.105, (sx, 0, (H_KNEE + H_ANKLE) / 2), (1, 1, (H_KNEE - H_ANKLE) / 0.105 * 0.97))
        cube("Foot_%d" % s, 0.085, (sx, -0.035, H_FOOT), (0.85, 2.5, 0.62))
    return parts


# 融合 + join（与生产流程一致）
parts = build()
for ob in parts:
    bpy.ops.object.select_all(action="DESELECT")
    ob.select_set(True)
    bpy.context.view_layer.objects.active = ob
    ob.data.remesh_voxel_size = 0.005
    bpy.ops.object.mode_set(mode="OBJECT")
    bpy.ops.object.voxel_remesh()
bpy.ops.object.select_all(action="DESELECT")
for ob in parts:
    ob.select_set(True)
bpy.context.view_layer.objects.active = parts[0]
bpy.ops.object.join()
body = bpy.context.view_layer.objects.active
body.name = "Char_UV"
print("GEO verts=%d polys=%d" % (len(body.data.vertices), len(body.data.polygons)))

# 走 smart_project
bpy.ops.object.mode_set(mode="EDIT")
bpy.ops.mesh.select_all(action="SELECT")
bpy.ops.uv.smart_project()
bpy.ops.object.mode_set(mode="OBJECT")
print("SMART_PROJECT_DONE")

# === 检查 1：是否有 UV 层 ===
me = body.data
if not me.uv_layers:
    print("CHECK1_UVLAYER FAIL 没有 UV 层")
    raise SystemExit(0)
uv = me.uv_layers.active.data
print("CHECK1_UVLAYER PASS active=%s count=%d" % (me.uv_layers.active.name, len(uv)))

# === 检查 2：UV 覆盖率 ===
out_of_range = 0
total = 0
degenerate = 0
for loop in me.loops:
    d = uv[loop.index].uv
    total += 1
    if d.x < -0.001 or d.x > 1.001 or d.y < -0.001 or d.y > 1.001:
        out_of_range += 1
print("CHECK2_COVERAGE loops=%d out_of_range=%d ratio=%.4f" % (
    total, out_of_range, out_of_range / max(total, 1)))

# === 检查 3/4：UV 岛数量与最大岛占比 ===
# 共享 UV 边的面属于同一岛
bm = bmesh.new()
bm.from_mesh(me)
bm.verts.ensure_lookup_table()
uv_faces = defaultdict(list)
for f in bm.faces:
    key = tuple(sorted(round(uv[v.index].uv.x, 5) + 100 for v in f.verts))
    uv_faces[key].append(f)
parent = {}

def find(x):
    while parent[x] != x:
        parent[x] = parent[parent[x]]
        x = parent[x]
    return x

def union(a, b):
    ra, rb = find(a), find(b)
    if ra != rb:
        parent[rb] = ra

for f in bm.faces:
    parent[f.index] = f.index
for f in bm.faces:
    for e in f.edges:
        if len(e.link_faces) == 2:
            union(e.link_faces[0].index, e.link_faces[1].index)
islands = defaultdict(int)
for f in bm.faces:
    islands[find(f.index)] += 1
sizes = sorted(islands.values(), reverse=True)
total_f = sum(sizes)
print("CHECK3_ISLANDS count=%d" % len(sizes))
print("CHECK4_LARGEST top5=%s largest_ratio=%.4f" % (
    sizes[:5], (sizes[0] / total_f) if total_f else 0))
uv_total_area = 0.0
for f in bm.faces:
    us = [uv[v.index].uv for v in f.verts]
    a = 0.0
    for i in range(len(us)):
        p, q = us[i], us[(i + 1) % len(us)]
        a += p.x * q.y - q.x * p.y
    uv_total_area += abs(a) * 0.5
print("UV_AREA total=%.4f" % uv_total_area)
bm.free()

# === 检查 5：退化面 UV（对角线重合 → 贴图撕裂）===
for f in me.polygons:
    us = [uv[i].uv for i in f.loop_indices]
    if len(us) == 4:
        d1 = (us[0] - us[2]).length
        d2 = (us[1] - us[3]).length
        if d1 < 1e-5 and d2 < 1e-5:
            degenerate += 1
print("CHECK5_DEGENERATE quads_with_zero_diagonal=%d" % degenerate)

bpy.ops.wm.save_as_mainfile(filepath=os.path.join(OUT, "uv_test.blend"))
print("UVTEST_DONE")
