---
title: "Install Server on Linux"
description: "Install the background service, set up sign-in, and complete your first task in a browser."
---

Use this guide to run Astral on a Linux server that stays on. Once installed, you can use Astral through a browser and close the installation terminal while it keeps running.

## Before you start

You need a Linux server using systemd, with an x86_64 or ARM64 CPU and terminal or SSH access. Bash, curl, Python 3, sha256sum, flock, systemctl, and loginctl must already be installed.

Sign in as the OS account that will run Astral. The installer requests sudo when needed to prepare background operation or register the command. Running as root also gives Bot commands on the server root privileges; Astral can run under an ordinary account.

## 1. Run the installer

Copy this command into a terminal **on the server**:

```sh
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
  https://github.com/oines/astral/releases/latest/download/install-server.sh | bash
```

The installer downloads and verifies the program, then starts its background service. If GitHub downloads fail, use the [alternative download command](#when-github-is-unreachable) below. If it reports a missing system component or permission, resolve that error and run it again.

## 2. Set up sign-in

Enter an Astral administrator username and password when prompted, then confirm the password. Password characters are hidden as you type.

You will use this account to sign in to Astral in a browser. It is configured separately from your SSH account. Keep it safe; if you forget the password, you can [reset web sign-in](/docs/hosting/security/#recover-account-access) on the server.

Installation is complete when the terminal displays `Astral Server … is ready`. You can close SSH: the service keeps running and starts again when the server boots. The installer enables user lingering when needed so this account's user services can run after logout.

## 3. Open Astral and complete a task

On your own computer, open `http://SERVER_IP:4545`, replacing `SERVER_IP` with the server's actual address. You can also use an address printed by the installer if it is reachable from your network. Sign in with the username and password you just created.

Follow [Complete your first task](/docs/start/first-steps/) to connect a model, choose the server machine, and send a task. The selected computer is the server. To work with files on your laptop, [connect the laptop](/docs/computers/connect/) separately.

If the page does not open, use [the connection checks](/docs/hosting/troubleshooting/#the-page-will-not-open-or-sign-in-fails) to check the service, address, and firewall. For ongoing access over the public internet, you can configure your own [HTTPS address](#use-an-https-reverse-proxy).

## Manage the background service

Run these commands on the server as **the same OS account used for installation**. Stopping or restarting interrupts running tasks.

| What you need | Command |
| --- | --- |
| Check service status | `systemctl --user status astral-server.service` |
| Read logs | `journalctl --user -u astral-server.service` |
| Stop now | `systemctl --user stop astral-server.service` |
| Start again | `systemctl --user start astral-server.service` |
| Restart | `systemctl --user restart astral-server.service` |
| Disable startup at boot | `systemctl --user disable astral-server.service` |
| Enable startup at boot | `systemctl --user enable astral-server.service` |

`stop` ends the current run; `disable` only changes automatic startup. Use both actions to stop and disable the service. If a command reports `Failed to connect to bus`, see [the user-manager checks](/docs/hosting/troubleshooting/#cannot-connect-to-the-user-service-manager).

## Update

Finish or stop current tasks, then open **Settings → About & diagnostics** and check for updates. When supported by your installation, select **Update and restart**.

You can also rerun the installation command on the server under the original OS account. If you chose a custom directory, keep the same `--root`. Updates preserve your account, conversations, computer identities, and settings. See [Updates and backups](/docs/hosting/maintenance/) for backup and recovery steps.

## Uninstall the background service

After stopping current tasks, run as the original OS account on the server:

```sh
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
  https://github.com/oines/astral/releases/latest/download/install-server.sh \
  -o install-server.sh
bash install-server.sh --uninstall
```

For a custom installation directory, change the last line to `bash install-server.sh --uninstall --root /original/absolute/installation/path`.

The script stops the service, disables automatic startup, and removes programs and command entries that can be safely removed. **Your account, conversations, computer files, settings, and backups remain.** Reinstall into the same directory to use them again. One complete release remains for reinstallation checks and recovery; commands shared with other computer installations also remain.

Uninstalling does not remove Docker containers, remote computer files, or account-wide lingering. A modified service definition or mismatched installation causes cleanup to stop with an explanation. Before separately deleting data, review [what to back up](/docs/hosting/maintenance/#what-to-back-up).

## Use an HTTPS reverse proxy

To use an address such as `https://astral.example.com`, configure Caddy, Nginx, or your existing proxy to provide HTTPS. Astral itself serves HTTP on port `4545` by default.

Point your domain at the server and allow external access to ports `80` and `443`. If you have no proxy yet, [install Caddy using its official guide](https://caddyserver.com/docs/install) and start its service.

With the official Caddy service, add this site to `/etc/caddy/Caddyfile`, keeping any existing sites and replacing the example domain:

```text title="Caddyfile"
astral.example.com {
    reverse_proxy 127.0.0.1:4545
}
```

Save the file and reload the configuration as described in [Caddy's service guide](https://caddyserver.com/docs/running#using-the-service):

```sh
sudo systemctl reload caddy
```

Open your HTTPS address and sign in. A fresh Astral installation needs no domain registration. If you enabled hostname restriction, first [allow the domain](/docs/hosting/security/#hostname-restriction).

A proxy on another machine also needs [trusted-proxy configuration](/docs/hosting/server-options/#a-proxy-on-another-machine). Passkeys must be configured and used from an actual HTTPS page.

## When GitHub is unreachable

You can explicitly choose the third-party [GH-Proxy service](https://gh-proxy.com/docs/github-accelerator/). This command proxies both the installer and its subsequent program downloads:

```sh
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
  https://gh-proxy.org/https://github.com/oines/astral/releases/latest/download/install-server.sh \
  | bash -s -- --github-proxy https://gh-proxy.org/
```

Downloads are still verified. This proxy is used only for GitHub downloads, and its availability depends on the service and your network. To stop using it later, rerun the installer with `--github-proxy ''`. Omitting the option retains your previous choice.

## Need a custom deployment?

[Server deployment options](/docs/hosting/server-options/) covers unattended installation, custom directories and ports, foreground operation, and access restrictions. For container computers, follow [Use containers](/docs/computers/containers/) to prepare Docker. Installing Astral does not install Docker or change Docker group membership.
