---
name: procedural-3d
description: 用 Blender 几何节点（Geometry Nodes）+ Python 做程序化场景建模。所有几何由 GN 生成、布局尺寸写在 JSON 里，可命令行一键重建与渲染。触发：「用几何节点建模」「程序化搭场景」「写个 GN 场景」「按 JSON 建场景」「Blender 几何节点」「用 GN 做材质」「程序化摆家具/建筑/道具阵列」。
metadata:
  short-description: "Blender 几何节点程序化建模"
  tags:
    - 3d
    - blender
    - geometry-nodes
    - procedural
    - modeling
  triggers:
    - "用几何节点建模"
    - "程序化搭场景"
    - "写个 GN 场景"
    - "按 JSON 建场景"
    - "Blender 几何节点"
    - "程序化摆家具"
    - "程序化建场景"
    - "GN 材质"
  priority: 73
---

# 程序化 3D 建模（Blender 几何节点）

## 什么时候用这个

**程序化 = 几何由规则生成，不是手摆。** 选型判断：

| 场景 | 用这个 | 理由 |
|---|---|---|
| 一面砖墙 / 一排栏杆 / 森林 / 路灯阵列 | ✅ | 重复结构，规则生成最省事 |
| 整间屋、家具陈设、场景布局 | ✅ | 数据驱动，改 JSON 就行 |
| 材质区分、随机变化（草高、锈迹） | ✅ | 噪声/随机字段天然擅长 |
| 一个孤立零件（支架、卡扣） | ❌ 用 `3d-model` | OpenSCAD 参数化更直接 |
| 高精度角色雕刻 | ❌ 用 `sculpting-character-pipeline` | GN 做不了拓扑和雕刻 |
| 从图还原已有物体 | ❌ | 见下面「单图还原是伪命题」 |

**核心优势**：改一个 JSON 参数就能重建整个场景，不用重开 Blender、不用手点。

## 立刻验证环境

```bash
BL=/path/to/opt/blender-4.5.14-linux-x64/blender

# 冒烟测试（8 项，覆盖 primitive/boolean/instance/材质/噪声/相机/灯）
$BL -b -noaudio --python skills/procedural-3d/scripts/gnkit_smoke.py 2>&1 | tail -12
```

**预期 `9 passed, 0 failed`。** 有失败先修环境，别往下写场景。
第 9 项 `cone radius applied` 是锥体半径回归——曾经它静默失效而前 8 项全绿。

已验证的 Blender **4.5.14 LTS** 事实（别自己踩）：

| 节点 | 真实签名 |
|---|---|
| `GeometryNodeTransform` | **单节点三输入** Translation/Rotation/Scale，**没有** `transform_space` 属性 |
| `GeometryNodeMeshGrid` | `Size X` / `Size Y` **分开两个 socket**，不是 `Size` 向量 |
| `GeometryNodeMeshCone` | 5.x 是 **`Radius Bottom` / `Radius Top`**，4.x 的 `Radius` / `Radius 2` 已改名。写老名字会被静默忽略，半径恒为 1 |
| `GeometryNodeMeshIcoSphere` | `Radius` / `Subdivisions`；`subdivisions=1` = 20 面，低模石头用 |
| `GeometryNodeMeshUVSphere` | `Segments` / `Rings`（**不是** `Ring Count`） |
| `FunctionNodeCompare` | 每个 data_type 各有一对 A/B，**按索引选**不是按名字 |
| `GeometryNodeInstanceOnPoints` | `Instance` 是纯 Geometry socket，**只能连线**，无 `default_value` |
| `objects.new(name, None)` | `modifiers.new('NODES')` 返回 **None**，必须先给 mesh 数据 |
| `JoinGeometry` | 会**压平分件材质**（多部件全变最后一个材质），不报错。要多材质就分件求值后在 Python 侧拼 |
| `_prim` 遇到未知 socket | 现在直接 `KeyError`，不再静默跳过 |
| `NodeGroupOutput` | **不能当输入源**，会报 Same input/output direction |

## 最小可运行示例

```python
import sys, bpy
sys.path.insert(0, "skills/procedural-3d/scripts")
import gnkit

t = gnkit.tree("Wall")           # 建几何节点树
o = gnkit.object("Wall", t)      # 挂到对象上

brick = gnkit.material("Brick", color=(0.55, 0.3, 0.25, 1.0), roughness=0.85)
proto = t.geo().prototype("GeometryNodeMeshCube", Size=(0.24, 0.1, 0.06))
pts   = t.geo().grid_points(size_x=4.0, size_y=0.5, count_x=16, count_y=2)

t.finish(
    pts.instance_on_points(proto)      # 32 块砖
      .realize()                        # 实例 → 真几何（关键，见下）
      .set_material(brick)
)
t.realize(o, save_as="/tmp/wall.blend")
```

**两个最容易踩的坑**：

1. **`t.finish(g)` 必须显式调用** —— 链式表达式只建节点，不会自动接输出
2. **`.realize()` 之后才有顶点** —— Instances 输出 `to_mesh()` 拿到的是 `verts: 0`，这不是 bug 是 Blender 的模型如此

## 核心 API

### 建树 / 挂对象

```python
t = gnkit.tree(name)              # 新建 GN 树
o = gnkit.object(name, t, location=(0,0,0))
t.geo()                           # 链式起点（MeshLine 作源）
t.finish(geo)                     # 接到组输出
t.realize(o, save_as=path)        # 强制求值 + 可选存盘
```

`t.realize()` 返回 `{"verts": N, "faces": M}` —— **这是你唯一的验收凭据**。返回 0 就说明链断了或忘了 realize。

### 几何动词（全部可链）

```python
g.cube(size=2)  g.cylinder(radius=1, depth=3, vertices=32)
g.cone(radius1=1, radius2=0, depth=2)  g.sphere(radius=1, segments=48, rings=24)
g.grid(size_x=10, size_y=10, verts_x=20, verts_y=20)  g.circle(radius=5)

g.transform(translate=(0,0,1), rotate=(0,0,45), scale=(2,2,2))  # 角度制
a.boolean(b, 'DIFFERENCE' | 'UNION' | 'INTERSECT')
a.join(b)
g.extrude(individual=False, offset=(0,0,1))
g.set_material(mat1, mat2)          # 也有别名 .material(...)
g.shade_smooth();  g.subdivide(level=2);  g.realize()

pts.instance_on_points(proto, rotation=(0,0,30), scale=(1,1,1))
proto = t.geo().prototype("GeometryNodeMeshCube", Size=(1,1,1))  # 未连线，可被实例化
```

### 场与噪声（做有机变化的关键）

```python
n = g.noise(scale=4.0, detail=3.0)        # → Fac socket
gt = g.compare('GREATER_THAN', n, 0.6)    # → 布尔选择
g.delete_faces(selection=gt)              # 按噪声删面
i = g.index()                              # 元素索引
r = g.random()                             # 随机向量
v = g.value(2.0)                           # 常量
c = g.combine(x, y, z)                     # 合成向量
m = g.math('MULTIPLY', a, 2.0)             # 标量运算
```

**`compare` 必须传 `data_type`**（默认 FLOAT），因为 A/B socket 按类型分组重名。

### 材质 / 灯 / 相机

```python
mat = gnkit.material("Red", color=(0.8,0.1,0.1,1.0), metallic=0.0, roughness=0.4,
                     emission=(1,0.5,0), emission_strength=2.0)   # 颜色是 RGBA 四元
sun = gnkit.light("Sun", kind='SUN', energy=3.0, angle=2.0, location=(0,0,10))
cam = gnkit.camera("Cam", location=(0,-6,3), look_at=(0,0,1), lens=50)
```

`gnkit.camera()` 自动算朝向，不用手搓欧拉角。

## 数据驱动：JSON 存参数

**这是本 skill 最有价值的部分** —— 参考实现 ProceduralParadise 的做法：引擎写 py，场景参数写 JSON，换场景不改代码。

```json
{
  "materials": { "brick": {"color": [0.55, 0.3, 0.25, 1.0], "roughness": 0.85} },
  "bricks": {"count_x": 16, "count_y": 2, "size": [0.24, 0.1, 0.06]},
  "sun": {"energy": 3.0, "angle": 2.0},
  "camera": {"location": [0, -6, 3], "look_at": [0, 0, 1], "lens": 50}
}
```

**写 JSON 时把"为什么是这个数"记在 `notes` 里**，这是 ProceduralParadise 最好的习惯：

> "Stair: 14 treads (0.12308 m risers) solved from the painted nosings (16 px rms)"
> "Sun: 55 deg high from azimuth 31 deg, so its shafts run down and to the left"

这样参数可追溯、可校正，换个人接手也知道每个数字的来历。详见 `references/data-driven.md`。

## 单图还原是伪命题（重要）

参考实现作者自己的结论：

> "不过 ai 画的图一般也不是 3D 结构正确的，所以换个角度就出问题了"
> "轮廓在设定图上逐个描到亚像素，厚度取自端视图"

**单张图没有深度信息，3D 结构不可推断。** 强行做就会在换角度时穿帮。

真要还原一张图，顺序是：
1. **能拿到多角度素材**（游戏内转圈截图 / 设定图的侧视图+端视图）→ 才有意义
2. 只有单图 → 明确告诉用户"结构靠猜"，别假装精准
3. 已知尺寸的物件 → 直接写进 JSON（比让模型猜靠谱得多）

## 验收闭环（别跳）

```
1. 写场景脚本 → $BL -b -noaudio --python your_scene.py
2. 看 realize() 返回的 verts/faces —— 0 就是断了
3. 渲染出来自己看（image_vqa 或直接看图）
4. 对不上就改 JSON 参数，重跑
```

**只看数字不看图是不够的。** 顶点数量对不代表形状对。

**本 skill 自己的例子就踩过这个坑**（值得记住）：

`examples/three_structures.py` 里砖墙 512 顶点、草地 18656 顶点，数字全对。但渲染出来：

- 草地网格 4×4 米、砖墙只有 0.5 米深 → **草地直接把砖墙埋了**
- 相机 `look_at=(0,0,0.6)` 对着草地中心，砖墙在画面边缘只剩一角
- 结果：图里三样东西挤在一起，谁都看不清

**教训：几何数量正确 ≠ 构图正确。** 多个场景放在同一文件里时尤其容易互相遮挡——要么分场景渲染，要么先摆开再摆相机。

## 组合场景的排版规则

多结构同场时，**先定尺度比例再摆相机**：

| 常见错误 | 后果 |
|---|---|
| 地面网格尺寸 >> 结构尺寸 | 结构被地面淹没 |
| 多个物体原点都在 (0,0,0) | 全部重叠成一坨 |
| `look_at` 指向地面而非结构 | 结构退到画面边缘 |
| 结构间距 < 自身宽度 | 穿插 |

**做法**：每个结构 `translate` 到自己的一角（如 x=-4 / 0 / +4），地面缩到刚好覆盖，相机 `look_at` 取三者的几何中心。

## 做游戏场景 / 地景（HD-2D、HD-3D）

**这一节是从零补上的**，原先本 skill 224 行里没有场景内容，只有一句「场景布局 ✅」。
现在有一条可运行的完整配方：**元素清单 → 分层 → 自动取景 → 渲图验收**。

```bash
/path/to/miniforge3/envs/bpyenv/bin/python3.13 skills/procedural-3d/examples/hd2d_diorama.py
```

产出三张图（主图 / 200px 缩略图 / 关雾对照图）+ 24 个元素的贴地与碰撞盒自检表。
**一次性可复现，不是笔记。** 方法论与踩坑细节见 `references/hd2d-recipes.md`。

### 整条链（层 -1 ~ 层 6）

上面那条只是入口。完整的场景流水线已经跑通七层——
场景拼装 → 大气光照 → 地形材质 → 资产散布 → 游戏素材交付，
**一次跑完全部 11 条用例**：

```bash
/path/to/miniforge3/envs/bpyenv/bin/python3.13 skills/procedural-3d/examples/run_all.py
```

各层配方、判据、以及每层最值钱的那条踩坑经验汇总在 **`references/scene-recipes.md`**。

⚠️ **`inspect_delivery.py` 必须换解释器跑**（`/path/to/miniforge3/envs/fairy/bin/python3`，
**唯一装了 trimesh 的解释器**——`/usr/bin/python3` 和 bpyenv 都没有）：它是导出方之外的
**独立检查**，用同一个解释器自检等于自己判自己卷面及格。

四条核心做法（每条都能在 `examples/hd2d_diorama.py` 里找到对应函数）：

1. **元素清单驱动**：JSON 的 `elements` 一条 = 一个 GN 树 = 一个对象。加元素只改 JSON。
2. **锚点固定在底面中心**，不贴地就 `SystemExit(1)` —— 教程要求「不应该有任何悬空的地方」，
   这条从口号变成断言（`check_anchor_and_collider`）。
3. **深度分层 + 空气透视**：`depth_layers` 每层一个 y 与一个往天光混色比例（`mix_to_air`），
   远景是被空气吃掉的，不是后期滤镜。
4. **自动取景**（`frame_camera`）：锁死视角方向，只沿视线推拉 + 画面内摆正，
   判据是 `world_to_camera_view` 的 NDC 包围盒。手写机位必裁掉远景。

### 场景类任务的自检点（与渲图无关的那部分）

- 集合要分类：`Terrain` / `Elements_*` / `Atmosphere` / `Lights` / `Camera`
- 压边体块宽度**不得超过场景横向跨度**，否则取景被它绑架、相机越推越远
- 前景压边件标 `fit: false` 排除出取景，位置由 `place_framing_elements()` 自动摆到画面下缘
- 体积罩子必须 `primitive_cube_add`：**空网格物体 = 没有体积，且不报错**

## 参考

- `references/data-driven.md` —— JSON schema 设计、机位标定、如何把"看图猜的数字"变成可追溯的参数
- `references/scene-recipes.md` —— **场景全链配方**：层 -1~6 每层的脚本、判据与最值钱的那条踩坑经验；配 `examples/run_all.py` 一键跑通
- `references/gn-recipes.md` —— 更多可复制配方：拱窗、栏杆阵列、随机草地、屋顶
- `references/hd2d-recipes.md` —— **游戏场景配方**：元素清单 / 锚点 / 分层 / 自动取景 / 空气透视，含 5 个实测踩坑
- `references/hdri.md` —— Poly Haven HDRI 取法（必须走代理；结构是 `hdri→1k→hdr→url` 四层；下载必须带 `-L`）
- `examples/hd2d_diorama.py` —— 上面那条配方的可运行实现（场景拼装，渲三张图）
- `examples/run_all.py` —— 一键跑完全部场景用例（约 2.5 分钟）
- `examples/scene_atmosphere.py` —— 大气与光照专项（物理天空 + 体积雾，开雾/关雾对照验收）
- 教材仓库：`workspace/pp_textbook`（AGPL-3.0，ShiyumeMeguri/ProceduralParadise）
  - **读它的 `Core/gn.py`（500 行，把 GN 系统重实现一遍）和 `Core/nodes.py`**
  - **它的 `scene.json` 的 `notes` 字段是全场最值得学的东西**
- 上游：`3d-model`（OpenSCAD 参数化零件）、`sculpting-character-pipeline`（高精度角色）
