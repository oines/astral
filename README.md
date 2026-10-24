# Astral

Astral is a self-hosted workspace for persistent agents. The macOS, Windows and Linux desktop apps and headless server use the same Web interface. Agents run commands and read or write files through explicitly assigned Computers: the server's local machine, a container, or another macOS, Windows or Linux machine.

Astral is pre-release software. Desktop packages and Server/Computer binaries target macOS, Windows and Linux on ARM64 and AMD64. Native acceptance is tracked in [GitHub Actions](https://github.com/oines/astral/actions) and the [verification guide](docs/verification.md); producing a package does not establish that every platform feature has passed interactive acceptance. No signed public installers or automatic Desktop updates have been released yet.

Releases follow one rolling version line, starting at `0.1.0` and increasing the patch number for each release. As in Magpie, the maintainer chooses a version and pushes its tag after review and CI; the [release workflow](docs/releasing.md) then builds, signs, notarizes, verifies and publishes its assets. The [Linux VPS installer](docs/headless-install.md) is prepared for the first verified public release; no ready release is currently available to install. Desktop downloads and the one-command Server installation will use the same release assets. Initial releases support manual updates with unchanged data formats; automatic Desktop updates and cross-format migrations remain unfinished.

The implementation follows [SPEC.md](SPEC.md). The server owns configuration, credentials, sessions, IM accounts and delivery receipts. [Astral Core](https://github.com/oines/astral-core) owns the persistent agent loop. [astral-models](https://github.com/oines/astral-models) translates Core requests into provider protocols.

UI changes must follow the [UI/UX specification](docs/ui-ux-spec.md), including product naming, settings navigation, shared visual language, and automatic saving with feedback. [AGENTS.md](AGENTS.md) links the requirements for future contributors and coding agents. The specification defines the target behavior; historical design reports are not acceptance evidence for the ongoing redesign.

## Build and start

The pinned development tools are in [mise.toml](mise.toml). Install those versions, then:

```sh
pnpm install --frozen-lockfile
GOWORK=off pnpm build:distribution
```

The build generates OpenAPI and the TypeScript SDK, builds the shared Web app, and embeds it in six binaries:

```text
dist/bin/astral-darwin-arm64
dist/bin/astral-darwin-amd64
dist/bin/astral-linux-arm64
dist/bin/astral-linux-amd64
dist/bin/astral-windows-arm64.exe
dist/bin/astral-windows-amd64.exe
dist/bin/checksums.txt
dist/bin/THIRD_PARTY_NOTICES.txt
dist/bin/LICENSE.txt
```

Start the binary for your machine, for example:

```sh
./dist/bin/astral-darwin-arm64 serve --data /tmp/astral-fresh/server --computers-dir /tmp/astral-fresh/computers --distribution-dir dist/bin
```

Open `http://127.0.0.1:4545`. The server prints the path of its private Owner token file; enter that token at the Web sign-in screen. The token is stored in an HttpOnly cookie after sign-in. The desktop app performs this local handoff in its main process.

Use a new, empty data directory for this version. Host schema 3 and Core format 10 replace the previous formats; older data is rejected, with no migration or compatibility mode. Choose a persistent location instead of `/tmp` for regular use.

A fresh installation includes a Bot named **Astral**, which you can rename later in Settings. The two-step setup connects a model service and selects a model, then selects or connects a Computer. **Start using Astral** grants access to the selected online Computer and saves both defaults together. The first conversation is ready to use those resources; there is no separate Bot creation step. Existing installations keep their Bots and resource choices, and setup does not reappear after completion or recreate a renamed or deleted Bot.

After setup, manage services in **Settings → Models** and Bot names, defaults and permissions in **Settings → Bots**. Saved models are immediately usable; no capability probe is required. New sessions inherit the Bot defaults. Provider keys and endpoints are global resources, so Bots do not need separate copies. Independent settings save automatically; connection parameters are submitted together with the local **Save connection** action.

The model adapters support Chat Completions for OpenAI, DeepSeek, Qwen and OpenRouter, and Responses for OpenAI and DeepSeek. Connection and capability failures are reported from actual model requests. Native reasoning options are preserved. Search uses one selected resource per Bot: Tavily, Exa, DeepSeek Search, Brave, Google Custom Search, Sogou/Tencent, Serper, SearXNG, Jina, Bocha, DuckDuckGo or Yandex. The unified `web_search` tool accepts `query` and optional `domains`, `recency` (days), and `limit`; omitted options keep upstream defaults, and Astral does not truncate result counts or search text. See [search setup and provider capabilities](docs/search.md).

## Desktop applications

After building the distribution:

```sh
pnpm --filter @astral/desktop package          # macOS DMG / ZIP
pnpm --filter @astral/desktop package:windows  # current-user Windows installer
pnpm --filter @astral/desktop package:linux    # Linux AppImage / deb
```

Native platform packages are written to `apps/desktop/release/`. Windows installs for the current user without administrator privileges. Linux supports AppImage and Debian packages. The default build is unsigned; signing and notarization require the distributor's Apple credentials and release process.

Electron manages the local Go server, local Daemon, window, menu, tray and login preference. Closing the window follows the General setting; quitting stops the managed service. A server also stops if its desktop parent dies. Renderer code receives neither the Owner token nor a general native filesystem API.

Desktop stores current data under the OS user configuration directory in `astral/schema-3/`: `~/Library/Application Support/astral/schema-3/` on macOS, `%APPDATA%\astral\schema-3\` on Windows, and `$XDG_CONFIG_HOME/astral/schema-3/` (normally `~/.config/astral/schema-3/`) on Linux. Data from earlier formats stays in its original directory. `ASTRAL_DESKTOP_DATA_DIR` can select another directory; it must be empty or already use the current formats.

Enrolling a native Windows Computer uses PowerShell and does not require WSL. Local containers use Docker Desktop in Linux-container mode; on macOS use Docker Desktop or OrbStack, and on Linux use the local Docker Engine. Docker Desktop itself may use WSL 2 internally, but Astral does not ask the user to run enrollment commands there.

## Computers, files and conversations

**Settings → Computers → Add Computer** offers three paths:

- **Use this machine** reuses the server's local Computer.
- **Use a local container** creates a persistent container through the server's local Docker-compatible runtime.
- **Connect another machine** produces an expiring enrollment command. Its container option creates a separate Computer on the machine where the command runs.

Native enrollment installs a visible **Astral Daemon** service for the current user: a macOS LaunchAgent, Windows logon task in Task Scheduler, or Linux `systemctl --user` unit. It never installs a system-wide daemon or requests elevation. The daemon runs with that user’s permissions after login.

New containers use Ubuntu 24.04 with development languages, build/network tools, Python data and document libraries, office/PDF/media/OCR tools, Chinese fonts and Playwright Chromium. **Computer details → Update Daemon** provides a local shell or PowerShell command. Updating restarts the native user service or the same existing container; it retains the Computer identity, Agent files and container-installed software. Existing containers are not rebuilt to adopt a new base image. Running tasks are interrupted by the restart.

Registration does not automatically grant a Bot access to a Computer. The initial setup explicitly grants access when you finish; subsequently, open the Bot's **Computer access** page to grant access and optionally set its default. A session can change its current Computer while idle. Existing process handles and file references remain attached to their original Computer.

AgentHome is shared by sessions of the same Agent on the same Computer. Forking a conversation preserves history and its branch relationship; it does not copy working files or IM bindings.

New sessions use that AgentHome as their default CWD. `switch_computer` can change one session's Computer or CWD, including a directory change on the same Computer. A command's `workdir` or shell `cd` is temporary and does not change the session CWD.

Environment snapshots are ordinary passive context returned by the Host's synchronous `CollectContext` callback. Scans run at creation, activation, complete assistant/tool boundaries and Computer/CWD changes. The Host prompt names the actual `Current Astral environment snapshot:` messages and contains only essential runtime conventions, with no imposed identity or persona. Its output instructions follow the current IM binding: unbound Sessions reply in Web, while bound Sessions receive the matching `send_message` instructions. It does not mention Probe or assume proactive mode is enabled. The optional persona in Bot settings is appended to each new model request; applying it affects every Session of that Bot immediately on its next request, without adding context messages or restarting conversations. Earlier environment snapshots remain in history. When compaction is needed, Core first drafts a summary of committed history, then collects a fresh environment and checks the full candidate request. Summary, environment and selected inputs commit together. A failed collection or budget check leaves the previous context intact and inputs pending; retrying preparation may regenerate the summary.

The initial environment also identifies the actual model, its effective context-window token limit and image-input support (including unknown support). Model-only changes add a short passive notice instead of another Computer environment snapshot; after compaction the fresh snapshot includes the current model.

Discovery runs on the selected Computer: global skills come from its OS user's `~/.agents/skills`, workspace skills from `<cwd>/.agents/skills`, and workspace guidance from exactly `<cwd>/AGENTS.md`. Parent directories are not searched. The snapshot supplies the current `AGENTS.md` content, and the Host prompt tells the model to follow it as the workspace instructions while working in that directory. Changing CWD changes the applicable workspace guidance. The model sees skill names, descriptions, scopes and absolute paths; content hashes stay internal, and body changes identify the files to reread. Environment text retains API target IDs and execution details while omitting boot IDs, repeated discovery identity and successful root diagnostics. Missing, unreadable, offline and unsupported discovery states remain explicit. History and context inspection do not scan or append snapshots.

Uploaded originals and generated files live on Computers. `astral-file://` links stream from the source Computer for preview or download. Images actually supplied to the model are also frozen inside the durable rollout, so history and replay remain valid after an original is removed or its source goes offline. That preserved model input does not substitute for an unavailable original-file download.

Messaging adapters cover **Telegram**, **QQ official Bot**, **QQ NapCat**, **Weixin ClawBot / iLink**, and **Feishu Bot**. Open **Settings → Bots**, choose a Bot, then use **Connect a platform**. QQ offers official and NapCat connection methods; Weixin uses QR authorization. See the [platform setup guide](docs/im-platforms.md) for credentials, permissions and actual protocol limits. Each platform navigation item opens its account list; each account has its own credentials, access settings and chats. New accounts use **Only specified chats** with an empty list; add a chat and choose a new or existing unbound Session. **All chats the account can access** activates private chats on their first message and groups on their first mention/reply. Activation starts at that message without importing earlier history. Scope changes retain bindings and per-chat settings; changing a binding stops both Sessions first.

Bound chats continuously contribute context and compact as needed. Private messages, mentions, replies and Web input use Steer. **Proactive mode** is off by default; when enabled for a group, one LLM Probe decides after 10 seconds of quiet or a 60-second window. It returns only `decide({participate:boolean})`, never a reason or an instruction. Probe follows the Session’s current model unless the Agent selects an override.

A bound Session is fully usable in Web. Assistant text is displayed there; IM messages are sent only by the bound `send_message` tool or an explicit Computer API call. File sends and text sends retain stable targets, source files and persisted receipts. Unknown delivery is inspected before retry. Received IM images are processed in memory into frozen native model payloads; other attachments remain metadata until downloaded. Original bytes are available from the current Computer API, without creating a Computer file during reception.

Start Computer API discovery at `local_api.capabilities_url`. The index links to focused instructions, examples and runtime-generated schemas for each capability, plus the complete OpenAPI. The API exposes owned accounts, authorized chat resolution, filtered message pagination, message-ID lookup, original attachment streaming, send receipts and a catalog of chat-scoped Telegram operations. See [the IM API guide](docs/im.md).

Core `Session` owns execution, queues, cancellation, compaction and model retries. Each input has a caller-supplied stable ID and an indexed durable receipt; resubmitting it returns the same receipt without reloading attachments or starting another run. Astral queues Web/IM input as Steer or FollowUp, then explicitly calls `Continue`. Queuing alone does not start execution. Stop and failure leave pending inputs for explicit continuation. Normal completion without an explicit send leaves IM silent. Passive maintenance uses Core’s ordinary compaction and drains accumulated input batches without starting the main Agent.

Normal generation and summaries use the same retry policy: at most three total attempts, a four-minute timeout per attempt, and cancellable exponential waits starting at one second and capped at five seconds unless a valid longer Retry-After applies. Retries reuse the frozen request. Failed stream fragments and retry errors remain execution records and never become model conversation. The Web UI shows attempt and retry progress; reconnecting only restores the event stream.

## Development and verification

For separate frontend and backend development:

```sh
GOWORK=off pnpm dev:server
pnpm dev
```

The development Web app proxies the API to port 4545. For desktop development, build the native binary first, then run `pnpm dev:desktop`.

Brand artwork lives in `packages/icons/assets/`: the color mark and light/dark wordmarks are shared by the Web app, while the layered monochrome mark is used for the tray and favicon. Run `pnpm build:icons` to regenerate the desktop SVG/PNG/ICNS and favicon from these SVG sources. The app icon uses a white or graphite rounded tile with macOS sizing and padding; the Dock switches between them with the system appearance. Web and desktop development/build commands also regenerate their icons automatically; macOS tray resources include both 1× and 2× versions.

The versions pinned in `go.mod` are publicly available and support standalone builds with `GOWORK=off`; sibling repositories are not required. Use that setting for verification and distribution builds so the result uses the committed dependencies. For coordinated development across Astral, Astral Core and astral-models, an ignored local Go workspace can select those checkouts. Build scripts honor an active workspace unless `GOWORK=off` is set, including when collecting dependency notices.

```sh
export GOWORK=off
go test -race ./...
go vet ./...
pnpm lint:go
pnpm lint
pnpm typecheck
pnpm test
pnpm build:distribution
pnpm test:desktop-service
pnpm test:integration
```

The integration command requires a running local Docker-compatible Linux runtime and the built distributions. It exercises the downloaded installer, real Daemons, container registration, actual shell/file execution and idempotent provisioning, and removes its own containers and volumes.

For repeatable UI checks without an external model account:

```sh
node scripts/dev-provider.mjs
```

Add a Chat Completions Provider with endpoint `http://127.0.0.1:4556`, any test-only API key, and model `astral-fixture`. The fixture supports native image inspection and prompts containing `[file]`, `[slow]`, `[retry]`, and `[silent]`. `[file]` intentionally writes `validation.md` in the selected Computer's AgentHome through the real command tool. Use a disposable Agent and data directory.

See [deployment](docs/deployment.md), [the verification guide](docs/verification.md), and [third-party acknowledgements](THIRD_PARTY.md). Fixture checks, live provider checks, and manual desktop checks are identified separately.

For contributions, see [CONTRIBUTING.md](CONTRIBUTING.md). Report suspected vulnerabilities privately using the channel in [SECURITY.md](SECURITY.md).

## License

Copyright (C) 2026 oines and Astral contributors.

Astral is free software licensed under the [GNU Affero General Public License, version 3 only](LICENSE) (`AGPL-3.0-only`), without any warranty. Source code and development history are available at [oines/astral](https://github.com/oines/astral).

Third-party components retain their own licenses and notices. See [THIRD_PARTY.md](THIRD_PARTY.md) and the `THIRD_PARTY_NOTICES.txt` included with distributions.
