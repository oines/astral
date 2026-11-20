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

Before generating the command, check two fields:
- **Server address:** an address the target computer can reach (LAN IP on the same network, or your VPN address). `127.0.0.1` points to the target itself and cannot reach another host.
- **Operating system:** the OS running the command (for a container, pick the host OS).

Run the **complete command** as the system user whose file permissions you want the Bot to have. It installs the background service and the `astral` CLI, then brings the computer online in Astral.

:::note[Pairing credentials and transport]
Enrollment commands carry temporary pairing credentials—use them only on the intended machine and regenerate if expired. Computers connect outward to Server over an encrypted channel that verifies the Server's identity, using either HTTP or HTTPS enrollment URLs.
:::

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

The Daemon is the Astral background service running on each connected computer. The host Daemon updates with Desktop or Server; update remote computers from their details via **Daemon maintenance → Update Daemon**.

:::caution[Updates interrupt active tasks]
Updating restarts the background service, so finish or stop important work first. Computer identity, credentials, and working files stay intact.
:::

If a machine is offline or managed update fails, run the update command shown in the same section directly on that machine (or on the container host).

## When the target Computer cannot reach GitHub

If GitHub is unreachable from the target computer, set a download proxy in its terminal before pasting the full enrollment or update command (only installer scripts and release downloads use this proxy—normal computer-to-Server traffic does not; see [download proxy guide](/docs/hosting/server/#when-github-is-unreachable)):

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
