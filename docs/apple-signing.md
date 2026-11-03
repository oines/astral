# Apple signing and notarization

Astral's automatic rolling release signs with Developer ID Application team `6N6W6Z6739`. Pull-request checks remain unsigned and receive no Apple credentials. Successful main preparation supplies a frozen source/version and immutable program artifact to the protected release stage; maintainers do not run a second candidate or sign a local version tag. See [the release pipeline](releasing.md).

The signed Darwin programs are shared by all desktop packages. Their single Apple submission runs alongside desktop packaging. Intel and Apple silicon Apps are packaged, signed and tested on separate native Mac runners in parallel. New public releases require all three Apple submissions to be Accepted, both native Apps to pass acceptance and all final file hashes to match. Pending or unknown status blocks publication.

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

The Environment variable `APPLE_TEAM_ID` is `6N6W6Z6739`. Its deployment policy allows only the `main` branch and `v*` tags. Existing reviewers and other protections are retained; unexpected broader branch policies stop setup rather than weakening protection. Secrets belong to this Environment, not repository-wide PR jobs. Environment policy and trusted-history checks complement one another: release jobs consume only validated main preparation with a matching source, version and immutable artifact identity. For unattended normal releases, the Environment must allow these trusted main jobs without requiring a human approval for each run; setup preserves deliberate administrator protections rather than weakening them automatically.

The local helper does not build, sign, commit, tag or publish. If setup fails partway through uploading, rerun it to complete the same configuration. It does not delete your original encrypted P12 backup. Updating the primary Apple Account password revokes app-specific passwords; generate a replacement and rerun setup when that happens.

## CI execution and cleanup

Each credentialed job creates a temporary keychain under `RUNNER_TEMP` with `scripts/mac-credentials.mjs setup`, validates the intended certificate/team and expiry, and validates the notarization profile. Dependencies and build inputs are prepared before loading credentials. Every job runs cleanup unconditionally, including failure or cancellation.

The release graph separates these tasks:

1. **Unified program signing:** sign both Darwin programs once, inspect their architecture, Developer ID, Hardened Runtime, secure timestamps and empty entitlements, then regenerate all six program hashes. Upload this signed distribution for the Mac, Windows and Linux package jobs.
2. **Unified program notarization:** verify those signed program bytes, archive them once and submit that archive to Apple. Keep `notarization/astral-darwin.zip` and its receipt for a delayed submission. This job does not block Windows/Linux packaging.
3. **Native Mac package jobs:** on each matching native runner, sign its App and verify nested Electron helpers, signatures, entitlements and embedded program checksums. Produce and sign the outer DMG, submit it once, and wait for Accepted before stapling the DMG and App. Verify Gatekeeper and generate the final ZIP only after these changes.
4. Reopen the exact downloadable ZIP and DMG. Verify signatures, architecture, tickets and Gatekeeper. Run the native ZIP App through startup/shutdown, then exercise the final package's real updater replacement and retained data. The mounted DMG App still receives signature/architecture/Gatekeeper checks; its outer DMG carries the validated ticket.
5. Each job exports its source/version, team, certificate expiry, submission ID and final asset hashes. App reports also record the actual architecture and native startup result. A credential-free aggregation job verifies the transferred file hashes and combines `notarization/binaries.json`, `x64.json` and `arm64.json`.
6. Write `mac-signing-report.json` **schema 3**, with `native_tested_arches: ["x64", "arm64"]`, three distinct Accepted submissions, complete signature/package checks and final hashes for both Darwin programs and all four Mac packages. Generate and seal the Desktop feeds afterward, using those same final package bytes.

The aggregator needs no Apple account or certificate. Its report proves the checks performed by the native jobs and binds them to the transferred bytes; it does not claim it ran Mac executables on Linux. A package with a changed hash, missing native architecture, foreign team, expired certificate, unconfirmed upload or incomplete submission inventory fails validation. Historical schema 2 reports remain historical evidence and do not satisfy the new dual-native release requirement.

## Checking a delayed submission

The upload uses `notarytool submit --no-wait` once. A successful CLI exit and a valid UUID are both required before waiting. The sanitized receipt records `upload_status: "completed"`, `submit_exit_code: 0`, the input filename, SHA256 and source SHA. An upload failure that happens to return a UUID is recorded as unconfirmed and stops; a UUID alone does not establish that the input finished uploading.

After confirmed upload, the script makes bounded two-minute waits and status queries for that same UUID, up to 15 rounds and a 30-minute elapsed budget, with short backoff. Invalid/Rejected or a response identifying another submission stops immediately. In Progress, unavailable status or network failure can continue within the budget but never approve publication. Exhausting the budget fails the job and retains diagnostics and the submitted input; it does not upload the archive again.

The **Apple notarization status** workflow on main can inspect existing Apple records without building or submitting another program. Leave `submission_id` empty for Astral history, or provide a recorded UUID. It uses the protected Environment and temporary-keychain cleanup, and records only sanitized submission metadata.

For an explicit status-wait resume, restore the original input and its `notarization/UUID.json` receipt under `apps/desktop/release/` in a disposable Mac CI checkout of the recorded source. Configure the same protected signing/notary context and `RELEASE_SOURCE_SHA`, then run:

```sh
node scripts/mac-notarize.mjs resume-submission /path/to/original-input.dmg UUID
```

The command checks the confirmed receipt, source, filename and exact SHA256 before continuing wait/info for the existing ID. It never resubmits changed or rebuilt bytes. It only resumes the status wait; the original signed App/DMG still need the remaining stapling, native checks, final package generation and sealing before release. Restarting the entire packaging job is a new build, not an automatic restoration of this checkpoint.

Apple DTS notes that In Progress may involve processing or additional analysis; repeated submission of identical bytes does not accelerate it. Keep the receipt and inspect the existing submission before deciding whether a new build is needed. See [request/upload distinction](https://developer.apple.com/forums/thread/837038), [CI retry guidance](https://developer.apple.com/forums/thread/814841) and the [notary team's Q&A](https://developer.apple.com/news/?id=s8eigiyj).

## Certificate replacement

Before the current certificate expires, obtain another Developer ID Application certificate in the same team and rerun the P12 setup. Keep developer membership active to request replacements. Previously signed and notarized Apps with secure timestamps continue working after normal certificate expiration; future builds use the replacement certificate. See [Apple's expiration guidance](https://developer.apple.com/help/account/certificates/create-developer-id-certificates).

References: [Apple notarization workflow](https://developer.apple.com/documentation/security/customizing-the-notarization-workflow), [GitHub's temporary-keychain pattern](https://docs.github.com/en/actions/how-tos/deploy/deploy-to-third-party-platforms/sign-xcode-applications), [electron-builder v26 Mac configuration](https://www.electron.build/v26/docs/mac/).
