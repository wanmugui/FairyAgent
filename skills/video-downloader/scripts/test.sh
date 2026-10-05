#!/bin/bash

# 视频下载技能测试脚本

echo "🎬 视频下载技能测试"
echo "===================="
echo ""

# 颜色定义
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 测试计数
PASSED=0
FAILED=0

# 测试函数
test_command() {
    local name="$1"
    local command="$2"

    echo -n "测试 $name... "
    if eval "$command" > /dev/null 2>&1; then
        echo -e "${GREEN}✓ 通过${NC}"
        ((PASSED++))
        return 0
    else
        echo -e "${RED}✗ 失败${NC}"
        ((FAILED++))
        return 1
    fi
}

echo "📋 检查依赖..."
echo ""

# 检查 yt-dlp
test_command "yt-dlp 安装" "which yt-dlp"
test_command "yt-dlp 版本" "yt-dlp --version"

# 检查 FFmpeg
test_command "FFmpeg 安装" "which ffmpeg"
test_command "FFmpeg 版本" "ffmpeg -version"

# 检查配置目录
CONFIG_DIR="$HOME/.config/yt-dlp"
if [ -d "$CONFIG_DIR" ]; then
    echo -e "${GREEN}✓ 配置目录存在${NC}: $CONFIG_DIR"
    ((PASSED++))
else
    echo -e "${YELLOW}⚠ 配置目录不存在${NC}: $CONFIG_DIR"
    ((FAILED++))
fi

# 检查下载目录
DOWNLOAD_DIR="$HOME/Videos"
if [ -d "$DOWNLOAD_DIR" ]; then
    echo -e "${GREEN}✓ 下载目录存在${NC}: $DOWNLOAD_DIR"
    ((PASSED++))
else
    echo -e "${YELLOW}⚠ 下载目录不存在${NC}: $DOWNLOAD_DIR"
    ((FAILED++))
fi

echo ""
echo "📊 测试结果:"
echo "------------"
echo -e "${GREEN}通过: $PASSED${NC}"
echo -e "${RED}失败: $FAILED${NC}"
echo ""

# 功能测试（可选，需要网络）
echo "🌐 功能测试（可选）..."
echo ""

read -p "是否进行功能测试？需要网络连接 (y/N): " -n 1 -r
echo ""

if [[ $REPLY =~ ^[Yy]$ ]]; then
    echo "测试 YouTube 支持..."
    if yt-dlp --list-extractors | grep -q "youtube"; then
        echo -e "${GREEN}✓ YouTube 支持正常${NC}"
        ((PASSED++))
    else
        echo -e "${RED}✗ YouTube 支持异常${NC}"
        ((FAILED++))
    fi

    echo "测试 B站支持..."
    if yt-dlp --list-extractors | grep -q "bilibili"; then
        echo -e "${GREEN}✓ B站 支持正常${NC}"
        ((PASSED++))
    else
        echo -e "${RED}✗ B站 支持异常${NC}"
        ((FAILED++))
    fi

    echo "测试 Twitter 支持..."
    if yt-dlp --list-extractors | grep -q "twitter"; then
        echo -e "${GREEN}✓ Twitter 支持正常${NC}"
        ((PASSED++))
    else
        echo -e "${RED}✗ Twitter 支持异常${NC}"
        ((FAILED++))
    fi

    echo ""
    echo "📊 更新后的测试结果:"
    echo "------------"
    echo -e "${GREEN}通过: $PASSED${NC}"
    echo -e "${RED}失败: $FAILED${NC}"
    echo ""
fi

# 提供下一步建议
echo "💡 下一步建议:"
echo "------------"

if [ $FAILED -eq 0 ]; then
    echo -e "${GREEN}✓ 所有测试通过！${NC}"
    echo ""
    echo "开始使用:"
    echo "  yt-dlp \"视频URL\""
    echo ""
    echo "查看帮助:"
    echo "  yt-dlp --help"
    echo ""
    echo "查看快速参考:"
    echo "  cat $(pwd)/quick-reference.md"
else
    echo -e "${YELLOW}⚠ 发现 $FAILED 个问题${NC}"
    echo ""
    echo "请运行安装脚本:"
    echo "  macOS/Linux: ./install.sh"
    echo "  Windows:     install.bat"
fi

echo ""
echo "✨ 测试完成！"
