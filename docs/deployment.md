# Deployment and operations

## Data directories

The default root is the operating system's user configuration directory plus `astral`: `~/Library/Application Support/astral` on macOS and `$XDG_CONFIG_HOME/astral` (normally `~/.config/astral`) on Linux.

| Location | Contents |
| --- | --- |
| `server/astral.db` | Versioned resources, IM observations, routing and delivery state |
| `server/core.db` | Durable Core rollouts, actual model requests and frozen multimodal inputs |
| `server/master.key` | Key for encrypted provider/channel credentials |
| `server/owner.token` | Owner access credential |
| `computers/local/` | Local Computer identity, Daemon state and AgentHome directories |
| `computers/provisioning/` | Local-container enrollment and provisioning files |

Use `--data` and `--computers-dir` to choose separate locations. Original attachments and working files belong to their Computer; remote files and container volumes are not stored in the server database. Frozen image payloads inside `core.db` are part of conversation history.

Back up the server data directory, including its encryption key, while the server is stopped or with a consistent SQLite backup procedure. Back up Computer homes and container volumes independently. Restoring configuration without `master.key` cannot decrypt saved credentials. Removing or revoking a resource in Settings preserves existing history and Computer files.

## Linux with HTTPS

Copy the appropriate binary and, for enrollment downloads and container creation, all four distribution binaries to the host. The Web app is embedded; Node and pnpm are not needed at runtime.

```sh
/opt/astral/bin/astral serve \
  --listen 127.0.0.1:4545 \
  --public-url https://astral.example.com \
  --data /var/lib/astral/server \
  --computers-dir /var/lib/astral/computers \
  --distribution-dir /opt/astral/distribution
```

Run the service as the account whose permissions the Local Computer should have. Commands execute directly as that account. Local AgentHome boundaries organize files; they do not form an operating-system sandbox.

A minimal Caddy reverse proxy is:

```caddyfile
astral.example.com {
    reverse_proxy 127.0.0.1:4545
}
```

The proxy must pass WebSocket upgrades and stream SSE responses without buffering. Caddy handles both in this configuration; see its [streaming documentation](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy#streaming). Keep the HTTP backend on loopback when TLS terminates at the proxy. Public access and cross-host Daemon connections use HTTPS/WSS. The server can alternatively terminate TLS with `--tls-cert` and `--tls-key`.

Example systemd unit, after creating an `astral` service account and writable data directories:

```ini
[Unit]
Description=Astral server and local Computer
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=astral
Group=astral
WorkingDirectory=/opt/astral
ExecStart=/opt/astral/bin/astral serve --listen 127.0.0.1:4545 --public-url https://astral.example.com --data /var/lib/astral/server --computers-dir /var/lib/astral/computers --distribution-dir /opt/astral/distribution
Restart=on-failure
RestartSec=3
TimeoutStopSec=30
UMask=0077

[Install]
WantedBy=multi-user.target
```

The service account needs access to the local Docker runtime only if you use the local-container shortcut. The target shown in the Web interface is this Linux server, regardless of the browser's operating system.

## Remote and container Computers

Generate an enrollment command in Settings and run it on the intended machine. The installer selects its OS/architecture, verifies the downloaded binary against the server's SHA-256 manifest, then registers the Daemon. Enrollment credentials expire after 15 minutes; the Daemon stores its own credential privately after registration. Repeating a successful command reuses the existing identity.

The installer executable is placed under `$XDG_DATA_HOME/astral/bin` or `~/.local/share/astral/bin`. Daemon state is under the default root's `computers/<server-hash>/<enrollment-id>` unless `--root` is supplied. The normal command starts a background Daemon and reports success only after its authenticated connection is online. `--foreground` is suitable for a process supervisor. For restart after OS reboot, supervise `astral computer daemon --root DIRECTORY --credentials DIRECTORY/credentials.json` using the target machine's service manager.

Container mode uses a Debian image with Bash, CA certificates, curl, Git, Python and ripgrep. It creates an independently identified container and a persistent volume. The generated enrollment file is mounted read-only. Linux Docker and OrbStack work with the current host-networking setup. Docker Desktop requires its [host-networking feature](https://docs.docker.com/engine/network/drivers/host/#docker-desktop) to be enabled. A remote Docker context is rejected by the local shortcut.

Container names are `astral-<operation-identity>` and volumes append `-data`; both carry an `astral.operation` label. Revocation disconnects the Computer and prevents further dispatch. It does not delete the container or its data volume. Stop or remove those explicitly on their host when no longer needed.

For a remote network, the enrollment URL must be reachable from the target machine. Astral does not create a VPN, NAT tunnel or relay.

## Owner access and CLI

The Owner controls one server data domain. Use its private token file to connect the CLI:

```sh
astral connect --server https://astral.example.com --token-file /private/path/owner.token
astral doctor
astral resources computers
astral resources agents
astral sessions new --agent AGENT_ID --title "Research"
astral sessions send SESSION_ID --text "Inspect the project"
astral sessions history SESSION_ID
astral sessions export SESSION_ID
astral sessions stop SESSION_ID
```

Use `astral request METHOD /api/v1/PATH --body-file FILE` for other documented Owner API operations. The running server exposes `/openapi.json`. Agent commands use their bound loopback `ASTRAL_API_URL`; they do not receive Owner, model-provider or Telegram credentials.

## Telegram

The bot must be available to Astral long polling. An existing webhook is reported as a conflict. The UI verifies the bot identity and shows platform diagnostics. If Telegram privacy mode prevents ordinary group observations, adjust that bot's settings or group access deliberately; Astral does not claim to receive events Telegram withholds.

Verify a Chat ID from Channels or the Agent's Conversation picker. Topics use the same Chat ID with a separate Topic ID. A Conversation has one enabled binding. Reassignment creates a new persistent Session, preserving the old history and the original destination of pending outbound intents.

For sends with an uncertain result, inspect the receipt and the destination before deciding on any manual resend. Re-enabling a Connection does not turn an unknown receipt into a confirmed success.

## Desktop troubleshooting

The default app uses the same macOS data root as the CLI. Do not run two servers against that directory simultaneously; the process lock reports the conflict. For an isolated desktop test, set `ASTRAL_DESKTOP_DATA_DIR` and optionally `ASTRAL_SERVER_LISTEN`. `ASTRAL_PUBLIC_URL`, `ASTRAL_TLS_CERT` and `ASTRAL_TLS_KEY` expose the corresponding managed-server configuration.

The desktop main process owns the Go child. A normal quit waits for service shutdown; a crashed parent is detected by the server. A detached remote Daemon and a container Computer have their own lifecycles.

Development packages are unsigned and unnotarized. A distributable signed release requires an Apple signing identity and notarization configuration; see [Electron's release requirements](https://www.electronjs.org/docs/latest/tutorial/code-signing). The build does not include credentials or silently attempt those steps.
