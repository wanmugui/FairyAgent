---
name: fairy-engineering
description: "当用户要求修改、修复、重构或实现 Fairy 仓库中的代码、配置、前端交互、Agent 运行时或发布流程时使用。强制建立回归基线并运行相关自动化回归；任何前端、UI、交互或视觉改动必须完成真实浏览器点击测试，禁止只凭构建通过宣告完成。"
metadata:
  short-description: "Fairy code changes with mandatory regression and click tests"
  tags:
    - engineering
    - code
    - regression
    - frontend
    - testing
  triggers:
    - 修改代码
    - 修复
    - 回归测试
    - 点击测试
  priority: 80
---

# Fairy Engineering

适用于 `.` 内的工程改动。目标不是“代码看起来写完了”，而是“行为已被回归验证，交互已被真实点击验证”。

## 不可协商的完成门禁

1. **编辑前先建立基线。** 找到与目标最接近的现有测试或可复现步骤，先运行一次，记录它原本是 pass 还是 fail。若没有测试，先补最小测试，或建立可重复的点击步骤。
2. **编辑后必须跑回归。** 至少运行受影响的测试集；涉及 Agent 或共享运行时默认运行 `go test ./...`。测试、构建、类型检查不能互相替代。
3. **前端、UI、交互、视觉改动必须真实点击测试。** 必须打开实际页面，点击触发入口，并验证 URL、DOM 状态、可见内容、持久化结果和控制台错误。只跑 `vite build` 不算完成。
4. **设计类改动必须有视觉证据。** 文本回复或“看起来应该没问题”不是证据。保留截图、元素状态、尺寸/位置或前后对比。
5. **失败不能掩盖。** 回归失败时先修根因；不能通过删测试、放宽断言、跳过测试、只改文档或把命令标成“可选”来结束任务。
6. **没有证据不得声称完成。** 最终说明必须列出：变更文件、执行的命令、真实结果、点击场景、截图/URL/状态证据、未覆盖风险。

## 工作流

1. 明确影响面：前端、Agent、配置、Prompt、工具、语音、发布，还是多项组合。
2. 读取最近的 `AGENTS.md`、相关实现和现有测试。不要先改后补理解。
3. 建立回归基线，同时写出可执行的 `done_when`。
4. 做最小必要修改，避免顺手重构无关模块。
5. 运行 [references/verification-matrix.md](references/verification-matrix.md) 中对应回归。
6. 若涉及前端/UI/交互/视觉，按 [references/frontend-click-test.md](references/frontend-click-test.md) 执行点击测试。
7. 运行 `scripts/verify.ps1` 做统一门禁；UI 改动必须带真实点击证据通过。
8. 提交或推送前再次确认工作树、测试结果和证据一致。

## 变更类型与强制验证

- **Agent / Go / Tool / Plan / JEV**：`go test ./...`；涉及工具调用时再做一次真实工具调用回归。
- **前端逻辑或界面**：Vite 构建 + 浏览器真实点击 + 控制台检查。设置、模型、主题、语音菜单等必须验证开关往返和重新打开后的持久化。
- **Prompt / 工具 schema**：`pnpm prompt:check`；Prompt 源文件改动时先 `pnpm prompt:build`。
- **配置和持久化**：验证 API 写入、重新读取、页面重载后的实际效果三层都一致。
- **语音 / TTS / STT**：调用真实服务入口，检查可播放产物或识别结果，不能只 mock 函数返回。
- **发布流程**：构建对应产物，重启服务或桌面进程，再做一次真实页面 smoke。
- **纯文档改动**：除非文档包含可执行命令，否则不强制点击测试；命令改动仍要实际运行。

## 强制点击测试的最低标准

每个交互至少验证：

1. 入口可见且可点击。
2. 点击后 URL、hash、面板、弹窗、开关或内容确实发生变化。
3. 关闭、再次打开或刷新后，状态符合预期。
4. 页面没有新增 console error。
5. 布局改动检查是否遮挡、溢出、错位或响应式破坏。

涉及设置面板、主题或 `/voice` 时，还必须验证主界面和语音页同步。涉及 `base` / `zzz` 皮肤时，两套主题都要点击切换并读取实际计算样式。

## 完成输出格式

```text
变更：
- <file>: <what changed>

基线：
- <command/scenario>: <before>

回归：
- <command>: <actual result>
- 浏览器点击：<URL/action/expected/actual/screenshot or state>

未覆盖：
- <remaining risk or none>
```

## 参考

- [验证矩阵](references/verification-matrix.md)：按改动类型选择命令和回归范围。
- [前端点击测试协议](references/frontend-click-test.md)：浏览器点击、状态断言和视觉检查的最低要求。
- `scripts/verify.ps1`：检测改动类型并运行基础回归；UI 改动需要显式点击证据。