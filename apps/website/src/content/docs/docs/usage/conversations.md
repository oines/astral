---
title: "Use sessions"
description: "Start work, add instructions, stop execution, and continue later."
---

A session belongs to one Bot and retains messages, execution records, and task context. Reopen it from the sidebar to continue earlier work.

The sidebar shows running and scheduled sessions first, then sessions bound to Telegram, QQ, WeChat, and Feishu, followed by pinned and recent ordinary sessions. Empty groups are hidden. QQ's official Bot and NapCat share the QQ group. A session appears once according to that order.

Use a session's menu to rename, archive, or restore an ordinary session. Bots can also rename or archive their own sessions through the Gateway API. A session bound to IM takes its chat's name and cannot be renamed or archived; its menu hides both actions. See [Schedules](/docs/usage/schedules/) to arrange future prompts.

## Start a task

Choose a Bot, create a session, check the model and computer shown near the input, then describe your task. Include the desired result, relevant files, and important constraints.

The computer determines where file and command operations run. The model determines how the task is interpreted and advanced. Both can change within a session.

## Add instructions during work

Ordinary sessions support two modes:

- **Queue:** handle the message after the current turn. Use it for a next task.
- **Steer:** deliver the new instruction at the next point where the running Bot can accept it. Use it to correct direction or add a constraint.

Pending queued messages can be edited, removed, or reordered. A new instruction does not undo an operation that has already run.

In a session bound to a chat platform, messages received from that chat join the Bot's following model steps while it is working, without a separate wakeup. When idle, private messages, direct mentions, replies to the Bot, and supported pokes wake it directly; ordinary group messages follow the [proactive participation setting](/docs/chat/groups/). Web input in a bound session uses Steer.

## Stop and continue

After stopping, wait for the state to confirm. Stopping does not roll back files or completed external actions; inspect execution records and actual results when needed.

When Continue is available, resume from the existing context or send updated instructions first. Restore an offline computer or failed model connection before resuming dependent work.

## Timezone

Choose the shared timezone in **Settings → General → Timezone**. **Follow system** uses the operating-system environment where Astral is installed, not the browser device or the computer selected for commands. Message times, reply dates, and schedules use this setting across clients. Today and yesterday are determined in that timezone; hover over a reply's time to see its complete date, seconds, and timezone.

Changing the timezone updates displayed dates without changing when events occurred. Only newly constructed model inputs use the new timezone. Earlier model context, environment snapshots, summaries, and frozen requests are not rewritten; network retries reuse their original request.

Bot and Session identity is supplied in the model’s System Prompt. New environment snapshots focus on the Computer, workspace, available skills and API discovery. A fork uses its own Session ID while retaining the copied history unchanged.

## Long conversations

As a conversation grows, Astral summarizes older context and keeps recent messages for the next model request. Received chat messages and images count toward the context budget even while the Bot stays silent. The budget uses the model's reported usage where it still applies, plus estimates for everything added afterward.

Automatic summarization can call the model without starting an ordinary task or sending an IM reply. Original history is preserved. If summarization or the final context check fails, existing context and pending messages remain available; address the reported error before continuing.

## Separate unrelated work

Keep related tasks together and use a new session for unrelated work. A copied or forked session is independent; later messages do not synchronize.

An external chat binds to one Astral session. See [Connect a chat](/docs/chat/overview/) before replacing that binding.
