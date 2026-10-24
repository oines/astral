# Release preparation

Astral uses one rolling release line with calendar versions: `YYYY.M.N`, for example `2026.10.1`. The month and within-month release number have no leading zeroes; `N` starts at 1. The Git tag is `vYYYY.M.N`. There are no beta/stable channels. Application versions and data format versions are independent.

## What is implemented

`scripts/release-version.mjs` provides one command for both manifests:

```sh
node scripts/release-version.mjs set 2026.10.1
node scripts/release-version.mjs check --tag v2026.10.1
```

`set` changes only the root and Desktop `package.json` versions. It does not commit, create a tag, install packages, or build. Review and commit both files together. The lockfile does not store the workspace package versions. The normal Go build injects the root package version; Electron reads the Desktop package version.

`.github/workflows/release.yml` is named **Signed Mac release candidate draft**. It is a preparation pipeline, not a public-release workflow. It accepts a pushed version tag, or a manual run from `main` with an existing tag. It verifies the tag, manifest agreement, that the source commit belongs to `main`, and that the ordinary `ci.yml` push workflow succeeded for that exact commit. A failed or absent native CI run blocks packaging. Pushing a tag before CI finishes requires rerunning the candidate workflow after CI succeeds. For the first credential/signing acceptance, manually run it from `main` with an empty tag: only the Mac signing path and its artifacts run; no version tag or Draft Release is created.

The workflow builds the six unified Server/Web UI/CLI/Daemon programs once on Linux. A dedicated Mac job signs and notarizes the two Darwin programs, refreshes all six checksums, and produces signed, notarized, stapled Intel/Apple Silicon DMG and ZIP files. Windows and Linux then reuse that signed distribution for their own unsigned x64/ARM64 NSIS/ZIP and AppImage/DEB packages. Source and package identity are pinned to the validated commit. The final Mac archives are reopened for verification, and the runner-native ZIP App must pass startup and shutdown acceptance. The other Mac architecture is not claimed to have run natively. See [Apple signing setup and execution](apple-signing.md).

The final job verifies the complete inventory, computes checksums for the exact candidate bytes, and creates a **Draft Release only**. It never publishes, changes repository visibility, or creates/moves tags. It refuses an existing published release or an unrelated draft. Retrying the same draft reuses an existing asset only when GitHub reports the matching SHA256 digest; a different or unavailable digest requires explicit cleanup of that asset. Partial failure leaves a draft.

Keep existing version tags fixed. If source or release-gate changes require a different prepared commit, increase `N` and create a new tag after its checks pass; do not move a candidate's tag to hide that change.

Only the Mac job receives signing secrets, through the `mac-signing` Environment restricted to `main` and version tags. Credentials use a temporary keychain with unconditional cleanup; missing credentials fail the job. Only the final draft job receives `contents: write`; build jobs have read access. It uses the job-scoped `GITHUB_TOKEN`, not a personal access token. Actions are pinned to commits. No signing credentials or tokens belong in an installer, Desktop build, repository source, or pull-request job.

## Download asset contract

Each complete candidate draft contains these exact unified executable names:

```text
astral-darwin-amd64
astral-darwin-arm64
astral-linux-amd64
astral-linux-arm64
astral-windows-amd64.exe
astral-windows-arm64.exe
```

All six are needed by a Headless Server or Desktop distribution that offers enrollment of other platforms. Installing only the server machine's executable would omit other computers' download programs.

Other assets are:

- `checksums.txt`: SHA256 lines for exactly the six unified executables, compatible with Astral's distribution downloads.
- `LICENSE.txt` and `THIRD_PARTY_NOTICES.txt`.
- `install-server.sh`: the versioned user-mode Linux VPS installer. Its eventual public URL is `https://github.com/oines/astral/releases/latest/download/install-server.sh`; this URL must fail until a verified public release exists.
- Desktop files named `Astral-${version}-${os}-${arch}.${ext}`, where `os` is `mac`, `win`, or `linux`, and `arch` is `x64` or `arm64`.
- `release-manifest.json`: version, tag, source SHA, CI/build URLs, current Host/Core formats, executable inventory, file sizes, hashes, and preparation limitations.
- `SHA256SUMS`: final-byte SHA256 lines for every other attachment, including the manifest and notes.
- `RELEASE_NOTES.md`: the draft's review limitations.
- `mac-signing-report.json`: accepted Apple submissions, signature/package acceptance, certificate expiration and exact final Mac hashes, required for Mac-signed candidates.

The workflow produces `schema_version: 1`, `purpose: "mac-signed-candidate"`, `public_release_ready: false`, `signing.macos: "signed-and-notarized"`, and `desktop_updater: "not-implemented"`. The collector's default unsigned-fixture mode remains available without `--mac-signed`; it cannot claim Apple signing or include a signing report. The `assets` inventory includes the generated `checksums.txt` and signing report hashes and sizes as well as every package/installer/notice. It excludes the manifest itself to avoid a circular hash; `SHA256SUMS` covers the manifest separately. The `data_formats: { host, core }` values come from the just-built native program's `astral version --json`, not a copied documentation value. `host_schema` and `core_format` repeat those values for the shell installer contract. Missing or conflicting runtime version/format metadata fails the candidate build. The manifest also records the exact Core module revision and that versioned migrations have not yet been implemented.

The landing page and VPS installer must reject this candidate manifest. A GitHub `latest` 404 before the first published release is an installation failure, not success or a signal to download an unverified development build. The complete public asset contract will become usable only after the public-release gates below exist and a verified release is published.

Checksums detect different bytes; they do not independently prove publisher identity. A signed program and a checksum manifest downloaded from the same origin provide different assurances. Sign final executables before hashing. Astral's HTTP `/downloads/checksums.txt` recalculates the actual distribution binaries; a stale build-directory checksum file must not be copied into a public release.

## Required before a public release

Do not manually publish a candidate draft. This implementation intentionally has no promotion operation and cannot mark `public_release_ready` true. Public release remains blocked until a separate fail-closed signing and acceptance path is implemented.

1. Finish integration of the cross-platform branch with current `main`, including the current Core format/dependency. Complete the Windows current-user Daemon stop/restart/update/uninstall lifecycle, macOS packaged-App Quit, and installed Linux Desktop acceptance on the final source revision. A previous commit's checks and a later fix without an executed test are different evidence.
2. Define the supported data baseline and implement versioned migrations using Goose in a separately packaged migration tool. Simple database changes can use SQL; JSON and historical record transformations need self-contained Go migrations. Normal Server code continues to accept only the current data format. For supported older formats, apply each required data transformation in order even when users skip application releases. Keep published migration steps immutable, reject out-of-order execution, and verify the retained chain in CI. Back up Host/Core data and keys together before modifying persistent data; Goose's per-database transactions do not replace coordinated recovery. Commit a new format only after its step succeeds and reject newer formats from older applications. Package replacement alone is not safe database rollback. Goose integration is planned, not implemented by this candidate workflow.
3. Run and pass the release-only Mac signing/notarization path on the final source with real credentials. The independent `electron-builder.release.cjs` requires Developer ID Application, Hardened Runtime, minimal Electron entitlements and timestamping. `mac-signing.mjs` verifies all enclosed Darwin executable slices and embedded hashes; `mac-notarize.mjs` requires Apple acceptance, stapling, Gatekeeper and final-package checks. These checks are implemented, but a passing fixture is not a real Apple submission or native acceptance run.
4. Complete the one-time [Apple credential setup](apple-signing.md) in the trusted `mac-signing` Environment. An active personal Apple Developer membership and a local identity do not by themselves configure CI. Windows Authenticode signing needs separate Windows credentials; the Apple certificate cannot sign a Windows executable. If an initial Windows release remains unsigned, document the publisher/SmartScreen limitation explicitly; do not represent it as verified publisher signing.
5. Add the Desktop updater and a real update metadata pipeline. There is currently no `electron-updater`, native update IPC, or production feed. Keep macOS ZIP in addition to DMG; use NSIS for Windows and define the Linux AppImage/DEB behavior separately. Await the existing asynchronous Server shutdown before installing; do not silently stop active work. Verify an actual N→N+1 install with retained data and migration failure recovery. Old development builds without an updater require one manual installation.
6. Preserve and combine per-architecture `latest-mac.yml`/Windows metadata rather than overwriting files from separate builder invocations. The current `package:mac-ci` uses separate serial x64 and ARM64 commands. Upload required update `.yml` and `.blockmap` files only after verifying their versions, architecture coverage, URLs and hashes against final signed artifacts. The present candidate collector deliberately rejects update metadata instead of claiming it is usable.
7. A public-release gate must check required signing, notarization, data compatibility, metadata and final native evidence before producing a manifest with `public_release_ready: true`. Only after complete upload and verification should it publish one Release for the validated tag. Never let a missing certificate turn into an unsigned public artifact. Keep ordinary pull-request CI independent of signing secrets.
8. The source repository is public. Its initial source/history credential and attachment review, asset-license cleanup and publicly retrievable dependency checks are complete; retain secret scanning and push protection for subsequent changes. Configure the landing page with published Desktop download links and the documented user-mode Linux VPS installer only after their real assets exist. A draft can coexist with a public source repository while release preparation continues.

Optional later work includes Windows commercial signing if not required for the first explicitly unsigned Windows distribution, Linux package repositories, differential download optimization, staged rollout, generated changelogs, SBOM/provenance attestations, and additional native architecture coverage. Do not add separate release channels to achieve those tasks.

References: [electron-builder v26 macOS configuration](https://www.electron.build/v26/docs/mac/), [v26 notarization](https://www.electron.build/v26/docs/notarization/), [v26 auto update](https://www.electron.build/v26/docs/features/auto-update/), and [GitHub workflow-token permissions](https://docs.github.com/en/actions/tutorials/authenticate-with-github_token).
