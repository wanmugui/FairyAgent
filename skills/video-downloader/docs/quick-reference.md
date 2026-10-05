# 视频下载快速参考卡

## 🚀 最常用命令

```bash
# 下载视频（最佳质量）
yt-dlp "URL"

# 🎯 下载视频 + 中英文字幕（推荐）
yt-dlp --write-subs --sub-langs zh-Hans,en --embed-subs "URL"

# 下载为 MP3
yt-dlp -x --audio-format mp3 "URL"

# 下载播放列表
yt-dlp "播放列表URL"

# 使用浏览器 cookies（获取最高画质）
yt-dlp --cookies-browser chrome "URL"
```

## 📱 平台快捷命令

### YouTube
```bash
# 下载视频
yt-dlp "YouTube URL"

# 下载播放列表
yt-dlp "YouTube Playlist URL"

# 下载字幕
yt-dlp --write-subs --sub-langs zh-Hans,en "YouTube URL"
```

### B站
```bash
# 下载视频
yt-dlp "B站 URL"

# 最高画质（需要 cookies）
yt-dlp --cookies-browser chrome "B站 URL"
```

### Twitter
```bash
yt-dlp "推文 URL"
```

### 抖音
```bash
yt-dlp "抖音分享链接"
```

## 🎯 质量选择

```bash
# 4K
yt-dlp -f "bestvideo[height<=2160]+bestaudio" "URL"

# 1080p
yt-dlp -f "bestvideo[height<=1080]+bestaudio" "URL"

# 720p
yt-dlp -f "bestvideo[height<=720]+bestaudio" "URL"

# 480p
yt-dlp -f "bestvideo[height<=480]+bestaudio" "URL"
```

## 📁 输出位置

```bash
# 指定文件名
yt-dlp -o "视频.mp4" "URL"

# 按平台分类
yt-dlp -o "%(extractor)s/%(title)s.%(ext)s" "URL"

# 按上传者分类
yt-dlp -o "%(uploader)s/%(title)s.%(ext)s" "URL"

# 完整路径
yt-dlp -o "~/Videos/YouTube/%(title)s.%(ext)s" "URL"
```

## 🛠️ 实用选项

```bash
# 查看可用格式
yt-dlp -F "URL"

# 限制下载速度（2MB/s）
yt-dlp --limit-rate 2M "URL"

# 使用代理
yt-dlp --proxy http://127.0.0.1:7890 "URL"

# 断点续传
yt-dlp --continue "URL"

# 只获取信息不下载
yt-dlp --skip-download --dump-json "URL"
```

## 💡 批量下载

```bash
# 从文件读取 URL 列表
yt-dlp -a urls.txt

# 下载并记录已下载
yt-dlp --download-archive archive.txt "URL"

# 只下载前 N 个
yt-dlp --playlist-end 10 "播放列表URL"
```

## 🔧 故障排除

```bash
# 更新 yt-dlp
yt-dlp --update

# 查看详细日志
yt-dlp --verbose "URL"

# 检查支持
yt-dlp --list-extractors | grep -i bilibili
```

## 📝 模板变量

| 变量 | 说明 | 示例 |
|------|------|------|
| `%(title)s` | 视频标题 | My Video |
| `%(uploader)s` | 上传者 | Channel Name |
| `%(upload_date)s` | 上传日期 | 20250127 |
| `%(extractor)s` | 网站名 | youtube, bilibili |
| `%(id)s` | 视频 ID | dQw4w9WgXcQ |
| `%(duration)s` | 时长（秒） | 300 |

## ⚠️ 常见错误

| 错误 | 原因 | 解决方案 |
|------|------|----------|
| `HTTP Error 403` | 需要登录 | 使用 `--cookies-browser chrome` |
| `video not found` | URL 错误或视频删除 | 检查 URL 是否正确 |
| `FFmpeg not found` | 未安装 FFmpeg | 安装 FFmpeg |
| `no suitable format` | 格式不支持 | 使用 `-f "best"` |
| `download interrupted` | 网络中断 | 使用 `--continue` |

## 🎨 配置示例

创建 `~/.config/yt-dlp/config` (macOS/Linux) 或 `yt-dlp.conf` (Windows)：

```ini
# 默认输出目录
-o ~/Videos/%(extractor)s/%(uploader)s/%(title)s.%(ext)s

# 合并为 MP4
--merge-output-format mp4

# 嵌入字幕
--embed-subs

# 并发数
--concurrent-fragments 4

# 限制速度
# --limit-rate 2M
```
