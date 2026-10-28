---
name: 3d-asset-pipeline
description: Use when 需要把一张图或一段文字变成可交付的 3D 素材、游戏场景或短视频：单件道具/角色走 图片→三视图→网格(glb/obj)，场景·地景走 procedural-3d 的元素清单+分层拼装，图生视频出 mp4。网格环节走 Blender 真实建模（按参考图比例用基本体搭建），图像推断路线已下线。触发场景：「把图转成 3D」「出三视图再建模」「图生视频」「整条素材链跑一遍」「3D 素材流水线」「做个游戏场景」「搭个 HD-2D 场景」「做片地形和树林」「场景元素批量出图」。
tags:
  - 3d
  - pipeline
  - asset
  - end-to-end
triggers:
  - 把图转成 3D
  - 出三视图再建模
  - 图生视频
  - 整条素材链跑一遍
  - 3D 素材流水线
  - 3d asset pipeline
  - 做个游戏场景
  - 搭个 HD-2D 场景
  - 做片地形和树林
  - 场景元素批量出图
priority: 60
---

# 3D 素材流水线

图片 → 三视图 → 3D 网格 → 短视频。四个环节各一个脚本，可以单独跑也可以串起来跑。

## 什么时候用

要**可交付的 3D 资产**或**短视频**，而不是只想看看效果。
只想生成/编辑一张图，用 `image_generate` 工具就够了，别开这条链。

## 决策：先定路线，别直接开跑

### 第一步：按**对象类型**分流（决定走哪个技能）

这张表是主路由。**先认对象，再看输入**——三条路的建模手法完全不同，走错了返工成本很高。

| 你要做的东西 | 走哪个技能 | 关键差别 |
|---|---|---|
| **角色**（人形、动物、绑定骨骼、蒙皮、动画） | `sculpting-character-pipeline` → `generating-3d-models` | 要骨骼、要蒙皮、要保体积；表面细节靠雕刻/程序化置换 |
| **单件道具**（树、岩石、建筑构件、武器、载具） | `generating-3d-models` | 不需要骨骼，但要**可实例化**、面数按近/中/远分级；用基本体 + 程序化散布 |
| **场景 / 地景**（地形、成片植被、水面、大气氛围） | `procedural-3d` | **不靠建模精度，靠散射/雾/光**。低多边形 + 好光照即可成立，反之不然 |

> ⚠️ **本表是「路由入口」，但运行时召回看的是本 skill 的 `description` / `triggers`，不是正文。**
> 所以「场景」一词必须同时写进 frontmatter（2026-10-16 已补：description 提到游戏场景/HD-2D/地形树林，
> triggers 加了 4 条场景说法），否则表格写得再全，主会话也召不回这个大类。

### 场景这条支现在能做什么、不能做什么

`procedural-3d` 的**整条场景链**（层 -1 ~ 层 6）已跑通，一键重跑全部 11 条用例：

```bash
/path/to/miniforge3/envs/bpyenv/bin/python3.13 skills/procedural-3d/examples/run_all.py
```

| 能做（已有可运行用例，路径均相对 `skills/procedural-3d/`） | 还做不到（别承诺） |
|---|---|
| 元素清单 → 分层拼装 → 自动取景 → 渲三张验收图（`procedural-3d/examples/hd2d_diorama.py`） | 元素边缘倒角、UV 平铺密度、360° 死角检查（层 2 剩余项） |
| 三灯打光台 + 体积雾 + AgX，五项自查全过（`procedural-3d/examples/layer3_light_rig.py`） | 从 HDRI **反解**太阳方向与强度（现在 HDRI 只当环境光） |
| 惊人杠杆：HDRI 世界 / Mist Pass 调色 / Glare 辉光 / 相机移轴（`procedural-3d/examples/layer3b_levers.py`） | 树与石头的碰撞代理（层 6 只做了地形那一半） |
| 地形起伏 + 热侵蚀 + 按高度分层材质（`procedural-3d/examples/layer4_terrain.py`） | 真引擎（Godot/Unity）内加载验证——本机无引擎，只做到 glTF 层 |
| GN `Distribute Points` 植被散布，坡度×高度双遮罩，万级实例 + 远景 billboard（`procedural-3d/examples/layer5_scatter.py`） | 批量三视图要联网调外部图像接口（`threeview.py --list` 已支持清单模式） |
| LOD 链 + 碰撞代理 + glTF 导出，并用 trimesh **独立检查**（`procedural-3d/examples/layer6_delivery.py` + `inspect_delivery.py`） | 放置点的旋转/缩放尚未作为数据导出（现在只导位置） |

⚠️ `inspect_delivery.py` 必须用 `/path/to/miniforge3/envs/fairy/bin/python3` 跑
（**唯一装了 trimesh 的解释器**）：它是导出方之外的独立检查，
用导出方自己的解释器自检等于自己判自己卷面及格。

各层配方、判据与每层最值钱的那条踩坑经验：`procedural-3d/references/scene-recipes.md`；
HD-2D 方法论与 5 个实测踩坑：`procedural-3d/references/hd2d-recipes.md`。
**起步就用上面的用例，不要从零自建。**

**别把角色当场景做**（用错会去做骨骼、白费功夫），**也别把场景当角色做**
（会对着地形调蒙皮权重，毫无意义）。跨类型的例子：

| 需求 | 正确做法 |
|---|---|
| 一片树林 | 场景路线：一棵树按道具做出来 → 用 GN `Distribute Points on Faces` 按坡度/高度撒一片 |
| 一个手持武器 | 道具路线 → 导出后挂到角色骨骼上 |
| 一张地图 | 场景路线；角色只是地图里的一个实例 |

### 第二步：按**输入**定链路（同一类型内怎么跑）

| 你的输入 | 走哪条 |
|---|---|
| 文字描述，没图 | `threeview.py --desc "..."` 直接出三视图 |
| 本地有一张角色图 | 先看「参考图的坑」一节，多数情况要靠 VLM 描述中转 |
| 有公网可访问的图 URL | `threeview.py --desc "..." --ref-url https://...` 锁角色 |

> `threeview.py` 的 `--style` 默认是 `anime game character, cel shaded, full body,
> no background props`——**出树/岩石/建筑必须显式覆盖**，否则出图全是人形。
> 且它**只支持单个 `--desc`**，没有批量参数；场景要出多种元素时得循环调用。

## 四个脚本

都在 `scripts/`，各自独立可执行。先 `cd scripts/`。

### 1. 出三视图（一次出图 → 三等分裁切）

```bash
python3 threeview.py --desc "Hoshimi Miyabi, very dark blue-black hair with thick bangs, side braids and a high ponytail, red-orange eyes, tall pointed animal ears, teal and deep cyan jacket over a white inner shirt, dark navy shorts, black arm guards, holding a large sword" -o out/miyabi
```

> ⚠️ 历史坑：本行曾写成 `long wavy blonde hair, red eyes, white+crimson jacket`，
> 那是错的——星见雅是**深蓝黑发、红橙瞳、蓝绿白配色**，红色只出现在眼睛和少量装饰，不是服装主色。
> 照抄错误描述会稳定产出「金发红瞳白红制服」的漂移角色，negative prompt 拦不住。
> 参考图真实特征先用 VLM 提取，再逐字写进 `--desc` 正文（见下方「参考图的坑」）。

出 `out/miyabi_sheet.png`（三人并排原图）+ `_front/_side/_back.png` + `_manifest.json`。

- 默认 `--ratio 16:9`：实测面板最高最完整。**别用 21:9**，面板只有 416px 高，纵向不够。
- **不要无脑三等分裁**。脚本用 `detect_bands()` 按实际人形区间裁，人形数不是 3 就自动重试
  （`--attempts`，默认 3）。实测 5 次里只有 3 次合格：模型会画成 6 个人，或者把中间朝向画错。
- 三等分会切掉人物头脚：实测三个人形实际落在 `(102,365) (538,776) (944,1153)`，
  而三等分是 `(0,426) (426,853) (853,1280)`。
- **朝向对不对仍要人眼/VLM 复核**。脚本只保证切对人形区间，保证不了朝向——
  实测有 3 个人形齐全但中间那个画成正面的情况。manifest 里 `side_narrower_than_front`
  字段是个弱提示（侧面投影通常更窄），不是保证。
- 提示词里那句「逐个指定朝向」和「no text / no watermark」是实测踩出来的，别删。

回归测试：`python3 scripts/test_detect_bands.py`（7 张已知图，锁住 accept/reject 行为）。

#### 批量：一份元素清单出多个场景元素（2026-10-16 新增）

场景元素（树/岩/屋/草/灯…）要成批出图，就用 `--list`，`-o` 当**输出目录**用：

```bash
python3 scripts/threeview.py --list scripts/elements-example.json -o out/scene --sleep 6
python3 scripts/threeview.py --list scripts/elements-example.json -o out/scene --dry-run   # 只看计划不调接口
python3 scripts/threeview.py --list scripts/elements-example.json -o out/scene --only el_rock_boulder   # 补跑
```

清单格式（示例见 `scripts/elements-example.json`）：

```json
{ "defaults": {"ratio": "16:9", "attempts": 3},
  "styles":    {"tree": "...", "rock": "..."},
  "elements":  [{"name": "el_tree_oak", "kind": "tree", "desc": "A medium-sized deciduous oak tree ..."}] }
```

**三件事必须知道，都是实测踩出来的**：

1. **`kind` 决定用哪套措辞和哪三个视图**：`character` → front/side/back + 人形措辞；
   其余 → **正交** front/side/top + 物件措辞。默认那套措辞里写着
   「full-body figures」「the BACK of her head」，拿它出树会稳定跑偏。
2. **style 优先级**：元素自带 `style` > 清单 `styles[kind]` > CLI `--style`。
   每个元素实际用了哪条会打进日志与 manifest（`style_source`）。
   **清单里没给对应 kind 的 style 时会显式打出「未按 kind 覆盖!」**——那就是废图前兆。
3. **提示词有 1500 字符硬上限**（接口返回 `status_code=2013`，文档里没写）。
   脚本会在 `resolve_plan` 阶段就量好长度并告警，超了直接中止**不再重试**
   （拿同一段超长文本重试只是再被拒一次）。

**「切出 3 段」不等于「就是 3 个正确视角」**，脚本会用两个量给出弱提示，务必人眼/VLM 复核：

- `side_narrower_than_front`：侧面投影通常更窄，不窄就多半没照排；
- `top_view_height_ratio`：真顶视图是俯视，投影高度必然明显更矮。
  **实测 6 个场景元素全部 ≈1.0**（第三格和第一格一样高）——模型经常给第三个立面而不是顶视图。
  ≥0.7 就在 manifest 里告警。这条不是推测，是按墨迹高度量出来的。

已知限制：细长物体（路灯杆）容易只画 1~2 个，或被拆成多段误判成 3 个物体；
这类失败 `error_kind` 会记成 `layout_bands_mismatch`，靠调 attempts 碰运气。

回归测试：`python3 scripts/test_batch_plan.py`（42 项，纯离线，不调接口）。

### 2. 单图 → 3D 网格（Blender 真实建模）

> ⚠️ **本机图像推断路线已下线。** `to3d.py` / `local_triposr.py` 依赖的权重文件不存在，
> 依赖它们的步骤一律不要尝试。云端生成 API 同样不可用（需 key，且出图不可控、成本不可预知）。
>
> 替代路线是**按参考图比例在 Blender 里真实建模**——可控、可复现、可直接交付。

三视图只用来**定比例关系和服装结构**，不要拿它量尺寸（扩散模型出的三视图不保证正交投影下几何闭合）。

```bash
# 以参考脚本为起点，按你的参考图调整比例后建模
python3 skills/sculpting-character-pipeline/references/blockout-character.py
```

改哪里：脚本里的 `H_CHEST` / `H_WAIST` / `H_HIP` / `H_KNEE` 等高度常量决定比例，
`VIEWS` 三个机位渲正/侧/3-4 视角。

- **实体验证**：30+ 基本体搭出 1.70m 人形，131K 顶点，包围盒 `z[0.004..1.651]`（脚到头顶完整）；
  像素检测到颈部收窄 70px、腰部 108px，三视角均确认人形可辨。
- **建模前先读** `skills/generating-3d-models/SKILL.md`，里面记了两个会静默毁掉模型的坑：
  ① voxel remesh 会**无声删除**不连通的体块（实测一次只剩一个头，不报错）；
  ② 无头环境笔刷雕刻 `brush` 是 read-only，只能用程序化置换。
- **渲完必须看图**，并用包围盒验证部件存活。文件非空、顶点数达标**都不算验证**。

### 3. 图 → 视频（mp4）

```bash
python3 to_video.py --image out/miyabi_front.png -o out/miyabi.mp4 \
  --prompt "she walks forward slowly, coat and hair fluttering, static camera"
```

- 不给 `--image` 就是文生视频。
- `--resolution` 只收 `512P / 768P / 1080P`。默认 768P。
- 首帧脚本会自动包成 `data:image/...;base64,`。**裸 base64 会被拒**（2013 invalid image url）。
- 首帧有两条硬性格式约束，`to_data_url()` 已经自动处理，你不用手动改：
  - **短边 ≥ 300px**，否则报 `2013 ... minimum pixel on the short side of the image: 300px`。
    三视图裁出来的侧面常常只有 130px 宽，必踩。
  - **长宽比在 0.5~2 之间**，否则报 `2013 ... aspect ratio not between 0.5 and 2`。
    脚本的做法是给短边补边（复制边缘像素），保住完整主体，不拉伸。
- **`1026 input new_sensitive` 是会飘的，别当 permanent ban**。同一张图当天先被拦、
  过一会儿同样的 key 再提交就过了。**遇到就重试**，重试比换图更有效。
  诊断技巧：内容审核判决出得很早——轮询到 `Processing` 就是过了（后面只是渲染）；
  很快返回 `Fail` + 1026 才是被拦。所以别干等渲染完，判过审就能走。

### 4. 整链串跑

```bash
cd scripts
python3 threeview.py --desc "$DESC" -o out/char
python3 ../sculpting-character-pipeline/references/blockout-character.py   # Blender 真实建模
python3 to_video.py --image out/char_front.png -o out/char.mp4 --prompt "$MOTION"
```

产物全在 `out/` 下，命名共享同一个前缀，方便串。

## 参考图的坑（2026-10-02 实测）

`subject_reference` **只收公网 URL**。裸 base64 和 `data:image/...;base64,` 前缀都会被
SSRF 校验拒掉：

```
status_code=1000  disallowed image url: localhost or private address not allowed
```

和图片体积无关（512px 缩图同样被拒）。所以本地图片想当参考图，得先传公网图床。
没有图床时的替代做法：用 VLM 读这张图拿到外观描述，再把描述喂给 `--desc`——
一致性不如锁参考图，但链路能跑通。

## API 环境的坑（2026-10-02 实测）

全部在 `scripts/mmclient.py` 里处理了，你不用操心，但要知道：

- 主机是 `api.minimaxi.com`，**不是** `api.minimax.io`。
- 密钥读 `config/local_secrets.json`，和 agent 自己用的是同一把。
  仓库根目录那个 `MINIMAX_KEY_TXT.txt` 是两行格式且**已失效**（`status_code=2049 invalid api key`）。
- **HTTP 状态码永远是 200**，鉴权失败、业务失败都是 200 + `base_resp.status_code != 0`。
  只看 HTTP 码会以为一切正常。
- 出图 `response_format` 收 `"base64"`（不是 `"b64_json"`），图在 `data.image_base64`。
- 本机访问外网要走代理：`export HTTPS_PROXY=http://127.0.0.1:7897`。

**不要打印、记录或外传 API key。** `load_key()` 只把它放进内存。

## 三视图为什么不用「三次分别出图」

见 [references/threeview-decision.md](references/threeview-decision.md)。一句话：
三次独立采样角色会漂，一次出图省 2/3 调用次数，实测也更稳。

## 排错

| 现象 | 原因 |
|---|---|
| `status_code=2049 invalid api key` | 用了 `MINIMAX_KEY_TXT.txt`；应读 `config/local_secrets.json` |
| `status_code=1000 disallowed image url` | 参考图不是公网 URL（`subject_reference` 只收公网 URL） |
| `status_code=2013 ... first_frame_image: invalid image url` | 首帧没包成 data URL，用 `to_data_url()` |
| `status_code=1026 input new_sensitive` | 审核飘了，**直接重试**（同图同 key 也可能就过） |
| `2013 ... short side ... 300px` | 首帧太小，`to_data_url()` 会自动放大，一般不用管 |
| `2013 ... aspect ratio not between 0.5 and 2` | 首帧太瘦/太扁，`to_data_url()` 会自动补边 |
| 三视图缺背面 / 画成 6 个 | 正常，靠 `--attempts` 自动重试；5 次里约 3 次合格 |
| 三视图有字 | 少了 "no text, no letters..." 那段 |
| 网格是躺着的 | 用了 `--orient raw`；默认 `stand` 会立起来 |
| 网格带一圈托盘 | 没抠背景，加 `--remove-bg`（默认已开） |
| `模型只剩一个头` | voxel remesh 删掉了不连通的体块，改成逐件 remesh + join，见 `generating-3d-models` 阶段 2 |
| `找不到 4.5.14` | 路径应为 `/path/to/Fairy/.tools/blender/`；若确实缺失，改用 bpyenv 的 5.1.0，结论不变 |
| 视频一直 Processing | 正常，6 秒 768P 通常要几分钟；`--interval` 别调到 5 秒以下 |
