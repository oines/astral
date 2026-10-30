---
title: "Schedule tasks"
description: "Deliver a prompt to a session once or on a repeating schedule."
---

Open **Schedules** in the sidebar to manage the current Bot's plans. You can add a task yourself or ask the Bot to create one through its Gateway API. Both appear in the same list and can be edited, paused, resumed, or deleted.

## Create a plan

Choose a title, how to run the task, and the prompt to deliver. **Use a selected session** continues an existing conversation. **Create a new session each time** starts an independent conversation for every occurrence, using the Bot's current default model and computer, without copying earlier history or an IM binding. You can create this kind of plan before any sessions exist; its last created session is linked from the task list.

Select a one-time date and time, or repeat daily, weekly, or with a custom five-field Cron expression. All plans use Astral's global timezone in **Settings → General → Timezone**. The default follows the operating system where Astral is installed, not the browser or the selected computer. You can choose an IANA timezone such as `Asia/Singapore`; there is no separate timezone for each plan.

The date and time you enter for a one-time plan are interpreted in that global timezone. Once saved, the plan keeps the same execution instant if you change the timezone; its displayed time changes. Repeating plans follow the new timezone and calculate their next future occurrence.

An existing target must belong to the same Bot. It uses that session's current context, model, computer, and permissions when the prompt arrives. A session can have several plans. Forking a session does not copy its plans.

Plans targeting a private session are hidden from other sessions' Gateway calls. A plan created by a private session in **Create a new session each time** mode is also private to that source; its new sessions inherit the source's private setting at execution time. You can manage all plans in the UI.

## When the time arrives

An idle session starts working. A busy session receives the prompt through **Steer** at its next input boundary. The prompt appears on the right with a clock, **Scheduled task**, and the task title, so you can distinguish it from a message you typed.

**Delivered** means the session accepted the prompt; it does not mean the Bot finished the work. In a session bound to IM, the Bot still needs to send a message explicitly to reply to the external chat.

Astral must remain running on the host that manages the plans. Closing the Desktop App completely stops its local scheduler. Missed times are skipped: a one-time task becomes **Missed**, and a repeating task resumes at its next future time. There is no catch-up run.

New scheduled messages use the current global timezone in their model-visible timestamps. Changing the setting does not rewrite earlier context, saved summaries, or frozen requests; an in-flight retry keeps its original request.

## Manage plans

An enabled plan with a next execution time and a fixed target puts that session in the sidebar's **Scheduled** group. A plan that creates a new session each time stays in the Schedules page; the created sessions appear in **In progress** or **Recent**. Each session appears only once. Paused and ended tasks remain in the Schedules page.

Pausing or deleting stops future deliveries. Inputs already accepted by a session stay in its queue. To repeat an expired one-time plan, set a new future time before enabling it. Archiving an ordinary session does not pause its tasks; permanently deleting a session or Bot removes its plans.

## Let the Bot manage schedules

The Bot discovers the `schedules` capability through its authenticated Computer Gateway. It can create plans for itself or another session of the same Bot. A daily plan uses a request such as:

```json
{
  "session_id": "<session-id>",
  "session_mode": "existing",
  "title": "Daily project review",
  "prompt": "Review today's project updates and report anything that needs attention.",
  "enabled": true,
  "timing": {
    "kind": "cron",
    "cron": "0 9 * * *"
  }
}
```

This Cron runs at 09:00 in Astral's global timezone. For a one-time API request, use `timing.kind: "once"` and an absolute RFC3339 `timing.at` with an explicit offset or `Z`. The API has no `timing.timezone` field.

Use `session_mode: "new"` and omit `session_id` to create an independent session for each occurrence. `last_session_id` identifies its most recent actual session. The API provides listing, reading, creation, updates, and deletion under `/v1/schedules`. Updates submit the full editable configuration with its current revision; deletion also requires the revision. A conflict means the plan changed and must be read again before editing. A failed connection during creation may leave the result uncertain; check the task list before creating another copy.
