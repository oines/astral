# Linux headless Server installation

The headless Server includes Astral's Web UI and the local Computer. It runs `astral serve` on the VPS. This is separate from the expiring **Connect another computer** command, which enrolls a Computer's Daemon into an existing Server.

This installer is release preparation. It requires a published GitHub Release marked `public_release_ready: true`; unsigned candidates, draft-only artifacts and an empty release list are rejected. Until such a release exists, there is no supported one-command download to install. Do not advertise a candidate workflow as an available release.

## Installation

Use a Linux amd64 or arm64 login account with a running `systemd --user` manager. The installer requires Bash, Python 3, curl, sha256sum, flock and normal GNU/Linux file utilities. They must already be installed; the script does not install dependencies, use sudo, create a system account or write a system-level service. A root shell without a user manager is rejected too.

Once a ready release is published, its installer asset supports this command:

```sh
curl -fsSL --proto '=https' --proto-redir '=https' https://github.com/oines/astral/releases/latest/download/install-server.sh | bash
```

To inspect the script and select a specific release, download it first:

```sh
curl -fL --proto '=https' --proto-redir '=https' \
  https://github.com/oines/astral/releases/download/v2026.10.1/install-server.sh \
  -o install-server.sh
less install-server.sh
bash install-server.sh --tag v2026.10.1
```

The version above illustrates the tag format; it is not a claim that this release exists. The repository is fixed to `oines/astral`; the script does not accept arbitrary binary URLs. `latest` resolves GitHub's latest published stable release once, then downloads from that explicit tag.

The installer obtains all six macOS/Linux/Windows distribution binaries, the license, third-party notices and checksum files. SHA-256 is checked against `SHA256SUMS`, the distribution binaries are additionally checked against `checksums.txt`, and metadata is checked against the release manifest. Before activating a program, its actual `version` and `version --data-formats` output must match the manifest. Checksums come from the same GitHub release over HTTPS; they do not constitute a separate publisher signature.

Programs live in versioned directories under `${XDG_DATA_HOME:-~/.local/share}/astral-server/releases/`. An atomic `current` symlink selects the active distribution. Databases, credentials, `master.key` and Computer files are outside those version directories, under `astral-server/data/`.

The service is visible as **Astral Server**:

```sh
systemctl --user status astral-server.service
journalctl --user -u astral-server.service
systemctl --user restart astral-server.service
systemctl --user stop astral-server.service
```

Its unit is in `${XDG_CONFIG_HOME:-~/.config}/systemd/user/astral-server.service`. The installer manages one Server service per user, enables it for that user's manager and starts it. `--root /absolute/dedicated/directory` selects another installation root; it cannot overwrite a service belonging to a different root or an unrelated unit.

## Opening the Web UI

The default listener is `127.0.0.1:4545`. From your computer:

```sh
ssh -N -L 4545:127.0.0.1:4545 USER@VPS
```

Open `http://127.0.0.1:4545` locally. The installer prints the path to the VPS's private `data/server/owner.token` file, but never its contents. Read that file yourself through your existing SSH access and enter the token on the Web sign-in screen. Protect it like a password; do not paste it into an issue or diagnostic report.

For a domain, keep the listener on loopback and put an HTTPS reverse proxy in front of it:

```sh
bash install-server.sh --public-url https://astral.example.com
```

See [deployment and operations](deployment.md) for proxy and WebSocket/SSE requirements. Direct TLS is also supported with `--tls-cert /absolute/cert.pem --tls-key /absolute/key.pem`. A non-loopback `--listen` requires an HTTPS public URL, and public HTTP URLs are rejected. The Go Server also applies its normal URL and TLS validation. The installer does not configure DNS, a firewall, certificates or a proxy.

## Logout and reboot

Enabling a user service does not itself keep the user's systemd manager alive after logout or start it at boot. If user lingering is absent or cannot be confirmed, the installer says so. Ask the VPS administrator to run this deliberately for the installation account:

```sh
loginctl enable-linger USER
```

The administrator may need an elevated session. The installer never runs this command or silently requests elevation. Without lingering or an equivalent administrator-provided user session, continued operation after logout or reboot is not guaranteed.

## Updating and failure recovery

Run the installer again, optionally selecting `--tag`. Omitted listen, public URL and TLS settings retain their saved values; pass `--public-url ''`, or both `--tls-cert '' --tls-key ''`, to clear those settings deliberately. Updating restarts the Server and interrupts active sessions and connected Computer dispatch.

Before stopping an existing service, the installer queries both executables' data-format metadata. It only allows the same Host schema and Core format. An old development binary without this capability, or nonempty unmanaged data without a trusted program, is rejected. There is no automatic migration across incompatible formats: use a separate empty root or an explicit migration procedure when one is provided.

If activation or startup fails, the installer restores the previous program selection, service definition and saved installer settings, then attempts to restart the previously running service. Old version directories remain available. It does not remove original data, credentials, encryption keys or Computer files. Recovery of the service is **not** a database rollback; the installer never promises to undo writes made during an attempted startup. If service restoration also fails, it reports the retained backup location and service inspection command.

Back up the data and encryption key consistently before updating; see [the backup instructions](deployment.md#data-directories). The installer does not replace that backup. A failed first startup can leave newly initialized data; its format-probe program is retained so a retry can validate the data format without guessing.

## Inspecting without installing

```sh
bash install-server.sh --help
bash install-server.sh --version
bash install-server.sh --dry-run
bash install-server.sh --tag v2026.10.1 --download-only /absolute/new-directory
```

`--dry-run` performs argument/configuration checks and prints the plan without network requests, file changes or service operations. `--download-only` needs Linux but no user manager; it downloads and validates the full distribution into a new directory, executes no downloaded program and changes no service or application data. The destination's parent directory must exist. Neither option pretends to prove native service startup.
