---
title: "通过 ACP 接入会话"
description: "将兼容客户端连接到 Bot，与 Astral 实时共享会话并挂载远程 MCP 工具。"
---

其他应用需要新建或继续某个 Bot 的 Astral 会话时，可以通过 ACP 接入。Bot 执行任务时，客户端和 Astral 可以同时查看这个会话。

## 连接客户端

1. 选用支持通过 Astral WebSocket 传输运行 [ACP v1 会话协议](https://agentclientprotocol.com/protocol/v1/session-setup) 的客户端，并确认它能在升级请求中发送 HTTP `Authorization` 请求头。只会通过 `stdio` 启动本地 ACP 命令的客户端需要兼容的适配器。
2. 打开**设置 → Bot → 目标 Bot → ACP 接入**，复制 WebSocket 地址，并为客户端创建一个有名称的令牌。关闭窗口前先复制令牌。
3. 使用 `Authorization: Bearer <token>` 连接刚才复制的 `/acp` 地址。跨网络连接时，使用 [HTTPS 部署](/zh-cn/docs/hosting/server/#通过-https-反向代理访问)的 `wss://` 地址。反向代理需要转发 `/acp` 的 WebSocket 升级请求和 `Authorization` 请求头。
4. 通过客户端新建会话，或从 Astral 会话地址中取出 `/sessions/` 后的 ID 交给客户端。发送一条消息后，在 Astral 中打开同一会话即可看到实时更新。

ACP 和 Web UI 共用服务器端口。同一台服务器上的所有 Bot 共用 `/acp` 地址，令牌决定客户端连接哪个 Bot。为不同应用或设备创建独立的令牌。令牌遗失或泄露时，在 **ACP 接入**中撤销并创建新的令牌。撤销会立刻断开使用该令牌的客户端。令牌只能访问该 Bot 的普通、未归档且非私密的会话，不能直接访问子代理或修改设置。

连接失败时，先确认客户端能访问复制的地址，以及反向代理会转发 WebSocket 升级和认证请求头。若升级返回 `401` 或 `403`，检查客户端正在使用的令牌是否仍在该 Bot 的令牌列表中。

`/acp` 仍受服务器监听地址、反向代理和主机名限制约束。远程管理开关单独控制 Owner 网页和 API。需要限制从哪些网络连接 ACP 时，在防火墙或反向代理中配置相应规则。

## 在 Astral 和客户端中使用同一会话

客户端发出的消息和 Bot 的回复会进入 Astral 的正常会话记录。客户端加载会话后，即使任务由 Astral 消息、计划任务或事件唤醒触发，也会收到消息、可读思考和工具执行的实时更新。客户端自己发送的消息由客户端显示，同一个连接会在该轮工作结束时收到结果。

某个会话加载失败时，可在同一连接上解决错误后重试。已经加载的其他会话会继续推送更新，并保留挂载的工具。

## 挂载远程 MCP 工具

客户端新建或加载会话时，在 `mcpServers` 中提供服务器。`type: "http"` 用于 [Streamable HTTP](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports)，`type: "sse"` 用于旧式 HTTP+SSE。MCP 地址必须能从 Astral Server 访问。远端要求请求头时，在 `headers` 中提供 `{ "name": "...", "value": "..." }`。本地 `stdio` 工具无法挂载。

按 Astral Server 所在的网络填写 HTTP 或 HTTPS 地址。同机服务可以填写 `127.0.0.1` 等回环地址，私网或 Tailscale 上的服务则填写 Server 在对应网络中可达的地址。只将 ACP 令牌交给可信客户端，因为客户端可以让 Astral Server 连接它能访问的服务。

同一会话同一时间只接受一个 ACP 连接挂载工具。其他连接可以传空的 `mcpServers` 列表来查看进度。挂载工具可由该会话及其委派的子代理调用。连接断开时，通过该连接进行的调用会立即停止。重新连接并带上同一组工具加载会话，即可恢复使用。断开后的一段时间内，Astral 会保留工具声明，带同一组工具重连可复用原有配置。若只有某一次工具调用失败，先查看工具结果或远端服务，再决定是否重试。

### 客户端与 Computer API 参考

Astral 在 WebSocket 上实现 ACP v1 JSON-RPC 会话协议子集，包含 `initialize`、`session/new`、`session/load` 和 `session/prompt` 请求，以及 `session/cancel` 通知。每条 WebSocket 文本消息发送一个 JSON-RPC 对象。`initialize` 返回 `protocolVersion: 1`，并声明历史加载及所支持的 MCP 传输。使用 `agentCapabilities._meta["dev.oines.astral/websocket"]` 检查 Astral WebSocket 传输版本，当前值为 `1`。输入消息支持文本和资源链接。`session/new` 的 `cwd` 填写 Bot 获准使用的默认电脑上的绝对路径。未配置默认电脑时，新会话不绑定工作目录。`session/load` 要求请求包含绝对路径 `cwd`，加载时沿用已有会话的工作目录。

模型收到的 ACP 消息标记为 `source="acp"`，网页消息标记为 `source="web"`。ACP 令牌有显示名时，模型收到的消息封装里的 `client` 属性仅包含经 XML 转义的显示名。`session/update` 中的 `_meta.trigger_source` 取自消息记录的来源。回放沿用每条历史消息原有的来源元数据。运行摘要通过独立的 `_astral/activityStatus` 通知发送。

获准电脑上的命令可以用 `GET /v1/mcp/tools` 查找当前会话挂载的工具，再通过 `POST /v1/mcp/tools/{name}/call` 调用。使用查询到的工具名称，并按它的输入结构填写 `arguments` 对象。请求结构和认证方式可从命令环境的 `local_api.capabilities_url` 进入 `mcp` 能力页查看。命令使用自己的 Computer API 凭据。
