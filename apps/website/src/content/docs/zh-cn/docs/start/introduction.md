---
title: "认识 Astral"
description: "在一个会话里，让 Bot 使用你的电脑完成任务。"
---

Astral 是运行在你自己设备上的个人 Agent。你把任务交给 Bot，它可以在获得授权的电脑上读取文件、执行命令，并把结果留在会话中，或发送回连接的聊天平台。

一个 Session 可以跨多台电脑工作。例如，在笔记本上读项目，在 Linux 服务器上运行测试，再到 Windows 电脑构建程序。任务上下文保留在同一个会话里，每次执行仍发生在具体的电脑上。

## 先完成第一个任务

1. [获取并运行 Astral](/zh-cn/docs/start/get-astral/)。
2. [连接模型，选择电脑](/zh-cn/docs/start/first-steps/)。
3. 发出一个可以检查结果的小任务，例如“列出当前目录的文件，并说明这个项目如何启动”。

聊天平台是可选的。先在 Astral 中确认模型和电脑能正常工作，再[接入 Telegram、QQ、微信或飞书](/zh-cn/docs/chat/overview/)。

## 三个需要区分的对象

| 对象 | 用来做什么 |
| --- | --- |
| Bot | 保存模型、人格、电脑权限和聊天平台账号等设置。 |
| 会话（Session） | 保存一项持续工作的消息和上下文。一个 Bot 可以有多个会话。 |
| 电脑（Computer） | 提供文件和命令执行环境，可以是服务所在机器、远程机器或容器。 |

连接一台电脑后，还要向具体 Bot 授权。会话切换电脑会保留上下文，但不会自动同步文件或搬走已经启动的进程。

## 根据你的需要继续

- 在多台设备上工作：[连接电脑](/zh-cn/docs/computers/connect/)与[跨电脑工作](/zh-cn/docs/computers/across-computers/)。
- 在团队聊天里交代任务：[群聊协作](/zh-cn/docs/chat/groups/)。
- 让 Astral 持续在线：[选择运行方式](/zh-cn/docs/hosting/architecture/)。
