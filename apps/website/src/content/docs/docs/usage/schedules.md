---
title: "Scheduled tasks"
description: "Start work at a chosen time or repeat it on a daily or weekly schedule."
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

The Bot can manage schedules for sessions belonging to the same Bot. Open **Scheduled tasks** afterward to confirm the timing and prompt, then edit or pause it if needed. If creation loses its connection or has an uncertain result, check the list before creating another copy.

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
