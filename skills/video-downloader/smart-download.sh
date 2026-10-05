#!/bin/bash

# 智能视频下载脚本
# 自动检测是否需要登录，并引导用户使用浏览器 cookies

set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 项目根目录
PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DOWNLOAD_DIR="$PROJECT_DIR/downloads"

# 确保下载目录存在
mkdir -p "$DOWNLOAD_DIR"

# 打印带颜色的消息
print_info() {
    echo -e "${BLUE}ℹ️  $1${NC}"
}

print_success() {
    echo -e "${GREEN}✅ $1${NC}"
}

print_warning() {
    echo -e "${YELLOW}⚠️  $1${NC}"
}

print_error() {
    echo -e "${RED}❌ $1${NC}"
}

print_header() {
    echo -e "\n${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${BLUE}$1${NC}"
    echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}\n"
}

# 检测 yt-dlp 路径
YTDLP_PATH=""
for path in \
    "/Library/Frameworks/Python.framework/Versions/3.12/bin/yt-dlp" \
    "/usr/local/bin/yt-dlp" \
    "$HOME/.local/bin/yt-dlp" \
    "yt-dlp"
do
    if command -v "$path" &> /dev/null || [ -x "$path" ]; then
        YTDLP_PATH="$path"
        break
    fi
done

if [ -z "$YTDLP_PATH" ]; then
    print_error "找不到 yt-dlp，请先运行 install.sh 安装"
    exit 1
fi

print_info "使用 yt-dlp: $YTDLP_PATH"

# 检测操作系统
if [[ "$OSTYPE" == "darwin"* ]]; then
    PLATFORM="macOS"
    BROWSER_COOKIES_CMD=""
elif [[ "$OSTYPE" == "linux-gnu"* ]]; then
    PLATFORM="Linux"
    BROWSER_COOKIES_CMD=""
else
    PLATFORM="Windows"
fi

# 尝试下载视频
try_download() {
    local url="$1"
    local use_cookies="$2"
    local cookies_file="$3"

    print_header "开始下载视频"

    local cmd="$YTDLP_PATH --write-subs --sub-langs zh-Hans,en --embed-subs"

    if [ "$use_cookies" = "true" ] && [ -n "$cookies_file" ]; then
        cmd="$cmd --cookies $cookies_file"
        print_info "使用浏览器 cookies"
    fi

    cmd="$cmd -o '$DOWNLOAD_DIR/%(extractor)s/%(uploader)s/%(title)s.%(ext)s'"
    cmd="$cmd '$url'"

    print_info "执行命令: $cmd"

    if eval "$cmd"; then
        print_success "下载完成！"
        print_info "文件保存在: $DOWNLOAD_DIR"
        return 0
    else
        return 1
    fi
}

# 导出浏览器 cookies
export_cookies() {
    print_header "导出浏览器 Cookies"

    local cookies_file="$DOWNLOAD_DIR/.cookies.txt"

    print_info "请选择你的浏览器:"
    echo "1) Chrome"
    echo "2) Safari (仅 macOS)"
    echo "3) Firefox"
    echo "4) Edge"
    echo "5) 取消"
    read -p "请输入选项 (1-5): " browser_choice

    local browser_name=""
    case $browser_choice in
        1)
            browser_name="chrome"
            ;;
        2)
            if [ "$PLATFORM" = "macOS" ]; then
                browser_name="safari"
            else
                print_error "Safari 仅在 macOS 上可用"
                return 1
            fi
            ;;
        3)
            browser_name="firefox"
            ;;
        4)
            browser_name="edge"
            ;;
        5)
            return 1
            ;;
        *)
            print_error "无效的选项"
            return 1
            ;;
    esac

    print_info "正在从 $browser_name 导出 cookies..."

    # 使用 browser-cookie3 库或手动方法
    # 这里提供一个简单的 Python 脚本方法
    if command -v python3 &> /dev/null; then
        python3 "$PROJECT_DIR/scripts/export-cookies.py" "$browser_name" "$cookies_file"
        if [ $? -eq 0 ]; then
            print_success "Cookies 导出成功！"
            echo "$cookies_file"
            return 0
        else
            print_error "Cookies 导出失败"
            return 1
        fi
    else
        print_error "需要 Python 3 才能导出 cookies"
        return 1
    fi
}

# 手动登录方法
manual_login_guide() {
    print_header "手动登录指南"

    cat << 'EOF'

某些网站（如小红书、B站、YouTube 等）需要登录才能访问。

请按以下步骤操作：

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

方法 1: 使用浏览器扩展（推荐）

1️⃣ 安装 "Get cookies.txt" 浏览器扩展:
   Chrome: https://chrome.google.com/webstore
   搜索 "Get cookies.txt LOCALLY"

2️⃣ 打开视频网站并在浏览器中登录

3️⃣ 点击扩展图标，下载 cookies.txt

4️⃣ 将 cookies.txt 文件放到:
   $DOWNLOAD_DIR/cookies.txt

5️⃣ 重新运行下载命令

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

方法 2: 使用浏览器开发工具

1️⃣ 在浏览器中打开视频网站并登录

2️⃣ 按 F12 打开开发者工具

3️⃣ 进入 Application/应用 > Cookies

4️⃣ 复制所有 cookies（格式: name=value）

5️⃣ 保存为 cookies.txt 文件

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

方法 3: 使用命令行工具（高级）

macOS:
  brew install browser-cookie3

  导出 Chrome cookies:
  browser-cookie3 chrome > cookies.txt

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

完成后，将 cookies.txt 文件放到这个位置:
$DOWNLOAD_DIR/cookies.txt

然后重新运行下载脚本。

EOF

    print_info "按任意键继续..."
    read -n 1
}

# 主函数
main() {
    clear

    print_header "🎬 智能视频下载器 v2.0"
    print_info "支持自动检测登录需求并引导使用浏览器 cookies"

    # 获取 URL
    if [ -z "$1" ]; then
        echo
        read -p "请输入视频 URL: " url
    else
        url="$1"
    fi

    if [ -z "$url" ]; then
        print_error "URL 不能为空"
        exit 1
    fi

    print_info "目标 URL: $url"
    echo

    # 第一次尝试：普通下载
    print_info "第 1 次尝试：普通下载..."
    if try_download "$url" "false" ""; then
        print_success "下载成功！"
        exit 0
    fi

    # 如果失败，分析原因
    print_warning "普通下载失败，可能需要登录"
    echo

    # 检查是否有 cookies 文件
    local cookies_file="$DOWNLOAD_DIR/cookies.txt"
    if [ -f "$cookies_file" ]; then
        print_info "发现现有的 cookies 文件"
        read -p "是否使用现有 cookies 重试? (y/n): " use_existing

        if [ "$use_existing" = "y" ] || [ "$use_existing" = "Y" ]; then
            print_info "使用 cookies 重试..."
            if try_download "$url" "true" "$cookies_file"; then
                print_success "下载成功！"
                exit 0
            else
                print_warning "使用 cookies 仍然失败"
            fi
        fi
    fi

    # 显示菜单
    while true; do
        echo
        print_header "请选择下一步操作:"
        echo "1) 查看登录指南（推荐新手）"
        echo "2) 尝试使用浏览器 cookies（需要手动导出）"
        echo "3) 不带字幕重试（可能字幕不是问题）"
        echo "4) 使用简化选项重试"
        echo "5) 退出"
        echo
        read -p "请选择 (1-5): " choice

        case $choice in
            1)
                manual_login_guide
                # 检查用户是否创建了 cookies 文件
                if [ -f "$cookies_file" ]; then
                    print_info "检测到 cookies.txt 文件"
                    read -p "是否现在重试下载? (y/n): " retry
                    if [ "$retry" = "y" ] || [ "$retry" = "Y" ]; then
                        try_download "$url" "true" "$cookies_file"
                        if [ $? -eq 0 ]; then
                            print_success "下载成功！"
                            exit 0
                        fi
                    fi
                fi
                ;;

            2)
                print_info "请先手动导出 cookies.txt"
                manual_login_guide
                if [ -f "$cookies_file" ]; then
                    try_download "$url" "true" "$cookies_file"
                    if [ $? -eq 0 ]; then
                        print_success "下载成功！"
                        exit 0
                    fi
                else
                    print_warning "未找到 cookies.txt 文件"
                fi
                ;;

            3)
                print_info "尝试不带字幕下载..."
                if $YTDLP_PATH -o "$DOWNLOAD_DIR/%(extractor)s/%(uploader)s/%(title)s.%(ext)s" "$url"; then
                    print_success "下载成功（不带字幕）！"
                    exit 0
                else
                    print_warning "仍然失败"
                fi
                ;;

            4)
                print_info "使用简化选项重试..."
                if $YTDLP_PATH --no-warnings --ignore-errors -o "$DOWNLOAD_DIR/%(extractor)s/%(uploader)s/%(title)s.%(ext)s" "$url"; then
                    print_success "下载成功！"
                    exit 0
                else
                    print_warning "仍然失败"
                fi
                ;;

            5)
                print_info "退出"
                exit 0
                ;;

            *)
                print_error "无效的选项"
                ;;
        esac
    done
}

# 运行主函数
main "$@"
