# Deployment and operations

The public deployment walkthrough is [Run a Server](../apps/website/src/content/docs/docs/hosting/server.md). The official Linux installer is the default path for a persistent background Server; foreground commands and custom layouts below are advanced operator choices.

## Data directories

The default root is the operating system's user configuration directory plus `astral`: `~/Library/Application Support/astral` on macOS, `%APPDATA%\astral` on Windows, and `$XDG_CONFIG_HOME/astral` (normally `~/.config/astral`) on Linux.

| Location | Contents |
| --- | --- |
| `server/astral.db` | Resources, IM state, the administrator account and browser-session records |
| `server/core.db` | Durable Core rollouts, actual model requests and frozen multimodal inputs |
| `server/master.key` | Key for encrypted provider/channel credentials and account secrets |
| `server/computer-identity/identity.pem` | Persistent private Server identity and its 100-year self-signed tunnel certificate |
| `server/network.json` | Saved listener, compatibility access-address hints and hostname policy; Desktop also saves its remote-management preference |
| `server/control.json` | Private, process-lifetime connection information for local account administration |
| `computers/local/` | Local Computer identity, Daemon state and AgentHome directories |
| `computers/provisioning/` | Local-container enrollment and provisioning files |

Use `--data` and `--computers-dir` to choose separate locations. Original attachments and working files belong to their Computer; remote files and container volumes are not stored in the Server database. Frozen image payloads inside `core.db` are part of conversation history.

The official Linux installer uses a separate installation root, `${XDG_DATA_HOME:-$HOME/.local/share}/astral-server` by default: Server data is `data/server/`, Computer data is `data/computers/`, automatic upgrade snapshots are `data/upgrade-backups/`, and `installation.json` records managed-installation ownership/startup choices. Recovery `--data` targets `data/server`, not the installation root. Desktop instead adds `schema-3-core-11/` beneath its platform root.

Back up the complete Server data directory while the Server is stopped or with a consistent SQLite backup procedure. Preserve `master.key` and the Computer tunnel identity along with the databases: the first decrypts saved credentials, and the second preserves enrolled Daemons' trust in this Server. Back up Computer homes and container volumes independently. The old `owner.token` login mechanism is removed; there is no old-token exchange or legacy authentication endpoint.

Before restoring, stop the actual process or service and verify it is inactive; retain a separate copy of current data. Restore a consistent backup to the same data path with the original ownership/private permissions, then start a matching program using the original startup configuration. An automatic upgrade snapshot's manifest and `files/` layout is not a ready-to-run Server directory. Do not move data alone and retry the old startup command: it still targets the old path and can create empty data there. Managed installation paths, metadata and service definitions must remain consistent.

## Official Linux background service

The installer keeps Astral under the current account's `systemd --user` manager. Before installation changes, it prepares lingering as needed and starts `user@UID.service` only when the manager is not running. Ordinary accounts can be prompted for sudo for these preparations and for a command entry when required. Server/Daemon execution still uses the original account; Docker installation/group access is separate. Successful installation keeps running after SSH logout and starts at boot.

Use the installation account for these operations:

| Operation | Command |
| --- | --- |
| Status | `systemctl --user status astral-server.service` |
| Start | `systemctl --user start astral-server.service` |
| Stop | `systemctl --user stop astral-server.service` |
| Restart | `systemctl --user restart astral-server.service` |
| Enable at boot | `systemctl --user enable astral-server.service` |
| Disable at boot | `systemctl --user disable astral-server.service` |
| Logs | `journalctl --user -u astral-server.service` |

Enable/disable does not start/stop the current process. Uninstall with the downloaded installer, `bash install-server.sh --uninstall`, adding the original `--root ABSOLUTE_PATH` for a custom installation. No separate Astral uninstall command is added. Ownership and shutdown must be verified before cleanup. Uninstallation removes the service and safely removable programs/links, retaining shared command/program use, all data/configuration/identities/backups and one complete verified release for reinstallation and recovery. It does not disable account-wide lingering or remove Docker/remote Computers.

## Listener, addresses and remote management

Astral serves HTTP on `0.0.0.0:4545` by default. Web management and the encrypted Computer tunnel share the main port. HTTPS is provided by a reverse proxy or Tailscale Serve.

A standalone Server, without Desktop managing it, enables remote management by default. Web settings cannot close this primary interface. An explicit `--allow-remote-management=false` or `ASTRAL_ALLOW_REMOTE_MANAGEMENT=false` can close direct remote management. Desktop retains its default-off, independently editable setting. An old App-saved false is not a standalone Server override. The official installer persists an explicit operator choice in `installation.json` and the user unit environment; omitted installer options retain that choice.

With remote management closed, actual loopback TCP peers still reach the authenticated management entry; remote peers can reach only health and the Computer tunnel. Host, Origin and forwarded client-address headers cannot make a remote peer local. Local proxies and SSH forwarding intentionally reach the local entry without granting a login identity. Listener scope remains independent.

Fresh standalone Servers accept valid hostnames without URL registration. Desktop and existing installations retain their prior Host restriction. **Restrict access hostnames** and its allowed domain/IP list are one local draft: edit, then explicitly save the group. Web updates must retain the current hostname. Only management pages, sign-in, OpenAPI and management APIs use this policy; Daemon enrollment, status and RPC use the independently authenticated Computer tunnel. Machine-local recovery remains separate.

```sh
astral config hostnames show
astral config hostnames disable
```

These commands find the installation and use the private pinned local control endpoint, or acquire the ordinary Server data lock while offline. Disabling retains the list and does not bypass account authentication. Use `--data DIRECTORY` for an explicit deployment. CLI/environment hostname overrides are reported and cannot be replaced by a lower-priority Web preference.

Network precedence remains explicit CLI, environment, saved setting, discovery/default, except standalone remote management uses only CLI/environment and its enabled deployment default. `--restrict-hostnames` / `ASTRAL_RESTRICT_HOSTNAMES` controls the policy; repeated `--allowed-hostname` / JSON-array `ASTRAL_ALLOWED_HOSTNAMES` supplies explicit names. Allowed names contain no scheme, port, path or wildcard. CLI `false` remains distinct from absence.

There is no required global access-URL editor. The Computer enrollment dialog selects discovered addresses or a custom reachable URL and prefers the browser's current non-loopback origin. Existing `--public-url`, `ASTRAL_PUBLIC_URLS` and saved values remain compatibility hints for enrollment and older derived Host policies. Changing them never rewrites an enrolled Daemon's stored URL. Discovery considers actual Tailscale Serve, full MagicDNS, Tailscale IP, LAN and loopback; it never invents HTTPS. Loopback-only listening still prevents direct Docker Desktop or remote Computer access.

Account authentication and exact same-origin/cross-site checks always apply. Cookies remain host-only and HttpOnly with SameSite protection. Passkeys require an actual HTTPS domain and bind the current RP ID, Origin and one-time challenge; they no longer require a separately registered access URL on unrestricted Servers. Proxy trust is unchanged and incoming unknown Host values are never silently saved.

## Download proxy

Official GitHub release URLs remain the default. Set `ASTRAL_GITHUB_PROXY=https://gh-proxy.org/` or pass the installer's `--github-proxy` to select an explicit third-party HTTPS prefix. It routes only the fixed official Astral release API and assets, preserving release, manifest and digest checks. It never proxies Server access, login or enrollment credentials, and does not silently fall back to a third-party service.

The Server installer stores the normalized choice in its owned installation marker and unit environment; independent update jobs preserve that selection. An explicitly empty choice restores direct downloads. Computer installer commands also read the variable when fetching their bootstrap script, so the same selected source covers bootstrap and later downloads. Public use instructions and the complete fallback command are in the [Server guide](../apps/website/src/content/docs/docs/hosting/server.md).

## Administrator login and browser devices

Desktop opens directly into its own local Server. Its main process sends a fresh random identity through the child's private stdin at startup and attaches it only to requests for that Server. This identity lasts for the managed process, is not a browser Cookie and is not exposed to renderer JavaScript. Localhost by itself grants no Desktop identity. Desktop does not appear in the Web device list and cannot be signed out by Web-session deletion, password changes or account recovery.

Web login uses one administrator account. A Desktop user creates it in **Settings → Security & Access** when browser access is needed. Headless administrators initialize or recover it through the deployment machine's local CLI:

```sh
astral auth setup --username admin
astral auth reset --username admin
```

These commands prompt for a hidden password and confirmation. They use the running Server's private loopback HTTPS control channel, protected by the pinned Server identity, a process-lifetime secret and data-directory permissions. They also work offline while holding the ordinary exclusive Server data lock. Account reset removes TOTP/Passkeys and revokes all Web logins; it does not remove Bots, conversations, Computer identities or Desktop access. No public first-visitor administrator registration or email recovery endpoint exists.

Run as the Server's OS account. Default installed Servers are discovered automatically. An explicit `--data` or `ASTRAL_SERVER_DATA_DIR` selects a target; a custom manual deployment must match its startup data path. Ambiguous discovery requires a terminal choice or an explicit target for noninteractive use.

Passwords use salted Argon2id hashes. A successful login creates a random browser-session token; the Server persists its digest and expiry. Cookies are HttpOnly and SameSite Strict, with Secure on HTTPS. Sessions default to 30 days, enforced by the Server. HTTP login is supported without a separate unsafe-login switch.

HTTP and HTTPS use separate cookie names (`astral_session` and `__Host-astral_session`). HTTPS always sets Secure, Path `/` and no Domain. Each protocol reads only its own cookie, so switching between HTTP and HTTPS on the same hostname does not cause a Secure-cookie overwrite failure; each login and logout affects its own session.

Public authentication requests have a 128 KiB body limit and a 10-second body-read deadline. At most eight requests per actual TCP peer and 64 globally may read authentication bodies concurrently; credential attempts have separate rate limits. These bounds apply to authentication requests, not file transfers or long-running Agent work.

Adding and confirming either TOTP or a Passkey automatically enables the second step; there is no independent 2FA switch. Password login then requires one available second factor: TOTP, Passkey on HTTPS, or an unused recovery code. The **Allow Passkey passwordless login** switch defaults off and requires at least one Passkey. When enabled, Passkey login can skip username, password and TOTP; choosing password login still requires the second step. Removing the final Passkey also disables passwordless login; removing the final authentication method disables 2FA and removes its recovery codes.

Passkeys are created as discoverable credentials and require user verification such as fingerprint, face or device PIN. Their RP ID is the HTTPS hostname; registering one domain does not make that credential usable at every configured IP/domain. HTTP pages cannot register or use them, including the localhost development exception. Desktop offers a discovered HTTPS entry when available, or links to the HTTPS setup guide. A password-plus-Passkey-only account must use HTTPS for that factor, or use a saved recovery code; HTTP does not silently skip 2FA.

**Logged-in devices** lists all unexpired Web sessions, including offline browsers and CLI logins, with user agent, login/last-used times and a current-device marker. One row represents a browser/CLI login session, not a physical hardware identity. Deletion revokes the credential immediately and ends its bound SSE/other requests; offline clients are rejected on their next request. Sign out all devices and account/password resets revoke Web sessions without touching Desktop or Daemons. Deleting a Web session does not delete its Passkey. Sensitive settings changes and remote-session deletion require recent complete verification; Desktop is its own trusted local recovery entry.

Completing that verification rotates the session's random credential; the previous credential cannot authorize new requests or inherit the renewed verification window. The session ID, already authenticated streams and original absolute expiry stay the same. Changing authentication methods revokes other Web sessions; changing the username or password revokes all of them and requires signing in again.

## Runtime options and unattended initialization

Explicit CLI arguments take priority over their corresponding environment values. Only network preferences additionally use saved App settings and discovery.

| CLI option | Environment | Meaning |
| --- | --- | --- |
| `--data` | `ASTRAL_SERVER_DATA_DIR` | Server data directory |
| `--computers-dir` | `ASTRAL_COMPUTER_DATA_DIR` | Local Daemon/provisioning data directory |
| `--web-dir` | `ASTRAL_WEB_DIR` | External shared Web build, when not using the embedded UI |
| `--distribution-dir` | `ASTRAL_DISTRIBUTION_DIR` | Local release binaries used for creating local Computer containers |
| `--installation-root` | `ASTRAL_INSTALLATION_ROOT` | Official Linux user-service installation root; enables managed updates only after ownership and running-service validation |
| `--no-local` | `ASTRAL_NO_LOCAL` | Do not start the local Daemon automatically |
| repeated `--trusted-proxy` | `ASTRAL_TRUSTED_PROXIES` | Trusted reverse-proxy IPs/CIDRs; environment is a JSON array |
| `--web-session-ttl` | `ASTRAL_WEB_SESSION_TTL` | Positive Go duration; default `720h` |
| `--initial-admin-username` | `ASTRAL_ADMIN_USERNAME` | First administrator username |
| `--initial-admin-password-file` | `ASTRAL_ADMIN_PASSWORD_FILE` or `ASTRAL_ADMIN_PASSWORD` | First administrator password; direct environment and file forms are mutually exclusive |

The official Linux installer passes `--installation-root` automatically and writes matching installation metadata. `ASTRAL_INSTALLATION_ROOT` identifies that same root when no CLI value is supplied; it does not turn an arbitrary Server into a managed installation. Web updates also verify the current UID, owned root and data paths, exact official user unit, loaded unit and running PID/process arguments. Custom services and units with overrides remain manual. Re-run the official installer with the same user and root to prepare an older official installation that lacks this metadata.

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
  --data /var/lib/astral/server \
  --computers-dir /var/lib/astral/computers \
  --distribution-dir /opt/astral/distribution
```

Run as the OS user whose permissions the local Computer should have. A custom user-service unit can launch the command above with `Restart=on-failure`, `UMask=0077` and `WantedBy=default.target`; the [headless installer](headless-install.md) handles persistent service setup, user-manager preparation and command registration. Commands on a native Computer execute as that user; AgentHome is not an OS sandbox.

A minimal Caddy configuration is:

```caddyfile
astral.example.com {
    reverse_proxy 127.0.0.1:4545
}
```

Proxies must preserve the external Host, pass WebSocket upgrades and stream SSE without buffering. By default only loopback peers are trusted to report `X-Forwarded-Proto: https`; other proxy addresses require `--trusted-proxy IP_OR_CIDR` or `ASTRAL_TRUSTED_PROXIES='["172.20.0.5"]'`. An explicit empty list/argument disables proxy trust. Forwarded Host and client-address headers do not establish trust. When hostname restriction is enabled, its list must admit the external domain. Passkey origin checks and Secure Cookies use the browser's actual HTTPS origin, with no required access-URL registration on unrestricted Servers.

**Connection IP** in the administrator's Computer details describes the current authenticated Daemon connection. Direct connections show the TCP peer. If that peer is a trusted proxy, the Server reads `X-Forwarded-For` from right to left, skips trusted proxies, and takes the first valid address outside the trusted list. If the chain cannot identify a client, the Server shows the TCP peer. The address is kept in memory for the lifetime of the connection and is used only for diagnostics.

For a single nginx proxy, set `proxy_set_header X-Forwarded-For $remote_addr;` to forward the observed client address. For multiple hops, configure trust for the actual proxies and have each hop forward a verified chain with its actual peer appended. Caddy sets the header by default and provides trusted-proxy options for chains. Cloudflare Tunnel works with its standard `X-Forwarded-For` header and a trusted `cloudflared` peer. An on-host `cloudflared` connecting over loopback is trusted by default. The result through Cloudflare can be the Daemon network's public egress IP. [nginx proxy headers](https://nginx.org/en/docs/http/ngx_http_proxy_module.html#proxy_set_header) · [Caddy trusted proxy options](https://caddyserver.com/docs/caddyfile/options#trusted-proxies) · [Cloudflare request headers](https://developers.cloudflare.com/fundamentals/reference/http-headers/#x-forwarded-for)

## Docker-hosted Server

This authentication and network model supports a Server inside a container: persist its complete Server data directory, publish the main HTTP port and initialize the administrator locally or through initial-account environment values. Standalone remote management is enabled unless explicitly overridden; select the externally reachable URL when enrolling a Computer. A separate proxy container is a remote peer; trust its exact IP/CIDR when it terminates HTTPS. Container-local address discovery cannot infer the host's published port, NAT address or public domain.

There is currently no official Server image or Compose package. The Ubuntu image described below is the Computer/Agent execution environment, not a Server deployment image. A Docker-hosted Server's local Computer executes inside that container, not on its host; `--no-local` lets a deployment use separate Daemons. Do not assume mounting a Docker socket alone makes the host-container shortcut work: Docker-host paths and the chosen Server address must be valid from the actual execution environment.

## Remote and container Computers

Generate an enrollment command in Settings and run it on the intended machine. The command includes a temporary token and the Server's public SPKI SHA-256 pin. The installer script and programs are fetched from `oines/astral`'s official HTTPS release assets, with SHA-256 verification. An HTTP Server cannot replace those executables or their trusted bootstrap script. The Server no longer serves `/install.sh`, `/install.ps1` or `/downloads/`.

Server/Daemon enrollment, container preparation, status and RPC use `GET /internal/computer-tunnel`: WS or WSS transports an inner TLS 1.3 connection. The Daemon checks the copied Server pin before sending enrollment or long-term credentials. Outer HTTPS retains normal CA/hostname verification; inner TLS remains required even over WSS. There is no plaintext fallback and no credential or Hello metadata in outer headers/query strings. VPN is optional; Astral does not create NAT traversal or a relay.

The Server identity is generated once, stored with its data and uses a 100-year self-signed certificate with no renewal process. Daemons pin the public key, not a domain or exact certificate bytes. Preserve that identity when moving the Server; changing the network address does not require changing its identity. A mismatch fails instead of silently accepting a replacement.

Temporary registration tokens expire after 15 minutes and are consumed on successful registration. Container preparation can extend the fixed deadline to 90 minutes after enrollment creation. A consumed command cannot recover long-term credentials. Repeating a successful command on the same machine can reuse its saved local credentials; it does not re-register with the consumed token. Until used or expired, treat the entire command as a secret. Each Computer receives its own long-term token, stored privately; revocation closes its connection and blocks further authentication.

The Server records the **Enable Astral API** choice with the enrollment and applies it when the Computer registers. It defaults to off for new remote enrollments, including remote containers. The Computer details page also lets the administrator change the policy. It applies to requests originating from that Computer and covers both the command API and new event deliveries. Turning it off revokes live command API credentials and cancels their outstanding requests. Execution and file access remain available according to each Bot's Computer permissions. The same policy applies to older Daemons that can already connect to the Server. If a long-term Computer token leaks, revoke the connection and enroll the Computer again.

The installer stores versioned executables under `$XDG_DATA_HOME/astral/bin/<sha256>/astral` (or `~/.local/share/astral/bin/…`) on macOS/Linux, and `%LOCALAPPDATA%\Astral\bin\<sha256>\astral.exe` on Windows. Daemon state stays under the default root's `computers/<server-hash>/<enrollment-id>` unless `--root` is supplied. Program files and Agent homes are separate. Multiple native instances may use distinct roots to connect to different Servers; each container has its own instance.

Native enrollment installs **Astral Daemon (<instance-id>)** for the current user. macOS uses a LaunchAgent visible in System Settings → General → Login Items & Extensions; Windows uses a visible Task Scheduler → Astral logon task with `InteractiveToken` and `LeastPrivilege`; Linux uses `systemctl --user`. Windows PowerShell enrollment/update bootstrap uses the `curl.exe` included with modern Windows, not WSL. Installation does not elevate or register a system-wide Astral service. Desktop manages its own local Daemon lifecycle, and Docker manages container Daemons.

Linux native background enrollment checks user-service availability before pairing. When the current account is root (UID 0), it prints its preparation actions, idempotently runs `loginctl enable-linger 0` and `systemctl start user@0.service`, and waits for the user manager within a bounded timeout. If preparation fails, it stops before pairing. Service startup, status and updates select the current UID's runtime directory rather than inheriting another account's session environment. Ordinary accounts are not elevated or automatically given lingering; they need an available user-service session. `--foreground` and container enrollment do not prepare a user manager.

For root enrollment, log in directly as root or enter a root login shell with `sudo -i`, then run the complete connection command. Do not preserve another account's `HOME` or XDG session environment. Agent commands execute with the root account's permissions. Lingering affects the whole account: uninstalling an Astral Daemon does not disable it or stop root's user manager, which may serve other user services.

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

Container mode uses a prebuilt Ubuntu 24.04 environment with Bash, Git/LFS, search/network/build tools, Python/uv and common data/document libraries, Node LTS/pnpm, Go, Rust/Cargo, Java 21/Maven, LibreOffice/Pandoc/Poppler, FFmpeg/ImageMagick/Tesseract, Chinese/emoji fonts and Playwright Chromium. Language and Python versions are pinned in `internal/platform/computer-image/Dockerfile`; Ubuntu security packages resolve when CI builds the image. The environment image contains no versioned Astral program. Its independent CI publishes Linux amd64/arm64 images only when the Dockerfile or other build inputs change. An application release pins an immutable image digest and can reuse it across multiple patch releases.

The creator pulls that digest for the engine's Linux architecture, creates the persistent data volume and stopped container, copies the matching unified Linux Astral executable into `/usr/local/bin/astral`, then starts `computer supervise`. The program lives in the container's writable layer, outside `/var/lib/astral`. The data volume and read-only enrollment-file bind remain the only required mounts; no host program directory is introduced. Client creation does not run `docker build`, install the image's packages or require a build toolchain. A first pull is substantial; subsequent creations reuse the cached image. Installed software and other writable-layer changes remain part of that container.

Compatibility targets are Docker Desktop, OrbStack, Colima and Docker-backed Rancher Desktop on macOS; Docker Engine, rootless Docker and Docker Desktop on Linux; and Docker Desktop Linux containers on Windows. Use a reachable local Docker CLI/context with Linux containers and the current user's engine permissions. This target list is not an acceptance record: record the actual provider/version, host OS and architecture for each test.

Astral detects/configures use of an existing engine, not its installation or group policy. Verify `docker info` without sudo as the Server's actual OS account. Granting docker-group membership grants host root-level privileges; an already running systemd user manager can retain the old supplementary groups even after a new shell logs in. Updating that manager requires an administrator-controlled maintenance window and interrupts its other user services. Rootless Docker must belong to the same account, use its local context and have its user service enabled plus lingering for logout/boot persistence. See the [public Linux Docker setup](../apps/website/src/content/docs/docs/computers/containers.md#docker-access-for-a-linux-server).

Docker Desktop reaches a confirmed same-host Server through `http://host.docker.internal:4545` on the configured main port. Astral selects and checks the container-to-Server route for the actual runtime instead of assuming that every runtime's host network is the physical host. It does not require users to enable Docker Desktop host networking or create a dedicated TLS bridge. That HTTP route still carries the inner pinned TLS tunnel. Remote containers retain the selected reachable enrollment address; remote Docker contexts are rejected by the local shortcut.

### Updating an enrolled Computer

Open Computer details and choose **Update Daemon** for a connected managed Daemon. Confirm that running tasks will stop. The Owner-only management operation uses the existing pinned TLS tunnel, resolves the official latest release to an immutable tag and SHA-256, and waits for the same Computer to reconnect with a new boot identity and the expected version. It accepts no executable URL or credentials from the browser or Agent API. It always checks the latest release, even when Daemon and Server currently have matching versions. An equal or older target is a no-op; a Daemon newer than the Server shows a reminder to update Desktop/Server without blocking further Daemon updates. Native updates run as separate, visible current-user one-shot jobs in launchd, systemd or Task Scheduler so stopping the old Daemon cannot kill its updater. Registrations are cleaned after completion. New containers run the same Astral binary as a supervisor, replacing only `/usr/local/bin/astral` and restarting its child Daemon inside the existing container. A failed update restores the previous program.

Alternatively, copy the shell/PowerShell update command. Run it on the enrolled machine, or on the Docker host for a container. The command fetches and checks the new official HTTPS release program; temporary enrollment tokens are not reused. Update commands do not accept `--server` or a replacement pin and do not preflight the Server's current preferred address. Status/reconnection uses the enrolled machine's existing `credentials.json` URL and pin, so computers paired through a second access address remain updatable. The saved Server pin is retained, never replaced by an update response.

After downloading a verified release program, its local commands are:

```sh
astral computer update --root /path/to/existing-daemon
astral computer update --container astral-EXISTING-CONTAINER
```

Native updates switch the user service executable and verify a new Daemon boot/version; failure attempts to restore the previous service executable.

Container updates transfer and verify the Linux executable, replace only `/usr/local/bin/astral` and restart the same container. The container ID, original environment image, writable layer, data volume, pairing mount, credentials and installed software remain. Failure attempts to restore the previous binary. No image rebuild or Agent-environment replacement occurs. Restarting interrupts active tasks. Updating Desktop or Server likewise does not recreate existing containers; a newly pinned environment digest affects only future container creation, including when the existing container came from an earlier application-built image.

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

Desktop stores current Host schema 3 / Core format 11 data under the OS user configuration directory in `astral/schema-3-core-11/`. Public application upgrades from 0.1.0 reuse that directory and retain setup, history and computer identities. Earlier private Desktop directories remain untouched and Core format 10 data is not imported or converted. CLI `--data` and `--computers-dir` can open those same directories in headless mode, but never run two Servers against the same data concurrently. `ASTRAL_DESKTOP_DATA_DIR` selects an isolated Desktop data root. `ASTRAL_SERVER_LISTEN`, `ASTRAL_PUBLIC_URLS` and `ASTRAL_ALLOW_REMOTE_MANAGEMENT` configure its managed Server with environment priority; Desktop does not promote defaults or environment values into CLI overrides.

The main process owns the Go child. Quitting waits for shutdown; loss of the private stdin pipe or desktop parent ends the managed Server. Enrolled user-service and container Daemons have separate lifecycles. Restarting Desktop establishes a fresh private Desktop identity without requiring Web account credentials.

Development packages are unsigned and unnotarized. Distributable signed releases require Apple signing and notarization configuration through the [release process](releasing.md); build sources contain no signing credentials. Packaging alone does not establish native interactive acceptance or public release availability.
