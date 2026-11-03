# Releases

Astral has one rolling release line. Application changes merged into `main` automatically prepare, validate, package and publish the next version. There is no separate Stable/Canary channel, manually selected candidate, local tag-signing step or second release build.

## Versions and source identity

Application versions retain the three-part `MAJOR.MINOR.PATCH` form, without leading zeroes or prerelease/build suffixes. The current series uses `0.1.(13 + prepare run number)`: the new preparation workflow's first run is `0.1.14`. The workflow counter is part of this version scheme; changing its identity or the version base requires a reviewed migration rather than silently resetting numbering. Failed and cancelled runs may leave gaps. A rerun keeps its original run number and version; a code change creates a new preparation and version. Application versions remain independent of persisted Host/Core data formats.

Before building, `prepare-release.yml` freezes `release-build.json`: repository, exact full `source_sha`, effective version, producer run ID, attempt, number, URL and workflow path. Go and Electron use that same effective version. The two committed package manifests remain a matching development baseline; CI applies the frozen version only in disposable checkouts and never commits a version bump back to `main`.

The manifest records this producer metadata as `build_provenance`, plus the packaging run URL. A package is identified by source, version, producer and its actual digest, not by an artifact name or whichever commit `main` contains later. The published `vMAJOR.MINOR.PATCH` annotated tag points to that exact source commit. CI creates it using the restricted release identity; it does not depend on a maintainer's personal GPG key. GitHub OIDC provenance attests `SHA256SUMS`, which binds every other public file, including the source/build manifest and release notes. Public data and package signatures keep their own verification contracts.

## One release line, two stages

### Preparation

`prepare-release.yml` responds to application changes on `main`. It runs source checks and the six-program distribution build in parallel. Preparation retains Go race, frontend checks and cross-platform native source tests. Service lifecycle, final installers and the pinned multi-architecture Computer environment are checked in the protected stage against the shared release programs instead of producing another set of release packages.

This stage is cancellable: a new main change supersedes obsolete preparation work. Successful preparation retains its frozen metadata and unsigned distribution as immutable Actions artifacts. The release consumer verifies the producer workflow, repository, event, source in trusted main history, effective version and artifact identity/digest before using them. Failed or cancelled preparation cannot publish.

### Signing, packaging and publication

A successful preparation starts `release.yml` automatically. This stage is protected from cancellation by later merges: at most one release runs, with only the latest pending release request retained. A superseded request is checked before entering the protected work. New merges do not interrupt a release already signing, verifying or uploading.

The protected stage performs these operations once:

1. Sign both Darwin programs, refresh all six program checksums and share that same distribution with every desktop build. Windows and Linux packages also include the signed Darwin programs used to enroll other computers.
2. Notarize the signed standalone Mac programs once, in parallel with desktop packaging. Windows/Linux do not wait for that Apple submission.
3. Build, sign and notarize Intel and Apple silicon Apps concurrently on their respective native runners. Each opens the final ZIP and DMG, checks signatures, architecture, tickets and Gatekeeper acceptance, exercises native startup/shutdown, and validates an actual update with retained data. The signing report aggregates both native architectures. See [Apple signing](apple-signing.md).
4. Build Windows and Linux packages once from the shared signed distribution. Retain final Windows ZIP startup/cleanup, NSIS replacement, installed Linux DEB startup, AppImage replacement and current-user headless installation/recovery acceptance. Pre-publication update fixtures use the same payload with an older App version and a loopback feed; they are distinct from untouched official previous releases.
5. Require all three Apple submissions to be `Accepted`. Confirmed uploads that remain pending, unknown status, rejected submissions, invalid signatures or failed native checks block publication. Waits and queries retain the same Apple ID; a timeout does not trigger another upload or authorize pending publication.
6. Assemble and validate the final complete bundle once, including packages, manifest, signing report, six update feeds, notes and both checksum lists. Seal that bundle as an immutable artifact with the digest of `SHA256SUMS`, and retain OIDC provenance for that inventory.
7. Download the sealed artifact by identity/digest and verify every local file against it. Publishing does not run the collector again, rebuild, sign, staple or rewrite any file. Create or reuse a matching draft, upload its exact files and verify GitHub's names, sizes, SHA256 digests and upload states. Only after the complete inventory matches does one publication operation make it public and select Latest.

Publication remains serialized and compares numeric versions, so an older completion cannot move Latest backwards. Published tags and assets are immutable. An interrupted upload can continue only with the original sealed artifact and byte-identical existing draft assets. Use Rerun failed jobs in that same release run after tag creation; a fresh dispatch has a different packaging identity and is not a continuation of those bytes. A retry cannot replace a published version or use newly rebuilt bytes under its tag.

## Failure and post-publication checks

A failed required stage blocks publication and retains its stage-specific diagnostics. For Apple delays, preserve the original submitted archive or DMG together with its receipt and SHA256; use the explicit same-ID resume command described in [Apple signing](apple-signing.md#checking-a-delayed-submission). Restarting a packaging job is a rebuild, not proof that it resumed the old Apple input.

After publication, native jobs download untouched previous official desktop packages, use their unchanged official Latest feed, and verify actual upgrade, old-process exit, retained Session/model records, keys and Computer identities. Mac Intel and Apple silicon both run natively; Windows and Linux currently exercise x64. A separate disposable Linux user installs the real published Server files and checks service startup, authenticated API and the online Local Computer. These checks retain evidence of the real public downloads. Their failure does not retract or modify an already public release.

The read-only `verify-published-update.yml` workflow remains available to retry public upgrade acceptance against existing `tag` and `previous_tag` values. It reads the published effective version and exact source rather than requiring development package versions to equal a historical release. It does not build, publish, move tags or replace assets.

Signing credentials are limited to disposable Mac jobs in the protected `mac-signing` Environment. They use temporary keychains and unconditional cleanup. Pull-request and Linux/Windows jobs receive no Apple credentials. Only the publication stage receives repository write permissions. OIDC authentication uses job-scoped permissions rather than a personal access token or local signing key. Signing Environment policy must allow the trusted main release jobs without a per-release approval if unattended publishing is required; credential setup and deliberate policy changes remain separate administrative actions.

## Computer environment images

The Ubuntu Computer environment has an independent multi-architecture image publication pipeline. Only changes to its Dockerfile or other build inputs require a new Linux amd64/arm64 environment image. An application release references a reviewed immutable image digest; application version changes and Daemon changes alone do not rebuild that environment. Verify that the pinned image is anonymously pullable for both architectures before publishing an application that requires it.

For a recipe change, the repository owner explicitly dispatches `computer-image.yml` on the reviewed candidate branch. This builds, tests and publishes that exact candidate without App signing credentials. Download its successful `computer-image-lock` artifact, replace `internal/platform/computer-image/image-lock.json`, run `node scripts/computer-image.mjs verify`, and include the lock in the same PR as the recipe. Ordinary CI rejects a stale lock, so do not wait for the recipe to merge before preparing its published digest. PR-triggered image jobs remain read-only and never publish; automatic publication runs only on `main` when build inputs change. An owner-dispatched rebuild can also refresh upstream security packages without changing the recipe; adopt its newly verified lock in a PR.

Applications pull the pinned environment, create the data volume and container, copy the matching Linux unified Astral program into its writable layer, and then start the existing supervisor. No user-side image build or host-mounted runtime directory is part of this path. Existing containers, including containers created by older application-built images, are retained across application updates. Daemon maintenance replaces only their program; it does not recreate the container or upgrade the environment image. Record the resolved digest, selected architecture and actual runtime in container acceptance evidence.

## Public assets and capabilities

A complete public Release contains these six unified executable names:

```text
astral-darwin-amd64
astral-darwin-arm64
astral-linux-amd64
astral-linux-arm64
astral-windows-amd64.exe
astral-windows-arm64.exe
```

All six are needed by a Server or Desktop that offers enrollment of other platforms. Other assets are:

- Desktop DMG/ZIP, NSIS/ZIP and AppImage/DEB files named `Astral-${version}-${os}-${arch}.${ext}`. Mac and Windows use `x64` or `arm64`; Linux AppImages use `x86_64` or `arm64`, and Linux DEBs use `amd64` or `arm64`.
- `checksums.txt`, covering exactly the six unified executables.
- `LICENSE.txt`, `THIRD_PARTY_NOTICES.txt` and the versioned Linux `install-server.sh`.
- `release-manifest.json`, identifying the source, CI/build runs, runtime data formats, asset sizes/hashes and implemented update capabilities.
- `mac-signing-report.json` schema 3, containing three distinct Accepted Apple submission IDs, signature/package checks, certificate expiration, both actual `native_tested_arches` and exact Mac artifact hashes. Historical schema 2 reports remain readable as historical evidence; they do not establish new dual-native acceptance.
- Six Desktop update feeds: `latest-x64.yml`, `latest-arm64.yml`, `latest-x64-mac.yml`, `latest-arm64-mac.yml`, `latest-x64-linux.yml` and `latest-arm64-linux-arm64.yml`. Each names one exact versioned NSIS, Mac ZIP or AppImage asset and its final-byte SHA-512 and size.
- `RELEASE_NOTES.md` and `SHA256SUMS`; the latter covers every other attachment, including the manifest and notes.

Public manifests retain `schema_version: 1`, `purpose: "release"` and `public_release_ready: true`. They include frozen `build_provenance` and the packaging run identity. New rolling releases require `signing.macos: "signed-and-notarized"`; historical releases may truthfully record an older pending policy. Windows and Linux packages remain unsigned, and Windows may show an unverified-publisher/SmartScreen prompt. The Apple certificate does not sign Windows programs.

Mac x64 and ARM64 are exercised on separate native runners. Windows and Linux currently exercise x64; producing their ARM64 packages does not establish ARM64 execution. Checksums bind exact bytes, while Developer ID and the OIDC inventory provenance establish the corresponding signing and workflow identities.

The collector's `candidate` command remains a review-only fixture utility, not a required publication track. Its unsigned output cannot become public. The landing page only advertises published releases. Its browser enhancement selects a complete GitHub Latest version and updates all eight download links and the matching release page together. A known verified, version-labelled static fallback stays usable if the API or JavaScript is unavailable; it is not a Latest claim and does not need a version-bump PR for each release. The Mac installation text refers to the selected release page's report rather than hard-coding a changing release's notarization claim.

## Initial data baseline and updates

The first supported public data baseline is **Host schema 3 / Core format 11**. Release metadata reports the actual current targets and those fixed baselines, rather than treating every later format change as unsupported. Earlier private development formats are outside the chain and are rejected; never relabel a database without converting its data. The application patch number is independent of the data version, and a release without a persisted-format change needs no empty migration.

**Versioned migrations.** Host and Core own separate Goose chains containing immutable SQL steps and self-contained Go transformations. The latest Go binary embeds every supported historical step; a user upgrading across several application releases downloads only the latest program, which applies all missing data steps in order. Normal runtime code accepts only its current format. See [the migration contract](migrations.md) for the maintenance CLI, frozen data definitions, per-step transactions and recovery acceptance.

At Server startup, including the Server managed by Desktop, the process obtains exclusive ownership before recovering an interrupted upgrade or inspecting the data. Pending migrations trigger a consistent snapshot of the Server directory, including both databases, keys and relevant configuration. A recovery journal stays through migration, store opening and persistent-state preparation. The Server commits and removes it at the beginning of `Start`, before local control, IM or HTTP accepts work. A failed step or pre-commit preparation closes database users and restores that snapshot; a pre-commit crash leaves the journal for the next startup. After commit, later restarts do not silently restore old data and discard acknowledged work. Installer health-check recovery is a separate, explicit restoration of this attempt's snapshot; Desktop does not promise automatic App rollback for every later failure. The backups live beside the Server directory in `upgrade-backups/`. They do not back up Computer work files or reverse external side effects.

**Desktop updates.** Packaged official Mac Apps, current-user Windows NSIS installations and Linux AppImages use `electron-updater`. They check the fixed public GitHub feed after launch and every six hours; automatic and manual checks fetch version metadata without downloading a package. An available update remains eligible for later checks. Settings → About & diagnostics has one state-dependent action: check, **Update and restart** when available, or the corresponding retry. Updating checks Latest again, explicitly downloads the complete package, validates its per-platform/architecture SHA-512 metadata, then stops the managed Server and waits for its process and pipes to exit before installation and restart. The current service remains usable during download, and the task-interruption consequence is explained before the click. Check retries never download or install. Ordinary quit never installs an update, and downgrades are disabled. Development or unsigned/ad-hoc Mac builds, Windows ZIP copies and Linux DEB installs show a manual-download route. A build without the updater needs one manual installation to join this path.

An authenticated Owner in the Web UI can use the same Desktop update action. The managed Go Server forwards empty check/update intents to its Electron parent over a private authenticated loopback bridge whose endpoint and token arrive through their private startup handshake. Browser and Agent input cannot select its endpoint, token, installer URL or executable. Go never replaces an App directly. Reconnection must match the persisted operation ID, target/operation version, terminal operation state and actual current version; a dropped HTTP connection alone neither confirms failure nor warrants another update.

The release collector creates the six update feeds after Mac signing, packaging and any stapling have finished. The feed URLs name immutable version-tag assets, and its hashes and sizes must match the exact final downloadable bytes. Blockmaps/differential downloads are disabled for this first implementation. Do not reuse electron-builder metadata produced before final signing or stapling.

**Daemon updates.** The authenticated Owner API can trigger a managed remote Daemon update over its existing pinned TLS connection. The Server resolves the official release; browser/Agent input cannot select arbitrary executable URLs. The update retains Computer identity, waits for a new boot identity and the expected version, and reports failure or timeout. A Daemon newer than its Server shows a reminder to update Desktop/Server, without blocking further Daemon updates. The action always resolves the latest release even when Server and Daemon currently match; an equal or older target is a no-op. Native one-shot update jobs and the new in-container supervisor own replacement independently of the old Daemon. Container updates preserve the original container, writable layer, installed tools and work directories. Older installations without that capability use the retained shell/PowerShell update command. See [Daemon maintenance](deployment.md#updating-an-enrolled-computer).

A container supervisor that rejects a handed-off update restarts the unchanged Daemon to release its in-memory update guard; this is a failed update, not a successful upgrade. Supervisor code itself stays resident while child Daemons are replaced. After installing a program with supervisor changes, restart the same container once to load those changes; recreating the container or its environment is unnecessary.

The official Linux VPS installer records its owned installation root, data directory, UID and user-service definition. Web updates are available only when this metadata, the actual current executable, the loaded user unit and running PID/arguments all match. `--installation-root` / `ASTRAL_INSTALLATION_ROOT` identifies the root but cannot bypass those checks. Older installer-managed services need one manual installer run to establish the current marker and unit. Foreground `serve`, custom services and Server containers retain a manual update route.

An Owner update creates a persistent operation under the installation root's `updates/`, outside the Server migration snapshot, and starts a separate visible one-shot systemd user job. It resolves and downloads the fixed official release using an immutable tag and verified installer checksum before stopping the Server service. The installer retains the root lock and performs the existing program switch, startup Goose migration and health check. Failed activation finishes restoring the previous program/service and any snapshot created by this attempt before the job records its terminal result. Same-format updates which create no new snapshot do not restore an unrelated historical backup. The webpage waits through disconnection and verifies the operation identity and actual version after return. Manual data recovery requires a matching application version, rather than asking an old binary to read newer data.

## First-release acceptance and download entry points

The source repository is public. History/credential review, attachment and asset-license cleanup, and public dependency checks are complete; retain secret scanning and push protection for later work.

Before treating a new version as available, require its successful automatic publication and inspect its complete assets and manifest. Code, fixture tests and a successful package build do not establish that a Release is published or that an actual N→N+1 Desktop update has passed; retain those as separate acceptance evidence. The final `published-install` job downloads the installer and distribution from the actual published tag, verifies the installer checksum, installs a current-user Server service, and checks its health, authenticated API and online local Computer before removing the test service. This runs under a disposable Linux CI user. It is a post-publication check: a failure does not retract an already published Release. The earlier native CI headless checks use actual programs with a local synthetic release transport and verify installation/recovery before publication. The website uses the public release entry points; post-publication failures remain visible in Actions and are not permission to rewrite public files.

For updates from the first public release, the native post-publication command is `node scripts/check-desktop-update.mjs --published-from v0.1.0`. Unlike the pre-publication `0.0.0` fixture, it installs the real old release, verifies its SHA256SUMS and Mac Developer ID signature, and uses the old App's unchanged official Latest source. It verifies the exact target source/version before installation, retained Session/model records, key and Computer identity, old-process exit, target Server health and final installed content against the public packages. Run it only in fresh disposable native CI accounts; Linux requires an X display. Its JSON evidence identifies both source commits and package hashes without storing credentials. A missing/newer Latest or a failed native replacement is a failed acceptance result, not permission to rewrite an existing public Release.

Optional later work includes Windows commercial signing, Linux package repositories, differential downloads, generated changelogs, SBOMs and more Windows/Linux native architecture coverage. These do not require additional release channels.

References: [Magpie release workflow](https://github.com/yetone/magpie/blob/main/.github/workflows/release.yml), [Magpie distribution workflow](https://github.com/yetone/magpie-releases/blob/main/.github/workflows/release.yml), [electron-builder v26 macOS](https://www.electron.build/v26/docs/mac/), [v26 auto update](https://www.electron.build/v26/docs/features/auto-update/) and [GitHub workflow-token permissions](https://docs.github.com/en/actions/tutorials/authenticate-with-github-token).
