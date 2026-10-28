# Fairy 验证矩阵

## 1. Agent / Go

工作目录：`agent`

```powershell
go test ./...
```

优先先跑最接近的包级测试，例如：

```powershell
go test ./internal/biz/tool/builtin -run TestPlan
go test . -run TestReasoning
```

最终门禁仍应至少跑一次完整 `go test ./...`。涉及实际工具调用时，再调用一次真实工具并保存输出或工件。

## 2. 前端构建

前端依赖存在时：

```powershell
frontend\node_modules\.bin\vite.cmd build --outDir "$env:TEMP\fairy-frontend-build" --emptyOutDir
```

同时检查后端入口语法：

```powershell
node --check frontend\server.cjs
```

构建通过只证明可以打包，不证明点击、路由、状态同步或视觉行为正确。

## 3. 前端交互点击

必须打开真实页面执行 [frontend-click-test.md](frontend-click-test.md)。最低覆盖：

- 主动入口：按钮/菜单/链接点击后到达目标 URL 或打开目标面板。
- 状态往返：打开、关闭、再次打开；开启、关闭、再次开启。
- 持久化：刷新页面或重新进入路由后仍保持正确状态。
- 同步：主界面、`/voice`、设置弹窗之间观察同一份配置。
- 主题：若改动涉及皮肤，必须点击验证 `base` 和 `zzz` 两套计算样式。

## 4. Prompt

Prompt 源文件：`config/system/parts/**`、`config/system/zh.md`、`config/tools/schemas.json`。

```powershell
pnpm prompt:build
pnpm prompt:check
```

如果只跑了 `build` 没有跑 `check`，不算完成。

## 5. 配置与持久化

配置改动至少验证：

1. 写接口成功。
2. 重新读取返回新值。
3. 刷新页面后 UI 仍然生效。
4. 相关模块没有回退到默认值。

涉及设置面板时，打开、修改、关闭、重开是强制路径。

## 6. 语音 / TTS / STT

- TTS：真实提交一段含有标点、数字和中英混合文本，确认有音频产物并可播放。
- STT：使用真实音频或麦克风输入，确认识别文本、入队和发送状态。
- 语音页设置：模型、自动回读、备用模型、主题都需要在共享设置面板和语音页工具栏之间往返验证。

只 mock 网络或只检查函数被调用不算集成回归。

## 7. 发布

```powershell
pnpm agent:build
```

如涉及桌面壳、Tauri 或启动脚本，还需要构建/重启对应产物，并重新打开页面 smoke。推送前记录 commit、远端地址和构建结果。