# Deployment and operations

## Data directories

The default root is the operating system's user configuration directory plus `astral`: `~/Library/Application Support/astral` on macOS, `%APPDATA%\astral` on Windows, and `$XDG_CONFIG_HOME/astral` (normally `~/.config/astral`) on Linux.

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

Copy the appropriate binary and, for enrollment downloads and container creation, all six distribution binaries to the host. The Web app is embedded; Node and pnpm are not needed at runtime.

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

The installer stores versioned executables under `$XDG_DATA_HOME/astral/bin/<sha256>/astral` (or `~/.local/share/astral/bin/…`) on macOS/Linux, and `%LOCALAPPDATA%\Astral\bin\<sha256>\astral.exe` on Windows. Daemon state stays under the default root's `computers/<server-hash>/<enrollment-id>` unless `--root` is supplied. Program files and Agent homes are separate.

Native enrollment automatically installs **Astral Daemon (<instance-id>)** for the current OS user. macOS uses a LaunchAgent, visible in System Settings → General → Login Items & Extensions; Windows uses a visible logon task under Task Scheduler → Astral, with `InteractiveToken` and `LeastPrivilege`; Linux uses a systemd user unit visible through `systemctl --user`. Installation does not elevate or enable Linux lingering. The user must have an available login/user service session. A desktop-managed local Daemon remains owned by its Server, and a container Daemon remains owned by Docker.

Use the exact executable printed by installation and the Computer data root for local maintenance:

```sh
astral computer service status --root DIRECTORY
astral computer service logs --root DIRECTORY
astral computer service stop --root DIRECTORY
astral computer service start --root DIRECTORY
astral computer service restart --root DIRECTORY
astral computer service uninstall --root DIRECTORY
```

Service status reports the local process manager; the Computers page separately reports authenticated Server connectivity. A running service alone does not mean that its Computer is online. `--foreground` skips service registration for an existing supervisor or a disposable test.

Container mode creates a Ubuntu 24.04 environment with Bash, Git/LFS, search/network/build tools, Python with uv and common data/document libraries, Node LTS/pnpm, Go, Rust/Cargo, Java 21/Maven, LibreOffice/Pandoc/Poppler, FFmpeg/ImageMagick/Tesseract, Chinese and emoji fonts, and Playwright Chromium. Language and Python package versions are pinned in `internal/platform/computer-image/Dockerfile`; Ubuntu security packages are resolved at image build. The image is intentionally substantial and the first build/download takes longer. Agent-installed software lives in the container writable layer as well as its data volume.

Local Docker endpoints include Unix sockets and the Windows Docker Engine named pipe. Docker Desktop must run Linux containers. The shortcut does not require enabling Docker Desktop host networking: same-host containers use a dedicated HTTPS bridge via `host.docker.internal` with its pinned certificate. Its persistent port and certificate let enrolled containers reconnect after desktop/server restarts. This listener exposes enrollment and authenticated Daemon transport only. It does not expose the Owner API or Web UI. Remote Docker contexts are rejected by the local shortcut.

### Updating an enrolled Computer

After updating the Desktop/Server distribution, open Computer details and copy the update command for the target computer's shell. Run it locally on the enrolled machine, or on the Docker host for a container. The command fetches and checks the new executable; temporary enrollment tokens are not reused. Native updates switch the user service to the new versioned executable and verify a new Daemon boot/version is online. Failure attempts to restore the previous service executable.

Container updates transfer and verify the Linux executable inside the existing container, replace only `/usr/local/bin/astral`, and restart the same container. The container identity, writable layer, data volume and credentials remain. Failure attempts to restore the previous binary. Updating does not rebuild the container to pick up a new Ubuntu/tool image. New environments use the new recipe; replacing an existing environment is a separate user decision. Restarting interrupts active tasks, so run the command when those tasks can be stopped.

The creator passes container name, creator hostname, image, data volume and mount path through `ASTRAL_CONTAINER_INFO`. The Daemon reports those values and its actual data root; the Server retains them for offline inspection. No Docker socket or host management service is added. Older local containers can recover their recorded name and volume from provisioning records; other unavailable fields remain unknown until reported by an updated Daemon.

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

Use `astral request METHOD /api/v1/PATH --body-file FILE` for other documented Owner API operations. The running server exposes `/openapi.json`. Agent commands use the bound loopback URL in the latest environment snapshot’s `local_api`; each Agent on a Computer receives its own port and view. they do not receive Owner, model-provider or Telegram credentials.

## Telegram

The bot must be available to Astral long polling. An existing webhook is reported as a conflict. The UI verifies the bot identity and shows platform diagnostics. If Telegram privacy mode prevents ordinary group observations, adjust that bot's settings or group access deliberately; Astral does not claim to receive events Telegram withholds.

Configure accounts in Settings → Bots → choose a Bot → Connect a platform → Telegram. Existing accounts are listed under that Bot's Telegram navigation item. Verify a Chat ID from the account's chat picker. Topics use the same Chat ID with a separate Topic ID. A Conversation has one enabled binding. Choose a new Session or an existing unbound Session belonging to the account's Bot. Reassignment stops both Sessions before changing the binding, preserving their histories and the original destination of pending outbound intents. See [IM accounts and Computer API](im.md) for scope, proactive mode and explicit sending.

## Desktop troubleshooting

Desktop stores its current data under the OS user configuration directory in `astral/schema-3/`. The CLI accepts explicit `--data` and `--computers-dir` paths when opening that same data through headless mode. Do not run two servers against one data directory simultaneously; the process lock reports the conflict. For an isolated desktop test, set `ASTRAL_DESKTOP_DATA_DIR` and optionally `ASTRAL_SERVER_LISTEN`. `ASTRAL_PUBLIC_URL`, `ASTRAL_TLS_CERT` and `ASTRAL_TLS_KEY` expose the corresponding managed-server configuration.

The desktop main process owns the Go child. A normal quit waits for service shutdown; a crashed parent is detected by the server. An enrolled user-service Daemon and a container Computer have their own lifecycles.

Development packages are unsigned and unnotarized. A distributable signed release requires an Apple signing identity and notarization configuration; see [Electron's release requirements](https://www.electronjs.org/docs/latest/tutorial/code-signing). The build does not include credentials or silently attempt those steps.
