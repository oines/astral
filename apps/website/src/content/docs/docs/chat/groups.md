---
title: "Work in group chats"
description: "Let members contribute to one task and receive results in the same chat."
---

Telegram, QQ, and Feishu support groups; Weixin currently supports private chats. Group members share the chat’s session to request work, add information, and receive results.

**For a public group, use a dedicated Bot and a separate container. We strongly recommend keeping your everyday computer out of its permissions.** Any member can request tasks that use the Bot’s authorized computers and platform permissions. Read [Use Astral safely](/docs/start/safe-use/) before connecting.

## Start with a direct mention

1. Add the platform Bot to the group and [authorize the chat](/docs/chat/overview/).
2. Mention the Bot or reply to one of its messages with a task.
3. Other members can add information. While the Bot is working, newly received messages join its following model steps. Mention or reply to wake it when it is idle.
4. Ask for the result or file to be sent back, then check the actual send outcome.

For example, one member requests a weekly report from a computer, another adds completed work, and the Bot creates and sends the report in the same session.

## Ordinary messages and proactive participation

Proactive participation is off by default. Ordinary messages delivered for a connected group still enter its context, but do not start a task by themselves while the Bot is idle.

While the Bot is working, it reads new messages in subsequent steps and decides how they relate to the task. Private chats follow the same rule.

Enable proactive participation per group to let Astral decide whether to wake an idle Bot after 10 seconds without a new message, or 60 seconds from the first pending message. It may decide to remain silent. Direct mentions, replies to the Bot, and supported pokes addressed to it wake it without waiting for this check; private messages also wake an idle Bot directly.



## Available context and permissions

Astral only sees messages the platform delivers. Telegram privacy mode, QQ permissions, and Feishu application permissions can limit ordinary group messages. Proactive mode neither expands those permissions nor imports all history from before the Bot joined.

Members share the chat session. Tasks use the **Bot's authorized computers**; sending a message does not create a separate operating-system account or private directory for each member.

## Keep chats separate

Different chats use separate sessions. The same Bot can still query other non-private sessions through the built-in API, and computer files may be shared. [Make personal work private](/docs/usage/conversations/#private-sessions); use separate Bots and environments for groups with different levels of trust.

Telegram forum topics can be connected individually. Feishu threads currently belong to their parent chat.

Replace a group's session in the account's connected-chat settings when needed. Existing histories remain; running tasks in the affected sessions stop.
