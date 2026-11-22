---
title: "Group chats"
description: "Collaborate on tasks with a Bot in group chats and control when it joins ordinary discussions."
---

In a connected group, members can work with the Bot together—one person starts a task, another adds details, and the Bot carries everything forward in one session before posting results back to the group.

Telegram, QQ, and Feishu support groups (Weixin is currently private chats only). First [connect your account and add the group](/docs/chat/overview/).

:::caution[Everyone in the group shares the Bot's permissions]
All members share one session plus the Bot's authorized computers and chat-account permissions. Use a dedicated Bot and isolated container for public groups—see [Permissions and privacy](/docs/start/safe-use/#use-a-separate-environment-for-public-groups).
:::

## Start a task and add details

- **Wake the Bot:** mention it using the app's native `@` mention, or reply to one of its messages:

> @Bot Turn the meeting notes below into “Decisions” and “Action items” and post the result back here.

- **Add context mid-run:** while the Bot is working, members can post updates such as “Launch moved to next Wednesday.” With the default settings, the Bot can read these updates on its next step. If **Use group context** is off, mention the Bot or reply to its message to make sure it receives the update.

:::tip[Test Telegram groups by replying first]
Telegram's privacy mode can restrict ordinary group messages. Test by replying directly to a Bot message first—see [Telegram group setup](/docs/chat/telegram/#use-groups).
:::

<span id="control-proactive-replies"></span>

## Choose which group messages the Bot uses

In **Settings → Bots**, open the Bot’s platform account. Under **Connected chats**, each group and each connected Telegram group topic has its own **Use group context** switch.

Leave it on (the default) if members want the Bot to follow their conversation. Ordinary messages delivered by the platform can then inform its work and later replies. Choose when an idle Bot may speak:

- **When mentioned** (default) — the Bot waits for a mention, a reply to one of its messages, or a supported poke addressed to it.
- **Join discussions** — the Bot also considers joining after 10 seconds of quiet or 60 seconds from the first pending message. It may choose to stay quiet.

Turn **Use group context** off if members prefer to address the Bot explicitly. Astral still archives ordinary group messages and events, including reactions, but skips adding them to the session. Direct mentions, replies, supported pokes and the Bot’s own messages still reach the session. Earlier context stays in place. Turning the switch back on restores the saved reply mode for new messages and leaves the intervening chat in the archive.

The Bot can use only what the platform delivers. If it needs something shared before the group was connected, resend it in a mention or reply.

## Multiple groups and private work

- **One session per group:** each group (and each Telegram forum topic) gets its own session; Feishu threads currently belong to their parent chat.
- **Keep private work separate:** sessions of the same Bot can query non-private sessions and share computer folders. Use a separate Bot for private work, or [mark personal sessions private](/docs/usage/conversations/#private-sessions). Changing a group's bound session stops active work on both sides while keeping their histories.
