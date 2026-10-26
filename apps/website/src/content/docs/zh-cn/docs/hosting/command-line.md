---
title: "命令行访问"
description: "登录已有 Server，查看资源并操作会话。"
---

`astral` 命令行可以连接已有 Server，查看资源、发送任务或导出记录。它使用网页登录账户，不需要复制 Server 私钥，也不使用旧版 Owner Token。

## 登录 Server

```sh
astral connect --server https://astral.example.com --username admin
astral doctor
```

替换示例地址和用户名。终端会提示隐藏输入密码；启用两步验证时，再输入验证器动态码或恢复码。CLI 不执行浏览器的 Passkey 交互。

登录凭据保存在当前用户的 Astral 配置目录中。该次 CLI 登录会显示在“已登录设备”里，可以单独撤销。

## 查看 Bot 和电脑

```sh
astral resources agents
astral resources computers
astral resources sessions
```

命令返回结构化结果。界面称 Bot 的对象在 CLI 集合名中仍叫 `agents`，使用返回的实际 ID 执行下一步。

## 新建并发送任务

下方 `BOT_ID` 和 `SESSION_ID` 是需要替换的占位符：

```sh
astral sessions new --agent BOT_ID --title "检查项目"
astral sessions send SESSION_ID --text "列出当前目录中的文件"
astral sessions history SESSION_ID
```

向正在运行的会话补充要求时，可使用 `--mode steer`。普通 `follow_up` 消息按排队方式处理。

```sh
astral sessions stop SESSION_ID
astral sessions continue SESSION_ID
astral sessions export SESSION_ID --output session.jsonl
```

导出会话记录不会打包电脑上的工作文件。停止不会回滚已经完成的操作。

## 调用管理接口

可用 `astral request GET /api/v1/bootstrap` 调用同一个已登录 Server。需要请求体时使用 `--body-file` 指定 JSON 文件，避免复杂内容被 shell 解释。

具体命令选项以当前程序的 `astral help` 为准。命令行登录与“连接电脑”是不同操作；接入执行机器请使用界面生成的[电脑接入命令](/zh-cn/docs/computers/connect/)。
