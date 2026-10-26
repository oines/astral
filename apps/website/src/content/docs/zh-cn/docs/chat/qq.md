---
title: "连接 QQ"
description: "选择官方 Bot 或 NapCat，配置账号并接入私聊或群聊。"
---

Astral 提供两种 QQ 接入方式。创建账号时先选对方式；它们使用不同凭据和聊天标识，不能混用。

| 方式 | 凭据 | 聊天标识 |
| --- | --- | --- |
| 官方 Bot | App ID、App Secret | `private:<user_openid>`、`group:<group_openid>` |
| NapCat | HTTP 地址、正向 WebSocket 地址、Access Token | `private:<QQ号>`、`group:<群号>` |

## QQ 官方 Bot

1. 在 [QQ 开放平台](https://q.qq.com/)创建 Bot，取得 App ID 和 App Secret。
2. 在平台配置可用的测试用户、测试群或发布范围，并授予需要的私聊、群消息和发送权限。
3. 在 Astral 选择 **连接平台 → QQ → QQ 官方 Bot**，填写凭据并连接。
4. 从获准用户或群给 Bot 发消息，让 Astral 发现聊天，再完成聊天授权与会话绑定。

默认 API 地址和 Token 地址通常不需更改。Astral 主动连接平台网关，不要求你提供公开 webhook。

官方 Bot 的 openid **不是 QQ 号或群号**，不能跨应用复用。当前支持私聊和获准的群，不接入 QQ 频道。普通群消息能否收到、何时可以回复、能发送哪些文件，都受账号实际权限和平台规则限制。

## QQ NapCat

先按 [NapCat 指南](https://napneko.github.io/)安装服务并登录目标 QQ 账号，然后配置：

1. 启用 HTTP 服务端。
2. 启用能够接收事件的**正向 WebSocket**服务端，不使用只处理 API 的 `/api` 端点。
3. 两端使用相同 Access Token，消息上报格式设为 `array`。
4. 如果需要读取远程 NapCat 主机上的附件，启用 `enableLocalFile2Url`，使文件接口能返回内容，而不只是本地路径。

在 Astral 选择 QQ NapCat，填写 Server 实际可达的两个地址与 Token。HTTP 和 WebSocket 必须对应同一个 QQ 账号。`127.0.0.1` 只有在 NapCat 与 Server 位于同一机器或适当网络环境时才适用。

## 完成接入并验证

在账号的聊天接入区域添加目标私聊或群，选择会话。群里明确提及 Bot，先验证文本回复，再测试文件发送。

NapCat 发送普通文本，Markdown 标记不会自动渲染。断线期间的消息没有自动历史重放；恢复连接不意味着已经补齐漏掉的消息。若发送结果不确定，先检查聊天实际状态，避免重复发送。
