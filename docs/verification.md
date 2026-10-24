# Verification guide

Checks apply to the exact source commit and environment in which they ran. Historical screenshots and local acceptance notes are not evidence that a newer build passes.

## Automated checks

Use the pinned tool versions in `mise.toml`, install JavaScript dependencies with `pnpm install --frozen-lockfile`, and disable sibling Go workspaces:

```sh
export GOWORK=off
go test -race ./...
pnpm lint
pnpm typecheck
pnpm test
pnpm build:distribution
pnpm test:desktop-service
pnpm test:daemon-service
```

The distribution build embeds the Web application, license and third-party notices in the Go executable. `check-desktop-service.mjs` checks Owner handoff, graceful shutdown, parent-death cleanup, data-lock release and preservation of Local Computer identity.

Container integration requires the built distributions and a running local Docker-compatible Linux runtime:

```sh
GOWORK=off go test -tags=integration -count=1 -timeout=20m ./internal/platform
```

These tests create isolated Computers and containers, exercise actual enrollment and command/file operations, and clean up the resources they create. Use a disposable development environment.

The current [CI workflow](../.github/workflows/ci.yml) is the executable source of truth for automated checks. Results are available in [GitHub Actions](https://github.com/oines/astral/actions). Its matrix runs native Windows, macOS and Linux tests, downloaded installers, current-user Daemon lifecycle and packaged Desktop startup/shutdown, plus the full Ubuntu container and in-place update checks. Its platform matrix is not a claim that every installation format or architecture has already passed interactive acceptance.

`pnpm test:daemon-service` installs and removes an isolated current-user service. Run it in a disposable user session with the platform service manager available. `pnpm test:packaged-desktop` checks a previously packaged App; Linux CI installs the native DEB and runs it in an X display so its sandbox hooks are exercised.

The native Linux job also runs `node scripts/check-headless-install.mjs`. It uses the actual built Server and a synthetic local release transport to check current-user service installation, authenticated HTTP readiness, identity-preserving reinstall, checksum rejection before stopping the old service and failed-start recovery. This check requires a disposable Linux user session and refuses an existing `astral-server.service`. It does not validate publisher signing or authorize a candidate release for publication.

## Manual acceptance

Use a separate data directory and synthetic credentials, accounts, conversations and files. Do not record active enrollment tokens, QR authorization codes, provider keys or private conversations in screenshots or logs.

- Exercise first use, model configuration, a real tool call, cancellation, restart and retained conversation history.
- Check each advertised operating system and architecture. Cross-compiling an executable does not establish that it runs on its target system.
- For Desktop, test installation, embedded Server startup, normal Quit, parent-death cleanup, menu/tray behavior and optional login startup.
- For standalone Daemons, test current-user service visibility, enrollment, restart, stop/start, identity-preserving update and uninstall.
- For containers, verify command/file use and update while retaining container identity, mounts, installed software and data.
- Apply the layout, keyboard, language, theme and narrow-screen checks in the [UI/UX specification](ui-ux-spec.md).
- Protocol-fixture tests do not replace live account validation for [IM integrations](im-platforms.md). Record which integrations were actually exercised without publishing credentials or private account data.

## Release acceptance

Before advertising automatic updates, test a real previous-to-next release installation and a skipped-version upgrade with preserved data and credentials. Verify migration failure recovery, program startup failure, download/checksum rejection and process cleanup. A fresh installation smoke test alone does not establish update safety.

Published installers require their own signing, notarization and installation checks. Development packages are currently unsigned; see [deployment](deployment.md). Record the source commit, platform, architecture and result for each acceptance run. Keep raw local screenshots and temporary fixture state outside tracked source; publish only deliberately reviewed examples.
