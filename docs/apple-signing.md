# Apple signing and notarization

The release workflow signs with the personal Developer ID Application team `6N6W6Z6739`. It never uses Apple Development or another team's identity. Ordinary pull-request builds remain unsigned and do not receive Apple credentials.

Pushing an existing version tag starts `.github/workflows/release.yml`. The selected commit must belong to `main`, both package versions must match the tag, and ordinary `main` CI must have succeeded for that exact commit. A manual run from `main` can select an existing tag. Leave the tag empty for a full-platform preview on `main`, including Mac signing/Apple submissions, Windows/Linux packages, real native updater acceptance and final inventory/feed validation; it uploads CI artifacts without creating a tag or Release. The workflow cannot create or move tags.

Tagged runs publish a Release after verified Mac Developer ID signing, confirmed Apple uploads, final native package checks and complete asset verification. Apple processing may still be pending; a rejection or invalid signature blocks publication. An empty-tag manual run exercises the full build/acceptance pipeline and retains a verified preview instead of publishing a Release. Windows and Linux packages are unsigned. Release builds include Desktop updating and the versioned-migration baseline Host 3 / Core 11; neither implementation nor fixture tests establishes that a public Release or an actual N→N+1 update has already passed.

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
2. Submit the two Darwin programs in a ZIP to Apple. The uploader must exit successfully and return a valid submission ID. Bare executables cannot carry stapled tickets; Apple records their notarization separately.
3. Package each Electron App with the independently configured release signing identity. The pre-signed Go programs are excluded from repeat signing. Electron receives only the JIT entitlement; Go receives none. Verify every enclosed Mach-O, architecture slice, identity, runtime, timestamp and embedded checksum before proceeding.
4. Build and sign each architecture's DMG from its prepackaged App, then submit the DMG once. An `Accepted` submission is stapled to the DMG and original App, validated and assessed with Gatekeeper. As in Magpie's release workflow, ticket download retries up to five times with increasing delays. A confirmed upload still `In Progress`, or temporarily unavailable after the bounded query, continues as notarization pending; `Invalid`/`Rejected` fails.
5. Generate the final ZIP from that signed App, including its stapled ticket when available. Keep the signed DMG bytes intact instead of rebuilding it after App stapling.
6. Extract the final ZIP and mount the final DMG read-only; recheck signatures and architecture for the enclosed Apps. Accepted submissions additionally require their applicable stapling and Gatekeeper checks. A pending submission never claims those checks passed. Run the runner-native ZIP App through startup, renderer, local-Daemon and shutdown acceptance. The other architecture is packaged and signature-checked, without claiming native execution.
7. Produce `mac-signing-report.json` **schema 2** with `status: "signed"`, `notarization_status: "accepted"` or `"pending"`, certificate expiration, three distinct submission IDs and exact hashes of the two Darwin programs and four Mac packages. Aggregate stapling/Gatekeeper flags are true only when all three submissions are accepted. Generate Desktop feeds from the final ZIP bytes afterward; their hashes must not describe an earlier, unstapled or differently signed package.

All Desktop platforms receive the same signed Darwin download programs. The collector rejects stale hashes, another team, missing proof or incomplete inventories. Public collection and publication require signed evidence and the explicit Goose migration contract. Review-only candidates retain `public_release_ready: false`. The manifest distinguishes `signed-and-notarized` from `signed-notarization-pending`; unsigned artifacts cannot use the pending state to bypass signing requirements.

Signing is mandatory; waiting indefinitely for Apple is not. A pending public Mac release must say so in its release notes. Gatekeeper may block the first launch even with a valid Developer ID signature. After trying to open the trusted official App, users can follow [Apple's Open Anyway instructions](https://support.apple.com/en-us/102445) under System Settings → Privacy & Security. Do not promise an uninterrupted first double-click or suggest disabling Gatekeeper globally.

## Checking a delayed submission

Use the **Apple notarization status** workflow on `main` to read existing Apple records without building or uploading another program. Leave `submission_id` empty to list Astral submissions, or supply an existing UUID to query it. It uses the same protected Environment and temporary-keychain cleanup as release signing, and records only public submission metadata. A CI wait timeout is not an Apple rejection; use the recorded Apple status before deciding whether to retry a build.

The release script uses native `notarytool submit --no-wait` and confirms that the uploader exits successfully and returns a valid UUID before calling `notarytool wait` with a two-minute timeout. Its sanitized diagnostic records `upload_status: "completed"` and `submit_exit_code: 0` only after that successful command. If the upload command fails but returns a UUID, the script retains it with `upload_status: "unconfirmed"` and a safe numeric exit code, or `null` if unavailable, then stops without waiting or querying. A UUID alone is not proof that the binary finished uploading.

If the subsequent wait fails or remains `In Progress`, the script queries that same UUID once. `Accepted` enables ticket/Gatekeeper verification; `In Progress` or an unavailable response after the confirmed upload records pending and permits signed packaging to continue. `Invalid`, `Rejected` and a response naming another UUID stop the job. It does not automatically resubmit the archive. The upload evidence does not identify Apple's internal processing stage; older records without that evidence cannot prove successful upload from their UUID or elapsed time.

A later Apple approval does not rewrite immutable published assets, checksums or their pending report. Use the status workflow to inspect that submission. Distributing newly stapled packages and corresponding update feeds requires a new patch release. An ended preview does not retain a signed archive for resuming Apple submission processing as a later build.

Apple DTS explains that `In Progress` can include incomplete uploads as well as processing or additional analysis. If a failed-upload request later disappears from history, retrying is appropriate. Repeatedly submitting an identical build while additional analysis is pending does not help; an updated build can be submitted while an earlier request remains pending. See [request/upload distinction](https://developer.apple.com/forums/thread/837038), [CI retry guidance](https://developer.apple.com/forums/thread/814841) and the [notary team's Q&A](https://developer.apple.com/news/?id=s8eigiyj).

## Certificate replacement

Before the current certificate expires, obtain another Developer ID Application certificate in the same team and rerun the P12 setup. Keep developer membership active to request replacements. Previously signed and notarized Apps with secure timestamps continue working after normal certificate expiration; future builds use the replacement certificate. See [Apple's expiration guidance](https://developer.apple.com/help/account/certificates/create-developer-id-certificates).

References: [Apple notarization workflow](https://developer.apple.com/documentation/security/customizing-the-notarization-workflow), [GitHub's temporary-keychain pattern](https://docs.github.com/en/actions/how-tos/deploy/deploy-to-third-party-platforms/sign-xcode-applications), [electron-builder v26 Mac configuration](https://www.electron.build/v26/docs/mac/).
