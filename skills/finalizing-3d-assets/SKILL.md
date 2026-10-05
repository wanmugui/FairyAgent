---
name: finalizing-3d-assets
description: Use when an AI-generated 3D mesh, character, prop, or model needs to become a deliverable asset: UV unwrapping, PBR texture sets, real-world scale, LOD, naming, or export to glTF/FBX/OBJ. 触发场景：「模型没法用」「贴图不对」「没有UV」「导出到引擎」「要LOD」「模型太大了」「这个网格怎么用」。
tags:
  - 3d
  - asset
  - export
  - optimization
triggers:
  - 模型没法用
  - 贴图不对
  - 没有UV
  - 导出到引擎
  - 要LOD
  - 模型太大了
  - 这个网格怎么用
priority: 68
---

# 资产化 3D 模型

**建模只占这项工作的一小部分。** 没有 UV、没有规范贴图、尺寸不对齐的网格不是资产，是草稿。**这一步才是交付物本身。**

## 本机环境事实（2026-10-02 实测）

三个工具**都装了，但都不在 PATH，且都装在 `/tmp` 下——重启即失**。
`command -v blender` 返回空，**不代表没装**，先按下面的路径找：

| 工具 | 真实位置 | 版本 | 在 PATH？ |
| --- | --- | --- | --- |
| Blender | `/tmp/assetpipe/blender-4.5.14-linux-x64/blender` | 4.5.14 LTS | 否 |
| gltfpack | `/tmp/assetpipe/node_modules/.bin/gltfpack` | 1.3 | 否 |
| gltf-transform | `/tmp/assetpipe/node_modules/.bin/gltf-transform` | 4.5.1 | 否 |
| trimesh | `python3 -c "import trimesh"` | 5.1.0 | 是 |
| PyMeshLab | — | 未装（镜像限流装不上） | — |

用之前先设 PATH，否则每条命令都得写全路径：

```bash
export PATH="/tmp/assetpipe/node_modules/.bin:$PATH"
BL=/tmp/assetpipe/blender-4.5.14-linux-x64/blender
```

`/tmp` 会被清空。真要长期用，把这些重装到 `/opt` 或用户目录，别指望 `/tmp`。

`3d-model` skill 走 OpenSCAD，适合代码化精确模型（支架、外壳、卡扣、齿轮）。有机形体（角色、生物、雕塑）才走本 skill。

## 压缩：先看清代价再选编码

实测同一真实资产（`miyabi_v2.glb`，783,876 B，39146 面，watertight）：

| 命令 | 体积 | `trimesh.load` | watertight |
| --- | --- | --- | --- |
| 原始（不压） | 783,876 B | OK | True |
| `gltf-transform optimize --compress quantize` | 398,316 B | **OK** | True |
| `gltf-transform optimize`（默认 = meshopt） | 138,248 B | **FAIL** | — |
| `gltfpack -i in -o out -cc` | 149,876 B | **FAIL** | — |
| `gltf-transform optimize --compress draco` | 79,088 B | ⚠️ 能载入但 `watertight=False` | False |

### ⚠️ 最要命的一条：默认压缩出来的文件 trimesh 读不了

`gltf-transform optimize` 默认和 `gltfpack -cc` 都会写入
`EXT_meshopt_compression` + `KHR_mesh_quantization`，POSITION 被量化成 int16/uint8。
**trimesh 5.1.0 解不了 meshopt 的 bufferView**，直接抛
`IndexError: list index out of range`（在 `_read_buffers` 里）。

文件本身是合法 glTF 2.0——magic `glTF`、version 2、声明长度与实际字节一致、
accessor 无越界。**是 trimesh 不支持这个扩展，不是文件坏了。**

所以：下游只要有 `trimesh` 参与校验/处理（比如 `3d-free-routes/verify_glb.py`），
就必须显式 `--compress quantize`，别用默认值。`quantize` 仍能压掉约一半体积且 watertight 保持。

### ⚠️ Draco 是另一个坑

`--compress draco` 体积最小（79,088 B），但：
- 依赖 DracoPy，**本机没装**
- 没装时 trimesh 不报错，而是**静默塞一堆 0 顶点占位**，`is_watertight` 直接变 `False`
- 解压后顶点位置全错

要用 Draco 先确认 DracoPy 在，下游也要能解。

### 顺序：Blender 导出后体积会涨，优化也救不回来

Blender 导出 glb 往往**比输入大**，而且后面再 optimize 也压不回去。本例实测：

| 步骤 | 体积 | 相对输入 |
| --- | --- | --- |
| 输入 `miyabi_v2.glb` | 783,876 B | 1.00× |
| Blender 导出（DECIMATE 0.5 后） | 2,702,224 B | **3.45×** |
| 再 `optimize --compress false` | 2,232,292 B | 2.85× |
| 再 `optimize --compress quantize` | 1,293,064 B | **1.65×** |

原因：Blender 把顶点色/UV 摊平成未优化的布局。**所以只在真需要 Blender 的功能
（重拓扑、UV 烘焙、LOD）时才走这一趟**；单纯要减面，直接在 glTF 层做，别绕 Blender。

### ⚠️ gltf-transform 出错时退出码仍是 0

`gltf-transform optimize in.glb out.glb --compress none`（`none` 是非法值，
合法值是 `draco` / `meshopt` / `quantize` / `false`）会打印
`Unknown compression type: "none"`，但**退出码是 0**，且不生成输出文件。

所以 `cmd && next_step` 这种链子会以为成功了，然后拿着一个不存在的文件继续跑。
**每次 optimize 之后都要确认产物存在**：

```bash
gltf-transform optimize in.glb out.glb --compress quantize && test -s out.glb \
  || echo "优化失败：$?"

## 验收命令（可直接抄）

```bash
export PATH="/tmp/assetpipe/node_modules/.bin:$PATH"
BL=/tmp/assetpipe/blender-4.5.14-linux-x64/blender

# 几何体检 + DECIMATE + UV 状态，一次跑完
$BL --background --python blender_check.py -- in.glb out.glb
```

`blender_check.py`（在 `workspace/`）逐项打印
`NONMANIFOLD_BEFORE/AFTER`、`SELF_INTERSECT_BEFORE/AFTER`、`UV_*`、`EXPORTED`。
本机实测：非流形边 0 → 0，自交面 0 → 0，DECIMATE ratio=0.5 后 39146 → 19572 面。

**⚠️ DECIMATE 会破坏水密性**：同一例输入 watertight=True，走完 Blender 后变 **False**。
需要水密（要打印、要布尔运算）就别用 DECIMATE，改在 `gltf-transform` 里做减面。

### Blender 4.5 headless 的三个 API 坑

1. **自交选择没有 `mesh.select_intersect`**。用
   `bpy.ops.mesh.intersect(mode="SELECT", separate_mode="NONE", solver="FAST")`。
   `separate_mode` 只能是 `ALL`/`CUT`/`NONE`，**给成 `ALL` 会真的把网格切开**
   （算子本意是 "Cut an intersection into faces"），于是「选一下」变成「切了模型」。
2. **`uv.select_overlap` 只认当前选择集**。必须先
   `bpy.ops.mesh.select_all(action="SELECT")` 再调；先 `DESELECT` 再调，
   **无论有没有重叠都返回 0**——静默假阴性，最坑的一种。
   自查办法见下。
3. **`uv.smart_project` 在 headless 下不产出 UV**。算子返回成功（`FINISHED`），
   但 UV 层数据长度仍是 0。判据不能看返回值，要看**数据长度有没有从 0 变成非 0**。

   顺带：glTF 导入的网格**可能根本没有 UV 层**，此时 `me.uv_layers[0]` 直接 IndexError，
   得先 `me.uv_layers.new(name="UVMap")`。

### 数字可信性：任何"0 缺陷"都要先做负对照

`uv_overlap_control.py` 把所有面的 UV 压到同一点，**必须**报出全重叠才算探测器有效。
我这次就是靠它发现上面第 2 条的假阴性——修正前报 0，修正后报 48/48。
**没做过负对照的检测器，报出来的 0 一律当作没测。**

### UV 重叠：本机测不了，别假装测了

`smart_project` 在本机 headless 下不生成 UV（上面第 3 条），所以
**UV 是否重叠这一项在本环境无法验证**，`blender_check.py` 会打印
`UV_OVERLAP: SKIPPED` 而不是编一个数。要测得有带 UI 的环境。

## 验收门（逐条可查，不靠"看起来还行"）

| 项 | 判据 | 常见翻车 |
| --- | --- | --- |
| **真实尺度** | 1 单位 = 1 米。人 1.7、门 2.0、桌 0.75 | AI 输出尺寸离谱且**不报错**——能加载能渲染，只是小如沙粒或大如山 |
| UV | 已展开，**无重叠**，seam 位置在隐蔽处（腋下、裆缝内侧） | 未展开或严重重叠，贴图糊成一片 |
| 贴图五件套 | albedo / normal / roughness / metallic / AO 齐全且分辨率一致 | 只有一张 diffuse，引擎里像塑料 |
| 色彩空间 | albedo 用 sRGB；normal/roughness/metallic/AO 用线性 | 贴图发灰或法线全错 |
| 法线 | 朝外一致 | 部分区域黑面、逆光面 |
| 几何 | 无退化面、无重叠面、法线连续 | 导入即报 warning 或直接崩引擎 |
| 面数 | 在目标预算内（手游 5-15k、三角 30-100k、PC 影视不限） | AI 默认常在 10 万以上 |
| 命名 | 与源文件同名、材质前缀统一、无 `Cube.001` | 从 Blender 默认名带出来的 |
| 导出 | 按目标引擎选格式 | 见下表 |

## 导出格式选择

| 格式 | 用在哪 |
| --- | --- |
| **glTF / GLB** | 引擎与 Web 默认首选。GLB 自带贴图，单文件不易丢 |
| **FBX** | 主流引擎、需要保留骨骼与动画时 |
| **OBJ + MTL** | 通用交换，**不保留动画**，贴图易丢 |
| **STL** | 仅 3D 打印，无贴图无颜色 |

## LOD

至少两级：L0 全精度、L1 减面 50% 用于远景。移动端还需 L2 减面 75%。减面用减面算法重做，**不要直接降面数**——后者会毁拓扑。

## 常见错误

**1. 只交付网格。** 引擎里显示成灰白塑料体，八成是缺 UV 或缺贴图。

**2. 忘了真实尺度。** 导入场景才发现小得离谱，前面所有工作白做。**在路线确定时就要定尺寸**（见 `planning-3d-asset-pipeline`）。

**3. 贴图色彩空间搞错。** albedo 用线性 → 发灰；法线用 sRGB → 光照完全不对。

**4. 直接降面数当 LOD。** 面数是变了，拓扑也毁了。要减面算法。

**5. 从 Blender 默认名导出。** `Cube.001`、`Material.001` 这类名字进版本库会打架。

**6. OBJ 当主力格式。** 它不保留动画，贴图还得手工配路径。

## 参考

- **REQUIRED SUB-SKILL:** 三视图来源用 `authoring-three-view-references`
- **REQUIRED SUB-SKILL:** 需要动起来时用 `animating-3d-assets`
- **REQUIRED SUB-SKILL:** 代码化精确模型改用 `3d-model`
