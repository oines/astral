# Building and developing Astral

This guide covers source builds, desktop packaging, first-run setup and development checks. Run commands from the repository root. For deployment and account administration, see [deployment and operations](deployment.md); for the execution model, see [computers, sessions and execution](runtime.md).

Astral is pre-release software. Desktop packages and Server/Computer binaries target macOS, Windows and Linux on ARM64 and AMD64. Native acceptance is tracked in [GitHub Actions](https://github.com/oines/astral/actions) and the [verification guide](verification.md); producing a package does not establish that every platform feature has passed interactive acceptance. Public installers and automatic Desktop updates are not yet available. See [the release process](releasing.md) for publication gates and supported updates.

## Build and start

The pinned development tools are in [mise.toml](../mise.toml). Install those versions, then:

```sh
pnpm install --frozen-lockfile
GOWORK=off pnpm build:distribution
```

The build generates OpenAPI and the TypeScript SDK, builds the shared Web app, and embeds it in six binaries:

```text
dist/bin/astral-darwin-arm64
dist/bin/astral-darwin-amd64
dist/bin/astral-linux-arm64
dist/bin/astral-linux-amd64
dist/bin/astral-windows-arm64.exe
dist/bin/astral-windows-amd64.exe
dist/bin/checksums.txt
dist/bin/THIRD_PARTY_NOTICES.txt
dist/bin/LICENSE.txt
```

Start the binary for your machine, for example:

```sh
./dist/bin/astral-darwin-arm64 serve --data /tmp/astral-fresh/server --computers-dir /tmp/astral-fresh/computers --distribution-dir dist/bin
```

Keep that Server running and initialize its Web administrator from another terminal:

```sh
./dist/bin/astral-darwin-arm64 auth setup --data /tmp/astral-fresh/server --username admin
```

The password is entered through a hidden terminal prompt. Open `http://127.0.0.1:4545` and sign in with the account. Desktop opens directly into its own local Server through a private process-lifetime startup identity; it does not require a Web account. The old Owner-token login and Cookie handoff are removed.

The HTTP listener defaults to `0.0.0.0:4545` so other Computers can connect. Direct remote Web UI and management API access defaults off; enable **Settings → Security & Access → Allow remote management** to permit it, while retaining account login. Configure multiple access addresses there or select a reachable address when adding a Computer. Explicit CLI arguments override environment, saved settings and discovery; repeated `--public-url` or JSON-array `ASTRAL_PUBLIC_URLS` supplies the list.

Password and TOTP login work over HTTP or HTTPS. A confirmed TOTP or Passkey automatically adds a second step; there is no separate 2FA switch. Passkeys require an actual HTTPS page and system user verification. Optional Passkey passwordless login defaults off and, when enabled, skips username, password and TOTP. Browser/CLI logins have independent sessions that default to 30 days with immediate device revocation; Desktop is absent from that list and cannot be revoked there.

Daemon traffic always uses TLS 1.3 inside WS/WSS, with a persistent Server identity, a 100-year self-signed certificate and a public-key pin in the pairing command. Registration tokens are consumed once. VPN is optional; browser HTTPS can be supplied by your reverse proxy or Tailscale Serve. Installers and executable downloads come from the official HTTPS release assets, never this Server's HTTP port. See [deployment](deployment.md) for account recovery, configuration, trusted proxies and container networking.

Use a new, empty data directory for this version. Host schema 3 and Core format 10 replace the previous formats; older data is rejected, with no migration or compatibility mode. Choose a persistent location instead of `/tmp` for regular use.

## First-run setup

A fresh installation includes a Bot named **Astral**, which you can rename later in Settings. The two-step setup connects a model service and selects a model, then selects or connects a Computer. **Start using Astral** grants access to the selected online Computer and saves both defaults together. The first conversation is ready to use those resources; there is no separate Bot creation step. Existing installations keep their Bots and resource choices, and setup does not reappear after completion or recreate a renamed or deleted Bot.

After setup, manage services in **Settings → Models** and Bot names, defaults and permissions in **Settings → Bots**. Saved models are immediately usable; no capability probe is required. New sessions inherit the Bot defaults. Provider keys and endpoints are global resources, so Bots do not need separate copies. Independent settings save automatically; connection parameters are submitted together with the local **Save connection** action.

The model adapters support Chat Completions for OpenAI, DeepSeek, Qwen and OpenRouter, and Responses for OpenAI and DeepSeek. Connection and capability failures are reported from actual model requests. Native reasoning options are preserved. Search uses one selected resource per Bot: Tavily, Exa, DeepSeek Search, Brave, Google Custom Search, Sogou/Tencent, Serper, SearXNG, Jina, Bocha, DuckDuckGo or Yandex. The unified `web_search` tool accepts `query` and optional `domains`, `recency` (days), and `limit`; omitted options keep upstream defaults, and Astral does not truncate result counts or search text. See [search setup and provider capabilities](search.md).

## Desktop applications

After building the distribution:

```sh
pnpm --filter @astral/desktop package          # macOS DMG / ZIP
pnpm --filter @astral/desktop package:windows  # current-user Windows installer
pnpm --filter @astral/desktop package:linux    # Linux AppImage / deb
```

Native platform packages are written to `apps/desktop/release/`. Windows installs for the current user without administrator privileges. Linux supports AppImage and Debian packages. The default build is unsigned; signing and notarization require the distributor's Apple credentials and release process.

Electron manages the local Go server, local Daemon, window, menu, tray and login preference. Closing the window follows the General setting; quitting stops the managed service. A server also stops if its desktop parent dies. Renderer code receives neither the private Desktop credential nor a general native filesystem API. The startup credential travels through private stdin and is not a browser Cookie; Web logout, password resets and device removal do not affect it.

Desktop stores current data under the OS user configuration directory in `astral/schema-3/`: `~/Library/Application Support/astral/schema-3/` on macOS, `%APPDATA%\astral\schema-3\` on Windows, and `$XDG_CONFIG_HOME/astral/schema-3/` (normally `~/.config/astral/schema-3/`) on Linux. Data from earlier formats stays in its original directory. `ASTRAL_DESKTOP_DATA_DIR` can select another directory; it must be empty or already use the current formats.

Enrolling a native Windows Computer uses PowerShell and the `curl.exe` included with modern Windows; it does not require WSL. Local containers use Docker Desktop in Linux-container mode; on macOS use Docker Desktop or OrbStack, and on Linux use the local Docker Engine. Docker Desktop itself may use WSL 2 internally, but Astral does not ask the user to run enrollment commands there.

## Development and verification

For separate frontend and backend development:

```sh
GOWORK=off pnpm dev:server
pnpm dev
```

The development Web app proxies the API to port 4545. For desktop development, build the native binary first, then run `pnpm dev:desktop`.

Brand artwork lives in `packages/icons/assets/`: the color mark and light/dark wordmarks are shared by the Web app, while the layered monochrome mark is used for the tray and favicon. Run `pnpm build:icons` to regenerate the desktop SVG/PNG/ICNS and favicon from these SVG sources. The app icon uses a white or graphite rounded tile with macOS sizing and padding; the Dock switches between them with the system appearance. Web and desktop development/build commands also regenerate their icons automatically; macOS tray resources include both 1× and 2× versions.

The versions pinned in `go.mod` are publicly available and support standalone builds with `GOWORK=off`; sibling repositories are not required. Use that setting for verification and distribution builds so the result uses the committed dependencies. For coordinated development across Astral, Astral Core and astral-models, an ignored local Go workspace can select those checkouts. Build scripts honor an active workspace unless `GOWORK=off` is set, including when collecting dependency notices.

```sh
export GOWORK=off
go test -race ./...
go vet ./...
pnpm lint:go
pnpm lint
pnpm typecheck
pnpm test
pnpm build:distribution
pnpm test:desktop-service
pnpm test:integration
```

The integration command requires a running local Docker-compatible Linux runtime and the built distributions. It exercises the downloaded installer, real Daemons, container registration, actual shell/file execution and idempotent provisioning, and removes its own containers and volumes.

For repeatable UI checks without an external model account:

```sh
node scripts/dev-provider.mjs
```

Add a Chat Completions Provider with endpoint `http://127.0.0.1:4556`, any test-only API key, and model `astral-fixture`. The fixture supports native image inspection and prompts containing `[file]`, `[slow]`, `[retry]`, and `[silent]`. `[file]` intentionally writes `validation.md` in the selected Computer's AgentHome through the real command tool. Use a disposable Agent and data directory.

See [deployment](deployment.md), [the verification guide](verification.md), and [third-party acknowledgements](../THIRD_PARTY.md). Fixture checks, live provider checks, and manual desktop checks are identified separately.

For contributions, see [CONTRIBUTING.md](../CONTRIBUTING.md). Report suspected vulnerabilities privately using the channel in [SECURITY.md](../SECURITY.md).
