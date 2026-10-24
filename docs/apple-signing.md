# Apple signing and notarization

The release workflow signs with the personal Developer ID Application team `6N6W6Z6739`. It never uses Apple Development or another team's identity. Ordinary pull-request builds remain unsigned and do not receive Apple credentials.

Pushing an existing version tag starts `.github/workflows/release.yml`. The selected commit must belong to `main`, both package versions must match the tag, and ordinary `main` CI must have succeeded for that exact commit. A manual run from `main` can select an existing tag. Leave the tag empty for an initial Mac-only signing/notarization acceptance run on `main`; it uploads CI artifacts without creating a tag or Release. The workflow cannot create or move tags.

This currently produces a **Mac-signed candidate Draft Release**. It does not publish a public release: versioned migrations, Desktop updating and their acceptance gates remain unfinished. Windows and Linux candidate packages are still unsigned.

## One-time credential setup

Keep all credentials out of the repository, chat, logs and build artifacts. The setup helper sends secrets directly to the `mac-signing` GitHub Environment in `oines/astral` using the authenticated `gh` CLI.

1. Open Keychain Access, select **login → My Certificates**, and find `Developer ID Application: LIN KAIYUAN (6N6W6Z6739)`. Export this identity, including its associated private key, as a password-protected `.p12`. Export only this identity. Save it in a private directory outside the checkout, and retain its password securely. A `.cer` alone cannot sign software on CI.
2. Sign in to [Apple Account](https://account.apple.com/) and choose **Sign-In and Security → App-Specific Passwords**. Generate one labelled `Astral GitHub Actions`. Use this dedicated password, never the primary Apple Account password. See [Apple's instructions](https://support.apple.com/en-us/102654).
3. From the checkout root, run this yourself in an interactive terminal:

   ```sh
   node scripts/setup-apple-signing.mjs
   ```

   Enter the encrypted P12 path, its password, the developer Apple ID and the dedicated app-specific password when prompted. Password input is hidden. The helper validates the certificate's public metadata and intended team before uploading; it never extracts a plaintext private key. It asks for confirmation before uploading to the named repository/environment.

The helper creates or checks these Environment secrets:

| Name | Value |
| --- | --- |
| `MAC_CERTIFICATE_P12` | Base64 of the encrypted P12 identity |
| `MAC_CERTIFICATE_PASSWORD` | P12 export password |
| `APPLE_ID` | Apple Account enrolled in the personal developer team |
| `APPLE_APP_SPECIFIC_PASSWORD` | Dedicated notarization password |

The Environment variable `APPLE_TEAM_ID` is `6N6W6Z6739`. Its deployment policy allows only the `main` branch and `v*` tags. Existing reviewers and other protections are retained; unexpected broader branch policies stop setup rather than weakening protection. Secrets belong to this Environment, not repository-wide PR jobs. Environment policy and trusted-history checks complement one another: a matching tag still has to point into `main` and pass exact-source CI.

The local helper does not build, sign, commit, tag or publish. If setup fails partway through uploading, rerun it to complete the same configuration. It does not delete your original encrypted P12 backup. Updating the primary Apple Account password revokes app-specific passwords; generate a replacement and rerun setup when that happens.

## CI execution and cleanup

The Mac job installs dependencies and builds Desktop sources before it imports credentials. `scripts/mac-credentials.mjs setup` creates a temporary keychain under `RUNNER_TEMP`, validates the identity and expiry, and validates notarization credentials with `notarytool store-credentials`. Subsequent commands receive a keychain/profile reference rather than the Apple password. Certificates approaching expiry produce a warning. An invalid or absent credential fails the job; there is no unsigned fallback.

The keychain password is generated for each job. The original keychain search list is saved and restored; the encrypted P12 import file and temporary keychain are deleted on setup failure and by an `always()` cleanup step. Cleanup failures fail the job. Hosted runners are disposable, including when a workflow is forcibly cancelled.

The signing and packaging order is:

1. Sign both standalone Darwin Go programs with Hardened Runtime and a secure timestamp; regenerate checksums for all six distribution programs.
2. Submit the two Darwin programs in a ZIP to Apple and require `Accepted`. Bare executables cannot carry stapled tickets; their notarization is recorded by Apple.
3. Package each Electron App with the independently configured release signing identity. The pre-signed Go programs are excluded from repeat signing. Electron receives only the JIT entitlement; Go receives none. Verify every enclosed Mach-O, architecture slice, identity, runtime, timestamp and embedded checksum before proceeding.
4. Submit each App as a temporary ZIP, require `Accepted`, staple/validate the App and assess it with Gatekeeper.
5. Generate the final ZIP from the stapled App. Build the DMG from that same prepackaged App, sign/notarize/staple/validate the DMG, and assess its signature with Gatekeeper.
6. Extract the final ZIP and mount the final DMG read-only; recheck the enclosed Apps. Run the native architecture's ZIP-extracted App through the existing startup/renderer/local-Daemon/shutdown smoke. The other architecture is packaged and signature-checked, not claimed to have run natively.
7. Produce `mac-signing-report.json` with the source, certificate expiration, five accepted Apple submission IDs, checks and exact hashes of the two Darwin programs and four Mac packages. Only then upload artifacts and assemble the draft's final checksums.

All Desktop platforms receive the same signed Darwin download programs. The collector rejects stale hashes, another team, missing proof or incomplete inventories. Missing migration/updater acceptance keeps `public_release_ready: false` even when Mac signing passes. The draft uploader has no publish operation.

## Certificate replacement

Before the current certificate expires, obtain another Developer ID Application certificate in the same team and rerun the P12 setup. Keep developer membership active to request replacements. Previously signed and notarized Apps with secure timestamps continue working after normal certificate expiration; future builds use the replacement certificate. See [Apple's expiration guidance](https://developer.apple.com/help/account/certificates/create-developer-id-certificates).

References: [Apple notarization workflow](https://developer.apple.com/documentation/security/customizing-the-notarization-workflow), [GitHub's temporary-keychain pattern](https://docs.github.com/en/actions/how-tos/deploy/deploy-to-third-party-platforms/sign-xcode-applications), [electron-builder v26 Mac configuration](https://www.electron.build/v26/docs/mac/).
