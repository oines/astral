# Verification record

Verification date: **2026-10-21**, Asia/Singapore. The acceptance baseline is [SPEC.md §16](../SPEC.md#16-验收矩阵). This record distinguishes executable checks, actual browser/OS behavior, and checks that still need an external account or an unlocked desktop.

## Reproducible checks

```sh
pnpm install --frozen-lockfile
GOWORK=off go test -race ./...
GOWORK=off go vet ./...
pnpm lint:go
pnpm lint
pnpm typecheck
pnpm test
pnpm build:distribution
pnpm test:desktop-service
pnpm test:integration
pnpm --filter @astral/desktop package
```

The frontend suite covers event/commit reconciliation, discarded failed-attempt previews, stale callbacks after route changes, permanent EventSource failures, silent-run ordering and the desktop navigation/IPC boundary. The Go suite uses actual SQLite stores, HTTP/SSE adapters, authenticated WebSocket Daemons, shell processes and file streams. The Telegram and search servers in automated tests are deterministic local protocol fixtures.

The tagged platform integration tests require the distribution binaries and a running local Docker runtime. They download and execute the actual installer, connect a host Daemon, create Computers through both container entry points, perform real Linux command/file operations, check that retries reuse one container, and verify that registrations do not modify Agent allowlists. Test containers and volumes are removed by cleanup hooks.

`check-desktop-service.mjs` launches the real Go binary through the desktop service class, verifies private Owner handoff, graceful shutdown, shutdown after a killed parent process, release of the data lock, and preservation of Local Computer identity on restart.

## Browser and runtime evidence

The browser checks used isolated test data and the repository's loopback model fixture. Model output came through the real model adapter and Core. Commands and files used actual Daemons.

| Check | Observed result |
| --- | --- |
| Provider/Profile/Agent setup | Completed in the shared Web UI; catalog recommendations loaded, explicit overrides retained, tool call/replay verification succeeded |
| Non-streaming model | Linux Web setup selected streaming Off; a tool call executed and its result produced a file link |
| Local and container execution | Real Daemons wrote `validation.md`; the file was read through the source-bound file API and rendered as Markdown |
| Container provisioning | OrbStack created and connected a Linux Computer; the existing Mac Computer remained separate; Agent assignment was explicit |
| Provisioning retry | Repeating the same intent returned the same operation and Computer; only one matching container existed |
| Source binding | After the Session switched to the container, a previous file link still read the original Mac Computer |
| Image history and replay | A small PNG was uploaded, delivered as a native data image to the provider, and frozen in the rollout. Its original was made unavailable; reloaded history and a Fork retained the exact image payload |
| Stop and queued input | Stop reached a cancelled terminal state. The independently accepted follow-up remained Waiting until explicit Continue, then applied once |
| Failed stream/retry | The fixture emitted `UNCOMMITTED_GHOST` and broke the stream. The retry committed one reply; the failed text disappeared from the transcript |
| Service restart | The page showed Reconnecting while the server was down, recovered its subscription without a reload, and retained history without the failed-attempt text |
| Silent end | `no_reply` completed with an “Ended without a reply” activity summary and no empty assistant bubble or continuation |
| Compaction | With sufficient recent context, the request estimate changed from 32,789 to 23,884 while earlier conversation entries and original image history remained accessible |
| Appearance and narrow layout | English/Chinese and dark/light state were persisted; a narrow viewport retained the composer and independent Stop control |
| Linux first use | The built Linux ARM64 server served the embedded UI. Provider, Profile and Agent were configured in the browser, and its Local Linux Computer executed the file-producing command |

![Linux server, actual command result and source file preview](verification/linux-file.png)

![Chinese dark settings on a narrow viewport](verification/settings-dark-zh.png)

## Acceptance matrix

The references below identify the implementation checks for each SPEC scenario. “Fixture” means the external service is locally simulated; it does not mean a live Telegram or search account was exercised.

| ID | Evidence |
| --- | --- |
| A01 | Packaged Electron startup plus `check-desktop-service.mjs`; shared browser setup/execution passed. Native window/menu/tray interactions require an unlocked macOS session; see limits below |
| A02 | Linux ARM64 binary ran inside a real Linux container, served the embedded shared UI, and completed first-use setup and file execution in the browser |
| A03 | Versioned global resources; `TestVersionedConfigurationAndCredentialReopen`; browser Agent setup selected global resources |
| A04 | `TestEnrollmentRetryAndAgentAllowlistRemainExplicit` and real container integration assert no implicit allowlist expansion |
| A05 | `TestLocalComputerReuseAndRemoteEnrollmentUseRealDaemonConnections`, `TestDistributionInstallerRegistersActualDaemon`, `TestContainerShortcutAndInstallerUseIndependentRealComputers` |
| A06 | New-session default selection in browser; `TestContextStatsStayOnFrozenRequestUntilRunFinishes`; Core model-selection/options integration tests |
| A07 | `TestPassiveObservationAndAtomicTriggerDoNotDuplicateOrInventActivation` rejects an active Owner Computer switch; running browser composer disables it |
| A08 | `TestSessionKeepsProcessOnOriginalComputerAndRetiresHandlesAcrossRestart`; browser switched an existing Session to the container |
| A09 | Same process-handle test, `TestProcessOriginalComputerAndBootCannotBeRebound`, and browser reading the Mac file after the switch |
| A10 | `TestTwoComputerFilesystemAndGatewayBindings`; stable per-Agent/Computer homes; browser Fork identifies shared Computer files |
| A11 | Real shell tests and browser file creation execute without per-command approval; unresolved outcomes have explicit Core records and UI |
| A12 | `TestTwoComputerFilesystemAndGatewayBindings` checks the injected gateway URL and rejects forged identity |
| A13 | `TestConcurrentLocalGatewaysKeepAgentSessionAndSourceIdentity` exercises simultaneous bound gateways |
| A14 | Channel binding uniqueness and distinct conversation/topic keys in `internal/channels/channels_test.go` |
| A15 | `TestTelegramImagesObservationsAndCommittedReplySurviveRecovery` covers ordinary group observation, mention activation and self-echo handling |
| A16 | That Telegram test and `TestPassiveObservationAndAtomicTriggerDoNotDuplicateOrInventActivation` assert no request for passive context |
| A17 | Both tests assert the trigger body appears once, with its associated observation and earlier context |
| A18 | Durable delivery reconciliation plus Core context-injection tests for pending/application and cold reopen |
| A19 | `TestImageCarriesOriginalAttachmentURIAndNativeImage` and message rendering tests cover normalized source, escaped body and native attachment |
| A20 | Telegram edit/retry/echo checks and `TestObservationIdentityKeepsEditsAndIsScopedToConnection`; unsupported deletion events are declared unsupported |
| A21 | Harness Submit uses a driving Web input, including an existing IM Session; the browser input path retains source binding |
| A22 | Telegram fixture asserts only the committed text is sent, excluding reasoning and tool output; failed-attempt projection tests exclude preview text |
| A23 | `TestOutboundUnknownIdempotencyRebindingAndRestart` and Telegram self-echo checks |
| A24 | Browser silent-end check; Core `TestToolEndRunPreservesInputsAndSkipsResultHooks`; channel fixture sends no placeholder |
| A25 | Harness atomic-trigger/independent-input test and Core `TestToolEndRunPreservesHostProcess` |
| A26 | Core `TestCompletedToolControlSurvivesColdStoreReopen`, `TestToolControlForksAndCompaction`, and provider replay tests |
| A27 | Core `TestControlBatchValidationPrecedesEveryToolEffect` rejects mixed control batches before any tool effect |
| A28 | Browser Markdown file previews on Mac/container/Linux; `TestOwnerAuthAndIndependentHistoricalImageAndOriginalDownload` |
| A29 | `TestFileSendRetainsSourceAndTextFragmentsStayOrdered` streams the actual source file into the Telegram fixture and records the receipt |
| A30 | `TestSessionImagesRemainSelfContainedAcrossDeletionReopenAndFork`, `TestRolloutImagesSurviveSourceDeletionReopenAndFork`, Telegram image recovery test, and actual browser upload/deletion/Fork check |
| A31 | `TestFileReferencesSnapshotRangeAndSourceFailure`, HTTP historical-image/original-download test, transport cancellation tests |
| A32 | `TestVerificationRevisionAndImmutableCredentials`, `TestCatalogRefreshRetainsOverridesAndNativeEfforts`; browser verification and Linux non-streaming execution |
| A33 | `TestExactlyOneSelectedProviderAndNoFallbackOnFailure` |
| A34 | Actual browser tool-argument preparation and completed Activity Line; `session-state.test.ts` distinguishes preparation from execution |
| A35 | `TestStopPreservesQueuedInputUntilExplicitContinue` and browser Stop/Waiting/Continue sequence |
| A36 | Composer displays actual approval/unresolved records; Core authorization and unknown-effect tests; normal shell execution remains direct |
| A37 | `TestEventSubscriptionPrecedesSnapshotAndReconcilesCommit`, frontend reducer/connection tests, actual server-restart browser check |
| A38 | Process-handle cold-restart test, outbound unknown/restart test and desktop parent-death check |
| A39 | Core Fork/compaction suites; browser Fork and actual compaction preserve original history, with no copied IM binding |
| A40 | `TestOutboundUnknownIdempotencyRebindingAndRestart`; Conversation lifecycle retains previous Sessions and destinations |
| A41 | Frozen-request context test, immutable credential/profile tests, current allowlist validation, source-pinned process tests |
| A42 | Shared Vue application, generated SDK, frontend lint/typecheck/tests, four-binary build and Electron package build |
| A43 | Telegram group/private/Topic schemas, validation, polling and send routes have local protocol-fixture coverage; no live bot account was used |
| A44 | `TestDistributionInstallerRegistersActualDaemon` downloads the installer and waits for a real registered connection |
| A45 | `TestContainerShortcutAndInstallerUseIndependentRealComputers` also runs the remote-container installer |
| A46 | That test retains separate local, shortcut-container and installer-container Computer identities; browser retained Mac and container |
| A47 | `TestContainerFailureIsDurableAndRequestRetryDoesNotDuplicate`; successful integration checks explicit assignment |
| A48 | Local shortcut test checks repeated use and Server restart reuse; browser “Use this machine” returned the existing Mac |
| A49 | Real local-container integration plus unavailable-runtime failure test and same-intent retry |
| A50 | Bootstrap host assertion and Linux Web first-use show the Server's Linux Computer instead of the browser's Mac |
| A51 | Generic Connection/Conversation/Binding/Adapter interfaces, normalized observations and generic outbox; only Telegram implementation is offered |
| A52 | Adapter capability fields and unsupported-operation errors; webhook/privacy diagnostics tested without inventing platform events |

Core test names refer to the pinned [astral-core](https://github.com/oines/astral-core) dependency. Application test files are under `internal/`; browser reducer tests are under `apps/web/src/`. A successful local workflow is not a claim that hosted GitHub Actions has run.

## External and manual limits

- Live `deepseek-flash` checks were run on 2026-10-20 against both Chat Completions and Responses in `astral-models`: tool call/result replay, durable reopen, ordinary continuation, `no_reply` and a later activation passed. These checks did not establish every provider/model capability or live image compatibility.
- Telegram and Tavily/Exa checks here use local protocol fixtures. A live bot's permissions, group visibility, account limits and network behavior must be checked with that deployment's credentials.
- The macOS session was locked during native UI validation. Application/service startup and lifecycle were exercised, but native menu, tray, close-window preference, login-start and file-picker interaction are not marked manually verified.
- Generated macOS packages are unsigned and unnotarized. ARM64 is the local execution architecture; Intel artifacts are cross-packaged.
- Remote enrollment was exercised through real downloaded installers and authenticated connections on local test hosts/containers. No claim is made about a user's external DNS, reverse proxy, VPN or NAT configuration.
