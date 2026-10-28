"""gnkit smoke test -- run inside Blender, asserts the API surface works.

    blender -b -noaudio --python gnkit_smoke.py

Exits non-zero (Blender quit code) if any assertion fails, so CI or the agent
can trust the exit code instead of eyeballing stdout.
"""

import sys
import os

# Allow running as: blender -b --python gnkit_smoke.py
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import bpy
import gnkit

PASS = []
FAIL = []


def check(name, fn):
    try:
        detail = fn()
        PASS.append(name)
        print(f"  PASS  {name}" + (f"  -> {detail}" if detail else ""))
    except Exception as exc:
        FAIL.append((name, exc))
        print(f"  FAIL  {name}: {type(exc).__name__}: {exc}")


# 1. single primitive
def t_cube():
    t = gnkit.tree("T_cube")
    o = gnkit.object("Cube", t)
    g = t.geo().cube(size=2.0).transform(translate=(0, 0, 1))
    t.finish(g)
    return t.realize(o)


# 2. bool: subtract a smaller cube -> a hollow-ish shape
def t_boolean():
    t = gnkit.tree("T_bool")
    o = gnkit.object("Bool", t)
    outer = t.geo().cube(size=2.0)
    inner = t.geo().cube(size=1.2).transform(translate=(0, 0, 0.4))
    t.finish(outer.boolean(inner, 'DIFFERENCE'))
    return t.realize(o)


# 3. transform math actually moves geometry
def t_transform_math():
    t = gnkit.tree("T_xform")
    o = gnkit.object("Moved", t)
    g = t.geo().cube(size=1.0)
    # scale by 3 on X only, via socket arithmetic
    sep = g.position()._sep()
    g2 = t.geo().cube(size=1.0)
    g2.transform(scale=(3.0, 1.0, 1.0))
    t.finish(g2)
    return t.realize(o)


# 4. materials wire up
def t_material():
    t = gnkit.tree("T_mat")
    o = gnkit.object("MatObj", t)
    m = gnkit.material("Red", color=(0.8, 0.1, 0.1, 1.0), roughness=0.3)
    t.finish(t.geo().cube(size=1.5).set_material(m))
    return t.realize(o)


# 5. instance-on-points: the arraying path
def t_instance():
    t = gnkit.tree("T_inst")
    o = gnkit.object("InstObj", t)
    proto = t.geo().prototype("GeometryNodeMeshCube", Size=(0.4, 0.4, 0.4))
    pts = t.geo().grid_points(size_x=4.0, size_y=4.0, count_x=3, count_y=3)
    inst = pts.instance_on_points(proto)
    counts = t.realize(o)          # before realize: 0 verts is expected
    t.finish(inst.realize())       # instances -> real geometry
    after = t.realize(o)
    assert after.get("verts", 0) > 0, f"realize() produced no geometry: {after}"
    return f"before_realize={counts['verts']} after_realize={after['verts']}"


# 6. camera aim helper produces sane rotation
def t_camera():
    cam = gnkit.camera("Cam", location=(0, -6, 3), look_at=(0, 0, 1), lens=50)
    return f"loc={tuple(round(v,2) for v in cam.location)} lens={cam.data.lens}"


# 7. light
def t_light():
    l = gnkit.light("Sun", kind='SUN', energy=3.0, angle=2.0)
    return f"energy={l.data.energy} angle={round(l.data.angle,4)}"


# 8. noise / random / compare field chain (used for organic variation)
def t_fields():
    t = gnkit.tree("T_field")
    o = gnkit.object("FieldObj", t)
    g = t.geo().sphere(radius=1.0)
    n = g.noise(scale=4.0, detail=3.0)
    gt = g.compare('GREATER_THAN', n, 0.6)
    t.finish(g.delete_faces(selection=gt))
    return t.realize(o)


# 9. cone 的半径必须真的生效（回归：5.1 把 socket 改名后参数被静默丢弃，
#    cone(radius1=3) 和 cone(radius1=1) 出来一样大，而冒烟测试全程绿灯。）
def t_cone_radius():
    import numpy as np
    t = gnkit.tree("T_cone")
    o = gnkit.object("ConeObj", t)
    g = t.geo().cone(radius1=3.0, radius2=0.0, depth=2.0, vertices=12)
    t.finish(g)
    t.realize(o)
    dg = bpy.context.evaluated_depsgraph_get()
    me = o.evaluated_get(dg).to_mesh()
    co = np.array([v.co[:] for v in me.vertices])
    o.evaluated_get(dg).to_mesh_clear()
    width = float(co[:, 0].max() - co[:, 0].min())
    assert abs(width - 6.0) < 1e-3, f"cone 直径应为 6.0（半径 3），实测 {width:.4f}"
    return f"radius=3 -> width={width:.3f}"


print("== gnkit smoke ==")
for name, fn in [
    ("cube primitive", t_cube),
    ("boolean difference", t_boolean),
    ("transform scale", t_transform_math),
    ("material assign", t_material),
    ("instance on points", t_instance),
    ("camera aim", t_camera),
    ("light", t_light),
    ("noise field + delete", t_fields),
    ("cone radius applied", t_cone_radius),
]:
    check(name, fn)

print(f"\n== summary: {len(PASS)} passed, {len(FAIL)} failed ==")
for name, exc in FAIL:
    print(f"  FAILED: {name}: {exc}")

if FAIL:
    sys.exit(1)
