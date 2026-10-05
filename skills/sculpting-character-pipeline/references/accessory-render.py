"""验证：Voxel Remesh 的「静默删除」在「带配饰的角色」上如何表现。

背景（见 dev-backlog 3d-blender-pipeline 第 4 组）：
24 5.14 独立 Blender，路径已固化。
已知结论：对全部体块一次性 voxel_remesh，会**静默删除**与主体不连通的网格。
本文回答的更具体的问题是：**配饰（帽子、披风、背包）这类天然是独立体块的部件，
在正确写法（逐件 remesh + join）下能否存活，以及错误写法下损失多少。**

实验设计：
  组 A 正确写法：逐件 remesh，最后 join
  组 B 错误写法：全部体块一次性 remesh
每个配饰在 remesh 前打标记点，用顶点距离判定存活，不靠看图。
"""
import bpy  # noqa: F401  必须最先 import
import bmesh
from mathutils import Vector

TOTAL_H = 1.70
HEAD_H = TOTAL_H / 7.5

H_CHIN = TOTAL_H - HEAD_H
H_SHOULDER = 1.400
H_CHEST = 1.270
H_WAIST = 1.060
H_HIP = 0.900
H_KNEE = 0.485
H_ANKLE = 0.090
H_FOOT = 0.030


def build_all():
    """返回 (parts, accessories) —— accessories 是配饰的标记信息。"""
    parts = []
    accs = []

    def ball(name, r, loc, scale=(1, 1, 1), acc=None):
        bpy.ops.mesh.primitive_uv_sphere_add(radius=r, segments=24, ring_count=16, location=loc)
        ob = bpy.context.active_object
        ob.name = name
        ob.scale = scale
        parts.append(ob)
        if acc:
            accs.append((acc, Vector(loc), r))
        return ob

    def cube(name, size, loc, scale=(1, 1, 1), acc=None):
        bpy.ops.mesh.primitive_cube_add(size=size, location=loc)
        ob = bpy.context.active_object
        ob.name = name
        ob.scale = scale
        parts.append(ob)
        if acc:
            accs.append((acc, Vector(loc), size * max(scale) * 0.5))
        return ob

    # 身体（与 blockout-character.py 相同的贴身尺寸）
    ball("Head", HEAD_H * 0.44, (0, 0, H_CHIN + HEAD_H * 0.50), scale=(0.90, 1.02, 1.10))
    cube("Neck", 0.10, (0, 0, H_CHIN - 0.035), scale=(1, 1, 0.8))
    ball("Ribcage", 0.175, (0, 0, H_CHEST), scale=(1.34, 0.76, 1.30))
    ball("Waist", 0.13, (0, 0, H_WAIST), scale=(1.02, 0.70, 1.00))
    ball("Pelvis", 0.170, (0, 0, H_HIP), scale=(1.22, 0.80, 0.95))
    for side in (-1, 1):
        sx = side * 0.150
        ball("Shoulder_%d" % side, 0.082, (sx, 0, H_SHOULDER), scale=(1, 1, 0.9))
        cube("UpperArm_%d" % side, 0.095, (side * 0.150, 0, H_SHOULDER - 0.145), scale=(1, 1, 2.9))
        ball("Elbow_%d" % side, 0.056, (side * 0.160, 0, H_SHOULDER - 0.290))
        cube("Forearm_%d" % side, 0.078, (side * 0.168, 0, H_SHOULDER - 0.430), scale=(1, 1, 2.5))
        ball("Hand_%d" % side, 0.058, (side * 0.175, 0, H_SHOULDER - 0.585), scale=(0.75, 0.55, 1.15))
        sx = side * 0.068
        ball("Hip_%d" % side, 0.082, (sx, 0, H_HIP - 0.055), scale=(1, 1, 0.95))
        cube("Thigh_%d" % side, 0.132, (sx, 0, (H_HIP + H_KNEE) / 2),
             scale=(1, 1, (H_HIP - H_KNEE) / 0.132 * 0.98))
        ball("Knee_%d" % side, 0.062, (sx, 0, H_KNEE), scale=(1, 0.95, 0.85))
        cube("Shin_%d" % side, 0.105, (sx, 0, (H_KNEE + H_ANKLE) / 2),
             scale=(1, 1, (H_KNEE - H_ANKLE) / 0.105 * 0.97))
        cube("Foot_%d" % side, 0.085, (sx, -0.035, H_FOOT), scale=(0.85, 2.5, 0.62))

    # 配饰：三件都是**独立体块**，接触程度各不相同
    # 1) 帽子：坐落在头顶，与颅顶相交（安全）
    ball("Acc_Hat", 0.10, (0, 0, H_CHIN + HEAD_H * 0.95), scale=(1.15, 1.15, 0.55),
         acc="Hat_touching")
    # 2) 披风：挂在肩后，轻微接触（临界）
    cube("Acc_Cape", 0.06, (0, -0.14, H_SHOULDER - 0.22), scale=(1.5, 0.35, 2.6),
         acc="Cape_grazing")
    # 3) 背包：**完全悬空**，不接触身体（危险）
    ball("Acc_Pack", 0.11, (0, 0.19, H_CHEST), scale=(1.0, 0.75, 1.25),
         acc="Pack_floating")

    return parts, accs


def run_group(label, per_part):
    bpy.ops.wm.read_factory_settings(use_empty=True)
    parts, accs = build_all()
    n_before = len(parts)

    if per_part:
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
        # join 后 parts 里的对象已失效，只有 body 有效
        parts = [body]
    else:
        bpy.ops.object.select_all(action="DESELECT")
        for ob in parts:
            ob.select_set(True)
        bpy.context.view_layer.objects.active = parts[0]
        for ob in parts:
            ob.data.remesh_voxel_size = 0.005
        bpy.ops.object.mode_set(mode="OBJECT")
        bpy.ops.object.voxel_remesh()
        body = bpy.context.view_layer.objects.active
        others = [o for o in parts if o is not body]
        for o in others:
            if o.name in bpy.data.objects:
                bpy.data.objects.remove(o, do_unlink=True)

    # 存活判定：配饰标记点附近是否存在顶点
    verts = [body.matrix_world @ v.co for v in body.data.vertices]
    bb = [body.matrix_world @ Vector(c) for c in body.bound_box]
    print("GROUP %s parts=%d verts=%d bbox_z[%.3f..%.3f] bbox_x[%.3f..%.3f]" % (
        label, n_before, len(body.data.vertices),
        min(v.z for v in bb), max(v.z for v in bb),
        min(v.x for v in bb), max(v.x for v in bb)))

    survived = []
    for name, center, radius in accs:
        # 在配饰包围球内找顶点
        near = 0
        for v in verts:
            if (v - center).length <= radius * 1.35:
                near += 1
        ok = near > 0
        survived.append((name, ok, near))
        print("  ACC %-16s %s  verts_near=%d" % (name, "ALIVE" if ok else "DEAD", near))

    alive = sum(1 for _, ok, _ in survived if ok)
    print("RESULT %s accessories_alive=%d/%d" % (label, alive, len(accs)))
    return alive, len(accs), len(body.data.vertices)


# 组 B 先跑（正确写法的对照对象在后面）
alive_b, tot, vb = run_group("B_bulk", per_part=False)
alive_a, tot, va = run_group("A_perpart", per_part=True)
print("SUMMARY bulk=%d/%d perpart=%d/%d" % (alive_b, tot, alive_a, tot))
print("PROBE_DONE")

# === 渲染验证：只渲正确写法的结果，配饰必须肉眼可见 ===
import os
OUT = "/home/user/Fairy/workspace/result"
os.makedirs(OUT, exist_ok=True)

bpy.ops.wm.read_factory_settings(use_empty=True)
parts, accs = build_all()
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
body.name = "Char_Accessories"

# 配饰用第二种材质，便于区分
mat_body = bpy.data.materials.new("Body")
mat_body.use_nodes = True
mat_body.node_tree.nodes["Principled BSDF"].inputs["Base Color"].default_value = (0.80, 0.62, 0.52, 1)
mat_acc = bpy.data.materials.new("Accessory")
mat_acc.use_nodes = True
mat_acc.node_tree.nodes["Principled BSDF"].inputs["Base Color"].default_value = (0.15, 0.20, 0.55, 1)

# join 后无法按对象分材质，用顶点 z 位置区分：帽子/披风/背包所在高度带
for p in body.data.polygons:
    c = body.matrix_world @ p.center
    if c.z > H_CHIN + HEAD_H * 0.80 or (c.y > 0.13 and c.z > H_CHEST - 0.20) or c.y < -0.10:
        p.material_index = 1
    else:
        p.material_index = 0
body.data.materials.append(mat_body)
body.data.materials.append(mat_acc)

bpy.ops.mesh.primitive_plane_add(size=8, location=(0, 0, 0))
ground = bpy.context.active_object
gm = bpy.data.materials.new("Ground")
gm.use_nodes = True
gm.node_tree.nodes["Principled BSDF"].inputs["Base Color"].default_value = (0.20, 0.21, 0.24, 1)
ground.data.materials.append(gm)

world = bpy.data.worlds.new("W")
world.use_nodes = True
world.node_tree.nodes["Background"].inputs[0].default_value = (0.05, 0.055, 0.07, 1)
scene = bpy.context.scene
scene.world = world
scene.render.engine = "CYCLES"
scene.cycles.samples = 32
scene.render.resolution_x = 620
scene.render.resolution_y = 820

def area(name, loc, energy, size):
    d = bpy.data.lights.new(name, type="AREA"); d.energy = energy; d.size = size
    o = bpy.data.objects.new(name, d); o.location = loc; scene.collection.objects.link(o)
area("Key", (1.8, -2.4, 2.6), 340, 2.0)
area("Fill", (-2.4, -1.6, 1.4), 120, 2.6)
area("Rim", (-0.8, 2.8, 2.2), 210, 1.6)

cd = bpy.data.cameras.new("C"); cam = bpy.data.objects.new("C", cd)
scene.collection.objects.link(cam); scene.camera = cam; cd.lens = 70

for name, loc in {"front": (0.0, -3.8, 1.00), "back34": (2.2, 3.0, 1.20)}.items():
    cam.location = loc
    cam.rotation_euler = (Vector((0, 0, 0.95)) - Vector(loc)).to_track_quat("-Z", "Y").to_euler()
    p = os.path.join(OUT, "acc_%s.png" % name)
    scene.render.filepath = p
    bpy.ops.render.render(write_still=True)
    print("ACC_RENDERED", p, os.path.getsize(p))
print("RENDER_DONE")
