# 📝 视频字幕下载完全指南

## 🎯 一键下载视频 + 字幕

```bash
# 下载视频并自动下载和嵌入字幕
yt-dlp --write-subs --embed-subs "视频URL"

# 下载视频 + 所有可用字幕
yt-dlp --write-subs --sub-langs all --embed-subs "视频URL"

# 下载视频 + 中英文字幕
yt-dlp --write-subs --sub-langs zh-Hans,en --embed-subs "视频URL"
```

## 📺 YouTube 字幕下载

### 下载人工字幕

```bash
# 下载所有人工字幕
yt-dlp --write-subs --sub-langs all "YouTube URL"

# 下载中文字幕（简体）
yt-dlp --write-subs --sub-langs zh-Hans "YouTube URL"

# 下载中文字幕（繁体）
yt-dlp --write-subs --sub-langs zh-Hant "YouTube URL"

# 下载英文字幕
yt-dlp --write-subs --sub-langs en "YouTube URL"

# 下载中日英字幕
yt-dlp --write-subs --sub-langs zh-Hans,zh-Hant,en,ja "YouTube URL"
```

### 下载自动生成字幕

```bash
# 下载自动生成的字幕
yt-dlp --write-auto-subs "YouTube URL"

# 下载中文自动字幕
yt-dlp --write-auto-subs --sub-langs zh-Hans "YouTube URL"

# 下载英文自动字幕
yt-dlp --write-auto-subs --sub-langs en "YouTube URL"

# 同时下载人工和自动字幕
yt-dlp --write-subs --write-auto-subs --sub-langs all "YouTube URL"
```

### 嵌入字幕到视频

```bash
# 下载并嵌入字幕到 MP4
yt-dlp --embed-subs --sub-langs zh-Hans,en "YouTube URL"

# 嵌入多个字幕轨
yt-dlp --embed-subs --sub-langs all "YouTube URL"
```

### 只下载字幕不下载视频

```bash
# 只下载字幕文件
yt-dlp --skip-download --write-subs "YouTube URL"

# 下载所有字幕格式（VTT, SRT 等）
yt-dlp --skip-download --write-subs --sub-langs all "YouTube URL"

# 保存为 SRT 格式
yt-dlp --skip-download --write-subs --sub-langs zh-Hans --convert-subs srt "YouTube URL"
```

### 转换字幕格式

```bash
# 转换为 SRT 格式
yt-dlp --convert-subs srt "YouTube URL"

# 转换为 ASS 格式
yt-dlp --convert-subs ass "YouTube URL"

# 转换为 LRC 格式（歌词）
yt-dlp --convert-subs lrc "YouTube URL"
```

## 📺 B站字幕下载

### 下载 CC 字幕

```bash
# 下载 B站 CC 字幕
yt-dlp --write-subs "B站URL"

# 下载中文 CC 字幕
yt-dlp --write-subs --sub-langs zh-Hans "B站URL"

# 下载并嵌入字幕
yt-dlp --write-subs --embed-subs "B站URL"
```

### 下载弹幕

⚠️ **注意：** yt-dlp 不直接支持弹幕下载，需要额外工具：

```bash
# 方法1：使用 danmaku 工具
# 安装：pip install danmaku
danmaku "B站URL" -o danmaku.xml

# 方法2：使用 BiliAssist
# GitHub: https://github.com/Connor136/BiliAssist
```

## 🎬 字幕格式说明

### 常见格式对比

| 格式 | 扩展名 | 特点 | 推荐场景 |
|------|--------|------|----------|
| SRT | .srt | 最通用，兼容性好 | 播放器、编辑软件 |
| VTT | .vtt | Web 标准 | 网页播放器 |
| ASS | .ass | 支持样式和特效 | 动漫、高级字幕 |
| LRC | .lrc | 歌词格式 | 音乐播放器 |

### 字幕格式转换

```bash
# 使用 FFmpeg 转换
ffmpeg -i input.vtt output.srt

# 批量转换 VTT 到 SRT
for file in *.vtt; do
    ffmpeg -i "$file" "${file%.vtt}.srt"
done
```

## 🔧 高级字幕功能

### 合并多语言字幕

```bash
# 下载视频并嵌入多个语言字幕
yt-dlp --embed-subs --sub-langs zh-Hans,zh-Hant,en,ja,ko "YouTube URL"
```

### 字幕处理选项

```bash
# 只下载已有字幕（不生成自动字幕）
yt-dlp --write-subs --skip-download "URL"

# 列出可用字幕
yt-dlp --list-subs "URL"

# 下载所有字幕并转换为 SRT
yt-dlp --write-subs --sub-langs all --convert-subs srt "URL"
```

### 字幕与视频同步

```bash
# 如果字幕不同步，可以手动调整
# 使用 FFmpeg 调整时间偏移
ffmpeg -i video.mp4 -itsoffset 0.5 -i subs.srt -c copy -map 0 -map 1 output.mp4
```

## 📋 批量下载字幕

### 从 URL 列表下载

```bash
# 创建 urls.txt，每行一个 URL
yt-dlp --skip-download --write-subs --sub-langs all -a urls.txt
```

### 下载播放列表的所有字幕

```bash
# 下载播放列表中每个视频的字幕
yt-dlp --write-subs --sub-langs zh-Hans,en "播放列表URL"

# 只下载字幕不下载视频
yt-dlp --skip-download --write-subs --sub-langs all "播放列表URL"
```

## 🌍 语言代码参考

### 常用语言代码

| 语言 | 代码 | 说明 |
|------|------|------|
| 中文简体 | zh-Hans | 大陆简体 |
| 中文繁体 | zh-Hant | 台湾/香港繁体 |
| 英语 | en | 英文 |
| 日语 | ja | 日文 |
| 韩语 | ko | 韩文 |
| 法语 | fr | 法文 |
| 德语 | de | 德文 |
| 西班牙语 | es | 西班牙文 |
| 俄语 | ru | 俄文 |
| 阿拉伯语 | ar | 阿拉伯文 |

### 下载多语言字幕示例

```bash
# 下载亚洲语言字幕
yt-dlp --write-subs --sub-langs zh-Hans,zh-Hant,ja,ko "URL"

# 下载欧洲语言字幕
yt-dlp --write-subs --sub-langs en,fr,de,es,it "URL"

# 下载所有可用语言
yt-dlp --write-subs --sub-langs all "URL"
```

## 💡 实用技巧

### 检查字幕可用性

```bash
# 列出视频的所有字幕
yt-dlp --list-subs "URL"

# 查看详细 JSON 信息
yt-dlp --dump-json "URL" | jq '.subtitles'
```

### 自动翻译字幕

```bash
# 下载英文字幕
yt-dlp --write-subs --sub-langs en --skip-download "URL"

# 使用翻译工具翻译（如 translate-shell）
# translate-shell -f en -t en input.srt -o output.srt
```

### 字幕编辑

```bash
# 合并多个 SRT 文件
# 使用 srt 工具或 Python 脚本

# 调整字幕时间轴
# 使用 SubtitleEdit 或在线工具
```

## ⚙️ 配置文件设置

在 `~/.config/yt-dlp/config` (macOS/Linux) 添加：

```ini
# 默认下载中英文字幕
--write-subs
--sub-langs zh-Hans,en

# 默认嵌入字幕
--embed-subs

# 默认转换为 SRT 格式
--convert-subs srt

# 只下载已有字幕，不生成自动字幕
# --no-write-auto-subs
```

## 🎯 实际使用示例

### YouTube 学习资源下载

```bash
# 下载 TED 演讲视频 + 中英文字幕
yt-dlp --write-subs --sub-langs zh-Hans,en --embed-subs "TED视频URL"

# 下载课程播放列表 + 字幕
yt-dlp --write-subs --sub-langs all --embed-subs "课程播放列表URL"
```

### B站教程下载

```bash
# 下载 B 站教程 + CC 字幕
yt-dlp --write-subs --embed-subs "B站教程URL"

# 使用 cookies 获取最高质量 + 字幕
yt-dlp --cookies-browser chrome --write-subs --embed-subs "B站URL"
```

### 只下载字幕做笔记

```bash
# 下载讲座字幕不下载视频
yt-dlp --skip-download --write-subs --sub-langs zh-Hans,en --convert-subs srt "讲座URL"

# 输出文件：视频名.zh-Hans.srt, 视频名.en.srt
```

## 🔍 故障排除

### 字幕下载失败

```bash
# 问题：找不到字幕
# 解决：检查是否有可用字幕
yt-dlp --list-subs "URL"

# 问题：字幕格式不支持
# 解决：转换字幕格式
yt-dlp --convert-subs srt "URL"
```

### 嵌入字幕失败

```bash
# 问题：无法嵌入字幕
# 原因：FFmpeg 未安装或格式不兼容

# 解决1：安装 FFmpeg
brew install ffmpeg  # macOS
sudo apt install ffmpeg  # Linux

# 解决2：使用不同格式
yt-dlp --write-subs --convert-subs srt --embed-subs "URL"
```

### 自动字幕问题

```bash
# 问题：自动字幕不准确
# 解决：寻找人工字幕源
yt-dlp --list-subs "URL"

# 只下载人工字幕
yt-dlp --write-subs --no-write-auto-subs "URL"
```

## 📚 参考资源

- [yt-dlp 字幕文档](https://github.com/yt-dlp/yt-dlp#subtitle-options)
- [FFmpeg 字幕处理](https://trac.ffmpeg.org/wiki/ExtractSubtitles)
- [SRT 格式规范](https://en.wikipedia.org/wiki/SubRip)
