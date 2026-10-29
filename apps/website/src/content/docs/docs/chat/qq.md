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

Official Bot openids are **not QQ user or group numbers** and cannot be reused across applications. This integration supports private chats and permitted groups, not QQ channels. Ordinary group messages, reply windows, and file sending depend on platform permissions. Message reactions are not supported by the current group/C2C integration; QQ channel features do not imply that support.

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

## NapCat replies, pokes and recalls

Replies carry the original text and media references when that message is already in the same chat's Astral archive. Referenced files remain separate from new attachments and are not downloaded automatically. If the original is missing, the context says so; Astral does not search another chat or fetch the message automatically.

Pokes appear as independent events with the actor, recipient, platform action text and custom suffix when NapCat supplies them. Another person poking the Bot can wake it in an authorized, bound chat. Pokes between other people, and actions performed by the Bot itself, remain passive context.

A recall appends an event identifying the operator and original message. It does not erase or rewrite context the Agent already received. These events follow the same chat permissions as messages and do not authorize a new chat. NapCat has no missed-message replay; separate delivered notices are recorded separately even when their visible contents match.

## NapCat reactions and outgoing pokes

NapCat provides partial group-reaction notifications, mainly for the logged-in account's own messages. Astral appends events preserving QQ emoji IDs and the actor when supplied; absent identities are explicitly unknown. Native counts remain reported values rather than reliable totals, and missing notifications are not invented. Known self actions do not wake the Bot. Reactions with an unknown actor are passive context only, without starting a participation check; a working Bot can still read them. Group reactions from a known external actor follow optional proactive rules.

The Agent can use the existing Computer API's `set_reaction` to add or remove a reaction on an archived message whose chat membership is verified, and `poke` to poke someone. Group targets must be specified and verified as group members; private targets can only be that chat's peer. These actions retain account and chat permissions without changing `send_message`.

A private window shake is separate from a poke. An incoming shake appears as `[QQ private window shake]` in context. The current NapCat API has no supported outbound window-shake operation, so Astral cannot send one and does not substitute a poke.
