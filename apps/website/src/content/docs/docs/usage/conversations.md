---
title: "Sessions and subagents"
description: "Run tasks, steer mid-turn, delegate parallel work to subagents, and manage archiving, privacy, and storage."
---

A **session** is an ongoing workspace where you and a Bot work on a task—ask follow-ups, adjust requirements, switch computers, and return anytime with context intact.

## Start a task and steer mid-run

Open a new session, verify the selected computer is where working files belong, and state the goal, source files, and constraints:

> Create weekly-plan.md in the current working directory with Monday through Friday and three empty slots per day. Send me a file link and confirm which computer and folder you used.

When the Bot is busy, choose how to deliver follow-up input:

| Mode | When to use it | How to send |
| --- | --- | --- |
| **Queue (default)** | Wait until the current turn finishes (e.g., “Put the results in a table next”). Editable and reorderable while waiting. | Normal send |
| **Steer** | Course-correct as soon as the Bot can accept input (e.g., “Only include this year's data”). Already-started actions are not undone. | Pick **Steer** in input bar, or press `Cmd/Ctrl + Enter` |

Expand any activity line to inspect commands, image thumbnails, subagent controls, and chat send results (viewing history never re-runs an action).

## Delegate background work

For long or parallel work, the Bot can hand subtasks to **subagents**—or you can ask directly:

> Check the backend and frontend separately with subagents. Keep the review read-only and summarize the risks here when finished.

- **Keep working in the main thread:** you can keep talking in the original session while subagents run.
- **Inspect live activity:** click any subagent status line to open its full conversation and tool logs in the right-hand panel; switch tabs without losing your reading spot or draft.
- **Reuse and shared files:** subagents keep their history for follow-ups and start on the parent's current computer and directory. Ask for read-only checks or separate directories when tasks shouldn't touch the same files.

<details class="astral-doc-details">
<summary>Subagent naming, model switching, timelines, and compatibility details</summary>

- **Hierarchy & naming:** Only the user-facing root Bot can create subagents (deeper subagents from earlier versions stay viewable and are included in model switches and cleanup). Ask the Bot to name subagents; unnamed ones receive a permanent short code.
- **Panel & timeline:** The subagent list groups **Running** above **Ended** (most recent first). The timeline logs starts, completions, failures, and stops. Deleting a subagent keeps status entries but closes access to its transcript.
- **Progress & control:** The Bot can finish its turn while waiting and wake when a subagent completes or errors; progress notes reach the parent without interrupting it. Manage or stop subagents through the original conversation (stopping the main session alone does not stop background subagents; permanently deleting the main session deletes its full subagent tree).
- **Instructions & model changes:** Subagents share system instructions, tool descriptions, and Bot persona updates on new requests (in-flight requests keep original settings). Changing the model in the main session input bar updates all existing subagents; you can also ask the Bot to change one named subagent's model without affecting others or starting a new turn. Ordinary forks keep independent settings.

</details>

## Stop, continue, and switch computers

- **Stop:** click **Stop** and wait for the session to go idle. Files already modified and messages already sent remain—review the last tool steps before resuming.
- **Continue:** resume when **Continue** appears, or send revised guidance first (e.g., “List which files changed before finishing”).
- **Switch computers:** pick another computer when idle, or let the Bot switch across authorized machines mid-task. Files stay on their original machines—see [Work across computers](/docs/computers/across-computers/).

## Separate unrelated work

Start a new session for unrelated tasks. Organize existing ones with:

- **Archive:** seals an ordinary session and removes it from the sidebar, stopping the session and its subagents and disabling schedules/event endpoints targeting it (changed files and sent messages stay). Old links remain readable, exportable, and forkable; click **Restore session** on the page or go to **Settings → Data & storage** to reopen (resume work or re-enable schedules manually afterward).
- **Fork from here:** creates an independent copy from any message to try another direction (messages don't sync, and schedules aren't copied).
- **Chat-bound sessions:** use the chat name and cannot be renamed or archived; use [Change conversation](/docs/chat/overview/) to start fresh.

## Private sessions

If one Bot handles both personal work and group chats, select **Make private** from the sidebar menu on personal sessions.

- **What it protects:** hides the session, its bound chat, its schedules, and its subagents (plus future forks) from built-in lookups by other sessions of the same Bot. You can still open and manage it in Desktop or a browser.
- **What stays separate:** history is still stored, requests still go to your model provider, and files on shared computers need OS-level isolation—see [Permissions and privacy](/docs/start/safe-use/).

## Long conversations and timezone

- **Automatic compaction:** as a session grows long, Astral summarizes older context while keeping recent messages and full readable history. (Unanswered chat messages and images also consume context.) If compaction fails, existing context and pending messages stay safe until the error is fixed.
- **Timezone:** set global display and schedule time under **Settings → General → Timezone**. **Follow system** uses the Astral host's timezone—choose explicitly if you access a remote Server from another region.

## Manage session and chat storage

Open **Settings → Data & storage**. Search the list by name or filter by Bot. Chats and sessions are sorted by estimated size.

- **Permanently delete an ordinary session:** archive and unbind it from any chat first. Deletion removes history, model logs, pending messages, recall data, schedules, and all subagents—leaving forks, computer files, and external chat messages untouched.
- **Clean up a connected chat:**

| Goal | Action | Effect |
| --- | --- | --- |
| Remove local chat archives | **Clear archive** or change **Retention** | Clear processed local messages, or keep the last 30, 90 or 180 days. Session history, platform messages and messages awaiting processing are retained. |
| Clear the chat's task history | **Reset session** | Stops work, deletes the old session, subagents, and schedules, and starts a fresh session with the same private setting; IM archive and platform messages remain. |

See [Updates and backups](/docs/hosting/maintenance/#review-and-clean-storage) for backups and application file cleanup.
