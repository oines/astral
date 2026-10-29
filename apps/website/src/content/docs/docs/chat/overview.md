---
title: "Connect a chat"
description: "Give tasks through chat, let your computers do the work, and receive results there."
---

Use Telegram, QQ, Weixin, or Feishu to give Astral tasks and receive results from your computers. Each connected chat has an independent Astral session.

## Before connecting

Complete the Bot's [model and computer setup](/docs/start/first-steps/) and verify a task inside Astral first.

| Platform | Connection | Supported chats |
| --- | --- | --- |
| [Telegram](/docs/chat/telegram/) | Bot Token | Private chats, groups, forum topics, channels |
| [QQ](/docs/chat/qq/) | Official Bot or NapCat | Private and group chats; identifiers and capabilities differ |
| [Weixin](/docs/chat/weixin/) | ClawBot / iLink QR authorization | Private chats |
| [Feishu](/docs/chat/feishu/) | Custom enterprise app Bot | Private and group chats |

## 1. Connect an account

Open **Settings → Bots → your Bot → Connect a platform**. Select a platform and enter credentials or complete QR authorization. Each platform retains an account list; accounts manage their chats separately.

A successful connection does not authorize every chat.

## 2. Choose chat access

New accounts start with “Only specified chats” and an empty list. Add a chat or select a discovered one, check its Chat ID, and supply a Telegram Topic ID when needed.

Alternatively, allow all chats the account can access. A private message or the first group mention or reply to the Bot creates a session automatically. This cannot expose messages the platform does not deliver.

Switching back to specified chats retains active chats and their settings. Use this mode before removing an individual chat.

## 3. Bind and verify

Create a new session or select an unbound session belonging to the same Bot. Send a small task, such as “Which computers can you use?”, and verify the reply in that chat.

Open the session in Astral to inspect messages, executions, and files. Results reach the external chat through explicit sends; every Web reply is not automatically forwarded.

## How the Bot understands its chat

For a bound chat, Astral describes the current platform, account connection, chat identity, type, title and optional topic once in the model's System prompt. This section stays identical while the chat data is unchanged; a renamed chat appears in the next new model request. Individual observations retain their own source, identity, sender, time, replies, attachments and notices without repeating the full chat information.

Ordinary unbound Web sessions have no current-chat section. Unbinding, replacing a binding or forking a session preserves the source of existing history, so old messages do not become messages from a new chat. Already prepared requests remain unchanged. Wakeups and delivery of new messages still follow the existing [group-chat rules](/docs/chat/groups/).

## Replace a binding or pause

Replacing the session binding stops work in both the original and target sessions while keeping their histories. Already accepted sends keep their original destination.

Disable the account to pause message reception and retain history. See [Group chats](/docs/chat/groups/) for triggers and proactive participation.
