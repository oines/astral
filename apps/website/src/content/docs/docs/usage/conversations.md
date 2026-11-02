---
title: "Use sessions"
description: "Start a task, add instructions while it runs, and return to earlier work."
---

A session is where you work with a Bot on a task. You can ask follow-up questions, change the brief, and return later with the earlier discussion still available. After first-time setup, you can start with the existing Astral Bot.

You need an available [model](/docs/usage/models/) and an [online computer the Bot can use](/docs/computers/connect/). The model interprets your request; the computer runs operations and holds the files.

## Start a task

Create a session in the main interface and check that its selected computer is where you want the working files to live. Describe the result, source material, and important limits. For example:

> Create weekly-plan.md in the current working directory. Include Monday through Friday with three empty task slots for each day. Give me a file link and tell me which computer and folder you saved it in.

Open the finished file to check its contents. See [Files and results](/docs/usage/files/) for providing your own material and downloading output.

## Add instructions during work

Choose how soon the Bot should handle the new message:

- **Queue** waits until the current turn finishes. Use it for a next step such as “After that, put the results in a table.” You can edit, delete, or reorder queued messages that have not been processed.
- **Steer** passes an instruction to the Bot as soon as it can accept input during the current work. Use it for corrections such as “Only include this year's data.” An operation that has already started is not automatically undone.

Web input defaults to Queue. Choose the mode in the input area, or use Command/Ctrl+Enter to Steer directly. You can also send instructions from Astral to sessions connected to messaging platforms. For messages sent on those platforms, see [Groups and proactive participation](/docs/chat/groups/).

## Delegate background work

For a longer task or work that can proceed in parallel, the Bot can delegate parts to **subagents**. You can ask for this explicitly, for example:

> Check the backend and frontend separately with subagents. Keep the review read-only and summarize the risks here when the checks finish.

You can ask the Bot to name subagents, such as “Source check” and “Frontend review.” If it leaves the name blank, Astral assigns a short code that stays the same. The initial task text is not used as the name, and follow-up instructions do not rename the subagent.

New subagents can be created at most two levels below the original conversation: original conversation → subagent → next-level subagent. The second level cannot create further subagents. Existing deeper subagents are kept and remain accessible at their recorded positions in the tree; model changes and session cleanup still include them according to their actual relationships.

You continue using the original conversation while the subagents work. The Bot can also delegate partway through a task. When a subagent naturally finishes a round of work, its parent is notified and can review the result, assign a follow-up, or report back to you. Progress reports reach the parent without interrupting it for every update.

Each created subagent appears as a clickable bubble in the conversation. Select it to open its full conversation and tool activity in the right-hand panel. If it creates another subagent, select that bubble to view the next level in the same panel. Use the breadcrumbs or back button to return; the panel retains each reading position, and your original conversation and draft stay in place.

Subagent conversations are read-only and do not appear in the main sidebar. Send corrections, follow-up requests, or instructions to stop a subtask in the original conversation; the Bot manages its own subagents. Stopping the original conversation alone does not stop every subagent working in the background.

To change the model or reasoning settings for this work, use the picker in the original conversation's input area. The choice updates all existing subagents, including deeper levels, and replaces any model they were previously assigned. Requests already sent keep their original settings; following requests and subtasks that have not started use the new choice. The selection survives a restart. New subagents inherit the latest choice by default; the Bot can still choose another model for a new subagent, and your next change in the original conversation updates it again. Other conversations and ordinary copies made with **Fork from here** keep their own settings.

You can also ask the Bot to change the model and reasoning settings for one existing subagent. Its parent changes only that subagent; any subagents below it keep their own settings. For example, ask for a more capable model for “Source check” while the other work continues as before. A model change alone does not start another round of work. Your next change in the original conversation's picker updates all existing subagents again.

Subagents retain their histories after a round ends, so the Bot can reuse them for related follow-up work. They use the same Bot's permitted computers and normally start on the parent's current computer and working directory. Their files may be shared with the original conversation and other subagents. Ask for read-only checks or separate working directories when tasks should not modify the same files.

## Stop and continue

Select Stop and wait for the session to leave its running state. Files already changed and messages already sent remain. Before continuing important work, review the last execution steps and check the actual files.

When **Continue** is available, resume using the existing context or send revised instructions first. For example: “Tell me which files have already changed before doing the remaining work.” If the computer is offline or the model connection failed, restore that connection first.

You can change the selected computer while the session is idle. During a task, the Bot can also use other computers it has permission to access. Files do not move with that selection; see [Work across computers](/docs/computers/across-computers/).

## Separate unrelated work

Keep follow-up questions and revisions in the same session so the Bot can use the earlier discussion. Start a separate session for unrelated work, such as a photo collection and a financial report.

Archiving puts completed ordinary sessions aside until you need them again. Sessions bound to a platform chat use the chat's name and cannot be renamed or archived. To start fresh in a connected chat while keeping the old history, [replace its session](/docs/chat/overview/).

Copying or forking a session lets you explore another approach from existing work. The copy is independent: later messages do not synchronize, and schedules are not copied.

## Private sessions

If one Bot handles both personal work and group chats, make personal sessions private to prevent its other sessions from looking up those records.

Choose **Make private** in the sidebar session menu. You can still open and manage it through Desktop or a browser.

Other sessions cannot discover, read, or change a private session through the built-in API. This also protects its currently bound chat and schedules targeting it. The private session can access its own resources, and forks inherit its private setting. When a chat is rebound, access follows the new session relationship.

Subagents follow their original conversation’s private setting. Changing it updates existing subagents and their descendants too.

Private sessions still retain history and send task context to the selected model service. Files shared on a computer and information already copied elsewhere need separate protection. Read [Use Astral safely](/docs/start/safe-use/) before working with sensitive material.

## Long conversations

As a session grows, Astral asks the model to summarize older context while retaining recent messages. The original history remains available. Incoming chat messages and pictures use the model's context space even when the Bot does not reply. If summarization fails, existing context and pending messages remain; address the displayed error before continuing.

## Timezone

Open **Settings → General → Timezone** to choose the timezone used for displayed dates and schedules. **Follow system** uses the host running Astral. If you access it from another location, you may want to choose a timezone explicitly.

Changing this setting updates displayed times across clients without changing when past messages occurred. See [Scheduled tasks](/docs/usage/schedules/) to arrange future work.

## Manage session and chat storage

To reclaim storage, open **Settings → Data & storage**, review total usage, then select a Bot and find the relevant chat or session. Sizes marked “about” estimate content size for comparison; the overview shows actual disk use.

An ordinary session must be archived and unbound from a chat before you can **permanently delete** it. Deletion removes its history, model-call records, pending messages, recall records, and schedules. It also permanently removes all its subagents and their descendants, including subagents with independent histories. Ordinary sessions created with **Fork from here**, working files on computers, and messages on external platforms remain.

For a connected chat, Astral keeps two kinds of records: the **IM archive** contains platform chat material, while the **session** contains the Bot's task history and execution records. Choose the action that matches what you want to remove:

| Your aim | Action | Effect |
| --- | --- | --- |
| Remove older platform chat archives | **Clear IM archive** | Deletes archives before your chosen date. Messages awaiting processing stay temporarily. Session history and platform messages remain. |
| Remove old archives regularly | **Archive retention** | The default is Keep forever. Choosing 30, 90, or 180 days immediately removes eligible old archives, then checks daily. Returning to Keep forever stops expiry but cannot restore deleted content. |
| Remove the chat's current task history | **Clear session** | Stops work, permanently deletes the old session, its subagents, and its schedules, and creates a session with the same private setting. The IM archive, account settings, and platform messages remain. |

Clearing the IM archive leaves material already included in a session. Clear that session separately if needed. To begin a new topic while keeping earlier work, use **Change conversation** instead.

The list updates after cleanup; disk space may be reclaimed later. Follow any error shown for the affected action. If the connection drops, reconnect and check the result before repeating cleanup. See [Storage maintenance](/docs/hosting/maintenance/#review-and-clean-storage) for backups and application files.
