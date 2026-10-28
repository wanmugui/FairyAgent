# 字幕下载功能测试与故障排除

## ✅ 环境验证

### 1. 检查工具安装

```bash
# 检查 yt-dlp
yt-dlp --version

# 检查 FFmpeg
ffmpeg -version | head -n 1

# 检查字幕支持
yt-dlp --list-extractors | grep -i subtitle
```

### 2. 测试基础功能

```bash
# 测试 1：查看字幕可用性
yt-dlp --list-subs "https://www.youtube.com/watch?v=PBkGNCgQVL8"

# 预期输出：
# [youtube] Extracting URL: ...
# Available subtitles for 视频:
# Language Formats
# zh-Hans  vtt, srv3, srv2, srv1
# en       vtt, srv3, srv2, srv1
```

## 🧪 功能测试清单

### ✅ 测试 1：列出字幕

```bash
# YouTube 测试
yt-dlp --list-subs "YouTube视频URL" --no-update

# B站测试
yt-dlp --list-subs "B站视频URL" --no-update

# 检查输出是否包含 "Available subtitles"
```

### ✅ 测试 2：下载字幕文件

```bash
# 只下载字幕（不下载视频）
yt-dlp --skip-download --write-subs --sub-langs en "视频URL" --no-update

# 预期结果：
# 生成 .vtt 或 .srt 字幕文件
# ls -l *.vtt *.srt
```

### ✅ 测试 3：下载视频 + 字幕

```bash
# 下载视频并嵌入字幕
yt-dlp --write-subs --sub-langs en --embed-subs "视频URL" --no-update

# 预期结果：
# 视频文件中包含字幕轨道
# 可以用播放器查看字幕
```

### ✅ 测试 4：多语言字幕

```bash
# 下载中英文字幕
yt-dlp --write-subs --sub-langs zh-Hans,en --embed-subs "视频URL" --no-update

# 预期结果：
# 视频包含两个字幕轨道
```

### ✅ 测试 5：字幕格式转换

```bash
# 转换为 SRT
yt-dlp --write-subs --convert-subs srt "视频URL" --no-update

# 预期结果：
# 生成 .srt 格式字幕
```

## 🔧 常见问题与解决方案

### ❌ 问题 1：找不到字幕

**错误信息：**
```
WARNING: video doesn't have subtitles
```

**原因：**
- 视频确实没有字幕
- 需要登录才能查看字幕
- 使用了错误的语言代码

**解决方案：**

```bash
# 1. 检查字幕可用性
yt-dlp --list-subs "视频URL"

# 2. 使用浏览器 cookies
yt-dlp --cookies-browser chrome --list-subs "视频URL"

# 3. 尝试自动字幕
yt-dlp --write-auto-subs --sub-langs all "视频URL"
```

### ❌ 问题 2：无法嵌入字幕

**错误信息：**
```
ERROR: FFmpeg not found
```

**原因：**
FFmpeg 未安装或不在 PATH 中

**解决方案：**

```bash
# macOS
brew install ffmpeg

# Linux
sudo apt install ffmpeg  # Ubuntu/Debian
sudo yum install ffmpeg  # CentOS/RHEL

# Windows
# 使用 Scoop
scoop install ffmpeg

# 或下载：https://ffmpeg.org/download.html
```

### ❌ 问题 3：YouTube 需要认证

**错误信息：**
```
ERROR: Sign in to confirm you're not a bot
```

**解决方案：**

```bash
# 方法 1：使用浏览器 cookies
yt-dlp --cookies-browser chrome "视频URL"

# 方法 2：导出 cookies 文件
# 1. 安装 browser-cookie-exporter
pip install browser-cookie-exporter

# 2. 导出 cookies
browser-cookie-exporter chrome > cookies.txt

# 3. 使用 cookies 文件
yt-dlp --cookies cookies.txt "视频URL"
```

### ❌ 问题 4：字幕格式不支持

**错误信息：**
```
ERROR: Unsupported subtitle format
```

**解决方案：**

```bash
# 转换为支持的格式
yt-dlp --write-subs --convert-subs srt "视频URL"

# 或使用 FFmpeg 转换
ffmpeg -i input.vtt output.srt
```

### ❌ 问题 5：字幕与视频不同步

**解决方案：**

```bash
# 使用 FFmpeg 调整时间偏移
# 延迟 0.5 秒
ffmpeg -i video.mp4 -itsoffset 0.5 -i subs.srt \
       -c copy -map 0 -map 1 output.mp4

# 提前 0.5 秒
ffmpeg -i video.mp4 -itsoffset -0.5 -i subs.srt \
       -c copy -map 0 -map 1 output.mp4
```

### ❌ 问题 6：B站无法下载高画质

**解决方案：**

```bash
# 使用浏览器 cookies
yt-dlp --cookies-browser chrome "B站URL"

# 或手动导出 cookies
# 1. 登录 B站
# 2. 使用浏览器扩展导出 cookies
# 3. 使用 cookies 文件
yt-dlp --cookies cookies.txt "B站URL"
```

### ❌ 问题 7：自动字幕不准确

**解决方案：**

```bash
# 优先使用人工字幕
yt-dlp --write-subs --no-write-auto-subs "视频URL"

# 检查是否有其他语言的字幕
yt-dlp --list-subs "视频URL"

# 组合使用人工和自动字幕
yt-dlp --write-subs --write-auto-subs "视频URL"
```

## 🎯 平台特定测试

### YouTube 字幕测试

```bash
# 测试视频（Big Buck Bunny）
TEST_URL="https://www.youtube.com/watch?v=PBkGNCgQVL8"

# 1. 列出字幕
yt-dlp --list-subs "$TEST_URL"

# 2. 下载英文字幕
yt-dlp --skip-download --write-subs --sub-langs en "$TEST_URL"

# 3. 下载视频 + 字幕
yt-dlp --write-subs --sub-langs en --embed-subs "$TEST_URL"
```

### B站字幕测试

```bash
# 测试视频（需要 cookies）
# 1. 列出字幕
yt-dlp --list-subs "B站URL" --cookies-browser chrome

# 2. 下载 CC 字幕
yt-dlp --write-subs --embed-subs "B站URL" --cookies-browser chrome

# 3. 只下载字幕
yt-dlp --skip-download --write-subs "B站URL" --cookies-browser chrome
```

### 其他平台测试

```bash
# Twitter
yt-dlp --list-subs "推文URL"

# 抖音
yt-dlp --list-subs "抖音URL"

# 通用测试
yt-dlp --list-subs "视频URL"
```

## 📊 测试报告模板

完成测试后，填写此报告：

```markdown
# 字幕下载测试报告

**测试日期：** 2025-01-27
**yt-dlp 版本：** 2025.10.14
**FFmpeg 版本：** 8.0

## 环境检查
- [x] yt-dlp 已安装
- [x] FFmpeg 已安装
- [x] 配置文件已创建

## 功能测试
- [ ] 列出字幕功能
- [ ] 下载字幕文件
- [ ] 嵌入字幕到视频
- [ ] 多语言字幕
- [ ] 字幕格式转换

## 平台测试
- [ ] YouTube 字幕
- [ ] B站 CC 字幕
- [ ] Twitter 字幕
- [ ] 其他平台

## 遇到的问题
1. 问题描述
   - 错误信息
   - 解决方案

## 测试结论
- [ ] 全部通过
- [ ] 部分通过
- [ ] 需要修复
```

## 🚀 快速测试脚本

创建并运行此脚本：

```bash
#!/bin/bash
echo "🧪 字幕功能快速测试"
echo ""

# 测试 1：环境
echo "1️⃣  环境检查"
yt-dlp --version
ffmpeg -version | head -n 1
echo ""

# 测试 2：列出字幕
echo "2️⃣  列出字幕测试"
yt-dlp --list-subs "https://www.youtube.com/watch?v=PBkGNCgQVL8" --no-update 2>&1 | grep -i "subtitle" | head -n 5
echo ""

# 测试 3：下载字幕
echo "3️⃣  下载字幕测试"
mkdir -p test-subs
cd test-subs
yt-dlp --skip-download --write-subs --sub-langs en --convert-subs srt \
       "https://www.youtube.com/watch?v=PBkGNCgQVL8" --no-update 2>&1 | tail -n 3

echo ""
echo "📁 字幕文件："
ls -lh *.srt 2>/dev/null || echo "未生成字幕文件"
cd ..
echo ""

echo "✅ 测试完成"
```

## 📞 获取帮助

如果遇到问题：

1. **查看文档：**
   ```bash
   yt-dlp --help | grep -A 5 "subtitle"
   ```

2. **启用详细日志：**
   ```bash
   yt-dlp --verbose "视频URL"
   ```

3. **报告问题：**
   - GitHub: https://github.com/yt-dlp/yt-dlp/issues
   - 搜索已有问题

4. **更新工具：**
   ```bash
   pip install -U yt-dlp
   ```

## ✅ 成功标志

测试成功的标志：

✅ 可以列出可用字幕
✅ 字幕文件正常生成
✅ 字幕可以嵌入视频
✅ 播放器可以显示字幕
✅ 多语言字幕正常工作
✅ 字幕格式转换成功

如果以上都通过，说明字幕下载功能完全正常！
