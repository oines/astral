---
title: "Group collaboration"
description: "Work on tasks together and choose when the Bot joins a discussion."
---

A connected group can give the Bot a task together. One member asks it to organize meeting notes, another adds a missing decision, and the Bot continues in the same Astral conversation before sending the result back.

Telegram, QQ, and Feishu support groups; Weixin currently supports private chats only. First [connect the account and add your group](/docs/chat/overview/).

## What group members share

The group shares one Astral conversation and **the Bot's authorized computers and platform accounts**. Members do not get separate operating-system accounts or directories. Group management tasks also use the platform account's own permissions.

Use a dedicated Bot and a separate environment, such as a container, for public groups. Avoid granting access to an everyday computer with personal files. Groups with different trust levels should use different Bots and execution environments; see [Safe use](/docs/start/safe-use/).

## Give tasks and add information

When the Bot is idle, **mention it** using the messaging app's mention feature, or reply to one of its messages:

> @Bot Please organize the following meeting notes into “Decisions” and “Action items,” then send the result to this group.

While it works, members can add information such as “The launch date is now next Wednesday.” The Bot reads new messages in later steps and decides how to adjust the task. Say “This adds to the previous task” or “This is a separate task” when that distinction matters.

Telegram privacy mode can restrict delivery. Test by replying to a Bot message first; see [Telegram group setup](/docs/chat/telegram/#use-groups).

## Should it reply to ordinary discussion?

By default, ordinary group messages do not start a task when the Bot is idle, but messages delivered by the platform are kept as context. The Bot also reads those new messages while working.

Choose **Join discussions** for the group in the account's connected-chat list to let it decide when to participate. This optional setting is per group and off by default. It evaluates after 10 seconds without a new message, or after 60 seconds from the first pending message, and can choose to stay quiet. Mentions, replies, and supported pokes addressed to the Bot trigger it directly without waiting for that evaluation.

Proactive participation does not expand the messages Telegram, QQ, or Feishu delivers, or load the group's entire earlier history. Resend source material when context is missing.

## Different groups and private work

Different groups use different Astral conversations. Telegram forum topics can be connected separately; Feishu threads currently belong to their parent chat.

The same Bot can query other non-private conversations through built-in APIs, and computer files may be shared. You can [make personal work private](/docs/usage/conversations/#private-sessions), but that does not retract content already copied elsewhere or isolate shared files. Changing a group's bound conversation stops work in both conversations and retains their histories.
