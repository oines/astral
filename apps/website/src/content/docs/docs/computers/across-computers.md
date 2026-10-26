---
title: "Work across computers"
description: "Use several computers in one session while keeping the task context."
---

The session holds task context; computers provide execution environments. Earlier messages and execution records stay in the same session when the computer changes.

## Assign work to machines

Grant the Bot access to the required computers and check that they are online. Use recognizable names such as “Development Mac,” “Linux test server,” and “Windows build machine.”

Then describe the task in one session:

> Read this project on the development Mac, run tests on the Linux server, and create the Windows build on the Windows machine. Tell me first if code or dependencies are missing.

You can select the session's current computer. The Bot can also switch between computers it is authorized to use. Check the computer attached to each execution to understand where the work happened.

## What stays with the session

- Existing messages, task context, and execution records.
- Generated file links and their original source computer.
- Processes already started on the previous machine, until they finish, are stopped, or the machine goes offline.

## What does not move automatically

Project files, dependencies, environment variables, sign-ins, and running processes are not copied. Prepare the code and tools on the next machine, or explicitly ask the Bot to use Git or an available file-transfer method.

Identical paths can refer to entirely different directories on different computers. Changing a working directory affects later operations; it does not move earlier output.

History remains readable while a source computer is offline, but live file access and process control require that computer to reconnect.
