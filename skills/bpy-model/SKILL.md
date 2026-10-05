---
name: bpy-model
description: Use when 用 Blender/bpy 做 3D 建模与渲染——写场景 JSON、造齿轮/圆柱/圆环等基本体或挤出体、做高精度角色/模型（Voxel Remesh 融合、Multires 细分雕刻、重拓扑、法线烘焙、几何节点、毛发）、存 .blend、渲静态图或渲旋转动画序列、转视频。触发场景：「用 bpy 建模」「bpy 渲染」「Blender 无头建模」「做高精度模型」「角色建模」「捏角色」「雕刻」「细分」「remesh」「重拓扑」「法线烘焙」「毛发」「做场景 JSON」「渲个动画序列」「模型转视频」「齿轮模型」「extrusion 挤出」「spin 旋转动画」。基本体/块面（blockout）只是第 0 档：要出高精度或角色必须走精度阶梯（见正文「精度阶梯」章节）。要写 .scad 参数化模型或 3D 打印件请改用 3d-model 技能（OpenSCAD），两者不要混用。
tags:
  - 3d
  - blender
  - bpy
  - render
  - modeling
triggers:
  - 用 bpy 建模
  - bpy 渲染
  - Blender 无头建模
  - 做场景 JSON
  - 渲个动画序列
  - 模型转视频
  - bpy
priority: 71
---

# 3D 建模（Blender / bpy，无 GUI）

用 `../3d-model/scripts/bpy_model.py` 驱动本机 bpy 跑「写场景 JSON → 建模 → 存 .blend → 渲染 → 看图 → 改」。

**核心不是把 JSON 写对，是每一版改动都真的看过渲染图。** 参数化建模最容易出现的
问题是"代码跑通但形状不对"，所以渲完必须自己看 PNG 再决定要不要改。

## 何时用 bpy，何时用 OpenSCAD

两个技能是**不同工具链，不要混用**。选错的代价是整条链路重写。

| 需求 | 用哪个 | 为什么 |
|---|---|---|
| 写 `.scad` 参数化模型、3D 打印件 | **3d-model**（OpenSCAD） | 参数化几何表达是 OpenSCAD 的强项，输出纯几何，尺寸精确 |
| 齿轮、圆环、挤出型材等现成形状 | **bpy-model** | bmesh 里有现成算子，且 `../3d-model/scripts/bpy_model.py` 已内置 10 种形状 |
| 多物体场景 + 灯光 + 相机 + 材质 | **bpy-model** | 场景 JSON 一步到位，灯光/相机都是数据 API 声明式配置 |
| 渲静态图 / 渲旋转动画序列 | **bpy-model** | `render --animation` 直接出序列帧，spin 还能自动转物体 |
| 模型转视频（mp4） | **bpy-model** | `../3d-model/scripts/model_to_video.py` 串起建模→渲染→编码 |
| 导出 STL/3MF 给 3D 打印 | **3d-model** | bpy 链路不做布尔/精度化，OpenSCAD 更可控 |
| 外部 AI 客户端要调建模能力 | **bpy-model** | 附带 `../3d-model/scripts/bpy_mcp_server.py`，可封成 MCP server |

## 环境

```bash
PY=/home/user/miniforge3/envs/bpyenv/bin/python3.13
M=skills/3d-model/scripts/bpy_model.py
```

**必须用 bpyenv 的 python3.13**——bpy 只发 cp313 wheel，系统 python3 装不上也跑不了。
`../3d-model/scripts/model_to_video.py` 例外：它自己不 import bpy，只负责编排，用系统 `python3` 即可。

## 四个子命令

### 1. 体检（不产生任何文件，任何时候先跑这个）

```bash
$PY $M doctor
```

### 2. 造单个形状并存图

`type` 是**位置参数**，不是 `--type`（写错会报 unrecognized arguments）。

```bash
$PY $M asset gear --name G --blend g.blend --render g.png
```

### 3. 按场景 JSON 建模

```bash
$PY $M scene scene.json --blend s.blend --render s.png
```

### 4. 渲已有 .blend（单张或序列）

```bash
$PY $M render s.blend --out frame.png          # 单张
$PY $M render s.blend --out f_ --animation      # 序列，输出 f_0001.png …
```

### 附：端到端转视频

```bash
python3 skills/3d-model/scripts/model_to_video.py demo --type gear --frames 45 --out-dir demo
```

## 可用形状

`cube` `plane` `sphere` `cylinder` `cone` `torus` `gear` `ico` —— 8 种基本体。

另有两个复合体，参数不同：

- **`extrusion`**：2D 轮廓挤出（墙体、型材、L 形件）。**必须**给 `outline` = 至少 3 个
  `[x,y]` 点，另有可选 `depth`（拉伸厚度，默认 1.0）。
- **`array`**：阵列（栏杆、栅格）。参数 `base`（被复制的单体块）、`count`（默认 4）、
  `step`（间距，默认 `[1,0,0]`）。

## 精度阶梯：做高精度得靠工具，不是靠资源站

⚠️ **上面 10 种形状只是第 0 档（blockout，形体块面）。** 堆基本体只能出"摆件级"
形体，**堆不出角色**。高精度来自 Blender 自己的工具链，按顺序升级：

### 1. Voxel Remesh —— 融合相交体，**最高杠杆的一步**

多根管状/块状网格互相穿插时，交界处必然有硬边、鼓包、粘连。先重拓扑成**一个连续
水密体**再谈细节。

```python
ob = bpy.data.objects[name]
bpy.context.view_layer.objects.active = ob
ob.select_set(True)
bpy.ops.object.mode_set(mode="EDIT")
bpy.ops.mesh.select_all(action="SELECT")
ob.data.remesh_voxel_size = 0.02   # 越小越细，代价是面数暴涨
bpy.ops.object.voxel_remesh()      # ← 实际是 bpy.ops.object.voxel_remesh
bpy.ops.object.mode_set(mode="OBJECT")
```

选 `voxel_size` 时以**目标最小特征**倒推：想留住 2mm 褶皱，voxel 取 1~2mm。
参考量级：voxel 0.02 的单体约 3~8 万面。

### 2. Multires Modifier + Sculpt —— 高模雕刻核心

先低模，加多级细分，逐级雕刻（褶皱、孔隙、肌肉起伏）。

```python
m = ob.modifiers.new("multires", "MULTIRES")
m.levels = 4          # 视口级别
m.render_levels = 6   # 渲染级别；每级 ×4 面数，基数 1k → 6 级约 4M 面
```

⚠️ **无头限制（务必先读）**：`Multires` 加级很容易，但**笔刷雕刻需要交互上下文**。
无头环境不要指望 `bpy.ops.sculpt.brush_stroke`，两条可行替代：

- **程序化置换（稳、可复现）**：Multires + `Displace` modifier 挂
  Voronoi/Noise 纹理。细节是"长出来"不是"雕出来"，形状可控性差，但不会崩。
- **模拟笔刷（高风险）**：`context_override` 注入 sculpt 上下文。sculpt 数据存在
  multires grid 上，盲写易产出不可复现的垃圾，且无法回退。**没跑通前不要上生产链。**

### 3. Quadriflow —— 重拓扑成干净四边面

高模面数高但拓扑全是三角面，不可用。重拓扑成规整四边面。

```python
bpy.ops.object.quadriflow_remesh(
    use_remesh_preserve_volume=True,
    use_remesh_preserve_sharp=True,
    target_number_of_faces=10000,   # 按目标平台定
)
```

### 4. Shrinkwrap + 法线烘焙 —— 实时交付标准流程

高模细节无法直接进引擎，烘成法线贴图转移给低模。

```python
low = bpy.data.objects["Low"]     # 低模（渲染用）
high = bpy.data.objects["High"]   # 高模（细节源，低模必须包住它）
sw = low.modifiers.new("shrink", "SHRINKWRAP")
sw.target, sw.wrap_method = high, "NEAREST_SURFACEPOINT"
sw.offset = 0.001
```

烘焙用 Cycles（EEVEE 不支持 normal bake）：`bpy.ops.object.bake(type="NORMAL")`，
且低模必须有 UV（`bpy.ops.uv.smart_project()`）。

### 5. Geometry Nodes —— 程序化高密度细节

散布、阵列、破损这类**规则重复**的细节，几何节点比手搓快几个数量级。可完全脚本化，
无头友好。代价：节点图在 Python 里冗长，且**参数化改动需要重跑图**。

### 6. Hair Curves —— 毛发/绒毛专用系统

高精度毛发不要用放样体假冒（轮廓和分缕都不对）。
⚠️ 真正 groom 毛发是交互的；无头只能退到粒子系统撒毛发，或曲线程序化近似。

### 7. 导出到 WebGL —— 卡通材质会丢色，必须手工兜底

`export_scene.gltf` 导出卡通材质时，**`ShaderToRGB` 不在 glTF 规范内**，
导出的 `pbrMetallicRoughness.baseColorFactor` 全变 `(0,0,0)`、
`emissiveFactor` 全变 `(1,1,1)` —— 全部材质塌成同一个白模，
颜色和区分度全丢，**而且不报任何错**。

补救：导出前从 ColorRamp 抓三段色存 JSON，网页端按材质名查表还原。

### 8. three.js 描边：别用反向壳

**反向壳（BackSide + 法线外推）在薄板和非凸物体上会彻底失效**：
壳从背面穿出来把本体盖住，画面出现大片纯黑矩形。
补救尝试全部无效——按 `g.type` 跳过、厚度随尺寸自适应、法线同向检测，
因为壳会挂在非凸物体的凹处，判定覆盖不到。

**正解：`three/addons/effects/OutlineEffect.js`**，屏幕空间后处理：

```js
const effect = new OutlineEffect(renderer, {
  defaultThickness: 0.0025, defaultColor: [0.02, 0.03, 0.07],
  defaultAlpha: 0.9, defaultKeepAlive: true
});
// 循环里用 effect.render(scene, camera) 代替 renderer.render()
```

**其他 three.js 坑**：
- `Box3.expandByObject(o)` 展开**整个子树**；子节点若是手工改过顶点、
  包围球从未重算的描边壳，会读出巨大陈旧值（实测包围盒 48 vs 真值 9.9）。
  自己遍历角点算。
- 描边壳必须 `recomputeBoundingSphere`，否则 AABB 剔除会连带出错。
- 室内补光点光要用 `distance + decay` 限范围，否则整栋楼被洗白。
- 调试别靠猜：先隐藏可疑对象截图对照，或用 eval 读回真实数值。


### 推荐顺序

```
blockout（低模块面）
  → Voxel Remesh（连续体）
  → Multires + 程序化置换（细节）
  → Quadriflow（重拓扑）
  → Shrinkwrap + 法线烘焙（转低模）
  → Hair Curves / Geometry Nodes
```

**每一级都要渲一张图对比。** 上一级没验过就往下堆，返工成本翻倍。
（这不是理论：曾经用 TripoSR 草模冒充精修件交付，返工时才发现模型本身就是废的。）

### 铁律：渲完必须自己看图

**视觉产物不能用指标代替验收。** 以下全部**不算**验证：

- 文件存在且非空
- 顶点/三角面数达标
- 网格能被 glb 正常载入
- 尺寸比例、包围盒、拓扑统计正确

以上全通过而模型是一坨废的情况**真实发生过**。正确做法：

1. 渲染出 PNG
2. **主动用 vision 看这张图**，问"这东西是什么、质量如何、有没有断裂穿模"
3. 看不过就当场返工，**不往下走、不写进 backlog、不报"完成"**
4. 报完成前，把图拿给用户看

⚠️ 提问时**不要预设结论**。问"躯干是否挺直、双臂有无重叠"会得到你想听的答案；
要问"面部五官是否清晰可辨、材质是纯色还是带纹理、像不像成品"这种开放问题。

## 场景 JSON 结构

```jsonc
{
  "objects": [
    {
      "type": "gear",              // 见上面的形状表
      "name": "Subject",           // 见下方 spin 的隐藏约定
      "location": [0, 0, 0],       // 可选，默认原点
      "rotation": [0, 0, 0],       // 可选，单位度
      "scale": [1, 1, 1],          // 可选
      "material": "steel",         // 可选
      "color": [0.6, 0.7, 0.8, 1.0],   // RGBA
      "metallic": 0.8,             // 可选
      "roughness": 0.35            // 可选
    }
  ],

  "studio": {
    "world": [0.05, 0.05, 0.06, 1.0],   // ⚠️ 是**颜色数组**，不是预设名字，见坑 6
    "lights": [
      { "name": "Key", "loc": [4, -4, 6], "energy": 1000, "type": "AREA", "size": 5 }
    ],
    "camera": { "name": "Camera", "loc": [6, -6, 5], "look_at": [0, 0, 0] }
  },

  "render": {
    "width": 800, "height": 600,     // 默认 800x600
    "fps": 24,                       // 默认 24
    "frame_start": 1,                // 默认 1
    "frame_end": 12,                 // ⚠️ 默认只有 1，见坑 7
    "transparent": false,            // 可选，透明背景
    "engine": "BLENDER_EEVEE"        // ⚠️ 只能这一种，见坑 3
  },

  "spin": { "axis": "Y", "degrees": 360 }   // 可选，旋转动画
}
```

### spin 旋转动画

`spin` 会把 `objects` 里的物体在 `frame_start..frame_end` 之间绕轴旋转，渲序列即得转台动画。

**两个不写在文档里、但会让你白跑一次的约定：**

1. **物体名必须是 `Subject`**，或者场景里**只有唯一一个**网格物体。`apply_spin()` 找不到
   就直接静默不转——你会得到一段完全静止的序列，而且没有任何报错。
2. **`frame_end` 默认是 1**，必须显式调大（比如 12）才有可看的动画。源码里
   `end = max(frame_end, frame_start + 1)` 会兜底到至少 2 帧，但 2 帧看不出旋转。

## 已知坑（逐条实测过，2026-10-03）

1. **`import bmesh` 必须在 `import bpy` 之后**，否则
   `ModuleNotFoundError: No module named 'bmesh'`。bmesh 是 bpy 初始化时注册进去的子模块。
2. **渲染引擎只有 `BLENDER_EEVEE`**，没有 `BLENDER_EEVEE_NEXT`（那是 4.2+ 的名字）。
   写 `BLENDER_EEVEE_NEXT` 会直接抛枚举错误。
3. **`bmesh.ops` 里没有 `create_torus`**。圆环和齿轮都是手写网格（`torus_mesh()` /
   `gear_mesh()` 自己拼顶点面），不是现成算子。别去 `bmesh.ops` 里找。
4. **用数据 API 加的相机必须显式 `scene.camera = cam`**，否则渲染时用的是默认相机，
   你调了半天 `cam.location` 完全不起作用。
5. **`studio.world` 是颜色数组 `[r,g,b,a]`，不是预设名字。** 写成
   `"world": "studio"` 会报
   `sequences of dimension 0 should contain 4 items, not 6`——因为代码直接
   `tuple("studio")` 成了 6 个字符。这是整个 JSON 里最容易踩的一个，报错信息完全不指向真因。
6. **`frame_end` 默认 1**，要动画必须显式给（见上面 spin 那节）。

### 一条被旧文档写错的说明

`workspace/result/建模通道说明.md` 称「无 GUI 下 `bpy.ops.mesh.primitive_*` 用不了，
只能用 bpy.data + bmesh.ops」。**这条在本环境（bpy 5.1.0）实测不成立**：
`bpy.ops.mesh.primitive_cube_add()` 返回 `{'FINISHED'}` 并实打实建出了 MESH 物体。

`bpy_model.py` 仍走 bmesh 数据 API，但那是它**自己的实现选择**（确定性更好、不依赖
上下文），不是被逼的。写新代码时别被这条误导，也别把"用 bpy.ops 会被拒"当成约束。

### 环境噪音（无害）

bpyenv 里**没装 numpy**，导致 bpy 的 gltf2 addon 注册失败，每次运行都刷一段
`ModuleNotFoundError: No module named 'numpy'` 的 traceback。
非致命——建模走 bmesh 数据 API，与该 addon 无关，**退出码仍是 0**。看到别慌。
要清静就 `python3.13 -m pip install numpy`。

## 附：MCP server

同一套能力已封成 MCP server（`skills/3d-model/scripts/bpy_mcp_server.py`），
外部 AI 客户端可直接调 `bpy_doctor` / `bpy_scene` / `bpy_asset` / `bpy_render`。
用 bpyenv 的 python3.13 启动，stdio 传输。验收脚本：`bpy_mcp_selftest.py`。
