---
name: video-downloader
description: 下载各种平台的视频（YouTube、B站、Twitter、微信视频号等）。支持选择质量、提取音频、批量下载、字幕下载等功能。使用 yt-dlp 作为核心下载引擎。
tags: [video, download, docs]
triggers: [视频下载文档, video-downloader 说明]
priority: 12
---

# 视频下载助手

这个技能帮助你从各种主流视频平台下载视频内容。支持 YouTube、B站（哔哩哔哩）、Twitter（X）、微信视频号等平台。

## 核心工具

我们使用 **yt-dlp** 作为下载引擎，这是一个强大的命令行视频下载工具，支持 1000+ 网站。

## 支持的平台

| 平台 | 支持状态 | 备注 |
|------|---------|------|
| YouTube | ✅ 完全支持 | 支持所有质量、字幕、播放列表 |
| B站 | ✅ 完全支持 | 支持 1080P 及以下，需要登录获取更高清晰度 |
| Twitter/X | ✅ 完全支持 | 支持视频和 GIF |
| 微信视频号 | ⚠️ 有限支持 | 需要视频链接或特殊处理 |
| 抖音 | ✅ 支持 | 需要分享链接 |
| 快手 | ✅ 支持 | 需要分享链接 |
| 其他平台 | 📋 查看支持列表 | 运行 `yt-dlp --list-extractors` |

## 安装步骤

### 1. 安装 yt-dlp

#### macOS (使用 Homebrew)
```bash
brew install yt-dlp
```

#### macOS (使用 pip)
```bash
pip3 install yt-dlp
```

#### Windows
```bash
# 使用 pip
pip install yt-dlp

# 或使用 Scoop
scoop bucket add extras
scoop install yt-dlp

# 或使用 Chocolatey
choco install yt-dlp
```

#### Linux
```bash
sudo curl -L https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp -o /usr/local/bin/yt-dlp
sudo chmod a+rx /usr/local/bin/yt-dlp
```

### 2. 安装 FFmpeg（用于视频合并和质量选择）

#### macOS
```bash
brew install ffmpeg
```

#### Windows
```bash
# 使用 Scoop
scoop install ffmpeg

# 或使用 Chocolatey
choco install ffmpeg
```

#### Linux
```bash
sudo apt install ffmpeg  # Ubuntu/Debian
sudo yum install ffmpeg  # CentOS/RHEL
```

### 3. 验证安装

```bash
yt-dlp --version
ffmpeg -version
```

## 使用方法

### 基础下载命令

#### 下载视频（默认最佳质量）
```bash
yt-dlp "视频URL"
```

#### 指定保存位置和文件名
```bash
yt-dlp -o "~/Downloads/%(title)s.%(ext)s" "视频URL"
```

#### 只下载音频（MP3）
```bash
yt-dlp -x --audio-format mp3 "视频URL"
```

## 平台专属命令

### YouTube

#### 下载播放列表
```bash
# 下载整个播放列表
yt-dlp "播放列表URL"

# 只下载前10个视频
yt-dlp --playlist-end 10 "播放列表URL"

# 从第5个开始下载
yt-dlp --playlist-start 5 "播放列表URL"
```

#### 下载字幕（推荐）
```bash
# 🎯 一键下载视频 + 中英文字幕（最常用）
yt-dlp --write-subs --sub-langs zh-Hans,en --embed-subs "视频URL"

# 下载视频 + 所有可用字幕
yt-dlp --write-subs --sub-langs all --embed-subs "视频URL"

# 下载所有可用字幕
yt-dlp --write-subs --sub-langs all "视频URL"

# 下载自动生成的字幕
yt-dlp --write-auto-subs "视频URL"

# 只下载字幕不下载视频
yt-dlp --skip-download --write-subs "视频URL"
```

#### 选择视频质量
```bash
# 下载 1080p 视频
yt-dlp -f "bestvideo[height<=1080]+bestaudio/best[height<=1080]" "视频URL"

# 下载 720p 视频
yt-dlp -f "bestvideo[height<=720]+bestaudio/best[height<=720]" "视频URL"

# 下载最高质量
yt-dlp -f "bestvideo+bestaudio/best" "视频URL"
```

### B站（哔哩哔哩）

#### 下载单个视频
```bash
yt-dlp "B站视频URL"
```

#### 下载 B 站合集
```bash
yt-dlp "B站合集URL"
```

#### 下载最高画质（需要登录）
```bash
# 方法1：使用浏览器 cookies
yt-dlp --cookies-browser chrome "B站视频URL"

# 方法2：导入 cookies 文件
yt-dlp --cookies cookies.txt "B站视频URL"
```

#### 只下载字幕（CC 字幕）
```bash
yt-dlp --write-subs --sub-langs zh-Hans,zh-Hant "B站视频URL"
```

### Twitter/X

#### 下载视频
```bash
yt-dlp "推文URL"
```

#### 下载高质量版本
```bash
yt-dlp -f "best" "推文URL"
```

#### 批量下载用户视频
```bash
yt-dlp "用户主页URL"
```

### 微信视频号

微信视频号下载较为特殊，通常需要：

```bash
# 直接使用分享链接
yt-dlp "微信视频号分享链接"

# 如果链接无效，可能需要：
# 1. 在浏览器中打开视频
# 2. 复制实际的视频URL
# 3. 使用 yt-dlp 下载
```

提示：微信视频号的下载支持可能会变化，如果遇到问题可以尝试更新 yt-dlp：
```bash
yt-dlp --update
```

### 抖音

```bash
# 下载抖音视频
yt-dlp "抖音分享链接"

# 下载无水印版本（如果支持）
yt-dlp --format "best" "抖音分享链接"
```

### 快手

```bash
yt-dlp "快手分享链接"
```

## 高级功能

### 批量下载

从文件读取 URL 列表：
```bash
# 创建 urls.txt，每行一个 URL
yt-dlp -a urls.txt
```

### 下载特定格式

```bash
# 下载为 MP4
yt-dlp --merge-output-format mp4 "视频URL"

# 下载为 MKV
yt-dlp --merge-output-format mkv "视频URL"
```

### 限制下载速度

```bash
# 限制为 1MB/s
yt-dlp --limit-rate 1M "视频URL"
```

### 断点续传

```bash
# 如果下载中断，使用此命令继续
yt-dlp --continue "视频URL"
```

### 下载元数据

```bash
# 只获取视频信息，不下载
yt-dlp --skip-download --write-info-json "视频URL"

# 打印视频信息
yt-dlp --dump-json "视频URL"
```

### 代理设置

```bash
# 使用 HTTP 代理
yt-dlp --proxy http://127.0.0.1:7890 "视频URL"

# 使用 SOCKS5 代理
yt-dlp --proxy socks5://127.0.0.1:1080 "视频URL"
```

## 常见问题

### Q1: 下载速度慢怎么办？
```bash
# 使用更多连接数
yt-dlp --concurrent-fragments 4 "视频URL"

# 使用代理
yt-dlp --proxy http://代理地址 "视频URL"
```

### Q2: B站只能下载低画质？
需要登录 B站 账户并使用 cookies：
```bash
# 从 Chrome 浏览器导出 cookies
yt-dlp --cookies-browser chrome "B站视频URL"
```

### Q3: 下载的视频没有声音？
安装 FFmpeg 并使用合并选项：
```bash
yt-dlp --merge-output-format mp4 "视频URL"
```

### Q4: 如何下载直播？
```bash
# 下载当前直播（直播结束时自动停止）
yt-dlp "直播URL"

# 从开始时刻下载（如果直播已开始一段时间）
yt-dlp --live-from-start "直播URL"
```

### Q5: URL 太长或含有特殊字符？
```bash
# 将 URL 放入引号中
yt-dlp '非常复杂的URL'
```

### Q6: 如何更新 yt-dlp？
```bash
yt-dlp --update
```

## 输出模板

`yt-dlp` 支持灵活的文件名模板：

```bash
# 基础模板
yt-dlp -o "%(title)s.%(ext)s" "URL"

# 包含上传者
yt-dlp -o "%(uploader)s-%(title)s.%(ext)s" "URL"

# 包含上传日期
yt-dlp -o "%(upload_date)s-%(title)s.%(ext)s" "URL"

# 按平台分类保存
yt-dlp -o "%(extractor)s/%(title)s.%(ext)s" "URL"

# 完整示例
yt-dlp -o "~/Videos/%(extractor)s/%(uploader)s/%(upload_date)s-%(title)s.%(ext)s" "URL"
```

可用的模板变量：
- `%(title)s` - 视频标题
- `%(uploader)s` - 上传者
- `%(upload_date)s` - 上传日期（YYYYMMDD）
- `%(extractor)s` - 网站名称（如 youtube, bilibili）
- `%(id)s` - 视频 ID
- `%(duration)s` - 视频时长
- `%(view_count)s` - 观看次数
- `%(like_count)s` - 点赞数

## 配置文件

你可以创建配置文件来避免重复输入选项。

### macOS/Linux 配置文件
创建 `~/.config/yt-dlp/config`：
```bash
# 默认下载到 ~/Videos
-o ~/Videos/%(title)s.%(ext)s

# 默认合并为 MP4
--merge-output-format mp4

# 默认嵌入字幕
--embed-subs

# 限制并发数
--concurrent-fragments 4
```

### Windows 配置文件
创建 `C:\Users\你的用户名\yt-dlp.conf` 或 `yt-dlp.conf`：
```ini
-o C:/Videos/%(title)s.%(ext)s
--merge-output-format mp4
--embed-subs
```

## 使用技巧

1. **查看可用格式**：
   ```bash
   yt-dlp -F "视频URL"
   ```

2. **下载特定格式**：
   ```bash
   yt-dlp -f 格式ID "视频URL"
   ```

3. **创建归档文件**（避免重复下载）：
   ```bash
   yt-dlp --download-archive archive.txt "URL或文件"
   ```

4. **播放列表中排除已下载视频**：
   ```bash
   yt-dlp --download-archive downloaded.txt "播放列表URL"
   ```

5. **只下载新上传的视频**：
   ```bash
   yt-dlp --download-archive archive.txt "播放列表URL"
   ```

## 法律声明

⚠️ **重要提示**：
- 仅供个人学习和研究使用
- 请遵守相关平台的版权政策和使用条款
- 下载的内容不得用于商业用途
- 请尊重原创者的版权
- 某些平台可能禁止下载，请谨慎使用

## 参考资料

- yt-dlp GitHub: https://github.com/yt-dlp/yt-dlp
- yt-dlp 文档: https://github.com/yt-dlp/yt-dlp#readme
- 支持的网站列表: https://github.com/yt-dlp/yt-dlp/blob/master/supportedsites.md
