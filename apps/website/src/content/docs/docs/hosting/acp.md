---
title: "Remote sessions with ACP"
description: "Connect a compatible client to a Bot, share live sessions with Astral, and attach remote MCP tools."
---

Use ACP when another application needs to start or resume a Bot's Astral session. The client and Astral can follow the same conversation as the Bot works.

## Connect a client

1. Use a client that supports the [ACP v1 session protocol](https://agentclientprotocol.com/protocol/v1/session-setup) over Astral's WebSocket transport and can send an HTTP `Authorization` header on the upgrade request. A client that only launches a local ACP command over `stdio` needs a compatible adapter.
2. In **Settings → Bots → your Bot → Basic settings → External access**, copy the **ACP WebSocket address**. Select **Create token**, choose **ACP**, and name the token for the client. Copy it before closing the dialog.
3. Connect to the copied `/acp` URL using `Authorization: Bearer <token>`. Use the `wss://` address of your [HTTPS deployment](/docs/hosting/server/#use-an-https-reverse-proxy) when connecting over a network. The reverse proxy must forward WebSocket upgrades and the `Authorization` header for `/acp`.
4. Open a new session or select an existing one in the client. For clients that ask for an ID, copy the part after `/sessions/` in the Astral session URL. Send a prompt and open the same session in Astral to see the interaction appear live.

ACP shares the Web UI's server port. All Bots on that server use the same `/acp` address, and the ACP token determines which Bot the client connects to. Use a separate token per app or device. If it is lost or exposed, revoke it in **External access** and create another. Revocation disconnects clients using that token immediately. Access is limited to ordinary unarchived, non-private sessions belonging to that Bot. Give tokens to clients you trust to change session models and archive these sessions. For HTTP access, create a separate [Bot API token](/docs/hosting/bot-api/).

If the connection fails, check that the client can reach the copied host and that your proxy forwards WebSocket upgrades and the authorization header. For a `401` or `403` during the upgrade, check that the client is using the current token and that the token still appears in this Bot's settings.

The `/acp` endpoint stays subject to the Server's listening address, reverse proxy, and hostname restriction. The remote management switch controls the Owner web interface and API separately. To restrict where ACP clients can connect from, use your firewall or reverse proxy rules.

## Share a session between Astral and the client

Client prompts and Bot responses appear in Astral's normal transcript. Once the client joins the session, it receives updates even when a message starts in Astral or a scheduled task or event starts the Bot. Updates include messages, readable thinking, tool activity, title changes, and context usage when the model reports it. Clients that declare support can also display completed compactions and the states of direct subagents. The client receives a response to its own prompt when that turn finishes. The same prompt is already available locally, so it is not echoed as a new user message to that connection.

Load a session to receive its full history. Use resume if the client already has the history or only needs future updates. Closing a session on the connection cancels its prompt from that connection and releases its subscription and attached tools. If the session is doing work started elsewhere, or a subagent is still active, wait for that work to finish and retry. The client can still join the session again. If loading fails, retry on the same connection after correcting the error. Other sessions keep streaming and retain their tools.

Deleting a session from the client archives it in Astral. It disappears from ACP discovery, its scheduled work pauses, and its history stays available in Astral's archive. Astral applies its usual archive rules, including restrictions on archiving a session bound to a platform chat.

## Send images

Select a Computer for the session and use a model that accepts images. An ACP prompt can include up to four inline PNG, JPEG, GIF, WebP, or BMP images. The WebSocket JSON message is limited to 1 MiB, including Base64 and prompt text, so the total original image bytes must be below 768 KiB. Each image can have at most 10 million pixels. For larger files, [upload them through the Bot API](/docs/hosting/bot-api/#upload-an-attachment) using a Bot API token and pass the returned URI as a `resource_link` in the ACP prompt. Astral sends frozen images inline when they fit its replay frame limit and can otherwise return file references for user attachments.

## Attach remote MCP tools

Include `mcpServers` when your client creates, loads, or resumes the session. Use `type: "http"` for [Streamable HTTP](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports) or `type: "sse"` for the older HTTP+SSE transport. The MCP URL must be reachable from the Astral Server. Supply any required request headers as `{ "name": "...", "value": "..." }` entries in `headers`. Local `stdio` tools cannot be attached.

Use an HTTP or HTTPS address as seen from Astral Server. For a service on the same host, use a loopback address such as `127.0.0.1`. For a service on a private network or Tailscale, use the address the Server can reach on that network. Give ACP tokens only to trusted clients, since they can ask Astral Server to connect to services it can reach.

One ACP connection at a time can attach tools to a session. Other clients can load it with an empty `mcpServers` list to follow its work. The attached tools are available to that session and the subagents it delegates to. If the connection drops, calls through it stop immediately. Reconnect and load the session with the same tool set to restore access. For a short period after disconnect, Astral keeps the tool declarations so a reconnect with the same tools can reuse the existing configuration. If a tool call alone fails, inspect the tool result or the remote service before retrying.

### Client and Computer API reference

Astral implements an ACP v1 JSON-RPC session subset over WebSocket. Send one JSON-RPC object per WebSocket text message. `initialize` reports `protocolVersion: 1`, supported session capabilities and MCP transports. `agentCapabilities._meta["dev.oines.astral/websocket"]` identifies the Astral transport version, currently `1`. `session/prompt` accepts text, resource links, and inline images. Send `session/cancel` as a notification to cancel the current turn.

Use `session/list` to discover eligible sessions by working directory or across the Bot. Follow `nextCursor` until it is absent. Sessions without a working directory can still be joined by ID. For `session/new`, use an absolute `cwd` on the Bot's authorized default Computer. With no default Computer, the session opens without a bound working directory. For `session/load` and `session/resume`, supply the session's working directory if it has one, or an absolute path if it is unbound. `session/close` ends the active attachment and `session/delete` archives the session.

`session/new`, `session/load` and `session/resume` return `configOptions` for the model and its supported thinking levels. Use `session/set_config_option` to change an option. The result contains the complete updated options, including the new model's thinking choices after a model change. Updates to the session's model or thinking level also arrive as `config_option_update` notifications.

The model sees `source="acp"` on ACP messages and `source="web"` on messages from the web interface. If the ACP token has a display name, the `client` attribute in the model's message envelope contains only that XML-escaped name. The `_meta.trigger_source` field in `session/update` follows the recorded message source. History replay uses the source metadata already recorded for each message. Astral sends its activity summary in a separate `_astral/activityStatus` notification.

Commands running on an authorized Computer can discover the session's attached tools with `GET /v1/mcp/tools` and invoke one with `POST /v1/mcp/tools/{name}/call`, passing the discovered name and an `arguments` object that follows its input schema. Follow the `mcp` capability from `local_api.capabilities_url` in the command environment for request schemas and authentication. The command uses its own Computer API credential.
