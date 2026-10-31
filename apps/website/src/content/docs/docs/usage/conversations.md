---
title: "Use sessions"
description: "Start work, add instructions, stop execution, and continue later."
---

A session belongs to one Bot and retains messages, execution records, and task context. Select it in the sidebar to continue. On narrow screens, one tap opens the session and closes the sidebar; the three-dot button on each row opens its menu.

The sidebar shows running and scheduled sessions first, then sessions bound to Telegram, QQ, WeChat, and Feishu, followed by pinned and recent ordinary sessions. Empty groups are hidden. QQ's official Bot and NapCat share the QQ group. A session appears once according to that order.

Use a session's menu to rename, archive, or restore an ordinary session. Bots can also rename or archive their own sessions through the Gateway API. A session bound to IM takes its chat's name and cannot be renamed or archived; its menu hides both actions. See [Schedules](/docs/usage/schedules/) to arrange future prompts.

## Start a task

Choose a Bot, create a session, check the model and computer shown near the input, then describe your task. Include the desired result, relevant files, and important constraints.

The computer determines where file and command operations run. The model determines how the task is interpreted and advanced. Both can change within a session.

## Private sessions

When personal work and a group share one Bot, make personal sessions private to keep other sessions from querying their records.

Choose **Make private** from the session's sidebar menu. A lock marks the session; choose **Turn off private mode** to reverse it. You can still open and manage it in Desktop and the browser.

Other sessions cannot discover, read, or modify a private session through the built-in API, or access its currently bound IM chat and plans targeting it. The private session retains access to its own resources. A fork inherits its source session's private setting.

Private sessions still save history and send the context needed for work to the selected model service. Files on shared computers are not isolated, and information already copied to another session remains there. Unbinding or rebinding a chat applies the new session relationship. See [Use Astral safely](/docs/start/safe-use/) for the full guidance.

## Add instructions during work

Web input supports two modes in every session, including sessions bound to a chat platform:

- **Queue:** handle the message after the current turn. Use it for a next task.
- **Steer:** deliver the new instruction at the next point where the running Bot can accept it. Use it to correct direction or add a constraint.

Pending queued messages can be edited, removed, or reordered. A new instruction does not undo an operation that has already run.

In a session bound to a chat platform, messages received from that chat join the Bot's following model steps while it is working, without a separate wakeup. When idle, private messages, direct mentions, replies to the Bot, and supported pokes wake it directly; ordinary group messages follow the [proactive participation setting](/docs/chat/groups/). Web input follows the same mode preference, with Queue as the default. Use Command/Ctrl+Enter to Steer a message directly. Confirming text with an input method does not send it; press Enter separately to send, or Shift+Enter for a new line.

## Stop and continue

After stopping, wait for the state to confirm. Stopping does not roll back files or completed external actions; inspect execution records and actual results when needed.

When Continue is available, resume from the existing context or send updated instructions first. Restore an offline computer or failed model connection before resuming dependent work.

## Timezone

Choose the timezone for displayed times and schedules under **Settings → General → Timezone**. **Follow system** uses the Astral host's timezone. Clients display messages and schedules in the new timezone after a change; the time an existing event occurred stays the same.

## Long conversations

The chat loads recent messages first and fetches earlier history as you approach the top. Reconnecting keeps loaded messages and your reading position. Switching back to a session still cached during this visit restores its loaded history and reading position before syncing new messages. Refreshing the page or evicting a cached session loads recent messages again.

Tool arguments, text output, and results arrive with each message page, so expanding an execution step shows its details immediately. Images and attachments load when needed. The original rollout remains available through **Export rollout**. Scrolling up while the Bot responds stops following new output; return to the bottom or click **Jump to latest** to follow again.

As a conversation grows, Astral asks the model to summarize older context and keeps recent messages. Incoming chat messages and images use context even when the Bot does not reply. Original history remains available.

If summarization fails, existing context and pending messages remain. Address the reported error before continuing.

## Manage session and chat storage

Open **Settings → Storage**, check disk usage, then select a Bot and find the chat or session to clean up. Usage refreshes when you enter and after cleanup; you can also refresh manually. Chat names and platforms identify the rows, with account details when a platform has several accounts.

“About” marks a content-size estimate for comparing chats and sessions. The overview shows measured disk usage. An unfinished or failed estimate is labeled.

Sessions without a chat binding can be **archived** or **restored**. Only archived, unbound sessions can be **permanently deleted**. Deletion removes that session's history, model calls, pending messages, recall index, and schedules. Independent forks, computer work files, and external platform messages remain.

For a bound chat, choose the content to remove:

| Action | Effect |
| --- | --- |
| **Clear IM archive** | Delete Astral's chat archive before the selected date. Messages awaiting processing stay temporarily; session records and platform messages remain. |
| **Archive retention** | Defaults to **Keep forever**. Choosing **30, 90, or 180 days** immediately clears eligible expired archives, then checks daily. Returning to Keep forever stops expiry; deleted content cannot be restored. |
| **Clear session** | Stop current work, permanently delete the old session and its plans, and create a new session with the same private setting for the chat. The IM archive, account settings, and platform messages remain. |

Content already included in a session remains after archive cleanup until you clear or delete that session separately. **Replacing the session** keeps the old history, useful when starting a new topic you may want to revisit later.

The list updates after cleanup. Astral reclaims disk space in the background, so it may be released later. Follow the affected operation's error or retry action if it fails. After a lost connection, let the page reconnect and check the result before starting the same cleanup again. See [Storage maintenance](/docs/hosting/maintenance/#review-and-clean-storage) for backups and application files.

## Separate unrelated work

Keep related tasks together and use a new session for unrelated work. A copied or forked session is independent; later messages do not synchronize.

An external chat binds to one Astral session. See [Connect a chat](/docs/chat/overview/) before replacing that binding.
