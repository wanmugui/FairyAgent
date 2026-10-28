---
name: qq-bot
description: "把 QQ 消息接到本机 Fairy：QQ 私聊或群里 @ 机器人即可对话，回复（文本与图片产物）发回 QQ。当用户要求'接入 QQ'、'QQ 机器人上线/重启/看状态'、'为什么 QQ 没回复'时使用。"
tags:
  - qq
  - im
  - channel
triggers:
  - 接入 QQ
  - QQ 机器人
  - QQ 没回复
  - qq bot
priority: 60
---

# QQ Bot Skill

把 QQ 当作 Fairy 的一个输入输出通道。桥接进程长期在线，收到消息后调用本机
`POST /api/chat`，把最终回复与图片产物发回 QQ。

## 依赖与配置

- 依赖：`pip install -r skills/qq-bot/requirements.txt`（已装在 Miniforge `fairy` 环境）
- 凭据：`~/.config/fairy/qq-bot.json`（`600`，仓库外，勿入版本库）
- 策略：仓库内 `config/channels.json` 的 `qq` 段

## 启停与排障

```bash
sudo systemctl status fairy-qq-bot
sudo systemctl restart fairy-qq-bot
journalctl -u fairy-qq-bot -n 100 -f
```

白名单为空时机器人拒绝所有消息——这是刻意的默认值。放行步骤：让对方发一条消息，
从日志取 `拒绝未授权来源 ... id=<openid>`，填进 `config/channels.json` 的
`allowC2C`（私聊）或 `allowGroups`（群），再重启。

## 能力边界

- 只能发送**图片**（平台限制），其它文件在消息里给出本机路径。
- 每个回合最多两条文本（超过 8 秒才补一条"正在处理"）。
- 长任务若超过 5 分钟，回复可能因被动窗口过期发送失败，日志会记录。

## 注意

仓库 `.gitattributes` 把 `*.py` 标记为 `eol=crlf`，检出后 `./bot.py` 的直接执行
会因 shebang 带 CR 而失败。始终用 `python bot.py` 或 systemd 单元（显式指定解释器）。
