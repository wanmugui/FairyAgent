---
name: wechat-bot
description: Use when 需要把微信官方 iLink 私聊接到本机 Fairy，或主动向指定微信用户推送文本、图片、视频、文件。触发场景：用户要求「接微信」「微信发消息」「把文件发到微信」「发视频到微信」「为什么微信没回」「wechatbot push」「微信通道没反应」。
tags:
  - wechat
  - bot
  - im
  - channel
triggers:
  - 接微信
  - 微信发消息
  - 把文件发到微信
  - 发视频到微信
  - 为什么微信没回
  - 微信通道没反应
priority: 60
---

# 微信通道（官方 iLink 私聊）

把微信私聊桥接到本机 Fairy：收消息 → 白名单 → 调 `/api/chat` → 回消息。
同一套通道也支持**主动推送**：文本、图片、视频、任意文件。

## 何时用

- 主人要求接入 / 重启 / 排查微信通道
- 主动往某个微信用户发消息，或把产物（图片、视频、报告、压缩包）推过去
- 微信没回、收不到、重复回复等通道问题

## 模块

| 文件 | 作用 |
|---|---|
| `login.py` / `link.py` | 扫码登录、账号绑定 |
| `wechatbot/ilink.py` | iLink 协议客户端：收发文本、图片、视频、文件 |
| `wechatbot/bot.py` | 长轮询收消息的主循环 |
| `wechatbot/fairy_client.py` | 调 Fairy `/api/chat` 并解析 SSE |
| `wechatbot/policy.py` | 白名单与会话映射（纯逻辑） |
| `wechatbot/state.py` | 落盘 `context_token`（**主动推送的前提**） |
| `wechatbot/filter.py` | 出站文本清洗与分片 |
| `wechatbot/push.py` | 主动推送 CLI |

凭据按账号分文件放在 `~/.config/fairy/wechat/`，运行期状态（`context_token`、`last-media`）同目录。
**这些文件是私密的，不要提交进仓库、不要打印到对话里。**

## 主动推送

```bash
cd skills/wechat-bot
TO=o9cq...@im.wechat

# 先干跑：只查账号和 token，不发
python3 -m wechatbot.push --to "$TO" --video clip.mp4 --file report.zip --dry-run

# 真发
python3 -m wechatbot.push --to "$TO" --text "看下这个" --video clip.mp4 --file report.zip
```

`--image` / `--video` / `--file` 都可重复，发送顺序固定为 **文本 → 图片 → 视频 → 文件**。
文件不存在会在发之前就拦下并退出 1，不会发一半才炸。

## 限制（务必先读）

**1. 主动推送必须先有 `context_token`**
token 来自「对方最近一次发来的消息」。对方从来没发过消息 → 推不出去，dry-run 会直接告诉你「没有——对方需要先发一条消息」。
**token 过期后同样推不出去**，需要对方再发一条唤醒。`--dry-run` 返回 0 才代表能发。

**2. 必须有人工确认渲染结果**
平台的响应体只有 `{"message_id": N}`，**没有 errcode 字段**，HTTP 200 + message_id 只证明消息信封被接受。
item 字段填错时平台可能**照样回 message_id 但客户端渲染不出来**。所以自动化流程里 message_id 只能算"已投递"，
不能当"用户已看到"。判断是否真的显示，要本人看一眼微信。

**3. 视频依赖 ffmpeg**
缩略图靠 ffmpeg 抽帧（优先第 1 秒，抽不到退回第 0 帧）。没装 ffmpeg 会退化成「不带缩略图发送」，
对方看到的是空白气泡。视频时长/宽高靠 ffprobe。

**4. 体积上限未知**
协议里没有明确的上传大小限制，代码里也没做前置校验。发大文件可能失败或被平台截断，
真要发大文件先小样本试。

## 协议字段来源（改代码前必看）

媒体 item 的字段形状是照 **photon-hq/wechat-ilink-client** 的 TypeScript 定义实现的
（`src/api/types.ts`），不是猜的：

| item | 字段 |
|---|---|
| `image_item` | `aeskey`、`media`、`mid_size`、`thumb_media`、`thumb_size`、`thumb_width/height` |
| `video_item` | `media`、`video_size`、`play_length`、`video_md5`、`thumb_media`、`thumb_size`、`thumb_width/height` |
| `file_item` | `media`、`file_name`、`md5`、`len` |

三个**必须记住的坑**：

- **`file_item.len` 是字符串不是数字**。填 int 平台会把整条 item 静默丢掉，且照样回 message_id。
  这条有单测钉死（`test_file_item_len_is_a_string_not_a_number`）。
- **视频必须有缩略图**，否则对方是空白气泡。
- **`video_item` 没有顶层 `aeskey`**（`image_item` 才有）。视频解密只认 `media.aes_key`，
  而 `media.aes_key` 的构造是「hex 再 base64」两层，和 `image_item.aeskey`（纯 hex）不是一回事。
  上传走 `getUploadUrl` 拿 `upload_param` / `thumb_upload_param`，视频是**两次上传**（正片 + 缩略图）。

## 改完怎么验

```bash
cd skills/wechat-bot && python3 -m pytest tests/ -q     # 基线 47 passed
```

加新字段时**同时加一条钉住字段形状的单测**。绿测可能是因为错误的原因绿——
`json` / `shutil` / `subprocess` 这类模块级导入缺失时，43 个旧测试全绿而新代码一调用就 `NameError`。
想确认测试真的在测东西，就故意改坏实现看它变不变红。

改动协议字段后，必须**真发一次**到主人微信并核对渲染结果，不能只跑单测就算完。

## 排障

| 现象 | 查什么 |
|---|---|
| 推送退出 1 且提示"没有——对方需要先发一条消息" | `context_token` 缺失或过期，让对方先发一条 |
| dry-run 说找不到账号 | `--to` 的 id 和 `~/.config/fairy/wechat/` 里的 context key 对不上 |
| 微信收不到回复 | bot.py 长轮询是否在跑；白名单 `policy.py`；Fairy `/api/chat` 是否通 |
| 视频是空白气泡 | ffmpeg 没装，或视频在第 1 秒前是纯黑 |
| 消息发了但看不见内容 | 十有八九是 item 字段形状错了，见上面协议表 |
