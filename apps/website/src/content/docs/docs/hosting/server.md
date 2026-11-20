---
title: "Run a Server"
description: "Install Astral Server on Linux with one command, create an admin login, and optionally serve it over HTTPS."
---

Deploy Astral on an always-on Linux server so you can open it from any browser and keep chat integrations and schedules running while your laptop is closed.

## Prerequisites

- A systemd-based Linux server (`x86_64` or `ARM64`) with `bash`, `curl`, `python3`, `sha256sum`, `flock`, `systemctl`, and `loginctl`.
- Sign in over SSH as the OS user that will run Astral (an ordinary user is recommended; running as `root` gives Bot commands on the host root privileges).

## 1. Run the installer

Run this **in the server's terminal** (if GitHub is unreachable, see [the proxy command](#when-github-is-unreachable) below):

```sh
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
  https://github.com/oines/astral/releases/latest/download/install-server.sh | bash
```

## 2. Create your web sign-in account

Follow the prompt to set an Astral administrator username and password (separate from your SSH credentials). Once you see `Astral Server … is ready`, the service is running and enabled at boot—you can close SSH (if you ever forget the password, [reset it on the server](/docs/hosting/security/#recover-account-access)).

## 3. Open Astral in your browser

Visit `http://SERVER_IP:4545` (or the address printed by the installer), sign in, and follow [Your first task](/docs/start/first-steps/).

:::note[The local computer here is the server]
Selecting the Astral host targets this Linux server. To work on files on your laptop, [connect your laptop](/docs/computers/connect/) separately. If the page won't open, check [Troubleshooting](/docs/hosting/troubleshooting/#the-page-will-not-open-or-sign-in-fails).
:::

## Manage the background service

Run on the server as **the same OS user used for installation** (stopping or restarting interrupts active tasks):

| Action | Command |
| --- | --- |
| Status / logs | `systemctl --user status astral-server.service` / `journalctl --user -u astral-server.service` |
| Start / stop / restart | `systemctl --user start\|stop\|restart astral-server.service` |
| Boot startup on / off | `systemctl --user enable\|disable astral-server.service` |

If you see `Failed to connect to bus`, check [Cannot connect to the user service manager](/docs/hosting/troubleshooting/#cannot-connect-to-the-user-service-manager).

## Update

Stop or finish active tasks, then open **Settings → About & diagnostics** and click **Update and restart**—or rerun the install command above under the same OS user (keep `--root` if customized). Accounts, sessions, computer identities, and settings are preserved; see [Updates and backups](/docs/hosting/maintenance/).

## Uninstall the background service

After stopping active tasks, run as the original OS user (append `--root /original/path` if customized):

```sh
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
  https://github.com/oines/astral/releases/latest/download/install-server.sh -o install-server.sh
bash install-server.sh --uninstall
```

:::note[Your data stays safe]
Uninstalling stops the service and cleans up binaries—**your account, sessions, files, settings, and backups remain** for reinstallation. Docker containers and account-wide lingering are untouched; review [what to back up](/docs/hosting/maintenance/#what-to-back-up) before deleting data manually.
:::

## Use an HTTPS reverse proxy

For public internet access (and Passkeys), place Caddy, Nginx, or another HTTPS proxy in front of Astral's default HTTP port `4545`. With [Caddy](https://caddyserver.com/docs/install), point your domain at the server, open ports `80`/`443`, and add to `/etc/caddy/Caddyfile`:

```text title="Caddyfile"
astral.example.com {
    reverse_proxy 127.0.0.1:4545
}
```

Reload Caddy (see [Caddy's service docs](https://caddyserver.com/docs/running#using-the-service)):

```sh
sudo systemctl reload caddy
```

Fresh installs need no domain registration; if hostname restriction is on, [allow the domain first](/docs/hosting/security/#hostname-restriction). If the proxy runs on another host, configure [trusted proxies](/docs/hosting/server-options/#a-proxy-on-another-machine).

## When GitHub is unreachable

Use the third-party [GH-Proxy service](https://gh-proxy.com/en/docs/github-accelerator/) to proxy both the installer and release downloads (checksums are still verified; clear later with `--github-proxy ''`):

```sh
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
  https://gh-proxy.org/https://github.com/oines/astral/releases/latest/download/install-server.sh \
  | bash -s -- --github-proxy https://gh-proxy.org/
```

For unattended installs, custom ports/directories, or foreground runs, see [Server options](/docs/hosting/server-options/). For container computers, see [Containers](/docs/computers/containers/).
