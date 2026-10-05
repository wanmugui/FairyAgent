---
name: video-edit
description: 剪辑视频：按计划切片、拼接、变速、竖屏裁切、烧中文字幕、加文字水印、换/静音/调音量，并生成能"回看"的预览（缩略图 + 短片 + 中间帧）。当用户说"把这段视频剪一下""去掉中间那段""剪成 1 分钟""压成竖屏发抖音/朋友圈""加字幕""加水印""拼接这几段""把 XX 到 XX 剪出来"时使用。配套 video-summary 先看内容、再剪，剪完自己看预览确认。
metadata:
  short-description: "Cut, caption, crop and preview video with ffmpeg"
  tags:
    - video
    - edit
    - ffmpeg
    - subtitle
    - watermark
    - vertical
    - clip
  triggers:
    - 剪视频
    - 剪辑
    - 剪一下
    - 加字幕
    - 加水印
    - 竖屏
    - 拼接
    - 去掉
    - 变速
    - 导出视频
    - edit video
    - cut video
    - add subtitles
tags:
  - video
  - edit
  - ffmpeg
  - subtitle
triggers:
  - 剪视频
  - 剪辑
  - 剪一下
  - 加字幕
  - 加水印
  - 竖屏
  - 拼接
  - 去掉
  - 变速
  - 导出视频
  - edit video
  - cut video
priority: 70
---

# 视频剪辑

## 两条路：单视频 plan vs 多素材时间线

| 场景 | 用哪个 |
|---|---|
| 剪**一个**视频：切段/变速/裁竖屏/字幕/水印 | `video_edit.py edit <视频> --plan plan.json`（原有，成熟） |
| **多个素材**拼一条：转场、多轨文字/图片图层、背景音乐 | `timeline_edit.py build timeline.json out.mp4`（多素材超集） |

`timeline_edit.py` 是 `video_edit.py` 的超集入口，**共用** probe/字幕/ffmpeg 助手，互不干扰。时间线 JSON 结构见脚本头部 `TIMELINE_SCHEMA` 或 `timeline_edit.py dump`。转场类型：`fade/fadeblack/fadewhite/wipeleft/wiperight/slideleft/slideup/circleopen/dissolve`。

> 注：`xfade` 要求相邻片段重叠，脚本已自动从两侧各扣掉 `transition_duration`；`transition` 和 `transition_duration` 都可写在**前一段**上（向后兜底）。

## 单视频 plan 流程

抄的是 ChatGPT/Claude 那套能让人"眼馋"的流程，不是某个按钮——**看 → 决定 → 执行 → 回看 → 迭代**：

```
1. 看     python skills/video-summary/scripts/video_digest.py <视频>   # 转录 + 关键帧 OCR（粗看）
          python skills/video-edit/scripts/video_edit.py probe <视频>   # 时长/分辨率/有没有音轨
          python skills/video-edit/scripts/video_edit.py frames <视频> --count 12 --ocr
2. 决定   写 plan.json（见下）
3. 执行   python skills/video-edit/scripts/video_edit.py edit <视频> --plan plan.json --out out.mp4
4. 回看   python skills/video-edit/scripts/video_edit.py preview out.mp4 --out preview/
          然后**真的去看** preview/contact-sheet.jpg 和 middle-frame.jpg（必要时 image_vqa 读字幕）
5. 迭代   不对就改 plan 再跑。剪完必须回看，不许不看就交付。
```

## plan.json

```json
{
  "segments": [{"start": 2.5, "end": 8.0}, {"start": 12.4, "end": 20.0}],
  "speed": 1.25,
  "crop": "vertical",
  "scale_width": null,
  "subtitles": {"items": [{"start": 0.5, "end": 3.0, "text": "第一句"}]},
  "watermark": {"text": "Fairy", "position": "br"},
  "audio": {"mute": false, "replace": null, "volume": 1.0},
  "export": {"crf": 20, "preset": "veryfast", "fps": null}
}
```

规则与坑：

- `segments` 是**保留**的片段（按顺序拼接）。不在里面的就是被剪掉的。`end` 必须大于 `start`。
- 不写 `segments` 就是整段（仅做变速/裁切/字幕/水印）。
- `speed` 只支持 0.5–2.0（音频用 `atempo`，超出范围平台不支持，要提速就分段）。
- `crop: "vertical"` = 中间裁成 9:16 并放大到 1080×1920；要固定尺寸就写 `{"width":1080,"height":1920}`（先等比放大再裁，不会变形）。
- 字幕二选一：`{"srt": "subs.srt"}` 或 `{"items": [...]}`。烧录用的是 libass，**中文字体这台机器有**（Noto Sans CJK），不会变方框；字重/描边可用 `force_style` 覆盖。
- 水印是文字水印（`position`: br/bl/tr/tl），走 drawtext + 同一个中文字体。
- 导出默认 `libx264`（正确优先）。这台机器有 AMD 核显，`--encoder vaapi` 能更快，但滤镜链不一样，**先用 CPU 出片、再用 vaapi 重跑一遍对比**，选看不出差别的那个。
- 大视频先 `probe` 看时长再决定策略：切片是流式复制的思路，但烧字幕/裁切必须重编码，时间与 CPU 成正比（24 核，1 分钟 1080p 大约十几秒）。

## 产物与交付

- `edit` 的成片、`preview` 的三样东西都落在你指定的目录（建议 `workspace/video/<名字>/`），前端能直接预览。
- 发到手机：QQ 能发图片（成片要发的话先把关键帧/封面发过去）；微信目前只有图片/文字，视频/文件还没实现——**别承诺"直接发微信"**。
- 交付话术要报清楚：剪掉了什么时间段、成片多长、什么分辨率、有没有字幕/水印，而不是只说"剪好了"。
