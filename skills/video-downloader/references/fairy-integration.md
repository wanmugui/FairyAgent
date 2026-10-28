# 在 Fairy 仓库里运行 video-downloader

上游脚本假设 `yt-dlp` / `ffmpeg` 在 PATH、终端是 UTF-8、下载落在 skill 目录里
的 `downloads/`。Fairy 这三条都不一样，照抄上游命令会出现"命令找不到"、
中文标题乱码、下载物落到 git 工作区里被误提交。

## 已固定的路径

| 东西 | 路径 |
| --- | --- |
| Python | `.tools\venv\Scripts\python.exe` |
| yt-dlp | `.tools\venv\Scripts\yt-dlp.exe` |
| ffmpeg | `imageio-ffmpeg` 自带，用 `python -c "import imageio_ffmpeg;print(imageio_ffmpeg.get_ffmpeg_exe())"` 取 |
| 下载目录 | `workspace\download\`（已被 .gitignore 覆盖） |

不要把下载物写进 `skills/video-downloader/downloads/`，那个目录在 git 里。

## 为什么用 imageio-ffmpeg 而不是系统 ffmpeg

这台机器上散落着好几个 `ffmpeg.exe`（Adobe、FormatFactory、Ultimate Vocal
Remover 等），版本和编译选项各不相同，PATH 上反而没有。`imageio-ffmpeg` 提供
一个固定版本的静态二进制，导入即可拿到绝对路径，不需要改 PATH，也不会因为别人
卸载某个软件而失效。

```python
import imageio_ffmpeg
FFMPEG = imageio_ffmpeg.get_ffmpeg_exe()
```

yt-dlp 需要 ffmpeg 时同样可以指过去：

```powershell
$ffmpeg = & $python -c "import imageio_ffmpeg;print(imageio_ffmpeg.get_ffmpeg_exe())"
& $ytdlp --ffmpeg-location (Split-Path $ffmpeg) ...
```

## 中文标题乱码

Windows PowerShell 5.1 的控制台代码页默认是 936，`yt-dlp --print` 输出的中文会
变成 `���ٷ�`。两种处理：

```powershell
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$env:PYTHONUTF8 = '1'
```

或者在 Python 里调用 yt-dlp 的 API，绕开控制台编码。

## 常用命令（Fairy 版）

```powershell
$py    = '.tools\venv\Scripts\python.exe'
$ytdlp = '.tools\venv\Scripts\yt-dlp.exe'
$out   = 'workspace\download'

# 只取元信息（不下载），用来判断有没有字幕
& $ytdlp --skip-download --dump-json $url

# 视频 + 中英字幕 + 嵌入
& $ytdlp -P $out --write-subs --sub-langs "zh-Hans,zh-CN,en" --embed-subs $url

# 只取字幕（video-summary 的首选路径，最准且几乎零成本）
& $ytdlp -P $out --skip-download --write-subs --sub-langs "zh-Hans,zh-CN,en" --convert-subs srt $url

# 只要音频（给 STT 用）
& $ytdlp -P $out -x --audio-format wav --postprocessor-args "-ar 16000 -ac 1" $url
```

## 字幕优先于 STT

`video-summary` 的第一选择永远是平台字幕：B 站 CC 字幕和 YouTube 人工/自动字幕
都是现成的文本，比任何本地 STT 都准，而且不需要下载整段音频做推理。只有拿不到
字幕时才回退到 `../video-summary/scripts/transcribe.py`。

可以用这条判断是否需要 STT：

```powershell
& $ytdlp --skip-download --dump-json $url | ConvertFrom-Json |
  Select-Object -ExpandProperty subtitles -ErrorAction SilentlyContinue
```

返回空 → 走 STT。

## 登录与 cookies

B 站 1080P 以上、YouTube 部分内容需要登录。已经登录的浏览器可以复用：

```powershell
& $ytdlp --cookies-from-browser chrome $url
```

上游的 `scripts/export-cookies.py` 用于把浏览器 cookies 导出成文件，适合同一条
命令要反复跑的情况。cookies 是凭证，导出文件放在 `workspace/` 下并且不要提交。

## 与 video-summary 的衔接

```text
video-downloader  →  视频文件 / 音频文件 / 字幕文件
                          ↓
video-summary     →  字幕或 STT 文本 + 关键帧 + image_vqa  →  摘要
```

两个 skill 通过文件路径衔接，不通过进程状态，所以可以分开跑：
先下载，之后（甚至另一天）再总结。
