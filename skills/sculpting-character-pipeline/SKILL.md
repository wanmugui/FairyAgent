---
name: sculpting-character-pipeline
description: Use when 在 Blender/bpy 里做高精度角色建模（人体、人物、头像）时使用——按工业流程的六个阶段推进（参考与比例 → blockout → Voxel Remesh 融合 → Multires 细节 → Quadriflow 重拓扑 → Shrinkwrap 与法线烘焙交付），并处理无头环境下笔刷雕刻不可用时的替代方案。触发：「角色建模」「高精度人物」「捏角色」「雕刻细分」「重拓扑」「拓扑布线」「法线烘焙」「角色交付」「character sculpting」「retopology」。
priority: 76
tags:
  - 3d
  - character
  - sculpting
  - retopology
  - baking
  - blender
  - pipeline
triggers:
  - 角色建模
  - 高精度人物
  - 捏角色
  - 雕刻细分
  - 重拓扑
  - 拓扑布线
  - 法线烘焙
  - character sculpting
  - retopology
  - 高模转低模
  - 角色交付
  - 眼睛拓扑
---

> **环境前提（2026-10-04 实测修订）**
>
> 本机有**两套 Blender，行为完全一致**，本文所有参数在两套上都成立，**不必区分版本**：
>
> | 入口 | 版本 | 用途 |
> |---|---|---|
> | `/home/user/Fairy/.tools/blender/blender-4.5.14-linux-x64/blender` | 4.5.14 LTS（独立版，163MB） | 完整流程，含 Cycles 烘焙 |
> | `/home/user/miniforge3/envs/bpyenv/bin/python3.13` | bpy **5.1.0**（pip 模块） | bpy 工具链 / `bpy` MCP 工具 |
>
> 双环境跑同一份探测脚本，**输出逐行相同**：`voxel_remesh`、`quadriflow_remesh(target_faces=)`、
> `MULTIRES`、`DISPLACE+VORONOI`、`NOISE` 无 `noise_scale` 属性、`symmetrize(direction="POSITIVE_X")`、
> `uv.smart_project` 全部一致。笔刷在两版上同样报 `attribute "brush" from "Sculpt" is read-only`。
>
> ✅ 4.5.14 已固化到 `/home/user/Fairy/.tools/blender/`（2026-10-04 搬入，md5 校验一致，5581 文件），
> **重启不会丢**。若确实缺失，就用 bpyenv 的 5.1.0，**结论不会因此改变**。
>
> 完整探测脚本见 `references/dual-env-probe.py`。

# 高精度角色建模流水线

角色建模不是"堆方块"，是一条**有严格顺序**的流水线。顺序错了，后面每一步都在补救前面的错，返工成本翻倍。

> **环境前提**：标注「实测」的参数在 **Blender 4.5.14 LTS 无头环境**验证过。
> 运行时只用 `/home/user/Fairy/.tools/blender/blender-4.5.14-linux-x64/blender`。
> **不要照抄网上教程的 3.x / 4.0 写法**，4.5 有多处 API 改名（见 `references/api-notes.md`）。

## ⚠️ 先读这一节：角色是「部件装配」，不是「一体雕刻」

**这是角色建模与其他 3D 建模最根本的差别，也是最常被搞错的地方。**

雕塑、岩石、布料是有机曲面——它们的建模思路是**把所有体块融成一个连续体**，再在上面雕细节。

**角色不是。** 角色是**一堆互相独立的部件穿在一起**：

```
Head_Face   头（不含头发）
Hair_Front  前发 / Hair_Back 后发 / Hair_Side 侧发
Eye_L / Eye_R  眼球（球体，独立）
Body_Torso  躯干（衣服内层）
Top_Upper   上衣 / Skirt_Dress 裙
Sleeve_L/R  袖子 / Glove_L/R 手套
Pants_L/R   裤腿 / Shoe_L/R 鞋
Accessory_01..n 饰品（每件独立）
```

**为什么必须分件：**

| 需求 | 融合成一体就废 |
|---|---|
| 头发要有自己的轮廓和摆动 | 融进头就没有分界，轮廓糊掉 |
| 换装 / 多套服装 | 改一个 mesh 就要重做全身 |
| 动画绑定 | 一个 mesh 绑不出可动的肩、肘、腕 |
| 材质分区 | 三渲二靠色块分界，融合后分界消失 |
| 局部返工 | 改袖口要重雕整个身体 |

**分件还决定风格**。三渲二 / 二次元角色的可读性**靠清晰的外轮廓和部件分界**，不靠光滑曲面。硬边、明确的分色边界——那是部件与部件的接缝给的。

**关键认知：融合（Voxel Remesh）只在「单个部件内部」用，绝不跨部件。**

- ✅ 头：眼球窝 + 颅骨 + 下颌 → 融成一个头
- ❌ 头 + 头发 + 耳朵 → 绝不能融

**部件的穿插是有意的**：让袖子插进肩、头发插进头皮，**靠穿插产生遮挡关系**，不做布尔、不做融合。穿插深度 2~5mm，够看不露缝。

---

## 六个阶段

```
0 参考与比例 → 1 部件清单与骨架 → 2 逐件 Blockout → 3 单件 Voxel Remesh + 细节
                                                              ↓
                      6 装配与验收 ← 5 重拓扑/UV/烘焙 ← 4（按需）
```

**每阶段必须渲图看过再进下一阶段。** 阶段间无返工窗口，攒到最后才发现脸型不对，等于从 0 重来。

### 阶段 0.5：定部件清单（角色专属，最容易被跳过）

**建任何几何之前，先把部件列成表。** 漏一个部件 = 后面要拆 = 返工。

```
必做部件（无此人形）：
  Head_Face, Hair_Front, Hair_Back, Hair_Side, Eye_L, Eye_R,
  Body_Torso, Neck, Top_Upper, Sleeve_L, Sleeve_R, Hand_L, Hand_R,
  Pants_L, Pants_R, Shoe_L, Shoe_R, Accessory_*

按角色增减：
  长发 → Hair_Back 拆成 Hair_Back_Mid / Hair_Back_Low（分层做，便于摆动）
  裙装 → Skirt_Dress 单件 + 可选 Underskirt
  披风/长发 → 需要额外考虑 SoftRig 或骨骼链，不是几何问题
```

**判定拆件还是合件**：会被动画单独驱动的、材质要分色的、造型上有独立轮廓的 → 一律独立。

### 阶段 0：参考与比例（不建模，但决定成败）

人在建模前先定**头身比**和关键测量值。缺这一步，第 1 阶段的 blockout 比例一定是错的。

一个成年人标准尺寸（1 单位 = 1 米）：

| 部位 | 尺寸 |
|---|---|
| 总高 | 1.70 |
| 头高 | 0.23（约 7.5 头身） |
| 眼高 | 0.94（身高的 0.553） |
| 肩宽 | 0.46（约 2 个头宽） |
| 腰高 | 0.62 |
| 胯高 | 0.53 |
| 手腕高 | 0.49 |
| 膝高 | 0.285 |

**女性约 7.5 头身，男性约 8 头身。** 二次元风格通常拉到 6.5~7 头身。

视点：人眼水平高度 = 身高的 0.553 处。相机要平视这个高度，否则透视会扭曲比例。

**三视图不是精确图纸**——扩散模型出的三视图只保证外观合理，**不保证正交投影下的几何闭合**。不要拿它量尺寸，用它定**比例关系和服装结构**。

### 阶段 1：Blockout —— 只定比例，不管细节

用基本体（球/柱/立方）搭出躯干、四肢、头的体块。**这一步只要求比例对，表面完全不需要光滑。**

关键：所有体块**必须互相穿插**。后续 Voxel Remesh 靠穿插来融合，分离的体块融不成连续体。

```python
# 头：球体压扁；躯干：倒梯形；四肢：圆柱
bpy.ops.mesh.primitive_uv_sphere_add(radius=0.11, segments=24, ring_count=16)
bpy.ops.mesh.primitive_cube_add(size=1)
bpy.ops.mesh.primitive_cylinder_add(vertices=16, radius=0.05, depth=0.6)
```

**不要对称镜像出双侧体块**——只建一半（单侧 + 中轴），阶段 4 再 symmetrize。手搭两侧容易左右不一致。

### 阶段 2：Voxel Remesh —— 融合成连续体（最高杠杆的一步）

blockout 是几十个独立网格，**重叠处有硬边、鼓包、粘连**。这一步把它们重拓扑成**一个连续水密体**，是后面所有细节操作的前提。

```python
bpy.ops.object.mode_set(mode="OBJECT")   # 4.5 必须在 OBJECT 模式，EDIT 会报 OperatorError
ob.data.remesh_voxel_size = 0.003       # 角色用毫米级，场景用厘米级
bpy.ops.object.voxel_remesh()
```

**voxel_size 怎么选**：按目标最小特征倒推。想留住 3mm 的褶皱，voxel 取 1.5~3mm。

面数量级参考：voxel 0.003 的成年人体块约 **3~8 万面**。0.02 只适合场景道具。

**这一步之后不要平滑**。平滑会把刚融好的结构抹圆，后面补不回来。

#### ⚠️ 致命陷阱：Voxel Remesh 会**静默删除**不连通的体块

这不是报错，是**无提示的部件丢失**。

**实测（2026-10-04）**：一次 30+ 体块的人形，把头/躯干/四肢一起选中 `voxel_remesh`，结果
**包围盒只剩 `z[1.432..1.651]`——即只剩下一个头，躯干和四肢全部消失**。
没有任何报错、没有警告，顶点数也照样打印。

原因：Voxel Remesh 只在**互相实体重叠**的网格之间融合。与主体不相连的网格不产生任何输出，**连同它的顶点一起被丢弃**。

**正确做法——逐件 remesh，最后 join**：

```python
# 逐件 remesh：每个部件自己融自己，丢失风险为零
for ob in parts:
    bpy.ops.object.select_all(action="DESELECT")
    ob.select_set(True)
    bpy.context.view_layer.objects.active = ob
    ob.data.remesh_voxel_size = 0.005
    bpy.ops.object.mode_set(mode="OBJECT")
    bpy.ops.object.voxel_remesh()

# 再合并成一个网格
bpy.ops.object.select_all(action="DESELECT")
for ob in parts:
    ob.select_set(True)
bpy.context.view_layer.objects.active = parts[0]
bpy.ops.object.join()
```

这与上文「融合只在单个部件内部用」是同一条原则，只是补上了**不这么做的后果**。

**渲图后必须验证包围盒**，这是唯一可靠的部件存活检查：

```python
bb = [body.matrix_world @ Vector(c) for c in body.bound_box]
print("z[%.3f..%.3f]" % (min(v.z for v in bb), max(v.z for v in bb)))
```

1.70m 的人形，z 下界必须接近 0（脚），上界接近 1.65（头顶）。
**若 z 下界是 1.4 左右，说明只剩头了——腿和躯干被删了。**

### 阶段 3：Multires + 细节 —— 本环境走程序化位移

Multires 只负责**加密度**，不负责改形状。真正的"雕刻"要动顶点。

#### ⚠️ 无头环境的硬限制：笔刷雕刻不可用

Blender 4.5 把笔刷改成了**资产引用**（`AssetWeakReference`）：

```
tool_settings.sculpt.brush        → 只读，headless 下恒为 None
tool_settings.sculpt.brush_asset_reference → 必须有资产库才能赋值
```

无头环境没有资产库，**`bpy.ops.sculpt.brush_stroke()` 走不通**。这不是调用方式问题，是数据层没有画笔可指。

> 历史上 `brush_stroke` 的参数在 4.x 反复改名（`brush` / `use_front` / `flip_normal` 已全部移除，
> `stroke` 结构从"每个点一个 dict"变成"每笔一个 dict、内含 location/pressure/size/is_start/mouse/mouse_event"，
> 首个采样点必须带 `name`）。**即使改对参数也仍然卡在资产引用上**——不要在这上面耗时间。

#### 替代方案：程序化置换（稳、可复现）

用 **Displace modifier + 程序化纹理** 加细节。细节是"长出来"的不是"雕出来的"，形状可控性差，但不会崩。

```python
m = ob.modifiers.new("multires", "MULTIRES")
m.levels = 4          # 视口级别
m.render_levels = 6   # 渲染级别；每级 ×4 面数，基数 1k → 6 级约 4M 面
bpy.ops.object.modifier_apply(modifier="multires")

# ⚠️ NOISE 纹理在 4.5 没有 noise_scale 属性，只有 VORONOI / MUSGRAVE / MARBLE / WOOD / STUCCI 有
tex = bpy.data.textures.new("Pores", type="VORONOI")
tex.noise_scale = 0.9     # 0.003 的网格体上要调到 0.02~0.05
tex.intensity = 1.0
d = ob.modifiers.new("detail", "DISPLACE")
d.texture = tex; d.strength = 0.03; d.mid_level = 0.5
bpy.ops.object.modifier_apply(modifier="detail")
```

**实测数据**（球体 voxel 0.03 + multires 4 级 = 21320 顶点）：
两层置换（VORONOI 0.9/strength 0.03 + MUSGRAVE 0.45/strength 0.05）位移了 **21289 / 21320 = 99.9%** 的顶点。

**堆多层不同频率的置换**模拟皮肤褶皱、肌肉起伏、布料褶皱——低频出体块结构，高频出毛孔。**每层 apply 掉**，不要留在栈上（modifier 顺序会互相影响）。

**局限**：置换只能加"噪声状"细节，做不出眼窝、鼻翼、法令纹这类**有方向的解剖结构**。这部分靠阶段 1 的体块和阶段 2 的融合质量兜。

### 阶段 4：Quadriflow —— 重拓扑成四边面

高模面数高但拓扑全是三角面，**不可用于游戏/引擎**，也不能做 UV 和绑定。Quadriflow 把它重建成规整四边面。

```python
bpy.ops.object.mode_set(mode="OBJECT")       # 同样必须 OBJECT 模式
bpy.ops.object.quadriflow_remesh(target_faces=8000)   # 参数名是 target_faces，不是 3.x 的 target_number_of_faces
```

**实测**：900 面目标 → 实际 **903 面，三角面 0 个**（纯四边面）。

角色拓扑布线规则（Quadriflow 不会自动满足，必须手调）：

| 部位 | 布线要求 |
|---|---|
| 眼周 | 环绕眼裂一圈 5~7 条边，瞳孔正上方一条竖边 |
| 嘴周 | 环绕口裂一圈 8~12 条边，嘴角对称 |
| 关节 | 肘/膝/肩各一圈边环，环上均匀 |
| 腋下/腹股沟 | 允许 5 边形，不要硬凑成 4 |
| 极点 | 头顶/脚底/腋窝必然有多边形，接受它 |

**不要强行全四边面。** 强行拉直会破坏轮廓。游戏引擎对 5~7 边形完全支持。

### 阶段 5：交付 —— Shrinkwrap + 法线烘焙

高模细节无法直接进引擎，烘成法线贴图转移给低模。

```python
low  = bpy.data.objects["Low"]      # 低模（渲染用）
high = bpy.data.objects["High"]     # 高模（细节源，低模必须包住它）
sw = low.modifiers.new("shrink", "SHRINKWRAP")
sw.target, sw.wrap_method = high, "NEAREST_SURFACEPOINT"
sw.offset = 0.001
```

烘焙要求：

- **必须切到 CYCLES 引擎**（EEVEE 不支持法线烘焙，报 `Can only be baked in Cycles`）
- 低模必须有 UV：`bpy.ops.object.mode_set(mode="EDIT"); bpy.ops.uv.smart_project()`
- 必须有目标 Image Texture 并设为 Active，**否则报 `No active image`**——凭空创建的 Image 不会被当作烘焙目标
- 烘焙前 `object.select_all(action='DESELECT')` 再单独选目标

烘完法线**必须亲眼看**：把材质接上 Normal Map，渲一张近景。**看不出高模细节的凹凸就是没烘上**，但网格统计一切正常。

## 非人形资产（道具 / 载具）

**结论：六个阶段里只有 0 和 0.5 需要改写，1/2/3/4/5 全部照搬。但阶段 2 的 voxel size 和阶段 5 的地位必须改。**

实测对象：一把长剑（刃/护手/握柄/剑首四个独立轮廓部件），真实尺寸总长 **1.032 m**、刃尖厚 **7.7 mm**。

### 阶段 0 / 0.5 的改写

| 原（人形） | 非人形替代 |
|---|---|
| 头身比、眼高、肩宽/胯宽/膝高 | 真实参照尺寸（刀的总长、载具的轴距与轮径） |
| 部件清单 Head_Face / Eye_L/R / Hand_L/R / Shoe_L/R | 按**独立轮廓 + 独立材质**拆：刃 / 护手 / 握柄 / 剑首 |
| 「会被动画单独驱动的」拆件标准 | 换成「渲染要分色 / 要单独换材 / 轮廓能分辨」 |
| 视点「人眼水平高度」 | 换成**真实握持高度**（剑柄 z≈1.0m，载具按座舱） |

### 阶段 2 的 voxel size 必须重算（实测，别照抄 0.008）

阶段 0 给的 0.008 是按 1.7 m 人体定的，**搬到道具上会直接毁掉薄特征**：

| voxel | 面数 | 体积变化 | 刃顶半宽 | 刃缘台阶 | 刃尖厚 |
|---|---|---|---|---|---|
| blockout | 366 | — | −14.30 mm | — | 7.7 mm |
| **0.008（角色值）** | 2,322 | −18.3% | **−8.00 mm** | **−1.42 / −1.45 mm** | 8.3 mm |
| 0.004 | 9,198 | −20.7% | −11.5 mm | −0.35 mm | 8.3 mm |
| 0.002 | 35,654 | −21.1% | −14.16 mm | −0.06 mm | 8.3 mm |

三条实测结论：

1. **刃尖单侧蚀掉 3.3 mm、刃缘留 1.4 mm 阶梯缺口**——台阶是能看见的，别只信面数统计。
2. **细化 voxel 不能挽回体积。** 0.008 → −18.3%，0.002 → −21.1%，**反而更差**并多花 15 倍面数。
   体积损失来自锐边/锐角被倒圆，是体素法的**固定代价**（本例约 −21%），不是分辨率不足。
3. **刃尖厚度被永久锁死在 8.3 mm**（原始 7.7 mm），无论 voxel 多细——刀锋做不进体素。
4. 定 voxel 的正确依据是**最薄特征的厚度**，不是主体尺寸：
   `voxel ≤ 最薄特征 / 3`，薄壁件才留得住；1.032 m 的剑配 0.008 是因为它的刀锋只有 7.7 mm。

### 阶段 5 从「质量收尾」升级为「不可省略」

角色的锐边本来就不重要，阶段 5 是锦上添花。**道具不同**：阶段 2 已经把棱线倒圆、体积少了两成，
**只有 Shrinkwrap + 法线烘焙能把锐边找回来**。道具的阶段 5 必须在交付前做，不能当可选项。

### 开口边数：Quadriflow **原样保留**，不是它算出来的（2026-10-05 实测）

在合成探针上（UV 球 160×80 ≈ 12,768 面，正面挖两个眼孔 + 一个嘴孔）实测：

| 开口 | 重拓扑前边数 | 重拓扑后边数 | 变化 |
|---|---|---|---|
| 嘴 | 34 | 35 | +1 |
| 眼.L | 26 | 26 | 0 |
| 眼.R | 26 | 25 | −1 |

**每环变化 ≤1 边**：Quadriflow 既不塌缩也不重排开口。线框复核一致——开口仍是连续闭合环，
主体为均匀四边面，仅开口附近有个别拉伸面。

> 🔑 **别指望从重拓扑里"测出"眼周几圈边。** 开口边数由**开口在表面上的尺寸**决定，
> Quadriflow 只负责原样保留。所以想要「眼周 5~7 边、嘴周 8~12 边」，得**在源网格上
> 就把开口开成那个尺寸**；等跑到阶段 4 是等不出来的。
>
> 另注：上表数字来自**合成探针**（孔径自定：眼 r=0.16 / 嘴 r=0.26），
> 证明的是"重拓扑不改边圈数"这条**规律**，不等于任何具体资产的眼周就是 26 边。

### 阶段 4 Quadriflow：可用，门槛是**输入面数**（2026-10-05 实测更正）

**无头环境下 `bpy.ops.object.quadriflow_remesh` 跑得通真实资产。** 旧结论
「只对基本体有效、真实资产一律 `{'CANCELLED'}`」是**误判，已作废**。

真正的门槛：**输入面数超过约 2.5 万面就被拒**。失败恒为 `t=0.0s` 立即返回——
是**输入校验阶段被拒**，不是"算不动"，也不是"poll 失败"。

实测矩阵（角色 `Character_Body`，先 `Decimate` 再 Quadriflow，无头）：

| 输入面数 | target_faces | 结果 | 耗时 |
|---|---|---|---|
| 3,785 | 2,000 | `{'FINISHED'}` | 0.7 s |
| 8,917 | 8,000 | `{'FINISHED'}` | — |
| 13,623 | 8,000 | `{'FINISHED'}` | 5.2 s |
| **22,979** | **8,000** | **`{'FINISHED'}`** | **5.9 s** |
| 26,556 | 8,000 | `{'CANCELLED'}` | 0.0 s |
| 37,397 | 8,000 | `{'CANCELLED'}` | 0.0 s |
| 97,458（原网格） | 2,000 / 8,000 / 20,000 | 全 `{'CANCELLED'}` | 均 0.0 s |

**`target_faces` 无关**：固定输入 13,623 面时 target 从 2,000 扫到 40,000 **全部成功**
（目标面数比输入还大也能出）；固定 target=8,000 时，输入 22,979 成功、26,556 必挂。

> ⚠️ **旧结论错在哪**：当年「已逐项排除」写的「输入面数（97k 与 2k 都失败）」——
> 那个 **2k 是 `target_faces`，不是输入面数**。**从没在真实几何的低面数区间试过**，
> 漏的正是这一格，于是把"输入过大"误读成了"无头环境不支持"。

**正确用法**：Quadriflow 前先 `Decimate` 把输入压到 ~24K 面以下
（`ratio 0.15` → 22,979 面，实测可跑），再 `quadriflow_remesh(target_faces=8000)`。
22,979 → 8,000 得到 **7,384 面纯四边面（tri=0、ngon=0）、非流形边=0、边界边=0**，
即闭合流形全四边面网格，渲图确认形体完整无破洞。

> 💡 排查口诀：**看到 `{'CANCELLED'}` 且 `t≈0`，先减面重试**，再怀疑环境。
> 顺手用 `bmesh` 只读统计非流形边/边界边，但别急着改网格。

> ⚠️ **别用 `bpy.ops.mesh.delete(type="VERT")` 排查网格问题**：在**全选**状态下
> 它会把**整个网格删空**（实测 97,460 → 0，网格瞬间消失）。诊断阶段只读，不下修改指令。

> ⚠️ 上一版还顺带断言"4.5.14 没有 Retopology 工作区"——本条的结论建立在
> **输入面数**上，GUI 路线有无不影响能否拿到重拓扑输出，不必再受它限制。

## 铁律

**1. 每渲一张，必须自己看。**

以下全部**不算**验证：
- 文件存在且非空
- 顶点/三角面数达标
- 网格能被 glb 正常载入
- 尺寸比例、包围盒、拓扑统计正确

以上全通过而模型是一坨废的情况**真实发生过**（拿 TripoSR 草模冒充精修件交付）。

⚠️ 提问时**不要预设结论**。问"面部五官是否清晰可辨"会得到你想听的；问"这张图里的角色有几根手指"才是真检验。

**2. 目视描述不如像素数据。**

VLM 会漏报也会误报。实测中它两次说"招牌没发光"，而像素实测该处 R-B=+40.5、亮度 210，发光成立。

用像素差值做客观判据。**但先算物体在画面中的真实坐标**：

```python
from bpy_extras.object_utils import world_to_camera_view
v = world_to_camera_view(scene, cam, obj.matrix_world.translation)
px, py = v.x * res_x, (1 - v.y) * res_y
```

我曾在错误坐标上量空气，得出"改灯无效"的错误结论——光其实打对了，是探针在别处。

**3. 全黑检测别只看文件大小。**

全黑 PNG 的 file size **反而偏大**（443KB），压缩后仅 1.5KB。必须缩到 560px 再看体积。

## 常见错误

**1. 跳过阶段 0 直接 blockout。** 比例一定错，改起来是整体返工。

**2. 在 headless 里死磕笔刷雕刻。** 4.5 的资产引用机制堵死了这条路，见阶段 3。**用程序化置换。**

**3. Multires 加完当雕刻做了。** Multires 只加密度，形状没变。不加置换就还是块面。

**4. Voxel Remesh 写 0.02。** 那是场景道具的量级，角色用 0.003。

**5. EEVEE 烘焙。** 报 `Can only be baked in Cycles`。

**6. symmetrize 用 `direction='X'`。** 4.5 要写全 `'POSITIVE_X'`。

**7. 阶段 5 之前不看脸。** 脸型在阶段 2 就定了，等到第 5 阶段发现，等于全白做。

## 目标规格

角色建模开始前**先写下来**，一路传到导出：

| 项 | 值 |
|---|---|
| 真实尺度 | 1 单位 = 1 米，人 1.70 |
| 低模面数 | 游戏 角色 8k~20k 四边面 |
| 高模面数 | 100k~500k（雕刻用） |
| 贴图 | 2048²（移动端）/ 4096²（PC） |
| 格式 | glTF 2.0 |

**AI 生成的网格尺寸经常离谱且不报错**——能加载、能渲染，只是放到场景里小得像粒沙或大得像山。锁定目标尺寸是路线确定时就要做的事，不能等到导出。

## 参考

- `references/api-notes.md` — Blender 4.5 API 改名速查与实测调用签名
- `references/proportions.md` — 人体测量参考表与三视图用法
- **REQUIRED SUB-SKILL:** 用 `bpy-model` 跑基础建模与渲染命令
- **REQUIRED SUB-SKILL:** 定路线与用途用 `planning-3d-asset-pipeline`
- **REQUIRED SUB-SKILL:** 导出交付用 `finalizing-3d-assets`
