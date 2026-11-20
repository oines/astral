---
title: "Containers"
description: "Run a Bot inside an isolated Linux Docker container with persistent files and tools."
---

A **container computer** appears in Astral just like another Linux machine—with its own filesystem and preinstalled tools, separated from your everyday computer. It is the recommended environment for a **dedicated public-group Bot** (see [Permissions and privacy](/docs/start/safe-use/#group-chats-use-a-dedicated-bot)).

If you want to work directly on existing files on a computer, [connect that computer](/docs/computers/connect/) instead. Host files are not visible inside a container unless uploaded, copied, or explicitly mounted.

## 1. Prepare a Docker runtime

Install and start a Docker-compatible runtime on **the machine that will host the container**:

| Host OS | Supported runtimes |
| --- | --- |
| **macOS** | Docker Desktop, OrbStack, Colima, or Rancher Desktop (Docker backend) |
| **Linux** | Docker Engine (including Rootless Docker) or Docker Desktop |
| **Windows** | Docker Desktop in Linux-container mode (run commands in PowerShell; no need to enter WSL) |

The OS account running Astral (or the enrollment command) must be able to run local `docker` commands against a local Docker context.

## 2. Create and verify the container

Go to **Settings → Computers → Add Computer** (Astral checks Docker on the Server host automatically; click **Check again** after fixing issues):

- **On the Server host:** choose **Use a local container** (meaning the Server machine, not necessarily your browser device).
- **On another machine:** choose **Connect another machine**, enable **Run in a container**, and run the generated command on that host.
- **Astral API toggle:** turn off [**Enable Astral API**](/docs/computers/connect/#control-what-the-computer-can-call) if the container only needs commands and file edits (defaults to on locally, off remotely).

The first creation downloads an Ubuntu 24.04 image preloaded with common dev, data, document, media, OCR, and browser tools plus CJK/emoji fonts (reused for future containers). Once online, grant access in Bot **Computer access** and verify:

> Create container-trial.md on the current computer with today's date, give me a file link, and tell me its folder.

:::note[Persistence and updates]
Containers retain working files and software installed inside them. Updating the Daemon, Desktop, or Server never recreates existing containers or wipes their files—still, back up important data before removing containers or volumes. Revoking access or deleting the Astral registration leaves the container on its host; remove it via your host's container manager when done.
:::

## Troubleshooting

| Symptom | Next step |
| --- | --- |
| Docker command missing | Install a runtime on the container host and verify the Astral OS user has `docker` in PATH. |
| Engine unreachable | Start Docker Desktop or the Docker service and select a local context. |
| Permission denied | Check which OS account runs Server; for Linux, see the collapsible steps below. |
| Image or download failure | Confirm the host can reach the image registry and GitHub releases. |
| Container cannot reach local Server | Local containers connect over the host's local container route (never falling back to a public proxy); check Server bind address and host firewall. |
| Docker works on my laptop, but option is disabled | The check runs on the Server host—use **Connect another machine** with **Run in a container** to run it on your laptop. |

<details class="astral-doc-details">
<summary>Linux Server Docker permissions and Rootless Docker setup</summary>

If Docker is installed per [Docker's official docs](https://docs.docker.com/engine/install/) on a Linux Server but Astral reports permission errors, sign in as **the exact OS account running Astral Server** and verify `docker info` succeeds without `sudo`:

```sh
id -un && id -nG && docker context show && docker info
```

**Option A: Grant `docker` group access** (note: `docker` group grants host root-equivalent access):

```sh
sudo usermod -aG docker SERVER_USER
```

Log in again so group changes apply; if that user's systemd user manager is already running, restart `user@UID.service` (find `UID` via `id -u`) or reboot during maintenance, then retest `docker info` and click **Check again**.

**Option B: Use Rootless Docker (least privilege):** install under the Server OS account per the [Rootless Docker guide](https://docs.docker.com/engine/security/rootless/), then run:

```sh
docker context use rootless
systemctl --user enable --now docker.service
docker info
```

Verify `rootless` appears in security options and lingering is enabled (`loginctl enable-linger SERVER_USER`). Pass any custom `DOCKER_HOST` or `DOCKER_CONTEXT` to the background Server service as well.

</details>
