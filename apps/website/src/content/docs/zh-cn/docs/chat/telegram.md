---
title: "连接 Telegram"
description: "使用 Bot Token 接入私聊、群聊和论坛 Topic。"
---

Telegram 通过 Bot Token 连接。准备一个由你管理的 Telegram Bot，并确认 Astral Server 能访问 Telegram API。

## 连接账号

1. 在 Telegram 的 BotFather 创建 Bot，取得 Bot Token。
2. 在 Astral 中打开 **设置 → Bot → 目标 Bot → 连接平台 → Telegram**。
3. 填写账号名称和 Bot Token，连接账号。通常保留默认服务器地址。
4. 到私聊发送消息，或把 Bot 加入目标群。

Astral 使用长轮询接收消息。如果同一个 Bot 已配置活动 webhook，连接验证会报错；先停止原有接收程序并清理 webhook，再由 Astral 接管。

## 找到 Chat ID

在 Bot 能收到并回复的聊天中发送：

```text
/chat_id
```

群里可使用 `/chat_id@你的Bot用户名`。Astral 会回复 Chat ID；在论坛 Topic 中还会给出 Topic ID。这个命令不需要模型或电脑，也不会自动授权聊天。

回到账号的“已接入聊天”，填写对应标识，选择会话。如果要区分论坛话题，Chat ID 使用父群标识，Topic ID 单独填写。

## 让 Bot 参与群聊

先完成 Astral 的聊天授权，再 `@Bot` 或回复它的消息。希望使用普通群消息作为上下文时，需要按 Telegram 的规则调整 BotFather 隐私模式或管理员权限。[Telegram 隐私模式说明](https://core.telegram.org/bots/features#privacy-mode)

普通群回复留在原群会话，包括回复其他成员时同时 `@Bot` 的消息。论坛 Topic 分别授权并绑定会话。

## 回复与选中引用

Bot 会看到原消息预览，以及 Telegram 提供的选中引用片段。需要它处理引用内容时，在新正文中提及 Bot，或回复它发出的消息。

引用中的图片、文件和贴纸保留原消息身份与下载入口，与新消息自身的附件分开，不会因为被引用而自动下载或生成图片预览。Agent 需要时可主动下载原件。即使 Astral 没有单独收到原消息，只要 Telegram 在同聊天回复中带来了原媒体信息，也能使用该入口。外部引用不会授予另一个聊天或文件的访问权限。

## 转发消息

转发消息会保留 Telegram 提供的原作者或来源聊天、原发送时间，以及本次转发者。原作者身份被隐藏时，只能看到平台提供的名字。

Bot 只能访问已授权的聊天。转发中的来源信息用于理解内容，唤醒行为按本次转发消息判断。

## 消息表情回应

接收群聊表情回应需要 Bot 是群管理员。Bot 可以看到平台交付的新增、移除和匿名数量变化；匿名汇总可能延迟，也不包含操作者身份。私聊回应的接收取决于平台。

Bot 可以通过内置 API 为获准聊天中的消息设置回应。已知其他成员的群回应遵循主动参与设置；自身动作、匿名回应和私聊回应不会单独唤醒 Bot，工作中的 Bot 仍可读取收到的事件。

## 文件与排查

Bot 可以按顺序发送文字、原生提及、图片和文件。Markdown 和 HTML 使用 Telegram Rich Message，可在文字段落之间嵌入媒体；没有用户名时也可以通过用户数字 ID 提及。文件从电脑流式上传到 Telegram，无需图床；文字中的空格、换行保留。具体 `parts` 参数和账号权限通过 Gateway 能力文档发现。

支持文本、图片和文件。发送图片时可按任务要求发送原图文档；实际大小和类型限制由 Telegram 决定。

连接正常但不回复时，检查聊天授权、Topic ID、消息接收范围，以及模型与电脑是否可用。向频道发送消息还需要对应的发言权限。

## 平台查询与管理

Bot 可以通过内置 API 查询成员和资料、管理群聊、话题、邀请和置顶等。具体方法与权限见[平台 API](/zh-cn/docs/chat/platform-api/)。