---
title: "Run a Server"
description: "Start Astral Server, create a web account, and configure network access."
---

Build the program using [Get Astral](/docs/start/get-astral/). These examples assume the matching executable is named `astral` and available on PATH. You can instead use its full path in `dist/bin/`; use `.exe` on Windows.

## Start and create an account

From the repository root:

```sh
astral serve --data ./data/server --computers-dir ./data/computers
```

The built `apps/web/dist/` supplies the product interface. Outside the repository, retain the distribution layout or point `--web-dir` to that directory.

In another terminal, initialize web sign-in for the **same data directory**:

```sh
astral auth setup --data ./data/server --username admin
```

Enter and confirm the hidden password, then open `http://127.0.0.1:4545` on the Server machine. Do not put the password in command-line arguments.

For persistent hosting, use absolute data paths and a system service manager. Reuse the same directories after restarting.

## Three network settings

| Setting | Effect |
| --- | --- |
| Listener | Network interfaces and port; changes need a restart |
| Access addresses | Full HTTP/HTTPS addresses for other devices; does not create a proxy or change the listener |
| Allow remote management | Allows direct remote Web and management API access, still requiring sign-in |

The default listener is `0.0.0.0:4545`. Remote management is off by default; computer connections remain available. Access addresses and the remote-management switch are available in network settings.

CLI arguments override environment variables, which override saved settings. The UI shows externally controlled values. Updated addresses apply to new enrollment commands; existing computers keep their saved address.

## Use an HTTPS reverse proxy

Astral serves HTTP. Use Caddy, Nginx, or your existing proxy for HTTPS. For a proxy on the same machine:

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
