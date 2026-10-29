---
title: "Use sessions"
description: "Start work, add instructions, stop execution, and continue later."
---

A session belongs to one Bot and retains messages, execution records, and task context. Reopen it from the sidebar to continue earlier work.

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

## Long conversations

As a conversation grows, Astral summarizes older context and keeps recent messages for the next model request. Received chat messages and images count toward the context budget even while the Bot stays silent. The budget uses the model's reported usage where it still applies, plus estimates for everything added afterward.

Automatic summarization can call the model without starting an ordinary task or sending an IM reply. Original history is preserved. If summarization or the final context check fails, existing context and pending messages remain available; address the reported error before continuing.

## Separate unrelated work

Keep related tasks together and use a new session for unrelated work. A copied or forked session is independent; later messages do not synchronize.

An external chat binds to one Astral session. See [Connect a chat](/docs/chat/overview/) before replacing that binding.
