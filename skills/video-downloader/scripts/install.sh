#!/bin/bash

# 视频下载工具安装脚本
# 支持 macOS 和 Linux

echo "🎬 视频下载工具安装脚本"
echo "=========================="
echo ""

# 检测操作系统
if [[ "$OSTYPE" == "darwin"* ]]; then
    OS="macOS"
elif [[ "$OSTYPE" == "linux-gnu"* ]]; then
    OS="Linux"
else
    echo "❌ 不支持的操作系统: $OSTYPE"
    exit 1
fi

echo "📱 检测到操作系统: $OS"
echo ""

# 检查 Homebrew（macOS）
if [[ "$OS" == "macOS" ]]; then
    if ! command -v brew &> /dev/null; then
        echo "📦 Homebrew 未安装，正在安装..."
        /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
    else
        echo "✅ Homebrew 已安装"
    fi
fi

# 安装 yt-dlp
echo ""
echo "📥 安装 yt-dlp..."
if [[ "$OS" == "macOS" ]]; then
    brew install yt-dlp
else
    sudo curl -L https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp -o /usr/local/bin/yt-dlp
    sudo chmod a+rx /usr/local/bin/yt-dlp
fi

# 安装 FFmpeg
echo ""
echo "🎥 安装 FFmpeg..."
if [[ "$OS" == "macOS" ]]; then
    brew install ffmpeg
else
    sudo apt install ffmpeg -y
fi

# 验证安装
echo ""
echo "✅ 验证安装..."
echo ""
yt-dlp --version
ffmpeg -version | head -n 1

# 创建配置目录
CONFIG_DIR="$HOME/.config/yt-dlp"
if [[ ! -d "$CONFIG_DIR" ]]; then
    echo ""
    echo "📁 创建配置目录: $CONFIG_DIR"
    mkdir -p "$CONFIG_DIR"
fi

# 创建默认配置
echo ""
echo "⚙️  创建默认配置..."
cat > "$CONFIG_DIR/config" << 'EOF'
# 视频下载默认配置

# 保存到用户视频目录
-o ~/Videos/%(extractor)s/%(uploader)s/%(title)s.%(ext)s

# 合并为 MP4 格式
--merge-output-format mp4

# 嵌入字幕
--embed-subs

--embed-chapters

# 并发片段数
--concurrent-fragments 4

# 限制下载速度（可选，取消注释以启用）
# --limit-rate 2M
EOF

echo "✅ 配置文件已创建: $CONFIG_DIR/config"
echo ""

# 创建下载目录
DOWNLOAD_DIR="$HOME/Videos"
if [[ ! -d "$DOWNLOAD_DIR" ]]; then
    mkdir -p "$DOWNLOAD_DIR"
    echo "📁 创建下载目录: $DOWNLOAD_DIR"
fi

echo ""
echo "🎉 安装完成！"
echo ""
echo "📋 快速开始："
echo "   下载视频:     yt-dlp '视频URL'"
echo "   下载音频:     yt-dlp -x --audio-format mp3 '视频URL'"
echo "   下载播放列表: yt-dlp '播放列表URL'"
echo "   查看帮助:     yt-dlp --help"
echo ""
echo "⚙️  配置文件位置: $CONFIG_DIR/config"
echo ""
