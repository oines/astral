---
title: "Remote sessions with ACP"
description: "Connect a compatible client to a Bot, share live sessions with Astral, and attach remote MCP tools."
---

Use ACP when another application needs to start or resume a Bot's Astral session. The client and Astral can follow the same conversation as the Bot works.

## Connect a client

1. Use a client that supports the [ACP v1 session protocol](https://agentclientprotocol.com/protocol/v1/session-setup) over Astral's WebSocket transport and can send an HTTP `Authorization` header on the upgrade request. A client that only launches a local ACP command over `stdio` needs a compatible adapter.
2. In **Settings → Bots → your Bot → ACP access**, copy the WebSocket address and create a named access token. Copy the token before closing the dialog.
3. Connect to the copied `/acp` URL using `Authorization: Bearer <token>`. Use the `wss://` address of your [HTTPS deployment](/docs/hosting/server/#use-an-https-reverse-proxy) when connecting over a network. The reverse proxy must forward WebSocket upgrades and the `Authorization` header for `/acp`.
4. Open a new session, or take the ID after `/sessions/` from an Astral session's URL and supply it to your client. Send a prompt and open the same session in Astral to see the interaction appear live.

ACP shares the Web UI's server port. All Bots on that server use the same `/acp` address, and the access token determines which Bot the client connects to. Use a separate token per app or device. If it is lost or exposed, revoke it in **ACP access** and create another. Revocation disconnects clients using that token immediately. Access is limited to ordinary unarchived, non-private sessions belonging to that Bot. Subagents and settings are outside the token's scope.

If the connection fails, check that the client can reach the copied host and that your proxy forwards WebSocket upgrades and the authorization header. For a `401` or `403` during the upgrade, check that the client is using the current token and that the token still appears in this Bot's settings.

The `/acp` endpoint stays subject to the Server's listening address, reverse proxy, and hostname restriction. The remote management switch controls the Owner web interface and API separately. To restrict where ACP clients can connect from, use your firewall or reverse proxy rules.

## Share a session between Astral and the client

Client prompts and Bot responses appear in Astral's normal transcript. Once the client has loaded the session, it receives updates even when a message starts in Astral or a scheduled task or event starts the Bot. Updates include messages, readable thinking, and tool activity. The client receives a response to its own prompt when that turn finishes. The same prompt is already available locally, so it is not echoed as a new user message to that connection.

If a session fails to load, correct the error and retry it on the same connection. Other loaded sessions continue streaming and retain their attached tools.

## Attach remote MCP tools

Include `mcpServers` when your client creates or loads the session. Use `type: "http"` for [Streamable HTTP](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports) or `type: "sse"` for the older HTTP+SSE transport. The MCP URL must be reachable from the Astral Server. Supply any required request headers as `{ "name": "...", "value": "..." }` entries in `headers`. Local `stdio` tools cannot be attached.

Use an HTTP or HTTPS address as seen from Astral Server. For a service on the same host, use a loopback address such as `127.0.0.1`. For a service on a private network or Tailscale, use the address the Server can reach on that network. Give ACP tokens only to trusted clients, since they can ask Astral Server to connect to services it can reach.

One ACP connection at a time can attach tools to a session. Other clients can load it with an empty `mcpServers` list to follow its work. The attached tools are available to that session and the subagents it delegates to. If the connection drops, calls through it stop immediately. Reconnect and load the session with the same tool set to restore access. For a short period after disconnect, Astral keeps the tool declarations so a reconnect with the same tools can reuse the existing configuration. If a tool call alone fails, inspect the tool result or the remote service before retrying.

### Client and Computer API reference

Astral implements the ACP v1 JSON-RPC session subset over WebSocket. The supported requests are `initialize`, `session/new`, `session/load` and `session/prompt`, with `session/cancel` as a notification. Send one JSON-RPC object per WebSocket text message. `initialize` reports `protocolVersion: 1`, history loading, and the allowed MCP transports. `agentCapabilities._meta["dev.oines.astral/websocket"]` identifies Astral's WebSocket transport version, currently `1`. A prompt can contain text and resource links. For `session/new`, use an absolute `cwd` on the Bot's authorized default Computer. With no default Computer configured, the session opens without a working directory. `session/load` requires an absolute `cwd` in the request and keeps the existing working directory.

The model sees `source="acp"` on ACP messages and `source="web"` on messages from the web interface. If the ACP token has a display name, the `client` attribute in the model's message envelope contains only that XML-escaped name. The `_meta.trigger_source` field in `session/update` follows the recorded message source. History replay uses the source metadata already recorded for each message. Astral sends its activity summary in a separate `_astral/activityStatus` notification.

Commands running on an authorized Computer can discover the session's attached tools with `GET /v1/mcp/tools` and invoke one with `POST /v1/mcp/tools/{name}/call`, passing the discovered name and an `arguments` object that follows its input schema. Follow the `mcp` capability from `local_api.capabilities_url` in the command environment for request schemas and authentication. The command uses its own Computer API credential.
