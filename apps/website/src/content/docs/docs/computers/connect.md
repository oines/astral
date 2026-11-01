---
title: "Connect a computer"
description: "Choose where a Bot works, connect the machine, and grant access."
---

A Bot needs a computer to read files, write files, and run commands. It can use the machine running Astral or another device. After connecting a computer, choose which Bot may use it.

## Use the server machine

To work with files on the Astral host, choose **Use the server machine** under **Settings → Computers → Add Computer**. With Desktop, this is normally the machine running the app. In a browser connected to a standalone Server, it means the Server host.

For example, opening an office Server's web interface on a laptop does not make the laptop the local computer. Connect the laptop separately or upload the files you need from it.

Local operations use the operating-system account running Astral Server. Once the computer is online, continue to [granting access and trying a task](#grant-a-bot-access). For a separate Linux environment, use a [container computer](/docs/computers/containers/).

## Connect another machine

Generate a command under **Settings → Computers → Add Computer → Connect another machine**, then run it in a terminal on **the machine you want to connect**. Use Shell on macOS or Linux, or PowerShell on Windows.

Before generating it, choose:

- **Server address:** the target machine must be able to reach this Astral Server. Use a reachable LAN address on the same network or a VPN address over your VPN. `127.0.0.1` refers to the machine itself and cannot reach a Server on a different host.
- **Operating system:** choose the system running the command. For a container, this is the container host's system.

Run the **complete command** as the system user whose file permissions you want the Bot to have. It installs a background service and the `astral` command. Return to Astral and wait for the computer to appear online. Terminal errors identify any failed connection or installation step.

The command contains temporary pairing credentials. Run it only on the intended computer and generate another if it expires. The target must be able to download matching official release files. An unpublished development version needs separately prepared programs; see [Build from source](/docs/hosting/build-from-source/).

Computers connect outward to the Server. HTTP and HTTPS enrollment addresses are supported; the computer channel encrypts its traffic and verifies the Server's identity. Browser sign-in has separate network requirements described in [Run a Server](/docs/hosting/server/).

## Grant a Bot access

Once online, allow the computer in **Settings → Bots → your Bot → Computer access**, optionally making it the default. Return to a session for that Bot, select the computer, and try a read-only task:

> Tell me the current computer's name and working directory, then list the files in that directory without changing anything.

Check that the reported location is the one you intended. This completes the path from connection to usable access. The default applies to new sessions; existing sessions keep their selections.

Granting access lets the Bot operate with the computer service account's file and command permissions, beyond its default working folder. Read the [access guidance](/docs/start/safe-use/) before exposing that environment to a public group or untrusted material.

## The computer is not usable yet

| What you see | What to check |
| --- | --- |
| Astral keeps waiting for the computer | Read the target terminal's error and check that it can reach the Server address used in the command. |
| Installation fails during download | Check access to official release files. Use the fallback below if GitHub is unreachable. |
| The script reports an existing `astral` command | The installer does not overwrite another command. If you manually added an older `astral.exe` directory to PATH, remove that directory from PATH, open a new terminal, and rerun the enrollment command. Do not delete programs or data used by a running service. |
| The computer is online but absent from a session's choices | Check that Bot's Computer access. Connecting alone does not grant permission. |
| It connects but cannot read a file | Check that the file is on this computer and readable by the background service's system user. |
| A previously working computer is offline | Check that both machines are running, then inspect the target's background service. |

## When the target Computer cannot reach GitHub

Select the public download proxy in the target terminal before pasting the full connection or update command from Astral. Keep its Server address and pairing parameters intact.

macOS / Linux:

```sh
export ASTRAL_GITHUB_PROXY=https://gh-proxy.org/
```

Windows PowerShell:

```powershell
$env:ASTRAL_GITHUB_PROXY = 'https://gh-proxy.org/'
```

This third-party service handles installation-script and GitHub release downloads. The computer's connection to Astral Server does not pass through it. See the [download proxy guide](/docs/hosting/server/#when-github-is-unreachable). Clear the selection for the current terminal with `unset ASTRAL_GITHUB_PROXY` in Shell or `$env:ASTRAL_GITHUB_PROXY = ''` in PowerShell.

## Update a connected Daemon

The Daemon is the Astral service running on a connected computer. For a remote computer, use **Daemon maintenance → Update Daemon** in its details. The server-machine Daemon updates with Desktop or Server.

Updating interrupts running tasks, so finish or stop important work first. Astral downloads, verifies, and restarts the service. Check its version after it reconnects. Computer identity, credentials, and working files remain. If the Daemon is newer than the Server, you can also arrange a Desktop or Server update.

For an offline computer, an unsupported managed update, or a failed update, use the command provided in the same section. Run a native computer's command on that computer, and a container's command on its host. Native updates keep the original enrollment user's permissions.

## Background services

The remote background service belongs to the system user who ran the enrollment command: a user LaunchAgent on macOS, user systemd on Linux, or a scheduled task on Windows. Choosing another user changes which files the Bot can access.

An ordinary Linux account needs working user services before background enrollment. Enrollment does not elevate it or automatically configure lingering to keep it running after logout. To enroll as root, log in as root or run `sudo -i` before pasting the full command, avoiding another account's environment. The Bot's commands will then have root permissions.

Root background enrollment prepares user services and enables lingering so they can run without a login session. A failed preparation stops before pairing. Lingering affects the whole account; uninstalling Astral does not disable it or stop the whole user manager. Foreground and container enrollment skip this preparation.

## Disconnect and stop a computer

Stop affected sessions and check for remaining work, then choose the action that matches your aim:

- To remove one Bot's access, revoke permission in that Bot's Computer access settings.
- To disconnect the computer from all Bots, choose **Revoke connection** in its details. You can then delete its Astral registration.
- To stop the background service, run these commands on the target as the user who enrolled it.

```sh
astral computer service stop
astral computer service status
```

Use `astral computer service start` when you want it running again. For longer-term removal, uninstall the service:

```sh
astral computer service uninstall
```

If several instances are installed, select the intended one. For unattended commands, add `--root /path/to/computer` to specify its data directory. Copy the path from Computer details if an older custom directory is not discovered.

Revoking access blocks subsequent operations; it does not undo completed file changes. Deleting the registration or uninstalling the service keeps working files. Deleting a registration does not uninstall the service or remove containers and volumes. Stop containers through the host's container manager and back up work before deletion. Desktop manages its local service with the app and its background-running settings.
