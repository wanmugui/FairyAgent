---
name: mission_overview
description: 简要说明本 agent-loop 测试框架的工作方式——技能注册表与配置的工具会暴露给 Agent。
tags: [agent-framework, mission, overview]
triggers: [agent 框架怎么工作, mission 框架, 技能注册表原理, agent-loop 机制]
priority: 15
---

你正运行在 mission agent-loop 框架中。该框架的工作方式如下：

1. 将配置中声明的 Skill 注册表注入系统提示词；需要具体指引时，再按注册表位置读取对应的 `SKILL.md`，而不是把全部技能正文预先注入上下文。
2. 从 `config/tools/schemas.json` 加载工具定义，并按照 `tool_runtime.tools` 的 backend 配置将工具注册为可调用函数：本地工具在 Agent 进程内执行，服务型工具通过统一 HTTP 工具服务调用。
3. 循环往复：模型响应 -> 工具调用 -> 工具结果 -> 下一次模型请求，直到没有工具调用为止。
4. 把完整的消息历史保存到 `runs/<timestamp>.json` 供后续查看。

