# 视频下载技能 (Video Downloader Skill)

这是一个用于 Claude Code 的视频下载技能，支持从 YouTube、B站、Twitter、微信视频号等多个平台下载视频。

## 功能特性

✅ **多平台支持**：YouTube、B站、Twitter/X、抖音、快手等
✅ **灵活下载**：支持选择质量、提取音频、批量下载
✅ **📝 字幕下载**：自动下载并嵌入字幕（中英文字幕）
✅ **播放列表**：支持批量下载播放列表
✅ **断点续传**：下载中断后可继续
✅ **格式转换**：自动合并视频和音频流

## 快速开始

### 1. 安装依赖

#### macOS / Linux
```bash
chmod +x install.sh
./install.sh
```

#### Windows
```batch
install.bat
```

### 2. 验证安装
```bash
yt-dlp --version
```

### 3. 开始下载
```bash
# 下载视频
yt-dlp "视频URL"

# 下载音频
yt-dlp -x --audio-format mp3 "视频URL"
```

## 在 Claude Code 中使用

安装此技能后，你可以这样使用：

```
你：使用 video-downloader 技能帮我下载这个 YouTube 视频：https://www.youtube.com/watch?v=xxx

Claude：我会使用 yt-dlp 下载这个视频...
[执行下载命令]

你：帮我把这个 B 站系列视频全部下载到 ~/Videos/B站/ 目录

Claude：我会批量下载这个系列视频...
[执行批量下载并分类保存]
```

## 支持的平台

| 平台 | 状态 |
|------|------|
| YouTube | ✅ 完全支持 |
| B站 | ✅ 完全支持 |
| Twitter/X | ✅ 完全支持 |
| 抖音 | ✅ 支持 |
| 快手 | ✅ 支持 |
| 微信视频号 | ⚠️ 有限支持 |
| 其他 1000+ 网站 | 📋 查看 [支持列表](https://github.com/yt-dlp/yt-dlp/blob/master/supportedsites.md) |

## 常用命令

### YouTube
```bash
# 下载播放列表
yt-dlp "播放列表URL"

# 下载字幕
yt-dlp --write-subs --sub-langs all "视频URL"
```

### B站
```bash
# 使用浏览器 cookies 获取最高画质
yt-dlp --cookies-browser chrome "B站视频URL"
```

### Twitter
```bash
# 下载推文视频
yt-dlp "推文URL"
```

### 批量下载
```bash
# 从文件读取 URL 列表
yt-dlp -a urls.txt
```

## 配置文件

安装后会自动创建配置文件：
- **macOS/Linux**: `~/.config/yt-dlp/config`
- **Windows**: `%USERPROFILE%\yt-dlp.conf`

默认配置：
- 视频保存到 `~/Videos` 或 `C:\Users\用户名\Videos`
- 自动合并为 MP4 格式
- 嵌入可用字幕
- 并发下载数: 4

## 文件结构

```
video-downloader/
├── SKILL.md           # 技能说明文档（主要文件）
├── README.md          # 本文件
├── install.sh         # macOS/Linux 安装脚本
├── install.bat        # Windows 安装脚本
└── examples.txt       # 使用示例
```

## 故障排除

### Q: 下载速度慢？
A: 使用代理或调整并发数：
```bash
yt-dlp --concurrent-fragments 4 --proxy http://代理地址 "URL"
```

### Q: B站只能低画质？
A: 需要使用浏览器 cookies：
```bash
yt-dlp --cookies-browser chrome "B站URL"
```

### Q: 视频没有声音？
A: 安装 FFmpeg：
```bash
brew install ffmpeg  # macOS
sudo apt install ffmpeg  # Linux
```

### Q: 如何更新 yt-dlp？
A: 运行：
```bash
yt-dlp --update
```

## 法律声明

⚠️ **重要提示**：
- 仅供个人学习和研究使用
- 请遵守相关平台的版权政策
- 下载的内容不得用于商业用途
- 请尊重原创者的版权

## 参考资料

- [yt-dlp GitHub](https://github.com/yt-dlp/yt-dlp)
- [yt-dlp 文档](https://github.com/yt-dlp/yt-dlp#readme)
- [支持网站列表](https://github.com/yt-dlp/yt-dlp/blob/master/supportedsites.md)

## 许可证

本技能遵循 MIT 许可证。
