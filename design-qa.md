# Astral UI/UX rebuild — design QA

Date: 2026-10-21
Final result: passed

This result covers the implemented UI, the browser flows listed below, and the native macOS settings-shortcut round trip. Residual platform checks are explicitly listed under validation limits.

## Comparison targets

The visual source is the actual local Memoh frontend, captured with synthetic reference data during the preceding page-by-page audit. The target is its compact settings structure, readable control hierarchy, progressive disclosure, and continuity between chat and settings. Astral keeps its own product model and the user's explicit requirements: no transcript banner and no gray containers around execution details.

Source visual truth directory:
`/Users/oines/.codex/visualizations/2026/10/20/01a15828-c320-7ef1-bdde-4d9bd6f6e731/astral-memoh-audit/`

Implementation screenshot directory:
`/Users/oines/.codex/visualizations/2026/10/20/01a15828-c320-7ef1-bdde-4d9bd6f6e731/astral-ui-rebuild/`

Primary implementation: `http://127.0.0.1:4546/`, served by the newly packaged desktop application's embedded Server. Development comparisons used `http://127.0.0.1:5173/` against the same isolated test workspace. Memoh's reference used `http://127.0.0.1:8082/`.

| Evidence | Source | Implementation |
| --- | --- | --- |
| Provider configuration | memoh-06-provider-detail.png | 06-provider-detail.png; 34-bundled-provider.png |
| Chat entry | memoh-02-chat-empty.png | 30-home.png; 31-bundled-home.png |
| Agent configuration | memoh-14-agent-general.png | 11-agent-identity.png |
| Appearance | memoh-08-appearance.png | 04-appearance.png; 26-dark-appearance.png |
| Navigation continuity | memoh-16-chat-draft-return.png | 01-draft-preserved.png; 32-bundled-draft-preserved.png |

The desktop source and implementation images are both 1280 × 720 pixels for a 1280 × 720 CSS viewport, normalized by the capture API to one image pixel per CSS pixel. Full-view comparison boards reduce both inputs equally to 640 × 360; focused provider comparisons retain 720-pixel content width without horizontal scaling. Source and implementation have different synthetic resource names and business fields, so exact text-position pixel matching is not claimed.

Additional responsive evidence uses an actual 390 × 640 CSS iframe viewport. The surrounding browser capture was cropped from (24, 24) to (414, 664), without scaling, into 24-mobile-home-viewport.png and 25-mobile-settings-viewport.png. These captures establish layout behavior at that viewport, not full mobile workflow coverage.

Native macOS evidence is recorded in 38-native-draft.png and 39-native-provider.png at 2480 × 1714 pixels for a 1240 × 857 window at 2× density, including the native title bar. desktop-preview.png is a uniform 2:1 downsample of the latter. These are additional application checks, not pixel comparisons against the differently sized browser reference.

## Combined visual comparison

All following combined inputs were opened and inspected together:

- provider-comparison-full.png — previous Astral, Memoh, rebuilt Astral at the same desktop viewport.
- provider-comparison-focus.png — readable connection fields and form actions from Memoh and rebuilt Astral.
- home-comparison-full.png — previous Astral, Memoh, rebuilt Astral.
- agent-comparison-full.png — previous Astral, Memoh, rebuilt Astral.

The rebuilt provider page now contains the connection form, save action, and the first model within the 720-pixel viewport. Its extra provider-family/protocol distinction is required by Astral's adapter model. The homepage now starts with a real composer. Agent settings group identity, model/search, Computer permissions, and channel conversations instead of presenting one long form.

## Findings and iteration history

| Priority | Earlier finding | Implemented fix | Post-fix evidence |
| --- | --- | --- | --- |
| P1 | Settings navigation destroyed the active chat and unsent draft. | Keep the chat mounted, retain the last chat route, store drafts by session, and keep settings panels alive. | 01-draft-preserved.png; 28-form-state-dark-en.png; 32-bundled-draft-preserved.png |
| P1 | The homepage required selecting an Agent and creating an empty session before typing. | Use the same composer on the homepage; create the session on first send/upload and retain the request identity across retries. | 02-first-message.png; 30-home.png; 33-bundled-first-message.png |
| P1 | Provider/model relationships were split across independent lists. | Show providers first and place each provider's models inside its detail view. | 05-providers.png; 06-provider-detail.png; 34-bundled-provider.png |
| P2 | Large titles, full-width fields and excessive spacing pushed key actions below the fold. | Use a 720-pixel settings content width, 18-pixel page title, 60-pixel standard rows, 36-pixel controls and 16-pixel group insets. | provider-comparison-full.png; provider-comparison-focus.png |
| P2 | The model editor exposed secondary capabilities, pricing and JSON fields immediately; an intermediate 560-pixel dialog cramped its main fields. | Use a 640-pixel modal, compact rows, visible connection testing, and an advanced disclosure. | 07-model-editor.png; 29-model-verification.png |
| P2 | Agent identity, defaults, permissions and channel bindings competed in one scrolling form. | Use a stable object heading and three focused tabs; keep dependent configuration links beside the fields. | 11-agent-identity.png; 12-agent-computers.png; 13-agent-channels.png |
| P2 | Empty search/channel pages did not explain the next action; outbox diagnostics appeared without a connection. | Add search-service choices and purposeful empty states; reveal channel diagnostics after a connection exists. | 08-search-services.png; 09-search-setup.png; 17-channels-empty.png; 18-channel-setup.png |
| P2 | File preview squeezed the composer; technical media types were used as labels. | Clamp side-panel width, switch to overlay at smaller widths, move environment choices outside the composer, and show a readable file-format label. | 23-file-preview.png; 24-mobile-home-viewport.png |
| P2 | Execution summaries did not say what happened, and prior details were presented as boxed inspector content. | Use semantic action summaries and independently expandable inline tool details on a transparent surface. | 22-inline-activity.png |
| P2 | An intermediate settings cache implementation failed while switching categories. | Use stable component identities and explicit cache keys. | Resource-category navigation, form-state preservation and final browser console checks passed. |
| P2 | Browser Back changed the settings route but left the model editor over the destination page. | Cache the two main surfaces with KeepAlive; deactivate teleported dialogs and menus with their parent. Model form values remain available on return. | 35-overlay-before.png; 36-overlay-after.png; 37-model-draft-restored.png; overlay-comparison.png |

The later comparisons found no remaining actionable P0/P1/P2 visual differences within the stated target and tested states. Native menu navigation was additionally changed from full page loading to an IPC notification followed by client-side routing, removing another source of draft loss. The final physical Cmd+, round trip passed and preserved the native composer draft.

## Required fidelity surfaces

- **Fonts and typography:** Inter Variable for Latin UI text with native CJK fallback; restrained settings titles, 14-pixel labels and 12–13-pixel supporting text. The focused comparison confirms matching control scale and alignment. Astral's CJK fallback differs from Memoh's MiSans; this is an accepted product difference. English dark-mode form text remains readable without overlapping its controls.
- **Spacing and rhythm:** Shared row, group, input, modal, navigation and composer dimensions replace per-page spacing. Standard settings content is 720 pixels; controls align to a 320-pixel column. The provider form and its model list fit together above the fold. Mobile forms stack through responsive rules.
- **Colors and tokens:** Neutral light and dark backgrounds, restrained borders and small semantic status accents. Light and dark provider/editor states were inspected. Disabled controls have a distinct state and focus outlines remain present. This is not a formal WCAG contrast certification.
- **Image quality and icons:** Existing Lucide icons remain vectors from the shared icon package. No generated illustrations or substitutes for Memoh's branding were introduced. Astral's wordmark and user-derived Agent initials are intentional product identity. The saved image attachment and Markdown file preview render through the actual file path.
- **Copy and content:** The setup states explain the next user action; technical fields move into disclosures where appropriate. Model-list status says “已验证”; tools summarize actual commands from stored events. Test conversation text and reference resource names are controlled fixtures, not shipped product copy.

## Page coverage

| Surface | Recorded final checks |
| --- | --- |
| General | Shared switches and select; browser-only limitation communicated |
| Appearance | Light/dark themes, Chinese/English, saved feedback |
| Models | Provider list, provider detail, model modal, advanced disclosure, saved connection, real local tool-calling verification |
| Agents | Identity, model/search defaults, Computer permissions, channel conversation tab, targeted navigation from a Computer |
| Computers | Online-first list, permissions count, detail, add-Computer choices |
| Web search | Service catalog, setup form, advanced fields |
| Channels | Empty state, setup form; connected diagnostics remain implemented |
| Storage | Usage, directory disclosure, active/archived conversation management |
| Security | Access status, server address, technical disclosure, sign-out affordance |
| About/diagnostics | Versions, system information and developer resources |
| Chat | Direct first send, streaming completion, draft preservation, inline tool details and file preview |

## Functional and build validation

- `pnpm typecheck` passed for web and desktop after the final code changes.
- `pnpm lint` passed after the final code changes.
- `pnpm test`: 3 files and 9 tests passed.
- `pnpm build:distribution` passed for macOS/Linux on arm64/amd64.
- `pnpm --filter @astral/desktop package` generated Apple Silicon and Intel DMG/ZIP artifacts.
- `pnpm test:desktop-service` passed startup, private Owner handoff, graceful stop, parent-crash cleanup and identity-preserving restart.
- `git diff --check` passed.
- The newly packaged Apple Silicon application launched and exposed its actual homepage through the native accessibility tree.
- Native Cmd+, opened General settings; returning to chat preserved the exact unsent draft. Native provider-detail controls and the resulting screenshot were inspected after the final package rebuild.
- Browser checks against the packaged Server verified first-send creation, streaming completion, preserved drafts through settings navigation, and provider detail rendering. The final bundled-page console returned no warning/error entries.
- Saved provider edits remained on the detail page. A draft provider name survived switching settings categories and changing theme/language.
- A Computer's Agent link selected the intended Agent and its Computer-permissions tab.
- Browser Back/Forward hid the model dialog on the destination page, restored its unsaved form on return, and left the chat draft intact. Chat model popovers also closed when moving to settings. The post-fix console had no error entries.
- The local tool-calling verification completed both model requests and displayed the new verification timestamp.

Build output retains existing large-chunk warnings for the editor/renderer dependency graph and reports the intentional static/dynamic page import overlap; neither failed the build.

## Intentional differences and validation limits

- This is an Astral implementation informed by Memoh, not a feature-for-feature Memoh clone. Bot sub-navigation, marketplace and unrelated Memoh features are outside scope.
- Execution details deliberately omit Memoh's gray outer cards in accordance with the user's request.
- The source screenshots use a synthetic Memoh API fixture. No claim is made about Memoh backend behavior or performance.
- Astral flows used an isolated local workspace and local model fixture. No external model/search/Telegram account was contacted during this UI pass. Connected Telegram delivery, search credentials, new remote enrollment and container provisioning were not re-exercised end to end.
- An intermediate native capture timed out; after the final rebuild and relaunch, native interaction, Cmd+, and screenshots worked. Login-at-start was not revalidated; this local unsigned build logged an OS permission refusal while setting the login item.
- Intel and Linux artifacts were built, but this machine only ran the Apple Silicon desktop application.
- Complete phone interaction coverage, screen-reader traversal, 200% text zoom and formal contrast auditing remain outside this pass. The 390 × 640 captures cover home/settings layout.

## Implementation checklist

- [x] Rebuild all ten settings categories and their principal resource/detail forms.
- [x] Replace the homepage's empty-session creation step with the shared composer.
- [x] Preserve chat route, unsent text, attached-file references and settings drafts during navigation.
- [x] Keep execution details inline and unboxed.
- [x] Compare actual source and implementation together, including readable focused regions.
- [x] Validate light/dark themes, bilingual content, narrow layout and the packaged browser build.
- [x] Generate updated macOS installation artifacts and record residual platform test gaps.

final result: passed

## Provider and model follow-up — 2026-10-21

This follow-up supersedes the earlier provider-family/protocol presentation and the 640-pixel model editor. The user requested Memoh's provider/model flow, its LLM presets, and the public APIs of additional coding plans, including Command Code and MiMo.

The actual Memoh model edit and add dialogs were opened in the reference build. Its compact form contains type, model ID, optional display name and description, capability checkboxes, and context window. Enabling the reasoning checkbox does not reveal extra controls. Its model rows expose enable, test, edit and delete actions. Memoh's 25 LLM presets are represented in Astral's 35-preset catalog; the additional presets are Command Code, OpenCode Console, OpenCode Go, MiMo API, MiMo Token Plan, MiniMax Token Plan, Z.AI Coding Plan, GLM Coding Plan, Bailian Coding Plan and Kimi Code.

Astral now uses a 428-pixel model dialog with ID, optional name/description, Vision/Tools/Reasoning and context window. Protocol, token/output details and JSON controls are in a closed advanced disclosure. The unsupported PDF and image-generation capabilities are not presented, and there is no redundant single-option Chat type selector. The 428-pixel provider add dialog uses a searchable brand picker and prefilled name/URL; choosing a region updates the endpoint. Provider detail owns its model preview/list, search, pagination, import and individual model actions. Brand vectors come from LobeHub Icons, with notices included in the distribution.

Reference evidence is in `/Users/oines/.codex/visualizations/2026/10/20/01a15828-c320-7ef1-bdde-4d9bd6f6e731/memoh-provider-review/`: `06-model-editor-basic.png`, `07-model-reasoning-enabled.png` and `08-add-model.png`. Follow-up implementation evidence is in the adjacent `astral-provider-rebuild/` directory:

- `01-model-editor.png`: saved description, capability states, compact fields and collapsed advanced controls.
- `02-provider-catalog.png`: provider cards with real vector brands.
- `03-mimo-token-plan.png`: prefilled MiMo Token Plan connection.
- `04-provider-picker.png`: searchable provider selection.
- `05-command-code-add.png`: Command Code automatically fills name and public endpoint.
- `06-dark-provider-catalog.png`, `07-dark-model-editor.png`: English text, brand contrast and field layout in dark mode.

The Memoh and Astral model-edit captures were viewed together at the same 1280 × 720 viewport, without rescaling or retouching. Modal width, control heights, field rhythm, copy hierarchy, capability checkboxes and footer actions were inspected. The desktop settings layout remains shared with the preceding pass. Differences in product capabilities, CJK font fallback and test model names are intentional.

The follow-up UI checks used a separate local data directory and model fixture. Creating a local provider imported its actual model list. Two model verification requests passed; editing its description, disabling/re-enabling the model and refreshing the catalog preserved the saved data and verification. Command Code's 96-model preview paginated correctly; searching reset pagination, and empty results have model-specific copy. The MiMo regional selector changed the endpoint to Singapore. Entering a detail view from a lower gallery card now resets scrolling to the top. No external account login or paid provider generation was performed.

Backend validation covers all importable built-in profiles, native per-model routing, non-destructive catalog import, enable switches on resolved models, OAuth device polling/expiry/token refresh, redirect and host boundaries, and metadata-only verification preservation. All three Go modules' full tests passed. Race checks passed for modelbridge/httpapi/harness, native SDK protocol/recovery tests, and Core's recovery integration. Frontend lint, web/desktop type checks and the 9 frontend tests passed.

Encrypted reasoning recovery is conditional on an explicit provider rejection, permits one extra request, preserves text/tool calls/results, and does not rewrite durable history or repeat tool execution. JSON and SSE fixtures exercise Anthropic and Google native replay alongside Responses. This is protocol fixture evidence, not a claim that every preset has been tested with a live subscription.

An additional compatibility regression confirms that renaming and disable/re-enable preserve encrypted replay from existing providers with no explicit replay revision. Connection, credential and account changes still isolate prior opaque state. The server owns this replay revision; a client-supplied value cannot override it. The fixture serializes the original encrypted reply through the new profile reference, so it exercises actual replay validation rather than only checking a stored flag.

The local cross-module build uses the active Go workspace so the desktop binaries contain the current Core and model SDK changes. A future standalone source release must first publish and pin those library changes; no library publication is claimed by this UI/build pass.

Both DMG and ZIP packages were rebuilt for Apple Silicon and Intel. The packaging run reused Electron 42.11.17 archives after verifying both cached archives against the official SHA-256 list. Packaged native binaries match the distribution binaries byte-for-byte. The desktop service startup, private Owner handoff, graceful shutdown, parent-crash cleanup and identity-preserving restart checks passed.

The new Apple Silicon application started with the isolated provider-review data directory. Its embedded production page (`08-packaged-model-editor.png`) shows version 0.1.0 and all 35 provider cards. The packaged UI passed the local model's real two-request verification, provider disable/re-enable, saved-description retrieval and model-specific empty search text. Its browser console contained no warnings or errors. All three modules also passed `go vet ./...`.

The Mac was locked during the follow-up native-window check. The application and its embedded production interface were verified, but native clicks and Cmd+, were not repeated for this build. The earlier native checks above apply to the prior UI pass. No screenshot from that earlier build is presented as evidence for this follow-up. Intel is build-verified only; the package remains locally unsigned as configured.


## Composer and transcript follow-up — 2026-10-21

The requested scope is a single-line composer that grows with its contents, plus transcript spacing and structure informed by Memoh. The reference was the running Memoh chat UI and its `chat-pane.vue`, `message-item.vue`, process grouping and Markdown styles. The comparison used matching Chinese text and Markdown at 1280 × 720, with real application components backed by isolated local fixtures. The Memoh source was not changed. Its existing two-row composer is intentionally replaced with a single input row in Astral, as requested.

The input form is now 50 pixels high when empty. Three explicit lines produce a 98-pixel form. The textarea stops growing at 200 pixels and scrolls internally; a 20-line input measured 488 pixels of scrollable content. Shift+Enter inserts a newline, Enter sends, sending restores the one-line height, and stopping a live local request returned the conversation to its cancelled state. Model, Computer, Agent and context controls sit in a smaller metadata row. Opening the actual file pane reduced the chat to 488 pixels; the metadata stayed on one row after label widths were adjusted with a container query, and there was no horizontal overflow.

The transcript now has explicit user and assistant turn groups. An assistant group contains its process, response and actions, including live previews associated with that run. A single tool call exposes its command and output in one click; multiple process items retain the aggregate disclosure. Finished text-only responses do not add an empty “worked” row. Errors, cancellation and silent completion remain visible. Copy was added beside the existing fork/details controls; copying the fixture reply returned the complete 322-character Markdown text.

The first comparison found that a descendant selector prevented the intended Markdown variables from reaching ordinary replies, and the file-pane check found an unnecessary second metadata row. Both were corrected and captured again. Paragraphs, lists, headings, quotations, tables and code blocks now use their actual `node-slot` boundaries, preventing compounded element margins. Actions are in normal flow beneath the answer.

| Fidelity surface | Result |
| --- | --- |
| Fonts and typography | Astral's existing Inter/system CJK stack is preserved. Body text is 15px with 1.65 line height; headings use restrained relative sizes. The hierarchy follows the reference without replacing the app's typefaces. |
| Spacing and layout | The content and composer share a 760px inner column. Turn gaps are 24px, ordinary Markdown block gaps 14px, and headings separate from the preceding block by 22px and following block by 8px. Single-line, multiline, scrolling, expanded tool and narrowed chat states were inspected. |
| Colors and tokens | Existing light/dark tokens are used for the user bubble, input, metadata and table surfaces. The neutral Astral palette is an intentional difference from Memoh's purple user bubble. Dark text, code and tables were checked in the running app. |
| Images and assets | This change adds no raster imagery. Controls use existing Lucide vectors, with no generated or approximated assets. The file preview displayed a real temporary Markdown file returned by Computer execution. |
| Copy and content | Existing actions and Chinese/English translations are retained. Copy feedback and the Markdown overflow-menu translation are available. Fixture content is clearly test content and is not a claim about a paid model's behavior. |

Evidence is saved in `/Users/oines/.codex/visualizations/2026/10/20/01a15828-c320-7ef1-bdde-4d9bd6f6e731/chat-polish/`:

1. `01-memoh-transcript.png` — initial reference structure; `06-memoh-matched-content.png` — matching long reference content.
2. `02-astral-composer-before.png`, `03-astral-transcript-before.png` — the oversized input and compounded spacing before this change.
3. `07-astral-transcript.png` — corrected typography and turn layout; viewed together with the matching Memoh capture in the same comparison input.
4. `05-multiline-composer.png`, `10-long-input-limit.png` — natural growth and internal scrolling. The earlier first-pass typography in these captures is superseded by `07`.
5. `08-tool-expanded.png`, `09-markdown-and-actions.png` — single-click command detail and the response's table, code and actions.
6. `11-narrow-chat-with-file.png` — the 488px chat pane with the actual file preview open and one metadata row.
7. `12-dark-transcript.png`, `13-dark-markdown.png` — dark-theme conversation and structured content.

The paired full-viewport images are legible at their native resolution; typography and padding were additionally checked through the rendered DOM's measured dimensions. No screenshot was stretched, retouched or substituted for the actual UI. App chrome and the requested one-line input are intentional differences from the reference.

Frontend lint, both web/desktop type checks and all 10 frontend tests passed. The new grouping test checks commentary/final-response grouping, intervening user input, silent turns and an active empty turn. Real UI checks covered keyboard input, resize, stop, copy, tool expansion, the relocated model menu, temporary-file preview and both themes. The Mac remains locked, so native-window input could not be repeated. Mobile viewport testing is not claimed; the narrow check used the actual desktop file pane. Distribution and packaged-page verification follows below.

Both macOS DMGs and ZIPs were rebuilt from the current web assets and native distribution. The Apple Silicon app was restarted against the isolated test data, and its embedded production page at `http://127.0.0.1:4548/sessions/session_72ecb8d3308b348d923ed7c9d3bd9491` was opened directly. `14-packaged-transcript.png` shows the final interface; measured values are a 50px empty form, 32px textarea, 15px body and 18px h2. `15-packaged-multiline.png` confirms a three-line input produces a 98px form. The draft was then cleared. The production browser console contained no warnings or errors.

Packaged binaries matched the built distribution byte-for-byte for both macOS architectures. Both DMGs passed `hdiutil verify`, and their SHA-256 manifest was refreshed. Desktop service startup, private Owner handoff, graceful stop, parent-crash cleanup and identity-preserving restart passed. The new native-window check remained blocked by the locked Mac; only the embedded page and service are claimed as runtime-verified for this package. Intel was build-verified.

final result: passed for the requested composer/transcript changes and embedded production UI; native-window input verification remains limited by the locked Mac.

## Transcript, composer and sidebar refinement — 2026-10-21

This pass supersedes the preceding transcript follow-up. The user requested that process disclosure follow Memoh's actual hierarchy and order, remove information absent from that UI, and improve the transcript, composer and sidebar using `Caho1/open-claude-ui` as a material and interaction reference.

Sources were inspected in their running interfaces before implementation: the existing Memoh checkout at `/Users/oines/project/memoh`, and a shallow clone of `https://github.com/Caho1/open-claude-ui` at `/tmp/astral-open-claude-ui`. The latter's `docs/claude-components-storybook.html` was served locally and its chat, composer and 36px sidebar example captured. It is a community reference; this review does not independently verify the repository's claims about official Claude internals. Astral's identity, Inter/system CJK font stack and existing functional architecture are retained. No proprietary fonts or brand assets were copied.

### Findings and repairs

- **P1 — Process order:** the preceding implementation collected all reasoning before all tools and put the entire process ahead of all assistant text. The new transcript follows committed message content and execution order. Consecutive process items share a group; intervening text ends that group. Tool lifecycle records collapse into a single invocation. Call IDs are scoped to their originating assistant message, so reused IDs cannot merge separate executions. Failed model responses and encrypted, textless reasoning do not become visible process items.
- **P2 — Disclosure clutter:** removed raw entry JSON, raw arguments, all-events disclosures, routine tool icons, successful exit codes and per-message detail buttons. A single process item is a bare row. Multiple items open into one quiet surface, with the actual command/result inside. User messages have one copy action; assistant turns have one copy action and a More menu containing the existing fork action. Cancellation remains a plain status; useful failure indicators remain.
- **P2 — Sidebar density:** removed the repeated conversation icon and Agent subtitle from every ordinary row. Rows are 36px, titles truncate consistently, selection uses a restrained fill and medium weight, and running/pinned/attention markers remain when meaningful. Agent identity remains available in the row tooltip and existing session controls. Search gains a surface only on hover/focus.
- **P2 — Composer material:** introduced restrained warm-neutral surfaces, a low-opacity border and shallow shadow, with modest hover/focus changes. The form remains 50px high when empty, grows to 98px for three lines, and preserves the existing maximum textarea height and metadata controls. The send/add buttons use the same 32px scale and 10px radius.
- **P2 found during visual iteration — Text inheritance:** rendering Markdown as a block root initially reset it to the surrounding 14px UI size. The transcript block now owns the 15px type size; final production measurements confirm 15px body and 18px h2.
- **P2 found during visual iteration — Short command output:** the terminal renderer's minimum height left unnecessary empty space beneath one-line output. Plain output now uses a compact preformatted block; ANSI output retains the existing terminal renderer. The final command/output card follows its content height.

### Evidence and comparison

All filenames below are under `/Users/oines/.codex/visualizations/2026/10/20/01a15828-c320-7ef1-bdde-4d9bd6f6e731/chat-polish/`. Browser captures are 1280 × 720 pixels at a 1280 × 720 CSS viewport; no image resizing or retouching was used. The reference Storybook has its own documentation chrome and different sample content. Its comparisons concern material, hierarchy and control scale, not a whole-app pixel clone. Memoh's process sample has one command; Astral's ordering test intentionally has three commands separated by commentary.

| Capture | Evidence |
| --- | --- |
| `23-memoh-process-details.png` | Actual Memoh process hierarchy: group, ordered thought/tool items, compact command/output. |
| `24-claude-chat-reference.png` | Actual cloned reference chat surfaces, typography and message treatment. |
| `31-claude-sidebar-reference.png` | Reference's comfortable 36px list density. |
| `35-claude-composer-reference.png` | Reference input in its focused state. |
| `25-astral-texture-first.png`, `28-astral-process-details.png` | First visual iteration; body inheritance and excess short-output height were subsequently repaired. |
| `30-astral-process-final.png` | Repaired ordered process and compact output in the development UI. |
| `32-astral-composer-focus.png` | Three-line focused input: 98px form / 80px textarea. |
| `33-astral-dark-texture.png` | Dark transcript, process, sidebar and composer. |
| `34-astral-narrow-dark.png` | Real file preview with a 488px chat pane, 30px metadata row and no horizontal overflow. |
| `36-packaged-texture-final.png` | Final embedded production interface from the rebuilt desktop package, with thought and command output expanded. |

The final Memoh and packaged Astral screenshots were opened together in the same comparison input. The focused composer source and three-line Astral capture were likewise opened together. The relevant text, surfaces and controls are legible at native resolution, so additional cropped images were unnecessary. Exact dimensions and reading position were checked through the rendered DOM. The final process expansion preserved scrollTop = 0.

### Required fidelity surfaces

- **Fonts and typography:** the existing licensed Inter/system CJK stack remains. Body text is 15px/1.65, h2 is 18px, secondary process labels are 13px, sidebar titles are 13px, and metadata is 12px. The root inheritance issue is repaired. No claim is made that Astral uses Claude's proprietary typefaces.
- **Spacing and layout rhythm:** shared 760px content measure remains; continuous process items share one capsule without an inner scrollbar. Plain command output no longer reserves terminal-height whitespace. Sidebar rows are 36px. Input remains compact and grows naturally. The 488px chat pane keeps its controls available without page overflow.
- **Colors and tokens:** main canvas `#fcfcfb`, sidebar `#f4f4f1`, raised white surfaces, translucent boundaries and restrained hover fills provide the light hierarchy. Dark surfaces use warm charcoal, with readable primary/secondary text. Both themes were visually inspected. Astral's neutral send button and existing accent are intentional differences from the community reference's clay branding.
- **Images and assets:** this pass adds no imagery or brand marks. Existing Lucide controls remain vector-sharp. Ordinary rows lose redundant icons; meaningful state icons remain. No generated or code-drawn substitute assets were introduced.
- **Copy and content:** obsolete diagnostic copy is absent from the transcript. Thinking, stopped, jump-to-latest and More labels have Chinese/English translations. Fixture flags and local model names are test content, not product instructions. The test command output is real local execution, with no external model requests.

### Functional and packaged verification

`pnpm lint`, both web/desktop type checks, all 13 frontend tests and `git diff --check` passed. The new tests cover chronological process/text interleaving, lifecycle deduplication, reused call IDs, failed/opaque reasoning, ordered streaming previews and history results whose lifecycle trace has been paginated away.

The running UI verified process expansion, per-turn copying (346 characters: commentary plus final Markdown, excluding reasoning), the More/fork menu, multiline input, narrow-file preview and both themes. The rebuilt package's embedded production page verified the same ordered disclosure, 50px composer, 15px body and 18px h2. A new local request was sent with Enter and stopped successfully; its transcript shows a plain stopped state and the input returns to one line. Final production console warnings/errors: none.

Both macOS DMG/ZIP packages were rebuilt. Each packaged macOS server binary matches the current distribution byte-for-byte; both DMGs passed `hdiutil verify`, and `Astral-0.1.0-sha256.txt` was refreshed. Desktop service startup, private Owner handoff, graceful stop, parent-crash cleanup and identity-preserving restart passed.

The Mac had been unlocked for the preceding native audit, then was locked again when this package's native window was selected. The user was asked to unlock it while independent checks continued. This pass therefore verifies the actual packaged embedded page and desktop service; it does not claim new native-window clicks. Intel remains build-verified. Mobile viewport testing is not claimed; narrow-layout evidence uses the actual desktop file pane.

No actionable P0/P1/P2 visual finding remains within the requested three surfaces. Native-window re-verification remains limited by the lock state.

final result: passed

## Native chrome, Agent scope and independent menu components — 2026-10-21

This pass incorporates the subsequent requests to remove process cards, integrate the macOS window controls, scope the sidebar to one Agent, improve model/reasoning selection and provider logos, and use an independent alternative to Memoh components. The earlier gray process-capsule styling is superseded.

The independent sources inspected were [shadcn-vue Model Selector](https://shadcn-vue.com/docs/ai-elements/model-selector), [Reka UI Dropdown Menu](https://reka-ui.com/docs/components/dropdown-menu), [AI Elements Vue](https://www.ai-elements-vue.com/), [Lobe Icons](https://github.com/lobehub/lobe-icons), and [SVGL](https://svgl.app/). Astral already uses Reka and static Lobe SVGs. The final implementation composes Reka directly into Astral's own component, with two direct dropdowns following the shadcn-vue model/effort pattern. No Memoh component is imported or copied. SVGL remains an alternative source rather than an added dependency. Memoh's local icon package wraps SVG files; nine sampled brand paths matched the installed Lobe assets exactly, which is evidence of equivalent artwork, not a complete provenance audit.

### Findings and repairs

- **P2 — Process surfaces:** removed the gray group surface and nested command card. Ordered disclosure, thought text, commands and results now use indentation and spacing on the transcript's background.
- **P2 — Window chrome:** macOS uses Electron's hidden title bar with native controls positioned inside the sidebar header. Main content has a real 40px drag region, settings reserve room for the controls, and the sidebar-open button moves clear of them. Interactive controls remain outside drag regions. The web layout does not acquire native window padding.
- **P1 — Agent scope:** the sidebar selector persists the current Agent and filters sessions strictly by its ID. Opening a session synchronizes that selector; starting a session uses the selected Agent. New-session drafts are keyed by Agent so switching does not mix unsent text or selections.
- **P2 — Selector styling:** inspection found that classes passed to the Select wrapper stopped at Reka's nonvisual root. Attributes now reach the actual trigger, giving the Agent selector the intended sidebar typography, transparent surface and hover treatment.
- **P2 — Model navigation:** the initial nested menu was rejected during native validation because mouse movement could close a submenu before selection. The final UI has independent model and reasoning menus, with current values visible in the metadata row. Selection closes the menu; reopening reflects the saved value. Models retain provider grouping and a search field for catalogs with at least 20 enabled entries. Reasoning options come from the selected model's capabilities; its default is labeled with the effective default tier when known.
- **P2 — Provider artwork:** retained independently packaged Lobe vectors and normalized them to 24px within a 40px neutral circular slot, vertically centered in each row. Monochrome brands follow the text color without inverting multicolor artwork. Inspection caught invalid unquoted SVG data URLs in the mask; quoting the URL repaired the initial black squares. Light and dark captures confirm the actual brand silhouettes.

### Visual evidence and fidelity

Captures remain under `/Users/oines/.codex/visualizations/2026/10/20/01a15828-c320-7ef1-bdde-4d9bd6f6e731/chat-polish/`.

- `37-native-titlebar-before.png`: separate native title bar before this change.
- `38-memoh-reasoning-menu.png`, `39-memoh-provider-icons.png`: inspected reference interaction and icon treatment; the final model UI intentionally uses the independent direct-menu pattern instead.
- `40-astral-provider-icons-before.png`: provider rows before normalization.
- `41-provider-icons-refined.png`: initial mask failure, superseded by `44` and `45`.
- `43-agent-isolation.png`: Research's own session list and a newly created Research session.
- `44-provider-icons-final-light.png`, `45-provider-icons-final-dark.png`: repaired brand rendering in both themes.
- `46-native-integrated-titlebar.png`, `47-native-settings-titlebar.png`, `49-native-collapsed-titlebar.png`: native title bar integration, settings navigation and collapsed sidebar. These interim native captures predate the final direct-menu and Agent-trigger build. macOS's screen-sharing indicator obscures the upper-left control area in captured images; it is OS-owned, not Astral UI.
- `48-direct-reasoning-menu.png`: a saved high reasoning tier, selected on reopening the final direct menu.
- `50-direct-model-search.png`: one matching model from a 21-model local test catalog.
- `51-direct-menu-narrow-chat.png`: actual file preview with a 488px chat area; metadata is 30px high and its 440px scrollWidth equals its width.

All five surfaces were reviewed: existing Inter/system CJK typography is retained; process and sidebar spacing follow the current compact rhythm; colors use Astral's light/dark tokens; provider artwork is actual Lobe SVG rather than an approximation; controls preserve translated labels, explicit defaults and capability-dependent choices. The requested unboxed process and direct menus are intentional departures from the Memoh reference. Screenshots are actual running UI and were not retouched.

### Functional checks

The real local server verified two-Agent session isolation, new-session ownership, draft isolation/restoration, direct-link synchronization and persisted Agent selection. Menu checks covered mouse selection, keyboard selection, model filtering, no results, saved reasoning on reopen, and hiding reasoning when switching to a model without that capability. Model changes still require the existing verification gate; the two selection fixtures were verified through actual local tool-call/replay checks. The 18 extra search-only fixtures were disabled after validation. No paid model or external account was used.

The final source passed lint, web/desktop type checks, 13 frontend tests and diff whitespace checks. Native distribution build completed for macOS/Linux on both supported architectures. Desktop service startup, private Owner handoff, graceful stop, parent-crash cleanup and identity-preserving restart passed. The native app verified Agent switching, settings navigation, sidebar collapse and full-screen entry/exit. Final package checks are recorded below.

Final packaging completed after the direct-menu and Select-trigger fixes. Both DMGs passed `hdiutil verify`; macOS arm64/amd64 packaged server binaries match the final distribution byte-for-byte, and the SHA-256 manifest was regenerated. Intel is build-verified; Apple Silicon was also run as a native app against the isolated local profile.

The final native app was reopened, the sidebar restored, and the process conversation opened. Selecting High with the mouse and reopening the direct reasoning menu showed High selected; the default was then restored. The actual expanded thought, command and result are unboxed in `52-native-final-unboxed-chat.png`. This final native capture and `37-native-titlebar-before.png` were inspected together: the separate centered-title strip is gone, while the sidebar heading, content and controls remain clear. The OS sharing indicator is present in both native captures and is not part of the application.

`53-packaged-final-unboxed-chat.png` records the same final embedded production page. Its current reload produced no console warnings or errors. The production preview was retained for the user. No actionable P0/P1/P2 finding remains in this requested scope. Full mobile-device testing is not claimed; narrow layout uses the real desktop file pane.

final result: passed

## Command detail blocks, interaction motion and sidebar cleanup — 2026-10-21

The user supplied a screenshot showing selectable process controls and uncontained command output, then requested overall motion/detail polish and removal of the sidebar's Astral label. This is a scoped continuation of the existing native application.

Reference and current captures (same 1280×720 browser viewport) were inspected together: `54-memoh-command-block-reference.png` shows Memoh's command/result content surface; `58-motion-final-light.png` shows the implementation. Only the inner command surface is adopted as a visual reference. The outer process list stays transparent as requested. `56-motion-command-blocks-dark.png` verifies the dark treatment; `57-motion-file-panel.png` verifies the real narrow chat/file layout. All captures are in the existing chat-polish evidence folder.

Repairs and refinements:

- Process summary, reasoning and tool headings no longer select text on pointer drag. A real drag selected `notes.md` from the result, while dragging the process heading produced an empty selection. Command/result text remains selectable.
- Each expanded tool has one themed content block with a thin border, 8px corners and 10px/12px padding. Command precedes output, with successful exit-zero noise still omitted. The ANSI terminal background uses the same raised surface token.
- A shared Reka Collapsible wrapper animates measured content height and opacity. It returns to natural height after entering, so streaming or expanded nested content can grow normally. Collapsed content is inert. Reasoning uses a native keyboard-operable button with aria-expanded.
- Motion uses shared 120/180/220ms timing: menu/popover/select and dialog entry/exit, sidebar and file panel space changes, disclosure chevrons, switches, button press feedback, new message entry and the return-to-bottom control. Settings content gets a short fade/4px entrance without remounting its cached forms. Streamed text is not repeatedly animated.
- Reduced-motion CSS removes repeated animation and effectively eliminates transitions. Settings' Web Animations path explicitly respects the same OS preference and cancels pending animation when it changes.
- The chat sidebar no longer contains an Astral wordmark. Agent selection and collapse share a compact web heading; macOS reserves its integrated traffic-light/drag area. The settings sidebar footer is version-only as well.
- Keyboard focus returns to the sidebar opener after collapse and moves to Agent after opening. A form dialog focuses its first editable field. Keyboard file opening focuses the file heading and closing restores the source link. Pointer resize cleanup also handles pointer cancellation.

Interaction evidence: real controls reported 180ms surface/collapse animations and 220ms panel transitions. A settings navigation was observed at opacity 0 and translateY(4px), then settled normally. Repeated process toggling left the group open and its children usable. Escape dismissed model/reasoning menus and the rename dialog; switching directly between menus did not leave a trapped overlay. The rename dialog remained centered and its input received focus. File panel focus restoration was confirmed by the active element returning to `Open the validation note`. A three-line composer measured 98px, sent a real local fixture request, and returned to the stopped state on cancellation.

All five fidelity surfaces were checked: typography retains the established 15px body/13px controls/12px code hierarchy; geometry keeps the existing content column and compact process indentation; surface colors adapt to both themes while the outer process background remains transparent; existing icon assets remain intact; and message/process chronology, hierarchy and composer placement are preserved. The removed sidebar branding is intentional.

Validation before packaging: full typecheck and all 13 tests passed; lint warnings were limited to one heading's formatting and were fixed, with targeted lint then clean. These changes are included in the subsequent verified desktop builds described below. OS reduced-motion preference and a full mobile device were not changed/tested; reduced-motion support was checked in source and normal-motion interactions in the running app.

## Model settings, direct model use, failed-send recovery and native controls — 2026-10-21

The user clarified that the concrete provider/model settings needed attention, requested immediate model use without a verification step, and reported three live defects: a failed send made the draft impossible to edit, the sidebar button was displaced or unclickable near native controls, and its hover surface overlapped the inset content's top edge.

The existing implementation was repaired in place. Screenshots are in `/Users/oines/.codex/visualizations/2026/10/20/01a15828-c320-7ef1-bdde-4d9bd6f6e731/model-settings-audit/`. Before/after captures were inspected together for provider detail, the advanced model editor and native chrome.

- Provider detail now uses one consistent label/control grid, with API Key labels aligned to the field rather than its help link. Model search/actions share a 36px height and normal vertical padding. Capability icons occupy fixed columns; switches and edit/delete actions line up across rows. Redundant verification indicators/actions are removed. The user's provider values were not changed.
- The model editor is 560px wide with paired identity/token fields and a full-width description. Advanced fields scroll inside the form while its heading and actions stay visible. At a 1280×720 viewport, the footer remained at y=598.8–651.8 with the form scrolled to 530.5px. An intentionally invalid JSON value exposed a clipped validation message during the audit; the error was moved outside the scroll area and verified visible before saving a valid local fixture. Focus-ring clipping was also corrected. Provider creation uses the same fixed-footer behavior.
- New or edited enabled model configurations can be selected immediately. Tool verification remains an explicit backend diagnostic, never a prerequisite or automatic request. Harness tests now execute without prior verification. A newly created local profile with no verification record completed a real server/model/session request; its verification record remained absent afterward. The test profile was disabled and its test session archived after checking.
- Failed sends no longer lock the draft. Only an active submission makes the text area read-only. Unchanged retries retain their delivery identity; edited content reconciles the previous request before receiving a new identity. An isolated loopback proxy reproduced both an unavailable submission and a response lost after durable acceptance, without stopping the user's server. The draft could be emptied, rewritten and resent. The lost-response retry used the same delivery ID and rendered exactly one message. Captures `20-composer-offline-editable.png` and `21-composer-retry-recovered.png` record these outcomes.
- A single persistent sidebar button keeps its position and focus when toggled. Web coordinates remained x=20/y=20 before and after toggling. Native controls now occupy a separate 48px titlebar, with the sidebar button at x=92/y=7 and a 34px hit area. The drag rectangle starts at x=136, beyond the button's right edge. The content pane begins at y=48 in both sidebar states, so the hover surface cannot meet its rounded top edge. Native screenshot pixels put the red control at x=36–59/y=36–59 in a 2× capture, confirming equal top/left offsets; its center and the sidebar icon center share y=48. Narrow native overlays also begin below the titlebar.

Final production settings captures are `24-provider-detail-production.png`, `25-model-editor-production.png` and `26-model-advanced-production.png`. `27-native-titlebar-final.png` shows the actual updated native application. After the user-control interruption, state was refreshed and the staged update completed while the app was idle. Sidebar activation was then verified again in both directions, and the original empty conversation was restored. `28-native-titlebar-collapsed-final.png` and `29-native-titlebar-expanded-final.png` record the final geometry.

Typography, spacing, surfaces, assets and hierarchy were checked against the existing application: established font sizes and icon assets remain; field alignment and action placement are consistent; themed surface tokens are reused; and provider → model → advanced-field hierarchy is preserved. The separate native toolbar and removal of verification controls are intentional changes. A full mobile-device and OS reduced-motion test are not claimed for this pass.

Validation: lint and web/desktop type checks passed; all 16 frontend tests passed; `go test -race ./internal/modelbridge ./internal/harness ./internal/httpapi` passed. Desktop service startup, private Owner handoff, graceful stop, parent-crash cleanup and identity-preserving restart passed. The refreshed production settings page had no console warnings or errors. Desktop updates are staged in a separate output directory before the short restart, preserving the user's active preview during builds.

## Whole-interface comparison with open-claude-ui — 2026-10-21

The user identified the large blue outline on the Agent selector, then expanded the request to compare the whole interface with the supplied reference. The reference is available locally at `/private/tmp/astral-open-claude-ui`. Its Storybook was run on loopback and all nine sections inspected: foundations, shell, chat, controls, code/artifacts, overlays, motion, borders, and the desktop 2.0 examples. The v1/v2 design documentation, theme tokens and relevant component sources were also reviewed. This is a community reference, not a claim of official Claude design specifications.

Evidence is in the existing `model-settings-audit` folder above. `reference/01-chat.png` through `reference/09-foundations.png` cover all Storybook sections; `reference/10-dark-foundations.png` records its dark surface hierarchy. Astral screenshots `30`–`59` were captured during this audit and opened from their saved files before acceptance.

### Flow and findings

| Step | Surface and health | Evidence and result |
| --- | --- | --- |
| 1 | Chat shell and sidebar — repaired | `31-chat-before.png`, `48-agent-keyboard-focus.png`, `49-agent-menu.png`. The detached 2px blue Agent outline is replaced by a 1px neutral inset outline and hover fill. Tab, Enter and Escape were exercised; focus returns to the selector. Session links and navigation use the same contained focus treatment. |
| 2 | Transcript, process and composer — consistent | `32-process-before.png`, `58-chat-dark-composer.png`. Existing compact typography, ordered process disclosure and 50px single-line composer remain. The composer owns its focus border; its textarea does not create a second halo. Process headings retain clear focus and readable selectable output. |
| 3 | Settings sections and entity forms — repaired | `33-general-before.png` through `47-advanced.png`, `51-simple-preferences.png`, `56-settings-dark.png`. General, Appearance, Models, Search, Computers, Channels, Agents, Storage, Security and About were visited. Simple preferences now present their value and chevron inside the existing row, avoiding a second permanent box. Editable forms retain input borders. Field focus, switches and checkboxes now use theme-aware neutral states. The channel form's switch was toggled by keyboard and restored before cancellation; no settings were saved. |
| 4 | Model/provider dialogs and menus — repaired | `52-model-editor-light.png`, `53-nested-select.png`, `57-model-editor-dark.png`, `59-model-menu-dark.png`. Shared control/panel/dialog radii, light two-layer shadows, dark inner hairlines and a restrained backdrop blur provide consistent depth. Model fields, checked capabilities, fixed actions, advanced scrolling and nested protocol selection remain legible. Escape dismisses menus and returns focus; both editors were cancelled without saving. |
| 5 | File preview and keyboard navigation — repaired | `50-file-heading-before.png`, `54-file-dark-focus.png`, `55-file-resize-focus.png`. A native blue outline remained on the programmatically focused file heading; this is now a neutral rounded outline. The separator uses a single focus line instead of a tall rectangle, and Right Arrow visibly resizes the panel. Monaco inherits Astral's background, text, widget and focus colors while keeping syntax highlighting. |
| 6 | Themes, motion and native delivery — verified to the stated limits | Both themes were inspected for the chat, controls, menus, settings and dialogs. Existing reduced-motion rules remain. The final macOS package verification and native capture are recorded below. |

### Design decisions

- **Typography:** keep the existing Inter/system hierarchy and add explicit system CJK fallbacks. No proprietary reference fonts are copied. Text remains the primary visual hierarchy.
- **Spacing and shape:** preserve the user's compact composer and the established chat column. Use 8px controls, 12px floating panels and 16px dialogs, with fine boundaries rather than nested boxes. Native titlebar geometry is preserved.
- **Color and depth:** warm neutral hover/selected fills replace high-saturation control states. Focus gets its own tokens rather than reusing link accent. Link and status colors remain meaningful; neutral focus does not suppress syntax highlighting. Dark controls invert their foreground/background appropriately.
- **Interaction:** keyboard focus remains visible. Menus use highlighted rows; links use inset outlines or underlines; fields use a fine border with a faint halo; the composer uses one outer border. Forced-colors overrides explicitly retain system Highlight outlines.
- **Assets and content:** retain the existing provider SVGs and control icons. No new decorative artwork, invented product feature, branding replacement or backend workflow was introduced to mimic the reference.

### Accepted examples

Step 1 — Agent keyboard focus and menu:

![Agent keyboard focus after refinement](/Users/oines/.codex/visualizations/2026/10/20/01a15828-c320-7ef1-bdde-4d9bd6f6e731/model-settings-audit/48-agent-keyboard-focus.png)

Step 3 — values presented within their settings row:

![General preferences after refinement](/Users/oines/.codex/visualizations/2026/10/20/01a15828-c320-7ef1-bdde-4d9bd6f6e731/model-settings-audit/51-simple-preferences.png)

Step 4 — model editor in both themes:

![Light model editor](/Users/oines/.codex/visualizations/2026/10/20/01a15828-c320-7ef1-bdde-4d9bd6f6e731/model-settings-audit/52-model-editor-light.png)

![Dark model editor](/Users/oines/.codex/visualizations/2026/10/20/01a15828-c320-7ef1-bdde-4d9bd6f6e731/model-settings-audit/57-model-editor-dark.png)

Step 5 — file preview with neutral keyboard focus:

![File preview focus after refinement](/Users/oines/.codex/visualizations/2026/10/20/01a15828-c320-7ef1-bdde-4d9bd6f6e731/model-settings-audit/54-file-dark-focus.png)

Step 2 — dark composer focused without an inner outline:

![Dark chat and focused composer](/Users/oines/.codex/visualizations/2026/10/20/01a15828-c320-7ef1-bdde-4d9bd6f6e731/model-settings-audit/58-chat-dark-composer.png)

### Verification and limits

The source passes `pnpm lint`, web/desktop type checks, all 16 frontend tests and `git diff --check`. Both macOS architectures were built, both DMGs passed `hdiutil verify`, and packaged server binaries match the distribution byte-for-byte. The native executables are arm64 and x86_64 respectively. Desktop-service startup, private Owner handoff, graceful stop, parent-crash cleanup and identity-preserving restart passed.

The dark visual audit used a read-only loopback preview that substitutes preferences locally. A local Python-file fixture was injected only into that preview's responses to exercise the real file-viewer component. These theme and file values were never saved to the user's backend, and no external model request was made. Screenshots of that fixture demonstrate component appearance and keyboard behavior; they do not test backend file registration or transfer. The supplied reference and the running app were inspected directly, rather than inferring appearance from source alone.

Accessibility evidence covers visible focus, selected states and the described keyboard paths. Full screen-reader behavior, forced-colors mode, OS reduced-motion mode and mobile-device workflows were not runtime-tested in this pass. The native-code-editor Find shortcut did not expose its widget through the browser automation surface, so its styling is source-verified rather than claimed as a completed UI path. Screenshots alone do not establish full accessibility compliance. Intel is build-verified; native interaction is checked on Apple Silicon.

## Settings hover-state correction — 2026-10-21

The user's next screenshot exposed a gap in the preceding audit: the shared settings list had been checked in its resting and keyboard states, but the entire pointer-hover geometry had not been exercised. `60-resource-hover-before.png` reproduces the reported square gray slab on the first Computer row. The fill touched the card's top edge while remaining horizontally inset.

The correction keeps the row's content, hit area and divider in place, and draws its highlight as a separate rounded surface with a 6px outer inset. Buttons and linked rows share the same treatment, covering Computers, Agents, channel rows, configured search providers and linked Agent permissions. Static rows containing separate actions do not become whole-row hover targets. Keyboard focus adds a fine outline to the same inset surface.

Two related state conflicts were repaired: the selected settings-navigation item now keeps its selected fill while hovered or focused, and disabled primary/secondary/ghost buttons and selects no longer pick up enabled hover styling. The previous primary-button hover opacity could override the disabled opacity. Settings tabs also retain their rounded top corners during pointer hover.

| Step | Check and health | Evidence |
| --- | --- | --- |
| 1 | First, last and single resource rows — repaired | `61-resource-hover-fixed.png`, `62-resource-last-hover.png`, `63-resource-single-hover.png`; all leave a consistent inset and rounded highlight. |
| 2 | Keyboard focus on the same row — verified | `64-resource-keyboard-focus.png`; Tab focuses the first row with its own fine neutral outline. Enter opens its existing detail view. |
| 3 | Linked permission rows and Agents — repaired | `65-related-link-hover.png`, `66-agents-hover.png`; both anchor and button rows use the same bounds without accidental text underlines. |
| 4 | Navigation, tabs and disabled actions — verified | `67-settings-tab-hover.png`; the selected navigation fill remained `rgb(228, 227, 222)` under actual pointer hover. The disabled Save button reported `:hover = true`, `disabled = true`, and opacity `0.45` before and after hovering. |

The saved before/after Computer screenshots were inspected together. No form values, permissions, connections or server-side settings were changed during this check. The existing channel list was empty, so its populated-row coverage is based on the shared component styles rather than a newly created connection.

![Corrected resource-row hover](/Users/oines/.codex/visualizations/2026/10/20/01a15828-c320-7ef1-bdde-4d9bd6f6e731/model-settings-audit/61-resource-hover-fixed.png)

![Corrected linked-row hover](/Users/oines/.codex/visualizations/2026/10/20/01a15828-c320-7ef1-bdde-4d9bd6f6e731/model-settings-audit/65-related-link-hover.png)
