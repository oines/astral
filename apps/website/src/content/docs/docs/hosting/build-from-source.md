---
title: "Build from source"
description: "Build Astral Server and Desktop for development or your own packaging."
---

This guide is for developers modifying Astral or building their own packages. For everyday use, [install a release](/docs/start/get-astral/). Run the commands below from the Astral repository root.

## Prepare the tools and source

Install the versions specified by the repository: Go **1.26.3**, Node.js **26.7.0**, and pnpm **10.31.0**. They are recorded in `mise.toml` and can also be managed with mise.

Get the source and install dependencies:

```sh
git clone https://github.com/oines/astral.git
cd astral
pnpm install --frozen-lockfile
```

Go dependencies are pinned in `go.mod`. The build commands use `GOWORK=off` to prevent a workspace file on your machine from substituting other local dependencies.

## Build the programs

On macOS or Linux:

```sh
GOWORK=off pnpm build:distribution
```

On Windows PowerShell:

```powershell
$env:GOWORK = 'off'
pnpm build:distribution
```

The result in `dist/bin/` contains x86_64 and ARM64 programs for macOS, Linux, and Windows. Each Server program includes the web interface. Choose the file matching the machine where it will run, then follow [foreground operation](/docs/hosting/server-options/#run-in-the-foreground) to start it and set up sign-in.

To build only the Server for your current system, replace `build:distribution` with `build`. Use the full distribution build for Desktop packaging or a Server that will create container computers, so the required platform programs are available.

## Run or package Desktop

After the full distribution build, start the desktop app in development mode:

```sh
pnpm dev:desktop
```

To create installation packages, run the appropriate command on the target operating system:

| Build system | Command |
| --- | --- |
| macOS | `pnpm --filter @astral/desktop package` |
| Windows | `pnpm --filter @astral/desktop package:windows` |
| Linux | `pnpm --filter @astral/desktop package:linux` |

Packages are written to `apps/desktop/release/`. Distributing them to others also requires the signing and notarization steps appropriate to the target system.

## Check interface or documentation changes

Astral's product interface is in `apps/web/`. After changes, run the project's `pnpm typecheck`, `pnpm test`, and relevant build.

The website and user documentation are in `apps/website/`. Preview them separately:

```sh
pnpm dev:website
```

The terminal prints the local preview address. When finished, run:

```sh
pnpm check:website
```

The website is a separate static site. Documentation changes do not require rebuilding Server or Desktop packages.

## Contribute and publish

Submit a pull request with the relevant tests and update the public documentation when behavior changes. Product changes merged into `main` enter Astral's single rolling release pipeline automatically. Maintainers do not bump package versions, create a local release tag or run a second candidate build for each release.

Automatic releases are triggered by product code, packaged resources, installers, shared dependencies and build or signing inputs. README, documentation, website, test and CI-only changes keep their checks without starting an application release. Shared root manifests and dependency locks still count as build inputs; embedded prompts and packaged license notices count as product resources.

The preparation workflow freezes the source commit, build identity and application version before compiling. Versions use `MAJOR.MINOR.PATCH`; the current `0.1.x` series derives its patch number from the preparation run number. Cancelled or failed preparations can leave gaps, and rerunning the same preparation retains its version. The version in a local source checkout is a development baseline; CI applies its frozen version to Go and Electron in its disposable checkout.

Tests and the unified program build run in parallel. After preparation succeeds, the release workflow signs the Darwin programs once and uses those same bytes in every desktop package. Intel and Apple silicon Mac packages are built and tested on their respective native runners in parallel. Required native installation, update and data-retention tests, migration checks, Developer ID signing and accepted Apple notarization all remain release requirements.

The pipeline then seals the final packages, manifest, update feeds, release notes and checksums. Publishing uploads those exact files; it does not rebuild or re-sign them. CI creates the version's annotated tag at the verified source commit and records an OIDC-authenticated provenance attestation for the sealed inventory. The public release includes the source and build evidence. After publication, separate checks download the real release, install Server and upgrade untouched previous official desktop installations.

New product changes replace obsolete preparation work and the latest waiting release request. A release already signing, verifying or publishing is allowed to finish. A failed required check blocks publication; a failure after publication remains visible in Actions and does not replace an already public version's files. Use the [Actions runs](https://github.com/oines/astral/actions) to identify the failing stage and its retained diagnostics before retrying it.

Automatic publication does not choose when your installed Astral restarts. Desktop checks for new versions and you decide when to install; see [Updates and backups](/docs/hosting/maintenance/). The download page checks GitHub Latest automatically. If that check is unavailable, its labelled, previously verified downloads remain usable without requiring a website edit for every new release.
