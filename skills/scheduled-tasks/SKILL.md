---
name: scheduled-tasks
description: 给主人定闹钟、定提醒、定周期任务。当用户说"X 分钟后提醒我…""明天早上 8 点叫我…""每天/每周几点做某事""到点提醒我点外卖/吃药/开会""帮我盯着，过一会儿再说一次"时使用。到点由 Fairy 自己跑一轮，并把结果推回主人最近用的那条通道（QQ 或微信），也可以只留在会话里。适合一次性提醒、每日/每周固定任务、每 N 分钟轮询一件事。
metadata:
  short-description: "Reminders and recurring tasks on Fairy's own scheduler"
  tags:
    - reminder
    - schedule
    - alarm
    - timer
    - cron
  triggers:
    - 提醒我
    - 叫我
    - 定个闹钟
    - 定时
    - 每天
    - 每周
    - 分钟后
    - 小时后
    - remind me
    - set a reminder
    - every day
tags:
  - schedule
  - reminder
  - timer
  - automation
triggers:
  - 提醒我
  - 叫我
  - 定个闹钟
  - 定时
  - 每天
  - 每周
  - 分钟后
  - 小时后
  - remind me
  - set a reminder
  - every day
priority: 50
---

# 定时任务

主人说"两分钟后提醒我点外卖""每天八点提醒我吃药"这类话时，**不要**去写 crontab、
不要 `sleep` 占着一个回合、也不要回答"做不到"。Fairy 自己有一个调度器，
用下面这个命令把任务交给它，然后简短回一句确认。

## 命令

```bash
cd "$AGENT_REPO_ROOT"

# 一次性：两分钟后提醒主人点外卖。不写推送目标时，默认推回他最近用的那条通道
# （在微信里提的就推微信，在 QQ 里提的就推 QQ；从网页端提的用"最近接上的那条"）
node skills/scheduled-tasks/schedule.mjs add \
  --title "点外卖" --in 2m \
  --prompt "提醒主人：该点外卖了"

# 指定通道，或干脆不推送（只留在会话里）
node skills/scheduled-tasks/schedule.mjs add --title "吃药" --daily 08:00 --prompt "提醒主人吃药" --notify-wechat
node skills/scheduled-tasks/schedule.mjs add --title "整理笔记" --every 60 --prompt "把今天的笔记归一下" --no-notify

# 每天 08:00 推到 QQ
node skills/scheduled-tasks/schedule.mjs add \
  --title "吃药提醒" --daily 08:00 \
  --prompt "提醒主人吃早饭时的药" --notify-qq

# 每周五 09:30 / 每 30 分钟 / 指定时刻（ISO）
node skills/scheduled-tasks/schedule.mjs add --title "周报" --weekly 5 09:30 --prompt "提醒主人写周报"
node skills/scheduled-tasks/schedule.mjs add --title "看盘" --every 30 --prompt "看一眼有没有需要提醒主人的行情"
node skills/scheduled-tasks/schedule.mjs add --title "面试" --at "2026-10-03T09:00:00+08:00" --prompt "提醒主人面试"

# 看 / 立刻跑 / 删
node skills/scheduled-tasks/schedule.mjs list
node skills/scheduled-tasks/schedule.mjs run --id st_xxxxxxxxxxxx
node skills/scheduled-tasks/schedule.mjs remove --id st_xxxxxxxxxxxx
```

## 规则

- **时间**：`--in 2m|90s|1h`、`--daily HH:MM`、`--weekly 1-7 HH:MM`（1 是周一）、
  `--every <分钟，最少 5>`、`--at <ISO 时间>`。一律按机器时区（Asia/Shanghai）。
- **`--prompt` 写给"到点时的自己"**：到点是在主人的会话里跑一轮正常任务，所以
  写清要做什么、要产出什么，例如"看看今天上海的天气，连提醒主人带伞一起回复"。
  不要写成对主人说的话（那是回复，不是任务）。
- **推送**：不带推送参数时脚本自己选目标——优先"这条会话自己那条通道"，网页端提的
  则用服务端给的"最近接上的那条"。要指定就用 `--notify-qq` / `--notify-wechat`
  （都可以不带值，脚本从配置里找那个人），只想留在会话里就 `--no-notify`。
- **两个平台都有"人得先说过话"的规矩**：QQ 走主动消息（额度与近期互动），微信必须
  带 `context_token`（对方最近给这个 bot 发过消息，桥落盘才有）。所以推送失败时脚本
  会明确报出来（"微信只能发给最近跟这个 bot 说过话的人"），这时要么让主人先发一条，
  要么改用 --no-notify，不要假装发出去了。
- **确认话术**：建完只回一句"好，X 分钟后提醒你点外卖"这种，把 `schedule_text`
  和 `next_run_text` 说清楚即可，不要长篇解释。
- **改主意**：主人说"不用提醒了/改到 9 点"，用 `list` 找到 id，再 `remove` 或
  `add` 一条新的。不要留两条重复的。
- 任务跟账号走：你在谁的会话里跑，任务就属于谁（脚本自己从
  `AGENT_SESSION_FILE` 认出账号），成员之间互相看不到。
