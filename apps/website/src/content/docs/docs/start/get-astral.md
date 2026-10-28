---
title: "Get Astral"
description: "Download Astral Desktop for macOS, Windows, or Linux, or install Server on a Linux VPS."
---

Download Astral Desktop for your computer, or install Astral Server on a Linux VPS.

<p><span data-astral-release-status data-fallback-version="0.1.2" data-latest-prefix="Latest release: " aria-live="polite">Available version: v0.1.2</span> · <a data-astral-release-notes href="https://github.com/oines/astral/releases/tag/v0.1.2">Release notes, all assets, and checksums</a> · <a href="https://github.com/oines/astral/releases/latest">Latest on GitHub</a></p>

## Download Desktop

| Platform | Architecture | Download |
| --- | --- | --- |
| macOS | Apple silicon | <a data-astral-download="mac-arm64-dmg" href="https://github.com/oines/astral/releases/download/v0.1.2/Astral-0.1.2-mac-arm64.dmg">DMG</a> |
| macOS | Intel | <a data-astral-download="mac-x64-dmg" href="https://github.com/oines/astral/releases/download/v0.1.2/Astral-0.1.2-mac-x64.dmg">DMG</a> |
| Windows | x64 | <a data-astral-download="win-x64-exe" href="https://github.com/oines/astral/releases/download/v0.1.2/Astral-0.1.2-win-x64.exe">EXE installer</a> |
| Windows | ARM64 | <a data-astral-download="win-arm64-exe" href="https://github.com/oines/astral/releases/download/v0.1.2/Astral-0.1.2-win-arm64.exe">EXE installer</a> |
| Linux | x86_64 | <a data-astral-download="linux-x64-appimage" href="https://github.com/oines/astral/releases/download/v0.1.2/Astral-0.1.2-linux-x86_64.AppImage">AppImage</a> · <a data-astral-download="linux-x64-deb" href="https://github.com/oines/astral/releases/download/v0.1.2/Astral-0.1.2-linux-amd64.deb">DEB</a> |
| Linux | ARM64 | <a data-astral-download="linux-arm64-appimage" href="https://github.com/oines/astral/releases/download/v0.1.2/Astral-0.1.2-linux-arm64.AppImage">AppImage</a> · <a data-astral-download="linux-arm64-deb" href="https://github.com/oines/astral/releases/download/v0.1.2/Astral-0.1.2-linux-arm64.deb">DEB</a> |

- **macOS:** open the DMG and move Astral to Applications. Official Mac packages are Developer ID signed. Check the release notes or signing report (`mac-signing-report.json`) for that release’s Apple notarization status.
- **Windows:** run the EXE to install for the current user. Windows packages are unsigned and may show SmartScreen prompts.
- **Linux:** make the AppImage executable before opening it, or install the DEB with your package manager. AppImage supports in-app updating; install newer DEB packages manually.

On Windows and Linux, the window controls are in the upper-right corner of the app header. The sidebar button is on the left; press Alt to show the app menu.

Open Astral and follow [First steps](/docs/start/first-steps/) to connect your model and choose a computer. See [Updates and backups](/docs/hosting/maintenance/) for in-app updates and the Host 3 / Core 11 data baseline. Desktop uses `schema-3-core-11/` and preserves older private-version directories without importing them.

## Install on a Linux VPS

```sh
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
  https://github.com/oines/astral/releases/latest/download/install-server.sh | bash
```

Supports Linux x86_64 and ARM64 with a running systemd user manager. The installer registers a current-user service and prints the commands for setting up web sign-in and opening Astral. Follow [Run a Server](/docs/hosting/server/) for requirements and remote access.

## Build from source

### Prepare the toolchain

Use the repository's versions: Go **1.26.3**, Node.js **26.7.0**, and pnpm **10.31.0**. The root `mise.toml` also declares these versions.

```sh
git clone https://github.com/oines/astral.git
cd astral
pnpm install --frozen-lockfile
```

The Core dependency, including its Models packages, is pinned in `go.mod`; a separate Core checkout is not required. `GOWORK=off` prevents a local `go.work` from replacing that dependency. Run the following commands from the Astral repository root.

### Build the Server and product interface

On macOS or Linux:

```sh
GOWORK=off pnpm build:distribution
```

In Windows PowerShell:

```powershell
$env:GOWORK = 'off'
pnpm build:distribution
```

Executables are written to `dist/bin/`. Select the file matching the target operating system and CPU architecture. `apps/web/dist/` contains the product interface; `apps/website/` contains the public website and this documentation.

Continue with [Run a Server](/docs/hosting/server/) for startup, browser sign-in, and remote access.

### Run Desktop from source

After building the distribution, run:

```sh
pnpm dev:desktop
```

Desktop manages its local runtime and opens directly into [first-time setup](/docs/start/first-steps/). To create an installer, run the matching command on that operating system:

| Build system | Command |
| --- | --- |
| macOS | `pnpm --filter @astral/desktop package` |
| Windows | `pnpm --filter @astral/desktop package:windows` |
| Linux | `pnpm --filter @astral/desktop package:linux` |

Packages appear in `apps/desktop/release/`. A locally built installer is not an officially signed or notarized release.

<script is:inline type="module" src="/scripts/release-downloads.js"></script>
