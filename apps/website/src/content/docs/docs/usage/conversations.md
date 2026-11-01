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

Private sessions still retain history and send task context to the selected model service. Files shared on a computer and information already copied elsewhere need separate protection. Read [Use Astral safely](/docs/start/safe-use/) before working with sensitive material.

## Long conversations

As a session grows, Astral asks the model to summarize older context while retaining recent messages. The original history remains available. Incoming chat messages and pictures use the model's context space even when the Bot does not reply. If summarization fails, existing context and pending messages remain; address the displayed error before continuing.

## Timezone

Open **Settings → General → Timezone** to choose the timezone used for displayed dates and schedules. **Follow system** uses the host running Astral. If you access it from another location, you may want to choose a timezone explicitly.

Changing this setting updates displayed times across clients without changing when past messages occurred. See [Scheduled tasks](/docs/usage/schedules/) to arrange future work.

## Manage session and chat storage

To reclaim storage, open **Settings → Data & storage**, review total usage, then select a Bot and find the relevant chat or session. Sizes marked “about” estimate content size for comparison; the overview shows actual disk use.

An ordinary session must be archived and unbound from a chat before you can **permanently delete** it. Deletion removes its history, model-call records, pending messages, recall records, and schedules. Independent forks, working files on computers, and messages on external platforms remain.

For a connected chat, Astral keeps two kinds of records: the **IM archive** contains platform chat material, while the **session** contains the Bot's task history and execution records. Choose the action that matches what you want to remove:

| Your aim | Action | Effect |
| --- | --- | --- |
| Remove older platform chat archives | **Clear IM archive** | Deletes archives before your chosen date. Messages awaiting processing stay temporarily. Session history and platform messages remain. |
| Remove old archives regularly | **Archive retention** | The default is Keep forever. Choosing 30, 90, or 180 days immediately removes eligible old archives, then checks daily. Returning to Keep forever stops expiry but cannot restore deleted content. |
| Remove the chat's current task history | **Clear session** | Stops work, permanently deletes the old session and its schedules, and creates a session with the same private setting. The IM archive, account settings, and platform messages remain. |

Clearing the IM archive leaves material already included in a session. Clear that session separately if needed. To begin a new topic while keeping earlier work, use **Change conversation** instead.

The list updates after cleanup; disk space may be reclaimed later. Follow any error shown for the affected action. If the connection drops, reconnect and check the result before repeating cleanup. See [Storage maintenance](/docs/hosting/maintenance/#review-and-clean-storage) for backups and application files.
