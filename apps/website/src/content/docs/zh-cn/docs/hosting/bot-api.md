---
title: "通过 Bot API 接入"
description: "通过 HTTP 使用 Bot、上传附件、读取会话历史和管理自动化。"
---

脚本或应用需要通过 HTTP 操作一个 Bot 时，可以使用 Bot API。编辑器或其他 ACP 客户端要加入 Astral 实时会话时，使用 [ACP](/zh-cn/docs/hosting/acp/)。

## 创建接入令牌

1. 打开**设置 → Bot → 目标 Bot → 基本设置 → 外部接入**，复制 **Bot API 地址**。
2. 点击**创建令牌**，用途选择 **Bot API**，并用应用或脚本的名称命名。关闭窗口前先复制令牌，令牌只显示这一次。
3. 请求时发送 `Authorization: Bearer <token>`。API 与 Astral 共用主机和端口，地址以 `/api/v1/bot` 结尾。通过网络连接时使用 HTTPS 地址，并让反向代理转发认证请求头。

为每个应用单独创建令牌，便于独立撤销。Bot API 令牌和 ACP 令牌分别用于各自的接入地址。撤销 Bot API 令牌后，使用该令牌的请求将被拒绝，已打开的事件流也会断开。

将复制的地址设为 `BOT_API`，把令牌作为密钥 `ASTRAL_BOT_TOKEN` 提供给应用。先用以下请求确认接入的是哪个 Bot，响应包含 Bot 的名称和 ID。

```sh
curl --fail-with-body "$BOT_API/self" \
  -H "Authorization: Bearer $ASTRAL_BOT_TOKEN"
```

返回 `401` 时，确认使用的是 Bot API 令牌，且它仍在**外部接入**的令牌列表里。找不到会话时，核对它是否属于 `GET /self` 返回的 Bot，以及它是否为私密会话。

## 新建或跟进会话

- 使用 `GET /sessions` 列出 Bot 的普通会话。新建会话时，向 `POST /sessions` 发送 JSON，例如 `{"title":"检查部署清单"}`。令牌已确定目标 Bot。后续请求使用响应中的会话 `id`。
- 发送任务使用 `POST /sessions/{id}/inputs`，请求体例如 `{"id":"deployment-check-1","text":"检查部署清单。"}`。为每条输入指定稳定的 ID。请求中断时，先用 `GET /sessions/{id}/inputs/{input}` 查询，再决定是否用同一 ID 重试。
- 使用 `GET /sessions/{id}/timeline?limit=30` 读取适合界面展示的记录。需要结构化会话记录时使用 `GET /sessions/{id}/history?limit=30`。响应有 `next_cursor` 时，把它原样作为 `cursor` 传给相同会话和端点。旧游标被拒绝时，重新读取第一页。
- 用能发送 Bearer 请求头的 HTTP 客户端连接 `GET /sessions/{id}/events`，接收服务器发送事件（SSE）。连接会先返回快照，再发送实时更新。收到 `resync` 或连接中断时，重新连接取得新快照。
- 使用 `GET /sessions/{id}/subagents` 查找子代理会话，再连接 `GET /sessions/{child_id}/events` 查看某个子代理的进度。子代理会话在此 API 中只读，任务和控制请求发给父会话。

使用 `GET /models` 查看可用模型和支持的推理选项。用 `POST /sessions/{id}/model` 选择模型，传入 `profile_id`，如需指定推理档位，再传入该模型支持的 `reasoning`。使用 `GET /computers` 查看 Bot 已获准使用的电脑，再用 `POST /sessions/{id}/computer` 为会话切换电脑。

归档或恢复普通会话时，向 `PATCH /sessions/{id}` 发送 `{"archived":true}` 或 `{"archived":false}`。归档会暂停会话工作并保留历史。恢复后需要手动重新启动工作或启用自动化。归档条件与网页中一致。

## 上传附件

为会话选好电脑，将会话 ID 设为 `SESSION_ID`，并把文件作为二进制请求体发送。`name` 参数填写文件名，需要时进行 URL 编码。也可以传入 `computer_id` 选择 Bot 获准使用的电脑。

```sh
curl --fail-with-body "$BOT_API/sessions/$SESSION_ID/attachments?name=diagram.png" \
  -H "Authorization: Bearer $ASTRAL_BOT_TOKEN" \
  -H "Content-Type: application/octet-stream" \
  --data-binary @diagram.png
```

把响应中的 `uri` 放进 `POST /sessions/{id}/inputs` 的 `files` 数组。ACP 客户端连接到同一个 Bot 和会话时，也可以将该 URI 作为 `resource_link` 发送。图片需要会话使用支持图片的模型。

## 管理自动化

- 用 `GET /schedules` 和 `POST /schedules` 查询或新建计划任务。有任务 ID 时，可以通过 `GET`、`PATCH`、`DELETE /schedules/{id}` 查看、修改和删除。修改或删除时使用当前的 `revision`。
- 用 `GET /wake-endpoints` 查找唤醒端点。已创建的端点可以通过 `GET`、`PATCH`、`DELETE /wake-endpoints/{id}` 查看、修改和删除。新唤醒端点由产生事件的获准电脑创建。

令牌仅在所属 Bot 的范围内生效。电脑授权、平台聊天绑定、私密根会话和会话永久删除由 Owner 在 Astral 中管理。

在 Astral 主机上用同一个 Bearer 令牌请求 `GET /api/v1/bot/openapi.json`，可查看可用方法、请求体和响应结构。
