---
name: video-summary
description: 把视频变成带时间戳的结构化摘要素材并写成总结。先用平台字幕（最准且零成本），没有字幕才用本地 STT；画面内容用本地 OCR 读屏上文字，需要真正视觉理解时再对该帧调用 image_vqa。适合"总结这个视频""这个视频讲了什么""帮我看完写个摘要""提取视频要点"等请求。
metadata:
  short-description: "Video digest + summary from subtitles, local STT and image_vqa"
  tags:
    - video
    - summary
    - transcript
    - stt
    - ocr
    - image_vqa
  triggers:
    - 总结视频
    - 视频总结
    - 视频摘要
    - 这个视频讲了什么
    - 提取视频要点
    - 看视频
    - 字幕
    - 转写
    - video summary
    - summarize this video
  priority: 70
---

# 视频总结

把视频变成"可被信任的摘要，并标注它建立在什么证据上"。

## 这个 skill 不做什么

**它不写摘要。** 摘要是你（Agent 的模型）的活。脚本只负责把视频压缩成一份**最小的可信素材包**：

```
transcript   带时间戳的文本（字幕优先，其次本地 STT）
keyframes    有界的画面采样，每帧带本地 OCR 出的屏上文字
outline      粗粒度时间分块，让模型能谈结构
```

为什么不直接在脚本里调一次大模型写摘要？因为你已经是一个模型，读一份结构化素材比再走一次 API 既便宜又更可控——你能追问、能只总结某一段、能在素材不足时说出来。

## 流程

### 第 1 步：拿到素材

```powershell
$py = '.tools\venv\Scripts\python.exe'
& $py skills\video-summary\scripts\video_digest.py "视频URL或本地路径" --out workspace\result\video-summary\<名字>
```

输入可以是 URL、视频文件、音频文件或字幕文件。常用参数：

| 参数 | 用途 |
| --- | --- |
| `--lang zh-Hans,zh-CN,zh,en` | 字幕语言优先级 |
| `--stt auto\|sherpa\|whisper` | 无字幕时的识别后端，默认 auto |
| `--interval 30` | 关键帧间隔秒数 |
| `--max-frames 40` | 关键帧上限 |
| `--no-frames` | 纯文本总结，跳过画面 |

产物在 `--out` 目录：`digest.json`（结构化，给程序用）和 `digest.md`（人读的素材包）。

**先读 `digest.md`。** 只有需要按机器处理时才读 `digest.json`。

### 第 2 步：素材不够时补画面（这才是 image_vqa 的用途）

`digest.md` 的"关键帧画面文字"来自**本地 OCR**——它只能读屏上文字，看不懂画面本身。所以：

- 每帧 OCR 都有字 → 通常够了，别再花钱。
- 有帧的 OCR 是空的，而这个视频是**教程、产品演示、操作录屏、图表讲解**那种"画面本身承载信息"的类型 → 对那一帧调用 `image_vqa`：

```
image_vqa(image_path="<digest 里的 frame 路径>", query="这一帧在演示什么？界面上有什么？")
```

`image_vqa` 现在是**本地优先**的：同样的问题会先命中已缓存的 OCR 结果，只有真正需要视觉理解时才回落到远端多模态模型。所以对关键帧提问不会重复付 OCR 的钱。

只对**必要的那几帧**提问。一段 15 分钟的演讲，六帧画面几乎一样，`image_vqa` 也就没有必要。

脚本会自动折叠**完全重复**的帧（幻灯片停留、静态桌面、暂停画面），但它不会把"同一场景的不同机位"合并——那需要语义判断，是你的活。所以看到六帧讲台演讲，别逐帧提问。

### 第 2b 步：专名必须看画面核对（别信转写里的英文名）

**这是本 skill 最容易出事的地方。** 本地语音后端在英文站名、品牌名、专有名词上错得离谱，
而且错得很自信。实测一次域名教程的转写里：把 `Cloudflare` 听成"邑邑AL"、把 `TLD-LIST`
听成"TLDLIS"、把 `DNSHE` 听成"DN Suite"、把 `DigitalPlat` 听成"DGTO PLAT"，
而 `NIC.IN` 那个平台**整段转写里根本没出现**。

**这些词通常就写在画面上。** 写摘要引用任何站名、平台名、域名、术语之前，
先看对应时间点的帧确认一遍，并把"转写听到的 vs 画面实际的"列成对照表放进产物。

本机 `vision_available()` 通常是 **False**（没配视觉服务），这时 `video_digest.py`
抽不出帧。**抽帧是独立步骤**，用这个脚本：

```bash
# 按固定间隔抽，覆盖全片，找专名用这个
python3 scripts/extract_frames.py "<url 或本地视频路径>" --out <产物目录> --interval 25 --max 10

# 或对准已知时间点精抽
python3 scripts/extract_frames.py "<url>" --out <产物目录> --times 48,60,96,116
```

帧落在 `<产物目录>/frames/NNN_<秒>s.jpg`，**没有 OCR**，直接用 `image_vqa`
或你自己的视觉读。提问要短，问长了会撑爆视觉模型的 maxTokens：

```
image_vqa(image_path="<帧路径>", mode="remote", query="屏幕上的网站名是什么？只回答名称。")
```

**为什么不把抽帧塞回转写脚本**：抽帧需要眼睛，而本机没有视觉服务。
塞进流水线只会让一个静默失败的能力看起来像能用的功能——踩过，别回退。

### 第 3 步：写摘要

要求：

1. **带时间戳**。每个要点挂上 `[HH:MM:SS]`，让人能跳回去核对。
2. **给结构，不是逐句复述**。按视频自身的分段（`outline`）组织，不要按转写顺序流水账。
3. **说清楚证据来源**。是人工字幕、自动字幕，还是本地 STT。自动字幕会听错人名和专有名词，STT 更会。
4. **区分"说到的"和"看到的"**。转写里没有、只从画面 OCR 得到的信息，要标明来自画面。
5. **素材不足就直说**。字幕/音频都拿不到、或转写明显残缺时，明确讲"这份总结基于 X，可能遗漏 Y"，不要装作看完了。

## 依赖与降级

| 环节 | 用什么 | 缺失时 |
| --- | --- | --- |
| 下载 | `video-downloader` skill / yt-dlp | 直接给本地文件路径 |
| 抽音轨 / 抽帧 | `.tools\bin\ffmpeg.exe`（imageio-ffmpeg 的副本） | 无法处理本地视频，只能收字幕文件 |
| 字幕解析 | 内置 SRT/VTT 解析 | — |
| 语音识别 | sherpa-onnx + `.tools/sherpa-models` 里的 **SenseVoice**（离线、中文最优） | 退回 faster-whisper（需 `HF_ENDPOINT`），再退回流式 Zipformer |
| 屏上文字 | 本地视觉服务 `127.0.0.1:8791`（与 `computer_observe` 的 vision 动作同一个服务） | 脚本会尝试自行启动它 |

**字幕永远优先于 STT。** 平台字幕是人工/平台模型产出的文本，比本地小模型准得多，而且不用下载整段音频。拿到字幕时脚本根本不会碰 STT。

## STT 后端的选择（2026-09 实测，别凭感觉改）

`--stt auto` 的顺序是 **SenseVoice → faster-whisper → 流式 Zipformer**，理由是实测出来的，不是设计出来的：

| 后端 | 3:58 中英混说科技视频实测 | 结论 |
| --- | --- | --- |
| SenseVoice（离线，227M） | 备案✓ 注册✓ 域名✓ 数字✓，13.7s | 中文最优，**auto 首选** |
| faster-whisper base | "有ip的就不需要cdn"、CDN✓，11.2s | **英文专名（CDN/Cloudflare/xyz）更准** |
| 流式 Zipformer | "在杭州市同语我这么多棋液"、"邑邑AL CDN"，4.6s | partial 实时模型，**离线批转写精度最差** |

**三个必须知道的点**：

1. **Zipformer 是给流式 partial 用的**，不要拿它做离线批转写——快但错得离谱。
2. **SenseVoice 对粤语/口音吃力**。上面的 B 站视频是粤语腔调，切片后中文词能保住但句子偏碎，这是模型能力边界，调 `_speech_runs` 的 `min_gap_ms`/`min_run_ms` 救不回来（实测放大到 800ms 反而更丢字：870→434 字）。当前 400/400 是字数最优。
3. **摘要关键在英文品牌名上，就显式 `--stt whisper`**，别指望 auto 兼顾。

模型目录会被向上查找 `.tools/sherpa-models`（旧代码硬编码 `.tools\...`，在 Linux/macOS 上退化成相对路径，永远找不到——这个坑已经修掉了）。

**绝不为了总结下载整段视频。** 有字幕就只下字幕；没字幕才下音频轨（`bestaudio`），不下视频流。关键帧需要画面时才需要视频文件——这种情况下脚本会在素材包里标注"关键帧来自视频流"。

## 实测参考

15 分钟英文演讲（Steve Jobs 斯坦福演讲，360p）：

```
字幕路径   341 段 / 11624 字，秒级完成
本地 STT   61 段 / 11568 字，35 秒（约 25x 实时）
关键帧     6 帧，OCR 全部为空（画面是讲台演讲，确实无字）
```

注意第二行：**本地 STT 的绝对质量不如字幕**（会把人名听成 `PICKSAR` 而不是 `Pixar`）。有字幕就用字幕，这不是可选项而是硬规则。

## 反模式

- 用 `--no-frames` 却声称"看了画面"。
- 把 15 分钟视频的完整转写直接丢给用户当摘要。
- 对每一帧都调 `image_vqa`。
- 转写里有的话当成画面里的信息（或反过来）。
- 自动字幕听错的人名不加说明地当成事实。
