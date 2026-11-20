---
title: "Schedules and events"
description: "Run tasks on a timer or wake a session automatically from local scripts and services."
---

Beyond manual prompts, Astral supports two automatic triggers:
- **Scheduled tasks:** deliver a prompt on a timer (e.g., morning task checks or Friday summaries).
- **Event wake-ups:** let local scripts or services wake a session whenever something happens (e.g., build failures or server alerts).

:::note[Host must stay running]
The host running Astral must be awake when a trigger fires. Fully quitting Desktop stops local schedules, and missed times are skipped without catch-up runs.
:::

## Create a timed schedule

Verify **Settings → General → Timezone** first (all schedules share this timezone; **Follow system** uses the Astral host's timezone).

### Option 1: From the UI

Open **Scheduled tasks** in your Bot's sidebar and choose:
1. **Target:** **Use a selected conversation** (keeps ongoing context) or **New conversation each time** (starts clean with the Bot's current model list and default computer—include full paths and instructions in the prompt).
2. **Prompt & timing:** pick daily/weekly options, or a 5-field Cron expression (e.g., `0 9 * * *`). Example prompt:

> Read tasks.md in the working directory. List unfinished items due today and report them in this session. Leave the file unchanged; if it's missing, tell me what to provide.

Check the **Next** run time after saving.

### Option 2: Ask the Bot directly

> Every Friday at 5 pm, review this week's unfinished items in this session. Check Astral's timezone first, create the schedule, and tell me its next run time.

By default, the Bot targets the current conversation (specify explicitly to target another conversation or start a new one each time; subagents cannot be targets). Review the created entry under **Scheduled tasks** afterward.

## Wake sessions from local programs: Event wake-ups

Ask your Bot inside a normal root conversation to register an **event endpoint**:

> Connect build-failure alerts on the build computer to this conversation, and note the listener script path and startup entry in the endpoint's maintenance notes.

The endpoint appears under **Scheduled tasks → Event wake-ups**, where you can edit its name, instructions, and maintenance notes, or pause and delete it. (Its receiving Computer and target conversation are fixed after creation—create a new endpoint to change targets.)

### Send events from the receiving Computer

Open the endpoint details, check **Receiving Computer** and **Target conversation**, and copy the localhost delivery URL. Call it from a program running on that **receiving Computer**:

```bash
curl --fail-with-body '<delivery URL from endpoint details>' \
  -H 'Content-Type: application/json' \
  --data-raw '{"event_id":"build-1842","text":"The build failed. Check the logs and continue the fix."}'
```

:::caution[Localhost only — never expose publicly]
The event delivery endpoint runs on a persistent localhost port without authentication. **Never** expose it to the public internet via reverse proxy, port forwarding, tunnel, or webhook forwarder—it is strictly for local programs on that Computer.
:::

Key details:
- **Deduplication:** pass a fresh `event_id` for each new event; retries reuse the same ID and payload (receipts last 7 days—duplicate IDs return the original receipt without re-waking, conflicting payloads are rejected).
- **Structured payload & response:** you may include a JSON `data` object. A `202` response confirms durable receipt without waiting for model processing; both Computer and Server must stay connected.
- **Instructions vs. maintenance notes:** **Instructions** tell the Bot how to handle the event; **Maintenance notes** record where the caller lives (e.g., `/opt/mc/listener.py, systemd: mc-events`) and are included with every delivery so the Bot can inspect or maintain the listener.

### Batching and temporary blocking

Events arriving close together are combined in a fixed one-second window and delivered as one batch. While the Agent is working, new events become context for its next model request without rewriting an in-flight generation.

<details class="astral-doc-details">
<summary>Buffer limits, 60-second temporary blocking, and 429 retry details</summary>

The endpoints targeting one conversation share one delivery buffer holding up to 20 events or 4 KiB. A separate batch may already have been delivered to the conversation and be waiting for the model to consume it; that batch has its own 20-event/4-KiB limit. At most these two batches are retained, regardless of the number of endpoints. Each batch’s content budget includes message bodies and one copy of each source’s endpoint name, instructions and notes; fixed formatting wrappers do not count. When the delivery buffer fills, the actual messages and a traffic-protection notice are delivered together, and the source endpoint is temporarily blocked for 60 seconds. Long content is explicitly marked as truncated. Individual events retain their boundaries, endpoint and source Computer. The transcript labels the batch **Event wake-up** with its endpoint and receiving Computer; the Bot also receives the Computer API entry for managing that endpoint.

Calls made while blocked receive `429` and `Retry-After`; the caller should wait before retrying. Rejected content is not replayed as a backlog after recovery, and it does not repeatedly invoke the model. The page shows when the 60-second block ends. If the shared delivery buffer is still full—for example, because the model is unavailable and has not consumed the earlier batch—new calls continue to receive `429` until processing frees space. Astral does not accumulate an unlimited queue. An endpoint manually paused by you or the Bot remains paused. Traffic protection does not classify error text or invoke a model to summarize messages.

</details>

## Managing schedules and events

- **Sidebar markers:** conversations with active fixed schedules or event endpoints appear in the **Scheduled** group (clock for timed tasks, lightning bolt for events; active runs and active subagents move to **Now** while keeping markers). Open the conversation's three-dot menu to jump straight to editing.
- **Verify outcomes:** once marked **Delivered to conversation**, open the session to check the actual result (include explicit send instructions in the prompt if results should go to a chat app).
- **Lifecycle rules:**
  - Pausing or deleting only stops future triggers; stop already-running work inside the session.
  - Archiving a session disables its schedules and event endpoints (re-enable manually after restoring); permanently deleting a session or Bot removes its plans.
  - Changing timezone keeps one-time instants unchanged (only display updates) while recalculating repeating next-run times.
  - Plans linked to private sessions stay hidden from other sessions and preserve privacy on newly created runs.

## Troubleshooting

| Symptom | What to check |
| --- | --- |
| Wrong next run time | Check **Settings → General → Timezone** and repeat rules (browser timezone doesn't override host settings). |
| Marked **Missed** | Confirm the Astral host was running at trigger time; give one-time tasks a future time and re-enable. |
| Delivered, no output | Open the conversation to check for model errors, offline computer, or missing permissions. |
| Missing in chat app | Check that the prompt asked to send the result to chat, and inspect the send tool result. |
| Edit says task changed | Reopen the latest entry before saving changes. |
