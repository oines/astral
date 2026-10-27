---
title: "Run a Server"
description: "Install Astral Server on a Linux VPS, create a web account, and configure network access."
---

Official Server binaries include the Web UI. A separate frontend build is not required.

## Install on a Linux VPS

Use Linux x86_64 or ARM64 with Bash, curl, Python 3, and a running systemd user manager. Run this command as the account that will own Astral:

```sh
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
  https://github.com/oines/astral/releases/latest/download/install-server.sh | bash
```

The default installation is `${XDG_DATA_HOME:-$HOME/.local/share}/astral-server`. The installer validates the release and registers `astral-server.service` with `systemd --user`. It does not install a system-wide service or enable user lingering. For continuous hosting after logout and reboot, ask the VPS administrator to enable lingering for that account with `loginctl enable-linger USER`.

To upgrade later, run the installation command again using the same account and root. Use `bash -s -- --tag v0.1.0` in place of `bash` to select a published version explicitly. Omitted network settings are preserved.

## Create web sign-in

Run the command printed by the installer. For the default installation:

```sh
"${XDG_DATA_HOME:-$HOME/.local/share}/astral-server/current/bin/astral" auth setup \
  --data "${XDG_DATA_HOME:-$HOME/.local/share}/astral-server/data/server" --username admin
```

Enter and confirm the hidden password. Direct remote management is off by default. To open Astral through SSH, run this on your own computer, replacing `USER@VPS`:

```sh
ssh -N -L 4545:127.0.0.1:4545 USER@VPS
```

Then open `http://127.0.0.1:4545` in your browser and sign in. The network settings below cover direct remote access and reverse proxies.

## Run manually

You can also start the matching [release executable](https://github.com/oines/astral/releases/latest) or a [source build](/docs/start/get-astral/). These examples assume it is named `astral` and available on PATH; use `.exe` on Windows.

```sh
astral serve --data ./data/server --computers-dir ./data/computers
```

In another terminal, initialize web sign-in for the **same data directory**:

```sh
astral auth setup --data ./data/server --username admin
```

Open `http://127.0.0.1:4545` on the Server machine. A manual foreground process stops with Ctrl+C. For persistent hosting, use absolute data paths and a service manager, and reuse the same directories after restarting.

## Three network settings

| Setting | Effect |
| --- | --- |
| Listener | Network interfaces and port; changes need a restart |
| Access addresses | Full HTTP/HTTPS addresses for other devices; does not create a proxy or change the listener |
| Allow remote management | Allows direct remote Web and management API access, still requiring sign-in |

The default listener is `0.0.0.0:4545`. Remote management is off by default; computer connections remain available. Access addresses and the remote-management switch are available in network settings.

CLI arguments override environment variables, which override saved settings. The UI shows externally controlled values. Updated addresses apply to new enrollment commands; existing computers keep their saved address.

## Use an HTTPS reverse proxy

Astral serves HTTP. Use Caddy, Nginx, or your existing proxy for HTTPS. For an installer-managed Server, rerun the installation command with `bash -s -- --public-url https://astral.example.com` in place of `bash`; omitted settings are preserved. For a foreground Server with a proxy on the same machine:

```sh
astral serve --data /srv/astral/server --computers-dir /srv/astral/computers \
  --public-url https://astral.example.com
```

Example Caddy configuration; replace the hostname with a domain you control and configure its DNS:

```text title="Caddyfile"
astral.example.com {
    reverse_proxy 127.0.0.1:4545
}
```

Preserve the external Host, support WebSocket upgrades, and stream SSE without buffering. Loopback proxies are trusted by default for HTTPS forwarding. For a separate proxy host or container, configure `--trusted-proxy IP_OR_CIDR` and enable `--allow-remote-management` for that remote management path.

A local proxy can expose the local management entry even while direct remote management is disabled. Account login still applies. Use HTTPS for browser credentials across public networks.

## Make computers reachable

Computers must reach their selected Server address. Astral does not provide NAT traversal or a relay; use your LAN, VPN, or public network.

A loopback-only listener prevents direct access from other machines and Docker Desktop containers. A containerized Server needs persistent data, a published port, and an explicit reachable address. There is no official Server container image yet; the Computer environment image is a different component.
