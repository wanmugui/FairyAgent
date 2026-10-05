---
name: bpy-model
description: Use when 用 Blender/bpy 做 3D 建模与渲染——写场景 JSON、造齿轮/圆柱/圆环等基本体或挤出体、存 .blend、渲静态图或渲旋转动画序列、转视频。触发场景：「用 bpy 建模」「bpy 渲染」「Blender 无头建模」「做场景 JSON」「渲个动画序列」「模型转视频」「齿轮模型」「extrusion 挤出」「spin 旋转动画」。要写 .scad 参数化模型或 3D 打印件请改用 3d-model 技能（OpenSCAD），两者不要混用。
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

用 `scripts/bpy_model.py` 驱动本机 bpy 跑「写场景 JSON → 建模 → 存 .blend → 渲染 → 看图 → 改」。

**核心不是把 JSON 写对，是每一版改动都真的看过渲染图。** 参数化建模最容易出现的
问题是"代码跑通但形状不对"，所以渲完必须自己看 PNG 再决定要不要改。

## 何时用 bpy，何时用 OpenSCAD

两个技能是**不同工具链，不要混用**。选错的代价是整条链路重写。

| 需求 | 用哪个 | 为什么 |
|---|---|---|
| 写 `.scad` 参数化模型、3D 打印件 | **3d-model**（OpenSCAD） | 参数化几何表达是 OpenSCAD 的强项，输出纯几何，尺寸精确 |
| 齿轮、圆环、挤出型材等现成形状 | **bpy-model** | bmesh 里有现成算子，且 `bpy_model.py` 已内置 10 种形状 |
| 多物体场景 + 灯光 + 相机 + 材质 | **bpy-model** | 场景 JSON 一步到位，灯光/相机都是数据 API 声明式配置 |
| 渲静态图 / 渲旋转动画序列 | **bpy-model** | `render --animation` 直接出序列帧，spin 还能自动转物体 |
| 模型转视频（mp4） | **bpy-model** | `model_to_video.py` 串起建模→渲染→编码 |
| 导出 STL/3MF 给 3D 打印 | **3d-model** | bpy 链路不做布尔/精度化，OpenSCAD 更可控 |
| 外部 AI 客户端要调建模能力 | **bpy-model** | 附带 `bpy_mcp_server.py`，可封成 MCP server |

## 环境

```bash
PY=/home/user/miniforge3/envs/bpyenv/bin/python3.13
M=skills/3d-model/scripts/bpy_model.py
```

**必须用 bpyenv 的 python3.13**——bpy 只发 cp313 wheel，系统 python3 装不上也跑不了。
`model_to_video.py` 例外：它自己不 import bpy，只负责编排，用系统 `python3` 即可。

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
