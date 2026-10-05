---
name: 3d-asset-pipeline
description: Use when 需要把一张图或一段文字变成可交付的 3D 素材或短视频：图片→三视图→网格(glb/obj)→图生视频(mp4) 全链路，用本机 TripoSR + MiniMax 出图/视频，可离线跑、不按张数花钱。触发场景：「把图转成 3D」「出三视图再建模」「图生视频」「整条素材链跑一遍」「3D 素材流水线」。
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
priority: 60
---

# 3D 素材流水线

图片 → 三视图 → 3D 网格 → 短视频。四个环节各一个脚本，可以单独跑也可以串起来跑。

## 什么时候用

要**可交付的 3D 资产**或**短视频**，而不是只想看看效果。
只想生成/编辑一张图，用 `image_generate` 工具就够了，别开这条链。

## 决策：先定路线，别直接开跑

| 你的输入 | 走哪条 |
|---|---|
| 文字描述，没图 | `threeview.py --desc "..."` 直接出三视图 |
| 本地有一张角色图 | 先看「参考图的坑」一节，多数情况要靠 VLM 描述中转 |
| 有公网可访问的图 URL | `threeview.py --desc "..." --ref-url https://...` 锁角色 |

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

回归测试：`python3 scripts/test_detect_bands.py`（4 张已知图，锁住 accept/reject 行为）。

### 2. 单图 → 3D 网格（本机 TripoSR，CPU，不花钱）

```bash
python3 to3d.py --image out/miyabi_front.png -o out/miyabi
```

- **必须抠背景**，默认 `--remove-bg` 开。不抠的话白底会被重建成一圈"托盘"状多余几何。
- 抠完自动裁紧主体再补成正方形，TripoSR 对输入构图敏感，别丢一张空白很多的图给它。
- **导出的网格默认是躺着的**：TripoSR 原始输出长轴在 X 上（实测 ext≈`[0.98, 0.58, 0.30]`）。
  脚本默认 `--orient stand` 绕 Z 转 90° 立起来（实测四种旋转里只有这个方向得到直立人形）。
  `--orient y_up` 是通用兜底（自动把最长轴立成 Y），`--orient raw` 保留躺姿。
- 吃 `out/miyabi_sheet.png` 时加 `--view front|side|back` 自动裁块。
- CPU 上 resolution=256 够用；要更细就调大，会明显变慢。
- **要后处理（去碎片 / 减面 / 平滑）就改用** `skills/generating-3d-models/scripts/local_triposr.py`
  ——那是本机建模的**规范实现**，带网格体检报告和回归测试。本目录的 `to3d.py` 是链路里的轻量版，
  两者在抠图和导出朝向这两个关键行为上保持一致。

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
python3 to3d.py     --image out/char_front.png -o out/char
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
| `找不到 TripoSR` | 用 `--triposr /path/to/triposr` 指定 |
| 视频一直 Processing | 正常，6 秒 768P 通常要几分钟；`--interval` 别调到 5 秒以下 |
