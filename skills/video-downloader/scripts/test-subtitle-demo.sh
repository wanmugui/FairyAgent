#!/bin/bash

# 字幕下载功能演示脚本
# 展示如何使用 yt-dlp 下载视频字幕

echo "📝 视频字幕下载功能演示"
echo "=========================="
echo ""
echo "✅ 环境检查..."
echo ""

# 检查安装
echo -n "yt-dlp 版本: "
yt-dlp --version | head -n 1

echo -n "FFmpeg 版本: "
ffmpeg -version | head -n 1

echo ""
echo "================================"
echo "📋 字幕下载功能测试"
echo "================================"
echo ""

# 测试 1：列出可用字幕
echo "🔍 测试 1：查看视频的可用字幕"
echo "-----------------------------------"
echo "测试视频：Big Buck Bunny (开源测试视频)"
echo ""

TEST_URL="https://www.youtube.com/watch?v=PBkGNCgQVL8"

echo "命令："
echo "yt-dlp --list-subs \"$TEST_URL\""
echo ""

# 执行测试
yt-dlp --list-subs "$TEST_URL" --no-update 2>&1 | grep -A 20 "Available subtitles" | head -n 25 || echo "（需要 cookies 或该视频无字幕）"

echo ""
echo "💡 提示：如果看到 'Available subtitles' 列表，说明该视频有字幕"
echo ""

# 测试 2：模拟字幕下载
echo "================================"
echo "🎯 测试 2：字幕下载命令演示"
echo "-----------------------------------"
echo ""

echo "1️⃣  下载视频 + 中英文字幕（推荐）"
echo "   命令："
echo "   yt-dlp --write-subs --sub-langs zh-Hans,en --embed-subs \"视频URL\""
echo ""

echo "2️⃣  只下载字幕不下载视频"
echo "   命令："
echo "   yt-dlp --skip-download --write-subs --sub-langs all \"视频URL\""
echo ""

echo "3️⃣  下载自动生成的字幕"
echo "   命令："
echo "   yt-dlp --write-auto-subs --sub-langs zh-Hans,en \"视频URL\""
echo ""

echo "4️⃣  转换字幕格式为 SRT"
echo "   命令："
echo "   yt-dlp --write-subs --convert-subs srt \"视频URL\""
echo ""

# 测试 3：字幕格式说明
echo "================================"
echo "📚 字幕格式说明"
echo "-----------------------------------"
echo ""

cat << 'EOF'
SRT (SubRip)   .srt  - 最通用，兼容性好，推荐
VTT (WebVTT)   .vtt  - Web 标准，用于在线播放
ASS (Advanced) .ass  - 支持样式和特效，适合动漫
LRC            .lrc  - 歌词格式，用于音乐
EOF

echo ""

# 测试 4：语言代码
echo "================================"
echo "🌍 常用语言代码"
echo "-----------------------------------"
echo ""

cat << 'EOF'
中文简体    zh-Hans
中文繁体    zh-Hant
英语        en
日语        ja
韩语        ko
法语        fr
德语        de
西班牙语    es
俄语        ru
EOF

echo ""

# 测试 5：配置文件
echo "================================"
echo "⚙️  配置文件设置"
echo "-----------------------------------"
echo ""

echo "要默认启用字幕下载，编辑配置文件："
echo ""
echo "macOS/Linux: ~/.config/yt-dlp/config"
echo "Windows:     %USERPROFILE%\yt-dlp.conf"
echo ""
echo "添加以下内容："
cat << 'EOF'
# 自动下载中英文字幕
--write-subs
--sub-langs zh-Hans,en

# 嵌入字幕到视频
--embed-subs
EOF

echo ""

# 实际测试选项
echo "================================"
echo "🧪 实际测试选项"
echo "-----------------------------------"
echo ""

read -p "是否进行实际下载测试？会下载一个短视频 (y/N): " -n 1 -r
echo ""

if [[ $REPLY =~ ^[Yy]$ ]]; then
    echo ""
    echo "📥 开始实际测试..."
    echo ""

    # 创建测试目录
    TEST_DIR="$HOME/Desktop/video-downloader-test"
    mkdir -p "$TEST_DIR"
    echo "✅ 测试目录: $TEST_DIR"

    # 测试一个简单的视频（使用 cookies）
    echo ""
    echo "正在下载测试视频的字幕..."
    echo ""

    # 使用一个简短的公开视频
    DEMO_URL="https://www.youtube.com/watch?v=PBkGNCgQVL8"

    # 只下载字幕
    echo "命令："
    echo "cd \"$TEST_DIR\""
    echo "yt-dlp --skip-download --write-subs --sub-langs en --convert-subs srt \"$DEMO_URL\""
    echo ""

    cd "$TEST_DIR"
    yt-dlp --skip-download --write-subs --sub-langs en --convert-subs srt "$DEMO_URL" --no-update 2>&1 | tail -n 10

    echo ""
    echo "📁 查看下载的字幕文件："
    ls -lh *.srt 2>/dev/null || echo "（未找到字幕文件，可能需要认证）"

    echo ""
    echo "✅ 测试完成！"
else
    echo ""
    echo "⏭️  跳过实际测试"
fi

echo ""
echo "================================"
echo "💡 使用建议"
echo "================================"
echo ""

cat << 'EOF'
1. YouTube 字幕下载：
   - 优先使用人工字幕：--write-subs
   - 备选自动字幕：--write-auto-subs
   - 需要登录时使用：--cookies-browser chrome

2. B站字幕下载：
   - CC 字幕：--write-subs
   - 需要登录获取高质量：--cookies-browser chrome

3. 只下载字幕做笔记：
   - 使用 --skip-download 只下载字幕
   - 转换为 SRT 格式便于阅读

4. 嵌入字幕到视频：
   - 使用 --embed-subs 自动嵌入
   - 确保安装了 FFmpeg

5. 批量下载字幕：
   - 播放列表：yt-dlp --skip-download --write-subs "播放列表URL"
   - URL 列表：yt-dlp --skip-download --write-subs -a urls.txt
EOF

echo ""
echo "✨ 演示完成！"
echo ""
echo "📚 查看详细文档："
echo "   cat subtitle-guide.md"
echo ""
