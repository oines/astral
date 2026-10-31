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

## Private sessions

Choose **Make private** from a session's sidebar menu. A lock identifies it; choose **Turn off private mode** to reverse this. You can still open and manage it normally in Desktop and the browser.

Other sessions, including those belonging to the same Bot, cannot discover, read, or modify a private session through the Gateway API. Its bound IM chat and plans targeting it are hidden too, even when the account allows access to all chats. The private session retains access to its own resources. A fork inherits its source session's private setting.

Chat privacy follows the current session binding. Unbinding or rebinding applies the new relationship. Permission checks also apply to known IDs and old cursors. This controls subsequent Gateway access; it does not remove information already copied into another session or isolate shared computer files.

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

The chat loads recent messages first and fetches earlier history as you approach the top. Reconnecting keeps loaded messages and your reading position. Switching back to a session still cached during this visit restores its loaded history and reading position before syncing new messages. Refreshing the page or evicting a cached session loads recent messages again.

Tool arguments, text output, and results arrive with each message page, so expanding an execution step shows its details immediately. Images and attachments load when needed. The original rollout remains available through **Export rollout**. Scrolling up while the Bot responds stops following new output; return to the bottom or click **Jump to latest** to follow again.

As a conversation grows, Astral summarizes older context and keeps recent messages for the next model request. Received chat messages and images count toward the context budget even while the Bot stays silent. The budget uses the model's reported usage where it still applies, plus estimates for everything added afterward.

Automatic summarization can call the model without starting an ordinary task or sending an IM reply. Original history is preserved. If summarization or the final context check fails, existing context and pending messages remain available; address the reported error before continuing.

## Manage session and chat storage

Astral 0.1.8 provides **Settings → Storage** for managing session and chat history. Choose a Bot to see its chats and other unbound sessions, including both archived and unarchived sessions. Search or sort the other-session list to find old work. Open a chat to inspect its IM archive separately from the records of its current Astral session.

The size bars are logical content estimates, including model attempts. Shared content and database overhead mean they cannot be added to the measured disk total or used to predict space freed. Refresh the estimate when needed; an incomplete estimate remains marked.

For an unbound session, **Archive** hides it from ordinary recent history and **Restore** makes it available again. **Permanently delete** requires an archived session with no chat binding and confirms the selected session before deletion. It removes that session's Astral history, execution attempts, pending inputs, recall records and scheduled tasks. Independent forks keep their histories. Computer work files and external platform messages remain.

For a bound chat, choose the action matching what you want to remove:

| Action | Effect |
| --- | --- |
| **Clear IM archive** | Removes eligible Astral archive payloads before the selected date. Keeps the current session's history and external platform messages. Messages still awaiting processing are retained. |
| **Archive retention** | Defaults to **Keep forever**. Choose **30**, **90** or **180 days** to clean eligible expired archives now and check daily afterward. Returning to forever stops automatic expiry; it does not restore deleted archives. |
| **Clear session** | Stops current work, creates a new session with the same private setting, changes the chat binding and permanently deletes the old session and its scheduled tasks. Keeps the IM archive, account settings and external platform messages. |

Archive cleanup keeps lightweight event receipts so a platform retry does not import the same old messages again. Content already in the session remains there until that session is separately cleared or deleted. An ordinary **Replace session** action continues to preserve the old session; only the explicit **Clear session** action deletes it.

Cleanup tasks retain their identity across a page change or lost connection. Check an uncertain result before retrying; retry continues the existing operation. After cleanup, background database reclamation may wait for an idle moment, and storage requests can wait during full compaction. A completed logical deletion and reclaimed disk space are separate results. See [Storage maintenance](/docs/hosting/maintenance/#review-and-clean-storage) for file cleanup, backup protection and reclamation status.

## Separate unrelated work

Keep related tasks together and use a new session for unrelated work. A copied or forked session is independent; later messages do not synchronize.

An external chat binds to one Astral session. See [Connect a chat](/docs/chat/overview/) before replacing that binding.
