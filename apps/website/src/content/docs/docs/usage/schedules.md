---
title: "Scheduled tasks"
description: "Run work on a schedule or resume it from a local program’s events."
---

A scheduled task gives the Bot instructions at a chosen time. Use it for work such as a morning task review or a Friday summary. Create it in the interface or ask the Bot to arrange it in a session. Both methods use the same task list.

The host running Astral must be running when the task is due. Fully quitting Desktop stops its local scheduler. Missed times are skipped, with no catch-up runs.

## Create a plan

First check **Settings → General → Timezone**. All schedules use this timezone. **Follow system** means the Astral host's timezone, which may differ from your browser or the computer performing the task.

Open **Scheduled tasks** for the current Bot. The main choice is whether to **Use a selected conversation** to continue earlier work or start a **New conversation each time** for independent runs.

The **Prompt** is the instruction delivered when the task is due. Specify the source material, work, and destination. For a Bot with access to your work computer, an example is:

> Read tasks.md in the working directory. List unfinished items due today and report them in this session. Keep the file unchanged. If it is missing, tell me what I need to provide.

After creating the task, check its **Next** time in the list. One-time tasks need a future time; ordinary repeating work can use the daily or weekly options.

A new conversation uses the Bot's model priority list and default computer at the time of the run. It does not copy earlier history or a messaging-platform binding. Include the paths and requirements it needs instead of relying on earlier discussion. You can create this kind of schedule before any sessions exist.

## Let the Bot manage schedules

You can also describe the arrangement in a session:

> Every Friday at 5 pm, review this week's unfinished items in this session. Check Astral's timezone first, then create the schedule and tell me its next run time.

When you ask the Bot to create a schedule, it uses the current conversation by default. Ask explicitly to use another conversation belonging to the same Bot or to start a new one each time. Subagent conversations cannot be schedule targets.

Open **Scheduled tasks** afterward to confirm the timing and prompt, then edit or pause it if needed. If creation loses its connection or has an uncertain result, check the list before creating another copy.

## Event wake-ups

Ask the Bot to register an event endpoint when an external program should hand work back to it. For example:

> Connect the Minecraft server messages on the computer running the server and deliver them to this conversation. Briefly record the listener’s script or configuration path and startup entry in the endpoint’s maintenance notes.

The Bot creates the endpoint, which then appears under **Scheduled tasks → Event wake-ups**. You can inspect it, edit its name, handling instructions and maintenance notes, pause it, or delete it. **Add task** continues to create timed tasks. Only user-facing root conversations can register and manage endpoints; subagents cannot use these APIs or be delivery targets.

If the Bot omits a target, the endpoint uses its current conversation. It can explicitly choose another accessible root conversation belonging to the same Bot. The target conversation and receiving Computer are fixed after creation; switching conversations or computers does not move them. To change either target, ask the Bot to create a new endpoint, update the caller, and disable the old endpoint.

### Connect a local program

Open the endpoint, check its **Receiving Computer** and **Target conversation**, then copy its delivery URL or request example. The external program must call that localhost address on the receiving Computer. Other Computers can manage the endpoint but cannot deliver events to it. Delivery is anonymous on this local entry point. The Daemon uses one persistent local port that remains the same after a restart. Do not expose this entry point to the public internet through a reverse proxy, port mapping, tunnel, or webhook forwarding service; it is only for programs on its owning Computer. Ordinary Computer API management and discovery still require the current command credential.

On the receiving Computer, replace the URL below with the one shown in the endpoint details:

```bash
curl --fail-with-body '<delivery URL from the endpoint details>' \
  -H 'Content-Type: application/json' \
  --data-raw '{"event_id":"build-1842","text":"The build failed. Check the logs and continue the fix."}'
```

Use a new `event_id` for each new event. Retries keep the original ID and content. Deduplication receipts are kept for seven days: during that time, the same ID returns its original receipt without waking the Bot again, and conflicting content is rejected. After the content enters the conversation, the receipt no longer keeps a copy of it. You can also attach structured `data`. A `202` response means the event was durably received, without waiting for model processing. The Computer and Server must remain connected to accept new deliveries.

**Instructions** describe how to handle these events. **Maintenance notes** locate the caller, for example `/opt/mc/listener.py, systemd: mc-events`. The system supplies the source Computer, so it does not need to be repeated in the notes. Each delivery includes the notes saved when its events were received, separately from the handling instructions, so the Bot can locate the listener. Editing notes affects later events only.

### Batching and temporary blocking

Events are combined in a fixed one-second window and delivered as one batch. While the Agent is working, new events become context for its next model request; an in-flight generation is not rewritten.

The endpoints targeting one conversation share one delivery buffer holding up to 20 events or 4 KiB. A separate batch may already have been delivered to the conversation and be waiting for the model to consume it; that batch has its own 20-event/4-KiB limit. At most these two batches are retained, regardless of the number of endpoints. Each batch’s content budget includes message bodies and one copy of each source’s endpoint name, instructions and notes; fixed formatting wrappers do not count. When the delivery buffer fills, the actual messages and a traffic-protection notice are delivered together, and the source endpoint is temporarily blocked for 60 seconds. Long content is explicitly marked as truncated. Individual events retain their boundaries, endpoint and source Computer. The transcript labels the batch **Event wake-up** with its endpoint and receiving Computer; the Bot also receives the Computer API entry for managing that endpoint.

Calls made while blocked receive `429` and `Retry-After`; the caller should wait before retrying. Rejected content is not replayed as a backlog after recovery, and it does not repeatedly invoke the model. The page shows when the 60-second block ends. If the shared delivery buffer is still full—for example, because the model is unavailable and has not consumed the earlier batch—new calls continue to receive `429` until processing frees space. Astral does not accumulate an unlimited queue. An endpoint manually paused by you or the Bot remains paused. Traffic protection does not classify error text or invoke a model to summarize messages.

## When the time arrives

A selected conversation uses its current model, computer, permissions, and history when the task arrives. An idle session starts work; a busy session receives it as a Steer instruction. The message shows a clock, the **Scheduled task** label, and its name.

When the list says **Delivered to conversation**, open that conversation to inspect the actual work. For a new-conversation schedule, use **Latest conversation** on the task. If results should go to an external chat, include that sending instruction and check that they arrive there.

An overdue one-time task becomes **Missed**. A repeating task waits for its next future time rather than replaying runs missed while Astral was stopped.

## Manage plans

Edit, pause, resume, or delete plans in the task list. Pausing or deleting prevents future deliveries. A prompt already received by a session must be handled or stopped in that session.

An enabled task targeting an existing conversation places that conversation in the sidebar's **Scheduled** group while a next run time exists. New-conversation schedules remain in the task page; sessions they create appear under in-progress or recent work.

Archiving an ordinary session keeps its schedules active. Permanently deleting a session or Bot removes its plans. Copying or forking a session does not copy them. Give an expired one-time task a future time before enabling it again.

### Timezone changes and custom timing

A saved one-time task keeps its actual execution instant when you change timezone; only its displayed time changes. Repeating plans recalculate the next time using the new timezone. Review the **Next** times after changing this setting.

For more complex timing, use a five-field Cron expression: minute, hour, day of month, month, and day of week. For example, `0 9 * * *` runs daily at 09:00 in Astral's global timezone. Use the ordinary daily or weekly controls when those fit your needs.

### Plans involving private sessions

Plans targeting a private session are hidden from other sessions. A new-conversation plan created by a private session is also protected by that source session; each new conversation inherits its source's private setting at execution time. You can still manage these plans in the interface.

## A task did not run as expected

- **The next time is wrong:** check Astral's global timezone and the repeat rule. Your browser timezone does not override them.
- **The task is marked Missed:** check whether the Astral host was running when it was due. Give a one-time task a new future time and enable it again.
- **It was delivered, but there is no result:** open the conversation and check for a model error, offline computer, or missing permission.
- **The result is in Astral but absent from the chat platform:** check the task's sending instructions and the actual send result.
- **An edit reports that the task changed:** reopen the current task before applying the change.
