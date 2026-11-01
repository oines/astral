# Linux headless Server installation

The headless Server includes Astral's Web UI and the local Computer. It runs `astral serve` on the VPS. This is separate from the expiring **Connect another computer** command, which enrolls a Computer's Daemon into an existing Server.

The installer requires a published GitHub Release marked `public_release_ready: true`; unsigned candidates, draft-only artifacts and an empty release list are rejected. The first supported public data baseline is Host schema 3 / Core format 11. The current implementation includes ordered Goose migrations and a separate Desktop updater; actual publication and N→N+1 update acceptance remain separate release checks.

## Installation

Use a Linux amd64 or arm64 account on a systemd host. The installer requires Bash, Python 3, curl, sha256sum, flock and normal GNU/Linux file utilities. It checks and enables lingering for the current account as needed, and starts `user@UID.service` only when the user's manager is not already running. A non-root account may need sudo for these system preparations or for registration in an existing system PATH directory. Preparation must succeed before installation changes begin. The Server remains a current-user `systemd --user` service; no system account or system-wide Astral service is created, and dependencies or Docker are not installed.

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

The service is visible as **Astral Server**. Run each operation as the installation account:

| Operation | Command |
| --- | --- |
| Status | `systemctl --user status astral-server.service` |
| Start now | `systemctl --user start astral-server.service` |
| Stop now | `systemctl --user stop astral-server.service` |
| Restart | `systemctl --user restart astral-server.service` |
| Enable automatic startup | `systemctl --user enable astral-server.service` |
| Disable automatic startup | `systemctl --user disable astral-server.service` |
| Read logs | `journalctl --user -u astral-server.service` |

Enable/disable changes automatic startup, not the running process; start/stop changes the process, not automatic startup. Stop before uninstalling or restoring data, and confirm the service is inactive.

Its unit is in `${XDG_CONFIG_HOME:-~/.config}/systemd/user/astral-server.service`. The installer manages one Server service per user, enables it for that user's manager and starts it. `--root /absolute/dedicated/directory` selects another installation root; it cannot overwrite a service belonging to a different root or an unrelated unit.

## First setup, command entry and Web access

The installer makes `astral` available on the current terminal's existing PATH and verifies that it runs. The stable entry points to `root/current/bin/astral`. Unrelated commands are not overwritten. Changing a profile or asking the user to reload PATH is not successful command registration.

An unconfigured Server prompts for its administrator username and a hidden password plus confirmation. For unattended setup use `ASTRAL_ADMIN_USERNAME` and `ASTRAL_ADMIN_PASSWORD_FILE`; keep the file private. Existing accounts skip initialization. An interrupted initial setup leaves a retryable installation and returns nonzero rather than claiming setup is complete.

An unattended example must download the script before executing it:

```sh
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
  https://github.com/oines/astral/releases/latest/download/install-server.sh -o install-server.sh
ASTRAL_ADMIN_USERNAME=admin \
ASTRAL_ADMIN_PASSWORD_FILE=/secure/path/astral-admin-password \
  bash install-server.sh
```

The account must also have the necessary OS authorization without an interactive prompt. Web administrator credentials do not grant sudo rights.

```sh
astral auth status
astral auth setup --username admin
```

Account and hostname-recovery commands automatically locate the matching installation. Explicit `--data` takes priority; multiple candidates require a choice or an explicit directory for noninteractive use. They use the private pinned local control channel while the Server runs, or the ordinary data lock while offline.

Standalone Servers default to remote Web access and accept hostnames without prior URL registration. Open the reachable Server IP/domain and actual port, then sign in. Listener bindings, the firewall and a reverse proxy remain the operator's responsibility. Desktop remains distinct. To intentionally close direct remote management, pass `--allow-remote-management=false` to the installer or set `ASTRAL_ALLOW_REMOTE_MANAGEMENT=false`. The explicit choice is saved in `installation.json` and the user-service startup environment; Web cannot change standalone reachability.

Web hostname restriction is optional. Its switch and list use one explicit local save. A misconfigured name can be recovered over SSH:

```sh
astral config hostnames show
astral config hostnames disable
```

Disabling keeps the list and account protection. Desktop and existing installations retain the previous Host restriction until deliberately changed. A startup hostname override must be changed at its source and restarted; the command reports that source. Computer tunnels and their pinned authentication are unaffected by Web hostname choices.

The Server provides HTTP. A local HTTPS reverse proxy can preserve the external Host and report `X-Forwarded-Proto: https`; only explicitly trusted peers may do so. Use `--trusted-proxy IP_OR_CIDR` for separate proxy hosts/containers. TOTP/password login works over HTTP or HTTPS; Passkeys require an actual HTTPS domain and remain scoped to its RP ID.

## Optional public GitHub proxy

For networks that cannot reach GitHub, the public manual provides an explicit GH-Proxy alternative. The bootstrap URL and the installer's internal API/asset downloads must both use the selected prefix:

```sh
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
  https://gh-proxy.org/https://github.com/oines/astral/releases/latest/download/install-server.sh \
  | bash -s -- --github-proxy https://gh-proxy.org/
```

`ASTRAL_GITHUB_PROXY` provides the same selection. Priority is explicit installer option, defined environment value, previous installation marker, then direct GitHub. Explicit empty disables the proxy. The choice persists into the user service and independent update task; metadata, release and digest checks remain. This is a third-party public service, not an Astral-operated CDN. Server addresses, management sessions and Computer credentials never use it.

The current installer verifies required CLI capabilities before stopping a service or writing new network settings. To install an older release without those capabilities, use that release's own installer. Normal upgrades retain credentials, identities, data and the hostname policy.

## Logout and reboot

The installer prepares lingering for the installation account before installing the enabled user service. Successful installation therefore supports continued operation after SSH logout and startup after a host reboot. It reports required system preparations and requests sudo when a normal account needs it; failure stops before service or program replacement. An already running user manager is not restarted, avoiding interruption of its other services.

Lingering is account-wide. It is not revoked on Astral uninstallation, because other user services, including Rootless Docker, may need it. A foreground `astral serve` is distinct: it installs no service and may end when SSH disconnects.

For root deployment, use a root login or `sudo -i`; do not preserve another account's HOME or XDG session environment. Local Computer commands then execute with root's privileges.

## Uninstallation

Download the installer as shown above, then use its explicit uninstall mode as the installation account:

```sh
bash install-server.sh --uninstall
bash install-server.sh --uninstall --root /absolute/dedicated/directory
```

The second command illustrates a custom installation root, not a second required step. Uninstallation validates installation ownership, the exact official unit and command entry, stops and disables the service, and verifies shutdown before cleanup. A mismatch or unconfirmed shutdown prevents further removal. It removes the unit and safely removable programs/links. A command or program shared with a registered Computer, another service or process remains and is reported.

Data, account configuration, Computer identities/files, installation metadata and backups remain. One complete verified release, including its native program and distribution assets, is retained through `data-program` for reinstallation format checks and recovery; uninstall does not empty the installation root. Additional releases needed by shared services are retained too. It does not disable lingering, remove Docker resources or uninstall remote Computers. Destructive data deletion is a separate operator action after backup.

## Updating and failure recovery

Run the installer again, optionally selecting `--tag`. Omitted network options retain their saved values. Pass `--public-url ''` for compatibility address discovery or `--allow-remote-management=false` for an explicit startup override. Listener and legacy address preferences use `data/server/network.json`; the remote-management choice is recorded in the installation marker and user unit environment. Explicit runtime CLI arguments and environment variables still take priority. Updating restarts the Server and interrupts active runs and connected Computer dispatch. Persisted browser logins remain valid until their expiry or explicit revocation; restarting the service is not a password reset.

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
