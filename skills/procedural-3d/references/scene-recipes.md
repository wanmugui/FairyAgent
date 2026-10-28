# 场景配方清单（3d-scene-quality 全部跑通的那条链）

**这份文档不是笔记，是跑通的配方。** 每一条都对应 `examples/` 下一个能直接跑的脚本，
参数都在同名 JSON 里。跑法：

```bash
/path/to/miniforge3/envs/bpyenv/bin/python3.13 skills/procedural-3d/examples/run_all.py
```

一次跑完全部 11 条用例（层 -1 到层 6），约 2.5 分钟。
文档里写「可一键跑通」而实际跑不通，那就是假的——所以固化物里必须有这条命令。

## 环境（别自己踩）

| 事实 | 值 |
|---|---|
| 主解释器 | `/path/to/miniforge3/envs/bpyenv/bin/python3.13`（**bpy 5.1.0 模块**，`import bpy` 即可，无需 blender 可执行文件） |
| 独立检查 | `/path/to/miniforge3/envs/fairy/bin/python3`（**唯一装了 trimesh 的解释器**） |
| 旧入口 | `/path/to/opt/blender-4.5.14-linux-x64/blender` 仍存在，但**本 skill 的场景用例全部走 bpy 模块** |

⚠️ **独立检查必须换解释器**：`inspect_delivery.py` 用 fair 环境（fairy conda）的 trimesh
独立读回 glb。同一个解释器导出自检 = 自己判自己卷面及格，层 6 的验收会作废。

⚠️ **「装了 trimesh 的那个 python」不是 `/usr/bin/python3`**：实测 `/usr/bin/python3`
与 `bpyenv` 都**没有** trimesh，只有 `envs/fairy` 有（trimesh 5.1.0）。
写错这个路径，独立检查会以 `ModuleNotFoundError` 直接挂掉。

## 七层配方

| 层 | 脚本 | 做什么 | 判据 |
|---|---|---|---|
| -1/2 | `hd2d_diorama.py` | 24 元素 HD-2D 场景：元素清单 → 分层 → 自动取景 | 全部元素贴地且求值出几何 |
| 2 | `build_props_batch.py` | 清单 → N 个 glb + 每件一张预览渲图 | 每件 glb + 渲图都出得来 |
| 3 | `layer3_light_rig.py` | 三灯打光台 + 体积雾 + AgX | **五项自查**：影子方向 / 色相数量 / 层次分离 / 缩略图可读 / 雾 |
| 3b | `layer3b_levers.py` | HDRI 世界、Mist Pass 调色、Glare 辉光、相机移轴 | **四项成对对照** |
| 4 | `layer4_terrain.py` | 脊状分形地形 + 热侵蚀 + ColorRamp 分层材质 | **三项**：山脊锐利 / 边界不呆板 / 缩略图可读 |
| 5 | `layer5_scatter.py` | GN 散布，坡度×高度双遮罩，远景 billboard | 万级实例 + 渲染不慢 |
| 6 | `layer6_delivery.py` + `inspect_delivery.py` | LOD 链 + 碰撞代理 + glTF 导出 + 独立检查 | **独立检查 7 项** |

## 每层最值钱的那一条经验

其余细节看代码注释（都在踩坑现场写的）。这里只留每层最容易被下一个人重犯的一条：

1. **层 3 — 影子判据要看掩码重叠，不是方位角。**
   影子被投影物截断时质心天生有偏，视角对齐后方位角仍差 28.9°。
   更狠的一条：**投影掩码要用探针相机渲，不能拿主相机去做 screen→world 换算**——
   两个不同视角混用，就是连续几轮对不上的根因。
2. **层 3 — 雾判据必须先量机理再定。**
   密度扫描实测：低密度净提亮、高密度净压暗。「雾一定抬起暗部」是错的假设。
   可靠签名是**降饱和**与**建立远近次序**（关雾时 far−near = −1.5，开雾 +7.1）。
3. **层 3b — 合成器 API 在 5.1 全变了。**
   `scene.node_tree` 不存在 → `compositing_node_group`；`Composite`/`MixRGB`/`Math` 全删 →
   `NodeGroupOutput` + `ShaderNodeMix`；`Glare` 参数搬到输入 socket，`Type` 是 MENU 型只能给 `'Fog Glow'`。
   还有最阴的一条：**Glare 的 Threshold 是线性场景值**，设 0.9 时两张图**逐像素完全相同**。
4. **层 4 — 山的高度阈值形成的是等高线，不是逐行交界。**
   量「边界不规则度」要对每一列求「岩石第一次出现的行号」。
   逐行统计岩石占比永远找不到交界。
5. **层 5 — 三个静默到零报错的坑。**
   资产原件 `hide_viewport` → `Collection Info` 拿到**空集合**，渲出一座空山；
   `Input Normal` 是 **VECTOR** 输出，直连 MapRange 静默得 0；
   `to_mesh()` 数不出实例，必须先 Realize Instances。
6. **层 5 — 树线是反向的。**
   低处满树、往上渐稀。写成「高处才长树」= 一堵树林墙长在山顶、近坡全秃。
7. **层 6 — 导出要按物体点名。**
   「选中所有可见物体」会让每个 glb 里装同一份整场景（实测三份文件字节数一模一样）。
   散布**不要 realize 成实几何**——每个实例变独立几何体，那不叫实例化；点单独导成 json。

## 一条通用铁律

**这一串里最贵的错误，没有一个是被报错抓到的。**

空山、逐像素相同的辉光、静默得 0 的遮罩、字节数相同的三个 glb——
它们全部**不报错、退出码为 0、还能渲出一张图**。
全是被「看图」和「换一个库独立复算」抓出来的：

- 看图（VLM 复核渲染结果）抓到了：树长在山顶、针状尖峰、树冠悬空、billboard 像破面片；
- 独立检查（trimesh 而不是 bpy）抓到了：LOD0 超预算 50%、「可实例化」判错对象。

所以两条规矩：**渲完必须自己看**；**判据的量纲和被测对象要对得上**
（「万级 instance」量的是实例数不是顶点数；「可实例化」说的是资产库+放置点不是场景 glb 的 node/mesh 比）。

## 相关文档

- `hd2d-recipes.md` —— HD-2D 元素清单 / 锚点 / 分层 / 自动取景 / 空气透视
- `gn-recipes.md` —— 拱窗、栏杆阵列、随机草地、屋顶等可复制片段
- `data-driven.md` —— JSON schema 设计、机位标定
- `hdri.md` —— Poly Haven HDRI 取法（必须走代理；结构是 `hdri→1k→hdr→url` 四层；下载必须带 `-L`）
