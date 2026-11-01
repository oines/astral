---
title: "从命令行使用 Astral"
description: "连接已有 Astral，查询 Bot、发送任务和导出会话。"
---

如果你希望在终端或自己的脚本中操作 Astral，可以使用 `astral` 命令。下面先完成一次“登录已有 Server → 新建会话 → 发送任务”的流程。

## 准备命令和账户

官方 Server 或电脑接入安装完成后，同一终端中应可以运行：

```sh
astral version
```

准备 Astral 的网页访问地址、管理员用户名和密码。这个账户应已经能在浏览器中登录。命令行登录用于操作 Astral；如果只是想让 Astral 使用这台电脑，改用[电脑接入流程](/zh-cn/docs/computers/connect/)。

## 登录已有 Server

把地址和用户名替换成自己的值：

```sh
astral connect --server https://astral.example.com --username admin
```

按提示输入密码。开启两步验证时，使用验证器动态码或恢复码；命令行无法完成浏览器的 Passkey 验证。

登录后检查连接：

```sh
astral doctor
```

登录信息保存在当前系统账号的 Astral 配置目录中。这次登录会出现在网页的“已登录设备”中，需要时可以单独退出它。

## 选择 Bot 并发送任务

**先取得 Bot 的 ID：**

```sh
astral resources agents
```

返回结果中找到目标 Bot 的名称和 `id`。命令行集合名使用 `agents`，对应界面中的 Bot。先确保这个 Bot 已配置模型和默认电脑。

**创建会话：** 把 `BOT_ID` 换成上一步的实际 ID。

```sh
astral sessions new --agent BOT_ID --title "检查工作目录"
```

返回结果包含新会话的 ID。把下面的 `SESSION_ID` 换成该 ID，再发送任务：

```sh
astral sessions send SESSION_ID --text "列出当前工作目录中的文件，不要修改它们"
```

任务开始后，查看实际执行和回复：

```sh
astral sessions history SESSION_ID
```

任务也会出现在 Astral 界面中。需要继续同一项工作时，向同一个会话 ID 发送后续要求。

## 常用命令

| 目的 | 命令 |
| --- | --- |
| 列出已连接电脑 | `astral resources computers` |
| 列出会话 | `astral resources sessions` |
| 在下次可处理新消息时补充当前任务 | `astral sessions send SESSION_ID --mode steer --text "新的要求"` |
| 停止任务 | `astral sessions stop SESSION_ID` |
| 继续会话 | `astral sessions continue SESSION_ID` |
| 导出记录到新文件 | `astral sessions export SESSION_ID --output session.jsonl` |

普通发送会等待当前工作结束后再处理；`steer` 让 Bot 在下一次可接收补充信息时使用新要求。停止任务不会撤销已经完成的文件操作。导出包含会话记录，工作文件需要另外保存。

## 在服务器终端恢复访问

以下命令直接在 Server 所在机器运行，不要求事先完成 `astral connect`：

| 目的 | 命令 |
| --- | --- |
| 检查是否已设置网页登录 | `astral auth status` |
| 第一次设置管理员 | `astral auth setup --username admin` |
| 忘记密码或丢失验证方式后重置 | `astral auth reset --username admin` |
| 查看主机名限制 | `astral config hostnames show` |
| 关闭主机名限制以恢复网页访问 | `astral config hostnames disable` |

账户重置会退出已有网页登录并移除两步验证，使用前阅读[恢复说明](/zh-cn/docs/hosting/security/#无法登录时恢复)。命令会寻找当前账号的安装；有多个目标时按提示选择，手动部署可用 `--data` 明确原 Server 数据目录。

后台 Server 的启停和日志使用 [systemctl 与 journalctl](/zh-cn/docs/hosting/server/#管理后台服务)。远程电脑的状态和日志则在那台电脑上使用 `astral computer service status`、`astral computer service logs`，需要重启时使用 `astral computer service restart`。

## 调用管理 API

已有登录的脚本可以使用 `astral request`，例如读取当前 Server 的初始化数据：

```sh
astral request GET /api/v1/bootstrap
```

需要 JSON 请求体时，用 `--body-file /path/to/request.json` 指定文件。接口路径、参数和返回结构以当前 Server 的 OpenAPI 为准；命令本身的选项可运行 `astral help` 查看。
