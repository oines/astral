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

Container integration requires the built distributions, access to the application's pinned prebuilt environment image, and a running local Docker-compatible Linux runtime:

```sh
GOWORK=off go test -tags=integration -count=1 -timeout=20m ./internal/platform
```

These tests create isolated Computers and containers, exercise actual enrollment and command/file operations, and clean up the resources they create. Use a disposable development environment.

Verify a cold pull by immutable digest and a cached-image creation without a client-side image build. Inspect the stopped-container creation and Daemon copy before startup, the data-volume and read-only enrollment mounts, and the absence of a new host-runtime mount. Confirm the copied program has the target Linux architecture and executable permissions. Retry failures at pull, create, copy and start without creating duplicate containers or reporting a connection before the Daemon is authenticated.

The runtime compatibility targets are macOS Docker Desktop/OrbStack/Colima/Docker-backed Rancher Desktop, Linux Docker Engine/rootless Docker/Docker Desktop, and Windows Docker Desktop with Linux containers. For each actual run record provider/version, host OS, engine architecture, Docker context, image digest and result. A provider-detection fixture or the ordinary native OS matrix does not establish live compatibility with every provider. Keep untested combinations explicit.

The current [CI workflow](../.github/workflows/ci.yml) is the executable source of truth for automated checks. Results are available in [GitHub Actions](https://github.com/oines/astral/actions). Its matrix runs native Windows, macOS and Linux tests, downloaded installers, current-user Daemon lifecycle and packaged Desktop startup/shutdown, plus the full Ubuntu container and in-place update checks. Its platform matrix is not a claim that every installation format or architecture has already passed interactive acceptance.

`pnpm test:daemon-service` installs and removes an isolated current-user service. Run it in a disposable user session with the platform service manager available. `pnpm test:packaged-desktop` checks a previously packaged App; Linux CI installs the native DEB and runs it in an X display so its sandbox hooks are exercised.

The packaged Desktop check also inspects the real renderer's titlebar layout, native Window Controls Overlay safe area on Windows/Linux, interactive controls, and light/dark appearance. It retains small cropped screenshots of the fresh onboarding and Appearance pages as CI artifacts. These show renderer layout, not operating-system caption-button pixels; physical dragging, double-click maximize/restore, resizing and window snapping remain native interaction checks.

The native Linux job also runs `node scripts/check-headless-install.mjs`. It uses the actual built Server and a synthetic local release transport to check current-user service installation, authenticated HTTP readiness, identity-preserving reinstall, checksum rejection before stopping the old service and failed-start recovery. This check requires a disposable Linux user session and refuses an existing `astral-server.service`. It does not validate publisher signing or authorize a candidate release for publication.

## Manual acceptance

Use a separate data directory and synthetic credentials, accounts, conversations and files. Do not record active enrollment tokens, QR authorization codes, provider keys or private conversations in screenshots or logs.

- Exercise first use, model configuration, a real tool call, cancellation, restart and retained conversation history.
- Check each advertised operating system and architecture. Cross-compiling an executable does not establish that it runs on its target system.
- For Desktop, test installation, embedded Server startup, normal Quit, parent-death cleanup, menu/tray behavior and optional login startup.
- For standalone Daemons, test current-user service visibility, enrollment, restart, stop/start, identity-preserving update and uninstall.
- For containers, verify command/file use and update while retaining container identity, original environment image, pairing/data mounts, installed software, writable-layer markers and data. Updating the application must not recreate an existing container, including one created by an older application-built image.
- Apply the layout, keyboard, language, theme and narrow-screen checks in the [UI/UX specification](ui-ux-spec.md).
- Protocol-fixture tests do not replace live account validation for [IM integrations](im-platforms.md). Record which integrations were actually exercised without publishing credentials or private account data.

## Release acceptance

Before advertising automatic updates, test a real previous-to-next release installation and a skipped-version upgrade with preserved data and credentials. Verify migration failure recovery, program startup failure, download/checksum rejection and process cleanup. A fresh installation smoke test alone does not establish update safety.

The default `check-desktop-update.mjs` constructs a `0.0.0` App from the candidate's own source and Go programs, then updates it through a loopback feed to the final candidate bytes. This checks the native updater mechanism, not compatibility with a previously published application.

After publishing the next version, a separate disposable native GitHub Actions user can run `node scripts/check-desktop-update.mjs --published-from v0.1.0`. This downloads the untouched old official package and verifies its public manifest, SHA256SUMS, and Mac signature. The old App keeps its fixed official Latest feed; no old ASAR, signature, update source or main-process inspector is changed. The target manifest must identify the exact checked-out release source, and Latest must identify that target version. The check preserves an idle persisted Session/model selection, data marker, encryption key and computer identity, verifies old process exit and relaunch, then compares the installed AppImage or ASAR plus Server binary with verified public bytes. It retains a small `astral-published-desktop-update.json` evidence report. This is post-publication acceptance: failure does not retract a Release that is already public. If a newer release has taken Latest, the check fails rather than installing and claiming the wrong target.

Keep the same-source fixture, real previous-release update, and skipped-version/migration tests as distinct evidence. Do not run installation/replacement fixtures against a developer's existing App, services, containers or user data.

Published installers require their own signing, notarization and installation checks. Development packages are currently unsigned; see [deployment](deployment.md). Record the source commit, platform, architecture and result for each acceptance run. Keep raw local screenshots and temporary fixture state outside tracked source; publish only deliberately reviewed examples.
