"""用 Blender 真实建模一个人形角色，渲正/侧/3-4 三视角。

目的：为「用 Blender 替代 Tripo 走真实建模」提供可验证的能力证据。
不走任何图像推断路线，全部用基本体 + 体块操作程序化生成。
"""
import bpy  # noqa: F401
import bmesh
import math
import os
import sys
from mathutils import Vector

OUT_DIR = "/home/user/Fairy/workspace/result"
os.makedirs(OUT_DIR, exist_ok=True)

# 真实尺度：1 单位 = 1 米，人 1.70，总高 7.5 头身
TOTAL_H = 1.70
HEAD_H = TOTAL_H / 7.5

bpy.ops.wm.read_factory_settings(use_empty=True)
scene = bpy.context.scene
scene.render.engine = "CYCLES"
scene.cycles.samples = 32
scene.render.resolution_x = 560
scene.render.resolution_y = 720
scene.render.film_transparent = False

# --- 收集体块（先建独立网格，靠穿插融合）---
parts = []


def add_ball(name, r, loc, scale=(1, 1, 1)):
    bpy.ops.mesh.primitive_uv_sphere_add(radius=r, segments=24, ring_count=16, location=loc)
    ob = bpy.context.active_object
    ob.name = name
    ob.scale = scale
    parts.append(ob)
    return ob


def add_cube(name, size, loc, scale=(1, 1, 1)):
    bpy.ops.mesh.primitive_cube_add(size=size, location=loc)
    ob = bpy.context.active_object
    ob.name = name
    ob.scale = scale
    parts.append(ob)
    return ob


# 关键高度（占身高比例）
H_HEAD_TOP = 1.655
H_CHIN = H_HEAD_TOP - HEAD_H          # ~1.432 下巴
H_SHOULDER = 1.400
H_CHEST = 1.270
H_WAIST = 1.060
H_HIP = 0.900
H_KNEE = 0.485
H_ANKLE = 0.090
H_FOOT = 0.030

# 1. 头（略呈蛋形，颅顶比下巴宽）
head = add_ball("Head", HEAD_H * 0.44, (0, 0, H_CHIN + HEAD_H * 0.50),
                scale=(0.90, 1.02, 1.10))

# 2. 颈
add_cube("Neck", 0.10, (0, 0, H_CHIN - 0.035), scale=(1, 1, 0.8))

# 3. 躯干：胸腔 + 腰 + 盆，蛋形叠放
add_ball("Ribcage", 0.175, (0, 0, H_CHEST), scale=(1.34, 0.76, 1.30))
add_ball("Waist", 0.13, (0, 0, H_WAIST), scale=(1.02, 0.70, 1.00))
add_ball("Pelvis", 0.170, (0, 0, H_HIP), scale=(1.22, 0.80, 0.95))

# 4. 手臂：上臂 + 前臂 + 球形关节，靠穿插连上
for side in (-1, 1):
    sx = side * 0.150
    add_ball("Shoulder_%s" % side, 0.082, (sx, 0, H_SHOULDER), scale=(1, 1, 0.9))
    add_cube("UpperArm_%s" % side, 0.095, (side * 0.150, 0, H_SHOULDER - 0.145),
             scale=(1, 1, 2.9))
    add_ball("Elbow_%s" % side, 0.056, (side * 0.160, 0, H_SHOULDER - 0.290))
    add_cube("Forearm_%s" % side, 0.078, (side * 0.168, 0, H_SHOULDER - 0.430),
             scale=(1, 1, 2.5))
    add_ball("Hand_%s" % side, 0.058, (side * 0.175, 0, H_SHOULDER - 0.585),
             scale=(0.75, 0.55, 1.15))

# 5. 腿：大腿 + 小腿 + 脚
for side in (-1, 1):
    sx = side * 0.068
    add_ball("Hip_%s" % side, 0.082, (sx, 0, H_HIP - 0.055), scale=(1, 1, 0.95))
    add_cube("Thigh_%s" % side, 0.132, (sx, 0, (H_HIP + H_KNEE) / 2),
             scale=(1, 1, (H_HIP - H_KNEE) / 0.115 * 0.98))
    add_ball("Knee_%s" % side, 0.062, (sx, 0, H_KNEE), scale=(1, 0.95, 0.85))
    add_cube("Shin_%s" % side, 0.105, (sx, 0, (H_KNEE + H_ANKLE) / 2),
             scale=(1, 1, (H_KNEE - H_ANKLE) / 0.090 * 0.97))
    add_cube("Foot_%s" % side, 0.085, (sx, -0.035, H_FOOT),
             scale=(0.85, 2.5, 0.62))

# --- 阶段 2：融合成连续体（体块互相穿插，voxel remesh 靠重叠区连接）---
# 先把互相穿插的单一部件各自 remesh（融合该部件内部，如头+颈），
# 再合并成一个网格。**不要对全部体块一次性 remesh**：
# voxel_remesh 会静默删除与主体不连通的网格，孤立的手/腿会直接消失。
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
body.name = "Character_Body"
parts = [body]

print("PIPELINE_AFTER_VOXEL verts=%d polys=%d" % (
    len(body.data.vertices), len(body.data.polygons)))

# --- 阶段 3：Multires 提密度 ---
m = body.modifiers.new("multires", "MULTIRES")
m.levels = 2
m.render_levels = 3
bpy.ops.object.modifier_apply(modifier="multires")
print("PIPELINE_AFTER_MULTIRES verts=%d" % len(body.data.vertices))

# --- 材质 + 灯光 + 地面 ---
mat = bpy.data.materials.new("Skin")
mat.use_nodes = True
bsdf = mat.node_tree.nodes["Principled BSDF"]
bsdf.inputs["Base Color"].default_value = (0.82, 0.68, 0.60, 1.0)
bsdf.inputs["Roughness"].default_value = 0.55
body.data.materials.append(mat)

bpy.ops.mesh.primitive_plane_add(size=8, location=(0, 0, 0))
ground = bpy.context.active_object
gmat = bpy.data.materials.new("Ground")
gmat.use_nodes = True
gmat.node_tree.nodes["Principled BSDF"].inputs["Base Color"].default_value = (0.22, 0.23, 0.26, 1)
ground.data.materials.append(gmat)

world = bpy.data.worlds.new("W")
world.use_nodes = True
world.node_tree.nodes["Background"].inputs[0].default_value = (0.05, 0.055, 0.07, 1)
world.node_tree.nodes["Background"].inputs[1].default_value = 1.0
scene.world = world


def add_area(name, loc, energy, size):
    d = bpy.data.lights.new(name, type="AREA")
    d.energy = energy
    d.size = size
    o = bpy.data.objects.new(name, d)
    o.location = loc
    scene.collection.objects.link(o)
    return o


add_area("Key", (1.8, -2.4, 2.6), 320, 2.0)
add_area("Fill", (-2.4, -1.6, 1.4), 110, 2.6)
add_area("Rim", (-0.8, 2.8, 2.2), 200, 1.6)

# 相机：人眼高度平视（身高 0.553 ≈ 0.94m）
cam_data = bpy.data.cameras.new("Cam")
cam = bpy.data.objects.new("Cam", cam_data)
scene.collection.objects.link(cam)
scene.camera = cam
cam_data.lens = 70


# --- 诊断：角色到底在哪 ---
from bpy_extras.object_utils import world_to_camera_view
import numpy as np
bb=[body.matrix_world @ Vector(c) for c in body.bound_box]
print("PIPELINE_BODY verts=%d polys=%d"%(len(body.data.vertices),len(body.data.polygons)))
print("PIPELINE_BBOX x[%.3f..%.3f] y[%.3f..%.3f] z[%.3f..%.3f]"%(
    min(b.x for b in bb),max(b.x for b in bb),
    min(b.y for b in bb),max(b.y for b in bb),
    min(b.z for b in bb),max(b.z for b in bb)))
mid=body.matrix_world.translation
print("PIPELINE_CENTER ndc=%s"%str(tuple(round(q,3) for q in world_to_camera_view(scene,cam,mid))[:2]))
print("PIPELINE_MATS=%s"%[m.name for m in body.data.materials])
print("PIPELINE_ENGINE=%s"%scene.render.engine)

VIEWS = {
    "front": (0.0, -3.6, 0.94),
    "side": (3.6, 0.0, 0.94),
    "three_quarter": (2.5, -2.6, 1.05),
}
TARGET = Vector((0.0, 0.0, 0.95))

for name, loc in VIEWS.items():
    cam.location = loc
    direction = TARGET - Vector(loc)
    cam.rotation_euler = direction.to_track_quat("-Z", "Y").to_euler()
    path = os.path.join(OUT_DIR, "char_%s.png" % name)
    scene.render.filepath = path
    bpy.ops.render.render(write_still=True)
    print("PIPELINE_RENDERED", path, os.path.getsize(path))

# 存 blend 供复用
blend_path = os.path.join(OUT_DIR, "character_blockout.blend")
bpy.ops.wm.save_as_mainfile(filepath=blend_path)
print("PIPELINE_BLEND", blend_path)
print("PIPELINE_DONE")
