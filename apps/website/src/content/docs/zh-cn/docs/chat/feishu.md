---
title: "连接飞书"
description: "使用企业自建应用 Bot 接收消息和发送任务结果。"
---

使用开启 Bot 能力的**企业自建应用**。群自定义 webhook Bot 只适合推送通知，不能替代这里需要的双向聊天连接。

## 配置飞书应用

在[飞书开放平台](https://open.feishu.cn/document/client-docs/bot-v3/bot-overview)创建应用，开启 Bot 能力并取得 App ID、App Secret。

事件订阅选择“使用长连接接收事件”，订阅 `im.message.receive_v1`。Astral 主动建立长连接，不需要公开 webhook。

按实际用途配置并发布应用权限：

| 用途 | 权限 |
| --- | --- |
| 接收私聊 | `im:message.p2p_msg:readonly` |
| 接收群内提及 | `im:message.group_at_msg:readonly` |
| 接收普通群消息 | `im:message.group_msg`，需平台允许 |
| 发送消息 | `im:message:send_as_bot` |
| 上传、读取附件 | `im:resource` |
| 读取聊天资料 | `im:chat:readonly` |

确认应用已发布、可用范围包含目标用户，并把 Bot 加入目标群。修改权限后也要完成平台要求的发布流程。

## 在 Astral 中连接

1. 打开 **设置 → Bot → 目标 Bot → 连接平台 → 飞书**。
2. 填写 App ID 和 App Secret，连接账号；通常保留默认服务器地址。
3. 在目标私聊或群里发送消息，让 Astral 发现聊天。
4. 在账号的“已接入聊天”中授权，绑定新会话或未绑定的现有会话。

飞书使用 `oc_…` 形式的 Chat ID。线程消息当前归入父聊天，不作为独立 Topic 授权。

## 回复与文件

私聊、群内提及和对 Bot 消息的回复可以直接触发任务。希望让 Bot 使用普通群消息时，还需获得对应权限。

支持文本、图片和文件等消息资源。文件与说明文字分别发送；文件发送失败时，文字成功不能替代附件成功。

## 常见连接问题

App ID 与 Secret 正确但没有消息时，检查应用是否发布、用户可见范围、Bot 是否在群中、长连接事件订阅及权限是否生效，再检查 Astral 的聊天授权。

缺少聊天资料权限可能只显示 Chat ID。连接成功也不保证所有消息、附件或管理操作都已被飞书授权。
