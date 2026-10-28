---
title: "Work in group chats"
description: "Let members contribute to one task and receive results in the same chat."
---

Telegram, QQ, and Feishu support group chats. Weixin currently supports private chats only. Members of a group contribute to the same bound session, so the Bot can use context it has received.

## Start with a direct mention

1. Add the platform Bot to the group and [authorize the chat](/docs/chat/overview/).
2. Mention the Bot or reply to one of its messages with a task.
3. Other members can add information. Mention or reply again when the addition needs immediate handling.
4. Ask for the result or file to be sent back, then check the actual send outcome.

For example, one member requests a weekly report from a computer, another adds completed work, and the Bot creates and sends the report in the same session.

## Ordinary messages and proactive participation

Proactive participation is off by default. Ordinary messages delivered for a connected group can still enter its context, but do not immediately start a task by themselves.

Enable proactive participation per group to let Astral assess whether to respond after a pause in messages. It may decide to remain silent. Direct mentions and replies do not wait for this decision.

Whenever an IM trigger or proactive decision wakes the Agent, it is reminded that ordinary assistant text does not go to IM: replying to the chat requires `send_message`. It can still decide that no reply is needed; this does not change the bound destination or chat permissions.

## Available context and permissions

Astral only sees messages the platform delivers. Telegram privacy mode, QQ permissions, and Feishu application permissions can limit ordinary group messages. Proactive mode neither expands those permissions nor imports all history from before the Bot joined.

Members share the chat session. Tasks use the **Bot's authorized computers**; sending a message does not create a separate operating-system account or private directory for each member.

## Keep chats separate

Different chats use separate sessions. Telegram forum topics can specify a Topic ID. Feishu threads currently belong to their parent chat rather than separate topic bindings.

Replace a group's session in the account's connected-chat settings when needed. Existing histories remain; running tasks in the affected sessions stop.
