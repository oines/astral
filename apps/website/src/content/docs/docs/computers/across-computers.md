---
title: "Work across computers"
description: "Share a task across machines while keeping track of files and execution locations."
---

One session can use several computers while retaining the discussion and execution history. This is useful when a task needs different environments, such as processing data on a server and preparing a report on an office computer.

Prepare the files and tools on each machine. Changing the selected computer does not synchronize them.

## Assign work to machines

First [connect the computers and grant the Bot access](/docs/computers/connect/). Check that they are online and give them recognizable names, such as “Office computer” and “Data server.” Then describe the division of work:

> Analyze /home/mei/data/sales.csv on the data server and create this month's sales summary. Transfer the summary to the office computer's report folder and make charts there. Check the transfer method before switching; do not assume both machines have the same directories.

You can select the current computer while the session is idle. The Bot can also switch between allowed computers during a task. Ask it to identify the machine and full path for an operation, and check its execution records when you need confirmation.

## What stays with the session

Messages, task instructions, and execution records remain in the session. Existing file links still point to their original computers. A server-generated file does not move when you select the office computer.

A process already started on the previous machine stays there too. Switching does not stop or migrate it; it may run until completion, until stopped, or until its connection ends. Keep track of unfinished work on the earlier computer.

## What does not move automatically

Project files, installed software, environment variables, sign-ins, and running processes do not migrate with the session. Ask the Bot explicitly to transfer required material using an available method; code projects can also use an existing Git repository. Confirm the destination and access before transferring files.

Identical paths on different computers can hold entirely different content. Before the first operation on a new machine, check that the files and required tools exist. Changing a working directory does not move output from earlier work either.

History remains readable if a source computer goes offline. Reading its original files or controlling processes requires that connection again. See [Files and results](/docs/usage/files/) for downloading and keeping important output.
