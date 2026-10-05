# 平台专属使用指南

## 📺 YouTube

### 基础下载

```bash
# 下载单个视频
yt-dlp "https://www.youtube.com/watch?v=VIDEO_ID"

# 下载短视频
yt-dlp "https://youtube.com/shorts/VIDEO_ID"

# 下载直播回放
yt-dlp "https://www.youtube.com/live/LIVE_ID"
```

### 播放列表操作

```bash
# 下载整个播放列表
yt-dlp "https://www.youtube.com/playlist?list=PLAYLIST_ID"

# 只下载前 5 个视频
yt-dlp --playlist-end 5 "播放列表URL"

# 从第 10 个开始下载
yt-dlp --playlist-start 10 "播放列表URL"

# 下载指定范围（第 5-15 个）
yt-dlp --playlist-start 5 --playlist-end 15 "播放列表URL"

# 反向下载（从最新到最旧）
yt-dlp --playlist-reverse "播放列表URL"

# 随机下载
yt-dlp --playlist-random "播放列表URL"
```

### 质量选择

```bash
# 1080p + 最佳音频
yt-dlp -f "bestvideo[height<=1080]+bestaudio/best[height<=1080]" "URL"

# 720p（更快）
yt-dlp -f "bestvideo[height<=720]+bestaudio/best[height<=720]" "URL"

# 4K 如果可用
yt-dlp -f "bestvideo[height<=2160]+bestaudio" "URL"
```

### 字幕处理

```bash
# 下载所有字幕
yt-dlp --write-subs --sub-langs all "URL"

# 下载中文字幕
yt-dlp --write-subs --sub-langs zh-Hans,zh-Hant "URL"

# 下载自动生成的字幕
yt-dlp --write-auto-subs --sub-langs zh-Hans,en "URL"

# 嵌入字幕到视频
yt-dlp --embed-subs --sub-langs zh-Hans,en "URL"

# 只下载字幕不下载视频
yt-dlp --skip-download --write-subs "URL"
```

### 高级功能

```bash
# 获取视频信息
yt-dlp --dump-json "URL" | jq '.'

# 只下载音频
yt-dlp -x --audio-format mp3 "URL"

# 下载为 FLAC（无损）
yt-dlp -x --audio-format flac "URL"

# 指定输出模板
yt-dlp -o "%(title)s-%(id)s.%(ext)s" "URL"

# 下载视频描述
yt-dlp --write-description "URL"

# 下载缩略图
yt-dlp --write-thumbnail "URL"

# 下载所有元数据
yt-dlp --write-info-json --write-description --write-thumbnail "URL"
```

---

## 📺 B站（哔哩哔哩）

### 基础下载

```bash
# 下载单个视频
yt-dlp "https://www.bilibili.com/video/BV..."

# 下载分 P 视频
yt-dlp "https://www.bilibili.com/video/BV...?p=2"
```

### 高质量下载（需要 Cookies）

```bash
# 方法1：使用浏览器 cookies
yt-dlp --cookies-browser chrome "B站URL"

# 方法2：使用 cookies 文件
yt-dlp --cookies cookies.txt "B站URL"

# 导出 cookies（需要 browser-cookie-exporter）
# 从 Chrome 导出
browser-cookie-exporter chrome > cookies.txt
```

### 系列和合集

```bash
# 下载系列视频
yt-dlp "https://www.bilibili.com/medialist/detail/ml..."

# 下载收藏夹
yt-dlp "https://www.bilibili.com/medialist/detail/fav..."

# 下载用户上传列表
yt-dlp "https://space.bilibili.com/UID/video"
```

### B站专属功能

```bash
# 下载弹幕（需要额外工具）
yt-dlp --write-subs "B站URL"

# 下载 CC 字幕
yt-dlp --write-subs --sub-langs zh-Hans "B站URL"

# 指定清晰度
yt-dlp -f "bestvideo[height<=1080]+bestaudio" "B站URL"
```

### B站注意事项

⚠️ **B站限制：**
- 需要登录才能下载高画质视频
- 使用 `--cookies-browser chrome` 可以获取登录状态
- 部分 4K 视频需要大会员

💡 **提示：**
```bash
# 更新 yt-dlp 以获得最佳 B站 支持
yt-dlp --update
```

---

## 🐦 Twitter / X

### 基础下载

```bash
# 下载推文视频
yt-dlp "https://twitter.com/user/status/TWEET_ID"

# 下载 X 平台视频
yt-dlp "https://x.com/user/status/TWEET_ID"
```

### 批量下载

```bash
# 下载用户所有视频
yt-dlp "https://twitter.com/USERNAME"

# 下载用户媒体（含图片）
yt-dlp -i "https://twitter.com/USERNAME/media"

# 下载搜索结果
yt-dlp "https://twitter.com/search?q=关键词"
```

### Twitter 专属选项

```bash
# 下载高质量版本
yt-dlp -f "best" "推文URL"

# 下载多个推文
yt-dlp "推文1 URL" "推文2 URL" "推文3 URL"

# 只下载不转换
yt-dlp --keep-video "推文URL"
```

---

## 🎵 抖音

### 基础下载

```bash
# 使用分享链接
yt-dlp "https://v.douyin.com/xxxxx/"

# 使用完整链接
yt-dlp "https://www.douyin.com/video/VIDEO_ID"
```

### 去除水印

```bash
# 尝试下载无水印版本
yt-dlp --format "best" "抖音URL"

# 下载原始视频
yt-dlp -f "best" "抖音URL"
```

### 批量下载

```bash
# 下载用户视频
yt-dlp "https://www.douyin.com/user/USER_ID"

# 下载合集
yt-dlp "抖音合集URL"
```

---

## 🎬 快手

### 基础下载

```bash
# 使用分享链接
yt-dlp "https://kuaishou.com/short-video/VIDEO_ID"

# 使用完整链接
yt-dlp "https://www.kuaishou.com/short-video/VIDEO_ID"
```

### 批量下载

```bash
# 下载用户视频
yt-dlp "https://www.kuaishou.com/profile/USER_ID"
```

---

## 💬 微信视频号

### 下载方法

微信视频号下载较为特殊，通常需要：

```bash
# 方法1：使用分享链接
yt-dlp "微信视频号分享链接"

# 方法2：在浏览器中打开视频
# 1. 复制视频实际 URL
# 2. 使用 yt-dlp 下载
yt-dlp "实际视频URL"
```

### 注意事项

⚠️ **微信视频号限制：**
- 直接分享链接可能无效
- 需要在浏览器中获取实际 URL
- yt-dlp 支持可能会变化

💡 **建议：**
```bash
# 定期更新 yt-dlp
yt-dlp --update

# 检查支持状态
yt-dlp --list-extractors | grep -i wechat
```

---

## 📝 通用技巧

### URL 处理

```bash
# URL 含有特殊字符时使用引号
yt-dlp '非常复杂的URL'

# URL 过长时使用文件
echo "长URL" > url.txt
yt-dlp -a url.txt
```

### 错误处理

```bash
# 重试失败的下载
yt-dlp --retries 10 "URL"

# 忽略错误继续
yt-dlp --ignore-errors "URL"

# 使用代理
yt-dlp --proxy http://127.0.0.1:7890 "URL"
```

### 性能优化

```bash
# 增加并发片段数
yt-dlp --concurrent-fragments 8 "URL"

# 限制下载速度
yt-dlp --limit-rate 2M "URL"

# 使用外部下载器
yt-dlp --external-downloader aria2c "URL"
```

### 文件管理

```bash
# 创建归档避免重复
yt-dlp --download-archive archive.txt "URL"

# 按日期分类
yt-dlp -o "%(upload_date)s/%(title)s.%(ext)s" "URL"

# 按播放列表分类
yt-dlp -o "%(playlist_title)s/%(playlist_index)s-%(title)s.%(ext)s" "播放列表URL"
```
