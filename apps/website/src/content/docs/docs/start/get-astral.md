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
git clone https://github.com/oines/astral-core.git ../agent-core
cp go.work.example go.work
```

This source refactor uses the matching Astral Core monorepo checkout, including its Models packages. Use matching revisions of both repositories and keep the local workspace enabled. Adjust the Core path in `go.work` when your checkouts are elsewhere. Run the following commands from the Astral repository root.

## Build the Server and product interface

On macOS or Linux:

```sh
pnpm build:distribution
```

In Windows PowerShell:

```powershell
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
