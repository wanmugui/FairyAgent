# Fairy

Fairy 是一个本地优先的个人 Agent 工作台：可以操作真实电脑、执行命令、读写文件、使用 Skills、保存本地记忆，并通过文字或实时语音与主人交互。

项目包含三层：

- `agent/`：跨平台 Go Agent 引擎。
- `frontend/`：React 对话界面、Node API 与 filemanager viewer。
- `voice/`：MOSS-TTS-Nano 与 Faster-Whisper 语音服务。

桌面版通过 Tauri 打包；根目录的 `Fairy.exe` 是当前 Windows 发布文件。

## 当前能力

- **真实主机 Agent**：可读写宿主文件、执行 shell/Python、启动服务、调用网络与本地工具。
- **Plan 单一状态机**：复杂任务只使用 session 级 `plan` 管理执行顺序、进度与验收，不再使用 todolist。
- **本地记忆**：`memory/` 保存用户画像、长期记忆、每日摘要与分层 KEY 分段记忆；分段记忆在交互结束后异步抽取原文关键句并写入索引。
- **Skills**：按需检索并加载 `skills/` 中的技能与脚本。
- **文字对话**：SSE 流式回复、工具调用折叠、Thinking/Reflection 卡片、附件上传与文件预览。
- **生成结果卡片**：`show_result` 产物会挂在对应回复后面，点击卡片直接打开右侧 viewer。
- **悬浮 viewer**：文字对话右侧复用 `filemanager/viewer.html`，支持 PDF、DOCX、XLSX/XLS、图片、文本、HTML、音视频和 PPTX 下载提示。
- **实时语音**：独立 `/voice` 页面，支持 MOSS-TTS-Nano 流式 TTS、自定义音色、Faster-Whisper STT 与 barge-in。
- **多模型**：DeepSeek-V4-Flash 与 MiniMax-M3，统一由 `config/config.json` 管理。
- **原生 MCP 工具**：Agent 可连接 stdio 或 Streamable HTTP MCP Server，在启动时动态发现并注册 `tools/list` 工具，通过统一的工具审批、超时和命名空间调用。
- **电脑操控工具**：基于 `cua-auto` 的本地原生工具，支持屏幕观察与截图、鼠标、键盘、窗口和剪贴板操作；不新增 HTTP 端口。
- **本地会话持久化**：session JSON、append-only events、usage、trace 与音频记录。
- **桌面启动**：Tauri 窗口、仓库根目录 exe、Windows/POSIX 启动脚本。

## 架构

```text
浏览器 / Tauri
  │
  ├─ 文字对话 http://127.0.0.1:5173
  ├─ 语音页面 http://127.0.0.1:5173/#/voice
  └─ filemanager viewer
       │
       ▼
Node API / 静态服务 frontend/server.cjs :8081
  ├─ /api/models
  ├─ /api/sessions
  ├─ /api/chat
  ├─ /api/upload
  ├─ /api/file-content
  └─ /viewer.html
       │
       ▼
Go Agent .tools/agent-loop-<platform>-<arch>
  ├─ LLM API
  ├─ tool registry / dispatcher
  ├─ MCP client / dynamic tools
  ├─ session + memory
  ├─ skills
  └─ subagent
       │
       ▼
语音服务 voice/voice_service.py
  ├─ HTTP TTS :8787
  └─ WebSocket STT :8788
```

## 快速启动

### 准备配置

公开仓不含真实机器配置，首次运行先复制示例：

```bash
cp config/config.example.json config/config.json
cp config/channels.example.json config/channels.json   # 可选，QQ/微信通道
```

再按需填入你自己的模型 API Key（`config/config.json` 里的 `api_key` 支持 `READ_FROM_*` 占位符，会从仓库根目录同名 txt 文件读取）。

### 一键开发模式

```bash
pnpm install
pnpm dev
```

`pnpm dev` 会：

1. 构建当前平台的 Go Agent 到 `.tools/`。
2. 检查并安装 `frontend/` 依赖。
3. 启动 Node API：`http://localhost:8081`。
4. 启动 Vite：`http://127.0.0.1:5173`。
5. 在可用时启动语音服务：TTS `:8787` / STT `:8788`。

打开：

- 文字对话：`http://127.0.0.1:5173/`
- 语音对话：`http://127.0.0.1:5173/#/voice`

### 使用桌面版

Windows 可直接运行：

```text
Fairy.exe
```

也可以使用：

```text
启动 Fairy.cmd
```

macOS/Linux：

```bash
./启动 Fairy.sh
```

桌面开发与打包：

```bash
pnpm tauri:dev
pnpm tauri:build
```

### 重启已启动的服务

```bash
pnpm service:restart
```

Windows 重启脚本会先验证监听进程属于当前仓库，再启动新服务。

## Python 环境

本地工具需要 Python 3.10+：

```bash
pnpm setup:python
```

默认创建 `.tools/venv`，包含文档解析和本地工具依赖。

语音服务依赖：

- `numpy`
- `onnxruntime`
- `sentencepiece`
- `websockets`
- `faster_whisper`（仅 STT 必需）

如果使用 Miniforge/Conda：

```bash
FAIRY_VOICE_PYTHON=/path/to/python pnpm dev
```

也可以分别指定：

```bash
FAIRY_STT_PYTHON=/path/to/python pnpm dev
```

如果环境没有 `faster_whisper`，TTS 仍可运行，但 STT 预加载会失败，`pnpm dev` 会打印警告。

电脑操控工具优先使用跨平台 `cua-auto`，要求 Python 3.11 到 3.13。`pnpm dev` 启动时会检查项目 venv 和必要模块，缺失时自动运行 `pnpm setup:python`；也可以手动执行初始化。Windows 额外内置 PowerShell 原生回退，因此在 `cua-auto` 不可用或 Python 版本不兼容时，鼠标、键盘、窗口、截图和剪贴板控制仍可工作。

## 语音模型

TTS 使用 MOSS-TTS-Nano 的 ONNX 推理：

- TTS：`models/voice/MOSS-TTS-Nano-100M-ONNX/`
- Codec：`models/voice/MOSS-Audio-Tokenizer-Nano-ONNX/`
- 参考音色：`fairy_vocals_48k.wav`（可选，不入库）
- 服务入口：`voice/voice_service.py`
- TTS HTTP：`:8787`
- STT WebSocket：`:8788`

当前微调模型托管在 ModelScope；该仓库若没有访问权限，可把已有模型放入上面的本地目录：

```bash
pip install modelscope
modelscope download \
  --model wanmugui/Fairy-TTS-v2-ONNX \
  --local_dir models/voice/MOSS-TTS-Nano-100M-ONNX
```

STT 模型放入：

```text
models/voice/faster-whisper-base/
```

TTS 文本清洗、分句和标题合并位于 `frontend/src/utils/ttsText.js`。

## 模型配置

模型统一配置在 `config/config.json`：

```json
{
  "default_model": "deepseek-v4-flash",
  "models": {
    "deepseek-v4-flash": {
      "display": "DeepSeek-V4-Flash",
      "api": {
        "base_url": "https://api.deepseek.com",
        "api_key": "READ_FROM_DEEPSEEK_KEY_TXT",
        "model": "deepseek-v4-flash"
      }
    },
    "minimax-m3": {
      "display": "MiniMax-M3",
      "api": {
        "base_url": "https://api.minimaxi.com/v1",
        "api_key": "READ_FROM_MINIMAX_KEY_TXT",
        "model": "MiniMax-M3"
      }
    }
  }
}
```

API key 从仓库根目录读取：

- `DEEPSEEK_key.txt`
- `MINIMAX_key.txt`

不要把真实 key 提交到 Git。

## MCP 工具

Fairy 内置 MCP Client，不需要为每个 MCP Server 单独修改 Go 代码。配置位于 `config/config.json` 的 `mcp_servers` 字段，Agent 启动时会连接已启用服务，依次执行 `initialize`、`notifications/initialized`、分页 `tools/list`，并把远端工具注册到统一工具表。

stdio 示例：

```json
{
  "mcp_servers": {
    "playwright": {
      "enabled": true,
      "required": false,
      "transport": "stdio",
      "command": "npx",
      "args": ["-y", "@playwright/mcp@latest"],
      "tool_prefix": "pw",
      "allowed_tools": [
        "browser_navigate",
        "browser_snapshot",
        "browser_click"
      ],
      "startup_timeout_sec": 30
    }
  }
}
```

Streamable HTTP 示例：

```json
{
  "mcp_servers": {
    "flyclaw": {
      "enabled": true,
      "transport": "streamable-http",
      "url": "http://127.0.0.1:18080/mcp",
      "tool_prefix": "flyclaw",
      "headers": {
        "Authorization": "Bearer local-token"
      }
    }
  }
}
```

注册后的工具名采用 `tool_prefix__remote_tool` 命名空间。上例会注册 `pw__browser_navigate`、`pw__browser_click`、`flyclaw__message_send` 等名称，避免不同 MCP Server 的工具互相覆盖。`allowed_tools` 为空时允许全部工具；`denied_tools` 优先级最高，两者都按完整工具名匹配且不区分大小写。

常用配置项：

| 字段 | 说明 |
|---|---|
| `enabled` / `disabled` | 是否启用该 Server；默认启用。 |
| `required` | `true` 时连接或 `tools/list` 失败会让 Agent 启动失败；`false` 时只记日志。 |
| `transport` | `stdio`、`http` 或 `streamable-http`；省略时按 `command`/`url` 推断。 |
| `command` / `args` / `env` / `cwd` | stdio Server 的启动参数和环境。 |
| `url` / `headers` | Streamable HTTP Server 地址与附加请求头。 |
| `protocol_version` | 覆盖 MCP 协议版本；默认 `2025-06-18`。 |
| `startup_timeout_sec` | 连接与初始 `tools/list` 的总超时，默认 30 秒。 |
| `tool_prefix` | Fairy 侧工具命名空间；默认使用 Server 名。 |

MCP 工具和普通工具共用 `tool_runtime.timeouts` 与审批策略。例如让浏览器点击和消息发送必须询问：

```json
{
  "tool_runtime": {
    "approval": {
      "pw__browser_click": "ask",
      "flyclaw__message_send": "ask"
    },
    "approval_mode": "ask"
  }
}
```

当前文字和语音请求都会为每轮 Agent 创建一个进程。因此 stdio MCP Server 也会随该轮进程启动和结束；需要跨轮保持浏览器上下文、登录态或长连接的服务，优先使用常驻的 Streamable HTTP sidecar。如果 MCP Server 本身只提供 stdio，需要先由 sidecar 或独立 gateway 转发为 HTTP。

## 电脑操控

`cua-auto` 是 Python 库，不是 MCP Server。Fairy 将它包装为原生本地工具，每次调用启动一个短生命周期 Python bridge，因此不会新增监听端口，也不会常驻后台进程。

| 工具 | 用途 |
|---|---|
| `computer_observe` | 获取屏幕信息、光标位置、活动窗口、窗口列表，或保存桌面截图。 |
| `computer_pointer` | 移动鼠标、点击、双击、右键、拖拽、滚动和按住/释放按钮。 |
| `computer_keyboard` | 输入文本、按键、组合键和按住/释放按键。 |
| `computer_window` | 激活、最小化、最大化、移动、缩放、关闭窗口，或用默认程序打开 URL/工作区文件。 |
| `computer_clipboard` | 读取或替换系统文本剪贴板。 |

典型流程是：先调用 `computer_observe(action=active_window/list_windows)` 找到目标窗口，再用 `computer_window` 激活它，执行 `computer_pointer` 或 `computer_keyboard`，最后再次截图或检查窗口状态验证结果。`computer_observe(action=screenshot)` 默认把 PNG 保存到：

```text
workspace/result/computer-screenshots/
```

返回的路径可以交给 `image_vqa` 做视觉判断，也可以交给 `show_result` 打开悬浮 viewer。

四个本地图片工具都直接跑在 Agent 进程里，不需要 `8080` 工具网关：

- `image_vqa`：本地直接调用 DeepSeek `deepseek-flash` 多模态接口。模型名、地址和输出预算在 `config/config.json` 的 `tools.imageVQA` 中配置。
- `image_search`：本地依次尝试多个公开图源（默认 `bing` → `baidu` → `wikimedia`，前一个源失败或不足时自动回退），支持 `download=false` 只返回候选 URL。图源顺序、单源超时和单图体积上限在 `tools.imageSearch` 中配置。
- `image_generate`：本地调用 MiniMax `image-01` 文生图（`POST {baseUrl}/image_generation`，Bearer 鉴权，复用根目录 `MINIMAX_key.txt`），默认把结果落盘到 `workspace/result/image-generate/`。**prompt 由 Agent 自己写好后原样传入**，工具不做改写，`prompt_optimizer` 默认关闭；支持的档位是 `1:1` / `16:9` / `4:3` / `3:2` / `2:3` / `3:4` / `9:16` / `21:9`，传 `1024x1024` 这类像素尺寸会映射到最接近的档位。模型、档位、超时和重试在 `tools.imageGenerate` 中配置。
- `image_generate` 只支持文生图，不接参考图；需要参考素材时用 `image_search`。

电脑工具没有暴露 `cua-auto.shell`，命令执行仍必须经过 Fairy 现有 `bash` 和安全策略。

端口保持独立，当前桌面操控新增端口为 `0`。现有本地服务预算如下：

| 服务 | 端口 | 是否必需 |
|---|---:|---|
| Vite 前端 | `5173` | 是 |
| Node API | `8081` | 是 |
| HTTP tool gateway mock | `8080` | 仅开发/兼容测试 |
| TTS | `8787` | 语音功能 |
| STT WebSocket | `8788` | 语音功能 |
| HTTP MCP sidecar | 建议 `18080-18089` | 仅 HTTP MCP Server 需要 |

后续新增 MCP 能力时优先使用 stdio。只有需要跨轮保持登录态、浏览器 profile 或长连接的 sidecar 才分配 HTTP MCP 端口。

## Prompt 目录

主 system prompt 与按需模块现在分开维护：

```text
config/
├─ system/
│  ├─ manifest.yml
│  ├─ zh.md                    # 构建产物
│  └─ parts/
│     └─ zh/
│        ├─ 01_contract.md
│        ├─ 02_resources.md
│        ├─ 03_context.md
│        ├─ 04_execution.md
│        ├─ 05_memory.md
│        ├─ 06_delivery.md
│        └─ 07_thinking.md
└─ modules/
   ├─ date_memory/
   ├─ finalize/
   ├─ generate_title/
   ├─ mem_agent/
   ├─ reflection/
   ├─ subtask/
   └─ summary/
```

构建与检查：

```bash
pnpm prompt:build
pnpm prompt:check
```

运行时通过 `prompts.modules_dir` 定位按需 prompt，不再沿旧 `config/locales` 路径查找。

## 关键机制

### Plan

- 复杂任务通过 `plan(action=update, ...)` 建立计划。
- 每完成一步立即 `plan(action=mark, ...)`。
- `<plan>` 是唯一的会话执行状态；不存在并行的 todolist。
- 未完成与进行中的计划项只在当前请求与计划问题/步骤明显相关，或用户明确要求继续当前计划时注入。

### Context

- 用户上传文件通过 `<file_context>` 传给 Agent，包含路径、文件名、大小、类型和文本预览。
- `is_full=false` 表示预览截断，Agent 需要用文件工具读取完整内容。
- 最终输出必须使用真实绝对路径，不把内部别名暴露给主人。

### 长报告

- 短回答直接在聊天里完成。
- 只有最终的长篇分析、调研报告、方案和规格文档使用 `<report>…</report>` 包裹全文。
- `<report>` 必须由模型明确输出，server 不补标签、不补正文；只在看到完整标签后原样保存到 `workspace/result/YYYY-MM-DD-<主题>.md`。
- 文字对话会在悬浮 viewer 中打开 Markdown 报告；语音对话打开完整 filemanager。
- 回复下方同时显示“生成结果”卡片，可再次点击打开 viewer。
- 聊天气泡只保留结论摘要、关键发现和报告路径，避免重复粘贴全文。

### Memory：分层 KEY 记忆

Fairy 的分段记忆不是把整段历史一直塞回上下文，也不是先让模型写一份 summary。它模拟的是“先想起关键词，再顺着关键词回忆原文”的机制：每段历史都保留原始内容，同时从原文中直接挑选少量关键句作为 KEY。

每次开始新一轮请求时，模型上下文由三部分组成：

- **本轮完整工作上下文**：本轮用户请求、模型操作、工具调用、搜索结果、查阅和调研内容、文件读取、中间回复及最终回复都按正常对话上下文进入模型。它们不会在本轮运行中被提前改写成 KEY 或 summary。
- **同一 Session 的正常工作上下文**：当前 Session 的历史继续作为完整对话上下文参与当前请求，并走正常的 summary 压缩机制。只有压缩发生后，才从最新的 summary marker 及其恢复尾部继续，避免重新带回已经压缩掉的旧原文。
- **跨 Session KEY 旁路**：`system prompt` 中额外附带一个紧凑的 active KEY 数组，只包含更早 Session 留下的简单 KEY，默认最多注入 `prompt_key_limit` 条。当前 Session 已产生的 KEY 不会在同一 Session 的新请求里自动回灌，避免刚做完的事情又触发检索。模型侧只看到 KEY 字符串，不携带 `memory://` 路径、session 或 Segment 元数据。

KEY 的路径解析和搜索由后端负责。`memory_search` 调用时会遍历根索引和历史分卷，返回命中的路径与元数据；模型不需要自行拼接或猜测存储位置。只有当前任务确实需要展开某条历史时，才读取对应的长记忆 Segment，未被激活的正文不会进入上下文。

KEY 不是概括，也不是模型改写出来的主题词。KEY 是由一个独立的短 prompt 抽取模型从分段原文中**原样复制**的 1 到 5 个关键句/字段，例如原文是“我昨天去了深圳，后面……”时，KEY 可以直接是“我昨天去了深圳”。抽取后会校验 KEY 是否真实存在于该段原文中，避免模型编造索引。

#### 1. 三层数据模型

| 层级 | 单位 | 作用 |
|---|---|---|
| Session | 一个自然日，格式 `YYYY-MM-DD` | 时间分区和生命周期。时区固定为 `Asia/Shanghai`；跨午夜的一次交互归属于它开始的那一天。 |
| Interaction | 一次用户请求 | 一次用户输入到本轮最终回复的完整交互。用户输入、模型操作、工具调用、工具结果和回复共享同一个 `interaction_id`。整轮失效、删除和审计都以它为单位。 |
| Segment | 可检索的长期记忆块 | 交互内部按内容类型和边界切分。用户请求单独成段；模型操作、工具结果和回复按字数或步数继续分段。实际检索和加载到上下文的是 Segment。 |

Segment 当前有三种 `kind`：

- `user_request`：用户这一轮说了什么。
- `tool_operation`：模型执行过的操作、工具调用与工具结果。
- `assistant_reply`：模型回复和流式输出内容。

默认一个 Segment 最多 `4000` 个字符、`8` 个执行步骤。达到边界后立即封口，保证长任务不会无限膨胀成一个无法定位的大段。

#### 2. 写入生命周期

1. 用户发起请求时，系统先在当天 Session 下分配一个 `interaction_id`，并创建 `manifest.json`。
2. Agent 运行期间，`TurnMemoryRecorder` 观察本轮事件，把用户请求、工具操作和回复写入当前 Segment；达到字数或步骤上限时封口并写入新的 Segment。
3. Segment 封口时先用本地规则写入一个 fallback KEY，把它立即变成一个可检索段，避免等待后台模型造成记忆空洞。
4. 随后后台异步调用 KEY 抽取模型。抽取模型只加载专用的短 prompt，不加载主对话的全部上下文；它读取该 Segment 原文，只返回 `{"keys":["原文关键句"]}`。
5. KEY 抽取完成后重新读取 Segment 的当前状态，确认它仍是 `active`，再合并 KEY 并追加到根索引。这样即使 KEY 抽取仍在进行时用户已经删除或失效了该段，旧结果也不会把记忆“复活”。
6. 抽取失败不会阻塞本轮回答，也不会改坏原 Segment；fallback KEY 仍然可以定位它。

因此，KEY 抽取发生在当前用户请求的关键路径之外：一次请求内保持完整记忆，请求完成或分段封口后才异步整理成长期可检索的记忆。

#### 3. 当前轮次与 Summary 的边界

当前轮次如果步数或上下文量过多，会触发 summary 机制，但同 Session working context、summary 和跨 Session KEY 目录处于不同的上下文层级：

- summary 的压缩范围是当前轮次的工作消息区，用来压缩本轮不断增长的对话、工具调用和调研过程。
- 包含跨 Session KEY 目录的 system prompt 不进入 summary 的压缩输入，也不会因为本轮触发 summary 而被改写、截断或重新概括。
- 已经存在于 active KEY 目录中的更早 Session KEY 保持原样，继续作为额外的一级索引存在。
- 当前 Session 已封口的 Interaction 不进入常驻 system prompt KEY 列表；它仍可通过同 Session 的正常 working context（含 summary）或显式 `memory_search` 召回。
- summary 生成后，最近一段上下文仍按 `summary_retain_tokens` 原样保留，避免当前轮次的近期操作和结论丢失。

每次真正调用 LLM 前都会先做一次 outbound preflight。它用本地保守估算检查 `workingMsgs`，并同时参考最近一次成功请求返回的 prompt tokens；超过 `summary_threshold_tokens`（当前默认 `60000`）时先裁剪超大工具结果，仍然超限就同步压缩当前轮次，最近的有效尾部保留为原文。provider 返回 context-length 400 时也会触发一次压缩后重试。也就是说：

```text
当前轮次内容过长
    -> 发送前 preflight 先裁剪、再压缩当前轮次工作区
    -> 不压缩额外注入的跨 Session KEY 目录

当前轮次结束
    -> 异步切分本轮 Segment
    -> 抽取新的原文 KEY
    -> 新 KEY 不注入当前 Session；进入下一个 Session 后才进入跨 Session KEY 目录
```

summary 负责让当前轮次跑得下去，KEY 目录负责让模型记得以后可以从哪里找回历史；两者不会互相替代。

#### 4. 检索流程

常驻上下文中模型侧只看到紧凑的 KEY 数组，例如：

```json
["我昨天去了深圳","修复登录问题","完成上海调研"]
```

完整路径、`session_id`、`interaction_id`、`segment_id` 和 `kind` 都保存在后端索引里。模型先匹配 KEY，需要确认具体历史内容时再调用工具搜索，由后端完成分卷遍历和路径解析：

```text
system prompt + compact active KEY array（仅跨 Session）
        +
同一 Session 的正常 working context + 当前轮次完整工作上下文
        │
        ├─ 当前任务命中某个 KEY
        │
        ▼
memory_search(query=KEY)   // 后端工具
        │
        ▼
后端搜索 root.jsonl + volumes
        │
        ▼
返回 session_id / interaction_id / segment_id / memory:// target
        │
        ▼
read_file 只读取被激活的 Segment
```

`memory_search` 会同时读取根索引和历史分卷，但只返回 `status=active` 的条目。命中分卷时会先返回分卷文件的 `memory://` 指针；模型需要沿分卷继续查找最终 Segment，而不是把整个分卷当成记忆正文加载。

#### 5. 容量与分卷

根索引不会无限增长。当根 KEY 数量达到 `max_root_index_entries`（当前默认 `80`）时，旧索引整体移入 `index/volumes/volume-*.jsonl`，根索引只留下一个指向该分卷的上级 KEY，例如：

```json
{"key":"最后一条可识别关键句","target":"memory://segmented/index/volumes/volume-20260918T120000.jsonl","type":"volume"}
```

这对应“KEY 加 system prompt 已经装不下时，把目录保存到文件，再用新的索引 KEY 指向文件”的设计。新请求不会自动加载整个分卷；只有根指针或搜索命中分卷后，才按需展开下一层。当前运行参数在 `config/config.json` 的 `segmented_memory` 中配置：

```json
{
  "enabled": true,
  "key_extraction": "llm",
  "max_segment_chars": 4000,
  "max_segment_steps": 8,
  "max_root_index_entries": 80,
  "prompt_key_limit": 80,
  "key_extraction_timeout_sec": 20,
  "key_wait_timeout_sec": 3,
  "max_concurrent_keys": 2
}
```

#### 6. 失效、删除与审计

错误记忆优先“失效”，而不是物理删除：

- 模型发现单段工具结果、文件路径或事实过期时，调用 `memory_invalidate_segment`，写入 `status=invalidated`、`deleted_by=model`、`reason` 和可选的 `replacement_id`。
- 整轮交互错误时，调用 `memory_invalidate_interaction`，级联失效该 Interaction 下所有 Segment 及其 KEY 索引。
- 失效记录仍保留审计信息，但 `memory_search` 和常驻 KEY 目录都会过滤掉非 active 条目。

用户主动删除时，默认以 Interaction 为单位：

- “仅删除本轮记忆”：删除该 Interaction 的所有 Segment、manifest 和指向它们的 KEY，保留聊天记录。
- “删除对话和记忆”：同时删除该 Interaction 的记忆和前端对话记录。
- 附件与生成产物不会因为删除记忆被默认删除；需要时由用户明确选择范围。

后端接口为 `DELETE /api/memory/interactions/{interaction_id}`，前端在每个完整 Interaction 的回复下方提供操作栏。模型可直接使用的记忆工具包括：

- `memory_search`
- `memory_invalidate_segment`
- `memory_invalidate_interaction`
- `memory_delete_segment`
- `memory_delete_interaction`

#### 7. 存储布局

```text
memory/segmented/
  index/
    root.jsonl
    volumes/
      volume-20260918T120000.jsonl
  sessions/
    2026-09-18/
      interactions/
        req-20260918-001/
          manifest.json
          s0001.json
          s0002.json
          s0003.json
```

用户画像、长期记忆和每日摘要仍然保留：

- 用户画像：`memory/user.md`
- 长期记忆：`memory/memory.md`
- 每日摘要：`memory/date-memory/YYYY-MM-DD.md`

分层 KEY 记忆是实时、可追溯的情景记忆层；会话结束后的静默总结器继续负责高价值偏好和长期事实的稳定沉淀。两者职责分开，互相补充。

### Viewer

- 独立 filemanager viewer：`/viewer.html?file=...`
- 文字对话内嵌 viewer：`/viewer.html?file=...&embedded=1`
- 语音对话的 `show_result`：打开完整 `index-fm.html` 新页面
- viewer 本地依赖位于 `filemanager/vendor/`，不依赖 CDN。
- `show_result` 会把结果路径通过 SSE 发给文字页面，自动打开右侧 viewer。

## 会话与数据

| 数据 | 位置 |
|---|---|
| 会话记录 | `memory/sessions/<session>/` |
| 会话 JSON | `memory/sessions/<session>/<session>.json` |
| 事件日志 | `memory/sessions/<session>/events.jsonl` |
| 用量 | `memory/sessions/<session>/usage.json` |
| 运行日志 | `runs/` |
| 工作文件 | `workspace/` |
| Agent 交付物 | `workspace/result/` |
| 本地记忆 | `memory/` |
| Skills | `skills/` |

## 目录结构

```text
Fairy/
├─ agent/                  Go Agent 引擎
├─ frontend/               React + Vite + Node API
├─ voice/                  TTS / STT 服务
├─ filemanager/            文件浏览、viewer 与本地预览库
├─ config/
│  ├─ config.json
│  ├─ system/              主 system prompt
│  ├─ modules/             按需 prompt
│  └─ tools/               工具 schema
├─ memory/                 本地记忆
├─ skills/                 Skills
├─ workspace/              工作区与交付物
├─ runs/                    Agent run logs
├─ scripts/                构建、启动、检查脚本
├─ src-tauri/              Tauri 桌面壳
├─ tests/                  行为协议测试
├─ tool_gateway/           HTTP 工具兼容网关与 mock
├─ Fairy.exe               Windows 桌面版
├─ 启动 Fairy.cmd
└─ 启动 Fairy.sh
```

## TODO

### CLongEval 中文长上下文数据集评估

状态：待测试，暂不下载或运行。

#### 1. 安装 `git-xet`

Windows：

```powershell
winget install git-xet
```

参考：<https://hf.co/docs/hub/git-xet>

#### 2. 克隆数据集

```bash
git clone https://huggingface.co/datasets/zexuanqiu22/CLongEval
```

如果只想克隆指针，不立即下载大文件：

```bash
GIT_LFS_SKIP_SMUDGE=1 git clone https://huggingface.co/datasets/zexuanqiu22/CLongEval
```

#### 3. 安装 Hugging Face CLI

Windows PowerShell：

```powershell
powershell -ExecutionPolicy ByPass -c "irm https://hf.co/cli/install.ps1 | iex"
```

#### 4. 下载数据集

```bash
hf download zexuanqiu22/CLongEval --repo-type=dataset
```

## 常用命令

```bash
# 开发
pnpm dev
pnpm service:restart

# Tauri
pnpm tauri:dev
pnpm tauri:build

# Prompt
pnpm prompt:build
pnpm prompt:check

# Python
pnpm setup:python

# 前端构建
pnpm --dir frontend build

# Agent 测试
cd agent && go test ./...

# 启动器测试
pnpm test:launcher

# 跨平台检查
pnpm test:cross-platform

# HTTP tool mock
pnpm gateway:mock
pnpm gateway:status
pnpm gateway:stop
```

## 安全

- Agent 运行在真实主机上，会执行真实命令、进程和网络请求。
- Windows 上优先使用 `powershell` 工具；它使用 PowerShell 7，固定 UTF-8 输出编码。`bash` 保留为兼容别名，同样走 PowerShell。普通非零退出码作为命令结果返回，不视为工具基础设施失败。
- 每条 stdout/stderr 默认只保留尾部 64KB；超过上限时完整输出写入 spill 文件并通过 `stdout_path` / `stderr_path` 返回。
- 后台任务记录 PID 和退出码，`bash_job` 可用 `offset_bytes` 与 `next_offset` 增量读取，不会重复消费同一段输出。
- 策略检查会解析 `;`、`&&`、`||`、管道、常见嵌套 shell 和引用路径；白名单存在时每条子命令都必须通过。
- `cua-auto` 直接操作当前用户桌面，可能移动真实鼠标、改写剪贴板或改变窗口焦点，并不是 VM 或后台隔离层。
- `computer_window(action=close)`、键盘输入和鼠标点击属于高风险动作；自动化前应先确认当前活动窗口，执行后必须验证结果。
- `bash` 默认拦截危险命令及受保护路径，必要时通过 `sandbox_permissions: "danger-full-access"` 和 `justification` 请求宿主审批例外。
- 不读取、展示或上传 `.env`、API key、密钥文件等敏感内容。
- 本地 viewer 的 embedded 模式使用 filemanager 的文件读取通道；不要把任意远程 URL 当作本地文件路径传入。

## 许可

项目当前未声明开源许可证。使用、分发或商用前请先确认仓库授权范围。
