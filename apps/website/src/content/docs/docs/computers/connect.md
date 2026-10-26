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

The command contains temporary pairing credentials. Use it only on the intended machine and generate a new command if it expires. The installer needs the matching official release assets. In a source-development environment without those published assets, do not assume a public download is available.

The remote computer connects outward to the Server. `127.0.0.1` refers to the same machine; a LAN, VPN, or public address must actually be reachable from the target. HTTP and HTTPS connection addresses are supported; the computer channel provides its own encryption and server identity verification. Browser access has separate requirements described in [Run a Server](/docs/hosting/server/).

## Update a connected Daemon

In a computer's details, use **Daemon maintenance → Update Daemon**. Confirm that running tasks will be interrupted. Astral checks the latest official release, downloads and verifies it, restarts the Daemon, and confirms the new version has reconnected. Identity, credentials and working files remain. If the Daemon is newer than your Server, Astral recommends updating Desktop or Server as well; the reminder does not block Daemon updates.

The server-machine Daemon updates with Desktop or Server. If the computer is offline, lacks managed update support, or an update fails, use the Shell or PowerShell update command in the same section. Run it on the enrolled machine, or on the container's host. Native updates stay within the enrolled operating-system user's account.

## Grant a Bot access

Open **Settings → Bots → your Bot → Computer access**, allow the new machine, and optionally make it the default for new sessions.

An online computer is not automatically available to every Bot. If it is absent from a session's choices, check that Bot's permission first.

## Background services

Remote enrollment installs a service for the current user: a LaunchAgent on macOS, user systemd on Linux, or a scheduled task on Windows. File access follows that operating-system account's permissions.

Revoking Bot access, removing a connection, and deleting files are different operations. See [Updates and backups](/docs/hosting/maintenance/) for service maintenance.
