---
name: generating-3d-models
description: Use when 需要把三视图参考图或概念图变成一个 3D 网格时使用——在 Blender 里按参考比例真实建模（blockout → voxel 融合 → multires → 重拓扑 → 导出），不依赖任何云端生成 API 或本地图像推断权重。触发：「三视图转模型」「生成 3D 模型」「出一张模型」「按参考图建模」「binding 骨骼」「让它动起来」。若用户明确要求图生网格的神经网络推断路线，说明该路线当前不可用。
license: Complete terms in LICENSE.txt
metadata:
  version: 2.0.0
  author: Fairy
tags:
  - 3d
  - mesh
  - generation
  - modeling
  - blender
  - rigging
triggers:
  - 三视图转模型
  - 生成 3D 模型
  - 出一张模型
priority: 64
    - 按参考图建模
    - 绑定骨骼
    - 动效
    - 动画
    - 过渡
    - 缓动
    - 让它动起来
---

# 3D 网格生成：Blender 真实建模

## 路线说明

**本 skill 只提供 Blender 程序化建模路线。**

原路线依赖两样东西，均已下线，不要再尝试：

| 已下线 | 原因 |
|---|---|
| Tripo 云端 API | 需 key，且出图不可控、成本不可预知 |
| 本地 TripoSR 权重 | 权重文件不存在，且图像推断出的网格**不可用于生产**（拓扑乱、无 UV、尺度离谱） |

**替代方案是真实建模**：用参考图定比例和结构，在 Blender 里用基本体和体块操作把形体搭出来。
这条路可控、可复现、可以直接交付，且本机两套 Blender 都验证跑通。

---

## 关键前提

> **没有失败测试的路线不要写进 skill。** 本文件所有 API 调用、参数名、坑，均由
> `references/` 下的两个脚本在 Blender 4.5.14 与 bpy 5.1.0 上实测产出，不是从文档抄的。

跑通证据（2026-10-04）：

- 30+ 基本体搭出 1.70m 人形，**131K 顶点**，包围盒 `z[0.004..1.651]`（脚到头顶完整）
- 像素检测到颈部收窄 70px、腰部 108px，正/侧/3-4 三视角均确认人形可辨
- 双环境探测脚本**输出逐行相同**

---

## 阶段 0：参考与比例

**先定头身比和关键高度，再动手建模。** 比例错，后面全白做。

成年人（1 单位 = 1 米，身高 1.70）：

| 部位 | 高度 |
|---|---|
| 头顶 | 1.655 |
| 下巴 | 1.432 |
| 肩 | 1.400 |
| 胸腔中心 | 1.270 |
| 腰 | 1.060 |
| 胯 | 0.900 |
| 膝 | 0.485 |
| 脚 | 0.030 |
| 眼高 | 0.94（身高的 0.553） |

**相机要平视眼高**，否则透视会扭曲比例。`../sculpting-character-pipeline/references/blockout-character.py` 里
`VIEWS` 三个机位都取 z≈0.94~1.05。

**三视图只用来定比例关系和服装结构，不要拿它量尺寸。** 扩散模型出的三视图
不保证正交投影下几何闭合。

---

## 阶段 1：Blockout

用基本体把形体堆出来。**这一步只要求比例对，表面完全不需要光滑。**

```python
bpy.ops.mesh.primitive_uv_sphere_add(radius=r, segments=24, ring_count=16, location=loc)
bpy.ops.mesh.primitive_cube_add(size=s, location=loc)
```

搭人形最少需要：头、颈、胸腔、腰、盆、双肩、双上臂、双肘、双前臂、双手、
双髋、双大腿、双膝、双小腿、双脚，约 25 个体块。

**肢体必须贴住躯干**——实测教训：手臂中心离躯干 0.21m 时，remesh 后手臂直接消失。
贴身（0.15m 左右）才有连续的轮廓。

---

## 阶段 2：Voxel 融合（最高杠杆，也是最大的坑）

### ⚠️ 致命陷阱：会**静默删除**不连通的体块

**没有报错，没有警告，顶点数照样打印。**

实测：25 个体块一起 `voxel_remesh`，结果包围盒只剩 `z[1.432..1.651]`——
**只剩下一个头，躯干四肢全部消失**。

原因：Voxel Remesh 只在互相**实体重叠**的网格间融合，不连通的不产生输出，连顶点一起丢弃。

### 正确做法：逐件 remesh，最后 join

```python
for ob in parts:
    bpy.ops.object.select_all(action="DESELECT")
    ob.select_set(True)
    bpy.context.view_layer.objects.active = ob
    ob.data.remesh_voxel_size = 0.005
    bpy.ops.object.mode_set(mode="OBJECT")   # 4.5/5.1 必须在 OBJECT 模式
    bpy.ops.object.voxel_remesh()

bpy.ops.object.select_all(action="DESELECT")
for ob in parts:
    ob.select_set(True)
bpy.context.view_layer.objects.active = parts[0]
bpy.ops.object.join()
```

### 渲图后必须验证包围盒

这是唯一可靠的部件存活检查：

```python
bb = [body.matrix_world @ Vector(c) for c in body.bound_box]
print("z[%.3f..%.3f]" % (min(v.z for v in bb), max(v.z for v in bb)))
```

1.70m 人形的 z 下界必须接近 0。**若 z 下界在 1.4 附近，说明只剩头了。**

`voxel_size` 选法：按目标最小特征倒推。想留住 3mm 褶皱，取 1.5~3mm。
角色用毫米级（0.005），场景道具才用 0.02。

---

## 阶段 3：细节

### 笔刷雕刻在无头环境不可用（已实测，不是推测）

```
AttributeError: bpy_struct: attribute "brush" from "Sculpt" is read-only
```

4.5.14 与 5.1.0 **报同样的错**。Blender 4.x 把笔刷改成资产引用
（`tool_settings.sculpt.brush_asset_reference`），无头环境没有资产库可指。

**不要在 `brush_stroke` 上耗时间**，替代方案是程序化置换：

```python
m = ob.modifiers.new("multires", "MULTIRES")
m.levels = 2
m.render_levels = 3
bpy.ops.object.modifier_apply(modifier="multires")

# 4.x 的 NOISE 纹理没有 noise_scale 属性，用 VORONOI
tex = bpy.data.textures.new("Pores", type="VORONOI")
tex.noise_scale = 0.9
d = ob.modifiers.new("detail", "DISPLACE")
d.texture, d.strength, d.mid_level = tex, 0.03, 0.5
bpy.ops.object.modifier_apply(modifier="detail")
```

**局限**：置换只能加噪声状细节，做不出眼窝、鼻翼这类有方向的解剖结构。
那部分靠阶段 1 的体块和阶段 2 的融合质量兜。

---

## 阶段 4：重拓扑

```python
bpy.ops.object.mode_set(mode="OBJECT")
bpy.ops.object.quadriflow_remesh(target_faces=8000)   # 不是 target_number_of_faces
```

**实测：旧参数名 `target_number_of_faces` 在 4.5.14 和 5.1.0 上都报
`TypeError: keyword ... unrecognized`。**

角色布线：眼周环绕 5~7 条边、嘴周 8~12 条、肘/膝/肩各一圈边环。
**不要强行全四边面**，5~7 边形引擎完全支持。

---

## 阶段 5：绑定与动画

真实建模路线下，绑定用普通骨骼体系：

```python
arm_data = bpy.data.armatures.new("rig")
arm_obj = bpy.data.objects.new("rig", arm_data)
bpy.context.collection.objects.link(arm_obj)
```

**先摆姿势再绑权重**：blockout 阶段就用 T-pose 或 A-pose 建，
否则后续蒙皮要反复重刷。

**合并还是每部件独立绑？先判断这条接缝是不是"身体的一部分"。** 2026-10-05 实测定案
（同一份垂臂 blockout、同一套 20 骨骼，只改是否对 join 后的整体做一次 voxel remesh）：

- **身体的一部分**（臂与胸廓本来就该长在一起）→ **必须融合后单骨架 + 自动权重。**
  热权重没有"这是不同部件"的概念：未融合时网格是 25 个互不相连的封闭壳体，
  胸廓那个壳有 **23% 的顶点绑到 `upper_arm.R`、23% 绑到 `upper_arm.L`**——
  46% 的胸廓被手臂骨驱动，手臂一外展就把躯干侧面拽成披风状薄片。
  对整体 remesh 融成单连通体（25 岛 → 1 岛）后，这个跨壳渗漏判据直接消失，
  几何归属一致率从 **74.77% 升到 88.08%**。
- **真正独立可动件**（外套袖、裙摆、可拆护甲）→ 保持为**独立物体 + 独立骨架**。
  一具骨架确实绑不住两个独立摆动的袖子，这条依然成立。

**绝不要把这两类 join 进同一个网格再跑 `ARMATURE_AUTO`**——那正是糖纸披风的成因。
判据不够用时，渲一张摆姿图：静息位永远看不出问题，一摆就露馅。

> 复核脚本：`workspace/probe_weight_bleed.py`（逐岛主导骨骼构成 + 几何归属一致率）。
> 注意"每骨主导顶点数 / 最近顶点数"这个比值**不能**用来判断渗漏：实测未融合时
> `upper_arm.L` 比值仅 0.88、融合后 1.05，两组都正常，这个指标没有判别力。
> 静息位垂臂时手与大腿贴在一起，`hand -> thigh` 的混淆（1845/813 个）同样是
> 指标误报而非渗漏，别当证据。

---

## 铁律

**1. 每渲一张，必须自己看。**

以下全部**不算**验证：文件存在且非空、顶点/面数达标、glb 能加载、包围盒正确。
**全通过而模型是一坨废的情况真实发生过。**

⚠️ 提问时**不要预设结论**。问"五官是否清晰可辨"会得到你想听的；
问"这张图里有几根手指"才是真检验。

**2. 先验像素，再看图。**

VLM 会漏报也会误报。用客观数据定位问题：

```python
from PIL import Image; import numpy as np
a = np.asarray(Image.open("char_front.png").convert("RGB")).astype(int)
lum = a.sum(axis=2)/3
mask = lum > lum[5,5] + 18          # 相对背景阈值，别用绝对值
ys, xs = np.nonzero(mask)
print("bbox x[%d..%d] y[%d..%d]" % (xs.min(), xs.max(), ys.min(), ys.max()))
print("逐行宽度:", [int(mask[y].sum()) for y in range(0, 720, 40)])
```

**逐行宽度能直接看出颈/腰收窄**，比问模型可靠得多。
绝对阈值（如 `>60`）会被背景噪声骗——实测背景亮度 63，直接全图"命中"。

**3. 全黑检测别只看文件大小。** 全黑 PNG 压缩后反而偏小。缩到 560px 再看体积。

---

## 常见错误

**1. 对全部体块一次性 voxel_remesh。** 孤立部件静默消失，见阶段 2。

**2. 肢体离躯干太远。** remesh 后手臂/腿直接不见。贴身 0.15m 量级。

**3. 在 headless 里死磕笔刷雕刻。** read-only，2 个版本都一样，用置换。

**4. quadriflow 写 `target_number_of_faces`。** TypeError，用 `target_faces`。

**5. symmetrize 用 `direction='X'`。** 4.5/5.1 都要写全 `'POSITIVE_X'`。

**6. NOISE 纹理设 `noise_scale`。** 4.x 没有这个属性，用 VORONOI。

**7. 阶段 5 之前不看脸。** 脸型在阶段 2 就定了，等导出时发现等于全白做。

**8. 相机瞄错却以为模型坏了。** 目标点用 `world_to_camera_view` 验证：

```python
v = world_to_camera_view(scene, cam, body.matrix_world.translation)
print("ndc=(%.3f,%.3f) 像素=(%.0f,%.0f)" % (v.x, v.y, v.x*W, (1-v.y)*H))
```

正确值应落在画面中心 (0.5, 0.5)。**先确认相机，再怀疑模型。**

---

## 环境

| 入口 | 版本 | 说明 |
|---|---|---|
| `/home/user/Fairy/.tools/blender/blender-4.5.14-linux-x64/blender` | 4.5.14 LTS | 完整流程，含 Cycles 烘焙。**已固化到 `.tools/`，重启不丢** |
| `/home/user/miniforge3/envs/bpyenv/bin/python3.13` | bpy 5.1.0 | bpy 工具链 / `bpy` MCP 工具 |

**两套行为完全一致**（`../sculpting-character-pipeline/references/dual-env-probe.py` 实测输出逐行相同），
拿不到 4.5.14 就用 5.1.0，结论不变。

bpyenv 里**没装 numpy**，涉及 numpy 的检查要另想办法。

---

## 参考

- `../sculpting-character-pipeline/references/dual-env-probe.py` — 双环境能力探测，改 API 后先跑它
- `../sculpting-character-pipeline/references/blockout-character.py` — 可直接跑通的人形 blockout + 三视角渲染
- **REQUIRED SUB-SKILL:** 高精度角色用 `sculpting-character-pipeline`
- **REQUIRED SUB-SKILL:** 导出交付用 `finalizing-3d-assets`
- **REQUIRED SUB-SKILL:** 三视图生成用 `authoring-three-view-references`
- **REQUIRED SUB-SKILL:** 端到端管线用 `3d-asset-pipeline`
