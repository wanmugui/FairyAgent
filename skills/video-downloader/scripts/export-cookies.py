#!/usr/bin/env python3
"""
浏览器 Cookies 导出工具
支持导出 Chrome、Safari、Firefox、Edge 的 cookies
"""

import sys
import os
import json
from pathlib import Path

def export_chrome_cookies(output_file):
    """导出 Chrome cookies"""
    try:
        print("正在尝试导出 Chrome cookies...")

        # 尝试使用 browser_cookie3
        try:
            import browser_cookie3
            cookies = browser_cookie3.chrome(domain_name='')
            print(f"✓ 成功读取 Chrome cookies，共 {len(list(cookies))} 个")

            # 写入 cookies.txt 格式
            with open(output_file, 'w') as f:
                for cookie in cookies:
                    # Netscape cookie 格式
                    f.write(f"{cookie.domain}\t")
                    f.write(f"{'TRUE' if cookie.domain.startswith('.') else 'FALSE'}\t")
                    f.write(f"{cookie.path}\t")
                    f.write(f"{'TRUE' if cookie.secure else 'FALSE'}\t")
                    f.write(f"{0 if cookie.expires is None else cookie.expires}\t")
                    f.write(f"{cookie.name}\t")
                    f.write(f"{cookie.value}\n")

            print(f"✓ Cookies 已保存到: {output_file}")
            return True
        except ImportError:
            print("⚠ 未安装 browser_cookie3，尝试手动方法...")
            return manual_export_guide("Chrome", output_file)
        except Exception as e:
            print(f"✗ 导出失败: {e}")
            return manual_export_guide("Chrome", output_file)

    except Exception as e:
        print(f"✗ 错误: {e}")
        return False

def export_safari_cookies(output_file):
    """导出 Safari cookies (仅 macOS)"""
    try:
        print("正在尝试导出 Safari cookies...")

        import browser_cookie3
        cookies = browser_cookie3.safari(domain_name='')
        print(f"✓ 成功读取 Safari cookies")

        with open(output_file, 'w') as f:
            for cookie in cookies:
                f.write(f"{cookie.domain}\t")
                f.write(f"{'TRUE' if cookie.domain.startswith('.') else 'FALSE'}\t")
                f.write(f"{cookie.path}\t")
                f.write(f"{'TRUE' if cookie.secure else 'FALSE'}\t")
                f.write(f"{0 if cookie.expires is None else cookie.expires}\t")
                f.write(f"{cookie.name}\t")
                f.write(f"{cookie.value}\n")

        print(f"✓ Cookies 已保存到: {output_file}")
        return True

    except ImportError:
        print("⚠ 未安装 browser_cookie3")
        return manual_export_guide("Safari", output_file)
    except Exception as e:
        print(f"✗ 导出失败: {e}")
        return manual_export_guide("Safari", output_file)

def export_firefox_cookies(output_file):
    """导出 Firefox cookies"""
    try:
        print("正在尝试导出 Firefox cookies...")

        import browser_cookie3
        cookies = browser_cookie3.firefox(domain_name='')
        print(f"✓ 成功读取 Firefox cookies")

        with open(output_file, 'w') as f:
            for cookie in cookies:
                f.write(f"{cookie.domain}\t")
                f.write(f"{'TRUE' if cookie.domain.startswith('.') else 'FALSE'}\t")
                f.write(f"{cookie.path}\t")
                f.write(f"{'TRUE' if cookie.secure else 'FALSE'}\t")
                f.write(f"{0 if cookie.expires is None else cookie.expires}\t")
                f.write(f"{cookie.name}\t")
                f.write(f"{cookie.value}\n")

        print(f"✓ Cookies 已保存到: {output_file}")
        return True

    except ImportError:
        print("⚠ 未安装 browser_cookie3")
        return manual_export_guide("Firefox", output_file)
    except Exception as e:
        print(f"✗ 导出失败: {e}")
        return manual_export_guide("Firefox", output_file)

def export_edge_cookies(output_file):
    """导出 Edge cookies"""
    try:
        print("正在尝试导出 Edge cookies...")

        import browser_cookie3
        cookies = browser_cookie3.edge(domain_name='')
        print(f"✓ 成功读取 Edge cookies")

        with open(output_file, 'w') as f:
            for cookie in cookies:
                f.write(f"{cookie.domain}\t")
                f.write(f"{'TRUE' if cookie.domain.startswith('.') else 'FALSE'}\t")
                f.write(f"{cookie.path}\t")
                f.write(f"{'TRUE' if cookie.secure else 'FALSE'}\t")
                f.write(f"{0 if cookie.expires is None else cookie.expires}\t")
                f.write(f"{cookie.name}\t")
                f.write(f"{cookie.value}\n")

        print(f"✓ Cookies 已保存到: {output_file}")
        return True

    except ImportError:
        print("⚠ 未安装 browser_cookie3")
        return manual_export_guide("Edge", output_file)
    except Exception as e:
        print(f"✗ 导出失败: {e}")
        return manual_export_guide("Edge", output_file)

def manual_export_guide(browser, output_file):
    """显示手动导出指南"""
    print(f"\n{'='*60}")
    print(f"📋 {browser} 手动导出指南")
    print(f"{'='*60}\n")

    print("方法 1: 使用浏览器扩展（最简单）")
    print("-" * 60)
    print("1. 安装 'Get cookies.txt' 扩展:")
    print("   Chrome/Edge: https://chrome.google.com/webstore")
    print("   Firefox: https://addons.mozilla.org/")
    print("   搜索 'Get cookies.txt LOCALLY'")
    print()
    print("2. 在浏览器中打开目标网站并登录")
    print("3. 点击扩展图标，导出 cookies.txt")
    print(f"4. 将文件保存到: {output_file}")
    print()

    print("方法 2: 使用开发者工具（高级用户）")
    print("-" * 60)
    print("1. 在浏览器中打开目标网站并登录")
    print("2. 按 F12 打开开发者工具")
    print("3. 进入 Application/应用 > Cookies")
    print("4. 复制所有 cookies 并保存为 cookies.txt 格式")
    print()

    print("方法 3: 安装 browser_cookie3（推荐）")
    print("-" * 60)
    print("运行以下命令安装:")
    print("  pip3 install browser-cookie3")
    print("或")
    print("  brew install browser-cookie3  # macOS")
    print()

    print(f"将导出的 cookies.txt 文件放在:")
    print(f"  {output_file}")
    print()

    return False

def main():
    if len(sys.argv) < 3:
        print("用法: python3 export-cookies.py <browser> <output_file>")
        print()
        print("浏览器选项:")
        print("  chrome   - Google Chrome")
        print("  safari   - Safari (仅 macOS)")
        print("  firefox  - Mozilla Firefox")
        print("  edge     - Microsoft Edge")
        print()
        print("示例:")
        print("  python3 export-cookies.py chrome ~/Downloads/cookies.txt")
        sys.exit(1)

    browser = sys.argv[1].lower()
    output_file = os.path.expanduser(sys.argv[2])

    print(f"\n{'='*60}")
    print(f"🍪 浏览器 Cookies 导出工具")
    print(f"{'='*60}\n")

    success = False

    if browser == "chrome":
        success = export_chrome_cookies(output_file)
    elif browser == "safari":
        success = export_safari_cookies(output_file)
    elif browser == "firefox":
        success = export_firefox_cookies(output_file)
    elif browser == "edge":
        success = export_edge_cookies(output_file)
    else:
        print(f"✗ 不支持的浏览器: {browser}")
        print("支持的浏览器: chrome, safari, firefox, edge")
        sys.exit(1)

    if success:
        print("\n✅ Cookies 导出成功！")
        print(f"📁 文件位置: {output_file}")
        print("\n现在可以使用 cookies 下载视频了:")
        print(f"  yt-dlp --cookies {output_file} \"视频URL\"")
    else:
        print("\n❌ 自动导出失败，请按照上面的指南手动导出")
        sys.exit(1)

if __name__ == "__main__":
    main()
