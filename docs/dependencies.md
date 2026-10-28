# Fairy 依赖清单

本文说明 `pnpm dev` 首次启动时会检查或自动准备哪些依赖。机器配置、API Key、
模型权重和运行时文件都不会提交到 Git。

## 系统工具

| 工具 | 最低版本 | 用途 | 启动器行为 |
|---|---:|---|---|
| Node.js | 22.5.0 | API、前端、认证 SQLite | 缺失时停止并给出安装地址 |
| pnpm | 9.0.0（仓库固定 10.15.1） | Node/前端依赖管理 | 缺失时停止 |
| Go | 1.24.0（`agent/go.mod` 为 1.24.4） | 编译原生 Agent | 首次启动自动编译 |
| Python | 3.10+，推荐 3.11-3.13 | 工具、语音、语义裁判 | 自动创建 `.tools/venv` |
| Rust | 1.77.2 | 仅 Tauri 桌面构建 | `pnpm tauri:build` 时使用 |
| Tauri CLI | 2.x | 仅 Tauri 桌面构建 | 需预先安装 |
| Visual Studio C++ Build Tools | 2022 | Windows Tauri 编译 | 仅桌面构建需要 |

## Node.js

根目录 `package.json` / `pnpm-lock.yaml`：

- `playwright-core`
- `ws`

前端 `frontend/package.json` / `frontend/pnpm-lock.yaml`：

- 运行：`react`、`react-dom`、`react-virtuoso`、`lucide-react`
- 构建：`vite`、`@vitejs/plugin-react`、`typescript`、`tsx`、`esbuild`

首次启动会自动执行前端安装；Node 依赖不提交 `node_modules/`。

## Go 与 Rust

- Go Agent 模块：`agent/go.mod`
- Tauri Rust 依赖：`src-tauri/Cargo.toml`、`src-tauri/Cargo.lock`

Go Agent 会在源码更新后自动重新编译到
`.tools/agent-loop-<platform>-<arch>`。

## Python

统一安装入口：

```bash
pnpm setup:python
```

项目 venv：`.tools/venv/`。

| 清单 | 内容 |
|---|---|
| `requirements.txt` | 文档解析、电脑操控、视频工具、语音、语义依赖的源清单 |
| `requirements.lock` | 上述依赖的固定版本与平台标记 |
| `voice/requirements.txt` | TTS：`numpy`、`onnxruntime`、`sentencepiece`、`websockets` |
| `voice/requirements-stt.txt` | STT：Faster-Whisper、sherpa-onnx、VAD、拼音与简繁转换 |
| `skills/qq-bot/requirements.txt` | QQ 桥接依赖 |
| `skills/wechat-bot/requirements.txt` | 微信桥接依赖 |

启动器会安装根锁文件和两份 `voice/` 清单。QQ / 微信桥接依赖由各自桥接启动器
按需安装。

主要锁定版本：

| 组件 | 版本 |
|---|---:|
| `numpy` | 2.5.2 |
| `onnxruntime` | 1.29.0 |
| `faster-whisper` | 1.2.0 |
| `sherpa-onnx` | 1.13.8 |
| `sentencepiece` | 0.2.2 |
| `websockets` | 17.3 |
| `torch` | 2.14.1 |
| `transformers` | 5.21.0 |

## 本地模型

模型清单源文件：`scripts/model-manifest.mjs`。

| 模型 | 运行时用途 | 默认位置 |
|---|---|---|
| `Systran/faster-whisper-base` | Faster-Whisper STT | `models/voice/faster-whisper-base/` |
| `MOSS-Audio-Tokenizer-Nano-ONNX` | TTS 音频编解码 | `models/voice/MOSS-Audio-Tokenizer-Nano-ONNX/` |
| `MOSS-TTS-Nano-100M-ONNX` | 本地 TTS | `models/voice/MOSS-TTS-Nano-100M-ONNX/` |
| `sherpa-onnx` 流式中文/英文模型 | 实时 partial STT | `.tools/sherpa-models/` |
| `BAAI/bge-small-zh-v1.5` | 工具结果语义相关性裁判 | `.tools/huggingface/` |

HuggingFace 模型默认使用 `hf-mirror.com`，并回退官方源。可用：

```bash
FAIRY_MODEL_OFFICIAL_FIRST=1 pnpm dev   # 官方源优先
FAIRY_SKIP_MODELS=1 pnpm dev            # 跳过语音模型检查
```

语义模型由 Transformers 在 `tools/stale_tiebreak_service.py` 启动时下载。

## 运行时目录

| 目录 | 是否提交 | 说明 |
|---|---|---|
| `.tools/` | 否 | 编译产物、Python venv、模型缓存、服务锁 |
| `models/` | 否 | 本地语音模型 |
| `node_modules/` | 否 | Node 依赖 |
| `frontend/node_modules/` | 否 | 前端依赖 |
| `config/config.json` | 否 | 首次启动从示例复制 |
| `config/channels.json` | 否 | 通道配置 |
| `config/local_secrets.json` | 否 | 本地密钥 |
