---
title: "Server deployment options"
description: "Unattended installation, custom directories and ports, foreground operation, and network access."
---

For an ordinary Linux background installation, follow [Install Server](/docs/hosting/server/). Use the options below when you need automated installation, different defaults, or your own process management.

## Unattended installation

If an automation script cannot enter a password interactively, prepare a password file readable only by the installation account. Put only the administrator password in it. Keep passwords out of public scripts and command arguments.

Download the installer, then run it with your own username and password-file path:

```sh
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
  https://github.com/oines/astral/releases/latest/download/install-server.sh \
  -o install-server.sh
ASTRAL_ADMIN_USERNAME=admin \
ASTRAL_ADMIN_PASSWORD_FILE=/secure/path/astral-admin-password \
  bash install-server.sh
```

Arrange permission for system preparation and command registration in advance. The web administrator password does not provide sudo authorization. Initial credentials do not reset an existing account during an update.

## Choose a directory, port, or version

Download `install-server.sh` as shown above, then choose the options you need:

| What to change | Example option |
| --- | --- |
| Installation and data root | `--root /srv/my-astral` |
| HTTP listening port | `--listen 0.0.0.0:4546` |
| Listen only on this machine | `--listen 127.0.0.1:4545` |
| Published version | Use the installer supplied with that release and pass `--tag TAG`, replacing `TAG` with its release tag |

For example, install into a custom directory using port `4546`:

```sh
bash install-server.sh --root /srv/my-astral --listen 0.0.0.0:4546
```

The installation account must be able to write that directory. Keep the same `--root` for updates and uninstalling. The official installer manages one `astral-server.service` per OS account; different roots cannot create multiple services with that same name.

The default root is `${XDG_DATA_HOME:-$HOME/.local/share}/astral-server`. Server data is in `data/server/`, and local computer files are in `data/computers/`. These differ from manual `serve` defaults. [Find the actual directories](/docs/hosting/maintenance/#locate-the-data) before backing up.

Existing network preferences are retained unless you explicitly supply an option to change them. To install an older release, use the installer included with that release so it does not require newer program capabilities.

## Run in the foreground

Use this for temporary debugging or a service you manage yourself. Obtain a [release program](/docs/start/get-astral/) or [build from source](/docs/hosting/build-from-source/). Name the program for your system `astral` and put it on PATH; use `astral.exe` on Windows.

Choose separate absolute directories for Server and local computer data. Replace the example paths, then run:

```sh
astral serve \
  --data /absolute/path/to/astral-data/server \
  --computers-dir /absolute/path/to/astral-data/computers
```

Leave that terminal running. In another terminal, set up sign-in for the same Server data directory:

```sh
astral auth setup --data /absolute/path/to/astral-data/server --username admin
```

Open the address printed in the startup log and sign in. Ctrl+C ends the foreground service; disconnecting SSH may also end it. For long-term operation, configure process management, logs, and boot startup yourself, keeping the same data paths.

## Restrict web access

A standalone Server accepts remote web sign-in by default. You can control network access through a firewall or VPN, or listen only on `127.0.0.1` and use SSH forwarding or a reverse proxy.

The official installer can save a choice to disable direct remote management:

```sh
bash install-server.sh --allow-remote-management=false
```

Use `--allow-remote-management=true` to restore it. Include the original `--root` for a custom installation. Computer connections continue while direct remote management is off. A local reverse proxy can still forward the web interface, and every entry requires sign-in.

For manual operation, use `astral serve --allow-remote-management=false` or `ASTRAL_ALLOW_REMOTE_MANAGEMENT=false`. Standalone Server has no web switch for this setting. Desktop exposes remote management in Security & access.

To restrict the domains or IP addresses allowed for management access, use [hostname restriction](/docs/hosting/security/#hostname-restriction).

## A proxy on another machine

The [HTTPS example](/docs/hosting/server/#use-an-https-reverse-proxy) assumes the proxy and Astral run on the same machine. If the proxy runs elsewhere or inside a container, tell Astral which source can report an external HTTPS connection.

Add `--trusted-proxy IP_OR_CIDR` to the Server's startup configuration, or specify the actual proxy source with `ASTRAL_TRUSTED_PROXIES`. Preserve the original Host, support WebSocket connections, and forward SSE promptly. Do not trust arbitrary sources as proxies.

Editing the official service definition makes updates manual; web updates require the installer's original service. Before rerunning the installer to restore managed updates, decide how to preserve any custom startup settings.

## Run Server inside a container

There is currently no official Server container image or Compose installation. The documented Ubuntu environment image is for container computers, not Server deployment.

A custom Server container needs persistent data and identity, published ports, an administrator account, and an address other computers can reach. Its “server machine” is the container itself. Use `--no-local` if work should run only on separate computers. Your own container configuration handles updates and recovery.
