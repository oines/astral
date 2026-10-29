---
title: "Work in group chats"
description: "Let members contribute to one task and receive results in the same chat."
---

Telegram, QQ, and Feishu support group chats. Weixin currently supports private chats only. Members of a group contribute to the same bound session, so the Bot can use context it has received.

## Start with a direct mention

1. Add the platform Bot to the group and [authorize the chat](/docs/chat/overview/).
2. Mention the Bot or reply to one of its messages with a task.
3. Other members can add information. While the Bot is working, newly received messages join its following model steps. Mention or reply to wake it when it is idle.
4. Ask for the result or file to be sent back, then check the actual send outcome.

For example, one member requests a weekly report from a computer, another adds completed work, and the Bot creates and sends the report in the same session.

## Ordinary messages and proactive participation

Proactive participation is off by default. Ordinary messages delivered for a connected group still enter its context, but do not start a task by themselves while the Bot is idle.

While the Bot is working, it reads new messages in its following model steps and decides how they relate to the current task. Astral does not run a separate participation check or add another wakeup for those messages. This also applies to private chats.

Enable proactive participation per group to let Astral decide whether to wake an idle Bot after 10 seconds without a new message, or 60 seconds from the first pending message. It may decide to remain silent. Direct mentions, replies to the Bot, and supported pokes addressed to it wake it without waiting for this check; private messages also wake an idle Bot directly.

If the Bot finishes before a newly received message reaches its next model step, Astral applies the idle rules to that message. Messages already read by the ongoing task do not trigger another wakeup, and a late participation result cannot wake it again for the same batch.

Whenever an IM trigger or proactive decision wakes the Agent, it is reminded that ordinary assistant text does not go to IM: replying to the chat requires `send_message`. It can still decide that no reply is needed; this does not change the bound destination or chat permissions.

## Available context and permissions

Astral only sees messages the platform delivers. Telegram privacy mode, QQ permissions, and Feishu application permissions can limit ordinary group messages. Proactive mode neither expands those permissions nor imports all history from before the Bot joined.

Members share the chat session. Tasks use the **Bot's authorized computers**; sending a message does not create a separate operating-system account or private directory for each member.

## Keep chats separate

Different chats use separate sessions. Telegram forum topics can specify a Topic ID. Feishu threads currently belong to their parent chat rather than separate topic bindings.

Replace a group's session in the account's connected-chat settings when needed. Existing histories remain; running tasks in the affected sessions stop.
