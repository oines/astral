---
title: "Connect a chat platform"
description: "Give Astral tasks in Telegram, QQ, Weixin, or Feishu and receive results in the same chat."
---

A connected chat lets you give Astral tasks from your usual messaging app. It uses the model and computers you configured, then sends back the results or files you ask to share. Each chat has its own Astral conversation, keeping tasks and follow-up messages together.

[Complete a task inside Astral](/docs/start/first-steps/) before connecting a platform. Astral must stay running to receive messages. To use it while your own computer is off, see [Running Astral](/docs/hosting/architecture/).

## Choose your connection method

Start at **Settings → Bots → your Bot → Connect a platform** and follow the guide for your account:

| Account | Guide |
| --- | --- |
| Telegram bot | [Connect Telegram](/docs/chat/telegram/) and get a token through BotFather |
| Regular QQ account or official QQ bot | [Connect QQ](/docs/chat/qq/) using NapCat or QQ Open Platform, respectively |
| Bot authorized through a Weixin QR code | [Connect Weixin](/docs/chat/weixin/), currently for private chats |
| Feishu enterprise custom-app bot | [Connect Feishu](/docs/chat/feishu/), including permissions, releases, and event configuration |

One Astral Bot can connect several platform accounts, each with its own chats. Group members can ask the Bot to use its authorized computers and platform permissions. Use a dedicated Bot and a [separate execution environment](/docs/start/safe-use/) for public groups.

## Choose chats after connecting the account

New accounts use **Only specified chats** with an empty list. The platform guide explains how to discover a chat or get its Chat ID; then add it under the account's **Connected chats**.

A new conversation starts with fresh work context. Choosing an existing conversation continues that context; it must belong to the same Bot and be unbound. Before connecting a group, check that any existing context is suitable for its members.

You can instead allow **All chats the account can access**. A new private message, or the first group mention or reply addressed to the Bot, then creates a conversation automatically. This expands the Bot's chat access; keep specified mode if it should serve only a few chats.

## Confirm the connection with a new message

After adding the chat, return to the platform and **send a new message**:

> Please reply in this chat with “Connection successful” and tell me which computer you are using.

Mention the Bot in a group or reply to a message it sent. Earlier discovery messages do not become tasks after authorization.

Check both the processing in Astral and the reply in the original chat. Next, send an image or small file and ask the Bot to read it and send it back. Say “send it back to this chat” when you want a result delivered: the Bot uses its sending tool, and working notes shown in Astral are not all forwarded.

## What matters during everyday use

- **Group replies:** mentions and replies call the Bot by default. See [Group collaboration](/docs/chat/groups/) for ordinary discussion.
- **Removing one chat:** first switch to **Only specified chats**. Switching retains connected chats and their settings.
- **Pausing an account:** turn off **Enabled** to stop reception and keep history. Stop any computer task already in progress separately in its conversation.
- **Changing the bound conversation:** stops work in both the original and target conversations and retains their histories. Previously accepted sends keep their original destination.

If the model runs out of quota, rejects its credentials, or denies access, Astral keeps the error in the corresponding conversation and pauses processing while continuing to receive and archive platform messages. After restoring quota or correcting the credentials, open that conversation in Astral and send a new input to retry; waiting messages will resume processing. Rate limits, temporary service failures, and network errors retry at intervals. Sending more chat messages does not make them retry sooner.

Processing also pauses if the conversation reports that its summary input exceeds the model's context window. Waiting messages are retained. Choose a model that can accommodate that summary input or correct its window configuration before retrying; service recovery or more chat messages do not remove this limit. Changing the model or connection configuration triggers another processing attempt. Work continues after compaction succeeds and the complete context passes its budget checks.

Ask the Bot directly to look up members or manage group files. Read the [Platform API reference](/docs/chat/platform-api/) only when you need custom calls.
