---
title: "连接 Telegram"
description: "创建 Telegram 机器人，接入一个私聊或群聊，并收到第一条回复。"
---

你需要一个由自己管理的 **Telegram 机器人**。它是你在 Telegram 中聊天的对象；Astral 中的 Bot 为它提供模型、电脑和工作上下文。下面先接通一个聊天，再说明群聊和文件的用法。

开始前，先完成 Astral 的[首次配置](/zh-cn/docs/start/first-steps/)。运行 Astral 的电脑或服务器必须能访问 Telegram API；只在手机上能打开 Telegram 还不够。

## 1. 创建机器人并复制 Token

1. 在 Telegram 打开官方 [@BotFather](https://t.me/BotFather)，发送 `/newbot`。
2. 按提示设置名称和用户名。记下用户名，后面会用它找到机器人。
3. 复制 BotFather 返回的 **Bot Token**。它是允许 Astral 使用这个机器人的凭据，请只填入连接表单，不要发到群里。

已有机器人可以继续使用原来的 Token。创建步骤见 [Telegram 官方说明](https://core.telegram.org/bots/features#creating-a-new-bot)。如果它已接入其他程序，先停止原程序接收消息；已配置 webhook 时，还要在原服务中移除它。Astral 使用长轮询接收消息，活动 webhook 会阻止连接。

## 2. 连接账号，取得聊天标识

在 **设置 → Bot → 目标 Bot → 连接平台 → Telegram** 中使用 Token 连接，通常保留默认服务器地址。然后到 Telegram 打开机器人的私聊，或把它加入目标群。

## 找到 Chat ID

Chat ID 用来告诉 Astral 你要接入哪一个聊天，不需要去 Telegram 后台查找。

在刚才的私聊中发送：

```text
/chat_id
```

群里发送 `/chat_id@你的Bot用户名`，把后半段换成 BotFather 中的用户名。例如用户名是 `my_astral_bot`，就发送 `/chat_id@my_astral_bot`。

Astral 会在原聊天返回 **Chat ID**。在论坛话题中，还会返回 **Topic ID**。这个命令可以在聊天尚未添加时使用，不调用模型，也不使用电脑。

## 3. 添加聊天并验证回复

在 Astral 账号的 **已接入聊天** 中选择发现的聊天，或使用返回的 Chat ID 查找，再添加到新会话或同一 Bot 的未绑定会话。论坛话题要同时填入父群 Chat ID 和该话题的 Topic ID。新账号默认只允许添加的聊天，`/chat_id` 本身不会授权。

完成后，在 Telegram **重新发送**“请在这个 Telegram 聊天里回复：连接成功”。群里请回复刚才返回 Chat ID 的那条 Bot 消息，再发送这句话，这样默认隐私模式下也能收到。

检查 Astral 对应会话是否开始处理，以及 Telegram 是否收到实际回复。

## 让 Bot 参与群聊

默认可以通过回复 Bot 的消息交代任务。要让 Bot 读取普通群讨论、接收更多提及，需调整 Telegram 的消息接收范围：在 BotFather 中关闭这个机器人的群隐私模式，然后将它重新加入群。Bot 担任群管理员时也能接收群消息，但请只授予实际需要的管理权限。[Telegram 隐私模式说明](https://core.telegram.org/bots/features#privacy-mode)

Astral 的群聊主动参与默认关闭。能够收到普通消息后，是否主动加入讨论仍在 Astral 的该群回复方式中设置，见[群聊协作](/zh-cn/docs/chat/groups/)。

论坛话题可以分别添加、授权并使用不同 Astral 会话。普通群中回复某位成员的消息仍留在原群会话，不会因此另开话题。

## 图片、文件、引用和群管理

可以直接发图片、文件或转发内容，让 Bot 读取并把结果发回来。回复和选中引用会保留平台提供的原消息预览、片段与媒体入口；需要它处理时，在新消息里提及 Bot 或回复 Bot 本身，引用中的名字不算提及。来源信息不会授予其他聊天的访问权限。

Bot 支持文字、原生提及、图片、文件和 Markdown／HTML 富文本。可以要求把图片作为原始文档发送；文件从电脑直接上传，无需图床，大小和格式受 Telegram 限制。

查询成员、管理话题、置顶或设置表情可以直接交给 Bot；接收群表情回应需机器人为管理员。普通回应不保证触发回复。自定义调用见[平台 API](/zh-cn/docs/chat/platform-api/)。

## 没有收到预期结果时

| 你看到的情况 | 下一步 |
| --- | --- |
| 无法连接账号 | 核对 Token、运行 Astral 的机器能否访问 Telegram，以及原接收程序和 webhook 是否已停用 |
| `/chat_id` 没有回复 | 确认账号启用、机器人已在聊天中且能发送消息；群里使用带用户名的完整命令 |
| Chat ID 能返回，但任务没有处理 | 在 Astral 添加聊天并选择会话，然后重新发送任务；论坛话题核对 Topic ID |
| 私聊正常，群里没响应 | 先回复 Bot 的现有消息测试，再检查 Telegram 隐私模式和 Astral 的群授权 |
| Astral 中已有处理结果，Telegram 没有消息 | 明确要求“发回这个聊天”，检查会话中的平台发送错误；频道还需授予发言权限 |

发送超时或结果不明时，先到 Telegram 检查实际消息，再决定是否重发。
