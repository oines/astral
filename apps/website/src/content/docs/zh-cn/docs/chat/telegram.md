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

普通群回复会留在原群的会话，包括回复其他成员时同时 `@Bot` 的消息。真正的 Topic 仍然分别授权、绑定会话；单有回复的 thread ID 不会创建新 Topic。这个规则用于新收到的消息，不会移动或重放已有归档。

## 回复与选中引用

Bot 会看到原消息预览；Telegram 提供选中引用时，还能看到你选中的准确片段。引用原文中的 `@Bot` 不等于本次提及 Bot；需要让它处理时，在新正文中提及它，或回复它发出的消息。

引用中的图片、文件和贴纸保留原消息身份与下载入口，与新消息自身的附件分开，不会因为被引用而自动下载或生成图片预览。Agent 需要时可主动下载原件。即使 Astral 没有单独收到原消息，只要 Telegram 在同聊天回复中带来了原媒体信息，也能使用该入口。外部引用不会授予另一个聊天或文件的访问权限。

## 文件与排查

支持文本、图片和文件。发送图片时可按任务要求发送原图文档；实际大小和类型限制由 Telegram 决定。

连接正常但不回复时，检查聊天是否获准、Topic ID 是否正确、Bot 是否能收到该消息、模型与电脑是否可用。频道还需要相应发言权限；不要仅凭连接成功判断可以发消息。
