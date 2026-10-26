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

`.github/workflows/release.yml` follows the same broad flow as Magpie: tag, build, sign, notarize, verify, publish. It also supports a manual run from `main` with an existing tag. The source must be in trusted `main` history, both manifests must match the tag, and ordinary `main`/push CI must have succeeded for that exact commit.

A manual run with an empty tag exercises only Mac signing and notarization. It creates CI artifacts without creating a tag or GitHub Release:

```sh
gh workflow run release.yml --ref main
```

For a tagged release:

1. Build all six unified Server/Web UI/CLI/Daemon programs once on Linux. Read the actual Host/Core data formats from the built native executable.
2. Sign and notarize both Darwin programs on a disposable Mac runner, then regenerate checksums. Sign each Electron App, notarize its final DMG, staple the DMG and original App, and create the ZIP from the stapled App. Reopen final DMG/ZIP files and verify signatures, architecture, tickets and Gatekeeper. Run the runner-native ZIP App through startup and shutdown acceptance. See [Apple signing](apple-signing.md).
3. Package Windows and Linux using that same distribution, including the signed Darwin download programs. Extract the final native Windows ZIP and verify startup, renderer, local Daemon and process cleanup. Install the final native Linux DEB and verify startup and normal quit. Run the final headless programs through real current-user service installation and failure recovery using the CI release transport.
4. Require every expected asset, its exact final hash, accepted Apple submissions and the supported data baseline. Assemble the public manifest and release notes.
5. Create or reuse a matching draft, upload every asset, and verify GitHub's returned name, size, SHA256 digest and upload state. Only then publish it. A failure before publication leaves a draft. An existing published Release is never overwritten; an exact matching retry is read-only.

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
- `mac-signing-report.json`, containing three distinct accepted Apple submissions, signature/package checks, certificate expiration and exact Mac artifact hashes.
- `RELEASE_NOTES.md` and `SHA256SUMS`; the latter covers every other attachment, including the manifest and notes.

Public manifests use `schema_version: 1`, `purpose: "release"` and `public_release_ready: true`. This means the installation assets passed release gates. It does not claim every future update feature is implemented. macOS is signed and notarized. Initial Windows and Linux packages are unsigned; Windows may show an unverified-publisher/SmartScreen prompt. The Apple certificate cannot sign Windows programs.

Only the runner's native architecture is exercised. Producing and inspecting the other architecture's packages does not prove native execution there. Checksums detect changed bytes but do not independently establish publisher identity.

The collector retains an explicit `candidate` command for review-only fixtures or drafts. Its unsigned default cannot become public. A public collection requires the signed Mac evidence. Candidates keep `public_release_ready: false`; the default draft uploader cannot publish them. The landing page must link only verified public releases; the VPS installer rejects manifests that are not ready. Until the first successful tagged workflow publishes its real assets, public download URLs may return 404.

## Initial data baseline and updates

The first public data baseline is **Host schema 3 / Core format 10**. The collector and publisher both enforce it. An unexpected format change blocks publication until versioned migration support and its acceptance tests are implemented. Existing data already using these formats can be retained. Earlier private development formats are outside this baseline and require a fresh installation; never relabel a database's format without converting its data.

Current releases support fresh installation and manual updates that retain the same data formats. Desktop users can install the next package over the existing App while retaining its data directory. The [Linux VPS installer](headless-install.md) checks both programs' format metadata before stopping the old current-user service and activating a replacement. A format mismatch is rejected. [Computer Daemon update commands](deployment.md#updating-an-enrolled-computer) retain the existing native service or container; they do not rebuild the Agent environment.

These capabilities remain unfinished and are recorded as such in the manifest:

- **Goose versioned migrations.** Implement them before changing published persistent formats. Keep every released migration immutable and apply each intervening data step when users skip application versions. Use SQL for suitable schema changes and self-contained Go migrations for JSON/history transformations. Normal runtime code accepts only the current format. Use the unified Go program for migration commands; a separate daemon or migration executable is not required. Back up Host/Core databases, keys and relevant files together, exclude a running Server, and test failure recovery. A per-database Goose transaction is not coordinated recovery across databases and files.
- **Desktop one-click updates and production update metadata.** Implement the updater, safe asynchronous shutdown and verified feed before advertising one-click installation. Preserve per-architecture metadata, hashes and blockmaps when needed, then test a real N→N+1 install with retained data and failed-update recovery. Development builds without an updater require one manual installation.
- **Remote Daemon update RPC.** Add the update action through the existing connection protocol, retain the copy-command fallback, and remind the user to update the Server if the Daemon is newer. Container updates must replace the mounted Daemon without recreating the container or losing installed tools and workspaces.

The application patch number does not imply a data migration. A release that changes no persisted format needs no empty migration script. Automated migrations must ship before the first incompatible public format change, rather than being replaced by runtime compatibility branches.

## First-release acceptance and download entry points

The source repository is public. History/credential review, attachment and asset-license cleanup, and public dependency checks are complete; retain secret scanning and push protection for later work.

Before advertising downloads, require a successful real tagged workflow and inspect its complete assets and manifest. The final `published-install` job downloads the installer and distribution from the actual published tag, verifies the installer checksum, installs a current-user Server service, and checks its health, authenticated API and online local Computer before removing the test service. This runs under a disposable Linux CI user. It is a post-publication check: a failure does not retract an already published Release. The earlier native CI headless checks use actual programs with a local synthetic release transport and verify installation/recovery before publication. Link the landing page to the actual published Desktop assets and the documented user-mode Linux VPS command only after these checks pass.

Optional later work includes Windows commercial signing, Linux package repositories, differential downloads, staged rollout, generated changelogs, SBOM/provenance attestations and more native architecture coverage. These do not require additional release channels.

References: [Magpie release workflow](https://github.com/yetone/magpie/blob/main/.github/workflows/release.yml), [Magpie distribution workflow](https://github.com/yetone/magpie-releases/blob/main/.github/workflows/release.yml), [electron-builder v26 macOS](https://www.electron.build/v26/docs/mac/), [v26 auto update](https://www.electron.build/v26/docs/features/auto-update/) and [GitHub workflow-token permissions](https://docs.github.com/en/actions/tutorials/authenticate-with-github-token).
