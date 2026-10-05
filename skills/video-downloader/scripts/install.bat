@echo off
REM 视频下载工具安装脚本 - Windows 版本

echo 🎬 视频下载工具安装脚本
echo ==========================
echo.

echo 📱 检测到操作系统: Windows
echo.

REM 检查 Scoop
echo 🔍 检查 Scoop 包管理器...
where scoop >nul 2>nul
if %ERRORLEVEL% EQU 0 (
    echo ✅ Scoop 已安装
    goto INSTALL_WITH_SCOOP
) else (
    echo ⚠️  Scoop 未安装
    echo.
    echo 💡 建议: 安装 Scoop 以获得更好的体验
    echo    访问: https://scoop.sh
    echo.
    choice /C YN /M "是否继续使用 pip 安装"
    if errorlevel 2 goto INSTALL_WITH_PIP
    if errorlevel 1 goto INSTALL_WITH_SCOOP
)

:INSTALL_WITH_SCOOP
echo.
echo 📥 使用 Scoop 安装 yt-dlp...
scoop bucket add extras
scoop install yt-dlp
echo.
echo 🎥 使用 Scoop 安装 FFmpeg...
scoop install ffmpeg
goto FINISH

:INSTALL_WITH_PIP
echo.
echo 📥 使用 pip 安装 yt-dlp...
pip install yt-dlp

echo.
echo ⚠️  注意: 你需要手动安装 FFmpeg
echo    推荐: https://ffmpeg.org/download.html#build-windows
echo    或使用: choco install ffmpeg
echo.

goto FINISH

:FINISH
echo.
echo ✅ 验证安装...
echo.
yt-dlp --version
echo.

REM 创建配置目录
set CONFIG_DIR=%USERPROFILE%\yt-dlp.conf
if not exist "%USERPROFILE%\Videos" mkdir "%USERPROFILE%\Videos"

echo ⚙️  创建默认配置...
(
    REM # 视频下载默认配置
    REM.
    REM # 保存到用户视频目录
    -o C:/Users/%USERNAME%/Videos/%%\(extractor\)s/%%\(uploader\)s/%%\(title\)s.%%\(ext\)s
    REM.
    REM # 合并为 MP4 格式
    REM --merge-output-format mp4
    REM.
    REM # 嵌入字幕
    REM --embed-subs
    REM.
    REM # 并发片段数
    REM --concurrent-fragments 4
) > "%CONFIG_DIR%"

echo ✅ 配置文件已创建: %CONFIG_DIR%
echo.

echo 🎉 安装完成！
echo.
echo 📋 快速开始:
echo    下载视频:     yt-dlp "视频URL"
echo    下载音频:     yt-dlp -x --audio-format mp3 "视频URL"
echo    下载播放列表: yt-dlp "播放列表URL"
echo    查看帮助:     yt-dlp --help
echo.
echo ⚙️  配置文件位置: %CONFIG_DIR%
echo.

pause
