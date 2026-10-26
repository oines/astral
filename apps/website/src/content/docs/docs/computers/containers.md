---
title: "Use containers"
description: "Give a Bot a separate, persistent execution environment."
---

A container appears as an independent computer with its own identity and filesystem. Use one when you want a task environment separate from your everyday system.

## Prepare the host

The host needs a running Docker-compatible environment: for example, Docker Desktop or OrbStack on macOS, Docker Desktop in Linux-container mode on Windows, or Docker Engine on Linux. The user running Astral or the enrollment command must be able to access it.

“Use a local container” uses the container runtime on the **Server machine**, not on the device displaying the browser interface.

## Create a container computer

- On the Server machine: open **Settings → Computers → Add Computer → Use a local container**.
- On another machine: choose “Connect another machine,” enable “Use a container,” and run the generated command on that host.

Initial preparation can take time. Follow the displayed stages and logs. If it fails, check that the runtime is running, your account can access it, and required resources are reachable.

After it comes online, grant your Bot access and select it in a session.

## Environment and persistence

The current Ubuntu-based environment includes common development, document, and media tools. Your Bot can install additional software. Host project files do not appear inside automatically; copy or mount them explicitly.

“Update Daemon” updates Astral's computer service and restarts the same container. Files and installed software remain, but running processes stop. This does not upgrade the base image.

Disconnecting a computer or revoking access does not delete its container or volumes. Back up work before removing those resources. Containers do not replace careful permission choices.
