# 🎬 视频下载技能 (Video Downloader Skill)

> 一个完整的视频和字幕下载解决方案，支持 YouTube、B站、Twitter 等主流平台

[![yt-dlp](https://img.shields.io/badge/yt--dlp-2025.10.14-brightgreen)](https://github.com/yt-dlp/yt-dlp)
[![Platforms](https://img.shields.io/badge/platforms-1000%2B-blue)](https://github.com/yt-dlp/yt-dlp/blob/master/supportedsites.md)
[![Subtitles](https://img.shields.io/badge/subtitles-100%2B%20languages-orange)](#)

## 🎯 一句话介绍

**下载任何视频 + 字幕，一个命令搞定！**

```bash
yt-dlp --write-subs --sub-langs zh-Hans,en --embed-subs "视频URL"
```

## ✨ 核心特性

✅ **多平台支持** - YouTube、B站、Twitter、抖音、快手等 1000+ 网站
✅ **📝 字幕下载** - 自动下载并嵌入中英文字幕（支持 100+ 语言）
✅ **🎨 质量选择** - 从 360p 到 4K，自由选择视频质量
✅ **📦 批量下载** - 一键下载整个播放列表
✅ **🎵 音频提取** - 提取 MP3、FLAC 等音频格式
✅ **⏸️ 断点续传** - 下载中断后可继续
✅ **🔄 格式转换** - 自动转换为 MP4 等格式
✅ **📁 统一管理** - 所有下载文件集中在 `downloads/` 文件夹

## 📁 文件结构

```
VideoDownloader-Skill/
├── 📄 README.md              # 本文件（主说明）
├── 🤖 SKILL.md               # Claude Code 技能文件
├── 📂 docs/                  # 完整文档
│   ├── 📖 subtitle-guide.md          # 字幕下载完全指南
│   ├── 📱 platform-guide.md          # 平台专属使用指南
│   ├── 💡 quick-reference.md         # 快速参考卡
│   ├── 🔧 test-and-troubleshoot.md   # 测试与故障排除
│   ├── ⚙️  config.example            # 配置文件示例
│   └── 💡 examples.txt               # 使用示例
├── 🔧 scripts/               # 脚本工具
│   ├── 🍎 install.sh                 # macOS/Linux 安装脚本
│   ├── 🪟 install.bat                # Windows 安装脚本
│   ├── 🧪 test.sh                    # 基础测试脚本
│   └── 🎬 test-subtitle-demo.sh      # 字幕功能演示
└── 📥 downloads/             # 下载目录（所有视频都在这里）
```

## 🚀 快速开始

### 1️⃣ 安装依赖

#### macOS
```bash
cd scripts
./install.sh
```

#### Windows
```batch
cd scripts
install.bat
```

#### 手动安装
```bash
# 安装 yt-dlp
pip install -U yt-dlp

# 安装 FFmpeg
brew install ffmpeg        # macOS
sudo apt install ffmpeg    # Linux
```

### 2️⃣ 开始下载

```bash
# 🎯 下载视频 + 中英文字幕（推荐）
yt-dlp --write-subs --sub-langs zh-Hans,en --embed-subs "视频URL"

# 📁 所有下载的文件都保存在 downloads/ 文件夹
```

### 3️⃣ 查看文件

```bash
# 进入下载目录
cd downloads

# 查看下载的视频和字幕
ls -lh
```

## 📝 常用命令速查

### 基础下载

```bash
# 下载视频（最佳质量）
yt-dlp "URL"

# 下载视频 + 中英文字幕
yt-dlp --write-subs --sub-langs zh-Hans,en --embed-subs "URL"

# 只下载音频（MP3）
yt-dlp -x --audio-format mp3 "URL"

# 下载播放列表
yt-dlp "播放列表URL"
```

### 质量选择

```bash
# 1080p
yt-dlp -f "bestvideo[height<=1080]+bestaudio" "URL"

# 720p（更快）
yt-dlp -f "bestvideo[height<=720]+bestaudio" "URL"

# 4K
yt-dlp -f "bestvideo[height<=2160]+bestaudio" "URL"
```

### 字幕相关

```bash
# 下载所有字幕
yt-dlp --write-subs --sub-langs all --embed-subs "URL"

# 只下载字幕不下载视频
yt-dlp --skip-download --write-subs "URL"

# 下载自动生成字幕
yt-dlp --write-auto-subs "URL"
```

### 平台专属

```bash
# YouTube - 使用 cookies
yt-dlp --cookies-browser chrome "YouTube URL"

# B站 - 最高画质
yt-dlp --cookies-browser chrome --write-subs "B站URL"

# Twitter
yt-dlp "推文URL"

# 抖音
yt-dlp "抖音分享链接"
```

## ⚙️ 配置自动下载

创建配置文件 `~/.config/yt-dlp/config`：

```ini
# 所有下载统一保存到 VideoDownloader-Skill/downloads/
-o ~/Desktop/my_projects/VideoDownloader-Skill/downloads/%(extractor)s/%(uploader)s/%(title)s.%(ext)s

# 自动下载中英文字幕
--write-subs
--sub-langs zh-Hans,en

# 嵌入字幕到视频
--embed-subs

# 合并为 MP4
--merge-output-format mp4

# 并发下载
--concurrent-fragments 4
```

然后只需运行：
```bash
yt-dlp "URL"  # 自动包含字幕！
```

## 📚 详细文档

| 文档 | 说明 | 路径 |
|------|------|------|
| 🎯 字幕完全指南 | 字幕下载、格式转换、多语言 | `docs/subtitle-guide.md` |
| 📱 平台使用指南 | YouTube、B站、Twitter 等 | `docs/platform-guide.md` |
| ⚡ 快速参考卡 | 常用命令速查 | `docs/quick-reference.md` |
| 🔧 故障排除 | 问题诊断和解决方案 | `docs/test-and-troubleshoot.md` |

## 🧪 测试功能

### 运行测试脚本

```bash
cd scripts

# 基础测试
./test.sh

# 字幕功能演示
./test-subtitle-demo.sh
```

### 手动测试

```bash
# 测试 1：环境检查
yt-dlp --version
ffmpeg -version

# 测试 2：查看字幕
yt-dlp --list-subs "https://www.youtube.com/watch?v=PBkGNCgQVL8"

# 测试 3：下载字幕
yt-dlp --skip-download --write-subs --sub-langs en --convert-subs srt \
       "https://www.youtube.com/watch?v=PBkGNCgQVL8"
```

## 📥 下载位置

**所有下载文件统一保存在：**
```
VideoDownloader-Skill/downloads/
├── YouTube/
│   ├── 频道名/
│   │   ├── 视频标题.mp4        # 视频文件
│   │   ├── 视频标题.zh-Hans.srt # 中文字幕
│   │   └── 视频标题.en.srt      # 英文字幕
│   └── ...
├── bilibili/
│   └── ...
└── twitter/
    └── ...
```

## 🎯 使用场景

### 学习资源下载

```bash
# TED 演讲 + 中英文字幕
yt-dlp --write-subs --sub-langs zh-Hans,en --embed-subs "TED演讲URL"

# 课程播放列表 + 字幕
yt-dlp --write-subs --sub-langs all "课程播放列表URL"
```

### 只做笔记

```bash
# 只下载字幕不下载视频
yt-dlp --skip-download --write-subs --sub-langs zh-Hans,en --convert-subs srt "URL"
```

### 音乐提取

```bash
# 提取音频为 MP3
yt-dlp -x --audio-format mp3 "音乐视频URL"
```

## 💡 在 Claude Code 中使用

安装此技能后：

```
你：使用 video-downloader 下载这个 TED 演讲，要带中英文字幕

Claude：好的，我会下载视频和字幕：
yt-dlp --write-subs --sub-langs zh-Hans,en --embed-subs "TED URL"
[下载到 downloads/ 文件夹...]

你：帮我把这个播放列表的所有字幕都下载下来

Claude：我会批量下载字幕：
yt-dlp --skip-download --write-subs --sub-langs all "播放列表URL"
[保存到 downloads/YouTube/...]
```

## 🔧 故障排除

### ❌ 找不到字幕

```bash
# 检查字幕可用性
yt-dlp --list-subs "URL"

# 使用浏览器 cookies
yt-dlp --cookies-browser chrome "URL"
```

### ❌ 无法嵌入字幕

```bash
# 安装 FFmpeg
brew install ffmpeg  # macOS
sudo apt install ffmpeg  # Linux
```

### ❌ YouTube 需要认证

```bash
# 使用浏览器 cookies
yt-dlp --cookies-browser chrome "YouTube URL"
```

更多问题解决：查看 `docs/test-and-troubleshoot.md`

## 🎓 支持的平台

| 平台 | 状态 | 说明 |
|------|------|------|
| YouTube | ✅ 完全支持 | 含播放列表、字幕、直播 |
| B站 | ✅ 完全支持 | CC 字幕、需要 cookies 获取高画质 |
| Twitter/X | ✅ 完全支持 | 视频、GIF |
| 抖音 | ✅ 支持 | 需分享链接 |
| 快手 | ✅ 支持 | 需分享链接 |
| 其他 | 📋 1000+ | 运行 `yt-dlp --list-extractors` |

## ⚠️ 法律声明

- 仅供个人学习和研究使用
- 请遵守相关平台的版权政策
- 下载的内容不得用于商业用途
- 请尊重原创者的版权

## 📞 获取帮助

- 📖 查看文档：`docs/` 文件夹
- 🧪 运行测试：`scripts/test.sh`
- 💬 官方文档：https://github.com/yt-dlp/yt-dlp
- 🐛 问题反馈：https://github.com/yt-dlp/yt-dlp/issues

## ✨ 更新日志

- **v1.0** (2025-01-27)
  - ✅ 完整的字幕下载支持
  - ✅ 多平台支持
  - ✅ 统一文件管理
  - ✅ 完整文档和测试脚本

---

**🎉 开始使用：**
```bash
cd scripts
./install.sh
yt-dlp --write-subs --sub-langs zh-Hans,en --embed-subs "你的视频URL"
```

**📚 查看文档：** `docs/` 文件夹

**📥 下载位置：** `downloads/` 文件夹
