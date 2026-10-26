---
title: "Connect QQ"
description: "Choose the official Bot integration or NapCat and authorize private or group chats."
---

Choose the connection type when creating a QQ account. The two methods use different credentials and chat identifiers.

| Method | Credentials | Chat identifiers |
| --- | --- | --- |
| Official Bot | App ID and App Secret | `private:<user_openid>`, `group:<group_openid>` |
| NapCat | HTTP URL, forward WebSocket URL, Access Token | `private:<QQ number>`, `group:<group number>` |

## Official QQ Bot

1. Create a Bot on the [QQ Open Platform](https://q.qq.com/) and obtain its App ID and App Secret.
2. Configure test users, groups, or release availability and obtain the needed receiving and sending permissions.
3. In Astral, choose **Connect a platform → QQ → Official Bot**, enter credentials, and connect.
4. Send a message from an allowed user or group so Astral can discover the chat, then authorize it and bind a session.

Normally keep the default API and token endpoints. Astral connects to the platform gateway; no public webhook is required.

Official Bot openids are **not QQ user or group numbers** and cannot be reused across applications. This integration supports private chats and permitted groups, not QQ channels. Ordinary group messages, reply windows, and file sending depend on platform permissions.

## NapCat

Install [NapCat](https://napneko.github.io/) and sign in to the intended QQ account, then:

1. Enable the HTTP server.
2. Enable a **forward WebSocket** server that delivers events, not the API-only `/api` endpoint.
3. Use the same Access Token on both, with message format `array`.
4. To read attachments hosted on a remote NapCat machine, enable `enableLocalFile2Url` so the file API can return content instead of only a local path.

Choose QQ NapCat in Astral and enter both URLs and the token. Both endpoints must represent the same QQ account and be reachable from Astral Server. A loopback address only works in the appropriate shared host/network environment.

## Verify the chat

Authorize the private chat or group and select a session. Mention the Bot in a group, verify a text reply, then try sending a file.

NapCat sends plain text; Markdown syntax is not rendered. It does not replay messages missed while disconnected. If a send outcome is uncertain, inspect the chat before sending again.
