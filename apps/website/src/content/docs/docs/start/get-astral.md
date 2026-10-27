---
title: "Get Astral"
description: "Build Astral from source and choose Desktop or a self-hosted Server."
---

Astral's source is public. Public release installers are not available yet; you can build from source now. Use the assets actually published on [GitHub Releases](https://github.com/oines/astral/releases) when installers become available.

## Prepare the toolchain

Use the repository's versions: Go **1.26.3**, Node.js **26.7.0**, and pnpm **10.31.0**. The root `mise.toml` also declares these versions.

```sh
git clone https://github.com/oines/astral.git
cd astral
pnpm install --frozen-lockfile
```

The Core dependency, including its Models packages, is pinned in `go.mod`; a separate Core checkout is not required. `GOWORK=off` prevents a local `go.work` from replacing that dependency. Run the following commands from the Astral repository root.

## Build the Server and product interface

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

## Run Desktop

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
