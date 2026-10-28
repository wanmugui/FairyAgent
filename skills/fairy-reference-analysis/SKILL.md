---
name: fairy-reference-analysis
description: "当用户希望从游戏、桌面应用、网站或软件中提炼 UI/UX、视觉样式、交互流程、玩法机制、经济系统或结构设计，或在明确授权范围内分析本地应用与协议时使用。优先采用截图、录屏、公开界面、DOM/日志和非加密数据；禁止绕过 DRM、反作弊、付费、账号或许可证限制，也不直接复制或再分发目标资产。"
metadata:
  short-description: "Analyze UI, mechanics, and authorized software structure"
  tags:
    - reverse-engineering
    - game-ui
    - mechanics
    - design-reference
    - electron
    - rpg-maker
  triggers:
    - 游戏 UI
    - 机制设计
    - 设计参考
    - 逆向分析
    - 反编译
    - 抓包
    - 协议分析
  priority: 75
---

# Fairy Reference Analysis

用于把已有产品中的**设计语言、交互方式和机制结构**转化为可复用的设计知识。目标是提炼模式，不是复制资产、绕过保护或复刻受版权保护的内容。

## 授权与安全边界

允许：

- 用户拥有或明确获授权的本地软件、游戏、应用和服务器。
- 公开页面、公开截图、宣传片、试玩视频、官方文档和用户可见界面。
- 对未加密配置、清单、日志、DOM、公开 API 和本地数据做只读分析。
- 用截图、录屏、OCR、颜色采样和交互录制度量 UI。

禁止：

- 绕过 DRM、许可证、付费墙、反作弊、签名校验或账号权限。
- 破解、提取或再分发受保护的素材、字体、音频、模型、剧情文本或私有数据。
- 使用恶意软件、凭据窃取、未授权入侵或规避服务条款。
- 把目标资产直接复制进产品；只能提炼颜色、层级、节奏、布局原则和机制模型。

授权不清楚时，先停下并让用户确认；不要默认“本地安装 = 允许逆向保护机制”。

## 三种模式

### 1. 视觉与 UI 参考

从截图、录屏、运行中的界面、DOM 或公开素材提炼：

- 布局栅格、层级、留白、对齐和切角
- 颜色、渐变、描边、阴影、纹理和透明度
- 字体层级、字重、字号和数字样式
- 按钮、弹窗、列表、卡片、状态条、图标和反馈
- 动效曲线、持续时间、转场和状态切换
- 信息密度、交互路径和状态机

详细协议见 [references/visual-reference.md](references/visual-reference.md)。

### 2. 玩法与机制设计

从实际游玩、录像、界面反馈和非加密数据中提炼：

- 核心循环、阶段循环和长期循环
- 资源、行动点、冷却、风险和收益
- 战斗公式、概率、成长曲线和平衡参数
- 解锁节奏、随机性、保底、经济与商店
- 玩家决策空间、信息不对称和反馈闭环

详细协议见 [references/game-mechanics.md](references/game-mechanics.md)。

### 3. 授权技术分析

仅对用户拥有或书面授权且不涉及保护绕过的目标：

- 识别引擎、运行时、目录结构和依赖
- 静态盘点清单、配置、协议描述和未加密数据格式
- 观察进程、日志、网络请求和状态变化
- 建立最小复现实验和可验证的行为模型

详细边界与流程见 [references/authorized-technical.md](references/authorized-technical.md)。

## 工作流

1. 明确目标和授权边界：要提炼 UI、机制，还是做技术结构分析。
2. 先做只读盘点，不修改目标、不解密受保护资产。
3. 优先用真实运行界面采集证据；加密或混淆内容只用可见行为分析。
4. 把观察事实与推断分开记录。
5. 输出中性设计文档：设计令牌、交互流程、机制模型、数据表和实现建议。
6. 若用户要求实现，只实现抽象后的原则，不复制原资产或受保护内容。
7. 方案涉及代码时，按 `fairy-engineering` 执行回归；涉及前端时继续做真实点击测试。

## 目标盘点脚本

先运行：

```powershell
powershell -ExecutionPolicy Bypass -File skills\fairy-reference-analysis\scripts\inventory-target.ps1 `
  -TargetPath "D:\steam\steamapps\common\DRAPLINE"
```

脚本只读扫描目标目录并生成 Markdown 清单，不复制、不解密、不修改目标。

对 RPG Maker 系游戏可继续生成机制统计摘要：

```powershell
python skills\fairy-reference-analysis\scripts\summarize-rmmz-mechanics.py `
  "D:\steam\steamapps\common\DRAPLINE" `
  --output workspace\reference-analysis\DRAPLINE-mechanics-summary.md
```

该脚本只输出数量、分布和数值范围，不导出剧情、对白或原始数据表。

## 输出格式

```text
目标与授权：
- <target / basis>

观察事实：
- <UI / interaction / mechanic evidence>

设计提炼：
- Layout:
- Color / type / shape:
- Motion:
- Interaction:
- Mechanics / economy:

实现建议：
- <how to apply the principles without copying assets>

未验证：
- <unknowns or authorization gaps>
```

## 参考

- [视觉与 UI 参考协议](references/visual-reference.md)
- [玩法机制分析协议](references/game-mechanics.md)
- [授权技术分析协议](references/authorized-technical.md)