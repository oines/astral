---
title: "Download and install"
description: "Install Astral on your own computer, or run it on an always-on Linux server."
---

**For a first try, install the desktop app.** Download the package for your computer, then open Astral to begin setup.

To keep Astral on a separate Linux server and use it through a browser, follow [Server installation](#install-on-a-linux-vps) below. Choose either approach.

<span id="download-desktop"></span>

## Choose a desktop download

<p><span data-astral-release-status data-fallback-version="0.1.10" data-latest-prefix="Latest release: " aria-live="polite">Available version: v0.1.10</span> · <a data-astral-release-notes href="https://github.com/oines/astral/releases/tag/v0.1.10">Release notes, all assets, and checksums</a> · <a href="https://github.com/oines/astral/releases/latest">Latest on GitHub</a></p>

For a Mac with an Apple M-series chip, choose Apple silicon; for an Intel Mac, choose Intel. Windows and Linux computers with Intel or AMD processors use x64 or x86_64 packages. ARM devices use ARM64.

| Platform | Architecture | Download |
| --- | --- | --- |
| macOS | Apple silicon | <a data-astral-download="mac-arm64-dmg" href="https://github.com/oines/astral/releases/download/v0.1.10/Astral-0.1.10-mac-arm64.dmg">DMG</a> |
| macOS | Intel | <a data-astral-download="mac-x64-dmg" href="https://github.com/oines/astral/releases/download/v0.1.10/Astral-0.1.10-mac-x64.dmg">DMG</a> |
| Windows | x64 | <a data-astral-download="win-x64-exe" href="https://github.com/oines/astral/releases/download/v0.1.10/Astral-0.1.10-win-x64.exe">EXE installer</a> |
| Windows | ARM64 | <a data-astral-download="win-arm64-exe" href="https://github.com/oines/astral/releases/download/v0.1.10/Astral-0.1.10-win-arm64.exe">EXE installer</a> |
| Linux | x86_64 | <a data-astral-download="linux-x64-appimage" href="https://github.com/oines/astral/releases/download/v0.1.10/Astral-0.1.10-linux-x86_64.AppImage">AppImage</a> · <a data-astral-download="linux-x64-deb" href="https://github.com/oines/astral/releases/download/v0.1.10/Astral-0.1.10-linux-amd64.deb">DEB</a> |
| Linux | ARM64 | <a data-astral-download="linux-arm64-appimage" href="https://github.com/oines/astral/releases/download/v0.1.10/Astral-0.1.10-linux-arm64.AppImage">AppImage</a> · <a data-astral-download="linux-arm64-deb" href="https://github.com/oines/astral/releases/download/v0.1.10/Astral-0.1.10-linux-arm64.deb">DEB</a> |

## Install and open Astral

- **macOS:** open the DMG, drag Astral to Applications, then open it there. The v0.1.10 Mac packages are signed and notarized by Apple; see the [signing report](https://github.com/oines/astral/releases/download/v0.1.10/mac-signing-report.json).
- **Windows:** double-click the EXE and follow the installer. The package is unsigned, so Windows may show a SmartScreen prompt. Check that it came from the official release linked on this page.
- **Linux:** for an AppImage, allow the file to run as a program in its file properties, then open it. For a Debian/Ubuntu package installer, choose the DEB. AppImage supports in-app updates; DEB users install a newer package manually. Linux packages are unsigned.

Once Astral opens, follow [Your first task](/docs/start/first-steps/) to connect a model and choose your computer. Desktop opens setup directly, without requiring a Web sign-in account first.

<span id="install-on-a-linux-vps"></span>

## Run Astral on a server

Prepare a systemd-based Linux x86_64 or ARM64 server. Connect to it over SSH, then run this in **the server's terminal**:

```sh
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
  https://github.com/oines/astral/releases/latest/download/install-server.sh | bash
```

Follow the prompts to create a Web username and password. Once installation finishes, open the displayed address and sign in. You can close SSH while the background service keeps running.

See [Install Server](/docs/hosting/server/) for requirements, remote access, and daily management. The desktop app above is enough to try Astral on your own laptop.

## When GitHub downloads fail

You can use the third-party [GH-Proxy download service](https://gh-proxy.com/en/docs/github-accelerator/). For a desktop package:

1. Copy its complete download link from the table above.
2. Add `https://gh-proxy.org/` at the beginning, with no space.
3. Open the resulting address in your browser.

A link starting with `https://github.com/` will start with `https://gh-proxy.org/https://github.com/` after this change. The proxy is a third-party service; availability depends on the network.

For Server, use the [complete proxy installation command](/docs/hosting/server/#when-github-is-unreachable) so subsequent program downloads use your chosen proxy too.

Developers who need to compile Astral can follow [Build from source](/docs/hosting/build-from-source/).

<script is:inline type="module" src="/scripts/release-downloads.js"></script>
