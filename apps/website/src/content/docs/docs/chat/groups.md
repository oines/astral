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

- **Add context mid-run:** while the Bot is working, members can post updates (e.g., “Launch moved to next Wednesday”). The Bot reads incoming messages on subsequent steps—say “This adds to the current task” or “Separate task” when clarity helps.

:::tip[Test Telegram groups by replying first]
Telegram's privacy mode can restrict ordinary group messages. Test by replying directly to a Bot message first—see [Telegram group setup](/docs/chat/telegram/#use-groups).
:::

## Control proactive replies

| Mode | Behavior | Where to change |
| --- | --- | --- |
| **Mentions & replies only (default)** | Idle Bots stay quiet during ordinary chat, while delivered messages remain available as context. | Default setting |
| **Join discussions (per group)** | Evaluates after 10 s of quiet—or 60 s from the first pending message—and may still stay quiet. Mentions, replies, and pokes trigger immediately. | Enable per group in **Connected chats** |

Proactive mode does not bypass platform delivery limits or fetch pre-join history—resend source material when needed.

## Multiple groups and private work

- **One session per group:** each group (and each Telegram forum topic) gets its own session; Feishu threads currently belong to their parent chat.
- **Keep private work separate:** sessions of the same Bot can query non-private sessions and share computer folders. Use a separate Bot for private work, or [mark personal sessions private](/docs/usage/conversations/#private-sessions). Changing a group's bound session stops active work on both sides while keeping their histories.
