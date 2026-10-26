# Releases

Astral uses one rolling release line with three-part versions: `MAJOR.MINOR.PATCH`, starting at `0.1.0`. During the current development series, each release increases the patch number: `0.1.0`, `0.1.1`, `0.1.2`, including multiple releases on the same day. The counter does not reset with a date. Each component is a nonnegative safe integer without leading zeroes; prerelease and build suffixes are not used. Git tags use `vMAJOR.MINOR.PATCH`. Application versions and data format versions are independent.

## Preparing a version

As in Magpie, the maintainer chooses a version and pushes its tag after review and CI. Ordinary source pushes do not choose a version, create a tag or publish a Release.

1. Work on a branch and open a pull request. Keep existing authorship and signatures.
2. For a release, update both manifests together:

   ```sh
   node scripts/release-version.mjs set 0.1.0
   node scripts/release-version.mjs check --tag v0.1.0
   ```

   `set` edits only the root and Desktop `package.json` versions. It does not commit, tag, install dependencies or build. The lockfile does not store these workspace versions. Go builds embed the root version; Electron reads the Desktop version.
3. Merge the reviewed change and wait for all ordinary `main` CI jobs to pass on that exact merge commit. This includes native Windows/macOS/Linux checks, user-mode services, headless installation/recovery and persistent container updates.
4. From that checked `main` commit, create and push a signed tag:

   ```sh
   git tag -s v0.1.0 -m 'Astral 0.1.0'
   git push origin v0.1.0
   ```

   These are maintainer actions, not commands run by the workflow. The example does not claim the tag already exists.

Keep published tags fixed. If a code change is needed, choose the next patch version; do not move a tag to conceal a different source revision. A tag pushed before ordinary CI finishes will fail validation; rerun its release workflow after the exact-source CI succeeds.

## Release workflow

`.github/workflows/release.yml` follows the same broad flow as Magpie: tag, build, sign, submit for notarization, verify, publish. It also supports a manual run from `main` with an existing tag. The source must be in trusted `main` history, both manifests must match the tag, and ordinary `main`/push CI must have succeeded for that exact commit.

A manual run with an empty tag rehearses the full platform pipeline: six binaries, Mac signing/Apple submissions, Windows/Linux packaging, native Desktop installation/update acceptance and the final inventory/feed checks. It retains CI artifacts and verified preview metadata without creating a tag or GitHub Release:

```sh
gh workflow run release.yml --ref main
```

Both tagged releases and empty-tag previews run the following build and acceptance stages:

1. Build all six unified Server/Web UI/CLI/Daemon programs once on Linux. Read the actual Host/Core data formats from the built native executable.
2. Sign both Darwin programs on a disposable Mac runner, then regenerate checksums. Sign each Electron App and its final DMG, and submit the programs and DMGs for Apple notarization. Accepted submissions receive stapling and Gatekeeper checks where applicable; a confirmed upload still pending after the bounded wait may ship with that status recorded. Invalid/rejected submissions, unconfirmed uploads and invalid signatures fail publication. Reopen final DMG/ZIP files and verify signatures and architecture, plus tickets and Gatekeeper when accepted. Run the runner-native ZIP App through startup and shutdown acceptance, plus a real native update from a disposable `0.0.0` fixture installation to the final candidate package. See [Apple signing](apple-signing.md).
3. Package Windows and Linux using that same distribution, including the signed Darwin download programs. Extract the final native Windows ZIP and verify startup, renderer, local Daemon and process cleanup. Install the final native Linux DEB and verify startup and normal quit. Exercise the final Windows NSIS and Linux AppImage through real native updates from disposable `0.0.0` fixtures, checking retained state and old-process exit. These fixtures use the same source with an older App version and a loopback update feed; they are not previously published releases. Run the final headless programs through real current-user service installation and failure recovery using the CI release transport.
4. Require every expected asset, its exact final hash, valid Mac signing evidence and the supported versioned-migration metadata. Generate and revalidate six platform/architecture Desktop update feeds from the final package bytes. Assemble the public manifest and release notes, explicitly distinguishing accepted notarization from pending notarization.
5. For an empty tag, retain the complete signed preview inventory and feed validation without publishing. For a version tag, create or reuse a matching draft, upload every asset, and verify GitHub's returned name, size, SHA256 digest and upload state. Only then publish it. A failure before publication leaves a draft. An existing published Release is never overwritten; an exact matching retry is read-only.

Different tags can build concurrently. The final publication job is serialized; numeric version comparison prevents an older version finishing late from replacing a newer version as Latest. A partial draft can reuse only byte-identical attachments. If a rebuilt attachment differs, remove the affected unpublished draft or asset deliberately before retrying; the workflow will not silently replace it.

Only the Mac job receives signing secrets, through the protected `mac-signing` Environment restricted to `main` and version tags. It uses a temporary keychain with unconditional cleanup; missing credentials fail the job. Only the final publication job has `contents: write`. It uses the job-scoped `GITHUB_TOKEN`, not a personal access token. Actions in the release workflow are pinned to commits. Pull-request CI receives no signing credentials.

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

- Desktop DMG/ZIP, NSIS/ZIP and AppImage/DEB files named `Astral-${version}-${os}-${arch}.${ext}`, with `os` equal to `mac`, `win` or `linux`, and `arch` equal to `x64` or `arm64`.
- `checksums.txt`, covering exactly the six unified executables.
- `LICENSE.txt`, `THIRD_PARTY_NOTICES.txt` and the versioned Linux `install-server.sh`.
- `release-manifest.json`, identifying the source, CI/build runs, runtime data formats, asset sizes/hashes and implemented update capabilities.
- `mac-signing-report.json` schema 2, containing three distinct confirmed-upload Apple submission IDs, their non-rejected status, signature/package checks, certificate expiration and exact Mac artifact hashes.
- Six Desktop update feeds: `latest-x64.yml`, `latest-arm64.yml`, `latest-x64-mac.yml`, `latest-arm64-mac.yml`, `latest-x64-linux.yml` and `latest-arm64-linux-arm64.yml`. Each names one exact versioned NSIS, Mac ZIP or AppImage asset and its final-byte SHA-512 and size.
- `RELEASE_NOTES.md` and `SHA256SUMS`; the latter covers every other attachment, including the manifest and notes.

Public manifests use `schema_version: 1`, `purpose: "release"` and `public_release_ready: true`. This means the installation assets passed release gates. It does not claim every future update feature is implemented. macOS has verified Developer ID signatures; `signing.macos` is either `signed-and-notarized` or `signed-notarization-pending`. Pending notarization may require manually allowing the App in macOS System Settings → Privacy & Security. Initial Windows and Linux packages are unsigned; Windows may show an unverified-publisher/SmartScreen prompt. The Apple certificate cannot sign Windows programs.

Only the runner's native architecture is exercised. Producing and inspecting the other architecture's packages does not prove native execution there. Checksums detect changed bytes but do not independently establish publisher identity.

The collector retains an explicit `candidate` command for review-only fixtures or drafts. Its unsigned default cannot become public. A public collection requires the signed Mac evidence. Candidates keep `public_release_ready: false`; the default draft uploader cannot publish them. The landing page must link only verified public releases; the VPS installer rejects manifests that are not ready. Until the first successful tagged workflow publishes its real assets, public download URLs may return 404.

## Initial data baseline and updates

The first supported public data baseline is **Host schema 3 / Core format 10**. Release metadata reports the actual current targets and those fixed baselines, rather than treating every later format change as unsupported. Earlier private development formats are outside the chain and are rejected; never relabel a database without converting its data. The application patch number is independent of the data version, and a release without a persisted-format change needs no empty migration.

**Versioned migrations.** Host and Core own separate Goose chains containing immutable SQL steps and self-contained Go transformations. The latest Go binary embeds every supported historical step; a user upgrading across several application releases downloads only the latest program, which applies all missing data steps in order. Normal runtime code accepts only its current format. See [the migration contract](migrations.md) for the maintenance CLI, frozen data definitions, per-step transactions and recovery acceptance.

At Server startup, including the Server managed by Desktop, the process obtains exclusive ownership before recovering an interrupted upgrade or inspecting the data. Pending migrations trigger a consistent snapshot of the Server directory, including both databases, keys and relevant configuration. A recovery journal stays through migration, store opening and persistent-state preparation. The Server commits and removes it at the beginning of `Start`, before local control, IM or HTTP accepts work. A failed step or pre-commit preparation closes database users and restores that snapshot; a pre-commit crash leaves the journal for the next startup. After commit, later restarts do not silently restore old data and discard acknowledged work. Installer health-check recovery is a separate, explicit restoration of this attempt's snapshot; Desktop does not promise automatic App rollback for every later failure. The backups live beside the Server directory in `upgrade-backups/`. They do not back up Computer work files or reverse external side effects.

**Desktop updates.** Packaged official Mac Apps, current-user Windows NSIS installations and Linux AppImages use `electron-updater`. They check the fixed public GitHub feed after launch and every six hours, download in the background, and offer **Restart and update** in Settings → About & diagnostics. Installation waits for the managed Server and its pipes to exit. Ordinary quit never installs an update. The updater downloads a complete latest package, validates its per-platform/architecture SHA-512 metadata and does not downgrade. Development or unsigned/ad-hoc Mac builds, Windows ZIP copies and Linux DEB installs show a manual-download route. A build without the updater needs one manual installation to join this path.

The release collector creates the six update feeds after Mac signing, packaging and any stapling have finished. The feed URLs name immutable version-tag assets, and its hashes and sizes must match the exact final downloadable bytes. Blockmaps/differential downloads are disabled for this first implementation. Do not reuse electron-builder metadata produced before final signing or stapling.

**Daemon updates.** The authenticated Owner API can trigger a managed remote Daemon update over its existing pinned TLS connection. The Server resolves the official release; browser/Agent input cannot select arbitrary executable URLs. The update retains Computer identity, waits for a new boot identity and the expected version, and reports failure or timeout. A Daemon newer than its Server shows a reminder to update Desktop/Server, without blocking further Daemon updates. The action always resolves the latest release even when Server and Daemon currently match; an equal or older target is a no-op. Native one-shot update jobs and the new in-container supervisor own replacement independently of the old Daemon. Container updates preserve the original container, writable layer, installed tools and work directories. Older installations without that capability use the retained shell/PowerShell update command. See [Daemon maintenance](deployment.md#updating-an-enrolled-computer).

The Linux VPS installer downloads and validates the latest distribution, stops the current-user service, and lets the new Server perform the same offline startup migration. Failed activation restores the previous program/service and any snapshot created by this attempt. Same-format updates which create no new snapshot do not restore an unrelated historical backup. Manual data recovery requires a matching application version, rather than asking an old binary to read newer data.

## First-release acceptance and download entry points

The source repository is public. History/credential review, attachment and asset-license cleanup, and public dependency checks are complete; retain secret scanning and push protection for later work.

Before advertising downloads, require a successful real tagged workflow and inspect its complete assets and manifest. Code, fixture tests and a successful package build do not establish that a Release is published or that an actual N→N+1 Desktop update has passed; retain those as separate acceptance evidence. The final `published-install` job downloads the installer and distribution from the actual published tag, verifies the installer checksum, installs a current-user Server service, and checks its health, authenticated API and online local Computer before removing the test service. This runs under a disposable Linux CI user. It is a post-publication check: a failure does not retract an already published Release. The earlier native CI headless checks use actual programs with a local synthetic release transport and verify installation/recovery before publication. Link the landing page to the actual published Desktop assets and the documented user-mode Linux VPS command only after these checks pass.

Optional later work includes Windows commercial signing, Linux package repositories, differential downloads, staged rollout, generated changelogs, SBOM/provenance attestations and more native architecture coverage. These do not require additional release channels.

References: [Magpie release workflow](https://github.com/yetone/magpie/blob/main/.github/workflows/release.yml), [Magpie distribution workflow](https://github.com/yetone/magpie-releases/blob/main/.github/workflows/release.yml), [electron-builder v26 macOS](https://www.electron.build/v26/docs/mac/), [v26 auto update](https://www.electron.build/v26/docs/features/auto-update/) and [GitHub workflow-token permissions](https://docs.github.com/en/actions/tutorials/authenticate-with-github-token).
