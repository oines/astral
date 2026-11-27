---
title: "通过 ACP 接入会话"
description: "将兼容客户端连接到 Bot，与 Astral 实时共享会话并挂载远程 MCP 工具。"
---

其他应用需要新建或继续某个 Bot 的 Astral 会话时，可以通过 ACP 接入。Bot 执行任务时，客户端和 Astral 可以同时查看这个会话。

## 连接客户端

1. 选用支持通过 Astral WebSocket 传输运行 [ACP v1 会话协议](https://agentclientprotocol.com/protocol/v1/session-setup) 的客户端，并确认它能在升级请求中发送 HTTP `Authorization` 请求头。只会通过 `stdio` 启动本地 ACP 命令的客户端需要兼容的适配器。
2. 打开**设置 → Bot → 目标 Bot → 基本设置 → 外部接入**，复制 **ACP WebSocket 地址**。点击**创建令牌**，用途选择 **ACP**，并用客户端的名称命名。关闭窗口前先复制令牌。
3. 使用 `Authorization: Bearer <token>` 连接刚才复制的 `/acp` 地址。跨网络连接时，使用 [HTTPS 部署](/zh-cn/docs/hosting/server/#通过-https-反向代理访问)的 `wss://` 地址。反向代理需要转发 `/acp` 的 WebSocket 升级请求和 `Authorization` 请求头。
4. 在客户端新建或选择已有会话。如果客户端要求填写 ID，从 Astral 会话地址中复制 `/sessions/` 后的部分。发送一条消息后，在 Astral 中打开同一会话即可看到实时更新。

ACP 和 Web UI 共用服务器端口。同一台服务器上的所有 Bot 共用 `/acp` 地址，ACP 令牌决定客户端连接哪个 Bot。为不同应用或设备创建独立的令牌。令牌遗失或泄露时，在**外部接入**中撤销并创建新的令牌。撤销会立刻断开使用该令牌的客户端。令牌只允许访问该 Bot 的普通、未归档且非私密的会话。请将令牌交给你信任的客户端，允许它更改这些会话的模型或归档会话。需要通过 HTTP 接入时，单独创建 [Bot API 令牌](/zh-cn/docs/hosting/bot-api/)。

连接失败时，先确认客户端能访问复制的地址，以及反向代理会转发 WebSocket 升级和认证请求头。若升级返回 `401` 或 `403`，检查客户端正在使用的令牌是否仍在该 Bot 的令牌列表中。

`/acp` 仍受服务器监听地址、反向代理和主机名限制约束。远程管理开关单独控制 Owner 网页和 API。需要限制从哪些网络连接 ACP 时，在防火墙或反向代理中配置相应规则。

## 在 Astral 和客户端中使用同一会话

客户端发出的消息和 Bot 的回复会进入 Astral 的正常会话记录。客户端加入会话后，即使任务由 Astral 消息、计划任务或事件唤醒触发，也能实时收到消息、可读思考、工具执行和标题变化。模型报告上下文用量时也会推送。声明相应能力的客户端还可显示压缩完成事件和直属子代理的状态。客户端自己发送的消息由客户端显示，同一连接会在该轮工作结束时收到结果。

要查看完整历史，使用加载会话。如果客户端已有历史或只关心接下来的进度，使用直接续聊。在连接上关闭会话时，会取消该连接为这个会话发起的运行，并释放订阅和挂载的工具。如果会话还有从其他入口发起的工作，或有子代理仍在运行，请等工作结束后重试。之后仍可重新加入。加载失败时可在同一连接上解决错误后重试，其他已加入的会话会继续推送更新并保留工具。

在客户端删除会话会将它归档。它会从 ACP 会话列表中移出，计划任务会暂停，历史可以在 Astral 的归档中查看。Astral 仍会检查归档条件，例如已绑定平台聊天的会话需先更换绑定。

## 发送图片

为会话选择一台电脑，并使用支持图片的模型。一条 ACP 消息最多附带四张 PNG、JPEG、GIF、WebP 或 BMP 图片。WebSocket JSON 消息上限为 1 MiB，包含 Base64 编码和正文，因此原图合计需要小于 768 KiB。每张图片最多 1000 万像素。更大的文件可以先用 Bot API 令牌[上传附件](/zh-cn/docs/hosting/bot-api/#上传附件)，再把返回的 URI 作为 `resource_link` 放入 ACP 消息。回放时，符合帧大小限制的冻结图片会随消息发送，过大的用户附件可通过文件引用表示。

## 挂载远程 MCP 工具

客户端新建、加载或直接续聊时，在 `mcpServers` 中提供服务器。`type: "http"` 用于 [Streamable HTTP](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports)，`type: "sse"` 用于旧式 HTTP+SSE。MCP 地址必须能从 Astral Server 访问。远端要求请求头时，在 `headers` 中提供 `{ "name": "...", "value": "..." }`。本地 `stdio` 工具无法挂载。

按 Astral Server 所在的网络填写 HTTP 或 HTTPS 地址。同机服务可以填写 `127.0.0.1` 等回环地址，私网或 Tailscale 上的服务则填写 Server 在对应网络中可达的地址。只将 ACP 令牌交给可信客户端，因为客户端可以让 Astral Server 连接它能访问的服务。

同一会话同一时间只接受一个 ACP 连接挂载工具。其他连接可以传空的 `mcpServers` 列表来查看进度。挂载工具可由该会话及其委派的子代理调用。连接断开时，通过该连接进行的调用会立即停止。重新连接并带上同一组工具加载会话，即可恢复使用。断开后的一段时间内，Astral 会保留工具声明，带同一组工具重连可复用原有配置。若只有某一次工具调用失败，先查看工具结果或远端服务，再决定是否重试。

### 客户端与 Computer API 参考

Astral 在 WebSocket 上实现 ACP v1 JSON-RPC 会话协议子集。每条 WebSocket 文本消息发送一个 JSON-RPC 对象。`initialize` 返回 `protocolVersion: 1`，并声明支持的会话能力及 MCP 传输。使用 `agentCapabilities._meta["dev.oines.astral/websocket"]` 查看 Astral 传输版本，当前值为 `1`。`session/prompt` 支持文本、资源链接和内联图片。取消当前轮时发送 `session/cancel` 通知。

使用 `session/list` 列出 Bot 可访问的会话，可以按工作目录过滤。有 `nextCursor` 时可继续请求下一页。没有绑定工作目录的会话可以通过 ID 加入。`session/new` 的 `cwd` 填写 Bot 获准使用的默认电脑上的绝对路径。未配置默认电脑时，新会话不绑定工作目录。`session/load` 和 `session/resume` 传入会话已有的工作目录，会话未绑定目录时可传绝对路径。`session/close` 结束当前连接上的会话，`session/delete` 归档会话。

`session/new`、`session/load` 和 `session/resume` 通过 `configOptions` 返回模型和可选择的推理档位。用 `session/set_config_option` 修改选项。响应会返回最新的完整选项，切换模型后，推理档位会对应新模型。会话的模型或推理档位变化时，也会推送 `config_option_update`。

模型收到的 ACP 消息标记为 `source="acp"`，网页消息标记为 `source="web"`。ACP 令牌有显示名时，模型收到的消息封装里的 `client` 属性仅包含经 XML 转义的显示名。`session/update` 中的 `_meta.trigger_source` 取自消息记录的来源。回放沿用每条历史消息原有的来源元数据。运行摘要通过独立的 `_astral/activityStatus` 通知发送。

获准电脑上的命令可以用 `GET /v1/mcp/tools` 查找当前会话挂载的工具，再通过 `POST /v1/mcp/tools/{name}/call` 调用。使用查询到的工具名称，并按它的输入结构填写 `arguments` 对象。请求结构和认证方式可从命令环境的 `local_api.capabilities_url` 进入 `mcp` 能力页查看。命令使用自己的 Computer API 凭据。
