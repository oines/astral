---
title: "Connect a chat"
description: "Give tasks through chat, let your computers do the work, and receive results there."
---

Use Telegram, QQ, Weixin, or Feishu to give Astral tasks and receive results from your computers. Each connected chat has an independent Astral session.

## Before connecting

Complete the Bot’s [model and computer setup](/docs/start/first-steps/) and verify a task inside Astral. For public groups, we strongly recommend a dedicated Bot and separate container, with no direct access to your everyday computer. Review [permissions and data access](/docs/start/safe-use/) before connecting.

| Platform | Connection | Supported chats |
| --- | --- | --- |
| [Telegram](/docs/chat/telegram/) | Bot Token | Private chats, groups, forum topics, channels |
| [QQ](/docs/chat/qq/) | Official Bot or NapCat | Private and group chats; identifiers and capabilities differ |
| [Weixin](/docs/chat/weixin/) | ClawBot / iLink QR authorization | Private chats |
| [Feishu](/docs/chat/feishu/) | Custom enterprise app Bot | Private and group chats |

## 1. Connect an account

Open **Settings → Bots → your Bot → Connect a platform**. Select a platform and enter credentials or complete QR authorization. Each platform retains an account list; accounts manage their chats separately.

After connecting, choose which chats this account may use.

## 2. Choose chat access

New accounts start with “Only specified chats” and an empty list. Add a chat or select a discovered one, check its Chat ID, and supply a Telegram Topic ID when needed.

Alternatively, allow all chats the account can access. A private message or the first group mention or reply to the Bot creates a session automatically. Its reach depends on the chats the platform allows that account to receive.

Switching back to specified chats retains active chats and their settings. Use this mode before removing an individual chat.

## 3. Bind and verify

Create a new session or select an unbound session belonging to the same Bot. Send a small task, such as “Which computers can you use?”, and verify the reply in that chat.

Open the session in Astral to inspect messages, executions, and files. Results reach the external chat through explicit sends; every Web reply is not automatically forwarded.

## How the Bot understands its chat

The Bot receives the current platform, chat name, and topic, together with each message’s sender, time, quotes, and attachments. Names inside forwards or quotes remain source information. Changing a binding or forking a session preserves the original chat source of earlier messages.

## Replace a binding or pause

Replacing the session binding stops work in both the original and target sessions while keeping their histories. Already accepted sends keep their original destination.

Disable the account to pause incoming messages and keep history. Stop any computer work already running in its session separately. See [Group chats](/docs/chat/groups/) for trigger rules.

## Platform queries and management

The Bot can also query members and profiles, moderate groups, and use platform-specific notice, file and essence operations through the Gateway. See [Platform APIs](/docs/chat/platform-api/) for discovery, permissions and examples.
