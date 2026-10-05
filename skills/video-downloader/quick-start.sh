#!/bin/bash

# 视频下载技能快速开始脚本

echo "🎬 视频下载技能 - 快速开始"
echo "================================"
echo ""

# 检查安装
echo "📋 检查环境..."
if command -v yt-dlp &> /dev/null; then
    echo "✅ yt-dlp 已安装: $(yt-dlp --version | head -n 1)"
else
    echo "❌ yt-dlp 未安装"
    echo ""
    echo "请先运行安装脚本："
    echo "  cd scripts && ./install.sh"
    exit 1
fi

if command -v ffmpeg &> /dev/null; then
    echo "✅ FFmpeg 已安装: $(ffmpeg -version | head -n 1)"
else
    echo "⚠️  FFmpeg 未安装（字幕嵌入需要）"
    echo ""
    echo "安装 FFmpeg："
    echo "  brew install ffmpeg  # macOS"
    echo "  sudo apt install ffmpeg  # Linux"
fi

echo ""
echo "================================"
echo "🚀 开始下载"
echo "================================"
echo ""

# 提示输入 URL
read -p "请输入视频 URL: " URL

if [ -z "$URL" ]; then
    echo "❌ URL 不能为空"
    exit 1
fi

echo ""
echo "选择下载选项："
echo "  1) 下载视频（最佳质量）"
echo "  2) 下载视频 + 中英文字幕（推荐）"
echo "  3) 下载所有字幕"
echo "  4) 只下载音频（MP3）"
echo "  5) 只下载字幕不下载视频"
echo ""
read -p "请选择 (1-5): " choice

case $choice in
    1)
        echo ""
        echo "📥 下载视频..."
        yt-dlp -o "./downloads/%(extractor)s/%(uploader)s/%(title)s.%(ext)s" "$URL"
        ;;
    2)
        echo ""
        echo "📥 下载视频 + 中英文字幕..."
        yt-dlp --write-subs --sub-langs zh-Hans,en --embed-subs \
               -o "./downloads/%(extractor)s/%(uploader)s/%(title)s.%(ext)s" "$URL"
        ;;
    3)
        echo ""
        echo "📥 下载视频 + 所有字幕..."
        yt-dlp --write-subs --sub-langs all --embed-subs \
               -o "./downloads/%(extractor)s/%(uploader)s/%(title)s.%(ext)s" "$URL"
        ;;
    4)
        echo ""
        echo "🎵 提取音频为 MP3..."
        yt-dlp -x --audio-format mp3 \
               -o "./downloads/Audio/%(title)s.%(ext)s" "$URL"
        ;;
    5)
        echo ""
        echo "📝 只下载字幕..."
        yt-dlp --skip-download --write-subs --sub-langs all --convert-subs srt \
               -o "./downloads/Subtitles/%(title)s.%(ext)s" "$URL"
        ;;
    *)
        echo "❌ 无效选择"
        exit 1
        ;;
esac

echo ""
echo "================================"
echo "✅ 下载完成！"
echo "================================"
echo ""
echo "📁 文件保存在: ./downloads/"
echo ""
echo "查看下载文件："
echo "  ls -lh downloads/"
echo ""
