---
name: video-downloader
description: 下载各种平台的视频和字幕（YouTube、B站、Twitter、抖音等）。自动下载中英文字幕并嵌入视频，支持选择质量、提取音频、批量下载。需要把视频转成文字摘要时交给 video-summary skill 处理。
metadata:
  short-description: "Download video and subtitles from 1000+ platforms via yt-dlp"
  tags:
    - video
    - download
    - subtitle
    - yt-dlp
    - bilibili
    - youtube
  triggers:
    - 下载视频
    - 下载字幕
    - 视频下载
    - 保存视频
    - 提取音频
    - youtube
    - 抖音视频
    - b站视频
    - yt-dlp
  priority: 60
---

# 🎬 视频下载助手

完整的视频和字幕下载解决方案，支持 YouTube、B站、Twitter、抖音、快手等 1000+ 主流平台。

> **在 Fairy 仓库里运行时，先读 [references/fairy-integration.md](references/fairy-integration.md)。**
> 上游脚本假设 `yt-dlp` 和 `ffmpeg` 在 PATH 上、终端是 UTF-8。Fairy 用的是
> `.tools\venv` 里的 Python，ffmpeg 由 `imageio-ffmpeg` 提供，控制台代码页需要显式设置。

## 🚀 快速开始

### 方式一：智能下载（推荐新手）

```bash
# 自动检测登录需求并引导使用浏览器 cookies
./smart-download.sh "视频URL"
```

**优势：**
- ✅ 自动检测是否需要登录
- ✅ 智能失败重试
- ✅ 交互式登录引导
- ✅ 支持使用浏览器 cookies

### 方式二：一键下载视频 + 字幕

```bash
# 下载视频并自动下载和嵌入中英文字幕
yt-dlp --write-subs --sub-langs zh-Hans,en --embed-subs "视频URL"
```

**所有下载文件自动保存在：** `downloads/` 文件夹

## 核心功能

### 📝 字幕下载（重点）

- ✅ **自动下载字幕** - 中英文字幕一键下载
- ✅ **嵌入字幕** - 自动嵌入到视频文件
- ✅ **多语言支持** - 支持 100+ 种语言
- ✅ **字幕格式** - SRT、VTT、ASS 等
- ✅ **只下载字幕** - 不下载视频，只下字幕

### 📺 平台支持

| 平台 | 字幕支持 | 质量选择 | 特殊要求 |
|------|---------|---------|----------|
| YouTube | ✅ 人工+自动 | ✅ 360p-4K | 需 cookies |
| B站 | ✅ CC 字幕 | ✅ 最高1080P | 需 cookies 登录 |
| Twitter | ✅ 平台字幕 | ✅ 原质量 | - |
| 抖音 | ⚠️ 有限 | ✅ 原质量 | - |
| 其他 1000+ | ✅ 大部分 | ✅ | - |

### 🎯 其他功能

- 🎵 音频提取（MP3、FLAC）
- 📦 批量下载（播放列表）
- ⏸️ 断点续传
- 🔄 格式转换（MP4 等）
- 📁 统一文件管理

## 常用命令

### 基础下载

```bash
# 下载视频（默认最佳质量）
yt-dlp "视频URL"

# 🎯 下载视频 + 中英文字幕（最常用）
yt-dlp --write-subs --sub-langs zh-Hans,en --embed-subs "视频URL"

# 下载为 MP3
yt-dlp -x --audio-format mp3 "视频URL"

# 下载播放列表
yt-dlp "播放列表URL"
```

### 字幕专属命令

```bash
# 下载所有可用字幕
yt-dlp --write-subs --sub-langs all --embed-subs "视频URL"

# 下载自动生成字幕
yt-dlp --write-auto-subs --sub-langs zh-Hans,en "视频URL"

# 只下载字幕不下载视频
yt-dlp --skip-download --write-subs --sub-langs all "视频URL"

# 转换字幕为 SRT 格式
yt-dlp --write-subs --convert-subs srt "视频URL"
```

### 质量选择

```bash
# 1080p + 最佳音频
yt-dlp -f "bestvideo[height<=1080]+bestaudio" "视频URL"

# 720p（更快下载）
yt-dlp -f "bestvideo[height<=720]+bestaudio" "视频URL"

# 4K 如果可用
yt-dlp -f "bestvideo[height<=2160]+bestaudio" "视频URL"
```

### 平台专属命令

#### YouTube

```bash
# 下载播放列表
yt-dlp "播放列表URL"

# 下载中英文字幕
yt-dlp --write-subs --sub-langs zh-Hans,en --embed-subs "YouTube URL"

# 使用浏览器 cookies（解决认证问题）
yt-dlp --cookies-browser chrome "YouTube URL"
```

#### B站

```bash
# 下载最高画质（需要 cookies）
yt-dlp --cookies-browser chrome "B站URL"

# 下载 CC 字幕
yt-dlp --write-subs --embed-subs "B站URL"
```

#### Twitter/X

```bash
# 下载推文视频
yt-dlp "推文URL"
```

#### 抖音/快手

```bash
# 使用分享链接
yt-dlp "抖音/快手分享链接"
```

## 🔐 智能登录下载功能

### 什么是智能下载？

某些平台（如小红书、B站、YouTube 私密视频等）需要登录才能访问或获取高质量视频。

**智能下载脚本**会：
1. 自动检测下载失败
2. 判断是否需要登录
3. 引导你使用浏览器 cookies
4. 自动重试下载

### 使用智能下载

```bash
# 交互式智能下载（推荐）
./smart-download.sh "视频URL"
```

**工作流程：**
1. 尝试普通下载
2. 如果失败，显示菜单：
   - 查看登录指南
   - 使用浏览器 cookies
   - 简化选项重试
3. 根据你的选择自动处理

### 手动导出浏览器 Cookies

#### 方法 1：使用浏览器扩展（最简单）

1. 安装扩展 **"Get cookies.txt"**
   - Chrome/Edge: https://chrome.google.com/webstore
   - Firefox: https://addons.mozilla.org/
   - 搜索 "Get cookies.txt LOCALLY"

2. 在浏览器中打开视频网站并登录

3. 点击扩展图标，下载 cookies.txt

4. 将 cookies.txt 放到 `downloads/cookies.txt`

5. 使用 cookies 下载：
```bash
yt-dlp --cookies downloads/cookies.txt --write-subs --sub-langs zh-Hans,en "视频URL"
```

#### 方法 2：使用 Python 脚本

```bash
# 安装依赖
pip3 install browser-cookie3

# 导出 Chrome cookies
python3 scripts/export-cookies.py chrome downloads/cookies.txt

# 导出 Safari cookies (macOS)
python3 scripts/export-cookies.py safari downloads/cookies.txt

# 导出 Firefox cookies
python3 scripts/export-cookies.py firefox downloads/cookies.txt
```

#### 方法 3：使用命令（高级）

```bash
# 使用导出的 cookies 下载
yt-dlp --cookies downloads/cookies.txt "视频URL"
```

### Cookies 支持的平台

| 平台 | 是否需要 Cookies | 说明 |
|------|-----------------|------|
| YouTube 公开视频 | ❌ 不需要 | 直接下载 |
| YouTube 私密/会员 | ✅ 需要 | 需要登录 |
| B站 | ✅ 推荐 | 获取最高画质 |
| 小红书 | ✅ 通常需要 | 反爬虫严格 |
| 抖音 | ⚠️ 有时需要 | 部分视频 |
| Twitter 公开 | ❌ 不需要 | 直接下载 |
| Twitter 私密 | ✅ 需要 | 需要登录 |

### 示例：下载需要登录的视频

```bash
# 1. 使用智能下载（自动处理）
./smart-download.sh "https://www.xiaohongshu.com/explore/xxx"

# 2. 或手动使用 cookies
yt-dlp --cookies downloads/cookies.txt --write-subs --sub-langs zh-Hans,en "视频URL"
```

## 📁 文件组织

**所有下载统一保存在：** `downloads/` 文件夹

```
downloads/
├── YouTube/
│   ├── 频道名/
│   │   ├── 视频标题.mp4              # 视频文件
│   │   ├── 视频标题.zh-Hans.srt       # 中文字幕
│   │   └── 视频标题.en.srt            # 英文字幕
│   └── ...
├── bilibili/
│   └── ...
└── twitter/
    └── ...
```

## 安装

### macOS / Linux

```bash
cd scripts
./install.sh
```

### Windows

```batch
cd scripts
install.bat
```

### 手动安装

```bash
# 安装 yt-dlp
pip install -U yt-dlp

# 安装 FFmpeg
brew install ffmpeg        # macOS
sudo apt install ffmpeg    # Linux
```

## 配置自动下载

创建 `~/.config/yt-dlp/config`：

```ini
# 统一保存到 downloads/ 文件夹
-o ~/Desktop/my_projects/VideoDownloader-Skill/downloads/%(extractor)s/%(uploader)s/%(title)s.%(ext)s

# 自动下载中英文字幕
--write-subs
--sub-langs zh-Hans,en

# 嵌入字幕
--embed-subs

# 合并为 MP4
--merge-output-format mp4
```

配置后，只需运行：
```bash
yt-dlp "URL"  # 自动包含字幕！
```

## 常见问题

### Q: 找不到字幕？
```bash
# 检查字幕可用性
yt-dlp --list-subs "URL"

# 使用浏览器 cookies
yt-dlp --cookies-browser chrome "URL"
```

### Q: 无法嵌入字幕？
```bash
# 安装 FFmpeg
brew install ffmpeg  # macOS
```

### Q: YouTube 需要认证？
```bash
# 使用浏览器 cookies
yt-dlp --cookies-browser chrome "YouTube URL"
```

### Q: B站只能低画质？
```bash
# 使用 cookies
yt-dlp --cookies-browser chrome "B站URL"
```

### Q: 如何更新 yt-dlp？
```bash
pip install -U yt-dlp
```

## 字幕语言代码

| 语言 | 代码 |
|------|------|
| 中文简体 | zh-Hans |
| 中文繁体 | zh-Hant |
| 英语 | en |
| 日语 | ja |
| 韩语 | ko |
| 法语 | fr |
| 德语 | de |
| 西班牙语 | es |

下载多语言字幕：
```bash
yt-dlp --write-subs --sub-langs zh-Hans,zh-Hant,en,ja,ko "URL"
```

## 批量下载

### 下载播放列表

```bash
# 下载整个播放列表 + 字幕
yt-dlp --write-subs --sub-langs all "播放列表URL"

# 只下载前 10 个
yt-dlp --playlist-end 10 "播放列表URL"
```

### 从文件批量下载

```bash
# 创建 urls.txt，每行一个 URL
yt-dlp --write-subs -a urls.txt
```

## 高级功能

### 只下载字幕（不下载视频）

```bash
# 只下载字幕文件
yt-dlp --skip-download --write-subs --sub-langs zh-Hans,en --convert-subs srt "URL"

# 输出：视频标题.zh-Hans.srt, 视频标题.en.srt
```

### 字幕格式转换

```bash
# 转换为 SRT
yt-dlp --write-subs --convert-subs srt "URL"

# 转换为 ASS
yt-dlp --write-subs --convert-subs ass "URL"
```

### 使用代理

```bash
# HTTP 代理
yt-dlp --proxy http://127.0.0.1:7890 "URL"

# SOCKS5 代理
yt-dlp --proxy socks5://127.0.0.1:1080 "URL"
```

## 测试

运行测试脚本：

```bash
cd scripts

# 基础测试
./test.sh

# 字幕功能演示
./test-subtitle-demo.sh
```

## 法律声明

⚠️ **重要提示：**
- 仅供个人学习和研究使用
- 请遵守相关平台的版权政策
- 下载的内容不得用于商业用途
- 请尊重原创者的版权

## 完整文档

详细文档请查看 `docs/` 文件夹：

- `subtitle-guide.md` - 字幕下载完全指南
- `platform-guide.md` - 平台专属使用指南
- `quick-reference.md` - 快速参考卡
- `test-and-troubleshoot.md` - 测试与故障排除

## 参考资料

- yt-dlp GitHub: https://github.com/yt-dlp/yt-dlp
- 支持网站列表: https://github.com/yt-dlp/yt-dlp/blob/master/supportedsites.md
