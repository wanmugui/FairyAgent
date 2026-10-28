"""实战示例：三种程序化结构。照着改参数就能用。

    blender -b -noaudio --python examples/three_structures.py

每个场景都打印 realize() 的顶点/面数——数字不对就别看渲染。
"""

import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "scripts"))

import bpy
import gnkit


def brick_wall():
    """一整面砖墙：原型立方体 + 网格实例化 + realize。"""
    t = gnkit.tree("BrickWall")
    o = gnkit.object("BrickWall", t)
    brick = gnkit.material("Brick", color=(0.55, 0.30, 0.25, 1.0), roughness=0.85)

    proto = t.geo().prototype("GeometryNodeMeshCube", Size=(0.24, 0.11, 0.06))
    pts = t.geo().grid_points(size_x=4.0, size_y=0.5, count_x=16, count_y=4)

    t.finish(
        pts.instance_on_points(proto)
        .realize()
        .set_material(brick)
        .transform(translate=(-3.0, 0, 0.4))
    )
    return "BrickWall", t.realize(o)


def fence():
    """栏杆：立柱 + 横杆两种原型，各自阵列后 join。"""
    t = gnkit.tree("Fence")
    o = gnkit.object("Fence", t)
    wood = gnkit.material("Wood", color=(0.42, 0.30, 0.18, 1.0), roughness=0.7)

    post = t.geo().prototype("GeometryNodeMeshCube", Size=(0.08, 0.08, 1.0))
    rail = t.geo().prototype("GeometryNodeMeshCube", Size=(0.06, 0.04, 0.06))

    line = t.geo().grid_points(size_x=4.0, size_y=0.0, count_x=9, count_y=2)
    posts = line.instance_on_points(post).realize()
    # 横杆靠实例旋转 90 度排成竖直方向
    rail_line = t.geo().grid_points(size_x=4.0, size_y=0.0, count_x=2, count_y=2)
    rails = rail_line.instance_on_points(rail, rotation=(0, 90, 0)).realize()
    rails.transform(translate=(0, 0, 0.35))
    rails.transform(translate=(0, 0, 0.75))

    t.finish(posts.join(rails).transform(translate=(2.5, 0, 0)).set_material(wood))
    return "Fence", t.realize(o)


def meadow():
    """随机草地：噪声挑高的草簇 + 球形灌木散布。"""
    t = gnkit.tree("Meadow")
    o = gnkit.object("Meadow", t)
    green = gnkit.material("Grass", color=(0.25, 0.45, 0.18, 1.0), roughness=0.9)

    blade = t.geo().prototype("GeometryNodeMeshCone", Radius=0.02, Radius2=0.0, Depth=0.35)
    # 草地缩到 2x2 并移到原点：比砖墙/栏杆小一圈，才不会把它们淹掉
    field = t.geo().grid_points(size_x=2.0, size_y=2.0, count_x=16, count_y=16)

    # 高度由噪声决定：低的地方不长草，用 delete 按 selection 剔掉
    g = field.instance_on_points(blade).realize()
    noise = g.noise(scale=6.0, detail=4.0)
    keep = g.compare('GREATER_THAN', noise, 0.55)
    t.finish(g.delete_faces(selection=keep).set_material(green))
    return "Meadow", t.realize(o)


def main():
    print("== procedural-3d examples ==")
    for fn in (brick_wall, fence, meadow):
        try:
            name, counts = fn()
            status = "OK " if counts.get("verts", 0) > 0 else "EMPTY"
            print(f"  {status}  {name:12s} verts={counts.get('verts')} faces={counts.get('faces')}")
        except Exception as exc:
            print(f"  ERR  {fn.__name__}: {type(exc).__name__}: {exc}")
            raise

    print("\n场景已建好，可直接渲染：")
    print("  blender -b --python-expr \"import bpy; bpy.ops.wm.save_as_mainfile(filepath='/tmp/proc_examples.blend')\"")


if __name__ == "__main__":
    main()
