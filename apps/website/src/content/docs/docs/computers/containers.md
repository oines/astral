---
title: "Use containers"
description: "Give a Bot a separate Linux environment with persistent files and installed tools."
---

A container gives a Bot a separate place to work. It appears as another computer in Astral, with its own files and software. For a public group, you can use a dedicated Bot with access only to that container rather than your everyday computer.

If you want the Bot to use files already on a machine, you can [connect that machine directly](/docs/computers/connect/). Host files do not automatically appear inside a container: upload, copy, or explicitly mount them. The host is the computer running the container. Plan permissions as well as the environment; see [public-group guidance](/docs/start/safe-use/#group-chats-use-a-dedicated-bot).

## Create a container computer

If Docker Desktop, OrbStack, or another compatible runtime is already installed and running, open **Settings → Computers → Add Computer**:

- Choose **Use a local container** to create it on the Astral Server host, including the Server bundled with Desktop. This is not necessarily the device displaying the browser.
- Choose **Connect another machine**, enable **Run in a container**, and run the generated command on that machine to create it elsewhere.

Leave **Enable Astral API** off if the container only needs to run commands and work with its files. The default is on for a local container and off for one on another machine. See [what the switch controls](/docs/computers/connect/#control-what-the-computer-can-call).

Astral downloads a prepared Ubuntu environment and starts the container. The first download can take time; the dialog or target terminal shows progress and errors. Later creations can reuse the downloaded image.

**Use a local container** connects to the Server through the container runtime's host access, rather than through your public web address or reverse proxy. Astral tests that route from the actual container environment and checks the Server's identity before using it. If local access fails, creation reports that problem instead of falling back to the public address. This applies to new local containers; existing container connections are not changed automatically. The other-machine command continues to use the Server address you selected for that machine.

Once online, grant access in the Bot's **Computer access** settings and select the container in a session. Try a small task to confirm the environment:

> Create container-trial.md on the current computer with today's date. Give me a file link and its directory. Leave other files unchanged.

Open the result, then use [Files and results](/docs/usage/files/) to provide your own material. The Ubuntu 24.04 environment includes common development, data, document, media, OCR, and browser tools, plus Chinese and emoji fonts. The Bot can install additional software as needed.

## Prepare the host

If you do not have a runtime yet, install and start an appropriate one on **the computer that will run the container**:

| Host | Runtime choices |
| --- | --- |
| macOS | Docker Desktop, OrbStack, Colima, or Rancher Desktop with a Docker-compatible backend |
| Linux | Docker Engine, including Rootless Docker, or Docker Desktop |
| Windows | Docker Desktop in Linux-container mode |

Astral uses the local Docker command to access the runtime. The system account running Astral or the enrollment command must have access. Select a local Docker context, which is a connection configuration for the container engine. The local-container shortcut does not create containers through a remote Docker context.

On Windows, use Astral and the PowerShell connection command directly; you do not need to enter WSL. The container runtime manages its own Linux environment.

Opening Add Computer checks Docker on the Server host. After fixing an environment problem, select **Check again**. A successful check enables creation; the check itself does not download an image or create a container.

## Container creation fails

| Message or symptom | Next step |
| --- | --- |
| Docker command missing | Install the runtime on the container host and ensure the Astral account can find its Docker command. |
| Engine unreachable | Start Docker Desktop or the relevant service and check that a local engine is selected. |
| Permission denied | Identify the Server's actual system account in the error. For a Linux Server, use the instructions below. |
| Image or program download fails | Check access to the official image registry and release downloads from that machine. |
| The container cannot reach the local Server | Check the Server's listening address and local firewall, and whether the container runtime can reach the Server host's port. A public web address does not replace this local route. |
| Docker works on the browser device but the option is unavailable | The check runs on the Server host. Use the other-machine container command to create one on the browser device. |

Expand **Error details** for other Docker diagnostics. You can still connect a native computer while the container environment is unavailable.

### Docker access for a Linux Server

Use this section for a Linux Server whose installed Docker environment is not accessible to Astral. Astral uses existing Docker; it does not install the engine, start its system service, or add users to groups. If Docker is not installed, follow the [official installation guide](https://docs.docker.com/engine/install/) first.

Sign in as **the operating-system account running Astral Server** and check:

```sh
id -un
id -nG
docker context show
docker info
```

The key result is that `docker info` works without sudo. This system account is separate from your Astral web username. A successful test under a different administrator account does not establish Server access.

If only `sudo docker info` succeeds, an administrator can grant docker-group access or choose Rootless Docker below. **The docker group gives host root-level privileges.** If that scope is acceptable, replace `SERVER_USER` with the actual Server account:

```sh
sudo usermod -aG docker SERVER_USER
```

Follow [Docker's post-installation guidance](https://docs.docker.com/engine/install/linux-postinstall/) to log in again after the group change. Running background processes may retain their old permissions: `newgrp docker` affects a new shell, and restarting Astral alone can still inherit the old groups.

If the account's systemd user manager was already running, have an administrator restart its `user@UID.service` during maintenance, or reboot the host. `UID` is the number returned by `id -u`. This interrupts the account's other services and tasks, so arrange downtime first. Check `docker info` again, start Astral, and select **Check again** in the interface.

### Rootless Docker

To avoid docker-group access, follow the [Rootless Docker guide](https://docs.docker.com/engine/security/rootless/) under the Server's system account, including its host prerequisites. Then select the local engine and enable its user service:

```sh
docker context use rootless
systemctl --user enable --now docker.service
docker info
```

Replace `rootless` if your context has another name. Check that `docker info` includes `rootless` under security options. Test as the Server user, which owns the configuration and service.

Lingering is needed to keep this user's services running after SSH logout and at boot. The official Astral Server installer prepares it; for a custom deployment, an administrator can run `loginctl enable-linger SERVER_USER`. The background Server must use the same Docker context as your terminal. Pass any explicit `DOCKER_HOST` or `DOCKER_CONTEXT` settings to that service too.

## Environment and persistence

The container keeps working files and installed software between uses. Back up important files separately, especially before removing a container or data volume.

**Update Daemon** updates Astral's program inside the container and restarts the service, interrupting running tasks. It preserves the container, files, and installed software without upgrading the Ubuntu base environment. Run the fallback update command on the Docker host; it restarts the existing container.

Updating Desktop or Server does not recreate existing containers either. The environment image selected by an application version is used for new containers. Create another container and transfer your work carefully when you need a new base environment.

Revoking a Bot's access, disconnecting a computer, or deleting its Astral registration keeps the host's container and volumes. Remove those through the host's container manager when needed, after confirming that important files are backed up.
