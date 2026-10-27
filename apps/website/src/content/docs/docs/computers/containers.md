---
title: "Use containers"
description: "Give a Bot a separate, persistent execution environment."
---

A container appears as an independent computer with its own identity and filesystem. Use one when you want a task environment separate from your everyday system.

## Prepare the host

The host needs a running Linux container engine accessible through its local Docker CLI and context. The account running Astral or the enrollment command must be able to use that engine.

| Host | Container environments targeted |
| --- | --- |
| macOS | Docker Desktop, OrbStack, Colima, or Rancher Desktop using its Docker-compatible backend |
| Linux | Docker Engine, including rootless Docker, or Docker Desktop |
| Windows | Docker Desktop in Linux-container mode |

Astral and the Windows connection command run natively; you do not need to enter WSL to use them. The runtime manages its own Linux environment. A local Docker context must be selected; the local-container shortcut does not create containers through a remote Docker context.

This is the compatibility scope, not a claim that every provider, version and CPU combination has been tested. Check the release's acceptance evidence for the environments actually exercised.

“Use a local container” uses the container runtime on the **Server machine**, not on the device displaying the browser interface.

## Create a container computer

- On the Server machine: open **Settings → Computers → Add Computer → Use a local container**.
- On another machine: choose “Connect another machine,” enable “Use a container,” and run the generated command on that host.

Astral downloads the official prebuilt Ubuntu environment selected by your application version, checks the container's route to the Server, creates the persistent workspace and container, copies the matching Linux Daemon into it, then starts it and waits for the computer to connect. Follow these stages and their logs in the dialog. The first image download can take time; Docker reuses the cached image for later creations.

Your computer does not build the image or install its toolchain from scratch. If preparation fails, check that the runtime is running, your account can access it, and the official image registry and release downloads are reachable.

After it comes online, grant your Bot access and select it in a session.

## Environment and persistence

The Ubuntu 24.04 environment includes common development, data, document, media, OCR and browser tools, with Chinese and emoji fonts. Your Bot can install additional software. Host project files do not appear inside automatically; copy or mount them explicitly.

The environment image and Astral program have separate lifecycles. Each application version selects an exact image digest. The image is rebuilt only when its Dockerfile or other build inputs change; an application release can reuse the same environment. Astral copies its program into the container's writable layer before starting it, outside the persistent work-data directory. It does not add a host-mounted program directory. The existing data-volume and read-only pairing-configuration mounts remain.

“Update Daemon” downloads and verifies the official program, then restarts the Daemon inside the same container. The container ID, writable layer, files and installed software remain; running tasks stop. The same Astral binary supervises the update, with no Docker socket access. This does not upgrade the base image. The fallback update command runs on the Docker host and restarts the existing container.

Updating Desktop or Server does not recreate existing container computers or replace their environment. A different environment image applies to newly created containers; keep the original container and its work when preparing another environment.

Disconnecting a computer or revoking access does not delete its container or volumes. Back up work before removing those resources. Containers do not replace careful permission choices.
