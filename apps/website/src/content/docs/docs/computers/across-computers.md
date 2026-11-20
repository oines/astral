---
title: "Work across computers"
description: "Span one session across multiple machines while keeping track of where files and processes live."
---

One session can work across several authorized computers—for example, crunching data on a Linux server and then generating charts on your office machine—with the entire discussion and execution history in one place.

## Divide work across machines

First [connect the computers and authorize the same Bot](/docs/computers/connect/). Give them clear names (like “Data server” and “Office computer”), then state who does what:

> Analyze `/home/mei/data/sales.csv` on the data server to build this month's sales summary, then transfer the summary to the office computer's report folder and create charts there. Verify the transfer method first—don't assume matching paths.

Pick a computer from the input bar while idle, or let the Bot switch between authorized machines during a task. Ask it to report the machine name and full path—or expand the execution records—whenever you want to verify.

## What stays vs. what doesn't move

| Item | Behavior when switching computers |
| --- | --- |
| **Messages & tool history** | Stay together in the same session with full context. |
| **Existing file links** | Continue pointing to the computer that created them. |
| **Running processes** | Stay running on the machine that started them (switching never stops or migrates them). |
| **Files, installed tools, env vars, logins** | Stay on their own machine—**never synced automatically**. Ask the Bot to copy files explicitly or use Git. |

:::note[Same path ≠ same file]
Identical paths on two machines can hold completely different files; verify files and tools exist before running work on a new machine. If a source computer goes offline, history stays readable, while opening its files or managing processes requires it to reconnect. See [Files and results](/docs/usage/files/) to download important outputs.
:::
