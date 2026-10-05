---
name: generating-3d-models
description: Use when turning approved three-view references or a concept image into a 3D mesh via the Tripo API/CLI **or the local offline TripoSR path**, including rig and animation. 触发场景：「三视图转模型」「生成 3D 模型」「Tripo」「出一张模型」「本机跑 3D」「离线建模」「绑定骨骼」「让它动起来」时的建模步骤。
tags:
  - 3d
  - model
  - mesh
  - generation
triggers:
  - 三视图转模型
  - 生成 3D 模型
  - 出一张模型
  - 本机跑 3D
  - 离线建模
  - 绑定骨骼
priority: 66
---

# 生成 3D 模型

**有两条路，先选路再动手**：

| | 云端 Tripo（本 skill 上半部分） | 本机 TripoSR（本地路径） |
| --- | --- | --- |
| 要 key / 联网 | 要 | **不要** |
| 计费 | 按量 | **免费** |
| 多视图利用 | 好（有专门端点） | 只吃单图，三视图需自己挑一张喂 |
| 细节 / PBR | 好 | 糙，只有顶点色 |
| 速度 | 快 | CPU 约 27 s/个（resolution=256，实测） |
| 适合 | 正式交付 | 草模、批量试、离线环境 |

**先用本机跑通形体，确认要什么再用云端出精修件**，比一上来就烧额度划算。

## 云端：Tripo（手动开关，默认关闭）

> **默认不走这里。** 云端已降为手动开关：只有主人明确要求"出精修件 / 在乎质量 / 不在乎额度"
> 时才开。命令前缀 `TRIPO_CLOUD=1`，凭证保留不删。
> 本机 TripoSR 权重已就绪（`workspace/triposr`，1.7 GB），离线可跑，不按张数花钱。


**有三视图就必须走多视图端点。** 用单图端点等于让模型脑补背面，三视图就白做了。

## 本机环境事实

```bash
export PATH="$HOME/.local/bin:$PATH"   # tripo 装在用户目录，不在默认 PATH
tripo --version                        # v0.5.1
tripo doctor                           # 环境自检，未登录时 api key 项为 false
```

| 项 | 事实 |
| --- | --- |
| 安装位置 | `/home/user/.local/bin/tripo`（`/usr/local` 无写权限，回退到用户目录） |
| 密钥格式 | `tsk_...` 开头 |
| 区域 | `cn`（国内）或 `ov`（海外），**影响计费与可用性** |
| 登录 | `tripo login --key tsk_... --region cn`（非交互必须给 region） |
| 输出目录 | 走外接盘，见下方「产物落盘」 |

## 选哪个端点

| 手上有什么 | 用哪个 | 说明 |
| --- | --- | --- |
| **2~4 张正交视图** | `multiview-to-model` | **首选**。视角信息被显式使用 |
| 单张概念图 | `image-to-model` | 背面靠模型脑补，一致性差 |
| 单张图，要先出视图 | `image-to-multiview` | 出 4 视图正交表，再喂回上面那条 |
| 只有文字 | `text-to-model` | 形体完全靠描述，控制力最弱 |

**链路推荐**：概念图 → `image-to-multiview` → `multiview-to-model`。比直接单图建模一致性高得多。

## 调用

```bash
export PATH="$HOME/.local/bin:$PATH"

# 单图 → 4 视图正交表
tripo generate image-to-multiview concept.png -o <输出目录> --json --no-open

# 2~4 视图 → 3D 模型（带 PBR 贴图）
tripo generate multiview-to-model front.png side.png back.png \
  -o <输出目录> --json --no-open \
  -p pbr=true -p texture=true -p texture_quality=detailed
```

`-p key=value` 可重复，用于传 API 额外参数（`texture`、`pbr`、`texture_quality` 等）。

其它能力：`tripo generate text-to-image --pose t_pose` 出 T-pose 模板图（角色三视图的姿势基准）；`tripo anim` 绑骨与动画；`tripo mesh` 网格操作；`tripo generate image-to-splat` 出高斯泼溅（场景渲染用）。

## 产物落盘

**大文件一律落外接盘**（实测 143 MB/s 写 / 168 MB/s 读）：

```
/media/harry/SHENSHENG/Fairy/assets/3d/<资产名>/
```

内置盘只放 md 与 json。生成前先确认目标目录存在，Tripo 不会自动建深层目录。

## 验收门

- [ ] 用的是 `multiview-to-model` 而非 `image-to-model`（若有视图）
- [ ] 视图数量 2~4 张，角度覆盖正/侧/背
- [ ] 角色为 T-pose 或 A-pose
- [ ] 输出含 PBR 贴图而非仅 albedo
- [ ] 真实尺度已确认（1 单位 = 1 米）
- [ ] 产物已下载到本地，且实际打开看过

模型生成完**不等于**资产可用——继续 **REQUIRED SUB-SKILL:** 用 `finalizing-3d-assets` 做资产化。

---

# 本地路径：本机 TripoSR

图 → glb/obj，不联网、不花钱、图不出本机。

```bash
cd scripts

# 最简：图 → glb
python3 local_triposr.py --image cat.png -o out/cat

# 交付推荐：去碎片 + 减面 + 平滑
python3 local_triposr.py --image cat.png -o out/cat \
    --keep-largest --target-faces 20000 --smooth 3 --format both

# 从三视图 sheet 的指定块建
python3 local_triposr.py --image out/char_sheet.png --view front -o out/char
```

### ⚠️ 永远别把三视图 sheet 整张喂进来

`--view` 不是可选优化，是**必须**。整张 sheet 喂进去，模型会把它当成「一个横向摊开的扁平物体」来重建，出来的是压扁的纸板。

实测（同一角色，A=只喂正面单图，B=喂整张三视图拼接图，同参数）：

| 指标 | A 单图 | B 整张 sheet |
| --- | --- | --- |
| 轮廓 IoU（正/侧/背均值） | **0.525** | 0.159 |
| 背面 IoU | **0.746** | 0.130 |
| 体积 / 包围盒（饱满度） | **0.088** | 0.058 |
| depth / width（越立体越大） | **0.543** | 0.440 |
| extents | `[0.61, 1.00, 0.33]` 站立 | `[1.01, 0.50, 0.22]` **宽大于高=躺着** |

B 输在每一项上，而且背面塌得最惨（0.746 → 0.130）——恰恰是三视图本该改善的那一面。

**推论：三视图对「单图入口」模型不仅没用，是负作用。** 它只在有**多视图入口**的模型（云端 `multiview-to-model` 端点，或 SF3D 这类真有 `n_input_views` 路径的模型）上才有价值，而且必须**按视图分别传参**，不能拼成一张图。

sheet 由 `3d-asset-pipeline/scripts/threeview.py` 生成，`to3d.py --view` 从中裁出单块。中间产物，跨阶段用绝对路径当 `--image` 传进来即可。

# 强制离线（只用本地缓存，不碰网络）
HF_HUB_OFFLINE=1 python3 local_triposr.py --image cat.png -o out/cat
```

## 常用参数

| 参数 | 默认 | 说明 |
| --- | --- | --- |
| `--resolution` | 256 | 网格分辨率，越大越慢 |
| `--keep-largest` | 关 | 去碎片，只保留最大连通体 |
| `--target-faces` | 0（不減） | 减面到约 N 面 |
| `--smooth` | 0 | Taubin 平滑迭代次数 |
| `--orient` | `stand` | `stand`=绕Z转90°立起来；`y_up`=自动把最长轴立成Y；`raw`=不转 |
| `--format` | `glb` | `glb` / `obj` / `both` |
| `--offline` | 关 | 等价于设 `HF_HUB_OFFLINE=1` + `TRANSFORMERS_OFFLINE=1` |
| `--no-remove-bg` | 关 | 不抠背景（**一般别用**，见下） |

## 本机依赖

| 项 | 值 |
| --- | --- |
| TripoSR 代码 | `/home/user/Fairy/workspace/triposr`（`--triposr` 可改） |
| 权重 | `workspace/triposr/weights/model.ckpt` + `config.yaml` |
| 骨干网络 | HF `facebook/dino-vitb16`，缓存在 `~/.cache/huggingface/hub/models--facebook--dino-vitb16` |
| torch | 2.14.1+**cpu** |
| trimesh / scikit-image / rembg / scipy | 5.1.0 / 0.26.0 / 2.0.85 / 1.18.1 |
| 离线验证 | `HF_HUB_OFFLINE=1` 实测可跑（exit 0，产出 glb），说明 HF 缓存完整 |

**`fast_simplification` 0.2.0 在本机是坏的**——它的 Python wrapper 会给 C++ 扩展传它不认识的
`agg` 参数，直接 `TypeError`。所以减面走 `trimesh.simplify_quadric_decimation`，别直接调它。

## 本地三处适配（都没改 TripoSR 源码）

1. TripoSR 的 `run.py` 顶层 `import moderngl`，headless 下直接 ImportError
   → 脚本直接调 `TSR` API，绕开 `run.py`。
2. 权重是旧版 transformers 键名，新版不认 → 加载时重映射
   （`attention.attention.query` → `attention.q_proj` 等）。
3. 原始输出是**躺着**的（长轴在 X 上），必须绕 Z 转 90° 才直立
   → `--orient stand` 默认开。

## 坑

| 现象 | 原因 / 怎么办 |
| --- | --- |
| 网格带一圈「托盘」 | 没抠背景，白底被当成几何。默认已开抠图，别加 `--no-remove-bg` |
| 网格是躺着 / 侧倒的 | 用了 `--orient raw`；默认 `stand` 会立起来 |
| 减面后报「不水密」 | **正常**。见下 |
| rembg 失败退回原图 | 首次运行要下模型，会慢；失败会打印原因并退回，不中断 |
| `找不到 TripoSR` | `--triposr /path/to/triposr` |

## 减面与水密性（实测，别抱幻想）

TripoSR 原始输出**完全水密**（resolution=256：46096 面、69144 条边全部恰好被 2 个面共用）。
但**只要减面就会破水密，且和减多少无关**：

| `--target-faces` | 结果 |
| --- | --- |
| 40000 | 2 条非流形边 |
| 30000 | 5 条 |
| 25000 | 4 条 |
| 20000 | 3 条 |
| 10000 | 4 条 |

破坏形式是**极少数边被 4 个面共用**（非流形边），**不是洞**。
要彻底修需要 manifold 重建，本机没装 `manifold3d` / `pymeshfix`，所以脚本**只如实报数**。

**步骤顺序也有讲究**：先平滑再减面会得到 9 条非流形边，先减面再平滑只有 3 条。
所以脚本固定按 **去碎片 → 减面 → 平滑** 的顺序跑。

> 曾经试过在减面后「去重面 + fill_holes」抢救，**实测是帮倒忙已删掉**：
> 去重面凭空多出 6 条边界边（洞），`fill_holes` 又把非流形边从 3 推到 9。
> 网格清清爽爽的时候别乱动拓扑。

要严格水密就别用 `--target-faces`；引擎真报非流形错误再想办法。

## 回归测试

```bash
python3 test_local_post.py    # UNIT_ALL_PASS：后处理三个函数（合成网格，秒级）
```

不依赖 TripoSR 权重，专门盖住「看起来能跑但其实没干活」的情况：
真实跑图常常只有 1 块碎片，`keep_largest` 走不到；`decimate` 会把顶点色弄丢。

## 常见错误

**1. 有三视图却用 `image-to-model`。** 最常见也最浪费——多视图端点的成本换来的一致性直接丢掉。

**2. `tripo` 报 command not found。** 它在 `~/.local/bin`，不在默认 PATH。每条命令前都要 `export PATH="$HOME/.local/bin:$PATH"`，或把它加进 shell 配置。

**3. 忘了 `--no-open`。** 非交互环境下 CLI 会尝试开浏览器导致挂住。

**4. 区域选错。** `cn` 和 `ov` 是不同平台、不同计费。key 和区域必须配套。

**5. 静默吞掉错误输出。** 认证失败、额度不足都会明确报错，**不要加 `2>/dev/null`**——否则失败原因全丢，只剩一句"没输出"。这是本项目已犯过的错。

**6. 拿生成结果直接交付。** 网格必须过 `finalizing-3d-assets` 的验收门（UV、贴图五件套、尺度、命名）。

## 参考

- **REQUIRED SUB-SKILL:** 上游三视图用 `authoring-three-view-references`
- **REQUIRED SUB-SKILL:** 资产化用 `finalizing-3d-assets`
- **REQUIRED SUB-SKILL:** 要动画用 `animating-3d-assets`
- **REQUIRED SUB-SKILL:** 路线判定用 `planning-3d-asset-pipeline`
