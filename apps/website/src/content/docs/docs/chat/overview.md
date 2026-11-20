---
title: "Connect a chat"
description: "Send tasks from Telegram, QQ, WeChat, or Feishu and get answers and files right back in the same chat."
---

Once connected, you can message Astral from your everyday chat apps. It runs tasks on your authorized computers and sends back the answers or files you ask for. **Each connected chat maps to its own Astral session**, so you can inspect or continue the same work in either place.

:::tip[Verify inside Astral first]
[Complete your first task](/docs/start/first-steps/) inside Astral before connecting a chat platform. Astral must stay running to receive messages—see [Desktop or Server?](/docs/hosting/architecture/) if you want it online while your laptop is closed.
:::

## 1. Choose a platform and connect an account

Open **Settings → Bots → your Bot → Connect a platform** and follow the setup guide for your platform:

| Platform | How it connects | Supported chats |
| --- | --- | --- |
| **[Telegram](/docs/chat/telegram/)** | Bot token from BotFather | Private chats, groups |
| **[QQ](/docs/chat/qq/)** | Regular QQ (NapCat) or Official QQ Bot | Private chats, groups |
| **[Weixin](/docs/chat/weixin/)** | QR-code authorized Bot | Private chats only |
| **[Feishu](/docs/chat/feishu/)** | Enterprise custom-app bot (permissions, release, events) | Private chats, groups |

One Astral Bot can connect multiple platform accounts, each managing its own credentials and allowed chats.

:::caution[Isolate public groups]
Group members can ask the Bot to use its authorized computers and chat-account permissions. Set up a dedicated Bot and [separate environment](/docs/start/safe-use/#use-a-separate-environment-for-public-groups) before connecting a public group.
:::

## 2. Choose which chats to allow

New accounts start in **Only specified chats** mode (empty list), which is the safest default:

1. Follow the platform guide to discover the chat or copy its Chat ID, then add it under **Connected chats**.
2. Choose either a **new session** for a clean slate or an unbound **existing session** from the same Bot (make sure any existing history is safe to share with a group).
3. Switch to **All chats the account can access** only if you want new private messages or first group mentions/replies to create sessions automatically.

## 3. Send a fresh message to verify

Return to your chat app and send a **new message** (mention or reply to the Bot in a group; earlier discovery messages do not auto-run):

> Please reply in this chat with “Connection successful” and tell me which computer you are using.

Once you see activity inside Astral and receive the reply in your chat app, you're connected. Try sending an image or small file next and ask the Bot to send a summary back.

:::note[Ask explicitly when you want files or replies sent back]
Working text inside Astral stays in the web interface—chat participants only see messages delivered through the sending tool (`send_message`). Say “send it back to this chat” when you want a file or final answer delivered there.
:::

## Everyday management

| Goal | How to do it |
| --- | --- |
| Control when the Bot speaks in groups | Mentions and replies wake it by default; see [Group chats](/docs/chat/groups/) for proactive participation. |
| Remove a single chat | Switch to **Only specified chats** first (existing entries and settings stay intact), then remove it. |
| Pause an account | Turn off **Enabled** to stop incoming messages while keeping history (stop active computer tasks separately in the session). |
| Start a fresh thread for a chat | Use **Change conversation**—both sessions pause with histories preserved (restore an archived target before binding). |
| List members or manage group files | Just ask the Bot directly; only see [Platform APIs](/docs/chat/platform-api/) for custom scripts. |

<details class="astral-doc-details">
<summary>Missed-reply reminders and model error recovery details</summary>

- **One-time reminder on direct triggers:** For private messages, mentions, or replies, if the Bot finishes normally without calling the sending tool, Astral nudges it once. It may still opt not to reply, and Astral never forwards internal working text automatically. This does not apply to proactive participation or failed/unconfirmed send attempts.
- **Quota, credential, or permission errors:** Processing pauses with the error shown while Astral keeps receiving and archiving incoming chat messages. After fixing credentials/quota, send new input in Astral—or switch to another working model—to resume queued messages. Rate limits and transient network errors retry on intervals; sending more chat messages will not speed up retries.
- **Summary exceeds context window:** Processing pauses while waiting messages stay safe. Switch to a model with a larger window or fix the model's window settings to trigger compaction and resume.

</details>
