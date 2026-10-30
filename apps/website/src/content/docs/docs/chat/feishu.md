---
title: "Connect Feishu"
description: "Use a custom enterprise app Bot to receive tasks and send results."
---

Use a **custom enterprise application with Bot capability**. A group's custom webhook Bot cannot replace the two-way connection required here.

## Configure the Feishu app

Create the application on the [Feishu Open Platform](https://open.feishu.cn/document/client-docs/bot-v3/bot-overview), enable Bot capability, and obtain its App ID and App Secret.

Choose long-connection event delivery and subscribe to `im.message.receive_v1`. To receive reactions, also subscribe to `im.message.reaction.created_v1` and `im.message.reaction.deleted_v1`. Astral establishes the connection; no public webhook is required.

Grant and publish permissions for the features you need:

| Purpose | Permission |
| --- | --- |
| Receive private messages | `im:message.p2p_msg:readonly` |
| Receive group mentions | `im:message.group_at_msg:readonly` |
| Receive ordinary group messages | `im:message.group_msg`, subject to approval |
| Send as the Bot | `im:message:send_as_bot` |
| Upload and read attachments | `im:resource` |
| Read chat information | `im:chat:readonly` |
| Receive and read message reactions | `im:message.reactions:read` |
| Add or remove the Bot's own reactions | `im:message.reactions:write_only` |

Publish the app, include the intended users in its availability scope, and add the Bot to the group. Permission changes must also complete the platform's publication process.

## Connect in Astral

1. Open **Settings → Bots → your Bot → Connect a platform → Feishu**.
2. Enter App ID and App Secret and connect. Normally retain the default server address.
3. Send a message in the target private chat or group so Astral can discover it.
4. Authorize it under Connected chats and bind a new or existing unbound session.

Feishu Chat IDs use `oc_…`. Threads currently belong to the parent chat rather than separate topic authorization.

## Replies and files

Private messages, mentions, and replies to the Bot can trigger tasks directly. Receiving ordinary group context requires the corresponding permission.

Text, images, files, and supported message resources are available. File and explanatory text sends are separate; successful text does not prove successful file delivery.

Ordered sends can combine text, native user mentions, and uploaded images in a post. Mentions use the user's application-scoped `open_id` (`ou_…`), not their displayed name. Markdown formatting is preserved; images occupy their own paragraphs. Generic files remain separate ordered messages with independent results.

## Message reactions

Reaction additions and removals enter context as separate events retaining the original message ID, the supplied user or app operator, and Feishu's emoji type. They do not rewrite the original message. Astral resolves the chat from its archive first, consulting the platform when needed. An unknown original that can no longer be read is not fabricated or allowed to block later events.

The Agent can use the existing Computer API's `list_reactions`, `add_reaction` and `delete_reaction` operations in an authorized chat with the corresponding permissions. Known self actions do not wake the Bot again. Reactions missing an actor identity are passive context only, without starting a participation check; a working Bot can still read them. Group reactions from a known external actor follow optional proactive rules; private-chat reactions do not directly wake it.

## If no messages arrive

Check publication, user availability, group membership, long-connection subscription, and effective permissions, then Astral's chat authorization.

Missing chat-information permission may leave only a Chat ID visible. A successful connection does not authorize every message, file, or management operation.
