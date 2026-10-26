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

Sessions bound to a chat platform use steering: direct mentions, replies, and Web input enter the same session.

## Stop and continue

After stopping, wait for the state to confirm. Stopping does not roll back files or completed external actions; inspect execution records and actual results when needed.

When Continue is available, resume from the existing context or send updated instructions first. Restore an offline computer or failed model connection before resuming dependent work.

## Separate unrelated work

Keep related tasks together and use a new session for unrelated work. A copied or forked session is independent; later messages do not synchronize.

An external chat binds to one Astral session. See [Connect a chat](/docs/chat/overview/) before replacing that binding.
