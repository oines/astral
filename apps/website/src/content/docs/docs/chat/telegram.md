---
title: "Connect Telegram"
description: "Use a Bot Token for private chats, groups, and forum topics."
---

Prepare a Telegram Bot you manage and ensure the Astral Server can reach Telegram's API.

## Connect the account

1. Create a Bot through BotFather and obtain its Bot Token.
2. Open **Settings → Bots → your Bot → Connect a platform → Telegram**.
3. Enter an account name and the token, then connect. Normally leave the server address at its default.
4. Message the Bot privately or add it to your group.

Astral receives updates through long polling. An active webhook for the same Bot prevents connection validation. Stop the previous receiver and remove its webhook before handing it over to Astral.

## Find the Chat ID

In a chat where the Bot can receive and reply, send:

```text
/chat_id
```

In groups, use `/chat_id@your_bot_username`. Astral replies with the Chat ID and, in a forum topic, the Topic ID. This utility needs no model or computer and does not authorize the chat.

Add these identifiers in Connected chats and choose a session. Forum topics use the parent group's Chat ID plus a separate Topic ID.

## Use groups

Authorize the chat in Astral, then mention the Bot or reply to it. To receive ordinary group messages as context, configure privacy mode or administrator permissions according to [Telegram's privacy rules](https://core.telegram.org/bots/features#privacy-mode).

## Files and troubleshooting

Text, images, and files are supported. Request an image as an original document when needed. Telegram's own size and format limits still apply.

If connected but silent, check chat authorization, Topic ID, message visibility, model access, and computer availability. Channels also need the relevant sending permissions.
