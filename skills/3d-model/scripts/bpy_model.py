#!/usr/bin/env python3
"""bpy 建模通道：用 Python 驱动 Blender 引擎，无需 GUI / 无需 ops。

对应视频里 "OpenCode + Blender MCP" 的同一条链路——AI 通过程序化接口
驱动 Blender。本脚本是那条通道的最小可用实现：建场景、拼装体块、材质、
灯光、相机、保存 .blend、渲染静帧/帧序列。

**关键约束（实测得出，别改回去）**：
以库方式 `import bpy` 时，`bpy.ops.mesh.primitive_*` 这类**建模操作器并未注册**
（会报 "could not be found"）。所以本脚本全部走 `bpy.data` + `bmesh.ops` 纯数据 API，
不依赖任何 UI context。可用渲染引擎只有 `BLENDER_EEVEE`（没有 EEVEE_NEXT）。

环境：/home/user/miniforge3/envs/bpyenv/bin/python3.13

子命令：
  doctor    检查 bpy 可用性
  scene     按 JSON 描述建场景并存 .blend（可选渲一张）
  render    渲静帧或帧序列
  asset     快速生成单个体块（冒烟/拼装用）
"""
from __future__ import annotations

import argparse
import json
import math
import sys
from pathlib import Path

import bpy
import mathutils
# bmesh 由 bpy 加载时注册，必须在 import bpy 之后（顺序反了会 ImportError）
import bmesh

# bpy 作为库加载时会打印些无害告警（音频/gltf 插件），不必干扰用户
ENGINE = "BLENDER_EEVEE"


# ---------------------------------------------------------------- 基础

def reset_scene() -> None:
    bpy.ops.wm.read_factory_settings(use_empty=True)


def _mat(name, color=(0.8, 0.8, 0.8, 1.0), metallic=0.0, rough=0.5):
    m = bpy.data.materials.get(name) or bpy.data.materials.new(name)
    m.use_nodes = True
    bsdf = m.node_tree.nodes.get("Principled BSDF")
    if bsdf:
        bsdf.inputs["Base Color"].default_value = tuple(color)
        bsdf.inputs["Metallic"].default_value = metallic
        bsdf.inputs["Roughness"].default_value = rough
    return m


def add_light(name="Light", loc=(4, -4, 6), energy=800, kind="AREA", size=5.0):
    data = bpy.data.lights.new(name, type=kind)
    data.energy = energy
    if kind == "AREA":
        data.size = size
    obj = bpy.data.objects.new(name, data)
    bpy.context.scene.collection.objects.link(obj)
    obj.location = loc
    # 让灯照向原点
    direction = mathutils.Vector((0, 0, 0)) - obj.location
    obj.rotation_euler = direction.to_track_quat("-Z", "Y").to_euler()
    return obj


def add_camera(name="Camera", loc=(6, -6, 5), look_at=(0, 0, 0)):
    data = bpy.data.cameras.new(name)
    obj = bpy.data.objects.new(name, data)
    bpy.context.scene.collection.objects.link(obj)
    obj.location = loc
    direction = mathutils.Vector(look_at) - obj.location
    obj.rotation_euler = direction.to_track_quat("-Z", "Y").to_euler()
    # 关键：数据 API 不会自动把新相机设为活动相机，不设这行渲染会报 "no camera"
    bpy.context.scene.camera = obj
    return obj


def default_studio(desc: dict) -> None:
    world_color = (desc.get("studio", {}) or {}).get("world", [0.05, 0.05, 0.06, 1.0])
    world = bpy.data.worlds.get("World") or bpy.data.worlds.new("World")
    world.use_nodes = True
    bg = world.node_tree.nodes.get("Background")
    if bg:
        bg.inputs[0].default_value = tuple(world_color)
    bpy.context.scene.world = world

    lights = (desc.get("studio", {}) or {}).get("lights") or [
        {"name": "Key", "loc": [5, -5, 7], "energy": 1000},
        {"name": "Fill", "loc": [-5, -2, 4], "energy": 300},
        {"name": "Rim", "loc": [0, 6, 6], "energy": 500},
    ]
    for l in lights:
        add_light(l.get("name", "Light"), l.get("loc", (4, -4, 6)),
                  l.get("energy", 800), l.get("type", "AREA"), l.get("size", 5.0))
    cam = (desc.get("studio", {}) or {}).get("camera", {}) or {}
    add_camera(cam.get("name", "Camera"), cam.get("loc", (6, -6, 5)),
               cam.get("look_at", (0, 0, 0)))


# ---------------------------------------------------------------- 体块（bmesh 纯数据 API）

def _mesh_from_bmesh(name: str, fill) -> bpy.types.Mesh:
    bm = bmesh.new()
    fill(bm)
    me = bpy.data.meshes.new(name)
    bm.to_mesh(me)
    bm.free()
    return me


def _torus_bm(bm, major=1.0, minor=0.25, seg=32, ring=12):
    """bmesh.ops 没有 create_torus，手写一个。"""
    verts = []
    for i in range(seg):
        a = 2 * math.pi * i / seg
        for j in range(ring):
            b = 2 * math.pi * j / ring
            r = major + minor * math.cos(b)
            verts.append(bm.verts.new((r * math.cos(a), r * math.sin(a), minor * math.sin(b))))
    bm.verts.ensure_lookup_table()
    for i in range(seg):
        for j in range(ring):
            v0 = verts[i * ring + j]
            v1 = verts[((i + 1) % seg) * ring + j]
            v2 = verts[((i + 1) % seg) * ring + (j + 1) % ring]
            v3 = verts[i * ring + (j + 1) % ring]
            bm.faces.new((v0, v1, v2, v3))


def _gear_bm(bm, teeth=12, r=1.0, depth=0.3, tooth=0.25):
    """齿轮轮廓挤出（视频里那类机械件）。"""
    import math
    pts = []
    for i in range(teeth):
        a0 = 2 * math.pi * i / teeth
        a1 = a0 + 2 * math.pi / teeth * 0.25
        a2 = a0 + 2 * math.pi / teeth * 0.5
        a3 = a0 + 2 * math.pi / teeth * 0.75
        for a, rr in ((a0, r), (a1, r + tooth), (a2, r), (a3, r)):
            pts.append((rr * math.cos(a), rr * math.sin(a)))
    vs = [bm.verts.new((x, y, 0)) for x, y in pts]
    f = bm.faces.new(vs)
    bmesh.ops.translate(bm, verts=bm.verts[:], vec=(0, 0, depth / 2))
    bmesh.ops.translate(bm, verts=bm.verts[:], vec=(0, 0, -depth))


PRIMS = {
    "cube":     lambda bm: bmesh.ops.create_cube(bm, size=1),
    "plane":    lambda bm: bmesh.ops.create_grid(bm, x_segments=1, y_segments=1, size=2),
    "sphere":   lambda bm: bmesh.ops.create_uvsphere(bm, u_segments=32, v_segments=16, radius=1),
    "cylinder": lambda bm: bmesh.ops.create_cone(bm, cap_ends=True, cap_tris=False,
                                                 segments=32, radius1=1, radius2=1, depth=2),
    "cone":     lambda bm: bmesh.ops.create_cone(bm, cap_ends=True, cap_tris=True,
                                                 segments=32, radius1=1, radius2=0, depth=2),
    "torus":    lambda bm: _torus_bm(bm),
    "gear":     lambda bm: _gear_bm(bm),
    "ico":      lambda bm: bmesh.ops.create_icosphere(bm, subdivisions=2, radius=1),
}


def add_primitive(spec: dict):
    kind = spec.get("type", "cube")
    if kind not in PRIMS:
        raise SystemExit(f"未知体块: {kind}（可选 {sorted(PRIMS)}）")
    me = _mesh_from_bmesh(kind, PRIMS[kind])
    obj = bpy.data.objects.new(spec.get("name", kind), me)
    bpy.context.scene.collection.objects.link(obj)
    obj.scale = tuple(spec.get("scale", (1, 1, 1)))
    obj.location = tuple(spec.get("location", (0, 0, 0)))
    rot = spec.get("rotation", (0, 0, 0))
    obj.rotation_euler = (math.radians(rot), 0, 0) if isinstance(rot, (int, float)) \
        else tuple(math.radians(a) for a in rot)
    if spec.get("material"):
        me.materials.append(_mat(spec["material"], spec.get("color", [0.8, 0.8, 0.8, 1.0]),
                                 spec.get("metallic", 0.0), spec.get("roughness", 0.5)))
    return obj


def add_extrusion(spec: dict):
    """2D 轮廓挤出成实体（墙体/型材）。"""
    pts = spec.get("outline") or []
    if len(pts) < 3:
        raise SystemExit("extrusion 需要 outline（>=3 个 [x,y]）")
    depth = float(spec.get("depth", 1.0))

    def fill(bm):
        vs = [bm.verts.new((x, y, 0)) for x, y in pts]
        face = bm.faces.new(vs)
        ret = bmesh.ops.extrude_face_region(bm, geom=[face])
        verts = [e for e in ret["geom"] if isinstance(e, bmesh.types.BMVert)]
        bmesh.ops.translate(bm, verts=verts, vec=(0, 0, depth))
        bmesh.ops.translate(bm, verts=bm.verts[:], vec=(0, 0, -depth / 2))
        bmesh.ops.recalc_face_normals(bm, faces=bm.faces)

    me = _mesh_from_bmesh(spec.get("name", "Extrusion"), fill)
    obj = bpy.data.objects.new(spec.get("name", "Extrusion"), me)
    bpy.context.scene.collection.objects.link(obj)
    obj.location = tuple(spec.get("location", (0, 0, 0)))
    if spec.get("material"):
        me.materials.append(_mat(spec["material"], spec.get("color", [0.8, 0.8, 0.8, 1.0])))
    return obj


def add_array(spec: dict):
    """阵列基础物体（栏杆/栅格）。用真实复制，不依赖 modifier ops。"""
    base = add_primitive(spec.get("base", {"type": "cube"}))
    count = int(spec.get("count", 4))
    step = mathutils.Vector(spec.get("step", (1, 0, 0)))
    made = [base]
    for i in range(1, count):
        dup = base.copy()
        dup.data = base.data.copy()
        dup.location = base.location + step * i
        bpy.context.scene.collection.objects.link(dup)
        made.append(dup)
    return made


def add_object(spec: dict):
    kind = spec.get("type", "cube")
    if kind == "extrusion":
        return add_extrusion(spec)
    if kind == "array":
        return add_array(spec)
    return add_primitive(spec)


# ---------------------------------------------------------------- 场景 / 渲染

def build_scene(desc: dict) -> None:
    reset_scene()
    for spec in desc.get("objects", []) or []:
        add_object(spec)
    default_studio(desc)
    r = desc.get("render", {}) or {}
    sc = bpy.context.scene
    # 引擎名做白名单校验：调用方传了本机不存在的引擎（如 EEVEE_NEXT）时自动兜底，
    # 不要让一个拼写差异直接炸掉整条渲染
    valid = {i.identifier for i in
             bpy.types.RenderSettings.bl_rna.properties["engine"].enum_items}
    engine = r.get("engine", ENGINE)
    if engine not in valid:
        print(f"  [warn] 引擎 {engine} 不可用，落到 {ENGINE}")
        engine = ENGINE
    sc.render.engine = engine
    sc.render.resolution_x = int(r.get("width", 1280))
    sc.render.resolution_y = int(r.get("height", 720))
    sc.render.resolution_percentage = 100
    sc.render.film_transparent = bool(r.get("transparent", False))
    sc.render.fps = int(r.get("fps", 30))
    sc.frame_start = int(r.get("frame_start", 1))
    sc.frame_end = int(r.get("frame_end", 1))
    apply_spin(desc)


def apply_spin(desc: dict) -> None:
    """给名为 Subject 的物体（或唯一物体）加旋转关键帧，做 turntable 动画。"""
    spin = desc.get("spin")
    if not spin:
        return
    objs = [o for o in bpy.context.scene.objects if o.type == "MESH"]
    if not objs:
        return
    target = bpy.context.scene.objects.get("Subject") or objs[0]
    axis = str(spin.get("axis", "Z")).upper()
    degrees = float(spin.get("degrees", 360))
    idx = {"X": 0, "Y": 1, "Z": 2}.get(axis, 2)
    sc = bpy.context.scene
    sc.frame_start = int(sc.frame_start)
    target.rotation_mode = "XYZ"
    target.rotation_euler[idx] = 0.0
    target.keyframe_insert("rotation_euler", index=idx, frame=sc.frame_start)
    target.rotation_euler[idx] = math.radians(degrees)
    end = max(sc.frame_end, sc.frame_start + 1)
    target.keyframe_insert("rotation_euler", index=idx, frame=end)


def save_blend(path: str) -> None:
    Path(path).parent.mkdir(parents=True, exist_ok=True)
    bpy.ops.wm.save_as_mainfile(filepath=str(Path(path).expanduser()))


def render_still(out: str, frame: int | None = None) -> None:
    sc = bpy.context.scene
    if frame is not None:
        sc.frame_set(int(frame))
    Path(out).parent.mkdir(parents=True, exist_ok=True)
    sc.render.filepath = str(Path(out).expanduser())
    sc.render.image_settings.file_format = "PNG"
    bpy.ops.render.render(write_still=True)


def render_range(prefix: str) -> None:
    sc = bpy.context.scene
    Path(prefix).parent.mkdir(parents=True, exist_ok=True)
    sc.render.filepath = str(Path(prefix).expanduser())
    sc.render.image_settings.file_format = "PNG"
    bpy.ops.render.render(animation=True)


# ---------------------------------------------------------------- CLI

def main() -> int:
    ap = argparse.ArgumentParser(description="bpy 建模通道（无 GUI，纯数据 API）")
    sub = ap.add_subparsers(dest="cmd", required=True)

    sub.add_parser("doctor", help="检查 bpy 可用性").set_defaults(func=lambda a: (
        print(f"  bpy 版本 : {bpy.app.version_string}"), print(f"  引擎     : {ENGINE}"),
        print("  可用性   : OK（无 GUI / 无 ops 上下文）"), 0)[-1])

    s = sub.add_parser("scene", help="按 JSON 建场景并存 .blend")
    s.add_argument("scene_json"); s.add_argument("--blend", required=True)
    s.add_argument("--render", help="额外渲一张 PNG")
    s.set_defaults(func=cmd_scene)

    r = sub.add_parser("render", help="渲图/帧序列")
    r.add_argument("blend"); r.add_argument("--out", required=True)
    r.add_argument("--animation", action="store_true")
    r.add_argument("--frame", type=int)
    r.set_defaults(func=cmd_render)

    a = sub.add_parser("asset", help="快速生成单体块")
    a.add_argument("type", choices=sorted(PRIMS) + ["extrusion"])
    a.add_argument("--name", default="Primitive")
    a.add_argument("--blend"); a.add_argument("--render")
    a.set_defaults(func=cmd_asset)

    args = ap.parse_args()
    return args.func(args)


def cmd_scene(args) -> int:
    desc = json.loads(Path(args.scene_json).read_text(encoding="utf-8"))
    build_scene(desc)
    save_blend(args.blend)
    print(f"  场景已存: {args.blend}")
    if args.render:
        render_still(args.render)
        print(f"  渲染已存: {args.render}")
    return 0


def cmd_render(args) -> int:
    bpy.ops.wm.open_mainfile(filepath=str(Path(args.blend).expanduser()))
    render_range(args.out) if args.animation else render_still(args.out, args.frame)
    print(f"  渲染完成: {args.out}")
    return 0


def cmd_asset(args) -> int:
    reset_scene()
    add_primitive({"type": args.type, "name": args.name,
                   "material": "m", "color": [0.9, 0.5, 0.2, 1.0]})
    default_studio({})
    sc = bpy.context.scene
    sc.render.engine = ENGINE
    sc.render.resolution_x, sc.render.resolution_y = 800, 600
    if args.blend:
        save_blend(args.blend)
    if args.render:
        render_still(args.render)
    print(f"  {args.type} 创建成功" + (f"，存 {args.blend}" if args.blend else "")
          + (f"，渲 {args.render}" if args.render else ""))
    return 0


if __name__ == "__main__":
    sys.exit(main())
