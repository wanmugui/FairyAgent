---
name: document-writing
description: "当任务进入结构化文档的成文阶段时使用，例如报告、proposal、spec、decision doc、研究报告最终撰写、长篇结构化 Markdown。仅在 writing / final synthesis 阶段触发，不用于前期搜索、取证、subtask 材料收集。"
tags:
  - writing
  - document
  - reflection
triggers:
  - 写报告
  - 写文档
  - 起草
  - 成文
  - 撰写报告
  - proposal
  - spec
priority: 48
---

# Document Writing

## 核心约束

- 先判断交付形态：用户明确要求导出文件、任务天然需要文件、或输出内容长度过长时，走“文件成文流程”；其他情况，走“直接回复流程”。
- 文件成文流程中，不要在中间阶段直接把整篇文章作为 assistant 最终内容输出。
- 文件成文流程中的章节内容必须逐节追加到 `/mnt/data/result/<slug>.md`，不要先生成完整全文再一次性写入。
- 直接回复流程不写结果文件，也不生成 section index；但仍必须先在章节大纲中列出 section-evidence，再把最终成文直接作为 assistant 回复。
- 默认由主线程完成成文与修订，不把整篇最终文档委派给子任务。
- “深度研究 / 报告 / 综述 / 分析”这些任务类型本身不等于必须导出文件；它们只说明需要结构化成文与证据覆盖。

### 交付形态判定口径

走“文件成文流程”的情况：

- 用户明确要求导出、生成、保存、落盘、给文件链接，或指定 `.md` / `.docx` / `.pdf` / `.pptx` 等文件形态
- 任务天然需要文件承载，例如正式报告、可提交材料、长篇方案、归档文档、可复用模板、需要后续编辑的文档
- 输出内容长度过长，不适合直接塞进对话；例如章节很多、表格 / 附录 / 引用清单较长、需要额外复核，或直接回复会显著影响可读性

走“直接回复流程”的情况：

- 用户只是要对话内的结论、简报、综述、研究回答或短中篇报告
- 虽然触发了 deep-research，但最终内容可以在对话中清晰呈现
- 用户没有要求文件，且没有明显的归档、提交、复用或超长篇幅需求

## 标准流程

### 1. 先生成章节大纲与 section-evidence

开始写正文之前，先生成最终文档的章节大纲。大纲里的每个顶层 section 都必须同时列出 section-evidence。

要求：

- section 必须是用户可见的主章节
- 标题不算 section
- 附录 / 对比矩阵 / 风险章节如果独立展示，也算 section
- 每个 section 必须列出：
  - `section_goal`：这一节要回答什么
  - `evidence_to_use`：这一节应该吸收哪些已有材料 / 子任务结果 / 关键事实
  - `open_issues`：这一节有哪些未确认项、冲突或风险必须保留
  - `citations`：这一节涉及哪些引用来源，是否包含用户上传文件 / 本地文档
  - `best_format`：这一节更适合段落、表格、清单、时间线还是对比矩阵

如果当前还没有清晰章节大纲，先补章节大纲和 section-evidence，再进入成文。

不要跳过这一步直接写“漂亮但失真”的总稿。

### 2. 选择成文流程

完成章节大纲与 section-evidence 后，按“交付形态判定口径”选择流程。

如果是直接回复流程：
- 直接在 assistant 回复中输出完整成文。
- 回复内容必须按章节大纲展开，并覆盖每个 section 的 section-evidence。

如果是文件成文流程，继续执行下面的文件步骤。

### 3. 初始化结果文件

文件成文流程不是把已经能一次性回复的全文再塞进文件里；它用于长文、正式文档或需要文件承载的结果。

先用 `bash` 运行 Python，创建或清空目标文件：

```text
/mnt/data/result/<slug>.md
```

要求：

- 用 Python `Path(...).parent.mkdir(parents=True, exist_ok=True)` 确保目录存在
- 用 Python `Path(...).write_text("", encoding="utf-8")` 初始化结果文件
- 不要在 assistant 消息里先生成完整全文；如果全文已经适合直接回复，应走“直接回复流程”

### 4. 逐章节成文并追加写入

按章节大纲顺序逐节成文。每完成一个 section，立即用 `bash` 运行 Python 追加写入结果文件。

追加写入示例：

```python
from pathlib import Path

path = Path("/mnt/data/result/<slug>.md")
section_text = """...当前 section 的完整 Markdown..."""

with path.open("a", encoding="utf-8") as f:
    if path.stat().st_size > 0:
        f.write("\n\n")
    f.write(section_text.rstrip() + "\n")
```

要求：

- 每次只生成并追加当前 section，不要把全文一次性放进一个 Python 字符串里写入
- 当前 section 必须覆盖大纲中对应的 `section_goal`、`evidence_to_use`、`open_issues`、`citations` 和 `best_format`
- 表格、清单、时间线、对比矩阵等适合结构化呈现的内容，应直接写入该 section 或附录 section
- 每个 section 追加完成后，主线程做一次很轻的本节自查：目标是否回答、证据是否支撑、引用是否保留、风险是否遗漏
- 如果本节自查发现问题，先修本节再继续下一个 section；不要把明显有问题的章节继续堆到文件里

### 5. 做一次轻量全文自查

所有 section 都追加完成后，主线程做一次轻量全文自查，重点看：

- section 是否覆盖了章节大纲里的 section-evidence
- 结论是否被证据支撑，是否出现无根据扩写
- section 之间是否前后矛盾、重复或术语不一致
- 风险 / 未确认项是否在结果文件里消失
- 应保留的表格 / 清单 / 时间线是否被压扁成泛化段落

默认不生成 section index，不逐章节调用 `reflection`。

### 6. 按需调用 reflection

- 产出内容需要严格保证可信度
- 文档用于正式提交、决策或对外发布
- 证据冲突、风险高，或主线程自查发现明显不确定问题
- 单个 section 特别复杂，主线程难以一次性判断是否覆盖完整

调用方式：

- 对存在问题的地方，按章节做局部 `reflection`

示例：
- `[Document File]`：`/mnt/data/result/<slug>.md`
- `[目标]`：这篇文档要完成什么
- `[Section Evidence]`：必须吸收的事实 / 子任务结果 / 引用 / 风险项
- `[Open Issues]`：必须保留的未确认项或冲突
- `[检查项]`：证据支撑、遗漏风险、章节矛盾、无根据扩写、结构化材料是否被压扁

### 7. 局部修订

根据主线程自查和可选 `reflection` 结果修订：

- 只修发现的问题，不重写无关内容
- 优先按 section 局部修订：读取相关片段，生成替换后的 section，再用 Python 脚本按标题边界替换该 section
- 如果改动很小，也可以用 Python 对明确文本片段做精确替换
- 修完后快速确认全文一致性，不再重跑整套 review

### 8. 输出定稿

- 若结果文件较短，直接在回复中输出结果文件；否则，简要输出结果总结。
- 在回复的最后附上定稿文件链接

## 不要做的事

- 不要写单独的中间文档；文件成文流程只维护 `/mnt/data/result/<slug>.md`
- 不要先生成完整全文再一次性写入文件；如果能这样做，通常应走直接回复流程
- 不要把多个 section 的独特结果为了省字硬并成一段
- 不要让用户可见结果里凭空出现比证据更强的结论
