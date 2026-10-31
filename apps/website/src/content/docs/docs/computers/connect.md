---
title: "Connect a computer"
description: "Connect the server machine or another device, then grant a Bot access."
---

Manage connections in **Settings → Computers**, then choose which Bot may use each machine in that Bot's **Computer access** settings.

## Use the server machine

Choose the server-machine option under Add Computer. It uses the machine and operating-system account running Astral Server. With Desktop, this is normally the device running Desktop.

Once online, its details show the environment, version, and data directory.

## Connect another machine

1. Open **Settings → Computers → Add Computer → Connect another machine**.
2. Select a Server address reachable from that machine and the target operating system.
3. Generate the connection command and run it in the target's Shell or PowerShell.
4. The command checks connectivity and installs a background service. Wait for the computer to appear online in Astral.

The command contains temporary pairing credentials. Use it only on the intended machine and generate a new command if it expires. The installer needs matching official release assets. For an unpublished development version, prepare the target computer’s program yourself.

The remote computer connects outward to the Server. `127.0.0.1` refers to the same machine; a LAN, VPN, or public address must actually be reachable from the target. HTTP and HTTPS connection addresses are supported; the computer channel provides its own encryption and server identity verification. Browser access has separate requirements described in [Run a Server](/docs/hosting/server/).

## Update a connected Daemon

In a computer's details, use **Daemon maintenance → Update Daemon**. Confirm that running tasks will be interrupted. Astral checks the latest official release, downloads and verifies it, restarts the Daemon, and confirms the new version has reconnected. Identity, credentials and working files remain. If the Daemon is newer than your Server, Astral recommends updating Desktop or Server as well; the reminder does not block Daemon updates.

The server-machine Daemon updates with Desktop or Server. If the computer is offline, lacks managed update support, or an update fails, use the Shell or PowerShell update command in the same section. Run it on the enrolled machine, or on the container's host. Native updates stay within the enrolled operating-system user's account.

## Grant a Bot access

Open **Settings → Bots → your Bot → Computer access**, allow the new machine, and optionally make it the default for new sessions.

If an online computer is missing from a session’s choices, check whether that Bot has been granted access.

## Background services

Remote enrollment installs a service for the current user: a LaunchAgent on macOS, user systemd on Linux, or a scheduled task on Windows. File access follows that operating-system account's permissions.

On Linux, native background enrollment checks user-service availability before pairing. For root (UID 0), Astral reports its preparation actions, enables root's lingering so its user services can run without a login session, and starts root's user manager. If the manager cannot be made available, enrollment stops before pairing. Ordinary accounts are not elevated or automatically given lingering. Foreground and container enrollment skip this preparation.

To connect as root, log in directly as root or run `sudo -i`, then paste the complete connection command. Avoid retaining another account's `HOME` or XDG session environment. Agent commands will have root's permissions. Lingering applies to the account, so uninstalling one Astral Daemon does not disable it or stop the whole user manager.

## Disconnect and stop a computer

Stop affected sessions first and check for work still running. Revoking a Bot's computer access blocks its subsequent operations. To disconnect the computer from all Bots, choose **Revoke connection** in its details.

Stop or uninstall a native remote service on the target computer, using the same operating-system account used for enrollment. Replace `--root` with the data directory shown in computer details. These examples assume `astral` is on PATH; use `astral.exe` on Windows:

```sh
astral computer service stop --root /path/to/computer
astral computer service status --root /path/to/computer
```

To use it again, run `astral computer service start --root /path/to/computer`. For longer-term removal, uninstall the background service:

```sh
astral computer service uninstall --root /path/to/computer
```

Uninstalling stops the service and removes its startup configuration, keeping computer data and work files. Stop a container computer through the host's container manager; back up work before removing a container or volume. Desktop manages its local service with the App; use the desktop background-running settings to stop it.

See [Updates and backups](/docs/hosting/maintenance/) for updating and preserving data.
