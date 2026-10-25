# Deployment and operations

## Data directories

The default root is the operating system's user configuration directory plus `astral`: `~/Library/Application Support/astral` on macOS, `%APPDATA%\astral` on Windows, and `$XDG_CONFIG_HOME/astral` (normally `~/.config/astral`) on Linux.

| Location | Contents |
| --- | --- |
| `server/astral.db` | Resources, IM state, the administrator account and browser-session records |
| `server/core.db` | Durable Core rollouts, actual model requests and frozen multimodal inputs |
| `server/master.key` | Key for encrypted provider/channel credentials and account secrets |
| `server/computer-identity/identity.pem` | Persistent private Server identity and its 100-year self-signed tunnel certificate |
| `server/network.json` | Saved listener, access-address list and remote-management preference |
| `server/control.json` | Private, process-lifetime connection information for local account administration |
| `computers/local/` | Local Computer identity, Daemon state and AgentHome directories |
| `computers/provisioning/` | Local-container enrollment and provisioning files |

Use `--data` and `--computers-dir` to choose separate locations. Original attachments and working files belong to their Computer; remote files and container volumes are not stored in the Server database. Frozen image payloads inside `core.db` are part of conversation history.

Back up the complete Server data directory while the Server is stopped or with a consistent SQLite backup procedure. Preserve `master.key` and the Computer tunnel identity along with the databases: the first decrypts saved credentials, and the second preserves enrolled Daemons' trust in this Server. Back up Computer homes and container volumes independently. The old `owner.token` login mechanism is removed; there is no old-token exchange or legacy authentication endpoint.

## Listener, addresses and remote management

Astral serves HTTP on `0.0.0.0:4545` by default. The Web UI, management API and encrypted Computer tunnel share this main port. Astral does not terminate browser HTTPS; configure a reverse proxy or Tailscale Serve when desired. Username/password and TOTP login work over either HTTP or HTTPS. Passkeys require an actual HTTPS browser page and a supported WebAuthn implementation.

**Allow remote management** defaults to off. Direct remote connections can reach the health check and Computer tunnel; the Web UI, sign-in, OpenAPI and management APIs are unavailable until this switch is enabled. Enabling it still requires account authentication. A remote-management request is classified by its actual TCP peer, never by `Host`, `Origin` or a forwarded-address header. An intentional loopback proxy, SSH tunnel or Tailscale Serve can expose the complete local management entry while the switch is off; it still cannot bypass Web login.

**Access addresses** is a list of complete HTTP/HTTPS origins. The first configured address is the default for new enrollment commands; the Add Computer dialog can select another discovered or custom address. Addresses do not change the listener or enable remote management. An empty list selects automatic discovery: an actual Tailscale Serve HTTPS proxy for this Server, full MagicDNS name, Tailscale IP, LAN IP and finally loopback. Installing Tailscale alone does not imply an HTTPS endpoint. Changing this list does not rewrite existing Daemon connection files.

Each network field resolves independently in this order:

1. Explicit `--listen`, repeated `--public-url`, or `--allow-remote-management[=false]` arguments.
2. `ASTRAL_SERVER_LISTEN`, `ASTRAL_PUBLIC_URLS`, or `ASTRAL_ALLOW_REMOTE_MANAGEMENT`.
3. Saved App settings in `server/network.json`.
4. Address discovery, then defaults.

The address list is replaced as a whole at the selected priority; lists are not merged. `--public-url ''` or `ASTRAL_PUBLIC_URLS='[]'` selects automatic discovery. An explicit false overrides a saved true. Process overrides do not overwrite saved settings; the UI shows the source and disables editing controlled fields. Addresses and remote management apply immediately; listener changes require a restart. Loopback-only binding prevents Docker Desktop containers from reaching the main port.

```sh
astral serve --listen 0.0.0.0:4545 --allow-remote-management \
  --public-url https://astral.example.com \
  --public-url http://100.64.0.10:4545
```

Equivalent environment values include:

```sh
export ASTRAL_SERVER_LISTEN='0.0.0.0:4545'
export ASTRAL_PUBLIC_URLS='["https://astral.example.com","http://100.64.0.10:4545"]'
export ASTRAL_ALLOW_REMOTE_MANAGEMENT=true
```

Management Host and cross-site checks are enabled by default. Allowed hostnames and origins are derived from explicit access addresses, loopback, actual local interfaces and reliable discovered addresses. Arbitrary incoming hosts are not learned, wildcard bind addresses are not access hostnames, and the Computer tunnel uses its own pinned authentication boundary.

## Administrator login and browser devices

Desktop opens directly into its own local Server. Its main process sends a fresh random identity through the child's private stdin at startup and attaches it only to requests for that Server. This identity lasts for the managed process, is not a browser Cookie and is not exposed to renderer JavaScript. Localhost by itself grants no Desktop identity. Desktop does not appear in the Web device list and cannot be signed out by Web-session deletion, password changes or account recovery.

Web login uses one administrator account. A Desktop user creates it in **Settings → Security & Access** when browser access is needed. Headless administrators initialize or recover it through the deployment machine's local CLI:

```sh
astral auth setup --data /path/to/server --username admin
astral auth reset --data /path/to/server --username admin
```

These commands prompt for a hidden password and confirmation. They use the running Server's private loopback HTTPS control channel, protected by the pinned Server identity, a process-lifetime secret and data-directory permissions. They also work offline while holding the ordinary exclusive Server data lock. Account reset removes TOTP/Passkeys and revokes all Web logins; it does not remove Bots, conversations, Computer identities or Desktop access. No public first-visitor administrator registration or email recovery endpoint exists.

Passwords use salted Argon2id hashes. A successful login creates a random browser-session token; the Server persists its digest and expiry. Cookies are HttpOnly and SameSite Strict, with Secure on HTTPS. Sessions default to 30 days, enforced by the Server. HTTP login is supported without a separate unsafe-login switch.

Public authentication requests have a 128 KiB body limit and a 10-second body-read deadline. At most eight requests per actual TCP peer and 64 globally may read authentication bodies concurrently; credential attempts have separate rate limits. These bounds apply to authentication requests, not file transfers or long-running Agent work.

Adding and confirming either TOTP or a Passkey automatically enables the second step; there is no independent 2FA switch. Password login then requires one available second factor: TOTP, Passkey on HTTPS, or an unused recovery code. The **Allow Passkey passwordless login** switch defaults off and requires at least one Passkey. When enabled, Passkey login can skip username, password and TOTP; choosing password login still requires the second step. Removing the final Passkey also disables passwordless login; removing the final authentication method disables 2FA and removes its recovery codes.

Passkeys are created as discoverable credentials and require user verification such as fingerprint, face or device PIN. Their RP ID is the HTTPS hostname; registering one domain does not make that credential usable at every configured IP/domain. HTTP pages cannot register or use them, including the localhost development exception. Desktop directs Passkey setup to the configured HTTPS Web page. A password-plus-Passkey-only account must use HTTPS for that factor, or use a saved recovery code; HTTP does not silently skip 2FA.

**Logged-in devices** lists all unexpired Web sessions, including offline browsers and CLI logins, with user agent, login/last-used times and a current-device marker. One row represents a browser/CLI login session, not a physical hardware identity. Deletion revokes the credential immediately and ends its bound SSE/other requests; offline clients are rejected on their next request. Sign out all devices and account/password resets revoke Web sessions without touching Desktop or Daemons. Deleting a Web session does not delete its Passkey. Sensitive settings changes and remote-session deletion require recent complete verification; Desktop is its own trusted local recovery entry.

Completing that verification rotates the session's random credential; the previous credential cannot authorize new requests or inherit the renewed verification window. The session ID, already authenticated streams and original absolute expiry stay the same. Changing authentication methods revokes other Web sessions; changing the password revokes all of them.

## Runtime options and unattended initialization

Explicit CLI arguments take priority over their corresponding environment values. Only network preferences additionally use saved App settings and discovery.

| CLI option | Environment | Meaning |
| --- | --- | --- |
| `--data` | `ASTRAL_SERVER_DATA_DIR` | Server data directory |
| `--computers-dir` | `ASTRAL_COMPUTER_DATA_DIR` | Local Daemon/provisioning data directory |
| `--web-dir` | `ASTRAL_WEB_DIR` | External shared Web build, when not using the embedded UI |
| `--distribution-dir` | `ASTRAL_DISTRIBUTION_DIR` | Local release binaries used for creating local Computer containers |
| `--no-local` | `ASTRAL_NO_LOCAL` | Do not start the local Daemon automatically |
| repeated `--trusted-proxy` | `ASTRAL_TRUSTED_PROXIES` | Trusted reverse-proxy IPs/CIDRs; environment is a JSON array |
| `--web-session-ttl` | `ASTRAL_WEB_SESSION_TTL` | Positive Go duration; default `720h` |
| `--initial-admin-username` | `ASTRAL_ADMIN_USERNAME` | First administrator username |
| `--initial-admin-password-file` | `ASTRAL_ADMIN_PASSWORD_FILE` or `ASTRAL_ADMIN_PASSWORD` | First administrator password; direct environment and file forms are mutually exclusive |

Initial administrator inputs apply only when no Web account exists; they never reset an existing password, factors or sessions on restart. For unattended deployment, supply the username and a private password file, for example:

```sh
ASTRAL_ADMIN_USERNAME=admin \
ASTRAL_ADMIN_PASSWORD_FILE=/run/secrets/astral-admin-password \
ASTRAL_SERVER_DATA_DIR=/var/lib/astral/server \
astral serve
```

The password file is read at startup; keep it private. To recover an existing account, use `astral auth reset` deliberately. The CLI does not accept a plaintext password command-line argument.

## Linux behind an HTTPS proxy

The shared Web app is embedded in the unified binary; Node and pnpm are not runtime dependencies. All six distribution binaries are useful when this Server creates local Computer containers; remote enrollment and maintenance installers fetch their programs from official HTTPS release assets instead of this Server.

```sh
/opt/astral/bin/astral serve \
  --listen 127.0.0.1:4545 \
  --public-url https://astral.example.com \
  --data /var/lib/astral/server \
  --computers-dir /var/lib/astral/computers \
  --distribution-dir /opt/astral/distribution
```

Run as the OS user whose permissions the local Computer should have. A user-service unit can launch the command above with `Restart=on-failure`, `UMask=0077` and `WantedBy=default.target`; the [headless installer](headless-install.md) manages this service without a system-wide daemon or elevation. Commands on a native Computer execute as that user; AgentHome is not an OS sandbox.

A minimal Caddy configuration is:

```caddyfile
astral.example.com {
    reverse_proxy 127.0.0.1:4545
}
```

Proxies must preserve the external Host, pass WebSocket upgrades and stream SSE without buffering. By default only loopback peers are trusted to report `X-Forwarded-Proto: https`; other proxy addresses require `--trusted-proxy IP_OR_CIDR` or `ASTRAL_TRUSTED_PROXIES='["172.20.0.5"]'`. An explicit empty list/argument disables proxy trust. Forwarded Host and client-address headers do not establish trust. The external HTTPS address must also be an allowed access address, so Passkey origin checks and Secure Cookies reflect the browser's actual URL.

## Docker-hosted Server

This authentication and network model supports a Server inside a container: persist its complete Server data directory, publish the main HTTP port, configure the externally reachable access address, explicitly enable remote management and initialize the administrator locally or through the initial-account environment values. A separate proxy container is a remote peer; trust its exact IP/CIDR when it terminates HTTPS. Container-local address discovery cannot infer the host's published port, NAT address or public domain.

There is currently no official Server image or Compose package. The Ubuntu image described below is the Computer/Agent execution environment, not a Server deployment image. A Docker-hosted Server's local Computer executes inside that container, not on its host; `--no-local` lets a deployment use separate Daemons. Do not assume mounting a Docker socket alone makes the host-container shortcut work: Docker-host paths and the chosen Server address must be valid from the actual execution environment.

## Remote and container Computers

Generate an enrollment command in Settings and run it on the intended machine. The command includes a temporary token and the Server's public SPKI SHA-256 pin. The installer script and programs are fetched from `oines/astral`'s official HTTPS release assets, with SHA-256 verification. An HTTP Server cannot replace those executables or their trusted bootstrap script. The Server no longer serves `/install.sh`, `/install.ps1` or `/downloads/`.

Server/Daemon enrollment, container preparation, status and RPC use `GET /internal/computer-tunnel`: WS or WSS transports an inner TLS 1.3 connection. The Daemon checks the copied Server pin before sending enrollment or long-term credentials. Outer HTTPS retains normal CA/hostname verification; inner TLS remains required even over WSS. There is no plaintext fallback and no credential or Hello metadata in outer headers/query strings. VPN is optional; Astral does not create NAT traversal or a relay.

The Server identity is generated once, stored with its data and uses a 100-year self-signed certificate with no renewal process. Daemons pin the public key, not a domain or exact certificate bytes. Preserve that identity when moving the Server; changing the network address does not require changing its identity. A mismatch fails instead of silently accepting a replacement.

Temporary registration tokens expire after 15 minutes and are consumed on successful registration. Container preparation can extend the fixed deadline to 90 minutes after enrollment creation. A consumed command cannot recover long-term credentials. Repeating a successful command on the same machine can reuse its saved local credentials; it does not re-register with the consumed token. Until used or expired, treat the entire command as a secret. Each Computer receives its own long-term token, stored privately; revocation closes its connection and blocks further authentication.

The installer stores versioned executables under `$XDG_DATA_HOME/astral/bin/<sha256>/astral` (or `~/.local/share/astral/bin/…`) on macOS/Linux, and `%LOCALAPPDATA%\Astral\bin\<sha256>\astral.exe` on Windows. Daemon state stays under the default root's `computers/<server-hash>/<enrollment-id>` unless `--root` is supplied. Program files and Agent homes are separate. Multiple native instances may use distinct roots to connect to different Servers; each container has its own instance.

Native enrollment installs **Astral Daemon (<instance-id>)** for the current user. macOS uses a LaunchAgent visible in System Settings → General → Login Items & Extensions; Windows uses a visible Task Scheduler → Astral logon task with `InteractiveToken` and `LeastPrivilege`; Linux uses `systemctl --user`. Windows PowerShell enrollment/update bootstrap uses the `curl.exe` included with modern Windows, not WSL. Installation does not elevate or enable Linux lingering. The user needs an available login/user-service session. Desktop manages its own local Daemon lifecycle, and Docker manages container Daemons.

```sh
astral computer status --root DIRECTORY
astral computer service status --root DIRECTORY
astral computer service logs --root DIRECTORY
astral computer service stop --root DIRECTORY
astral computer service start --root DIRECTORY
astral computer service restart --root DIRECTORY
astral computer service uninstall --root DIRECTORY
```

`computer status` authenticates through the pinned encrypted tunnel. `computer service status` inspects the local process manager; a running service alone does not prove Server connectivity. `--foreground` skips service installation and keeps the enrolled Daemon attached to the terminal until stopped.

Container mode creates Ubuntu 24.04 with Bash, Git/LFS, search/network/build tools, Python/uv and common data/document libraries, Node LTS/pnpm, Go, Rust/Cargo, Java 21/Maven, LibreOffice/Pandoc/Poppler, FFmpeg/ImageMagick/Tesseract, Chinese/emoji fonts and Playwright Chromium. Language and Python versions are pinned in `internal/platform/computer-image/Dockerfile`; Ubuntu security packages resolve at build time. The first build/download is substantial. Installed software lives in the writable layer as well as its data volume.

Docker Desktop uses Linux containers and reaches a confirmed same-host Server through `http://host.docker.internal:4545` on the configured main port. It does not require Docker Desktop host networking or a dedicated TLS bridge. That HTTP route still carries the inner pinned TLS tunnel. Linux Docker and OrbStack use their host-network path. Remote containers retain the selected reachable enrollment address; remote Docker contexts are rejected by the local shortcut.

### Updating an enrolled Computer

Open Computer details and copy the shell/PowerShell update command. Run it on the enrolled machine, or on the Docker host for a container. The command fetches and checks the new official HTTPS release program; temporary enrollment tokens are not reused. Update commands do not accept `--server` or a replacement pin and do not preflight the Server's current preferred address. Status/reconnection uses the enrolled machine's existing `credentials.json` URL and pin, so computers paired through a second access address remain updatable. The saved Server pin is retained, never replaced by an update response.

After downloading a verified release program, its local commands are:

```sh
astral computer update --root /path/to/existing-daemon
astral computer update --container astral-EXISTING-CONTAINER
```

Native updates switch the user service executable and verify a new Daemon boot/version; failure attempts to restore the previous service executable.

Container updates transfer and verify the Linux executable, replace only `/usr/local/bin/astral` and restart the same container. The container ID, writable layer, data volume, credentials and installed software remain. Failure attempts to restore the previous binary. No image rebuild or Agent-environment replacement occurs. Restarting interrupts active tasks.

The creator supplies name, host, image, volume and mount metadata through `ASTRAL_CONTAINER_INFO`; the Daemon reports its actual root. The Server retains this information for offline inspection. No Docker socket or additional host service is added. Containers are named `astral-<operation-identity>` and their volumes append `-data`, with `astral.operation` labels. Revocation does not delete their container, data volume or files; remove those deliberately on the host when no longer needed.

## Web account and CLI

CLI management uses the same administrator account and Web-session lifecycle:

```sh
astral connect --server https://astral.example.com --username admin
astral doctor
astral resources computers
astral resources agents
astral sessions new --agent AGENT_ID --title "Research"
astral sessions send SESSION_ID --text "Inspect the project"
astral sessions history SESSION_ID
astral sessions export SESSION_ID
astral sessions stop SESSION_ID
```

Login prompts for a hidden password and, if needed, a TOTP or recovery code. Interactive browser Passkey ceremonies are not performed by this CLI. Its login credential is stored privately in `client.session`, while `client.json` records the selected Server and session-file path; these live under the default OS Astral root. The CLI session appears in logged-in devices and can be revoked. It does not use the Desktop identity or an Owner bearer token.

Use `astral request METHOD /api/v1/PATH --body-file FILE` for other management operations. `/openapi.json` describes the current routes. Agent commands use the separate bound loopback API in the latest environment snapshot's `local_api`; that per-Agent view never receives administrator, provider or IM account credentials.

## Telegram

The bot must be available to Astral long polling. An existing webhook is reported as a conflict. The UI verifies the bot identity and shows platform diagnostics. If Telegram privacy mode prevents ordinary group observations, adjust that bot's settings or group access deliberately; Astral does not claim to receive events Telegram withholds.

Configure accounts in Settings → Bots → choose a Bot → Connect a platform → Telegram. Existing accounts are listed under that Bot's Telegram navigation item. Verify a Chat ID from the account's chat picker. Topics use the same Chat ID with a separate Topic ID. A Conversation has one enabled binding. Choose a new Session or an existing unbound Session belonging to the account's Bot. Reassignment stops both Sessions before changing the binding, preserving their histories and the original destination of pending outbound intents. See [IM accounts and Computer API](im.md) for scope, proactive mode and explicit sending.

## Desktop troubleshooting

Desktop stores current data under the OS user configuration directory in `astral/schema-3/`. CLI `--data` and `--computers-dir` can open those same directories in headless mode, but never run two Servers against the same data concurrently. `ASTRAL_DESKTOP_DATA_DIR` selects an isolated Desktop data root. `ASTRAL_SERVER_LISTEN`, `ASTRAL_PUBLIC_URLS` and `ASTRAL_ALLOW_REMOTE_MANAGEMENT` configure its managed Server with environment priority; Desktop does not promote defaults or environment values into CLI overrides.

The main process owns the Go child. Quitting waits for shutdown; loss of the private stdin pipe or desktop parent ends the managed Server. Enrolled user-service and container Daemons have separate lifecycles. Restarting Desktop establishes a fresh private Desktop identity without requiring Web account credentials.

Development packages are unsigned and unnotarized. Distributable signed releases require Apple signing and notarization configuration through the [release process](releasing.md); build sources contain no signing credentials. Packaging alone does not establish native interactive acceptance or public release availability.
