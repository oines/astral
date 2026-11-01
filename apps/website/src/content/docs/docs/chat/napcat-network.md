---
title: "NapCat networking"
description: "Choose the addresses Astral needs when NapCat runs in Docker or on another computer."
---

Use this page when NapCat does not run directly on the same computer as Astral in the [QQ guide](/docs/chat/qq/#napcat). First identify **where Astral Server runs**: Desktop uses a local server, while a browser-based deployment connects from the server host.

Astral needs both NapCat's HTTP and forward WebSocket services. It allows `http://` and `ws://` only on loopback addresses. Other destinations require `https://` and `wss://`, including private-network IPs and Docker service names.

| Your setup | Method |
| --- | --- |
| NapCat in Docker; Astral runs directly on the same host | Publish both ports to the host's `127.0.0.1`, as shown below |
| NapCat on another computer; Astral runs directly on a computer or server | Start SSH forwarding on Astral's host, then use loopback addresses |
| Astral is also in a separate container, or you already use a reverse proxy | Provide HTTPS and WSS endpoints reachable from Astral's container |
| You only need to open a remote NapCat management page | Forward the WebUI port separately, as shown at the end |

`0.0.0.0` tells a service which interfaces to listen on; it is not a destination address for Astral. A container's `127.0.0.1` refers only to that container.

## NapCat in Docker, Astral on the host

Use `docker ps -a` to find an existing NapCat container and preserve its configuration and data volumes. Do not deploy another instance for the same QQ account.

Check or add port publishing in the existing Compose service:

```yaml
services:
  napcat:
    # Keep the existing image, environment, volumes, and other settings.
    ports:
      - "127.0.0.1:6099:6099" # Management page
      - "127.0.0.1:3000:3000" # HTTP service
      - "127.0.0.1:3001:3001" # Forward WebSocket service
```

This is a configuration fragment, not a complete installation file. Apply changes with `docker compose up -d napcat` from the original Compose directory; replace `napcat` if your service has another name. Keep the persistent data volumes. Recreating the container briefly interrupts the connection.

Enable both services in NapCat WebUI, with Host set to `0.0.0.0` inside the container and ports matching `3000` and `3001`. Enter these addresses in Astral:

- HTTP: `http://127.0.0.1:3000`
- Forward WebSocket: `ws://127.0.0.1:3001`

If an existing mapping is `13000:3000`, use host port `13000` in Astral. The same applies to WebSocket. Both Docker port publishing and the services themselves must be configured. See [Docker's port-publishing documentation](https://docs.docker.com/engine/network/port-publishing/).

## NapCat on another computer: SSH forwarding

Use this method when you can SSH into NapCat's host and Astral runs directly on a computer or server. NapCat's services can keep listening only on that host's `127.0.0.1`.

Run this in a terminal **on the machine running Astral Server**:

```sh
ssh -N -o ExitOnForwardFailure=yes \
  -L 127.0.0.1:13000:127.0.0.1:3000 \
  -L 127.0.0.1:13001:127.0.0.1:3001 \
  USER@NAPCAT_HOST
```

Replace `USER` and `NAPCAT_HOST` with the SSH username and NapCat host. Replace the final `3000` and `3001` with its actual OneBot ports. Leave the terminal and SSH connection running, then enter these addresses in Astral:

- HTTP: `http://127.0.0.1:13000`
- Forward WebSocket: `ws://127.0.0.1:13001`

The first port numbers, `13000` and `13001`, belong to Astral's host and must be available. If SSH disconnects, Astral loses access to NapCat. After restoring the tunnel, resend any needed messages missed during the interruption.

If Astral is in a container, a tunnel on the host does not become available through the container's own `127.0.0.1`. Use the HTTPS/WSS approach below, or arrange a loopback tunnel within the same network environment you manage for Astral.

## Astral in a container or behind an HTTPS reverse proxy

Provide encrypted endpoints that Astral can reach for both OneBot services:

1. Make sure the proxy can reach NapCat's HTTP and forward WebSocket services. On the same Docker network, its upstreams can use actual service names and ports such as `napcat:3000` and `napcat:3001`. NapCat listens on `0.0.0.0` inside its container.
2. Use a TLS certificate trusted by Astral's host and matching the domain. Route HTTPS to the HTTP service and WSS to the forward WebSocket service. Preserve the Authorization header.
3. Enable WebSocket upgrades in the proxy. For Nginx, see the [official WebSocket proxy configuration](https://nginx.org/en/docs/http/websocket.html). Restrict access to Astral's network, keep OneBot token authentication, and keep WebUI private.
4. Enter your actual endpoints in Astral, for example `https://qq-api.example.com` and `wss://qq-events.example.com`. Both must reach the same QQ account. Do not route WebSocket to the API-only `/api` endpoint.

Do not enter a non-loopback plaintext address such as `http://napcat:3000` or `ws://192.168.1.20:3001` directly in Astral. A VPN provides network reachability without changing Astral's URL requirements.

If NapCat runs natively on the host and the proxy runs in a container, the proxy also needs a reachable host address. `host.docker.internal` works only where the runtime provides it; Linux setups may need a host-gateway mapping. A host service bound only to `127.0.0.1` is normally unreachable from a bridge-network container.

## Open only the remote management page

Start a WebUI tunnel on your own computer:

```sh
ssh -N -o ExitOnForwardFailure=yes \
  -L 127.0.0.1:16099:127.0.0.1:6099 USER@NAPCAT_HOST
```

Replace the SSH identity and actual WebUI port. Open `http://127.0.0.1:16099/webui/` using the management credentials from NapCat's log, and leave the terminal running.

This tunnel only opens NapCat's management page. QQ messages still need the HTTP and WebSocket connections prepared above. Return to the [QQ guide](/docs/chat/qq/#napcat) to enable the services, connect the account, and test a chat.
