---
title: "When something goes wrong"
description: "Find help for connection failures, failed tasks, offline computers, and missing chat replies."
---

Choose the situation closest to yours below. Keep any specific error shown on the page: it can identify whether the failure is in sign-in, a model connection, or an operation on a particular computer.

## The page will not open or sign-in fails

### The browser cannot connect

For an official Linux installation, check this in a server terminal:

```sh
systemctl --user status astral-server.service
```

- **The service is stopped:** start it with `systemctl --user start astral-server.service`. If it cannot start, inspect `journalctl --user -u astral-server.service` for the error.
- **The service is running:** check the server's actual IP address and port. The default is `http://SERVER_IP:4545`. In a remote browser, `127.0.0.1` refers to the browser's own computer.
- **The address is correct:** run `curl http://127.0.0.1:4545/health` on the server. If that works but remote access fails, check the firewall, cloud security rules, and reverse proxy. Use your actual port if it differs.

Desktop disables direct remote management by default; enable it in **Settings → Security & access** when needed. Standalone Server enables it unless its startup configuration explicitly turns it off.

### The page opens but you cannot sign in

| What you see | What to do |
| --- | --- |
| Web sign-in has not been configured | Run `astral auth setup --username admin` on the server, set the password, then sign in |
| Incorrect username or password | Check your input; use [account recovery](/docs/hosting/security/#recover-account-access) if the password is lost |
| You were signed out after changing account details | Sign in with the updated details |
| Authenticator or Passkey is unavailable | Try another configured method or a recovery code; Passkeys require the original HTTPS domain |
| `403 unrecognized management host` | Run `astral config hostnames show` on the server and use the [hostname recovery steps](/docs/hosting/security/#hostname-restriction) |

## Installation downloads fail

For GitHub connection errors or timeouts, try the [complete alternative installation command](/docs/hosting/server/#when-github-is-unreachable). It handles both the installer and subsequent program downloads.

If the error reports a checksum, version mismatch, or unsupported release, keep that error. Use the installer supplied with an older release when installing it; do not skip verification to continue.

## A model does not reply or cannot perform a task

Open a conversation with the same Bot in Astral and send a simple message such as “Reply with connection working.”

- **No text reply:** inspect the conversation error. Handle quota, API Key, service-address, or unavailable-model errors in the corresponding model connection. A custom model service must be reachable from the machine running Astral.
- **Text replies work, but files and commands do not:** check that the selected computer is online and authorized for the Bot. Also check that the selected model supports tool calls.
- **Execution started, but a command failed:** expand that execution record to see the command, computer, and error. Missing software or an incorrect path must be handled on the computer doing the work.

See [Models and search](/docs/usage/models/) for connection setup.

## A computer stays offline

Check that the target computer is on, awake, and able to reach the machine running Astral. Then run these checks on **the connected computer**:

| What to check | Command |
| --- | --- |
| Connection to its original Astral Server | `astral computer status` |
| Whether its background connection service is running | `astral computer service status` |
| Background service logs | `astral computer service logs` |

These commands apply to remote native computers configured by the enrollment installer. Select the original installation when prompted, or supply its `--root`. For a container computer, first check the running container on its Docker host.

If a new enrollment command has expired, generate another one from Add Computer. When the Server address changes, existing computers still use their saved address, so check whether that original connection remains reachable.

## Creating a container fails

The local-container option shows the actual check result and the next action. Recheck after resolving it. For Docker access by a Linux background account, see [container runtime setup](/docs/computers/containers/).

If creation has already started, inspect its log for the failed stage. The first environment-image download can take some time. Keep the specific download or container-start error for further investigation.

## A platform account is connected but the Bot does not reply

1. Send a small task to the same Bot inside Astral to check its model and computer.
2. Confirm the intended private or group chat is connected in the platform account's settings. In a group, use a real mention of the Bot or reply to its message.
3. In the original chat, send “Reply in this chat with: connection working.” Check the corresponding Astral conversation for that incoming message.

**If Astral never receives it**, check the platform's incoming-message permissions and event configuration. **If Astral handles it but the original chat has no reply**, inspect the sending result and any platform restrictions.

Continue with the relevant guide: [Telegram](/docs/chat/telegram/), [QQ](/docs/chat/qq/), [Weixin](/docs/chat/weixin/), or [Feishu](/docs/chat/feishu/).

## A file will not open or was not sent

Check that the computer holding the file is online and the original file still exists. File operations use the conversation's selected computer; the same path on another computer may contain nothing.

For a failed platform upload, read the actual sending error. If the outcome is uncertain, check the destination chat before resending. See [Files and results](/docs/usage/files/) for providing, saving, and downloading files.

## Cannot connect to the user service manager

Use this section when `systemctl --user` reports `Failed to connect to bus` in a Linux terminal.

First confirm that the terminal uses the OS account that installed Astral. Some sessions, including certain `sudo -i` sessions, lack the environment needed to reach the user service. Run under that account:

```sh
export XDG_RUNTIME_DIR="/run/user/$(id -u)"
export DBUS_SESSION_BUS_ADDRESS="unix:path=$XDG_RUNTIME_DIR/bus"
systemctl --user status astral-server.service
```

If that directory has no `bus`, check the installer's preparation error and the system's systemd user-session and D-Bus components. Do not substitute another account's UID or runtime directory.

## If you still need help

Find your version under **Settings → About & diagnostics**, then include these details in a [GitHub issue](https://github.com/oines/astral/issues):

- Desktop or Server, and the operating system.
- The step that failed, what you expected, and what happened.
- The relevant complete error; include the computer type or chat connection method when applicable.

Remove passwords, API Keys, Tokens, recovery codes, and private conversation content before submitting.
