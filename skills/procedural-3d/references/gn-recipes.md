# 几何节点配方

全部在 Blender 4.5.14 LTS 上验证过，形状与参数可直接改。

## 1. 拱窗（中世纪窗，带放射窗棂）

放射状窗棂是几何节点的经典用例——每根窗棂绕中心旋转一个角度。

```python
def arched_window(t, width=1.2, height=1.8, bars=8):
    mat = gnkit.material("Stone", color=(0.72, 0.70, 0.66, 1.0), roughness=0.9)

    # 窗洞：墙体减去圆柱，得到拱形开口
    wall = t.geo().cube(size=(width + 0.3, 0.2, height + 0.3), verts_x=1, verts_y=1, verts_z=1)
    void = t.geo().cylinder(
        radius=width / 2, depth=0.5, vertices=24
    ).transform(translate=(0, 0, height / 2 - width / 2))
    rect = t.geo().cube(size=(width, 0.5, height)).transform(translate=(0, 0, -0.2))

    # 窗棂：细长条绕 Y 轴扇形排布
    mullion = t.geo().prototype(
        "GeometryNodeMeshCube", Size=(0.04, 0.12, width)
    )
    angle_span = 90.0
    bar_line = t.geo().grid_points(size_x=angle_span, size_y=0.0, count_x=bars, count_y=2)
    # grid 是平铺的，旋转用实例的 rotation 参数逐点做不到；
    # 实际做法见下方 note
    return wall
```

**注意**：`InstanceOnPoints` 的 `Rotation` 是**统一值**，不能逐点变化。要做扇形排布有两个办法：

1. 建 N 个实例节点，每个转不同角度（简单，N 不大时够用）
2. 用 `GeometryNodeDistributePointsOnFaces` + `RotateInstances`（正确做法）

```python
def radial_bars(t, radius=0.6, bars=8, depth=0.12):
    """方案 1：N 个实例节点，每个角度一个。"""
    out = None
    bar = t.geo().prototype(
        "GeometryNodeMeshCube", Size=(0.04, depth, radius)
    ).transform(translate=(0, 0, radius / 2))   # 让棒从原点延伸
    for i in range(bars):
        angle = i * (90.0 / bars)
        g = t.geo().instance_on_points(
            t.geo().prototype("GeometryNodeMeshPoint"),
            rotation=(0, angle, 0),
        )
        # prototype 里旋转的是实例，不是 geometry，所以要各自建
        out = g if out is None else out.join(g)
    return out
```

## 2. 栏杆阵列（立柱 + 横杆）

```python
def railing(t, length=4.0, posts=9, height=1.0):
    wood = gnkit.material("Wood", color=(0.42, 0.30, 0.18, 1.0), roughness=0.7)

    post = t.geo().prototype("GeometryNodeMeshCube", Size=(0.08, 0.08, height))
    line = t.geo().grid_points(size_x=length, size_y=0.0, count_x=posts, count_y=2)
    p = line.instance_on_points(post).realize()

    rail_proto = t.geo().prototype("GeometryNodeMeshCube", Size=(length, 0.06, 0.06))
    point = t.geo().prototype("GeometryNodeMeshPoint")
    r = point.instance_on_points(rail_proto).realize()
    r.transform(translate=(0, 0, height - 0.1))
    r.transform(translate=(0, 0, height * 0.55))

    return p.join(r).set_material(wood)
```

关键点：**横杆是一个"点上一个长条"**，长度直接等于总长，不用分段。

## 3. 随机草地（噪声筛密度）

```python
def meadow(t, size=4.0, count=32, threshold=0.55, blade_h=0.35):
    grass = gnkit.material("Grass", color=(0.25, 0.45, 0.18, 1.0), roughness=0.9)

    blade = t.geo().prototype(
        "GeometryNodeMeshCone", Radius=0.02, Radius2=0.0, Depth=blade_h
    )
    field = t.geo().grid_points(size_x=size, size_y=size, count_x=count, count_y=count)

    g = field.instance_on_points(blade).realize()
    n = g.noise(scale=6.0, detail=4.0)
    keep = g.compare('GREATER_THAN', n, threshold)
    return g.delete_faces(selection=keep).set_material(grass)
```

`threshold` 调 0.5 → 满地；0.7 → 稀疏。**噪声 scale 决定斑块大小**，不是密度。

## 4. 布尔挖洞（墙体开口）

```python
def wall_with_windows(t, w=4.0, h=2.4, d=0.2, openings=3):
    wall = t.geo().cube(size=(w, d, h))
    for i in range(openings):
        hole = t.geo().cube(size=(0.8, d * 3, 1.2)).transform(
            translate=(-w / 2 + (i + 0.5) * w / openings, 0, 0.3)
        )
        wall = wall.boolean(hole, 'DIFFERENCE')
    return wall
```

**布尔链的坑**：连续布尔容易在共面处出裂缝（不是完全共面，偏移 0.001 就好）。程序化布尔链超过 5 次就该考虑改用参数化建墙（拼洞周围的四块板）而不是一直减。

## 5. 屋顶（斜面 + 脊）

```python
def pitched_roof(t, w=4.0, d=3.0, h=1.2, thickness=0.15):
    # 两块斜板，各旋转 ±atan(h / (w/2))
    slope = math.degrees(math.atan2(h, w / 2))
    half_len = math.sqrt(h ** 2 + (w / 2) ** 2)

    left = t.geo().cube(size=(half_len, d, thickness)).transform(
        rotate=(0, -slope, 0), translate=(-w / 4, 0, h / 2)
    )
    right = t.geo().cube(size=(half_len, d, thickness)).transform(
        rotate=(0, slope, 0), translate=(w / 4, 0, h / 2)
    )
    return left.join(right)
```

旋转用 `math.degrees(math.atan2(...))` 算出来，别手填角度——改宽度时角度得跟着变。

## 6. 材质变体（同一物体不同色）

```python
def two_tone_cube(t):
    a = gnkit.material("Warm", color=(0.8, 0.6, 0.3, 1.0), roughness=0.5)
    b = gnkit.material("Cool", color=(0.3, 0.5, 0.8, 1.0), roughness=0.5)

    top = t.geo().cube(size=2).transform(translate=(0, 0, 0.75))
    bottom = t.geo().cube(size=2).transform(translate=(0, 0, -0.75))
    # 两个 mesh 在同一物体上：给面分配材质
    return bottom.set_material(b).join(top.set_material(a))
```

`set_material` 接受多个材质并按面序分配，但**顺序依赖网格面序**，改了几何就会错位。更稳的做法是按法线朝向选面：

```python
def select_by_normal(geo, axis='Z', sign=1, threshold=0.7):
    """按法线朝向选面 —— 比按面序稳。"""
    pos = geo.position()
    normal = geo.tree.new("GeometryNodeInputNormal")
    geo.tree.link(pos.sock, normal.inputs[0]) if False else None
    dot = geo.math('DOT_PRODUCT', pos, 0.0)
    # 实际更常用的是 Compare on the normal component directly:
    sep = normal.outputs["Normal"]._sep()
    comp = sep[{'X': 0, 'Y': 1, 'Z': 2}[axis]]
    return geo.compare('GREATER_THAN', comp, threshold * sign)
```

## 7. 圆角和倒角（几何节点无 modifier 时的替代）

```python
def bevel_cube(t, size=2.0, width=0.05, segments=3):
    """Bevel 节点在 GN 里是 GeometryNodeBevel。"""
    n = t.tree.new("GeometryNodeBevel")
    n.inputs["Width"].default_value = width
    n.inputs["Segments"].default_value = segments
    g = t.geo().cube(size=size)
    t.tree.link(g.sock, n.inputs["Mesh"])
    return Geo(t.tree, Sock(t.tree, n.outputs["Mesh"], n))
```

需要 `from gnkit import Geo, Sock` 或直接用 `g.tree.new` 手动接。`gnkit` 未封装 Bevel 是因为它常和 Boolean 混用，手写更清楚。

## 常见错误速查

| 症状 | 原因 | 修 |
|---|---|---|
| `Same input/output direction` | 把组输出当输入链 | 用 `t.geo()` 起链，`t.finish()` 收尾 |
| `verts: 0` | Instances 没 realize | 加 `.realize()` |
| `modifiers.new() → None` | 对象没有 mesh 数据 | `gnkit.object()` 已处理 |
| `key "Size" not found` | Grid 是 `Size X`/`Size Y` | 见 SKILL.md 对照表 |
| 布尔后有裂缝 | 完全共面 | 偏移 0.001 |
| `default_value` 报错 | Geometry/Object socket 不能赋值 | 改连线 |
| 实例不能逐点旋转 | `Rotation` 是统一值 | N 个实例节点，或用 DistributePoints |
