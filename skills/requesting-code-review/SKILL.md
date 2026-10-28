---
name: requesting-code-review
description: 在完成任务、实现重大特性，或合并前用于验证工作是否满足需求时使用
metadata:
  short-description: "Codex skill: requesting-code-review"
  tags:
    - code-review
    - review
  triggers:
    - 代码评审
    - review
    - 审查代码
    - 检查代码
  priority: 70
---

# 请求代码评审

派发代码评审子 agent，以在问题级联之前发现它们。评审者获得精准构造的上下文以做评估 —— 永远不会是你会话的历史。

**核心原则：** 尽早评审，经常评审。

## 何时请求评审

**强制：**

- 在子 agent 驱动的开发中，每完成一项任务后
- 完成重大特性后
- 合并到 main 之前

**可选但有价值：**

- 卡住时（换个视角）
- 重构之前（基线检查）
- 修复复杂 bug 之后

## 如何请求

**1. 获取 git SHA：**

```bash
BASE_SHA=$(git rev-parse HEAD~1)  # 或 origin/main
HEAD_SHA=$(git rev-parse HEAD)
```

**2. 派发代码评审子 agent：**

派发 `general-purpose` 子 agent，填充 [code-reviewer.md](code-reviewer.md) 处的模板

**占位符：**

- `{DESCRIPTION}` —— 你构建内容的简要概述
- `{PLAN_OR_REQUIREMENTS}` —— 它应当做什么
- `{BASE_SHA}` —— 起始 commit
- `{HEAD_SHA}` —— 结束 commit

**3. 处理反馈：**

- 立即修复 Critical 问题
- 在继续之前修复 Important 问题
- 记录 Minor 问题以备后续
- 若评审者错了要反驳（带理由）

## 例子

```
[刚完成任务 2：添加验证函数]

你：让我在继续之前请求代码评审。

BASE_SHA=$(git log --oneline | grep "Task 1" | head -1 | awk '{print $1}')
HEAD_SHA=$(git rev-parse HEAD)

[派发代码评审子 agent]
  DESCRIPTION: 添加了 verifyIndex() 和 repairIndex()，含 4 类问题
  PLAN_OR_REQUIREMENTS: 来自 docs/superpowers/plans/deployment-plan.md 的任务 2
  BASE_SHA: a7981ec
  HEAD_SHA: 3df7661

[子 agent 返回]:
  Strengths: 架构清晰、测试真实
  Issues:
    Important: 缺少进度指示
    Minor: 报告间隔的魔数 (100)
  Assessment: 可以继续

你：[修复进度指示]
[继续到任务 3]
```

## 常见自我说服

| 借口 | 现实 |
|------|------|
| "我会自己评审 diff，而不是派发评审者" | 你是协调者 —— 直接评审 diff 会消耗你继续推进工作所需的上下文窗口。派发评审者子 agent：diff 与评估都活在它的上下文里，只有结论回到你这里。 |
| "评审者需要我整个会话历史才能理解这次改动" | 提供精准构造的上下文，而非你的会话历史。这样让评审者聚焦工作产物，而不是你的思考过程。 |

## 危险信号

**绝不：**

- 因为"很简单"而跳过评审
- 忽略 Critical 问题
- 在未修复的 Important 问题存在时继续
- 与有效的技术反馈争辩

**若评审者错了：**

- 用技术理由反驳
- 展示能证明它有效的代码/测试
- 请求澄清

模板位置：[code-reviewer.md](code-reviewer.md)