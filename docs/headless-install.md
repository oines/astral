# Linux headless Server installation

The headless Server includes Astral's Web UI and the local Computer. It runs `astral serve` on the VPS. This is separate from the expiring **Connect another computer** command, which enrolls a Computer's Daemon into an existing Server.

The installer requires a published GitHub Release marked `public_release_ready: true`; unsigned candidates, draft-only artifacts and an empty release list are rejected. Until such a release exists, there is no supported one-command download to install. The first supported public data baseline is Host schema 3 / Core format 11. The current implementation includes ordered Goose migrations and a separate Desktop updater; actual publication and N→N+1 update acceptance remain separate release checks.

## Installation

Use a Linux amd64 or arm64 login account with a running `systemd --user` manager. The installer requires Bash, Python 3, curl, sha256sum, flock and normal GNU/Linux file utilities. They must already be installed; the script does not install dependencies, use sudo, create a system account or write a system-level service. A root shell without a user manager is rejected too.

Once a ready release is published, its installer asset supports this command:

```sh
curl -fsSL --proto '=https' --proto-redir '=https' https://github.com/oines/astral/releases/latest/download/install-server.sh | bash
```

To inspect the script and select a specific release, download it first:

```sh
curl -fL --proto '=https' --proto-redir '=https' \
  https://github.com/oines/astral/releases/download/v0.1.0/install-server.sh \
  -o install-server.sh
less install-server.sh
bash install-server.sh --tag v0.1.0
```

The version above illustrates the tag format; it is not a claim that this release exists. The repository is fixed to `oines/astral`; the script does not accept arbitrary binary URLs. `latest` resolves GitHub's latest published stable release once, then downloads from that explicit tag.

The installer obtains all six macOS/Linux/Windows distribution binaries, the license, third-party notices and checksum files. SHA-256 is checked against `SHA256SUMS`, the distribution binaries are additionally checked against `checksums.txt`, and metadata is checked against the release manifest. Before activating a program, its actual `version` and `version --data-formats` output must match the manifest. Checksums come from the same GitHub release over HTTPS; they do not constitute a separate publisher signature.

Programs live in versioned directories under `${XDG_DATA_HOME:-~/.local/share}/astral-server/releases/`. An atomic `current` symlink selects the active distribution. Databases, credentials, `master.key`, the persistent Computer tunnel identity and Computer files are outside those version directories, under `astral-server/data/`. Keep the full data directory when updating or replacing a program.

The service is visible as **Astral Server**:

```sh
systemctl --user status astral-server.service
journalctl --user -u astral-server.service
systemctl --user restart astral-server.service
systemctl --user stop astral-server.service
```

Its unit is in `${XDG_CONFIG_HOME:-~/.config}/systemd/user/astral-server.service`. The installer manages one Server service per user, enables it for that user's manager and starts it. `--root /absolute/dedicated/directory` selects another installation root; it cannot overwrite a service belonging to a different root or an unrelated unit.

## Opening the Web UI

The default HTTP listener is `0.0.0.0:4545`, with direct remote management disabled. Computers can connect on that port. To open the local management entry from your computer:

```sh
ssh -N -L 4545:127.0.0.1:4545 USER@VPS
```

First create the Web administrator on the VPS using the command printed by the installer. For the default installation root:

```sh
~/.local/share/astral-server/current/bin/astral auth setup \
  --data ~/.local/share/astral-server/data/server --username admin
```

The command prompts for a hidden password and confirmation. It uses the running Server's private local control channel and does not publish an account-creation endpoint. The same command works offline while holding the data lock. `astral auth reset --data DIRECTORY --username admin` deliberately resets an existing account, clears its factors and revokes every Web login while retaining business data and Computer identities.

Open `http://127.0.0.1:4545` locally and sign in with that username and password. Localhost and an SSH tunnel do not grant Desktop identity or bypass login. There is no `owner.token` login or old-token compatibility path.

For direct LAN/Tailscale browser access, enable remote management explicitly, either in Settings after signing in over SSH or during installation:

```sh
bash install-server.sh --allow-remote-management --public-url http://YOUR-TAILSCALE-IP:4545
```

For a domain, a loopback listener and your own HTTPS reverse proxy can expose the local management entry:

```sh
bash install-server.sh --listen 127.0.0.1:4545 --public-url https://astral.example.com
```

The installer accepts one `--public-url`; set multiple access addresses afterward in Settings, or use repeated `astral serve --public-url` arguments / the JSON-array `ASTRAL_PUBLIC_URLS` environment value for a directly managed process. The first configured address is used by default in enrollment commands. Runtime CLI arguments take priority over environment, saved App settings and automatic discovery; an override does not rewrite lower-priority settings.

See [deployment and operations](deployment.md) for proxy, Host, trusted-proxy and WebSocket/SSE requirements. A local proxy intentionally reaches the loopback management entry regardless of the direct remote-management switch, but account login remains required. Separate proxy containers require explicit trusted-proxy IP/CIDR configuration to report HTTPS; forwarded client/Host headers do not make a remote connection local.

Username/password and TOTP login are allowed over HTTP or HTTPS. Adding and confirming TOTP or a Passkey automatically enables the second step, with no separate 2FA switch. Passkeys can only be added or used from an actual HTTPS Web page and are tied to its hostname. When explicitly enabled, Passkey passwordless login skips username, password and TOTP. Recovery codes can replace the second step. The Web device list shows browser and CLI sessions; deletion invalidates them immediately and ends associated event streams. Desktop identities are separate and cannot be revoked there.

Daemon connections remain encrypted even when their access address begins with HTTP: WS/WSS always carries a pinned inner TLS 1.3 connection. Its identity is a persistent Server key with a 100-year self-signed certificate, without automatic renewal. Enrollment commands pin that public key, and their temporary token is consumed once on successful registration. The copied installer and programs are fetched from the official HTTPS release, not from the HTTP Server. Preserve the Server identity when backing up or moving the deployment.

Astral does not configure DNS, a firewall, public HTTPS certificates or a proxy. This installer is a native Linux user-service deployment; an official Docker Server image/Compose package is not included. The Ubuntu Computer image is an Agent execution environment, not the Server image.

For remote API use, `astral connect --server URL --username admin` prompts for the password and an available TOTP/recovery code. It stores a private `client.session` credential under the local Astral root; it follows the same expiry and device-revocation rules as Web login. It does not use the private local account-administration channel as a remote authentication shortcut.

## Logout and reboot

Enabling a user service does not itself keep the user's systemd manager alive after logout or start it at boot. If user lingering is absent or cannot be confirmed, the installer says so. Ask the VPS administrator to run this deliberately for the installation account:

```sh
loginctl enable-linger USER
```

The administrator may need an elevated session. The installer never runs this command or silently requests elevation. Without lingering or an equivalent administrator-provided user session, continued operation after logout or reboot is not guaranteed.

## Updating and failure recovery

Run the installer again, optionally selecting `--tag`. Omitted network options retain their saved values. Pass `--public-url ''` for automatic address selection or `--allow-remote-management=false` to disable direct remote management. Installer options update `data/server/network.json`; the user-service unit leaves network options to the normal resolver so App settings remain editable. Explicit runtime CLI arguments and environment variables still take priority. Updating restarts the Server and interrupts active runs and connected Computer dispatch. Persisted browser logins remain valid until their expiry or explicit revocation; restarting the service is not a password reset.

Before stopping an existing service, the installer checks the release manifest and both programs' format metadata against the forward migration ranges, beginning at Host 3 / Core 11. The latest binary contains all supported historical steps; intermediate application versions are not downloaded. An older development format below the baseline, a future data format, missing metadata or nonempty unmanaged data without a trusted program is rejected before changing the old service. There is no legacy Owner-token, native Server-TLS or old Daemon protocol reader.

After the service stops, the new Server obtains exclusive data ownership, recovers any interrupted upgrade, snapshots the complete Server directory and applies missing Host/Core migrations in order. Each database step commits data and version bookkeeping together. A recovery journal remains through store opening and persistent-state preparation; migration or pre-commit preparation failure restores the verified snapshot. The Server commits at the beginning of `Start`, before local control, IM workers or HTTP serving. Later restarts do not automatically restore pre-upgrade data after new work may have been accepted. Installer activation/health failure uses the separate recovery below. Backups are placed under `data/upgrade-backups/`, outside the versioned program directories. See [the migration contract](migrations.md) for inspection and explicit recovery commands.

If activation or startup fails, the installer stops the attempted service, recovers any pending migration journal and restores a committed snapshot only if this attempt created a new upgrade record. It then restores the previous program selection, service definition and saved settings, and attempts to restart the previously running service. A same-format update that created no new snapshot does not restore an unrelated old backup. Old release directories remain available. Computer files and container environments are not part of Server data restoration, and external side effects are not undone. If recovery also fails, the installer reports the retained locations and service inspection command.

Keep independent consistent backups of data, the encryption key, persistent Server identity and Computer files; see [the backup instructions](deployment.md#data-directories). The automatic upgrade snapshot protects an attempted data transition and does not replace that backup policy. A failed first startup can leave newly initialized data; its format-probe program is retained so a retry can validate the data format without guessing.

## Inspecting without installing

```sh
bash install-server.sh --help
bash install-server.sh --version
bash install-server.sh --dry-run
bash install-server.sh --tag v0.1.0 --download-only /absolute/new-directory
```

`--dry-run` performs argument/configuration checks and prints the plan without network requests, file changes or service operations. `--download-only` needs Linux but no user manager; it downloads and validates the full distribution into a new directory, executes no downloaded program and changes no service or application data. The destination's parent directory must exist. Neither option pretends to prove native service startup.
