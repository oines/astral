---
title: "Connect computers"
description: "Connect your local machine or other computers to Astral, then authorize a Bot to use them."
---

A Bot needs an online **computer** to read files and run commands. You can use the machine running Astral itself, or connect additional laptops, desktops, and servers.

Setup takes two steps: **1. Connect the computer → 2. Grant a Bot access**. Connecting a machine alone does not make it visible to any Bot.

## Option 1: Use the Astral host

To work with files on the machine running Astral, go to **Settings → Computers → Add Computer** and choose **Use the server machine**.

- **Desktop:** the computer running the Desktop app.
- **Standalone Server:** the Linux server hosting Astral (not the laptop running your browser).

Commands run under the OS account hosting Astral Server. Once it shows online, jump to [Grant a Bot access](#grant-a-bot-access). For a separate Linux environment, use a [container computer](/docs/computers/containers/) instead.

## Option 2: Connect another machine

Open **Settings → Computers → Add Computer → Connect another machine** to generate an enrollment command, then run it in a terminal on **the target computer** (Shell on macOS/Linux, PowerShell on Windows).

Before generating the command, check three fields:
- **Server address:** an address the target computer can reach (LAN IP on the same network, or your VPN address). `127.0.0.1` points to the target itself and cannot reach another host.
- **Operating system:** the OS running the command (for a container, pick the host OS).
- **Download source:** keep **Official GitHub**, or choose a third-party mirror / custom HTTPS URL if the target machine cannot reach GitHub directly.

Run the **complete command** as the system user whose file permissions you want the Bot to have. The installer downloads the standalone `astral-daemon` binary, installs the user background service, and brings the computer online in Astral. Permanent services remember any selected download source for future Daemon updates.

:::note[Pairing credentials and transport]
Enrollment commands carry temporary pairing credentials—use them only on the intended machine and regenerate if expired. Computers connect outward to Server over an encrypted channel that verifies the Server's identity, using either HTTP or HTTPS enrollment URLs.
:::

## Option 3: Connect a temporary computer (one session only)

For shared lab computers, borrowed laptops, or short-lived virtual machines where you do not want to install a background service, connect a **temporary computer** directly from the session you are working in:

1. Open the computer menu in a root session's input bar and choose **Connect temporary Computer**.
2. Set the **Server address**, **Duration** (2, 4, or 8 hours), **Target operating system**, and **Download source**, then click **Generate command**.
3. Open a terminal in the folder you want to work in and run the command (or check **Already have astral-daemon** if you brought `astral-daemon` on a USB drive).

What makes a temporary computer different:
- **Zero local installation:** downloads into a temporary directory, runs in the foreground, does not modify `PATH`, requires no administrator privileges, does not write `credentials.json`, and deletes its temporary folder on exit.
- **Starts in your current folder:** the session's initial working directory on that machine is the folder where you ran the command.
- **Scoped to one session:** only the owning root session and its subagents can use it—never other sessions or Bots. Connecting it notifies the session context without automatically switching your active computer; select it in the computer menu (or ask the Bot to switch) when you are ready.
- **Untrusted-host defaults:** **Astral API** is permanently disabled and cannot be turned on; Managed Skills are not synced to the machine.
- **Automatic cleanup:** press `Ctrl+C`, close the terminal, let the duration expire, archive/delete the session, or click **Disconnect** in the session's computer menu (or **Disconnect and remove** under **Settings → Computers → Temporary connections**). Once removed, it disappears from all lists while your conversation history remains intact.

## Grant a Bot access

Once the computer is online, enable it under **Settings → Bots → your Bot → Computer access** (optionally set it as the default for new sessions).

Open a session for that Bot, select the computer, and verify with a read-only check:

> Tell me the current computer's name and working directory, then list the files in that directory without changing anything.

:::caution[Scope of access]
The Bot inherits the file and command permissions of the OS account running the service, not just the default working directory. Review [Permissions and privacy](/docs/start/safe-use/) before exposing a machine to a public group or untrusted files.
:::

## Control what the computer can call

Toggle **Enable Astral API** when adding a computer or anytime in its details:

| Use case | Recommended setting |
| --- | --- |
| Remote build host that only runs commands and edits files | **Off** (default for new remote computers and remote containers) |
| Trusted machine whose scripts call Astral APIs or deliver local event wake-ups | **On** (default for the server host and local containers) |

When turned off, Server blocks API calls and new event deliveries from that computer across all Bots—without blocking normal commands or file edits, and without needing a Daemon update. If a computer's credentials ever leak, [revoke its connection](#disconnect-and-stop-a-computer) and re-pair.

## Update a connected Daemon

The Daemon is the Astral background service running on each connected computer. The host Daemon updates with Desktop or Server; update remote computers from their details via **Daemon maintenance → Update Daemon**. You can pick a **Download source** in the same section before starting the update—the Daemon uses it for that download and remembers it in its user service configuration (pick **Official GitHub** to switch back to direct downloads).

:::caution[Updates interrupt active tasks]
Updating restarts the background service, so finish or stop important work first. Computer identity, credentials, and working files stay intact.
:::

If a machine is offline or managed update fails, copy the update command shown in the same section and run it directly on that machine (or on the container host; container computers let you pick the host OS).

## When the target Computer cannot reach GitHub

Choose a mirror or custom HTTPS prefix under **Download source** when adding a remote computer, connecting a temporary computer, or updating a Daemon. Switching the download source updates the command immediately without regenerating the pairing token.

Only installer scripts and release downloads use the mirror—normal computer-to-Server traffic stays direct, and every downloaded binary is still verified against SHA-256 checksums. If you run installer commands manually from a script, you can also set `ASTRAL_GITHUB_PROXY` in the terminal beforehand (see [download proxy guide](/docs/hosting/server/#when-github-is-unreachable)):

```sh
# macOS / Linux (clear with: unset ASTRAL_GITHUB_PROXY)
export ASTRAL_GITHUB_PROXY=https://gh-proxy.org/
```

```powershell
# Windows PowerShell (clear with: $env:ASTRAL_GITHUB_PROXY = '')
$env:ASTRAL_GITHUB_PROXY = 'https://gh-proxy.org/'
```

## Quick troubleshooting

| Symptom | What to check |
| --- | --- |
| Stuck waiting for enrollment | Read the terminal output on the target machine and verify it can reach the Server address. |
| Download fails during install | Set `ASTRAL_GITHUB_PROXY` above; unpublished source builds require [Build from source](/docs/hosting/build-from-source/). |
| Existing `astral` command reported | The installer won't overwrite another binary. Remove any manually added old `astral.exe` folder from PATH, reopen the terminal, and rerun (don't delete files of a running service). |
| Online, but missing in a session | Grant permission in that Bot's **Computer access** settings. |
| Cannot read a file | Verify the file lives on this computer and is readable by the service's OS account. |
| Keeps dropping during large transfers | Update Astral Server (or Desktop) and the Daemon on each involved computer. |

## Disconnect and stop a computer

Stop active sessions first, then pick the level you need:

- **Remove one Bot's access:** uncheck the computer in that Bot's **Computer access**.
- **Disconnect from all Bots:** select **Revoke connection** in computer details (you can then delete its registration).
- **Stop or uninstall the background service** (run as the user who enrolled it):

```sh
astral computer service stop      # Pause (use start to resume, status to inspect)
astral computer service uninstall # Remove the background service
```

:::caution[What deleting a registration does]
- **Permanently deleting a registration** removes all event wake-up endpoints on that computer and clears buffered events not yet delivered to a session (existing session history and other computers' events stay intact).
- **Working files are kept.** Deleting a registration does not stop or uninstall the remote Daemon or delete Docker containers/volumes—manage containers on their host.
:::

<details class="astral-doc-details">
<summary>Connection IP, slow networks, Linux background services, and multiple instances</summary>

- **Connection IP & trusted proxies:** Online computers show **Connection IP** in details—the peer address seen by Server, or the forwarded client IP when behind a [configured trusted proxy](/docs/hosting/server-options/#a-proxy-on-another-machine) such as Cloudflare Tunnel.
- **Slow or dropped links:** Slow links delay output while Astral buffers command output on the machine. Once a disconnect is confirmed, running commands are stopped and do not auto-restart on reconnect; check file state before re-running. Closing a browser tab never disconnects a computer.
- **OS service types & Linux lingering:** macOS uses a user LaunchAgent, Linux uses user systemd, and Windows uses a user scheduled task. Ordinary Linux accounts need working user services and do not automatically enable lingering. To enroll as `root`, log in as root or run `sudo -i` first—root background enrollment sets up user services and lingering automatically (uninstalling Astral does not disable account-wide lingering; foreground and container modes skip this).
- **Multiple instances:** Follow the prompt when running `astral computer service`, or pass `--root /path/to/computer` for non-interactive commands (copy legacy custom paths from computer details). Desktop manages its local service via its app settings.

</details>
